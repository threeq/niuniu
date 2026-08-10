package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// consentGuardRouter wires ConsentGuard with a blocked() that reports the fixed
// `needsConsent` value, simulating an authenticated caller who has / hasn't
// accepted the agreement.
func consentGuardRouter(needsConsent bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ConsentGuard(func(*gin.Context) bool { return needsConsent }))
	h := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/api/projects", h)
	r.POST("/api/projects", h)
	r.POST("/api/consent/accept", h)
	r.GET("/api/consent/status", h)
	r.POST("/api/auth/logout", h)
	r.DELETE("/api/projects", h)
	return r
}

func TestConsentGuardAllowsWhenConsented(t *testing.T) {
	r := consentGuardRouter(false)
	if do(r, "POST", "/api/projects") != http.StatusOK {
		t.Fatal("consented caller must be allowed to write")
	}
}

func TestConsentGuardBlocksWritesWhenNotConsented(t *testing.T) {
	r := consentGuardRouter(true)
	if got := do(r, "POST", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("expected 403 for un-consented write, got %d", got)
	}
	if got := do(r, "DELETE", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("expected 403 for un-consented delete, got %d", got)
	}
	if got := do(r, "GET", "/api/projects"); got != http.StatusOK {
		t.Fatalf("reads must pass even when not consented, got %d", got)
	}
	if got := do(r, "POST", "/api/consent/accept"); got != http.StatusOK {
		t.Fatalf("accept must pass when not consented, got %d", got)
	}
	if got := do(r, "GET", "/api/consent/status"); got != http.StatusOK {
		t.Fatalf("status must pass when not consented, got %d", got)
	}
	if got := do(r, "POST", "/api/auth/logout"); got != http.StatusOK {
		t.Fatalf("logout must pass when not consented, got %d", got)
	}
}

func TestConsentRunGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	blocked := gin.New()
	blocked.Use(ConsentRunGate(func(*gin.Context) bool { return true }))
	blocked.GET("/ws/workspaces/1/terminal", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(blocked, "GET", "/ws/workspaces/1/terminal"); got != http.StatusForbidden {
		t.Fatalf("run gate must block WS handshake when not consented, got %d", got)
	}

	allowed := gin.New()
	allowed.Use(ConsentRunGate(func(*gin.Context) bool { return false }))
	allowed.GET("/ws/workspaces/1/terminal", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(allowed, "GET", "/ws/workspaces/1/terminal"); got != http.StatusOK {
		t.Fatalf("run gate must allow WS handshake when consented, got %d", got)
	}
}

// blockedFromContext is the production wiring shape: derive "needs consent" from
// the resolved auth_user_id, returning false (allow) when no user is resolved.
func TestConsentGuardIgnoresUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ConsentGuard(func(c *gin.Context) bool {
		_, ok := c.Get("auth_user_id")
		if !ok {
			return false // unauthenticated -> let auth middleware handle it
		}
		return true
	}))
	r.POST("/api/projects", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/projects"); got != http.StatusOK {
		t.Fatalf("guard must not block when no user is resolved, got %d", got)
	}
}
