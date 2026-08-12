package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPluginInstallGlobalScopeRequiresAdminInTeamMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewPluginInstallHandler(nil, nil, nil, nil, true)
	router := gin.New()
	router.POST("/plugins/install", func(c *gin.Context) {
		c.Set("auth_role", "member")
		h.Install(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/plugins/install", strings.NewReader(`{
		"scope": "global",
		"source": "superpowers@claude-plugins-official"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

func TestPluginMarketplaceGlobalScopeRequiresAdminBeforeManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewPluginInstallHandler(nil, nil, nil, nil, true)
	router := gin.New()
	router.POST("/plugins/marketplaces", func(c *gin.Context) {
		c.Set("auth_role", "member")
		h.AddMarketplace(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/plugins/marketplaces", strings.NewReader(`{
		"scope": "global",
		"source": "threeq/niuniu-skills"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}
