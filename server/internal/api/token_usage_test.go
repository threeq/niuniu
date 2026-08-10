package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

func newTokenUsageTestHandler(t *testing.T) (*TokenUsageHandler, int64) {
	t.Helper()
	store.Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "tu-ws", Path: "/tmp/tu-ws", Status: "created", OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("create ws: %v", err)
	}
	_ = q.UpsertWorkspaceTokenHourly(context.Background(), store.UpsertWorkspaceTokenHourlyParams{
		WorkspaceID: ws.ID, BucketHour: time.Now().UTC().Truncate(time.Hour), InputTokens: 42,
	})
	return &TokenUsageHandler{Svc: service.NewTokenUsageService(q), DB: db}, ws.ID
}

func TestWorkspaceUsageSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, wsID := newTokenUsageTestHandler(t)
	r := gin.New()
	r.GET("/workspaces/:id/token-usage", h.WorkspaceUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/token-usage", nil)
	_ = wsID
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Buckets []service.TokenBucket `json:"buckets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].InputTokens != 42 {
		t.Fatalf("buckets wrong: %+v", resp.Buckets)
	}
}

func TestWorkspaceUsageInvalidRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTokenUsageTestHandler(t)
	r := gin.New()
	r.GET("/workspaces/:id/token-usage", h.WorkspaceUsage)

	rec := httptest.NewRecorder()
	// from after to -> 400
	req := httptest.NewRequest(http.MethodGet,
		"/workspaces/1/token-usage?from=2026-06-02T00:00:00Z&to=2026-06-01T00:00:00Z", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOwnerUsageMissingOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTokenUsageTestHandler(t)
	r := gin.New()
	r.GET("/token-usage", h.OwnerUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/token-usage", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
