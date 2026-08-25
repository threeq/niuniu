package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// mfaEnrollWriteAllowlist is the set of paths that stay writable for a member
// who is required to enable two-factor authentication but has not done so yet.
// It is deliberately minimal: exactly the calls needed to *complete enrollment*
// (or to back out of the session), and nothing else.
//
// Reads (GET/HEAD/OPTIONS) are always allowed and are not listed here.
// Login/refresh/mfa-verify live outside the /api group and are not subject to
// this guard.
//
// /api/auth/mfa/disable is intentionally absent: allowing it would let a member
// under a mandatory-enrollment policy turn the requirement straight back off.
// Disabling is only reachable once the policy no longer applies to them.
//
// The consent paths ARE listed. Both gates are mounted on the same group and a
// brand-new member typically trips both; if this guard blocked consent/accept
// while ConsentGuard blocked the MFA-setup write, the two overlays would
// deadlock and lock the user out permanently. Listing consent here breaks the
// cycle in one direction (see consentWriteAllowlist for the other).
var mfaEnrollWriteAllowlist = map[string]bool{
	"/api/auth/mfa/setup":  true,
	"/api/auth/mfa/enable": true,
	"/api/auth/mfa/policy": true,
	"/api/auth/mfa/status": true,
	"/api/auth/logout":     true,
	"/api/auth/me":         true,
	"/api/consent/accept":  true,
	"/api/consent/status":  true,
}

func mfaEnrollPathAllowed(path string) bool {
	return mfaEnrollWriteAllowlist[path]
}

// abortMFAEnroll writes a standard error envelope and aborts the request.
func abortMFAEnroll(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, ErrorResponse{Error: ErrorDetail{
		Code:    "MFA_SETUP_REQUIRED",
		Message: "团队安全策略要求开启两步验证，请先完成开启后再使用",
	}})
}

// MFAEnrollGuard blocks non-read requests from a caller whose role mandates TOTP
// two-factor authentication when they have not enabled it yet. `blocked`
// reports that condition for the request's caller; it returns false for
// unauthenticated requests (no resolved user) so the auth middleware owns that
// rejection.
//
// Mirrors ConsentGuard: GET/HEAD/OPTIONS and the allowlist always pass. Applied
// on both the /api group and the /mcp group so the AI execution interface
// (agent-driven MCP tool calls) is gated identically to the UI — a member who
// never opens the enrollment page still cannot get work done through an agent.
func MFAEnrollGuard(blocked func(c *gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if mfaEnrollPathAllowed(c.Request.URL.Path) {
			c.Next()
			return
		}
		if blocked(c) {
			abortMFAEnroll(c)
			return
		}
		c.Next()
	}
}

// MFAEnrollRunGate blocks run-class endpoints (WebSocket terminal / agent /
// local-runner handshakes, which are GET upgrades and therefore slip past the
// GET-allowing MFAEnrollGuard) for a caller who still owes enrollment. Apply it
// as a per-route middleware on run-class routes ONLY — never globally, since it
// would also block the read-only streams (/ws/sse, /ws/notify) that the SPA
// needs in order to render the enrollment page itself.
func MFAEnrollRunGate(blocked func(c *gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if blocked(c) {
			abortMFAEnroll(c)
			return
		}
		c.Next()
	}
}
