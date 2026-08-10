package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// AgentFileHandler manages file-based agent CRUD (distinct from AgentHandler which handles PTY agents).
type AgentFileHandler struct {
	svc   *service.AgentService
	Authz *service.Authz
}

func NewAgentFileHandler(svc *service.AgentService) *AgentFileHandler {
	return &AgentFileHandler{svc: svc}
}

func (h *AgentFileHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var err error
	agents, err := h.svc.List(c.Request.Context())
	if userID > 0 {
		agents, err = h.svc.ListForUser(c.Request.Context(), userID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"agents": agents})
}

func (h *AgentFileHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessAgent(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	detail, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h *AgentFileHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var input service.CreateAgentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	owner := service.OwnerRef{Type: "user", ID: userID}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	agent, err := h.svc.Create(c.Request.Context(), input, owner.Type, owner.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, agent)
}

func (h *AgentFileHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var input service.UpdateAgentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, input); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *AgentFileHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Sync re-downloads agent from source_url. Placeholder for now.
func (h *AgentFileHandler) Sync(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "sync not yet implemented"})
}

// Import downloads agent from URL. Placeholder for now.
func (h *AgentFileHandler) Import(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "import not yet implemented"})
}
