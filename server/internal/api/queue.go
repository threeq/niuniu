package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type QueueHandler struct {
	q            *store.Queries
	db           *sql.DB
	notifyHub    *notify.NotificationHub
	broadcast    func(workspaceID int64, eventType, content string) // SSE broadcast
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz
}

func NewQueueHandler(q *store.Queries, db *sql.DB, notifyHub *notify.NotificationHub, broadcast func(workspaceID int64, eventType, content string), workspaceSvc *service.WorkspaceService) *QueueHandler {
	return &QueueHandler{q: q, db: db, notifyHub: notifyHub, broadcast: broadcast, workspaceSvc: workspaceSvc}
}

func (h *QueueHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	items, err := h.q.ListQueueItems(c.Request.Context(), workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}
	if items == nil {
		items = []store.WorkspaceQueue{}
	}
	c.JSON(http.StatusOK, items)
}

func (h *QueueHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "content is required")
		return
	}
	ctx := c.Request.Context()
	maxPos, _ := h.q.GetMaxQueuePosition(ctx, workspaceID)
	item, err := h.q.CreateQueueItem(ctx, store.CreateQueueItemParams{
		WorkspaceID: workspaceID,
		Content:     req.Content,
		Position:    maxPos + 1000,
		Source:      "user",
	})
	if err != nil {
		InternalError(c, err)
		return
	}
	h.broadcastQueueUpdate(workspaceID)
	c.JSON(http.StatusCreated, item)
}

func (h *QueueHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	queueID, err := strconv.ParseInt(c.Param("queueId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid queue ID")
		return
	}
	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "content is required")
		return
	}
	if err := h.q.UpdateQueueItemContent(c.Request.Context(), store.UpdateQueueItemContentParams{
		Content:     req.Content,
		ID:          queueID,
		WorkspaceID: workspaceID,
	}); err != nil {
		InternalError(c, err)
		return
	}
	h.broadcastQueueUpdate(workspaceID)
	c.Status(http.StatusNoContent)
}

func (h *QueueHandler) Reorder(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req struct {
		Items []struct {
			ID       int64   `json:"id"`
			Position float64 `json:"position"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "items array is required")
		return
	}
	ctx := c.Request.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		InternalError(c, err)
		return
	}
	defer tx.Rollback()
	qtx := store.NewQueriesTx(tx)
	for _, item := range req.Items {
		if err := qtx.UpdateQueueItemPosition(ctx, store.UpdateQueueItemPositionParams{
			Position:    item.Position,
			ID:          item.ID,
			WorkspaceID: workspaceID,
		}); err != nil {
			InternalError(c, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		InternalError(c, err)
		return
	}
	h.broadcastQueueUpdate(workspaceID)
	c.Status(http.StatusNoContent)
}

func (h *QueueHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	queueID, err := strconv.ParseInt(c.Param("queueId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid queue ID")
		return
	}
	if err := h.q.DeleteQueueItem(c.Request.Context(), store.DeleteQueueItemParams{
		ID:          queueID,
		WorkspaceID: workspaceID,
	}); err != nil {
		InternalError(c, err)
		return
	}
	h.broadcastQueueUpdate(workspaceID)
	c.Status(http.StatusNoContent)
}

func (h *QueueHandler) broadcastQueueUpdate(workspaceID int64) {
	if h.broadcast != nil {
		h.broadcast(workspaceID, "queue_update", "")
	}
	if h.notifyHub != nil {
		h.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicQueue, Action: "changed", ID: workspaceID})
	}
}
