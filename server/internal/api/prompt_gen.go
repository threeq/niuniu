package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// PromptGenHandler handles AI prompt generation endpoints.
type PromptGenHandler struct {
	Svc *service.PromptGenService
}

// GeneratePrompt generates an agent role's system prompt.
func (h *PromptGenHandler) GeneratePrompt(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Name == "" || req.Description == "" {
		BadRequest(c, "name and description are required")
		return
	}

	result, err := h.Svc.GeneratePrompt(c.Request.Context(), req.Name, req.Description, req.Context)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// SuggestColumnOp generates or refines op_instruction + when_to_use for a kanban
// column via a one-shot Claude CLI subprocess (same auth path as suggest-goal-condition).
// When current_op_instruction or current_when_to_use are provided, the model
// improves the existing content rather than generating from scratch.
func (h *PromptGenHandler) SuggestColumnOp(c *gin.Context) {
	var req struct {
		ColumnName           string   `json:"column_name"`
		GateSpecNames        []string `json:"gate_spec_names"`
		CurrentOpInstruction string   `json:"current_op_instruction"`
		CurrentWhenToUse     string   `json:"current_when_to_use"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.ColumnName == "" {
		BadRequest(c, "column_name is required")
		return
	}

	if !agentproxy.ClaudeCLIAvailable() {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": gin.H{"code": "CLI_UNAVAILABLE", "message": "claude CLI not available"},
		})
		return
	}

	// Resolve the user's Claude account config dir — same pattern as
	// KanbanHandler.SuggestGoalCondition so both use the same credentials.
	userID := c.GetInt64("auth_user_id")
	configDir := ""

	result, err := agentproxy.SuggestColumnOpFields(
		c.Request.Context(),
		req.ColumnName,
		req.GateSpecNames,
		req.CurrentOpInstruction,
		req.CurrentWhenToUse,
		configDir,
	)
	if err != nil {
		if !agentproxy.ClaudeCLIAvailable() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{"code": "CLI_UNAVAILABLE", "message": "claude CLI not available"},
			})
			return
		}
		slog.Warn("SuggestColumnOp failed",
			"user_id", userID, "column", req.ColumnName,
			"error", err)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"code": "SUGGEST_FAILED", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// OptimizePrompt improves an existing prompt based on feedback.
func (h *PromptGenHandler) OptimizePrompt(c *gin.Context) {
	var req struct {
		CurrentPrompt string `json:"current_prompt"`
		Feedback      string `json:"feedback"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.CurrentPrompt == "" || req.Feedback == "" {
		BadRequest(c, "current_prompt and feedback are required")
		return
	}

	result, err := h.Svc.OptimizePrompt(c.Request.Context(), req.CurrentPrompt, req.Feedback)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
