package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// askUserOtherSentinel is the internal label the SPA's AskUserQuestionCard
// emits when the user picks the implicit "Other" choice (and supplies
// notes). The server strips this sentinel before persisting / forwarding
// to Claude so the sentinel stays niuniu-internal.
const askUserOtherSentinel = "__other__"

// AskUserHandler serves the chat-side REST endpoints that back the
// ask-user-question UI:
//   - List pending ask-user requests for a workspace
//   - Submit answers (decide)
type AskUserHandler struct {
	DB    *store.DB
	Svc   *service.AskUserService
	Authz *service.Authz
}

// askUserRequestDTO is the wire shape for one pending request. questions is
// decoded from the JSON column so the frontend does not double-parse.
type askUserRequestDTO struct {
	ID          int64                   `json:"id"`
	WorkspaceID int64                   `json:"workspace_id"`
	SessionID   string                  `json:"session_id"`
	Questions   []event.AskUserQuestion `json:"questions"`
	RequestedAt int64                   `json:"requested_at"` // unix ms
	ExpiresAt   int64                   `json:"expires_at"`   // unix ms
	Status      string                  `json:"status"`
}

// askUserAnswerBody mirrors service.AskUserAnswer in the request body.
type askUserAnswerBody struct {
	Question string   `json:"question"`
	Labels   []string `json:"labels"`
	Notes    string   `json:"notes,omitempty"`
}

// askUserDecideBody is the JSON body for POST /agent-ask-user-decisions/:requestID.
type askUserDecideBody struct {
	Answers []askUserAnswerBody `json:"answers"`
}

// ListByWorkspace handles GET /api/workspaces/:id/ask-user-requests
// and returns pending requests for the workspace.
func (h *AskUserHandler) ListByWorkspace(c *gin.Context) {
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
	rows, err := q.ListPendingAskUserRequestsByWorkspace(ctx, wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]askUserRequestDTO, 0, len(rows))
	for _, r := range rows {
		var questions []event.AskUserQuestion
		_ = json.Unmarshal([]byte(r.QuestionsJson), &questions)
		if questions == nil {
			questions = []event.AskUserQuestion{}
		}
		out = append(out, askUserRequestDTO{
			ID:          r.ID,
			WorkspaceID: r.WorkspaceID,
			SessionID:   r.SessionID,
			Questions:   questions,
			RequestedAt: r.RequestedAt.UnixMilli(),
			ExpiresAt:   r.ExpiresAt.UnixMilli(),
			Status:      r.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}

// Decide handles POST /api/agent-ask-user-decisions/:requestID
// — records the user's submitted answers and unblocks the waiting MCP call.
func (h *AskUserHandler) Decide(c *gin.Context) {
	reqID, err := strconv.ParseInt(c.Param("requestID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid request id")
		return
	}
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	if h.Authz != nil && userID > 0 {
		if aerr := h.Authz.CanAccessAskUserRequest(ctx, userID, reqID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	var body askUserDecideBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if len(body.Answers) == 0 {
		BadRequest(c, "answers must be non-empty")
		return
	}
	answers := make([]service.AskUserAnswer, 0, len(body.Answers))
	for i, a := range body.Answers {
		if a.Question == "" {
			BadRequest(c, "answers["+strconv.Itoa(i)+"].question is required")
			return
		}
		// The SPA marks the freeform-text path with the niuniu-internal
		// sentinel "__other__" so the radio/checkbox group can host an
		// "Other" choice. Strip it before the answer reaches the service
		// — Claude must never see niuniu-internal markers in its tool
		// result. The user's actual freeform text rides on `notes`.
		filtered := make([]string, 0, len(a.Labels))
		for _, l := range a.Labels {
			if l != askUserOtherSentinel {
				filtered = append(filtered, l)
			}
		}
		notes := a.Notes
		if len(filtered) == 0 && notes == "" {
			BadRequest(c, "answers["+strconv.Itoa(i)+"] must have at least one label or non-empty notes")
			return
		}
		answers = append(answers, service.AskUserAnswer{
			Question: a.Question,
			Labels:   filtered,
			Notes:    notes,
		})
	}

	if err := h.Svc.Decide(ctx, reqID, service.AskUserDecision{
		Answers: answers,
		UserID:  userID,
	}); err != nil {
		if errors.Is(err, service.ErrAskUserAlreadyDecided) {
			Conflict(c, "ask-user request already decided")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
