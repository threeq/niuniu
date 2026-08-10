package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type WorkspaceTaskHandler struct {
	Q     *store.Queries
	Proxy *agentproxy.AgentProxy
}

func (h *WorkspaceTaskHandler) List(c *gin.Context) {
	wsId, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	ctx := c.Request.Context()

	batchId, err := h.Q.GetLatestBatchId(ctx, wsId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusOK, gin.H{"tasks": []store.WorkspaceTask{}})
			return
		}
		InternalError(c, err)
		return
	}

	tasks, err := h.Q.ListWorkspaceTasksByBatch(ctx, store.ListWorkspaceTasksByBatchParams{
		WorkspaceID: wsId,
		BatchID:     batchId,
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	h.interruptStaleIfIdle(ctx, wsId)

	// Re-fetch in case tasks were interrupted
	tasks, err = h.Q.ListWorkspaceTasksByBatch(ctx, store.ListWorkspaceTasksByBatchParams{
		WorkspaceID: wsId,
		BatchID:     batchId,
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

// interruptStaleIfIdle marks in_progress tasks as interrupted when the session is idle.
func (h *WorkspaceTaskHandler) interruptStaleIfIdle(ctx context.Context, wsId int64) {
	session := h.Proxy.GetSession(wsId)
	if session == nil || session.Status() == agentproxy.StatusIdle {
		if err := h.Q.InterruptInProgressTasks(ctx, wsId); err != nil {
			slog.Warn("interruptStaleIfIdle failed", "workspaceID", wsId, "err", err)
		}
	}
}
