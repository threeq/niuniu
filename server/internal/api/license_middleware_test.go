package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/license"
)

func guardRouter(readOnly bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseGuard(func() bool { return readOnly }))
	h := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/api/projects", h)
	r.POST("/api/projects", h)
	r.POST("/api/admin/license", h)
	r.POST("/api/auth/logout", h)
	r.POST("/api/auth/password/change", h)
	r.POST("/api/auth/mfa/setup", h)
	r.POST("/api/auth/mfa/reset/:id", h)
	r.POST("/api/consent/accept", h)
	r.PATCH("/api/projects", h)
	r.DELETE("/api/projects", h)
	return r
}

func do(r *gin.Engine, method, path string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w.Code
}

func TestGuardAllowsWhenActive(t *testing.T) {
	r := guardRouter(false)
	if do(r, "POST", "/api/projects") != http.StatusOK {
		t.Fatal("active license must allow writes")
	}
}

func TestRunGateBlocksWhenLocked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseRunGate(func() bool { return true }))
	r.GET("/ws/workspaces/1/terminal", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "GET", "/ws/workspaces/1/terminal"); got != http.StatusForbidden {
		t.Fatalf("run gate must block WS handshake when locked, got %d", got)
	}
}

func TestRunGateAllowsWhenActive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseRunGate(func() bool { return false }))
	r.GET("/ws/workspaces/1/terminal", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "GET", "/ws/workspaces/1/terminal"); got != http.StatusOK {
		t.Fatalf("run gate must allow WS handshake when active, got %d", got)
	}
}

func TestGuardBlocksWritesWhenReadOnly(t *testing.T) {
	r := guardRouter(true)
	if got := do(r, "POST", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", got)
	}
	if got := do(r, "GET", "/api/projects"); got != http.StatusOK {
		t.Fatalf("reads must pass, got %d", got)
	}
	if got := do(r, "POST", "/api/admin/license"); got != http.StatusOK {
		t.Fatalf("license upload must pass, got %d", got)
	}
	if got := do(r, "POST", "/api/auth/logout"); got != http.StatusOK {
		t.Fatalf("logout must pass, got %d", got)
	}
	if got := do(r, "POST", "/api/auth/password/change"); got != http.StatusOK {
		t.Fatalf("password change must pass when read-only, got %d", got)
	}
	if got := do(r, "POST", "/api/auth/mfa/setup"); got != http.StatusOK {
		t.Fatalf("self-service MFA setup must pass when read-only, got %d", got)
	}
	if got := do(r, "POST", "/api/consent/accept"); got != http.StatusOK {
		t.Fatalf("consent accept must pass when read-only (else fresh team upgrade locks users out of the consent gate), got %d", got)
	}
	if got := do(r, "POST", "/api/auth/mfa/reset/42"); got != http.StatusForbidden {
		t.Fatalf("admin MFA reset must be blocked when read-only, got %d", got)
	}
	if got := do(r, "PATCH", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("PATCH must be blocked when read-only, got %d", got)
	}
	if got := do(r, "DELETE", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("DELETE must be blocked when read-only, got %d", got)
	}
}

func TestSeatGateBlocksWhenFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseSeatGate(func(*gin.Context) error { return license.ErrSeatExceeded }))
	r.POST("/api/auth/users", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/auth/users"); got != http.StatusForbidden {
		t.Fatalf("seat gate must block create when full, got %d", got)
	}
}

func TestSeatGateInternalErrorOnOtherError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseSeatGate(func(*gin.Context) error { return errors.New("db connection lost") }))
	r.POST("/api/auth/users", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/auth/users"); got != http.StatusInternalServerError {
		t.Fatalf("seat gate must return 500 on non-seat errors, got %d", got)
	}
}

func TestSeatGateAllowsWhenAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseSeatGate(func(*gin.Context) error { return nil }))
	r.POST("/api/auth/users", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/auth/users"); got != http.StatusOK {
		t.Fatalf("seat gate must allow create when seats available, got %d", got)
	}
}

func TestFeatureGateBlocksWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseFeatureGate(func(f string) bool { return false }, license.FeatureOrg))
	r.POST("/api/orgs", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/orgs"); got != http.StatusForbidden {
		t.Fatalf("feature gate must block route when feature disabled, got %d", got)
	}
}

func TestFeatureGateAllowsWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LicenseFeatureGate(func(f string) bool { return true }, license.FeatureOrg))
	r.POST("/api/orgs", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/orgs"); got != http.StatusOK {
		t.Fatalf("feature gate must allow route when feature enabled, got %d", got)
	}
}

func TestNopGateFeatureEnabled(t *testing.T) {
	n := license.NopGate{}
	if n.FeatureEnabled(license.FeatureOrg) {
		t.Fatal("open-source NopGate must disable multi-tenant org")
	}
	if !n.FeatureEnabled("some_future_feature") {
		t.Fatal("NopGate must enable non-gated features (Tier 2/3 reserved) by default")
	}
}
