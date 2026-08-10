package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/license"
)

// licenseWriteAllowlist is the set of paths that remain writable even when the
// license is locked, so users can still log out, secure their account, and
// install a new license. Reads (GET/HEAD/OPTIONS) are always allowed and are
// not listed here. Login/refresh/mfa-verify live outside the /api group and
// are not subject to this guard.
//
// Self-service MFA writes are listed here explicitly. The admin MFA reset
// (POST /api/auth/mfa/reset/:id) is intentionally NOT listed: it restructures
// another user's account and must be blocked during lockout, just like other
// state-changing system writes (e.g. PUT /api/config).
// Consent accept/status are listed too: recording acceptance of the privacy &
// disclaimer agreement is a legal/compliance action orthogonal to commercial
// licensing. A freshly upgraded (or expired) team server is read-only and
// unlicensed, but the consent gate still appears after login; if accept were
// blocked here the user would be permanently stuck — the consent overlay can
// never be dismissed, locking them out before an admin even installs a license.
var licenseWriteAllowlist = map[string]bool{
	"/api/auth/logout":                      true,
	"/api/auth/me":                          true,
	"/api/auth/password/change":             true,
	"/api/admin/license":                    true,
	"/api/license/status":                   true,
	"/api/consent/accept":                   true,
	"/api/consent/status":                   true,
	"/api/auth/mfa/setup":                   true,
	"/api/auth/mfa/enable":                  true,
	"/api/auth/mfa/disable":                 true,
	"/api/auth/mfa/backup-codes/regenerate": true,
}

func licensePathAllowed(path string) bool {
	return licenseWriteAllowlist[path]
}

// abortLicense writes a standard error envelope and aborts the request.
func abortLicense(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}

// LicenseGuard blocks non-read requests when readOnly() is true. readOnly is a
// closure over LicenseService.ReadOnly() so the middleware stays
// dependency-light and easy to test.
func LicenseGuard(readOnly func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !readOnly() {
			c.Next()
			return
		}
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if licensePathAllowed(c.Request.URL.Path) {
			c.Next()
			return
		}
		abortLicense(c, http.StatusForbidden, "LICENSE_EXPIRED", "授权已过期,当前为只读模式")
	}
}

// LicenseRunGate blocks run-class endpoints (e.g. WebSocket terminal / agent /
// account-login handshakes, which are GET upgrades and therefore slip past the
// GET-allowing LicenseGuard) when the license is locked. Apply it as a
// per-route middleware on run-class routes ONLY — never globally, since it
// would also block read-only streams. `locked` is a closure over
// LicenseService.ReadOnly().
func LicenseRunGate(locked func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if locked() {
			abortLicense(c, http.StatusForbidden, "LICENSE_EXPIRED", "授权已过期,当前为只读模式")
			return
		}
		c.Next()
	}
}

// LicenseSeatGate blocks creation of a new user account when the licensed seat
// count is already reached. `check` is a closure over
// LicenseService.CheckSeatAvailable; a non-nil error means no seat is
// available. Apply it as a per-route middleware on user-creation routes only.
func LicenseSeatGate(check func(*gin.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := check(c); err != nil {
			if errors.Is(err, license.ErrSeatExceeded) {
				abortLicense(c, http.StatusForbidden, "LICENSE_SEAT_EXCEEDED", "已达授权席位上限,无法创建新用户")
			} else {
				abortLicense(c, http.StatusInternalServerError, "INTERNAL_ERROR", "seat check failed")
			}
			return
		}
		c.Next()
	}
}
