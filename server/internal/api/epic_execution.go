package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// EpicExecutionHandler exposes the Executable Epic (E4) execution engine over
// REST. It authorizes via the kanban service (issue -> column -> project) using
// the same pattern as SetIssueExecFields.
type EpicExecutionHandler struct {
	svc    *service.EpicExecutionService
	kanban *service.KanbanService
	Authz  *service.Authz
}

func NewEpicExecutionHandler(svc *service.EpicExecutionService, kanban *service.KanbanService) *EpicExecutionHandler {
	return &EpicExecutionHandler{svc: svc, kanban: kanban}
}

// authzIssueProject resolves the issue's project and checks access. Returns true
// on success (or when authz is disabled); writes an HTTP error and returns false
// otherwise.
func (h *EpicExecutionHandler) authzIssueProject(c *gin.Context, userID, issueID int64) bool {
	if userID <= 0 || h.Authz == nil {
		return true
	}
	issue, ierr := h.kanban.GetIssue(c.Request.Context(), issueID)
	if ierr != nil {
		NotFound(c, "ISSUE")
		return false
	}
	col, cerr := h.kanban.GetColumn(c.Request.Context(), issue.ColumnID)
	if cerr != nil {
		NotFound(c, "COLUMN")
		return false
	}
	if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, col.ProjectID); aerr != nil {
		writeAuthzError(c, aerr)
		return false
	}
	return true
}

// EpicProgressResponse is the wire format for epic execution progress.
type EpicProgressResponse struct {
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	ExecStatus string `json:"exec_status"`
}

// StartWorkspaceResponse is the start_workspace result (orchestration dispatch).
// When the owner is at its concurrent-workspace cap the request is queued
// (Queued=true, WorkspaceID=0) and starts later when a slot frees. Warn carries a
// near-budget advisory so the orchestration agent can ask_user before continuing.
type StartWorkspaceResponse struct {
	WorkspaceID   int64   `json:"workspace_id"`
	Queued        bool    `json:"queued,omitempty"`
	QueuePosition int     `json:"queue_position,omitempty"`
	Warn          string  `json:"warn,omitempty"`
	SpentUSD      float64 `json:"spent_usd,omitempty"`
	BudgetUSD     float64 `json:"budget_usd,omitempty"`
}

// StartWorkspace creates a workspace for the given issue (mode-B child dispatch).
// Backs the start_workspace MCP tool and POST /api/issues/:id/start-workspace.
func (h *EpicExecutionHandler) StartWorkspace(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if !h.authzIssueProject(c, userID, issueID) {
		return
	}
	out, err := h.svc.StartWorkspaceForIssue(c.Request.Context(), issueID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, StartWorkspaceResponse{
		WorkspaceID:   out.WorkspaceID,
		Queued:        out.Queued,
		QueuePosition: out.QueuePosition,
		Warn:          out.Warn,
		SpentUSD:      out.SpentUSD,
		BudgetUSD:     out.BudgetUSD,
	})
}

// AdvanceIssueRequest is the POST /mcp/issues/:id/advance body.
type AdvanceIssueRequest struct {
	ToColumn string `json:"to_column"`
	Reason   string `json:"reason"`
}

// AbandonIssueRequest is the POST /mcp/issues/:id/abandon body.
type AbandonIssueRequest struct {
	Reason string `json:"reason"`
}

// RequestChangesRequest is the POST .../request-changes body (Review 闭环 #623).
type RequestChangesRequest struct {
	Comment string `json:"comment"`
	Author  string `json:"author"`
}

// RequestChanges is the reviewer "需修改 / 打回" entry of the Review 闭环 (#623): it
// records the issue-level review comment and bounces the card back to the implement
// lane, where AdvanceIssue injects the two-layer review context (kanban issue
// comments + unresolved diff comments) into the agent's continuation. Backs the
// request_changes MCP tool and POST /api/issues/:id/request-changes.
func (h *EpicExecutionHandler) RequestChanges(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if !h.authzIssueProject(c, userID, issueID) {
		return
	}
	var req RequestChangesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	res, err := h.svc.RequestChanges(c.Request.Context(), service.RequestChangesInput{
		IssueID:      issueID,
		Comment:      req.Comment,
		Author:       req.Author,
		CallerUserID: userID,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// AdvanceIssue moves an issue to another column and, when the destination is an
// `instruct` column, ensures its workspace and sends the column's instruction to
// the agent (spec §6). Backs the advance_issue MCP tool. Skips / back-tracks are
// allowed. POST /mcp/issues/:id/advance  body: {"to_column":"<id|name>","reason":""}
func (h *EpicExecutionHandler) AdvanceIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if !h.authzIssueProject(c, userID, issueID) {
		return
	}
	var req AdvanceIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if strings.TrimSpace(req.ToColumn) == "" {
		BadRequest(c, "to_column is required")
		return
	}
	res, err := h.svc.AdvanceIssue(c.Request.Context(), service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     req.ToColumn,
		Reason:       req.Reason,
		CallerUserID: userID,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// AbandonIssue parks an issue back in the backlog with a reason and marks it
// abandoned (spec §19, abandoned-with-reason). Backs the abandon_issue MCP tool.
// POST /mcp/issues/:id/abandon  body: {"reason":"..."}
func (h *EpicExecutionHandler) AbandonIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if !h.authzIssueProject(c, userID, issueID) {
		return
	}
	var req AbandonIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		BadRequest(c, "reason is required")
		return
	}
	res, err := h.svc.AbandonIssue(c.Request.Context(), service.AbandonIssueInput{
		IssueID:      issueID,
		Reason:       req.Reason,
		CallerUserID: userID,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// MergeToMain asks the epic's control-workspace agent to merge the epic feature
// branch into the repos' default branches. It does NOT git-merge in the backend.
// Requires the issue to be an epic whose exec_status is 'done' (review complete).
// POST /api/issues/:id/merge-to-main
func (h *EpicExecutionHandler) MergeToMain(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if !h.authzIssueProject(c, userID, issueID) {
		return
	}
	issue, err := h.kanban.GetIssue(c.Request.Context(), issueID)
	if err != nil {
		NotFound(c, "ISSUE")
		return
	}
	if issue.IssueType != "epic" {
		BadRequest(c, "issue is not an epic")
		return
	}
	if issue.ExecStatus != "done" {
		BadRequest(c, "epic must be 'done' (review complete) before merging to main")
		return
	}
	if err := h.svc.RequestMergeToMain(c.Request.Context(), issueID); err != nil {
		BadRequest(c, err.Error())
		return
	}
	done, total, execStatus, err := h.svc.GetEpicProgress(c.Request.Context(), issueID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, EpicProgressResponse{Done: done, Total: total, ExecStatus: execStatus})
}

// GetEpicProgress returns derived execution progress for an Epic.
// GET /api/issues/:id/epic-progress
func (h *EpicExecutionHandler) GetEpicProgress(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if !h.authzIssueProject(c, userID, issueID) {
		return
	}
	done, total, execStatus, err := h.svc.GetEpicProgress(c.Request.Context(), issueID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, EpicProgressResponse{Done: done, Total: total, ExecStatus: execStatus})
}
