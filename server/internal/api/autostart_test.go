package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func TestAutostartHandler_Unsupported(t *testing.T) {
	h := NewAutostartHandler("") // no NIUNIU_PERSONAL_EXE => team/hosted mode
	r := gin.New()
	r.GET("/api/autostart", h.Get)
	r.PUT("/api/autostart", h.Put)

	// GET reports not supported.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/autostart", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	var got autostartStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Supported || got.Enabled {
		t.Fatalf("got %+v, want supported=false enabled=false", got)
	}

	// PUT is a no-op that still reports not supported (does not 500).
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/autostart", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", w.Code)
	}
	got = autostartStatus{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Supported {
		t.Fatalf("PUT got supported=true, want false")
	}
}

func TestAutostartHandler_BadBody(t *testing.T) {
	h := NewAutostartHandler("/tmp/fake/niuniu-desktop")
	r := gin.New()
	r.PUT("/api/autostart", h.Put)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/autostart", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT bad body status = %d, want 400", w.Code)
	}
}
