package api

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/registry"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// HarnessHandler handles all harness-related API endpoints.
type HarnessHandler struct {
	Svc          *service.HarnessService
	Registry     *registry.AgentRegistry
	Proxy        *agentproxy.AgentProxy
	Q            *store.Queries
	DB           *sql.DB // raw DB for fields not in sqlc model (triggered_by)
	WorkspaceSvc *service.WorkspaceService
	MCPWriter    *service.MCPConfigGenerator
	Authz        *service.Authz
}

// ---- Spec endpoints ----

// GET /api/harness/specs
func (h *HarnessHandler) ListGlobalSpecs(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var err error
	specs, err := h.Svc.ListGlobalSpecs(c.Request.Context())
	if userID > 0 {
		specs, err = h.Svc.ListGlobalSpecsForUser(c.Request.Context(), userID)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, specs)
}

// GET /api/harness/specs/resolve
func (h *HarnessHandler) ResolveForProject(c *gin.Context) {
	specs, err := h.Svc.ResolveForProject(c.Request.Context(), nil)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, specs)
}

// GET /api/harness/specs/:id
func (h *HarnessHandler) GetSpec(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid spec ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessHarnessSpec(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	spec, err := h.Q.GetHarnessSpec(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, spec)
}

// POST /api/harness/specs
func (h *HarnessHandler) CreateSpec(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var input service.CreateSpecInput
	if err := c.ShouldBindJSON(&input); err != nil {
		BadRequest(c, err.Error())
		return
	}
	// harness specs are a single GLOBAL engineering-standards library — they are
	// not owned by a user or org. Every spec is created under the global sentinel
	// (owner_type='user', owner_id=0); the per-kanban relationship lives in
	// column_gate_specs, not on the spec itself. Mirrors the global-default edit
	// path (CanAccessHarnessSpec special-cases owner_id=0 for all callers).
	_ = userID
	spec, err := h.Svc.CreateSpec(c.Request.Context(), input, "user", 0)
	if err != nil {
		// validateSpecInput returns invalid-kind / invalid-threshold-op errors
		// that are user input problems, not server faults — map to 400.
		if isSpecValidationError(err) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, spec)
}

// isSpecValidationError surfaces validateSpec's typed sentinels as 400 so the
// SPA can render the message inline instead of a generic 500.
func isSpecValidationError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, service.ErrInvalidKind) ||
		errors.Is(err, service.ErrInvalidThresholdOp) ||
		errors.Is(err, service.ErrInvalidPattern) ||
		errors.Is(err, service.ErrInvalidExtractRegex) ||
		errors.Is(err, service.ErrInvalidFilePaths) ||
		errors.Is(err, service.ErrUnsupportedScope) ||
		errors.Is(err, service.ErrUnsupportedTriggerScope)
}

// PUT /api/harness/specs/:id
// callerIsOwnerAdmin reports whether userID is an admin of the spec's owner: the
// owner themself for a personal ('user') spec, or an org owner/admin for an org spec.
func (h *HarnessHandler) callerIsOwnerAdmin(c *gin.Context, userID int64, owner service.OwnerRef) bool {
	// harness_specs are global (owner sentinel user,0): lowering a floor-bound
	// global spec's severity is a deployment-wide change → require global admin.
	if owner.Type == "user" && owner.ID == 0 {
		return h.Authz.IsGlobalAdmin(c.Request.Context(), userID)
	}
	switch owner.Type {
	case "user":
		return owner.ID == userID
	case "org":
		return h.Authz.CanManageOrg(c.Request.Context(), userID, owner.ID) == nil
	default:
		return false
	}
}

func (h *HarnessHandler) UpdateSpec(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid spec ID")
		return
	}
	var owner service.OwnerRef
	if userID > 0 && h.Authz != nil {
		o, aerr := h.Authz.CanAccessHarnessSpec(c.Request.Context(), userID, id)
		if aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
		owner = o
	}
	var input service.UpdateSpecInput
	if err := c.ShouldBindJSON(&input); err != nil {
		BadRequest(c, err.Error())
		return
	}
	// Floor-config write permission (spec §18): downgrading the severity of a spec
	// that is bound as a project floor (applicability='always') from 'error' would
	// architect-away the floor, so it requires org owner/admin (personal owners pass).
	if userID > 0 && h.Authz != nil {
		cur, cerr := h.Svc.CurrentSeverity(c.Request.Context(), id)
		if cerr == nil && cur == "error" && input.Severity != "error" {
			floor, ferr := h.Svc.IsSpecBoundAsFloor(c.Request.Context(), id)
			if ferr != nil {
				InternalError(c, ferr)
				return
			}
			if floor && !h.callerIsOwnerAdmin(c, userID, owner) {
				RespondError(c, http.StatusForbidden, "ADMIN_REQUIRED",
					"only an org owner/admin may lower the severity of a spec bound as a project floor")
				return
			}
		}
	}
	if err := h.Svc.UpdateSpec(c.Request.Context(), id, input); err != nil {
		if isSpecValidationError(err) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// DELETE /api/harness/specs/:id
func (h *HarnessHandler) DeleteSpec(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid spec ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessHarnessSpec(c.Request.Context(), userID, id); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	if err := h.Svc.DeleteSpec(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// POST /api/harness/specs/:id/test
// On-demand spec execution. Resolves the spec, mirrors it into the in-memory
// harness.Spec shape, and dispatches the registered checker once with the
// caller-supplied inputs. Returns a single CheckResult — the runner skips
// disabled specs and specs without a registered checker, so we handle both
// by returning a synthesized "skip" status when no result comes back.
func (h *HarnessHandler) TestSpec(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid spec ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessHarnessSpec(c.Request.Context(), userID, id); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	var body struct {
		CommitMessage string `json:"commit_message"`
		BranchName    string `json:"branch_name"`
		AgentOutput   string `json:"agent_output"`
	}
	// Body is optional — ignore parse errors when content-length is zero.
	// NOTE: workspace_path is intentionally NOT accepted from the request body.
	// Allowing the caller to choose a working directory for command-* checkers
	// would let a low-privilege user pivot a trusted spec command into running
	// at any host path (e.g. /etc, C:\Windows) and exfiltrate the captured
	// output. On-demand tests run in an empty WorkspacePath, which makes
	// path-sensitive checks (file_exists, command-exit-code with cmd.Dir set)
	// safely behave as if rooted at the server process cwd. Phase-exit gates
	// (the real workflow path) resolve workspace_path via OwnerRef, which
	// already gates on workspace ownership.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			BadRequest(c, err.Error())
			return
		}
	}

	row, err := h.Q.GetHarnessSpec(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}

	// Mirror the store row into harness.Spec. The runner skips !Enabled
	// specs unconditionally — for an explicit "test this rule" endpoint we
	// want execution regardless, so we force Enabled=true here.
	spec := harness.Spec{
		ID:               row.ID,
		Scope:            "global", // harness_specs is a global library
		Category:         row.Category,
		Name:             row.Name,
		Enabled:          true,
		Severity:         row.Severity,
		Config:           row.Config,
		Kind:             row.Kind,
		Target:           row.Target,
		Pattern:          row.Pattern,
		PatternFlags:     row.PatternFlags,
		Command:          row.Command,
		TimeoutSec:       int(row.TimeoutSec),
		ExpectedExitCode: int(row.ExpectedExitCode),
		ExtractRegex:     row.ExtractRegex,
		ThresholdValue:   row.ThresholdValue,
		ThresholdOp:      row.ThresholdOp,
		FilePaths:        row.FilePaths,
		TriggerOn:        row.TriggerOn,
	}

	// Use RunSingle (not RunAll): always returns one CheckResult, doesn't
	// filter out "no checker" skips, and stays decoupled from RunAll's
	// gate-result aggregation semantics.
	runner := h.Svc.CheckRunner()
	result := runner.RunSingle(c.Request.Context(), spec, harness.CheckEnv{
		CommitMessage: body.CommitMessage,
		BranchName:    body.BranchName,
		AgentOutput:   body.AgentOutput,
	})
	c.JSON(http.StatusOK, result)
}

// PreCommitCheckRequest carries the agent's pre-commit context. Reused by
// the HTTP-side handler (/api/workspaces/:id/harness/pre-commit-check) and
// the MCP-side handler (/mcp/workspaces/:id/harness/pre-commit-check) that
// the niuniu-mcp `harness_pre_commit_check` tool wraps.
type PreCommitCheckRequest struct {
	CommitMessage string `json:"commit_message"`
	BranchName    string `json:"branch_name"`
	StagedDiff    string `json:"staged_diff,omitempty"`
}

// PreCommitCheckResponse is the aggregate verdict returned to the agent.
//
// blocked is true when any spec with severity='error' produced status='fail';
// the agent should refuse to commit. severity='warning' fails surface in
// `results` but do not flip `blocked`. severity='info' fails are silent in
// the boolean verdict but still recorded.
type PreCommitCheckResponse struct {
	Blocked bool                  `json:"blocked"`
	Results []harness.CheckResult `json:"results"`
}

// POST /api/workspaces/:id/harness/pre-commit-check (also /mcp/...)
//
// Runs every spec with trigger_on='pre_commit' visible to the caller's
// project against the supplied commit context, persists each result into
// harness_checks, and returns the aggregate verdict.
//
// Agent flow: niuniu-mcp registers `harness_pre_commit_check` MCP tool;
// the agent calls it before `git commit`. If blocked=true the agent
// explains the failures to the user and waits for confirmation rather
// than committing.
func (h *HarnessHandler) PreCommitCheck(c *gin.Context) {
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

	// Cap request body at 1MB. The body carries a staged_diff string that
	// gets forwarded to AI judges; without a cap a malicious caller could
	// pipe a 50MB diff and burn ~$12 per call against an enabled ai_judge
	// spec. Agents that need to evaluate larger changesets should summarise
	// before sending.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)

	var body PreCommitCheckRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			BadRequest(c, err.Error())
			return
		}
	}

	allSpecs, err := h.Svc.ResolveForProject(c.Request.Context(), nil)
	if err != nil {
		InternalError(c, err)
		return
	}

	preCommitSpecs := make([]harness.Spec, 0, len(allSpecs))
	for _, s := range allSpecs {
		if !s.Enabled {
			continue
		}
		if s.TriggerOn != harness.TriggerPreCommit {
			continue
		}
		preCommitSpecs = append(preCommitSpecs, s)
	}

	// Resolve workspace's working directory so command-* specs run in the
	// user's git checkout, not the niuniu server cwd. CanAccessWorkspace
	// already validated access; we just read the row for its `path`.
	wsRow, wsErr := h.Q.GetWorkspace(c.Request.Context(), workspaceID)
	workspacePath := ""
	if wsErr == nil {
		workspacePath = wsRow.Path
	}

	results := h.Svc.CheckRunner().RunAll(c.Request.Context(), preCommitSpecs, harness.CheckOpts{
		CommitMessage: body.CommitMessage,
		BranchName:    body.BranchName,
		AgentOutput:   body.StagedDiff, // diff sits in the same slot as agent_output for now
		Phase:         "pre_commit",
		WorkspacePath: workspacePath,
	})

	// Detect specs that produced no result (no checker registered for the
	// spec's kind+name). Surface them as explicit errors instead of silent
	// skips so the agent sees the misconfiguration.
	seen := make(map[int64]bool, len(results))
	for _, r := range results {
		seen[r.SpecID] = true
	}
	for _, s := range preCommitSpecs {
		if !seen[s.ID] {
			results = append(results, harness.CheckResult{
				SpecID:  s.ID,
				Status:  "error",
				Message: fmt.Sprintf("no checker registered for kind=%q name=%q — spec was filtered to pre_commit but cannot execute", s.Kind, s.Name),
			})
		}
	}

	// Persist each result (including the synthetic errors above) so
	// post-mortems can attribute pre-commit blocks. Skip rows are dropped
	// to avoid table bloat; they're already logged by the runner.
	for _, r := range results {
		if r.Status == "skip" {
			continue
		}
		_, perr := h.Q.CreateHarnessCheck(c.Request.Context(), store.CreateHarnessCheckParams{
			WorkspaceID: workspaceID,
			RunID:       sql.NullInt64{},
			SpecID:      r.SpecID,
			PhaseName:   "pre_commit",
			Status:      r.Status,
			Message:     r.Message,
			Details:     r.Details,
			DurationMs:  r.DurationMs,
			CostUsd:     r.CostUSD,
		})
		if perr != nil {
			slog.Warn("persist pre-commit check failed", "spec_id", r.SpecID, "error", perr)
		}
	}

	blocked := h.Svc.CheckRunner().HasBlockingFailure(preCommitSpecs, results)
	c.JSON(http.StatusOK, PreCommitCheckResponse{
		Blocked: blocked,
		Results: results,
	})
}

// POST /api/workspaces/:id/harness/gate-check
// Returns existing check results for the workspace. In future this will
// trigger live gate checks; for now the Agent calls this after the pipeline
// runner has already executed checks.
func (h *HarnessHandler) RunGateCheck(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	checks, err := h.Q.ListHarnessChecksByWorkspace(c.Request.Context(), workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}
	hasBlocking := false
	for _, ch := range checks {
		if ch.Status == "fail" && ch.Severity == "error" {
			hasBlocking = true
			break
		}
	}
	slog.Info("harness: RunGateCheck result", "workspaceID", workspaceID, "checkCount", len(checks), "blocking", hasBlocking)
	c.JSON(http.StatusOK, map[string]any{"checks": toHarnessCheckResponses(checks), "blocking": hasBlocking})
}

// GET /api/workspaces/:id/harness/checks
func (h *HarnessHandler) ListChecks(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	checks, err := h.Q.ListHarnessChecksByWorkspace(c.Request.Context(), workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toHarnessCheckResponses(checks))
}
