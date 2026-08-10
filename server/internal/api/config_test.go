package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/config"
)

// newConfigTestHandler wires a ConfigHandler over an in-memory cfg with a Save
// hook that counts calls instead of writing config.yaml, so the GET/PUT
// round-trip is exercised in isolation from the user's real config file. The
// returned *int is that save counter.
func newConfigTestHandler(cfg *config.Config) (*ConfigHandler, *int) {
	saves := new(int)
	h := &ConfigHandler{
		Cfg: cfg,
		Save: func(c *config.Config) error {
			*saves++
			return nil
		},
	}
	return h, saves
}

func configRouter(h *ConfigHandler) *gin.Engine {
	r := gin.New()
	r.GET("/api/config", h.Get)
	r.PUT("/api/config", h.Update)
	return r
}

func TestConfigHandler_GetExposesTelemetryDefault(t *testing.T) {
	cfg := &config.Config{}
	cfg.Telemetry.Enabled = true // mirrors the viper default (opt-out)
	h, _ := newConfigTestHandler(cfg)
	r := configRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["telemetry_enabled"] != true {
		t.Fatalf("telemetry_enabled = %v, want true", got["telemetry_enabled"])
	}
}

func TestConfigHandler_RoundTripTelemetryToggle(t *testing.T) {
	cfg := &config.Config{}
	cfg.Telemetry.Enabled = true
	h, saves := newConfigTestHandler(cfg)
	r := configRouter(h)

	// PUT telemetry_enabled=false.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"telemetry_enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var put map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &put); err != nil {
		t.Fatalf("decode PUT: %v", err)
	}
	if put["telemetry_enabled"] != false {
		t.Fatalf("PUT response telemetry_enabled = %v, want false", put["telemetry_enabled"])
	}
	if *saves != 1 {
		t.Fatalf("Save called %d times, want 1", *saves)
	}
	if cfg.Telemetry.Enabled {
		t.Fatalf("cfg.Telemetry.Enabled = true, want false after PUT")
	}

	// GET reflects the persisted toggle.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var get map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &get); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if get["telemetry_enabled"] != false {
		t.Fatalf("GET after PUT telemetry_enabled = %v, want false", get["telemetry_enabled"])
	}
}

// An omitted telemetry_enabled must leave the current value untouched (partial
// update semantics) — a PUT changing only the editor must not flip telemetry.
func TestConfigHandler_PartialUpdateLeavesTelemetry(t *testing.T) {
	cfg := &config.Config{}
	cfg.Telemetry.Enabled = true
	cfg.Editor.VSCodeMode = "local"
	h, _ := newConfigTestHandler(cfg)
	r := configRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"editor":{"vscode_mode":"remote"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !cfg.Telemetry.Enabled {
		t.Fatalf("telemetry flipped to false on an unrelated PUT")
	}
	if cfg.Editor.VSCodeMode != "remote" {
		t.Fatalf("vscode_mode = %q, want remote", cfg.Editor.VSCodeMode)
	}
}

// TestConfigHandler_TogglePushesToReporter guards the #366 -> #365 wire-up: a
// PUT that changes telemetry_enabled must invoke OnTelemetryToggle (which server.New
// wires to the running reporter's SetEnabled) so a live reporter stops/resumes
// without a restart. Persisting the config field alone is not enough.
func TestConfigHandler_TogglePushesToReporter(t *testing.T) {
	cfg := &config.Config{}
	cfg.Telemetry.Enabled = true
	h, _ := newConfigTestHandler(cfg)

	var got *bool
	h.OnTelemetryToggle = func(v bool) { got = &v }
	r := configRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"telemetry_enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got == nil {
		t.Fatal("OnTelemetryToggle was not invoked on a telemetry_enabled change")
	}
	if *got != false {
		t.Fatalf("OnTelemetryToggle received %v, want false", *got)
	}

	// An unrelated PUT (no telemetry_enabled) must NOT invoke the callback.
	got = nil
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"editor":{"vscode_mode":"remote"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if got != nil {
		t.Fatalf("OnTelemetryToggle invoked on an unrelated PUT (got %v)", *got)
	}
}

func TestConfigHandler_RejectsBadVSCodeMode(t *testing.T) {
	cfg := &config.Config{}
	cfg.Telemetry.Enabled = true
	h, saves := newConfigTestHandler(cfg)
	r := configRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"editor":{"vscode_mode":"banana"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", w.Code)
	}
	if *saves != 0 {
		t.Fatalf("Save called %d times on a rejected PUT, want 0", *saves)
	}
}

// fakeSettingsReader is a DB-free serverSettingsReader for the assistant flag test.
type fakeSettingsReader struct{ val int }

func (f fakeSettingsReader) GetInt(_ context.Context, _ string, _ int) int { return f.val }

// GET /api/config exposes assistant_enabled: false when no Settings backend is
// wired (unit/default), true when the admin flag reads > 0.
func TestConfigHandler_AssistantEnabledFlag(t *testing.T) {
	cfg := &config.Config{}
	h, _ := newConfigTestHandler(cfg)
	r := configRouter(h)

	decode := func() map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", w.Code)
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}

	if got := decode(); got["assistant_enabled"] != false {
		t.Fatalf("default assistant_enabled = %v, want false", got["assistant_enabled"])
	}

	h.Settings = fakeSettingsReader{val: 1}
	if got := decode(); got["assistant_enabled"] != true {
		t.Fatalf("enabled assistant_enabled = %v, want true", got["assistant_enabled"])
	}
}
