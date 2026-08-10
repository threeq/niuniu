package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// PermissionHandler serves the chat-side REST endpoints that back the
// permission-prompt UI:
//   - List pending permission requests for a workspace
//   - Decide (allow/deny, optionally with always + matcher)
//   - List/Delete allowlist entries
//
// Spec: docs/superpowers/specs/2026-05-01-chat-permission-prompt-design.md
type PermissionHandler struct {
	DB    *store.DB
	Perm  *service.PermissionService
	Authz *service.Authz
}

// permissionRequestDTO is the wire shape for a single pending request. tool_input
// is decoded from its JSON column so the frontend does not have to double-parse.
type permissionRequestDTO struct {
	ID          int64          `json:"id"`
	WorkspaceID int64          `json:"workspace_id"`
	SessionID   string         `json:"session_id"`
	ToolName    string         `json:"tool_name"`
	ToolInput   map[string]any `json:"tool_input"`
	RequestedAt int64          `json:"requested_at"`
	ExpiresAt   int64          `json:"expires_at"`
	Status      string         `json:"status"`
}

// permissionAllowlistDTO is the wire shape for an allowlist entry.
type permissionAllowlistDTO struct {
	ID           int64  `json:"id"`
	WorkspaceID  int64  `json:"workspace_id"`
	ToolName     string `json:"tool_name"`
	MatcherKind  string `json:"matcher_kind"`
	MatcherValue string `json:"matcher_value"`
	CreatedBy    int64  `json:"created_by,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

// decideMatcher mirrors service.Matcher in the request body shape.
type decideMatcher struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// decideBody is the JSON body for POST /agent-permission-decisions/:requestID.
type decideBody struct {
	Decision    string         `json:"decision"`              // "allow" | "deny"
	Scope       string         `json:"scope"`                 // "once" | "always"
	Matcher     *decideMatcher `json:"matcher,omitempty"`     // required when scope=always
	DenyMessage string         `json:"deny_message,omitempty"`
}

// ListByWorkspace handles GET /api/workspaces/:id/permission-requests
// and returns pending requests for the workspace.
func (h *PermissionHandler) ListByWorkspace(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace id")
		return
	}
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	if h.Authz != nil && userID > 0 {
		if _, aerr := h.Authz.CanAccessWorkspace(ctx, userID, wsID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	q := store.New(h.DB)
	rows, err := q.ListPendingPermissionRequestsByWorkspace(ctx, wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]permissionRequestDTO, 0, len(rows))
	for _, r := range rows {
		var input map[string]any
		_ = json.Unmarshal([]byte(r.ToolInput), &input)
		if input == nil {
			input = map[string]any{}
		}
		out = append(out, permissionRequestDTO{
			ID:          r.ID,
			WorkspaceID: r.WorkspaceID,
			SessionID:   r.SessionID,
			ToolName:    r.ToolName,
			ToolInput:   input,
			RequestedAt: r.RequestedAt.UnixMilli(),
			ExpiresAt:   r.ExpiresAt.UnixMilli(),
			Status:      r.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}

// Decide handles POST /api/agent-permission-decisions/:requestID
// — records the user's allow/deny answer and unblocks the waiting MCP call.
func (h *PermissionHandler) Decide(c *gin.Context) {
	reqID, err := strconv.ParseInt(c.Param("requestID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid request id")
		return
	}
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	if h.Authz != nil && userID > 0 {
		if aerr := h.Authz.CanAccessPermissionRequest(ctx, userID, reqID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	var body decideBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.Decision != "allow" && body.Decision != "deny" {
		BadRequest(c, "decision must be 'allow' or 'deny'")
		return
	}
	if body.Decision == "allow" && body.Scope == "always" && body.Matcher == nil {
		BadRequest(c, "matcher is required when scope='always'")
		return
	}

	d := service.Decision{
		Allow:       body.Decision == "allow",
		Always:      body.Scope == "always",
		DenyMessage: body.DenyMessage,
		UserID:      userID,
	}
	if body.Matcher != nil {
		d.Matcher = &service.Matcher{Kind: body.Matcher.Kind, Value: body.Matcher.Value}
	}

	// Spec §6 high-risk-vs-'any' matcher rule is enforced inside
	// service.Decide (single source of truth, inside the tx). The handler
	// only maps the structured error to HTTP 422.
	if err := h.Perm.Decide(ctx, reqID, d); err != nil {
		if errors.Is(err, service.ErrAlreadyDecided) {
			Conflict(c, "permission request already decided")
			return
		}
		if errors.Is(err, service.ErrMatcherAnyForHighRisk) {
			RespondError(c, http.StatusUnprocessableEntity, "MATCHER_REQUIRED", err.Error())
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListAllowlist handles GET /api/workspaces/:id/permission-allowlist
// — returns all allowlist entries for the workspace.
func (h *PermissionHandler) ListAllowlist(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace id")
		return
	}
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	if h.Authz != nil && userID > 0 {
		if _, aerr := h.Authz.CanAccessWorkspace(ctx, userID, wsID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	q := store.New(h.DB)
	rows, err := q.ListPermissionAllowlistByWorkspace(ctx, wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]permissionAllowlistDTO, 0, len(rows))
	for _, r := range rows {
		dto := permissionAllowlistDTO{
			ID:           r.ID,
			WorkspaceID:  r.WorkspaceID,
			ToolName:     r.ToolName,
			MatcherKind:  r.MatcherKind,
			MatcherValue: r.MatcherValue,
			CreatedAt:    r.CreatedAt.UnixMilli(),
		}
		if r.CreatedBy.Valid {
			dto.CreatedBy = r.CreatedBy.Int64
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, gin.H{"entries": out})
}

// DeleteAllowlist handles DELETE /api/permission-allowlist/:entryID
// — removes a single allowlist entry. The workspace owning the entry is
// fetched first so we can run the standard workspace-access check.
func (h *PermissionHandler) DeleteAllowlist(c *gin.Context) {
	entryID, err := strconv.ParseInt(c.Param("entryID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid entry id")
		return
	}
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	q := store.New(h.DB)
	wsID, err := q.GetPermissionAllowlistEntryWorkspaceID(ctx, entryID)
	if err != nil {
		NotFound(c, "allowlist_entry")
		return
	}
	if h.Authz != nil && userID > 0 {
		if _, aerr := h.Authz.CanAccessWorkspace(ctx, userID, wsID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	if _, err := q.DeletePermissionAllowlist(ctx, entryID); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
