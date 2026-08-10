package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/registry"
)

type AgentRegistryHandler struct {
	reg *registry.AgentRegistry
}

func NewAgentRegistryHandler(reg *registry.AgentRegistry) *AgentRegistryHandler {
	return &AgentRegistryHandler{reg: reg}
}

func (h *AgentRegistryHandler) List(c *gin.Context) {
	result, err := h.reg.ListAll(c.Request.Context())
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *AgentRegistryHandler) Get(c *gin.Context) {
	source := c.Param("source")
	name := c.Param("name")
	detail, err := h.reg.Get(c.Request.Context(), source, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "unknown source") {
			NotFound(c, "agent")
		} else {
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, detail)
}

type cloneRequest struct {
	Source  string `json:"source" binding:"required"`
	Name    string `json:"name" binding:"required"`
	NewName string `json:"new_name"`
}

func (h *AgentRegistryHandler) Clone(c *gin.Context) {
	var req cloneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.NewName == "" {
		req.NewName = req.Name
	}
	info, err := h.reg.Clone(c.Request.Context(), req.Source, req.Name, req.NewName)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, info)
}

func (h *AgentRegistryHandler) Refresh(c *gin.Context) {
	source := c.Param("source")
	if err := h.reg.Refresh(c.Request.Context(), source); err != nil {
		InternalError(c, err)
		return
	}
	result, err := h.reg.ListAll(c.Request.Context())
	if err != nil {
		InternalError(c, err)
		return
	}
	agents := result[source]
	c.JSON(http.StatusOK, gin.H{"count": len(agents)})
}

type createCustomRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Content     string `json:"content" binding:"required"`
}

func (h *AgentRegistryHandler) CreateCustom(c *gin.Context) {
	var req createCustomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	info, err := h.reg.CreateCustom(c.Request.Context(), registry.CreateCustomAgentInput{
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
	})
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, info)
}

type updateCustomRequest struct {
	Description string `json:"description"`
	Content     string `json:"content" binding:"required"`
}

func (h *AgentRegistryHandler) UpdateCustom(c *gin.Context) {
	name := c.Param("name")
	var req updateCustomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.reg.UpdateCustom(c.Request.Context(), name, registry.UpdateCustomAgentInput{
		Description: req.Description,
		Content:     req.Content,
	}); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *AgentRegistryHandler) DeleteCustom(c *gin.Context) {
	name := c.Param("name")
	if err := h.reg.DeleteCustom(c.Request.Context(), name); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
