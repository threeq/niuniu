package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// TestSPAFileHandler_DrawioNonIndexEntry locks in the draw.io offline-embed
// serving contract. The bug this guards against: the draw.io iframe pointed at
// "/drawio/index.html", but http.FileServer 301-redirects any "*/index.html"
// request to "./", which stranded the iframe on the SPA's "Not Found" route in
// the embedded/desktop server (Vite dev hid it by serving index.html directly).
// The fix serves a non-index "drawio.html" entry.
func TestSPAFileHandler_DrawioNonIndexEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frontend := fstest.MapFS{
		"index.html":           {Data: []byte(`<!doctype html><html lang="zh-CN"><body>niuniu spa</body></html>`)},
		"drawio/index.html":    {Data: []byte(`<!doctype html><html><body>draw.io editor App.main mxClient</body></html>`)},
		"drawio/drawio.html":   {Data: []byte(`<!doctype html><html><body>draw.io editor App.main mxClient</body></html>`)},
		"drawio/js/app.min.js": {Data: []byte(`// drawio bundle`)},
	}
	r := gin.New()
	r.NoRoute(spaFileHandler(frontend))

	serve := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}

	// The fix: the non-index entry is served as the draw.io document, 200, no redirect.
	if w := serve("/drawio/drawio.html?embed=1&proto=json"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "draw.io editor") {
		t.Fatalf("/drawio/drawio.html: want 200 draw.io doc, got %d body=%q", w.Code, w.Body.String())
	}

	// A deep vendored asset is served verbatim (not the SPA fallback).
	if w := serve("/drawio/js/app.min.js"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "drawio bundle") {
		t.Fatalf("/drawio/js/app.min.js: want 200 asset, got %d body=%q", w.Code, w.Body.String())
	}

	// The trap: the index.html entry 301-redirects (stdlib http.FileServer rule) —
	// this is exactly why the iframe must NOT use index.html.
	if w := serve("/drawio/index.html?embed=1"); w.Code != http.StatusMovedPermanently {
		t.Fatalf("/drawio/index.html: expected 301 redirect (FileServer index rule), got %d", w.Code)
	}

	// Unknown client-side route still falls back to the SPA shell.
	if w := serve("/workspaces/123"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "niuniu spa") {
		t.Fatalf("/workspaces/123: want SPA fallback, got %d body=%q", w.Code, w.Body.String())
	}
}
