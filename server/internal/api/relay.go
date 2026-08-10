package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/relayclient"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// RelayHandler wraps service.RelayService for HTTP.  All routes live under
// /api/relay/* and are available to any authenticated caller — browser or
// Wails webview — because the relay business lives in this process
// regardless of shell.
//
// Per-user scoping: scopeFor derives a scope from the auth middleware
// context (auth_user_id) so a multi-user niuniu-server deployment gives
// every caller their own relay account, tunnel, pairing slot, and trusted-
// mobiles list.  When auth is disabled (niuniu-desktop embedded mode) the
// scope falls back to service.DefaultScope and the behavior matches the old
// single-user flow.
type RelayHandler struct {
	svc *service.RelayService

	// Throttle for verify-email-request calls. Spammy clicks shouldn't flood
	// the relay (which, at time of writing, doesn't rate-limit this route).
	// One request per verifyEmailInterval per scope. Guarded by a mutex so
	// the load-compare-store sequence is atomic — two simultaneous requests
	// cannot both pass the gate.
	verifyEmailMu   sync.Mutex
	verifyEmailLast map[string]time.Time
	// verifyEmailInterval is set to verifyEmailMinInterval by NewRelayHandler
	// and only ever reassigned directly by tests — there's no lock around
	// reassignment, so tests must mutate this before the router starts
	// serving requests.
	verifyEmailInterval time.Duration
}

// verifyEmailMinInterval is the minimum spacing between verify-email-request
// calls for the same scope. Short enough that a legitimate user re-opening
// the dialog isn't surprised, long enough that a stuck click loop can't
// exhaust the relay's mail budget.
const verifyEmailMinInterval = 30 * time.Second

// verifyEmailGCThreshold is the map size above which stale entries (older
// than 2× the throttle interval — no longer relevant for rate-limiting) are
// swept on the next request. Keeps the per-process footprint bounded in
// multi-tenant deployments while staying O(1) amortized.
const verifyEmailGCThreshold = 256

func NewRelayHandler(svc *service.RelayService) *RelayHandler {
	return &RelayHandler{
		svc:                 svc,
		verifyEmailLast:     make(map[string]time.Time),
		verifyEmailInterval: verifyEmailMinInterval,
	}
}

// scopeFor extracts the caller's scope from the gin context.  Prefers the
// auth_user_id set by auth.Middleware; falls back to service.DefaultScope
// (personal / auth-disabled deployments).
func scopeFor(c *gin.Context) string {
	if v, ok := c.Get("auth_user_id"); ok {
		switch x := v.(type) {
		case int64:
			if x > 0 {
				return strconv.FormatInt(x, 10)
			}
		case int:
			if x > 0 {
				return strconv.Itoa(x)
			}
		case string:
			if x != "" {
				return x
			}
		}
	}
	return service.DefaultScope
}

// GetStatus — GET /api/relay/status
func (h *RelayHandler) GetStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.GetStatus(scopeFor(c)))
}

type relayRequestCodeReq struct {
	Email string `json:"email"`
	URL   string `json:"url"`
}

// RequestEmailCode — POST /api/relay/email-code/request
func (h *RelayHandler) RequestEmailCode(c *gin.Context) {
	var req relayRequestCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.RequestEmailCode(scopeFor(c), req.Email, req.URL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

type relayVerifyCodeReq struct {
	Email string `json:"email"`
	Code  string `json:"code"`
	URL   string `json:"url"`
}

// VerifyEmailCode — POST /api/relay/email-code/verify
func (h *RelayHandler) VerifyEmailCode(c *gin.Context) {
	var req relayVerifyCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	st, err := h.svc.VerifyEmailCode(scopeFor(c), req.Email, req.Code, req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "status": st})
		return
	}
	c.JSON(http.StatusOK, st)
}

// Logout — POST /api/relay/logout
func (h *RelayHandler) Logout(c *gin.Context) {
	if err := h.svc.Logout(scopeFor(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// StartPairing — POST /api/relay/pairing/start
func (h *RelayHandler) StartPairing(c *gin.Context) {
	st, err := h.svc.StartPairing(scopeFor(c))
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "state": st})
		return
	}
	c.JSON(http.StatusOK, st)
}

// PairingStatus — GET /api/relay/pairing/status
func (h *RelayHandler) PairingStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.PairingStatus(scopeFor(c)))
}

// ConfirmPairing — POST /api/relay/pairing/confirm
func (h *RelayHandler) ConfirmPairing(c *gin.Context) {
	if err := h.svc.ConfirmPairing(scopeFor(c)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// RejectPairing — POST /api/relay/pairing/reject
func (h *RelayHandler) RejectPairing(c *gin.Context) {
	if err := h.svc.RejectPairing(scopeFor(c)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// tryReserveVerifyEmail atomically checks the throttle for scope and, if
// allowed, reserves the slot at `now`. Returns (retryAfterSeconds, false) when
// blocked and (0, true) when the caller may proceed. The reserved timestamp
// can be rolled back via releaseVerifyEmail if the downstream call fails.
func (h *RelayHandler) tryReserveVerifyEmail(scope string, now time.Time) (int, bool) {
	h.verifyEmailMu.Lock()
	defer h.verifyEmailMu.Unlock()
	if ts, ok := h.verifyEmailLast[scope]; ok {
		if wait := h.verifyEmailInterval - now.Sub(ts); wait > 0 {
			return int(wait.Seconds()) + 1, false
		}
	}
	h.verifyEmailLast[scope] = now
	if len(h.verifyEmailLast) > verifyEmailGCThreshold {
		h.sweepVerifyEmailLocked(now)
	}
	return 0, true
}

// releaseVerifyEmail rolls back a reservation made at `reservedAt` when the
// downstream call fails. Only clears the entry if no other caller has since
// overwritten it — otherwise we'd undo someone else's successful request.
func (h *RelayHandler) releaseVerifyEmail(scope string, reservedAt time.Time) {
	h.verifyEmailMu.Lock()
	defer h.verifyEmailMu.Unlock()
	if ts, ok := h.verifyEmailLast[scope]; ok && ts.Equal(reservedAt) {
		delete(h.verifyEmailLast, scope)
	}
}

// sweepVerifyEmailLocked drops entries older than 2× the throttle window —
// past that point they no longer affect rate-limit decisions. Caller must
// hold verifyEmailMu.
func (h *RelayHandler) sweepVerifyEmailLocked(now time.Time) {
	cutoff := now.Add(-2 * h.verifyEmailInterval)
	for k, ts := range h.verifyEmailLast {
		if ts.Before(cutoff) {
			delete(h.verifyEmailLast, k)
		}
	}
}

// RequestEmailVerification — POST /api/relay/verify-email/request
// Asks the relay to (re)send the verification email. Used by the pair flow to
// unblock the "unverified account + 1 desktop already used" 403 case.
// Throttled to one call per verifyEmailInterval per scope.
func (h *RelayHandler) RequestEmailVerification(c *gin.Context) {
	scope := scopeFor(c)
	now := time.Now()

	retryAfter, ok := h.tryReserveVerifyEmail(scope, now)
	if !ok {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "rate_limited",
			"retry_after": retryAfter,
		})
		return
	}

	if err := h.svc.RequestEmailVerification(scope); err != nil {
		h.releaseVerifyEmail(scope, now)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// ListTrustedMobiles — GET /api/relay/trusted-mobiles
func (h *RelayHandler) ListTrustedMobiles(c *gin.Context) {
	out := h.svc.ListTrustedMobiles(scopeFor(c))
	if out == nil {
		out = []service.TrustedMobileInfo{}
	}
	c.JSON(http.StatusOK, out)
}

// RemoveTrustedMobile — DELETE /api/relay/trusted-mobiles/:xpubHex
func (h *RelayHandler) RemoveTrustedMobile(c *gin.Context) {
	xpubHex := c.Param("xpubHex")
	if xpubHex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "xpub_hex required"})
		return
	}
	if err := h.svc.RemoveTrustedMobile(scopeFor(c), xpubHex); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// --- billing proxy endpoints ------------------------------------------------
//
// The relay exposes /api/billing/* and /api/mobile-devices/:id DELETE for the
// account. The desktop frontend talks only to niuniu-server, so these routes
// proxy through the scoped relay client (which handles login + token refresh).

// GetBillingUsage — GET /api/relay/billing/usage
func (h *RelayHandler) GetBillingUsage(c *gin.Context) {
	out, err := h.svc.GetBillingUsage(scopeFor(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// GetBillingPlan — GET /api/relay/billing/my-plan
func (h *RelayHandler) GetBillingPlan(c *gin.Context) {
	out, err := h.svc.GetBillingPlan(scopeFor(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// ListBillingPlans — GET /api/relay/billing/plans
func (h *RelayHandler) ListBillingPlans(c *gin.Context) {
	out, err := h.svc.ListBillingPlans(scopeFor(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if out == nil {
		out = []relayclient.Plan{}
	}
	c.JSON(http.StatusOK, out)
}

type relayChangePlanReq struct {
	TargetPlanID string `json:"target_plan_id" binding:"required"`
}

// ChangeBillingPlan — POST /api/relay/billing/change-plan
// Body: {"target_plan_id": "..."}. Free plans come back with {"applied": true};
// paid plans come back with {"checkout_url": "..."} — the frontend opens that
// URL in the system browser via the Wails shell binding.
func (h *RelayHandler) ChangeBillingPlan(c *gin.Context) {
	var req relayChangePlanReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := h.svc.ChangeBillingPlan(scopeFor(c), req.TargetPlanID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// ListRemoteMobileDevices — GET /api/relay/mobile-devices
// Returns the account's non-revoked mobile_devices rows on the relay, so the
// UI can surface "revoke stale mobile" actions distinct from the local
// trusted-mobiles allowlist.
func (h *RelayHandler) ListRemoteMobileDevices(c *gin.Context) {
	out, err := h.svc.ListRemoteMobileDevices(scopeFor(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if out == nil {
		out = []relayclient.MobileDeviceSummary{}
	}
	c.JSON(http.StatusOK, out)
}

// RevokeRemoteMobileDevice — DELETE /api/relay/mobile-devices/:id
func (h *RelayHandler) RevokeRemoteMobileDevice(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mobile_id required"})
		return
	}
	if err := h.svc.RevokeRemoteMobileDevice(scopeFor(c), id); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// ListRemoteAccountDesktops — GET /api/relay/desktops
// Returns the account's non-revoked desktops on the relay so the UI can
// surface them next to the mobile list. The default UI shows only the
// caller's local desktop; this lets the user see and revoke stale
// registrations from earlier installs / dev builds that still hold a
// desktop_id slot.
func (h *RelayHandler) ListRemoteAccountDesktops(c *gin.Context) {
	out, err := h.svc.ListRemoteAccountDesktops(scopeFor(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if out == nil {
		out = []relayclient.DesktopSummary{}
	}
	c.JSON(http.StatusOK, out)
}

// RevokeRemoteAccountDesktop — DELETE /api/relay/desktops/:id
func (h *RelayHandler) RevokeRemoteAccountDesktop(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "desktop_id required"})
		return
	}
	if err := h.svc.RevokeRemoteAccountDesktop(scopeFor(c), id); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
