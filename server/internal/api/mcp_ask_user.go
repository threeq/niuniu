package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// MCPAskUserHandler serves /mcp/ask-user-question — the niuniu-mcp shim
// posts here for every niuniu_ask_user_question call. Mirrors
// MCPPermissionHandler: blocks until the user answers in chat or the
// request hits its timeout.
type MCPAskUserHandler struct {
	Svc *service.AskUserService
	Q   *store.Queries
}

// Prompt is POST /mcp/ask-user-question.
//
// Request body: {"questions": [{question, header, multiSelect, options[]}]}
// Response body: AskUserResult ({"answered": true, "answers": [...]} or
// {"answered": false, "reason": "timeout"|"cancelled"}).
func (h *MCPAskUserHandler) Prompt(c *gin.Context) {
	var body struct {
		Questions []event.AskUserQuestion `json:"questions"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(body.Questions) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "questions array is required and must be non-empty"})
		return
	}
	for i, q := range body.Questions {
		if q.Question == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "questions[" + strconv.Itoa(i) + "].question is required"})
			return
		}
		if len(q.Options) < 2 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "questions[" + strconv.Itoa(i) + "].options must have at least 2 entries"})
			return
		}
		// Reject duplicate option labels and the niuniu-internal "__other__"
		// sentinel up front. Both would break the SPA's selection-set
		// semantics (set keyed on label) and the freeform-vs-real-option
		// disambiguation.
		seen := make(map[string]struct{}, len(q.Options))
		for j, opt := range q.Options {
			if opt.Label == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "questions[" + strconv.Itoa(i) + "].options[" + strconv.Itoa(j) + "].label is required"})
				return
			}
			if opt.Label == "__other__" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "questions[" + strconv.Itoa(i) + "].options[" + strconv.Itoa(j) + "].label '__other__' is reserved"})
				return
			}
			if _, dup := seen[opt.Label]; dup {
				c.JSON(http.StatusBadRequest, gin.H{"error": "questions[" + strconv.Itoa(i) + "].options have duplicate label: " + opt.Label})
				return
			}
			seen[opt.Label] = struct{}{}
		}
	}

	wsID := c.GetInt64("mcp_workspace_id")
	if wsID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no workspace context"})
		return
	}

	ws, err := h.Q.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace lookup: " + err.Error()})
		return
	}
	sessionID := ws.SessionID.String

	resp, err := h.Svc.Request(c.Request.Context(),
		wsID, ws.OwnerType, ws.OwnerID, sessionID, body.Questions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out, _ := json.Marshal(resp)
	c.Data(http.StatusOK, "application/json", out)
}

