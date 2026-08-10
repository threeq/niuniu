package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AssistantHandler powers the conversational "office assistant" entry point
// (#388): a non-technical user describes a task in one sentence and the backend
// transparently provisions everything the agent needs — a backing issue, a
// no-repo workspace (#383), and an autohost goal_condition that lets the agent
// self-judge completion via the [AUTOHOST_DONE] sentinel. The kanban remains the
// source of truth (the chat UI is just a friendlier surface over it).
type AssistantHandler struct {
	Kanban    *service.KanbanService
	Workspace *service.WorkspaceService
	Project   *service.ProjectService
	Authz     *service.Authz
	Q         *store.Queries
	// Proxy forwards a dispatched message into the target plan's agent session.
	Proxy *agentproxy.AgentProxy
	// Router classifies whether an incoming message continues an existing plan
	// or starts a new one (nil = always-new fallback).
	Router *service.AssistantRouter
	// Blueprint attaches the office-doc scene as the assistant project's default,
	// so assistant workspaces auto-mount the office skills + persona + the
	// deliverable-manifest guidance.
	Blueprint *service.ProjectBlueprintService
	// dispatch is the project-parameterized routing core (W2). createPlan
	// delegates issue+workspace provisioning here so the WebUI assistant and the
	// IM inbound pipeline share one code path (RouteInProject).
	dispatch *service.AssistantDispatchService
	// db is the raw connection used to stamp triggered_by on managed-task
	// schedules (the same field the schedule handler persists for membership
	// enforcement). Optional; best-effort when nil.
	db *sql.DB
	// scheduleChanged registers a freshly created managed-task schedule with the
	// running scheduler (wired in server.go to scheduler.OnScheduleChanged).
	scheduleChanged func(scheduleID int64, deleted bool)
}

// SetDB wires the raw DB connection used to stamp triggered_by on managed-task
// schedules.
func (h *AssistantHandler) SetDB(db *sql.DB) { h.db = db }

// SetScheduleChanged registers the callback that hands a new managed-task
// schedule to the scheduler so it begins firing immediately.
func (h *AssistantHandler) SetScheduleChanged(fn func(scheduleID int64, deleted bool)) {
	h.scheduleChanged = fn
}

func NewAssistantHandler(
	kanban *service.KanbanService,
	workspace *service.WorkspaceService,
	project *service.ProjectService,
	authz *service.Authz,
	q *store.Queries,
	proxy *agentproxy.AgentProxy,
	router *service.AssistantRouter,
	blueprint *service.ProjectBlueprintService,
) *AssistantHandler {
	return &AssistantHandler{
		Kanban:    kanban,
		Workspace: workspace,
		Project:   project,
		Authz:     authz,
		Q:         q,
		Proxy:     proxy,
		Router:    router,
		Blueprint: blueprint,
		dispatch:  service.NewAssistantDispatchService(kanban, workspace, q, router),
	}
}

// assistantSceneSlug is the office scene auto-mounted on assistant workspaces —
// it carries the document skills, the non-technical persona, and the
// deliverable-manifest (.niuniu/artifacts.json) instruction.
const assistantSceneSlug = "office-doc"

// assistantFileBatchSceneSlug is the scene-gated file-batch capability the
// assistant also mounts by default so the agent can organize / rename / move
// local files on request (high-risk ops still pass a confirmation gate).
const assistantFileBatchSceneSlug = "file-batch"

// assistantProjectName is the well-known display name of the per-owner project
// that backs the conversational assistant. All assistant-created issues land
// here so the kanban view ("📋 看板" toggle) still shows the full truth.
const assistantProjectName = "牛牛助手"

// AssistantQuickCreateRequest is the one-sentence-in payload. Only Description
// is required; Title is derived from it when omitted.
type AssistantQuickCreateRequest struct {
	Description string                       `json:"description" binding:"required"`
	Title       string                       `json:"title,omitempty"`
	Owner       *CreateWorkspaceOwnerRequest `json:"owner,omitempty"`
}

// AssistantQuickCreateResponse hands the SPA the ids it needs to subscribe to
// the agent stream (agent-sse-store) and to surface the "幕后" provenance chips.
type AssistantQuickCreateResponse struct {
	IssueID     int64  `json:"issue_id"`
	WorkspaceID int64  `json:"workspace_id"`
	ProjectID   int64  `json:"project_id"`
	ColumnID    int64  `json:"column_id"`
	IssueTitle  string `json:"issue_title"`
}

// AssistantPlanDTO is one plan (an issue + its no-repo workspace) in the
// assistant's project — the unit the multi-plan UI lists, switches between, and
// deletes.
type AssistantPlanDTO struct {
	IssueID     int64  `json:"issue_id"`
	WorkspaceID int64  `json:"workspace_id"`
	ProjectID   int64  `json:"project_id"`
	ColumnID    int64  `json:"column_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	UpdatedAt   int64  `json:"updated_at"`
	// ParentIssueID is the top-level conversation a plan belongs to (0 = a
	// top-level task itself). Tasks created implicitly mid-conversation become
	// children of the active conversation so the rail can group them.
	ParentIssueID int64 `json:"parent_issue_id"`
	// Schedules are the cron managed-tasks bound to this plan's workspace (empty
	// when none). The composer surfaces them so the user knows the task has a
	// scheduled run.
	Schedules []AssistantPlanScheduleDTO `json:"schedules,omitempty"`
}

// AssistantPlanScheduleDTO is one cron schedule bound to a plan's workspace.
type AssistantPlanScheduleDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	CronExpr string `json:"cron_expr"`
	Enabled  bool   `json:"enabled"`
}

// AssistantDispatchRequest is the multi-plan entry point: one message that the
// backend routes to an existing plan or a new one.
type AssistantDispatchRequest struct {
	Description  string                       `json:"description" binding:"required"`
	ActivePlanID int64                        `json:"active_plan_id,omitempty"`
	ForceNew     bool                         `json:"force_new,omitempty"`
	Owner        *CreateWorkspaceOwnerRequest `json:"owner,omitempty"`
}

// AssistantDispatchResponse tells the SPA which plan the message landed in and
// whether it was freshly created (so the UI can append + select it).
type AssistantDispatchResponse struct {
	Plan  AssistantPlanDTO `json:"plan"`
	IsNew bool             `json:"is_new"`
}

// QuickCreate: POST /api/assistant/quick-create
//
// Steps: resolve owner → find-or-create the per-owner 办公助手 project → create an
// issue in its first column with goal_condition = description → create a no-repo
// workspace bound to that issue. The new workspace is seeded with autohost as its
// permission mode (WorkspaceService.Create default), and autohost reads the
// issue's goal_condition, so the agent runs to completion on its own.
func (h *AssistantHandler) QuickCreate(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()

	var req AssistantQuickCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		BadRequest(c, "description is required")
		return
	}

	// Resolve owner: explicit owner wins, otherwise the caller's personal space.
	owner := service.OwnerRef{Type: "user", ID: userID}
	if req.Owner != nil && req.Owner.Type != "" {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(ctx, userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	plan, err := h.createPlan(ctx, userID, owner, description, req.Title, 0, c.GetHeader("X-Niuniu-Language"))
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusCreated, AssistantQuickCreateResponse{
		IssueID:     plan.IssueID,
		WorkspaceID: plan.WorkspaceID,
		ProjectID:   plan.ProjectID,
		ColumnID:    plan.ColumnID,
		IssueTitle:  plan.Title,
	})
}

// createPlan provisions one plan: find-or-create the assistant project, create
// the backing issue with its goal_condition (autohost self-completion), and a
// no-repo workspace bound to it. Shared by QuickCreate and Dispatch(new).
// parentIssueID (>0) nests the plan under a top-level conversation so the rail
// can group it; 0 makes it a top-level task. The caller must already have
// resolved a top-level parent (CreateIssue caps the hierarchy at two levels).
func (h *AssistantHandler) createPlan(ctx context.Context, userID int64, owner service.OwnerRef, description, titleHint string, parentIssueID int64, language string) (AssistantPlanDTO, error) {
	projectID, columnID, err := h.ensureAssistantProject(ctx, userID, owner)
	if err != nil {
		return AssistantPlanDTO{}, err
	}

	var createdBy *int64
	if userID > 0 {
		createdBy = &userID
	}
	// Delegate issue+workspace provisioning to the generalized routing core so
	// the assistant project (NoRepo) and IM-bound projects share one path. The
	// assistant is always a no-repo office workspace.
	target, err := h.dispatch.CreatePlanInProject(ctx, owner, projectID, columnID,
		description, titleHint, parentIssueID, service.PlanCreateOpts{
			Language:        language,
			NoRepo:          true,
			ClaudeAccountID: resolveDefaultClaudeAccount(ctx, h.Q, owner.Type, owner.ID),
			CreatedBy:       createdBy,
		})
	if err != nil {
		return AssistantPlanDTO{}, err
	}

	// Re-read the workspace status for the DTO (CreatePlanInProject returns the
	// routing target, not the workspace row).
	status := "running"
	if ws, gerr := h.Q.GetWorkspace(ctx, target.WorkspaceID); gerr == nil {
		status = ws.Status
	}
	return AssistantPlanDTO{
		IssueID:       target.IssueID,
		WorkspaceID:   target.WorkspaceID,
		ProjectID:     projectID,
		ColumnID:      columnID,
		Title:         target.Title,
		Status:        status,
		ParentIssueID: parentIssueID,
	}, nil
}

// ensureAssistantProject returns the (projectID, firstColumnID) of the owner's
// 办公助手 project, creating it (with the default kanban columns) on first use.
func (h *AssistantHandler) ensureAssistantProject(ctx context.Context, userID int64, owner service.OwnerRef) (int64, int64, error) {
	projectID, columnID, err := h.resolveAssistantProject(ctx, userID, owner)
	if err != nil {
		return 0, 0, err
	}
	// Attach office-doc + file-batch as the project's default scenes so
	// workspaces created under it auto-mount the office skills + non-technical
	// persona + the deliverable-manifest guidance, plus the scene-gated
	// file-batch capability. Idempotent; best-effort so a missing scene never
	// blocks task creation. Also self-heals projects created before this.
	if h.Blueprint != nil {
		if aerr := h.Blueprint.AttachScenesToProject(ctx, projectID, owner,
			[]service.BlueprintScene{{Slug: assistantSceneSlug}, {Slug: assistantFileBatchSceneSlug}}); aerr != nil {
			slog.Warn("assistant: attach default scenes failed", "project", projectID, "err", aerr)
		}
	}
	return projectID, columnID, nil
}

// resolveAssistantProject find-or-creates the owner's 牛牛助手 project and
// returns its (projectID, firstColumnID).
func (h *AssistantHandler) resolveAssistantProject(ctx context.Context, userID int64, owner service.OwnerRef) (int64, int64, error) {
	// Reuse an existing assistant project if present.
	if projectID, columnID, found, err := h.findAssistantProject(ctx, userID, owner); err != nil {
		return 0, 0, err
	} else if found {
		return projectID, columnID, nil
	}

	// None yet — create it with the standard columns.
	project, err := h.Kanban.CreateProjectWithDefaults(ctx,
		assistantProjectName, "对话式牛牛助手自动创建的任务看板", owner.Type, owner.ID)
	if err != nil {
		// A same-named project already exists for this owner (a concurrent
		// create raced ahead of findAssistantProject) — reuse it. The lookup is
		// owner-scoped, so a different owner's project never lands here.
		if errors.Is(err, service.ErrProjectNameExists) && h.Q != nil {
			existing, gerr := h.Q.GetProjectByOwnerAndName(ctx, store.GetProjectByOwnerAndNameParams{
				OwnerType: owner.Type,
				OwnerID:   owner.ID,
				Name:      assistantProjectName,
			})
			if gerr == nil {
				col, cerr := h.firstColumn(ctx, existing.ID)
				if cerr != nil {
					return 0, 0, cerr
				}
				return existing.ID, col, nil
			}
		}
		return 0, 0, err
	}
	col, err := h.firstColumn(ctx, project.ID)
	if err != nil {
		return 0, 0, err
	}
	return project.ID, col, nil
}

// findAssistantProject locates the owner's 办公助手 project without creating it.
// found=false means the owner has no assistant project yet.
func (h *AssistantHandler) findAssistantProject(ctx context.Context, userID int64, owner service.OwnerRef) (projectID, columnID int64, found bool, err error) {
	if h.Project == nil || userID <= 0 {
		return 0, 0, false, nil
	}
	projects, err := h.Project.ListForUser(ctx, userID)
	if err != nil {
		return 0, 0, false, err
	}
	for _, p := range projects {
		if p.OwnerType == owner.Type && p.OwnerID == owner.ID && p.Name == assistantProjectName {
			col, cerr := h.firstColumn(ctx, p.ID)
			if cerr != nil {
				return 0, 0, false, cerr
			}
			return p.ID, col, true, nil
		}
	}
	return 0, 0, false, nil
}

// firstColumn returns the id of the lowest-position column of a project (the
// "待办" lane in the default seed) — where assistant issues are parked.
func (h *AssistantHandler) firstColumn(ctx context.Context, projectID int64) (int64, error) {
	columns, err := h.Kanban.ListColumns(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, errors.New("assistant project has no columns")
	}
	first := columns[0]
	for _, col := range columns[1:] {
		if col.Position < first.Position {
			first = col
		}
	}
	return first.ID, nil
}

// resolveAssistantOwner derives the effective owner from the request, falling
// back to the caller's personal space, and enforces write access.
func (h *AssistantHandler) resolveAssistantOwner(c *gin.Context, userID int64, reqOwner *CreateWorkspaceOwnerRequest) (service.OwnerRef, bool) {
	owner := service.OwnerRef{Type: "user", ID: userID}
	if reqOwner != nil && reqOwner.Type != "" {
		owner = service.OwnerRef{Type: reqOwner.Type, ID: reqOwner.ID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return owner, false
		}
	}
	return owner, true
}

// listPlans returns the owner's plans (issues in the assistant project, each
// with its no-repo workspace), newest-activity first. Empty when the owner has
// no assistant project yet.
func (h *AssistantHandler) listPlans(ctx context.Context, userID int64, owner service.OwnerRef) ([]AssistantPlanDTO, error) {
	projectID, columnID, found, err := h.findAssistantProject(ctx, userID, owner)
	if err != nil || !found {
		return nil, err
	}
	issues, err := h.Kanban.ListIssuesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	plans := make([]AssistantPlanDTO, 0, len(issues))
	for _, issue := range issues {
		dto := AssistantPlanDTO{
			IssueID:   issue.ID,
			ProjectID: projectID,
			ColumnID:  columnID,
			Title:     issue.Title,
			UpdatedAt: issue.UpdatedAt.Unix(),
		}
		if issue.ParentIssueID.Valid {
			dto.ParentIssueID = issue.ParentIssueID.Int64
		}
		// A plan must have its backing workspace; skip orphan issues.
		workspaces, werr := h.Workspace.GetWorkspacesByIssue(ctx, issue.ID)
		if werr != nil {
			return nil, werr
		}
		if len(workspaces) == 0 {
			continue
		}
		ws := workspaces[0]
		dto.WorkspaceID = ws.ID
		dto.Status = ws.Status
		if ws.UpdatedAt.Unix() > dto.UpdatedAt {
			dto.UpdatedAt = ws.UpdatedAt.Unix()
		}
		// Surface any cron schedules bound to this plan's workspace so the UI
		// can show a "定时任务" indicator. Best-effort: a query error just omits them.
		if h.Q != nil {
			if scheds, serr := h.Q.ListSchedules(ctx, ws.ID); serr == nil {
				for _, sc := range scheds {
					dto.Schedules = append(dto.Schedules, AssistantPlanScheduleDTO{
						ID:       sc.ID,
						Name:     sc.Name,
						CronExpr: sc.CronExpr,
						Enabled:  sc.Enabled != 0,
					})
				}
			}
		}
		plans = append(plans, dto)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].UpdatedAt > plans[j].UpdatedAt })
	return plans, nil
}

// ListPlans: GET /api/assistant/plans — the multi-plan switcher's source.
func (h *AssistantHandler) ListPlans(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	owner, ok := h.resolveAssistantOwner(c, userID, nil)
	if !ok {
		return
	}
	plans, err := h.listPlans(c.Request.Context(), userID, owner)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": plans})
}

// Dispatch: POST /api/assistant/dispatch — route one message to an existing
// plan or a new one and return the target. It does NOT deliver the message: the
// client then uploads any attachments to the resolved workspace and sends the
// turn via POST /workspaces/:id/messages (so attachments + knowledge dirs ride
// the normal send path, and a new plan's workspace exists before upload).
func (h *AssistantHandler) Dispatch(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()

	var req AssistantDispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		BadRequest(c, "description is required")
		return
	}

	owner, ok := h.resolveAssistantOwner(c, userID, req.Owner)
	if !ok {
		return
	}

	plans, err := h.listPlans(ctx, userID, owner)
	if err != nil {
		InternalError(c, err)
		return
	}

	target, isNew, err := h.routeMessage(ctx, userID, owner, description, req, plans, c.GetHeader("X-Niuniu-Language"))
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, AssistantDispatchResponse{Plan: target, IsNew: isNew})
}

// routeMessage decides the destination plan: explicit new, classifier verdict,
// or deterministic fallback (active plan, else new).
func (h *AssistantHandler) routeMessage(ctx context.Context, userID int64, owner service.OwnerRef, description string, req AssistantDispatchRequest, plans []AssistantPlanDTO, language string) (AssistantPlanDTO, bool, error) {
	planByID := func(id int64) (AssistantPlanDTO, bool) {
		for _, p := range plans {
			if p.IssueID == id {
				return p, true
			}
		}
		return AssistantPlanDTO{}, false
	}

	// parentForNew nests an implicitly-created task under the active
	// conversation's top-level ancestor (so it shows as a subtask in the rail).
	// Resolves to the ancestor — never a child — because CreateIssue caps the
	// hierarchy at two levels. 0 when there's no active conversation (an
	// explicit "new task" stays top-level).
	parentForNew := func() int64 {
		if req.ActivePlanID <= 0 {
			return 0
		}
		if p, ok := planByID(req.ActivePlanID); ok {
			if p.ParentIssueID > 0 {
				return p.ParentIssueID
			}
			return p.IssueID
		}
		return 0
	}

	// 1. Explicit "new task", or no existing plans → create a top-level task.
	if req.ForceNew || len(plans) == 0 {
		p, err := h.createPlan(ctx, userID, owner, description, "", 0, language)
		return p, true, err
	}

	// 2. Task isolation: when the user is inside a specific task, that task
	//    receives the message — the classifier must NOT re-route it to a
	//    different task (that made messages appear to "jump" between tasks).
	//    The classifier only runs for ambiguous input with no active task.
	if req.ActivePlanID > 0 {
		if p, ok := planByID(req.ActivePlanID); ok {
			return p, false, nil
		}
	}

	// 3. No active task: ask the classifier (when wired) to pick / create.
	decision := service.DispatchDecision{Action: service.DispatchNew}
	if h.Router != nil {
		summaries := make([]service.AssistantPlanSummary, 0, len(plans))
		for _, p := range plans {
			summaries = append(summaries, service.AssistantPlanSummary{PlanID: p.IssueID, Title: p.Title})
		}
		if d, err := h.Router.Classify(ctx, summaries, description); err == nil {
			decision = d
		} else {
			// Router unavailable: continue the active plan if the client named one.
			if req.ActivePlanID > 0 {
				decision = service.DispatchDecision{Action: service.DispatchContinue, PlanID: req.ActivePlanID}
			}
		}
	} else if req.ActivePlanID > 0 {
		decision = service.DispatchDecision{Action: service.DispatchContinue, PlanID: req.ActivePlanID}
	}

	if decision.Action == service.DispatchContinue {
		if p, ok := planByID(decision.PlanID); ok {
			return p, false, nil
		}
	}

	// 3. New plan (title hint from the classifier when present). Created
	//    mid-conversation, so it nests under the active conversation (#req4).
	p, err := h.createPlan(ctx, userID, owner, description, decision.Title, parentForNew(), language)
	return p, true, err
}

// DeletePlan: DELETE /api/assistant/plans/:issueId — permanently remove a plan
// (its workspace + on-disk files, then the issue). Idempotent on missing parts.
func (h *AssistantHandler) DeletePlan(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()

	issueID, err := strconv.ParseInt(c.Param("issueId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue id")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(ctx, userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// Delete the backing workspace (DB rows + on-disk directory) first.
	workspaces, err := h.Workspace.GetWorkspacesByIssue(ctx, issueID)
	if err != nil {
		InternalError(c, err)
		return
	}
	for _, ws := range workspaces {
		if derr := h.Workspace.Delete(ctx, ws.ID); derr != nil {
			InternalError(c, derr)
			return
		}
	}

	if derr := h.Kanban.DeleteIssue(ctx, issueID); derr != nil {
		InternalError(c, derr)
		return
	}
	c.Status(http.StatusNoContent)
}

// deriveTitle makes a short issue title from a free-form description: the first
// line, clipped to a sane length so the kanban card stays readable.
func deriveTitle(description string) string {
	title := description
	if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
		title = title[:idx]
	}
	title = strings.TrimSpace(title)
	const maxRunes = 40
	runes := []rune(title)
	if len(runes) > maxRunes {
		title = string(runes[:maxRunes]) + "…"
	}
	if title == "" {
		title = "牛牛助手任务"
	}
	return title
}
