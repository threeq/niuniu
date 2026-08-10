package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
)

const changelogHTML = `<ol><li>
<h2><a href="https://github.com/threeq/niuniu-public/releases/tag/v0.7.0">v0.7.0</a></h2>
<div class="mt-1 text-sm">2026-06-28</div>
<a href="https://github.com/threeq/niuniu-public/releases/download/v0.7.0/niuniu-desktop-v0.7.0-windows-amd64.exe"><span>Windows</span></a>
</li></ol>`

func newAppUpdateRouter(t *testing.T, upstream string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := api.NewAppUpdateHandler(upstream)
	r := gin.New()
	r.GET("/app-update/latest", h.Latest)
	return r
}

func TestAppUpdate_LatestProxiesChangelog(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(changelogHTML))
	}))
	defer srv.Close()

	r := newAppUpdateRouter(t, srv.URL)

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/app-update/latest", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d body=%s", w.Code, w.Body.String())
		}
		var out struct {
			TagName string `json:"tag_name"`
			HTMLURL string `json:"html_url"`
			Assets  []struct {
				Name string `json:"name"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.TagName != "v0.7.0" {
			t.Errorf("tag = %q, want v0.7.0", out.TagName)
		}
		if len(out.Assets) != 1 || out.Assets[0].Name != "niuniu-desktop-v0.7.0-windows-amd64.exe" {
			t.Errorf("assets = %+v", out.Assets)
		}
	}
	// Second request must be served from cache (within the 30m TTL).
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hit %d times, want 1 (cache miss)", got)
	}
}

func TestAppUpdate_UpstreamErrorIs502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer srv.Close()

	r := newAppUpdateRouter(t, srv.URL)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/app-update/latest", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502; body=%s", w.Code, w.Body.String())
	}
}
