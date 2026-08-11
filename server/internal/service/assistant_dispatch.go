package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrProjectNoColumns means a project has no kanban columns, so no task can be
// parked in it (should never happen for projects created with default columns).
var ErrProjectNoColumns = errors.New("project has no columns")

// ErrTaskNotInProject means the issue targeted for deletion does not live in the
// project the caller is scoped to (a shared bot must never delete another
// project's task). Callers surface it as a de-jargonized "not found".
var ErrTaskNotInProject = errors.New("task not in project")

// AssistantDispatchService is the project-parameterized routing core extracted
// from the WebUI assistant handler (api/assistant.go). Historically the handler
// hard-wired every task into the per-owner "牛牛助手" project via
// ensureAssistantProject; W2 generalizes that into RouteInProject(projectID,…)
// so any project — the WebUI assistant project, or an IM-bot-bound engineering /
// office project — reuses the exact same "continue-or-create issue+workspace"
// logic. The WebUI handler now delegates issue+workspace creation here, and the
// IM inbound pipeline (IMBotService.HandleInbound) calls RouteInProject with the
// channel's bound project.
//
// It deliberately holds only the service-layer collaborators (Kanban, Workspace,
// store, and the optional LLM classifier) — never agentproxy — so it stays free
// of an import cycle. Message delivery to the agent session is the caller's job
// (WebUI relies on the client's POST /workspaces/:id/messages; IM does its own
// agentproxy.Deliver right after routing).
type AssistantDispatchService struct {
	kanban     *KanbanService
	workspace  *WorkspaceService
	q          *store.Queries
	classifier *AssistantRouter // optional; nil = deterministic new-or-active only
}

// NewAssistantDispatchService wires the routing core. classifier may be nil.
func NewAssistantDispatchService(kanban *KanbanService, workspace *WorkspaceService, q *store.Queries, classifier *AssistantRouter) *AssistantDispatchService {
	return &AssistantDispatchService{kanban: kanban, workspace: workspace, q: q, classifier: classifier}
}

// PlanTarget is the resolved destination of a routed message: the issue +
// workspace (with its project/column) the message belongs to.
type PlanTarget struct {
	IssueID     int64
	WorkspaceID int64
	ProjectID   int64
	ColumnID    int64
	Title       string
	IsNew       bool
}

// RouteHint tunes RouteInProject's continue-vs-new decision.
type RouteHint struct {
	// ForceNew skips classification and always creates a fresh task.
	ForceNew bool
	// ActiveIssueID, when >0 and still backed by a workspace in the project,
	// continues that task without consulting the classifier (task isolation:
	// the caller already knows which task the user is in — e.g. an IM thread /
	// active-issue pointer).
	ActiveIssueID int64
	// TitleHint overrides the derived title for a newly created task.
	TitleHint string
	// Language seeds the workspace CLAUDE.md "User Language" directive.
	Language string
	// PermissionMode overrides the created workspace's NIUNIU_PERMISSION_MODE.
	// Empty defaults to "autohost". Interactive flows (e.g. IM-bot onboarding)
	// pass "bypassPermissions" so the auto-continue watchdog does not run.
	PermissionMode string
}

// PlanCreateOpts are the lower-level knobs for CreatePlanInProject.
type PlanCreateOpts struct {
	Language        string
	NoRepo          bool
	Repos           []RepoBranch
	CreatedBy       *int64
	// PermissionMode overrides the created workspace's NIUNIU_PERMISSION_MODE
	// ("" => "autohost"). See RouteHint.PermissionMode.
	PermissionMode string
}

// RouteInProject decides whether a message continues an existing task in
// projectID or starts a new one, creating the issue+workspace when new. The
// destination column is the project's first (lowest-position) lane and repos are
// derived from the project's attachments (no repos => a no-repo office workspace),
// so callers need only supply the project id + text.
func (s *AssistantDispatchService) RouteInProject(ctx context.Context, owner OwnerRef, projectID int64, text string, hint RouteHint) (PlanTarget, error) {
	if !hint.ForceNew {
		// 1. Explicit active task (thread / pointer) — continue it, no classifier.
		if hint.ActiveIssueID > 0 {
			if t, ok := s.planForIssue(ctx, projectID, hint.ActiveIssueID); ok {
				return t, nil
			}
		}
		// 2. Otherwise let the classifier pick among existing plans (when wired).
		plans, err := s.listProjectPlans(ctx, projectID)
		if err != nil {
			return PlanTarget{}, err
		}
		if len(plans) > 0 && s.classifier != nil {
			summaries := make([]AssistantPlanSummary, 0, len(plans))
			for _, p := range plans {
				summaries = append(summaries, AssistantPlanSummary{PlanID: p.IssueID, Title: p.Title})
			}
			if d, cerr := s.classifier.Classify(ctx, summaries, text); cerr == nil {
				if d.Action == DispatchContinue {
					for _, p := range plans {
						if p.IssueID == d.PlanID {
							return p, nil
						}
					}
				}
				if d.Title != "" && hint.TitleHint == "" {
					hint.TitleHint = d.Title
				}
			}
		}
	}

	// New task in this project.
	columnID, err := s.firstColumn(ctx, projectID)
	if err != nil {
		return PlanTarget{}, err
	}
	repos, err := s.projectRepoBranches(ctx, projectID)
	if err != nil {
		return PlanTarget{}, err
	}
	return s.CreatePlanInProject(ctx, owner, projectID, columnID, text, hint.TitleHint, 0, PlanCreateOpts{
		Language:        hint.Language,
		NoRepo:          len(repos) == 0,
		Repos:           repos,
		PermissionMode:  hint.PermissionMode,
	})
}

// CreatePlanInProject provisions one plan (issue + workspace) in the given
// project/column: create the issue, set its goal_condition (autohost
// self-completion), and create the backing workspace. This is the generalized
// body of the old api/assistant.go createPlan — the WebUI handler now calls it
// with the assistant project (NoRepo=true), IM calls it with the bound project.
func (s *AssistantDispatchService) CreatePlanInProject(ctx context.Context, owner OwnerRef, projectID, columnID int64, description, titleHint string, parentIssueID int64, opts PlanCreateOpts) (PlanTarget, error) {
	title := strings.TrimSpace(titleHint)
	if title == "" {
		title = DeriveTaskTitle(description)
	}
	var parent *int64
	if parentIssueID > 0 {
		parent = &parentIssueID
	}
	issue, err := s.kanban.CreateIssue(ctx, columnID, title, description,
		0 /*priority*/, 0 /*position*/, "", "", "", 0,
		parent, "task", 0 /*execWave*/, nil, nil, ownerUserID(owner, opts.CreatedBy))
	if err != nil {
		return PlanTarget{}, err
	}
	// goal_condition drives autohost self-completion ([AUTOHOST_DONE]).
	if err := s.kanban.SetIssueGoalCondition(ctx, issue.ID, description); err != nil {
		return PlanTarget{}, err
	}

	issueID := issue.ID
	in := CreateWorkspaceInput{
		IssueID:         &issueID,
		Name:            title,
		OwnerType:       owner.Type,
		OwnerID:         owner.ID,
		CreatedBy:       opts.CreatedBy,
		NoRepo:          opts.NoRepo,
		Repos:           opts.Repos,
		Language:        opts.Language,
		PermissionMode:  opts.PermissionMode,
	}
	result, err := s.workspace.Create(ctx, in)
	if err != nil {
		return PlanTarget{}, err
	}
	return PlanTarget{
		IssueID:     issue.ID,
		WorkspaceID: result.Workspace.ID,
		ProjectID:   projectID,
		ColumnID:    columnID,
		Title:       title,
		IsNew:       true,
	}, nil
}

// StartWorkspaceForExistingIssue provisions a backing workspace for an issue that
// already exists in projectID but has none yet — the IM `#<id>` control's "start
// work on a kanban issue" path (requirement: an issue created without a workspace
// can be picked up directly from a chat). It is idempotent: an issue that already
// has a workspace resolves to that workspace instead of creating a second one. The
// issue must live in projectID (ErrTaskNotInProject otherwise) so a shared bot can
// never start work on another project's issue. Column/repos are derived from the
// issue's project exactly as CreatePlanInProject does for a new task.
func (s *AssistantDispatchService) StartWorkspaceForExistingIssue(ctx context.Context, owner OwnerRef, projectID, issueID int64) (PlanTarget, error) {
	// Reuse an existing workspace-backed plan (idempotent re-start).
	if t, ok := s.planForIssue(ctx, projectID, issueID); ok {
		return t, nil
	}
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return PlanTarget{}, err
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return PlanTarget{}, err
	}
	if col.ProjectID != projectID {
		return PlanTarget{}, ErrTaskNotInProject
	}
	repos, err := s.projectRepoBranches(ctx, projectID)
	if err != nil {
		return PlanTarget{}, err
	}
	issueRef := issue.ID
	result, err := s.workspace.Create(ctx, CreateWorkspaceInput{
		IssueID:         &issueRef,
		Name:            issue.Title,
		OwnerType:       owner.Type,
		OwnerID:         owner.ID,
		NoRepo:          len(repos) == 0,
		Repos:           repos,
	})
	if err != nil {
		return PlanTarget{}, err
	}
	return PlanTarget{
		IssueID:     issue.ID,
		WorkspaceID: result.Workspace.ID,
		ProjectID:   projectID,
		ColumnID:    issue.ColumnID,
		Title:       issue.Title,
		IsNew:       true,
	}, nil
}

// DeleteTask removes issueID together with its backing workspace(s) — the
// inverse of CreatePlanInProject. It first re-verifies the issue actually lives
// in projectID (ErrTaskNotInProject otherwise) so a shared bot can never delete
// another project's task. stop, when non-nil, is invoked per workspace to
// terminate its running agent session before the on-disk cleanup (the same
// pre-delete stop the WebUI handler does via proxy.RemoveSession); passing nil is
// safe for tasks with no live agent. Workspace removal is destructive and
// unconditional (force semantics): the command is an explicit "throw this task
// away", so uncommitted-change protection is intentionally not applied here.
func (s *AssistantDispatchService) DeleteTask(ctx context.Context, projectID, issueID int64, stop func(context.Context, int64)) error {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return err
	}
	if col.ProjectID != projectID {
		return ErrTaskNotInProject
	}
	workspaces, err := s.workspace.GetWorkspacesByIssue(ctx, issueID)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		if stop != nil {
			stop(ctx, ws.ID)
		}
		if derr := s.workspace.Delete(ctx, ws.ID); derr != nil {
			return derr
		}
	}
	return s.kanban.DeleteIssue(ctx, issueID)
}

// planForIssue resolves an issue in the project to its plan target (issue +
// workspace), reporting ok=false if the issue is not in the project or has no
// workspace.
func (s *AssistantDispatchService) planForIssue(ctx context.Context, projectID, issueID int64) (PlanTarget, bool) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return PlanTarget{}, false
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil || col.ProjectID != projectID {
		return PlanTarget{}, false
	}
	workspaces, err := s.workspace.GetWorkspacesByIssue(ctx, issueID)
	if err != nil || len(workspaces) == 0 {
		return PlanTarget{}, false
	}
	return PlanTarget{
		IssueID:     issueID,
		WorkspaceID: workspaces[0].ID,
		ProjectID:   projectID,
		ColumnID:    issue.ColumnID,
		Title:       issue.Title,
	}, true
}

// listProjectPlans returns the project's issues that have a backing workspace,
// newest-activity first — the candidate set for the classifier.
func (s *AssistantDispatchService) listProjectPlans(ctx context.Context, projectID int64) ([]PlanTarget, error) {
	// Single JOIN (issue + its primary workspace) instead of an N+1
	// ListIssuesByProject-then-GetWorkspacesByIssue-per-issue loop, which under
	// SQLite's single writer contends with agent/UI traffic on busy projects.
	rows, err := s.q.ListProjectPlansWithWorkspace(ctx, projectID)
	if err != nil {
		return nil, err
	}
	plans := make([]PlanTarget, 0, len(rows))
	for _, r := range rows {
		plans = append(plans, PlanTarget{
			IssueID:     r.ID,
			WorkspaceID: r.WorkspaceID,
			ProjectID:   projectID,
			ColumnID:    r.ColumnID,
			Title:       r.Title,
		})
	}
	return plans, nil
}

// firstColumn returns the id of the project's lowest-position column (the "待办"
// lane in the default seed) where new tasks are parked.
func (s *AssistantDispatchService) firstColumn(ctx context.Context, projectID int64) (int64, error) {
	columns, err := s.q.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, ErrProjectNoColumns
	}
	first := columns[0]
	for _, col := range columns[1:] {
		if col.Position < first.Position {
			first = col
		}
	}
	return first.ID, nil
}

// projectRepoBranches builds the RepoBranch list for a project's attached
// repositories (each forked from its configured default branch). An empty result
// means the project has no repos, so the caller creates a no-repo workspace.
func (s *AssistantDispatchService) projectRepoBranches(ctx context.Context, projectID int64) ([]RepoBranch, error) {
	rows, err := s.q.ListProjectRepositories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	repos := make([]RepoBranch, 0, len(rows))
	for _, r := range rows {
		branch := strings.TrimSpace(r.ProjectDefaultBranch)
		if branch == "" {
			branch = strings.TrimSpace(r.RepoDefaultBranch.String)
		}
		repos = append(repos, RepoBranch{RepoID: r.RepositoryID, Branch: branch, Base: branch})
	}
	// Deterministic order for reproducible worktree provisioning.
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	return repos, nil
}

// ownerUserID picks the acting user id for issue creation: the explicit creator
// when known, else the owner's own id for a personal (user) owner, else 0.
func ownerUserID(owner OwnerRef, createdBy *int64) int64 {
	if createdBy != nil {
		return *createdBy
	}
	if owner.Type == "user" {
		return owner.ID
	}
	return 0
}

// DeriveTaskTitle makes a short issue title from a free-form description: the
// first line, clipped to a sane length so the kanban card stays readable.
func DeriveTaskTitle(description string) string {
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
