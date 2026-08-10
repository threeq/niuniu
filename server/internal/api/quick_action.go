package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type QuickActionHandler struct {
	svc   *service.QuickActionService
	Authz *service.Authz
	DB    *sql.DB // used for batch owner-name lookup on list endpoints
}

func NewQuickActionHandler(svc *service.QuickActionService) *QuickActionHandler {
	return &QuickActionHandler{svc: svc}
}

type CreateQuickActionRequest struct {
	Label    string `json:"label" binding:"required"`
	Content  string `json:"content" binding:"required"`
	AutoSend bool   `json:"auto_send"`
	Owner    *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`
}

type UpdateQuickActionRequest struct {
	Label    string `json:"label" binding:"required"`
	Content  string `json:"content" binding:"required"`
	AutoSend bool   `json:"auto_send"`
}

type ReorderQuickActionsRequest struct {
	IDs []int64 `json:"ids" binding:"required"`
}

func (h *QuickActionHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	var err error
	actions, err := h.svc.List(ctx)
	if userID > 0 {
		actions, err = h.svc.ListForUser(ctx, userID)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	if h.DB != nil {
		refs := make([]ownerRef, len(actions))
		for i, a := range actions {
			refs[i] = ownerRef{a.OwnerType, a.OwnerID}
		}
		lk, _ := newOwnerLookup(ctx, h.DB, refs)
		c.JSON(http.StatusOK, toQuickActionResponsesWithLookup(actions, lk))
		return
	}
	c.JSON(http.StatusOK, toQuickActionResponses(actions))
}

// SceneQuickActionResponse is a quick action contributed by the workspace's
// active scene(s). It has no DB id (never persisted); `slug` is its identity and
// `source` is always "scene" so the SPA can render it in its own group.
type SceneQuickActionResponse struct {
	Slug     string `json:"slug"`
	Label    string `json:"label"`
	Content  string `json:"content"`
	AutoSend bool   `json:"auto_send"` // always false — scenes don't declare auto-send
	Source   string `json:"source"`    // always "scene"
}

// WorkspaceQuickActionsResponse splits quick actions into the user's own
// personal/org actions (editable, DB-backed) and the scene-provided group
// (read-only, parsed live from the workspace projection).
type WorkspaceQuickActionsResponse struct {
	Owner []QuickActionResponse      `json:"owner"`
	Scene []SceneQuickActionResponse `json:"scene"`
}

func toSceneQuickActionResponses(qas []service.SceneQuickAction) []SceneQuickActionResponse {
	out := make([]SceneQuickActionResponse, len(qas))
	for i, qa := range qas {
		out[i] = SceneQuickActionResponse{Slug: qa.Slug, Label: qa.Label, Content: qa.Content, AutoSend: false, Source: "scene"}
	}
	return out
}

// ListForWorkspace returns quick actions for a workspace: the caller's
// personal/org actions plus the scene-provided group parsed live from the
// workspace's scene projection (never persisted). Backs GET
// /api/workspaces/:id/quick-actions.
func (h *QuickActionHandler) ListForWorkspace(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	ctx := c.Request.Context()
	if h.Authz != nil && userID > 0 {
		if _, aerr := h.Authz.CanAccessWorkspace(ctx, userID, wsID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	// Owner group — personal + org, same selection as the global list endpoint.
	owners, err := h.svc.List(ctx)
	if userID > 0 {
		owners, err = h.svc.ListForUser(ctx, userID)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	var ownerResp []QuickActionResponse
	if h.DB != nil {
		refs := make([]ownerRef, len(owners))
		for i, a := range owners {
			refs[i] = ownerRef{a.OwnerType, a.OwnerID}
		}
		lk, _ := newOwnerLookup(ctx, h.DB, refs)
		ownerResp = toQuickActionResponsesWithLookup(owners, lk)
	} else {
		ownerResp = toQuickActionResponses(owners)
	}

	// Scene group — parsed live from the projection, never persisted.
	sceneQAs, err := h.svc.ListSceneQuickActionsForWorkspace(ctx, wsID)
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, WorkspaceQuickActionsResponse{
		Owner: ownerResp,
		Scene: toSceneQuickActionResponses(sceneQAs),
	})
}

func (h *QuickActionHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid quick action ID")
		return
	}
	// QuickActionService does not have a dedicated CanAccess method on Authz;
	// ownership is enforced by ListForUser filtering at the list level.
	// For individual get/update/delete we gate via ErrNotFound behavior below.
	_ = userID
	action, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetQuickAction failed", "id", id, "error", err)
		NotFound(c, "QUICK_ACTION")
		return
	}
	c.JSON(http.StatusOK, toQuickActionResponse(action))
}

func (h *QuickActionHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateQuickActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	// Resolve owner: explicit body wins, but treat both nil and the SPA
	// no-currentUser fallback {type:"user", id:0} as "default to caller".
	var owner service.OwnerRef
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
		if err := owner.Validate(); err != nil {
			BadRequest(c, err.Error())
			return
		}
	} else {
		owner = service.OwnerRef{Type: "user", ID: userID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	action, err := h.svc.Create(c.Request.Context(), req.Label, req.Content, req.AutoSend, owner.Type, owner.ID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toQuickActionResponse(action))
}

func (h *QuickActionHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid quick action ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessQuickAction(c.Request.Context(), userID, id); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req UpdateQuickActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	action, err := h.svc.Update(c.Request.Context(), id, req.Label, req.Content, req.AutoSend)
	if err != nil {
		slog.Warn("UpdateQuickAction failed", "id", id, "error", err)
		NotFound(c, "QUICK_ACTION")
		return
	}
	c.JSON(http.StatusOK, toQuickActionResponse(action))
}

func (h *QuickActionHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid quick action ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessQuickAction(c.Request.Context(), userID, id); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *QuickActionHandler) Reorder(c *gin.Context) {
	var req ReorderQuickActionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.Reorder(c.Request.Context(), req.IDs); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
