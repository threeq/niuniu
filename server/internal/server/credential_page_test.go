package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// TestServeCredentialPage_HardenedShell guards the credential submission page
// (issue #727): the SPA shell must be served for /credential/:token WITHOUT
// falling through the generic SPA handler, because only this route attaches the
// hardened headers (no-store / no-referrer / strict CSP) to the document. The
// CSP must forbid every external origin — the page collects secrets.
func TestServeCredentialPage_HardenedShell(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frontend := fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><html lang="zh-CN"><body>niuniu spa</body></html>`)},
	}
	s := &Server{engine: gin.New(), frontendFS: frontend}
	s.engine.GET("/credential/:token", s.serveCredentialPage)

	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/credential/some-token", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 SPA shell, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "niuniu spa") {
		t.Fatalf("body is not the SPA shell: %q", w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if rp := w.Header().Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", rp)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	}
	if strings.Contains(csp, "http://") || strings.Contains(csp, "https://") {
		t.Errorf("CSP = %q must not allow any external origin", csp)
	}
}
