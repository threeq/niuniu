package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ManagedTaskHandler backs the managed-task ("定时任务") provisioning path the
// conversational agent reaches through the create_managed_task MCP tool
// (POST /mcp/managed-tasks). A managed task is a recurring, no-repo workspace
// bound to a cron schedule. This handler owns the per-owner backing project
// that collects such tasks so they still show up on the kanban and the
// /schedules page.
//
// Issue+workspace provisioning is delegated to the shared
// project-parameterized routing core (service.DispatchService), which the
// IM-bot inbound pipeline and AI onboarding also reuse.
type ManagedTaskHandler struct {
	Kanban    *service.KanbanService
	Workspace *service.WorkspaceService
	Project   *service.ProjectService
	Authz     *service.Authz
	Q         *store.Queries
	// Blueprint attaches the office-doc scene as the backing project's default,
	// so managed-task workspaces auto-mount the office skills + persona + the
	// deliverable-manifest guidance.
	Blueprint *service.ProjectBlueprintService
	// dispatch is the project-parameterized routing core. createPlan delegates
	// issue+workspace provisioning here so managed tasks and the IM inbound
	// pipeline share one code path (RouteInProject / CreatePlanInProject).
	dispatch *service.DispatchService
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
func (h *ManagedTaskHandler) SetDB(db *sql.DB) { h.db = db }

// SetScheduleChanged registers the callback that hands a new managed-task
// schedule to the scheduler so it begins firing immediately.
func (h *ManagedTaskHandler) SetScheduleChanged(fn func(scheduleID int64, deleted bool)) {
	h.scheduleChanged = fn
}

func NewManagedTaskHandler(
	kanban *service.KanbanService,
	workspace *service.WorkspaceService,
	project *service.ProjectService,
	authz *service.Authz,
	q *store.Queries,
	blueprint *service.ProjectBlueprintService,
) *ManagedTaskHandler {
	return &ManagedTaskHandler{
		Kanban:    kanban,
		Workspace: workspace,
		Project:   project,
		Authz:     authz,
		Q:         q,
		Blueprint: blueprint,
		// Managed-task provisioning always creates a fresh task (no
		// continue-vs-new classification), so the LLM classifier is unused here.
		dispatch: service.NewDispatchService(kanban, workspace, q, nil),
	}
}

// officeDocSceneSlug is the office scene auto-mounted on managed-task
// workspaces — it carries the document skills, the non-technical persona, and
// the deliverable-manifest (.niuniu/artifacts.json) instruction.
const officeDocSceneSlug = "office-doc"

// fileBatchSceneSlug is the scene-gated file-batch capability mounted
// by default so the agent can organize / rename / move local files on request
// (high-risk ops still pass a confirmation gate).
const fileBatchSceneSlug = "file-batch"

// managedTaskProjectName is the well-known display name of the per-owner project
// that backs managed tasks. All managed-task-created issues land here so the
// kanban view still shows the full truth.
const managedTaskProjectName = "定时任务"

// ManagedTaskPlanDTO is one plan (an issue + its no-repo workspace) in the
// backing project — the unit the managed-task path resolves and returns.
type ManagedTaskPlanDTO struct {
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
}

// createPlan provisions one plan: find-or-create the backing project, create
// the backing issue with its goal_condition (autohost self-completion), and a
// no-repo workspace bound to it. Shared by the managed-task path.
// parentIssueID (>0) nests the plan under a top-level conversation so the rail
// can group it; 0 makes it a top-level task. The caller must already have
// resolved a top-level parent (CreateIssue caps the hierarchy at two levels).
func (h *ManagedTaskHandler) createPlan(ctx context.Context, userID int64, owner service.OwnerRef, description, titleHint string, parentIssueID int64, language string) (ManagedTaskPlanDTO, error) {
	projectID, columnID, err := h.ensureProject(ctx, userID, owner)
	if err != nil {
		return ManagedTaskPlanDTO{}, err
	}

	var createdBy *int64
	if userID > 0 {
		createdBy = &userID
	}
	// Delegate issue+workspace provisioning to the generalized routing core so
	// the backing project (NoRepo) and IM-bound projects share one path. A
	// managed task is always a no-repo office workspace.
	target, err := h.dispatch.CreatePlanInProject(ctx, owner, projectID, columnID,
		description, titleHint, parentIssueID, service.PlanCreateOpts{
			Language:  language,
			NoRepo:    true,
			CreatedBy: createdBy,
		})
	if err != nil {
		return ManagedTaskPlanDTO{}, err
	}

	// Re-read the workspace status for the DTO (CreatePlanInProject returns the
	// routing target, not the workspace row).
	status := "running"
	if ws, gerr := h.Q.GetWorkspace(ctx, target.WorkspaceID); gerr == nil {
		status = ws.Status
	}
	return ManagedTaskPlanDTO{
		IssueID:       target.IssueID,
		WorkspaceID:   target.WorkspaceID,
		ProjectID:     projectID,
		ColumnID:      columnID,
		Title:         target.Title,
		Status:        status,
		ParentIssueID: parentIssueID,
	}, nil
}

// ensureProject returns the (projectID, firstColumnID) of the owner's
// backing project, creating it (with the default kanban columns) on first use.
func (h *ManagedTaskHandler) ensureProject(ctx context.Context, userID int64, owner service.OwnerRef) (int64, int64, error) {
	projectID, columnID, err := h.resolveProject(ctx, userID, owner)
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
			[]service.BlueprintScene{{Slug: officeDocSceneSlug}, {Slug: fileBatchSceneSlug}}); aerr != nil {
			slog.Warn("assistant: attach default scenes failed", "project", projectID, "err", aerr)
		}
	}
	return projectID, columnID, nil
}

// resolveProject find-or-creates the owner's backing project and
// returns its (projectID, firstColumnID).
func (h *ManagedTaskHandler) resolveProject(ctx context.Context, userID int64, owner service.OwnerRef) (int64, int64, error) {
	// Reuse an existing backing project if present.
	if projectID, columnID, found, err := h.findProject(ctx, userID, owner); err != nil {
		return 0, 0, err
	} else if found {
		return projectID, columnID, nil
	}

	// None yet — create it with the standard columns.
	project, err := h.Kanban.CreateProjectWithDefaults(ctx,
		managedTaskProjectName, "定时任务自动创建的看板", owner.Type, owner.ID)
	if err != nil {
		// A same-named project already exists for this owner (a concurrent
		// create raced ahead of findProject) — reuse it. The lookup is
		// owner-scoped, so a different owner's project never lands here.
		if errors.Is(err, service.ErrProjectNameExists) && h.Q != nil {
			existing, gerr := h.Q.GetProjectByOwnerAndName(ctx, store.GetProjectByOwnerAndNameParams{
				OwnerType: owner.Type,
				OwnerID:   owner.ID,
				Name:      managedTaskProjectName,
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

// findProject locates the owner's backing project without creating it.
// found=false means the owner has no backing project yet.
func (h *ManagedTaskHandler) findProject(ctx context.Context, userID int64, owner service.OwnerRef) (projectID, columnID int64, found bool, err error) {
	if h.Project == nil || userID <= 0 {
		return 0, 0, false, nil
	}
	projects, err := h.Project.ListForUser(ctx, userID)
	if err != nil {
		return 0, 0, false, err
	}
	for _, p := range projects {
		if p.OwnerType == owner.Type && p.OwnerID == owner.ID && p.Name == managedTaskProjectName {
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
// "待办" lane in the default seed) — where managed-task issues are parked.
func (h *ManagedTaskHandler) firstColumn(ctx context.Context, projectID int64) (int64, error) {
	columns, err := h.Kanban.ListColumns(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, errors.New("managed-task project has no columns")
	}
	first := columns[0]
	for _, col := range columns[1:] {
		if col.Position < first.Position {
			first = col
		}
	}
	return first.ID, nil
}

// resolveOwner derives the effective owner from the request, falling
// back to the caller's personal space, and enforces write access.
func (h *ManagedTaskHandler) resolveOwner(c *gin.Context, userID int64, reqOwner *CreateWorkspaceOwnerRequest) (service.OwnerRef, bool) {
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
