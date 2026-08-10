package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// MCPTokenAuth returns a middleware that validates MCP bearer tokens on /mcp/* routes.
// It extracts the raw token from "Authorization: Bearer <token>", calls
// sess.Validate to resolve the workspace_id, then loads the workspace's
// current_session_user_id and sets auth_user_id in the Gin context so
// downstream handlers can enforce resource ownership.
//
// If no Authorization header is present the request is allowed through
// with auth_user_id=0 (the existing LocalhostOnly gate is the primary
// security barrier; token auth is additive for multi-tenant enforcement).
// If the header IS present but the token is invalid, the request is rejected
// with 401.
//
// It takes the driver-aware *store.Queries (not a raw *sql.DB): the identity
// lookups run on both SQLite and PostgreSQL, and a raw `?` query on the pgx
// connection would reach PG verbatim (SQLSTATE 42601) and silently resolve
// auth_user_id=0 — 401-ing every credential-scoped MCP tool on team edition.
func MCPTokenAuth(sess *service.MCPSessionService, q *store.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "MCP requires Bearer token"})
			return
		}

		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid Authorization header format"})
			return
		}

		raw := strings.TrimPrefix(authHeader, "Bearer ")
		raw = strings.TrimSpace(raw)
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "empty bearer token"})
			return
		}

		ctx := c.Request.Context()
		workspaceID, err := sess.Validate(ctx, raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		if ws, wErr := q.GetWorkspace(ctx, workspaceID); wErr == nil {
			if userID := resolveMCPIdentity(ctx, q, ws); userID != 0 {
				c.Set("auth_user_id", userID)
			}
		}

		// Set workspace_id so handlers that need it can skip a DB lookup.
		c.Set("mcp_workspace_id", workspaceID)
		c.Next()
	}
}

// resolveMCPIdentity picks the effective user identity for credential-scoped
// MCP tools (/mcp/external-*, /mcp/data-proxy, /mcp/dashboards), which
// hard-require auth_user_id != 0 and 401 otherwise. Kanban/issue tools tolerate
// 0 (they guard authz behind `if userID > 0`), which is why only the credential
// tools broke.
//
// Resolution order, most specific first:
//  1. active session user — set at user-initiated agent start / interactive send.
//  2. personal-workspace owner — owner_id is a real user id only when
//     owner_type='user'; for org-owned workspaces it is the org id.
//  3. workspace creator (created_by) — covers autonomous starts (scheduler /
//     autohost / gate pass userID=0, leaving current_session_user_id NULL).
//  4. org owner/admin member — last resort for org-owned workspaces whose
//     created_by is also unset (legacy / no-issue workspaces). Without it those
//     autonomously-started agents 401 on every credential tool. Picks the org's
//     owner, else an admin, else any member (GetOrgFallbackIdentity).
func resolveMCPIdentity(ctx context.Context, q *store.Queries, ws store.Workspace) int64 {
	switch {
	case ws.CurrentSessionUserID.Valid && ws.CurrentSessionUserID.Int64 != 0:
		return ws.CurrentSessionUserID.Int64
	case ws.OwnerType == "user" && ws.OwnerID != 0:
		return ws.OwnerID
	case ws.CreatedBy.Valid && ws.CreatedBy.Int64 != 0:
		return ws.CreatedBy.Int64
	}
	if ws.OwnerType == "org" && ws.OwnerID != 0 {
		if uid, err := q.GetOrgFallbackIdentity(ctx, ws.OwnerID); err == nil && uid != 0 {
			return uid
		}
	}
	return 0
}
