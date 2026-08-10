package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// consentWriteAllowlist is the set of paths that remain writable even when the
// caller has not yet accepted the current agreement, so they can read/accept
// the agreement and manage their session. Reads (GET/HEAD/OPTIONS) are always
// allowed and not listed here. Login/refresh/mfa-verify live outside the /api
// group and are not subject to this guard.
var consentWriteAllowlist = map[string]bool{
	"/api/consent/accept": true,
	"/api/consent/status": true,
	"/api/auth/logout":    true,
	"/api/auth/me":        true,
}

func consentPathAllowed(path string) bool {
	return consentWriteAllowlist[path]
}

// abortConsent writes a standard error envelope and aborts the request.
func abortConsent(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}

// ConsentGuard blocks non-read requests from a caller who has not accepted the
// current privacy & disclaimer agreement. `blocked` reports, for the request's
// caller, whether consent is missing; it returns false for unauthenticated
// requests (no resolved user) so the auth middleware owns that rejection.
//
// Mirrors LicenseGuard: GET/HEAD/OPTIONS and the allowlist always pass. Applied
// on both the /api group and the /mcp group so the AI execution interface
// (agent-driven MCP tool calls) is gated identically to the UI.
func ConsentGuard(blocked func(c *gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if consentPathAllowed(c.Request.URL.Path) {
			c.Next()
			return
		}
		if blocked(c) {
			abortConsent(c, http.StatusForbidden, "CONSENT_REQUIRED", "请先阅读并同意数据隐私与免责声明")
			return
		}
		c.Next()
	}
}

// ConsentRunGate blocks run-class endpoints (WebSocket terminal / agent /
// account-login handshakes, which are GET upgrades and therefore slip past the
// GET-allowing ConsentGuard) when the caller has not accepted the agreement.
// Apply it as a per-route middleware on run-class routes ONLY — never globally,
// since it would also block read-only streams.
func ConsentRunGate(blocked func(c *gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if blocked(c) {
			abortConsent(c, http.StatusForbidden, "CONSENT_REQUIRED", "请先阅读并同意数据隐私与免责声明")
			return
		}
		c.Next()
	}
}
