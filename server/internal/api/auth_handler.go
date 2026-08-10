package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// mapAuthUserError classifies errors from the auth user-management service
// into the right HTTP response. sql.ErrNoRows -> 404, UNIQUE-violation
// substrings -> 400 with a friendly message; *service.AuthError carries an
// explicit code and is mapped via codeToHTTPStatus.
func mapAuthUserError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		NotFound(c, "user")
		return
	}
	var ae *service.AuthError
	if errors.As(err, &ae) {
		writeAuthError(c, ae)
		return
	}
	msg := err.Error()
	// modernc/sqlite surfaces "UNIQUE constraint failed: users.username";
	// pgx surfaces `pq: duplicate key value violates unique constraint
	// "users_username_key"`. Both reach us as the err.Error() string.
	if strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "duplicate key") {
		BadRequest(c, "username already exists")
		return
	}
	BadRequest(c, msg)
}

// writeAuthError maps a typed *service.AuthError onto the right HTTP status +
// JSON envelope. retry_after is in seconds; details.failed_rules lists the
// password policy rules that failed.
func writeAuthError(c *gin.Context, ae *service.AuthError) {
	body := gin.H{"code": ae.Code, "message": ae.Message}
	status := http.StatusBadRequest

	switch ae.Code {
	case service.CodeAccountLocked:
		status = http.StatusLocked // 423
		body["retry_after"] = retryAfterSeconds(ae.RetryAfter)
	case service.CodeIPLocked:
		status = http.StatusTooManyRequests // 429
		body["retry_after"] = retryAfterSeconds(ae.RetryAfter)
	case service.CodeInvalidCredentials:
		status = http.StatusUnauthorized // 401
	case service.CodeWeakPassword:
		status = http.StatusBadRequest // 400
		if len(ae.FailedRules) > 0 {
			body["details"] = gin.H{"failed_rules": ae.FailedRules}
		}
	}
	c.AbortWithStatusJSON(status, gin.H{"error": body})
}

// retryAfterSeconds rounds the duration up to whole seconds (Sub(now) can be
// nanoseconds short of the next whole second; ceil avoids the off-by-one
// that would let the client retry one tick too early).
func retryAfterSeconds(d time.Duration) int {
	secs := int(d / time.Second)
	if d > 0 && d-time.Duration(secs)*time.Second > 0 {
		secs++
	}
	if secs < 1 && d > 0 {
		secs = 1
	}
	return secs
}

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "username and password are required")
		return
	}

	ip := c.ClientIP()
	ua := c.Request.UserAgent()
	trustCookie, _ := c.Cookie("mfa_trust")

	tokens, err := h.svc.LoginWithMeta(c.Request.Context(), req.Username, req.Password, ip, ua, trustCookie)
	if err != nil {
		var ae *service.AuthError
		if errors.As(err, &ae) {
			writeAuthError(c, ae)
			return
		}
		// Any non-AuthError is a real backend fault (DB down etc.); 500.
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, tokens)
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "refresh_token is required")
		return
	}

	tokens, err := h.svc.Refresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "invalid or expired refresh token"},
		})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "refresh_token is required")
		return
	}

	h.svc.Logout(c.Request.Context(), req.RefreshToken) //nolint:errcheck
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

func (h *AuthHandler) ListUsers(c *gin.Context) {
	users, err := h.svc.ListUsers(c.Request.Context())
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, users)
}

func (h *AuthHandler) DeleteUserByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	// Prevent users from deleting themselves
	if currentID, exists := c.Get("auth_user_id"); exists {
		if uid, ok := currentID.(int64); ok && uid == id {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "BAD_REQUEST", "message": "cannot delete yourself"},
			})
			return
		}
	}
	if err := h.svc.DeleteUser(c.Request.Context(), id); err != nil {
		// Service-layer constraint failures (e.g. "cannot remove last admin")
		// are user-correctable, not server bugs.
		mapAuthUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "user deleted"})
}

func (h *AuthHandler) Me(c *gin.Context) {
	userID, exists := c.Get("auth_user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}

	uid, ok := userID.(int64)
	if !ok {
		InternalError(c, fmt.Errorf("invalid user ID type in context"))
		return
	}
	user, err := h.svc.GetUser(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"role":         user.Role,
	})
}

// callerID returns the authenticated user ID. In production, it comes from
// IdentityResolver (c.Get("auth_user_id")). In handler unit tests, no middleware
// runs, so we accept "X-Test-Caller-ID" as a fallback.
func callerID(c *gin.Context) (int64, bool) {
	if v, ok := c.Get("auth_user_id"); ok {
		if id, ok := v.(int64); ok {
			return id, true
		}
	}
	if h := c.GetHeader("X-Test-Caller-ID"); h != "" {
		if id, err := strconv.ParseInt(h, 10, 64); err == nil {
			return id, true
		}
	}
	return 0, false
}

func (h *AuthHandler) CreateUser(c *gin.Context) {
	var req struct {
		Username    string `json:"username" binding:"required"`
		Password    string `json:"password" binding:"required"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "username and password are required")
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}
	if req.Role != "admin" && req.Role != "member" {
		BadRequest(c, "invalid role")
		return
	}

	// Password strength is enforced inside the service via the configured
	// policy; the handler no longer hard-codes a 6-character floor.
	user, err := h.svc.CreateUser(c.Request.Context(), req.Username, req.Password, req.DisplayName, req.Role)
	if err != nil {
		mapAuthUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"role":         user.Role,
		"created_at":   user.CreatedAt,
	})
}

func (h *AuthHandler) UpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid body")
		return
	}
	caller, _ := callerID(c) // 0 when missing — service still applies last-admin guard
	user, err := h.svc.UpdateUser(c.Request.Context(), id, caller, req.DisplayName, req.Role)
	if err != nil {
		mapAuthUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"role":         user.Role,
		"created_at":   user.CreatedAt,
	})
}

func (h *AuthHandler) ResetPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "password is required")
		return
	}
	if err := h.svc.ResetPassword(c.Request.Context(), id, req.Password); err != nil {
		mapAuthUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password reset"})
}

// ChangePassword lets a logged-in user rotate their own password. Requires the
// current password; new password must satisfy the policy. Other sessions are
// revoked on success.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "missing identity"},
		})
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "current_password and new_password are required")
		return
	}
	if err := h.svc.ChangePassword(c.Request.Context(), uid, req.CurrentPassword, req.NewPassword); err != nil {
		var ae *service.AuthError
		if errors.As(err, &ae) {
			writeAuthError(c, ae)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			NotFound(c, "user")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password changed"})
}

// LoginHistory returns the current user's most recent login attempts.
// Default cap is 50; honours ?limit=N up to 200.
func (h *AuthHandler) LoginHistory(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "missing identity"},
		})
		return
	}
	limit := int64(50)
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			if n > 200 {
				n = 200
			}
			limit = n
		}
	}
	rows, err := h.svc.Audit().ListRecentForUser(c.Request.Context(), uid, limit)
	if err != nil {
		InternalError(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"id":         r.ID,
			"user_id":    nullInt64Value(r.UserID),
			"username":   r.Username,
			"ip":         r.Ip,
			"user_agent": r.UserAgent,
			"success":    r.Success == 1,
			"reason":     r.Reason,
			"created_at": r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func nullInt64Value(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}
