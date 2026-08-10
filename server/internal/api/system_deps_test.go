package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/service"
)

func newTestRouter(svc *service.SystemDepsService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSystemDepsHandler(svc, false)
	r.POST("/api/system-deps/git-identity", h.SetGitIdentity)
	return r
}

func TestSetGitIdentity_OK(t *testing.T) {
	cfg := t.TempDir() + "/.gitconfig"
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	svc := service.NewSystemDepsService()
	r := newTestRouter(svc)
	body, _ := json.Marshal(map[string]string{"name": "Alice", "email": "alice@example.com"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/system-deps/git-identity", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	name, email, _ := git.GetGlobalIdentity(req.Context())
	if name != "Alice" || email != "alice@example.com" {
		t.Fatalf("identity not persisted: name=%q email=%q", name, email)
	}
}

func TestSetGitIdentity_BadEmail(t *testing.T) {
	cfg := t.TempDir() + "/.gitconfig"
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	svc := service.NewSystemDepsService()
	r := newTestRouter(svc)
	body, _ := json.Marshal(map[string]string{"name": "Alice", "email": "not-an-email"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/system-deps/git-identity", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_GIT_IDENTITY") {
		t.Fatalf("missing error code: %s", w.Body.String())
	}
}

func TestSetGitIdentity_EmptyName(t *testing.T) {
	cfg := t.TempDir() + "/.gitconfig"
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	svc := service.NewSystemDepsService()
	r := newTestRouter(svc)
	body, _ := json.Marshal(map[string]string{"name": "", "email": "a@b.c"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/system-deps/git-identity", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}
