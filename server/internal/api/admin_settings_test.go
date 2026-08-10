package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// fakeDrainer records DrainAllQueuedOwners calls so the handler test can assert
// that raising the concurrency cap triggers an immediate drain. The drain runs in
// a goroutine, so calls are signalled over a buffered channel.
type fakeDrainer struct {
	called chan struct{}
}

func newFakeDrainer() *fakeDrainer { return &fakeDrainer{called: make(chan struct{}, 4)} }

func (f *fakeDrainer) DrainAllQueuedOwners(_ context.Context) {
	select {
	case f.called <- struct{}{}:
	default:
	}
}

// drained reports whether a drain fired within a short window (true) or not (false).
func (f *fakeDrainer) drained() bool {
	select {
	case <-f.called:
		return true
	case <-time.After(500 * time.Millisecond):
		return false
	}
}

// setupAdminSettingsDB opens an in-memory SQLite DB with the schema applied.
// Inlined here to avoid an api → testing → server → api import cycle when
// using niutest helpers.
func setupAdminSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db
}

func newAdminSettingsRouter(t *testing.T) (*gin.Engine, *fakeDrainer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupAdminSettingsDB(t)
	svc := service.NewServerSettingsService(store.Wrap(db))
	drainer := newFakeDrainer()
	h := api.NewAdminSettingsHandler(svc, drainer)
	r := gin.New()
	r.GET("/admin/settings/:key", h.GetSetting)
	r.PUT("/admin/settings/:key", h.PutSetting)
	return r, drainer
}

func TestAdminSettings_OrchestrationKeys(t *testing.T) {
	r, _ := newAdminSettingsRouter(t)

	// PUT a valid budget, GET it back.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/admin/settings/orchestration.chain_cost_budget_usd",
		bytes.NewBufferString(`{"value":"45"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT budget got %d body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/admin/settings/orchestration.chain_cost_budget_usd", nil)
	r.ServeHTTP(w, req)
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["value"] != "45" {
		t.Fatalf("budget = %q, want 45", got["value"])
	}

	// Budget rejects negatives / non-integers.
	for _, bad := range []string{"-1", "abc", "", "4.5"} {
		w = httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"value": bad})
		req = httptest.NewRequest("PUT", "/admin/settings/orchestration.chain_cost_budget_usd", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("budget=%q expected 400, got %d", bad, w.Code)
		}
	}

	// Warn ratio rejects >100.
	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/admin/settings/orchestration.chain_cost_warn_ratio",
		bytes.NewBufferString(`{"value":"101"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("warn 101 expected 400, got %d", w.Code)
	}

	// Default before any write returns the whitelist default (5 for concurrency).
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/admin/settings/orchestration.max_concurrent_workspaces", nil)
	r.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["value"] != "5" {
		t.Fatalf("concurrency default = %q, want 5", got["value"])
	}
}

// TestAdminSettings_ConcurrencyChangeTriggersDrain asserts that updating the
// concurrency cap fires a global queue drain (so freed capacity is used at once),
// while updating an unrelated key does not.
func TestAdminSettings_ConcurrencyChangeTriggersDrain(t *testing.T) {
	r, drainer := newAdminSettingsRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/admin/settings/orchestration.max_concurrent_workspaces",
		bytes.NewBufferString(`{"value":"0"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT concurrency got %d body=%s", w.Code, w.Body.String())
	}
	if !drainer.drained() {
		t.Fatal("raising/removing the concurrency cap should trigger a queue drain")
	}

	// An unrelated key must NOT trigger a drain.
	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/admin/settings/orchestration.max_batch_issues",
		bytes.NewBufferString(`{"value":"30"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT batch got %d body=%s", w.Code, w.Body.String())
	}
	if drainer.drained() {
		t.Fatal("changing an unrelated setting must not trigger a queue drain")
	}
}

func TestAdminSettings_UnknownKey(t *testing.T) {
	r, _ := newAdminSettingsRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/settings/unknown.key", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET unknown got %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/admin/settings/unknown.key",
		bytes.NewBufferString(`{"value":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("PUT unknown got %d, want 404", w.Code)
	}
}
