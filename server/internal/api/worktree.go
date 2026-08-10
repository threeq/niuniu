package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type WorktreeHandler struct {
	svc *service.WorktreeService
}

func NewWorktreeHandler(svc *service.WorktreeService) *WorktreeHandler {
	return &WorktreeHandler{svc: svc}
}

func (h *WorktreeHandler) List(c *gin.Context) {
	wsID, _ := strconv.ParseInt(c.Query("workspace_id"), 10, 64)
	repoID, _ := strconv.ParseInt(c.Query("repo_id"), 10, 64)

	repos, err := h.svc.List(c.Request.Context(), wsID, repoID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, repos)
}

func (h *WorktreeHandler) Create(c *gin.Context) {
	var input service.CreateWorktreeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := h.svc.Create(c.Request.Context(), input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *WorktreeHandler) Remove(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.svc.Remove(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (h *WorktreeHandler) GetTree(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	// Find worktree to get its path
	worktrees, err := h.svc.List(c.Request.Context(), 0, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var worktreePath string
	for _, wr := range worktrees {
		if wr.ID == id {
			worktreePath = wr.WorktreePath
			break
		}
	}
	if worktreePath == "" {
		slog.Warn("GetTree failed - worktree not found", "id", id)
		c.JSON(http.StatusNotFound, gin.H{"error": "worktree not found"})
		return
	}

	// Allow sub-path for lazy loading
	path := c.Query("path")
	if path != "" {
		worktreePath = worktreePath + "/" + path
	}

	tree, err := h.svc.GetTree(c.Request.Context(), worktreePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, tree)
}

func (h *WorktreeHandler) GetStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	status, err := h.svc.GetStatus(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetStatus failed", "id", id, "error", err)
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}
