package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// CheckpointHandler exposes the autohost 安全网 hidden-ref checkpoint system over
// REST (issue-centric, authorized via issue -> column -> project) and over the MCP
// surface (workspace-scoped, so the agent needs only its own workspace id). Both
// call the same CheckpointService (see service/checkpoint.go).
type CheckpointHandler struct {
	svc    *service.CheckpointService
	kanban *service.KanbanService
	q      *store.Queries
	Authz  *service.Authz
}

// NewCheckpointHandler constructs the handler. Authz is set separately (like the
// other handlers) at wiring time.
func NewCheckpointHandler(svc *service.CheckpointService, kanban *service.KanbanService, q *store.Queries) *CheckpointHandler {
	return &CheckpointHandler{svc: svc, kanban: kanban, q: q}
}

// authzWorkspace checks the caller can access the workspace. Returns true on
// success (or when authz is disabled / no authenticated user — e.g. the MCP path,
// whose token auth is enforced upstream); writes an HTTP error and returns false.
func (h *CheckpointHandler) authzWorkspace(c *gin.Context, userID, workspaceID int64) bool {
	if userID <= 0 || h.Authz == nil {
		return true
	}
	if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
		writeAuthzError(c, aerr)
		return false
	}
	return true
}

// issueForWorkspace resolves the issue linked to a workspace (1:1). Writes an error
// and returns (0,false) when the workspace has no issue.
func (h *CheckpointHandler) issueForWorkspace(c *gin.Context, workspaceID int64) (int64, bool) {
	ws, err := h.q.GetWorkspace(c.Request.Context(), workspaceID)
	if err != nil {
		NotFound(c, "WORKSPACE")
		return 0, false
	}
	if !ws.IssueID.Valid {
		BadRequest(c, "workspace has no linked issue")
		return 0, false
	}
	return ws.IssueID.Int64, true
}

// RevertRequest is the revert body: the step to rewind every repo worktree to.
type RevertRequest struct {
	Step int `json:"step"`
}

// --- Workspace-scoped (served on both /api for the SPA and /mcp for the agent) ---
//
// Checkpoints are a per-worktree git concept, so the surface is bound to the
// WORKSPACE, not the issue: the handlers resolve the workspace's 1:1 issue
// internally (checkpoint rows are keyed by issue in the DB) but every entry point
// takes a workspace id. /api callers are authorized via CanAccessWorkspace; the
// /mcp twin relies on MCP token auth upstream (userID is 0, so authz is skipped).

// TimelineForWorkspace returns the checkpoint timeline for a workspace's issue.
// GET /api|/mcp /workspaces/:id/checkpoints
func (h *CheckpointHandler) TimelineForWorkspace(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if !h.authzWorkspace(c, userID, wsID) {
		return
	}
	issueID, ok := h.issueForWorkspace(c, wsID)
	if !ok {
		return
	}
	h.writeTimeline(c, issueID)
}

// DiffForWorkspace returns a single checkpoint's diff, validating it belongs to the
// workspace's issue. GET /api|/mcp /workspaces/:id/checkpoints/:cid/diff
func (h *CheckpointHandler) DiffForWorkspace(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if !h.authzWorkspace(c, userID, wsID) {
		return
	}
	issueID, ok := h.issueForWorkspace(c, wsID)
	if !ok {
		return
	}
	h.writeDiff(c, issueID)
}

// RevertForWorkspace rewinds the workspace's issue to a checkpoint step.
// POST /api|/mcp /workspaces/:id/checkpoints/revert  body: {"step":N}
func (h *CheckpointHandler) RevertForWorkspace(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if !h.authzWorkspace(c, userID, wsID) {
		return
	}
	issueID, ok := h.issueForWorkspace(c, wsID)
	if !ok {
		return
	}
	h.writeRevert(c, issueID)
}

// CreateForWorkspace takes a manual checkpoint of the workspace's worktree(s).
// POST /mcp/workspaces/:id/checkpoints  body: {"label":"..."}
func (h *CheckpointHandler) CreateForWorkspace(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if !h.authzWorkspace(c, userID, wsID) {
		return
	}
	issueID, ok := h.issueForWorkspace(c, wsID)
	if !ok {
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	_ = c.ShouldBindJSON(&body)
	res, err := h.svc.Snapshot(c.Request.Context(), issueID, wsID, service.CheckpointKindManual, body.Label, "")
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// --- shared bodies ---

func (h *CheckpointHandler) writeTimeline(c *gin.Context, issueID int64) {
	steps, err := h.svc.Timeline(c.Request.Context(), issueID)
	if err != nil {
		InternalError(c, err)
		return
	}
	if steps == nil {
		steps = []service.CheckpointStep{}
	}
	c.JSON(http.StatusOK, gin.H{"issue_id": issueID, "checkpoints": steps})
}

func (h *CheckpointHandler) writeDiff(c *gin.Context, issueID int64) {
	cid, err := strconv.ParseInt(c.Param("cid"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid checkpoint ID")
		return
	}
	owner, ok := h.svc.CheckpointIssueID(c.Request.Context(), cid)
	if !ok || owner != issueID {
		NotFound(c, "CHECKPOINT")
		return
	}
	diffs, err := h.svc.StepDiff(c.Request.Context(), cid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"checkpoint_id": cid, "files": diffs})
}

func (h *CheckpointHandler) writeRevert(c *gin.Context, issueID int64) {
	var req RevertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if req.Step <= 0 {
		BadRequest(c, "step must be a positive checkpoint step")
		return
	}
	results, err := h.svc.Revert(c.Request.Context(), issueID, req.Step)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"issue_id": issueID, "step": req.Step, "repos": results})
}
