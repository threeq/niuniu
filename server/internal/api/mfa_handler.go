package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/internal/mfapolicy"
	"github.com/niuniu-dev/niuniu/internal/service"
)

func parseInt(s string, out *int64) (bool, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return false, err
	}
	*out = v
	return true, nil
}

type MFAHandler struct {
	svc *service.AuthService
	// policy answers whether the caller's role mandates two-factor enrollment.
	// May be nil when mandatory enrollment is not configured; Policy() then
	// reports an all-false status.
	policy *mfapolicy.Service
}

func NewMFAHandler(svc *service.AuthService) *MFAHandler {
	return &MFAHandler{svc: svc}
}

// SetPolicy wires the mandatory-enrollment policy service. Separate from the
// constructor so existing callers/tests that only exercise the self-service MFA
// endpoints keep working unchanged.
func (h *MFAHandler) SetPolicy(p *mfapolicy.Service) {
	h.policy = p
}

// Policy reports whether the caller MUST have two-factor authentication enabled
// and whether they already do. It drives the SPA's blocking enrollment page.
//
// Read-only, and on the allowlist of both the consent and MFA-enrollment
// guards, so it stays reachable for exactly the users who are being blocked —
// otherwise the SPA could not learn why.
//
// On a policy read error this reports needs_setup=false (fail OPEN) so a
// transient DB hiccup cannot strand a user behind an un-dismissable page. The
// server-side MFAEnrollGuard fails closed and remains the real enforcement
// point, so failing open here costs nothing.
func (h *MFAHandler) Policy(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	if h.policy == nil {
		c.JSON(http.StatusOK, mfapolicy.Status{})
		return
	}
	st, err := h.policy.Status(c.Request.Context(), uid)
	if err != nil {
		slog.Warn("mfa policy status read failed", "user_id", uid, "error", err)
		c.JSON(http.StatusOK, mfapolicy.Status{})
		return
	}
	c.JSON(http.StatusOK, st)
}

func (h *MFAHandler) Setup(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	if h.svc.MFA == nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "MFA_NOT_AVAILABLE"}})
		return
	}
	user, err := h.svc.GetUser(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	if user.MfaEnabled == 1 {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code": "MFA_ALREADY_ENABLED"}})
		return
	}
	result, err := h.svc.MFA.Setup(c.Request.Context(), uid, "牛牛/Niuniu", user.Username)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provisioning_uri": result.ProvisioningURI,
		"qr_data_uri":      result.QRDataURI,
		"secret":           result.Secret,
	})
}

func (h *MFAHandler) Enable(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "code is required")
		return
	}
	codes, err := h.svc.MFA.Enable(c.Request.Context(), uid, req.Code, 10)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "MFA_INVALID", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"backup_codes": codes})
}

func (h *MFAHandler) Verify(c *gin.Context) {
	var req struct {
		MFAToken    string `json:"mfa_token" binding:"required"`
		Code        string `json:"code" binding:"required"`
		TrustDevice bool   `json:"trust_device"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "mfa_token and code are required")
		return
	}
	claims, err := auth.ValidateMFAStateToken(req.MFAToken, h.svc.Secret())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "MFA_TOKEN_EXPIRED", "message": "mfa token expired or invalid"}})
		return
	}
	if err := h.svc.MFA.ValidateCodeOrBackup(c.Request.Context(), claims.UserID, req.Code); err != nil {
		h.svc.Audit().RecordAttempt(c.Request.Context(), sql.NullInt64{Int64: claims.UserID, Valid: true}, "", c.ClientIP(), c.Request.UserAgent(), "mfa_failed", false)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "MFA_INVALID", "message": err.Error()}})
		return
	}

	h.svc.Audit().RecordAttempt(c.Request.Context(), sql.NullInt64{Int64: claims.UserID, Valid: true}, "", c.ClientIP(), c.Request.UserAgent(), "ok", true)

	user, err := h.svc.GetUser(c.Request.Context(), claims.UserID)
	if err != nil {
		InternalError(c, err)
		return
	}
	tokens, err := h.svc.GenerateTokenPair(c.Request.Context(), user)
	if err != nil {
		InternalError(c, err)
		return
	}

	resp := gin.H{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_in":    tokens.ExpiresIn,
	}

	if req.TrustDevice {
		raw, err := h.svc.MFA.TrustDevice(c.Request.Context(), claims.UserID, c.Request.UserAgent(), 30*24*time.Hour)
		if err == nil {
			c.SetCookie("mfa_trust", raw, 30*24*3600, "/", "", true, true)
		}
	}

	c.JSON(http.StatusOK, resp)
}

func (h *MFAHandler) Disable(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	// A member whose role mandates two-factor authentication may not turn it
	// back off. Without this check the requirement would be trivially defeated:
	// MFAEnrollGuard only blocks /mfa/disable while enrollment is still owed, so
	// the moment a user enables MFA the path opens and they could immediately
	// disable it again. The admin reset endpoint remains the escape hatch for a
	// genuinely locked-out member.
	if h.policy != nil {
		user, err := h.svc.GetUser(c.Request.Context(), uid)
		if err != nil {
			InternalError(c, err)
			return
		}
		if h.policy.RoleRequired(user.Role) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"code":    "MFA_REQUIRED_BY_POLICY",
				"message": "团队安全策略要求保持两步验证开启，无法关闭",
			}})
			return
		}
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "code is required")
		return
	}
	if err := h.svc.MFA.Disable(c.Request.Context(), uid, req.Code); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "MFA_INVALID", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "mfa disabled"})
}

func (h *MFAHandler) Status(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	if h.svc.MFA == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	st, err := h.svc.MFA.Status(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

func (h *MFAHandler) RegenerateBackupCodes(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "code is required")
		return
	}
	codes, err := h.svc.MFA.RegenerateBackupCodes(c.Request.Context(), uid, req.Code, 10)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "MFA_INVALID", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"backup_codes": codes})
}

func (h *MFAHandler) AdminResetMFA(c *gin.Context) {
	idStr := c.Param("id")
	var id int64
	if _, err := parseInt(idStr, &id); err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	if err := h.svc.MFA.AdminResetMFA(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "mfa reset"})
}

func (h *MFAHandler) CheckTrustedDevice(c *gin.Context) int64 {
	if h.svc.MFA == nil {
		return 0
	}
	raw, err := c.Cookie("mfa_trust")
	if err != nil {
		return 0
	}
	uid, valid := h.svc.MFA.CheckTrustedDevice(c.Request.Context(), raw)
	if !valid {
		return 0
	}
	return uid
}
