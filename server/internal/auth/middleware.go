package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func Middleware(enabled bool, secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}

		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": "missing authentication token"},
			})
			return
		}

		claims, err := ValidateToken(tokenStr, secret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": "invalid or expired token"},
			})
			return
		}

		c.Set("auth_user", claims)
		c.Set("auth_user_id", claims.UserID)
		c.Set("auth_username", claims.Username)
		c.Set("auth_role", claims.Role)
		c.Next()
	}
}

// RequireRole returns middleware that rejects requests from users
// whose role is not in the allowed set. Must be placed after Middleware.
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		role, _ := c.Get("auth_role")
		roleStr, _ := role.(string)
		if roleStr == "" || !allowed[roleStr] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "FORBIDDEN", "message": "insufficient permissions"},
			})
			return
		}
		c.Next()
	}
}

// RequireAdmin gates a handler behind admin role.
//
// In personal mode (authEnabled=false), every authenticated identity passes
// — there is a single local user who is implicitly admin. In team mode, the
// caller's JWT `role` claim must be "admin" or "owner". This is consumed by
// admin-only endpoints (/api/admin/*) that mutate server-wide settings.
//
// Must be placed after Middleware so `auth_role` is populated.
func RequireAdmin(authEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authEnabled {
			c.Next()
			return
		}
		userID := c.GetInt64("auth_user_id")
		if userID == 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"},
			})
			return
		}
		role, _ := c.Get("auth_role")
		roleStr, _ := role.(string)
		if roleStr != "admin" && roleStr != "owner" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "FORBIDDEN", "message": "admin required"},
			})
			return
		}
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if t, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return t
	}
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}
