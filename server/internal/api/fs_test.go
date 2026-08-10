package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func TestFSHandler_ListDirs_Personal(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"reports", "data", ".hidden"} {
		if err := os.Mkdir(filepath.Join(base, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewFSHandler(true)
	r := gin.New()
	r.GET("/fs/list-dirs", h.ListDirs)

	req := httptest.NewRequest(http.MethodGet, "/fs/list-dirs?path="+filepath.ToSlash(base), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
		Dirs []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"dirs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Only the two non-hidden directories, sorted, no files.
	if len(resp.Dirs) != 2 {
		t.Fatalf("want 2 dirs, got %d: %+v", len(resp.Dirs), resp.Dirs)
	}
	if resp.Dirs[0].Name != "data" || resp.Dirs[1].Name != "reports" {
		t.Fatalf("unexpected dirs (want sorted data,reports): %+v", resp.Dirs)
	}
}

func TestFSHandler_ListDirs_DisabledInTeam(t *testing.T) {
	h := NewFSHandler(false) // team edition
	r := gin.New()
	r.GET("/fs/list-dirs", h.ListDirs)
	req := httptest.NewRequest(http.MethodGet, "/fs/list-dirs?path="+filepath.ToSlash(t.TempDir()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("team edition should 404, got %d", w.Code)
	}
}

func TestFSHandler_ListDirs_RejectsRelative(t *testing.T) {
	h := NewFSHandler(true)
	r := gin.New()
	r.GET("/fs/list-dirs", h.ListDirs)
	req := httptest.NewRequest(http.MethodGet, "/fs/list-dirs?path=relative/dir", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("relative path should 400, got %d", w.Code)
	}
}
