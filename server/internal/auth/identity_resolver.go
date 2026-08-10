package auth

import (
	"database/sql"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/config"
)

// IdentityResolver runs BEFORE the JWT-checking Middleware on every transport
// that carries user requests (REST, /mcp, SSE, WS). It sets auth_user_id and
// auth_role in the Gin context from whichever source is appropriate.
//
// Resolution order (spec §5.7):
//  1. /mcp/* paths → pass through (MCP has its own token middleware)
//  2. JWT header/query present → parse and set
//  3. Auth.Enabled=false → seed user from cfg.Auth.SingleUser
//  4. Otherwise → leave context clean; downstream Middleware rejects
func IdentityResolver(cfg *config.Config, db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.FullPath(), "/mcp/") || strings.HasPrefix(c.Request.URL.Path, "/mcp/") {
			c.Next()
			return
		}

		if claims, ok := tryParseJWT(c, cfg); ok {
			c.Set("auth_user_id", claims.UserID)
			c.Set("auth_role", claims.Role)
			c.Set("auth_username", claims.Username)
			c.Next()
			return
		}

		if !cfg.Auth.Enabled {
			username := cfg.Auth.SingleUser.Username
			if username == "" {
				username = "local"
			}
			var id int64
			var role string
			err := db.QueryRow(`SELECT id, role FROM users WHERE username = ?`, username).Scan(&id, &role)
			if err == nil {
				c.Set("auth_user_id", id)
				c.Set("auth_role", role)
				c.Set("auth_username", username)
			} else if err != sql.ErrNoRows {
				slog.Warn("IdentityResolver: seed user lookup failed", "error", err)
			}
		}
		c.Next()
	}
}

// tryParseJWT attempts to parse a JWT from Authorization header or ?token query.
// Returns parsed claims if successful, (nil, false) otherwise.
// Reuses extractToken (middleware.go) and ValidateToken (jwt.go) — no logic duplication.
func tryParseJWT(c *gin.Context, cfg *config.Config) (*Claims, bool) {
	raw := extractToken(c)
	if raw == "" {
		return nil, false
	}
	secret := config.GetAuthSecret()
	claims, err := ValidateToken(raw, secret)
	if err != nil {
		return nil, false
	}
	return claims, true
}
