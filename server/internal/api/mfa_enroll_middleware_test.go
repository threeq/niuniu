package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// mfaEnrollGuardRouter wires MFAEnrollGuard with a blocked() that reports the
// fixed `needsSetup` value, simulating an authenticated caller who has / hasn't
// finished mandatory two-factor enrollment.
func mfaEnrollGuardRouter(needsSetup bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MFAEnrollGuard(func(*gin.Context) bool { return needsSetup }))
	h := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/api/projects", h)
	r.POST("/api/projects", h)
	r.DELETE("/api/projects", h)
	r.POST("/api/auth/mfa/setup", h)
	r.POST("/api/auth/mfa/enable", h)
	r.POST("/api/auth/mfa/disable", h)
	r.GET("/api/auth/mfa/policy", h)
	r.POST("/api/auth/logout", h)
	r.POST("/api/consent/accept", h)
	return r
}

func TestMFAEnrollGuardAllowsWhenEnrolled(t *testing.T) {
	r := mfaEnrollGuardRouter(false)
	if got := do(r, "POST", "/api/projects"); got != http.StatusOK {
		t.Fatalf("enrolled caller must be allowed to write, got %d", got)
	}
	if got := do(r, "POST", "/api/auth/mfa/disable"); got != http.StatusOK {
		t.Fatalf("disable must pass once the caller is not blocked, got %d", got)
	}
}

func TestMFAEnrollGuardBlocksWritesWhenSetupOwed(t *testing.T) {
	r := mfaEnrollGuardRouter(true)
	if got := do(r, "POST", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("expected 403 for un-enrolled write, got %d", got)
	}
	if got := do(r, "DELETE", "/api/projects"); got != http.StatusForbidden {
		t.Fatalf("expected 403 for un-enrolled delete, got %d", got)
	}
	if got := do(r, "GET", "/api/projects"); got != http.StatusOK {
		t.Fatalf("reads must pass even when enrollment is owed, got %d", got)
	}
}

// The enrollment flow itself must stay reachable, or the gate would be a
// permanent lockout rather than a guided onboarding step.
func TestMFAEnrollGuardAllowsEnrollmentFlow(t *testing.T) {
	r := mfaEnrollGuardRouter(true)
	for _, p := range []string{
		"/api/auth/mfa/setup",
		"/api/auth/mfa/enable",
		"/api/auth/logout",
	} {
		if got := do(r, "POST", p); got != http.StatusOK {
			t.Fatalf("%s must pass while enrollment is owed, got %d", p, got)
		}
	}
	if got := do(r, "GET", "/api/auth/mfa/policy"); got != http.StatusOK {
		t.Fatalf("policy read must pass while enrollment is owed, got %d", got)
	}
}

// Self-service disable must NOT be an escape hatch: a member under a mandatory
// policy could otherwise enable then immediately disable to clear the gate.
func TestMFAEnrollGuardBlocksDisableWhenSetupOwed(t *testing.T) {
	r := mfaEnrollGuardRouter(true)
	if got := do(r, "POST", "/api/auth/mfa/disable"); got != http.StatusForbidden {
		t.Fatalf("disable must be blocked while enrollment is owed, got %d", got)
	}
}

// Consent and MFA are two independent blocking gates on the same group. Each
// must allowlist the other's completion call, or a member owing both would
// deadlock with no way out.
func TestMFAEnrollAndConsentGatesDoNotDeadlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ConsentGuard(func(*gin.Context) bool { return true }))
	r.Use(MFAEnrollGuard(func(*gin.Context) bool { return true }))
	h := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.POST("/api/consent/accept", h)
	r.POST("/api/auth/mfa/setup", h)
	r.POST("/api/auth/mfa/enable", h)

	for _, p := range []string{
		"/api/consent/accept",
		"/api/auth/mfa/setup",
		"/api/auth/mfa/enable",
	} {
		if got := do(r, "POST", p); got != http.StatusOK {
			t.Fatalf("%s must pass through BOTH gates, got %d", p, got)
		}
	}
}

func TestMFAEnrollRunGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	blocked := gin.New()
	blocked.Use(MFAEnrollRunGate(func(*gin.Context) bool { return true }))
	blocked.GET("/ws/workspaces/1/terminal", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(blocked, "GET", "/ws/workspaces/1/terminal"); got != http.StatusForbidden {
		t.Fatalf("run gate must block WS handshake while enrollment is owed, got %d", got)
	}

	allowed := gin.New()
	allowed.Use(MFAEnrollRunGate(func(*gin.Context) bool { return false }))
	allowed.GET("/ws/workspaces/1/terminal", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(allowed, "GET", "/ws/workspaces/1/terminal"); got != http.StatusOK {
		t.Fatalf("run gate must allow WS handshake once enrolled, got %d", got)
	}
}

// Production wiring shape: derive the blocked flag from the resolved
// auth_user_id, returning false (allow) when no user is resolved so the auth
// middleware owns unauthenticated rejection.
func TestMFAEnrollGuardIgnoresUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MFAEnrollGuard(func(c *gin.Context) bool {
		_, ok := c.Get("auth_user_id")
		if !ok {
			return false
		}
		return true
	}))
	r.POST("/api/projects", func(c *gin.Context) { c.Status(http.StatusOK) })
	if got := do(r, "POST", "/api/projects"); got != http.StatusOK {
		t.Fatalf("guard must not block when no user is resolved, got %d", got)
	}
}

// The error code is a contract with the SPA: api.ts maps it to the enrollment
// gate, so a rename here silently breaks the UI.
func TestMFAEnrollGuardErrorCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MFAEnrollGuard(func(*gin.Context) bool { return true }))
	r.POST("/api/projects", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects", nil))
	if body := w.Body.String(); !strings.Contains(body, "MFA_SETUP_REQUIRED") {
		t.Fatalf("expected MFA_SETUP_REQUIRED in body, got %s", body)
	}
}
