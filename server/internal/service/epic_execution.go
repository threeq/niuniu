package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// WorkspaceCreator is the narrow subset of WorkspaceService the Epic execution
// engine needs to spin up a workspace per child issue. Defined as an interface
// so tests can inject a fake that records Create calls without touching disk.
// *WorkspaceService already satisfies it.
type WorkspaceCreator interface {
	Create(ctx context.Context, input CreateWorkspaceInput) (*WorkspaceResult, error)
	// SetWorkspaceEnv sets a workspace env var. The engine uses it to put Epic
	// workspaces into autohost mode (NIUNIU_PERMISSION_MODE=autohost) so their
	// agents loop until the goal_condition judge says done.
	SetWorkspaceEnv(ctx context.Context, workspaceID int64, key, value string) error
}

// WorkspaceCompleter requests completion of a workspace through the single
// completion choke point (RequestWorkspaceCompletion): it runs the project's
// column-native floor gate and only finalizes (status=completed + issue
// lifecycle=completed + publishes workspace_completed) when the底线 is green.
// *WorkspaceOpsService satisfies it. The engine uses it to auto-complete an
// Epic-managed workspace when its agent finishes autonomously (terminal autohost
// done) — bypassing the human needs_review gate but NOT the floor gate (§22.7).
type WorkspaceCompleter interface {
	RequestWorkspaceCompletion(ctx context.Context, workspaceID int64, trigger CompletionTrigger) (RequestCompletionResult, error)
}

// ReviewConfirmer asks the user for one lightweight confirmation of an epic's review
// outcome before the epic is finalized (spec §22.7/§23.1: an epic is the unit of user
// intent, so "经 epic review 阶段对账 + 一次人为确认后收尾"). Optional (nil in tests /
// when no ask-user broker is wired): nil => the epic auto-finalizes (the pre-§22.7
// behaviour). Only epics use it — child issues roll their correctness up to this epic
// review, and standalone issues keep the human needs_review gate.
type ReviewConfirmer interface {
	// ConfirmEpicReview blocks until the user answers (or the request times out /
	// is cancelled). Returns true ONLY on an explicit confirm; false (with nil error)
	// on decline/timeout/cancel so the epic stays in review for the user to act on.
	ConfirmEpicReview(ctx context.Context, ws store.Workspace, epic store.Issue, children []store.Issue) (bool, error)
}

// WorkspaceMerger is the narrow surface the Epic engine needs to commit a child
// workspace's worktree(s) and merge them into the epic feature branch when the
// child auto-completes, so later waves build on the integrated result.
// *WorkspaceOpsService satisfies it. Optional (nil in tests): when nil the
// engine skips the commit/merge step entirely (fake-based tests do no real git).
type WorkspaceMerger interface {
	// CommitWorktree commits any pending work in the named worktree.
	CommitWorktree(ctx context.Context, workspaceID int64, worktreeName, message string) error
	// MergeWorktree merges the named worktree's branch into targetBranch.
	MergeWorktree(ctx context.Context, workspaceID int64, worktreeName, targetBranch string) error
	// SyncBranchIntoWorktree fast-forwards the named worktree's current branch to
	// sourceBranch (the opposite direction of MergeWorktree). The Epic engine uses
	// it to bring the epic feature branch's accumulated child work into the epic's
	// own workspace so that workspace reflects the integrated result while still
	// diffing against the real baseline. Fast-forward-only: a diverged worktree is
	// left untouched (caller logs and skips) rather than risking a conflict.
	SyncBranchIntoWorktree(ctx context.Context, workspaceID int64, worktreeName, sourceBranch string) error
}

// EpicExecutionService drives the capability tools an Epic orchestration agent
// (mode-B) relies on: dispatching child workspaces (StartWorkspaceForIssue),
// ensuring the epic feature branch, integrating a completed child's worktrees into
// that branch (mergeChildIntoEpic, serialized through the single worker goroutine),
// recording child failure (applyChildFailure / failChildIntegration), and writing
// the epic's terminal exec_status back when its orchestration workspace finishes
// (OnWorkspaceCompleted). The former mode-A wave-by-wave scheduling state machine
// (ExecuteEpic/startWave/advance + the in-process run map) was retired once these
// tools covered failure/merge/finalization for the orchestration-agent path.
type EpicExecutionService struct {
	db        *store.DB
	q         *store.Queries
	kanban    *KanbanService
	wsCreator WorkspaceCreator
	bus       *event.Bus
	// agentProxy is optional (nil in tests). When set, each child workspace the
	// engine dispatches gets a kickoff message (the child's title + description)
	// so its agent starts working autonomously. Wired via SetAgentProxy.
	agentProxy AgentProxyShim
	// completer is optional (nil in tests). When set, an Epic-managed workspace
	// whose agent finishes (terminal autohost done) is auto-marked done so the
	// epic advances without a human clicking "mark done". Wired via SetCompleter.
	completer WorkspaceCompleter
	// merger is optional (nil in tests). When set, a CHILD workspace that
	// auto-completes has its worktree(s) committed + merged into the epic feature
	// branch (epic/<id>) before being marked done. Wired via SetMerger.
	merger WorkspaceMerger
	// reviewConfirmer is optional (nil in tests / when no ask-user broker is wired).
	// When set, an EPIC's review workspace finishing asks the user for one lightweight
	// confirmation before the epic is finalized (spec §22.7/§23.1). nil => auto-finalize.
	// Wired via SetReviewConfirmer.
	reviewConfirmer ReviewConfirmer
	// confirmingEpics guards against launching a second concurrent review-confirm for
	// the same epic while one is already awaiting the user. Guarded by confirmMu.
	confirmMu       sync.Mutex
	confirmingEpics map[int64]struct{}
	// goalSuggester is optional (nil in tests / when the claude CLI is absent).
	// When set, ensureWorkspace infers a one-line goal_condition (haiku) for a
	// standalone issue that has none, instead of falling back to the issue
	// title+description. Wired via SetGoalSuggester. See ai_native_advance.go.
	goalSuggester GoalConditionSuggester
	// runAsync runs a detached side-task. Defaults to `go f()`; tests override it
	// (SetAsyncRunner) to run inline for deterministic assertions.
	runAsync func(func())
	// guard is optional (nil in tests). When set, StartWorkspaceForIssue runs the
	// orchestration cost guardrails (per-owner concurrency queue + chain cost
	// budget) before creating a workspace. nil = no limits (legacy behaviour).
	// Spec 2026-06-06 section 16. *EpicExecutionService implements WorkspaceStarter
	// so the guard can drain its queue back through StartQueuedWorkspace.
	guard *OrchestrationGuard

	// dirtyChecker overrides how AdvanceIssue probes a workspace for uncommitted
	// code before allowing a move into a `complete` column. nil = the default
	// git-status probe (gitWorkspaceDirty). Tests inject a fake via
	// SetCompletionDirtyChecker. See ai_native_advance.go.
	dirtyChecker func(ctx context.Context, ws store.Workspace) (bool, string)

	// routeStepLimit / columnReentryLimit bound advance_issue routing (spec §23.2):
	// the total routing steps for an issue and how many times it may re-enter any one
	// column, before it lands blocked-needs-human. <=0 => the package defaults. Wired
	// via SetRoutingLimits (server.New reads env). See ai_native_advance.go.
	routeStepLimit     int
	columnReentryLimit int

	// execEvents is optional (nil in tests). When set, the execution path records
	// a first-class per-issue execution timeline (advance moves / terminal
	// transitions / gate results / ask_user round-trips / cost). Wired via
	// SetExecEventService. Spec §23.7. Recording is always best-effort.
	execEvents *ExecEventService

	// checkpoints is optional (nil in tests). When set, AdvanceIssue takes a
	// hidden-ref checkpoint of the issue's worktree(s) when it enters the implement
	// (instruct) column, seeding the autohost 安全网 timeline. Wired via
	// SetCheckpointService. Best-effort; never blocks the move. See checkpoint.go.
	checkpoints *CheckpointService

	// mergeMu serializes ALL child->epic-branch merges. Historically only the
	// single worker goroutine merged (OnAgentDone), which serialized implicitly;
	// the advance_issue complete-column path now also merges synchronously on the
	// MCP request goroutine, so an explicit mutex keeps concurrent merges into the
	// same epic branch from racing on the git index.
	mergeMu sync.Mutex

	// mu guards the event-bus subscription lifecycle fields below (subCh/workCh/quit).
	mu sync.Mutex

	// subCh is the event-bus subscription channel held by Start so Stop can
	// Unsubscribe it. Guarded by mu.
	subCh chan event.OutputEvent
	// workCh carries decoded terminal events from the (hot) bus-consumer goroutine
	// to the worker goroutine, which runs the slow handlers (DB + git merges) OFF
	// the bus path. Keeping the consumer hot means its 64-entry bus buffer never
	// backs up under the agent-output flood, so workspace_completed / agent_done
	// are not dropped. A single worker also serialises git merges into the epic
	// branch (no index.lock races). Guarded by mu.
	workCh chan epicJob
	// quit is closed by Stop to end both the consumer and worker goroutines
	// (Bus.Unsubscribe does not close the channel). Guarded by mu.
	quit chan struct{}
}

// epicJob is a decoded terminal event handed from the bus consumer to the worker.
type epicJob struct {
	kind        epicJobKind
	issueID     int64
	success     bool
	workspaceID int64
}

type epicJobKind int

const (
	jobWorkspaceCompleted epicJobKind = iota
	jobAgentDone
)

// SetAgentProxy wires the agent proxy so dispatched child workspaces are kicked
// off with the child issue's title + description. Optional; nil-safe.
func (s *EpicExecutionService) SetAgentProxy(p AgentProxyShim) {
	s.agentProxy = p
}

// SetCompleter wires the workspace completer used to auto-complete an
// Epic-managed workspace when its agent finishes. Optional; nil-safe.
func (s *EpicExecutionService) SetCompleter(c WorkspaceCompleter) {
	s.completer = c
}

// SetReviewConfirmer wires the human review-confirmation broker used to confirm an
// epic's review outcome before finalizing it (spec §22.7/§23.1). Optional; nil-safe.
func (s *EpicExecutionService) SetReviewConfirmer(c ReviewConfirmer) {
	s.reviewConfirmer = c
}

// SetMerger wires the workspace merger used to commit + merge a child
// workspace's worktree(s) into the epic feature branch on completion.
// Optional; nil-safe.
func (s *EpicExecutionService) SetMerger(m WorkspaceMerger) {
	s.merger = m
}

// SetGuard wires the orchestration cost guard. Optional; nil = no limits.
func (s *EpicExecutionService) SetGuard(g *OrchestrationGuard) {
	s.guard = g
}

// epicBranchName is the deterministic feature-branch name for an Epic. All of
// the Epic's child/control workspaces are based on this branch so wave-by-wave
// work accumulates on it, and the final human merge folds it into main.
func epicBranchName(epicID int64) string {
	return fmt.Sprintf("epic/%d", epicID)
}

// epicIDForIssue returns the governing epic id for an issue and whether the
// issue is Epic-managed: the issue itself when it is an epic, its parent when it
// is a child of an epic. A standalone issue returns (0, false).
func epicIDForIssue(issue store.Issue) (epicID int64, isEpicManaged bool) {
	if issue.IssueType == "epic" {
		return issue.ID, true
	}
	if issue.ParentIssueID.Valid {
		return issue.ParentIssueID.Int64, true
	}
	return 0, false
}

// NewEpicExecutionService constructs the engine. db/q follow the driver-aware
// store pattern (db is wrapped once here). wsCreator is *WorkspaceService in
// production, a fake in tests. bus may be nil (Start becomes a no-op).
func NewEpicExecutionService(db *sql.DB, q *store.Queries, kanban *KanbanService, wsCreator WorkspaceCreator, bus *event.Bus) *EpicExecutionService {
	return &EpicExecutionService{
		db:        store.Wrap(db),
		q:         q,
		kanban:    kanban,
		wsCreator: wsCreator,
		bus:       bus,
	}
}

// RequestMergeToMain asks the epic's active control workspace agent to merge the
// epic feature branch (epic/<epicID>) into each repo's default branch. It does
// NOT run any git in the backend: it locates the epic's non-archived control
// workspace and sends a merge prompt to its agent session, which performs the
// merge (resolving conflicts in place) and reports back.
//
// Returns an error when the epic has no active control workspace (the review
// phase should have created one) or no agent backend is wired.
func (s *EpicExecutionService) RequestMergeToMain(ctx context.Context, epicID int64) error {
	if s.agentProxy == nil {
		return fmt.Errorf("agent backend not available")
	}
	epic, err := s.q.GetIssue(ctx, epicID)
	if err != nil {
		return fmt.Errorf("load epic %d: %w", epicID, err)
	}
	workspaces, err := s.q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: epicID, Valid: true})
	if err != nil {
		return fmt.Errorf("list epic workspaces: %w", err)
	}
	var ws *store.Workspace
	for i := range workspaces {
		if workspaces[i].IsArchived == 0 {
			ws = &workspaces[i]
			break
		}
	}
	if ws == nil || ws.Path == "" {
		return fmt.Errorf("epic has no active control workspace to perform the merge")
	}

	// Collect the per-repo default branch names for the prompt.
	var defaults []string
	if prs, lErr := s.listProjectReposForIssue(ctx, epicID); lErr == nil {
		for _, r := range prs {
			if r.defaultBranch != "" {
				defaults = append(defaults, r.defaultBranch)
			}
		}
	}
	prompt := composeMergeToMainPrompt(epic, defaults)

	sess, err := s.agentProxy.GetOrStartSession(ctx, ws.ID, 0)
	if err != nil || sess == nil {
		return fmt.Errorf("agent session unavailable: %w", err)
	}
	sess.SendKickoff(ctx, ws.Path, prompt, "")
	slog.Info("epic merge-to-main: prompt sent to control workspace", "epicID", epicID, "workspaceID", ws.ID)
	return nil
}

// composeMergeToMainPrompt builds the message instructing the epic's control
// workspace agent to merge the feature branch into the repos' default branches.
func composeMergeToMainPrompt(epic store.Issue, defaultBranches []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 合并 Epic「%s」到主分支\n\n", epic.Title)
	fmt.Fprintf(&b, "请把当前 Epic 的功能分支 `%s` 合并到主分支(各仓库的默认分支)", epicBranchName(epic.ID))
	if len(defaultBranches) > 0 {
		seen := map[string]bool{}
		uniq := make([]string, 0, len(defaultBranches))
		for _, d := range defaultBranches {
			if !seen[d] {
				seen[d] = true
				uniq = append(uniq, d)
			}
		}
		fmt.Fprintf(&b, "(%s)", strings.Join(uniq, ", "))
	}
	b.WriteString("；如有冲突请就地解决后完成合并并提交；完成后报告结果。\n")
	return b.String()
}

// createWorkspaceForIssue resolves the issue's project owner + repos and creates
// a workspace bound to that issue (1:1). Shared by the mode-B orchestration-ws
// path and the start_workspace MCP tool. Returns the created workspace.
// callerUserID is the human who triggered creation when known (0 = none, e.g.
// the async start_workspace MCP / queue-drain paths). When > 0 it becomes the
// explicit workspace creator (step 1 of 工作空间创建人确定顺序), taking precedence
// over the issue/epic created_by fallback resolved inside WorkspaceService.Create.
func (s *EpicExecutionService) createWorkspaceForIssue(ctx context.Context, issueID, callerUserID int64) (*WorkspaceResult, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("load issue %d: %w", issueID, err)
	}
	ownerRow, err := s.q.GetProjectAndLifecycleByIssueID(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("resolve issue owner: %w", err)
	}
	// Determine the governing epic: the issue itself if it is an epic, its parent
	// if it is a child. Epic-managed workspaces are based on the epic feature
	// branch; standalone issues keep the project default branch.
	var repos []RepoBranch
	var language string
	if epicID, isEpicManaged := epicIDForIssue(issue); isEpicManaged {
		if err := s.ensureEpicBranch(ctx, epicID); err != nil {
			return nil, fmt.Errorf("ensure epic branch: %w", err)
		}
		if issue.IssueType == "epic" {
			// The epic's OWN orchestration/review workspace FORKS from the epic feature
			// branch (so it holds the accumulated child work immediately, and any review
			// fixes land on a branch that merges back into the feature branch) but records
			// its diff BASE as the project default branch (the real baseline, e.g. main),
			// so its diff/ahead view shows the whole epic's cumulative changes relative to
			// the baseline. As later children land on the feature branch, it is synced in
			// via syncEpicWorkspaceLocked.
			repos, err = s.resolveEpicControlRepos(ctx, epicID)
		} else {
			// Child of an epic: base on the epic feature branch (epic/<id>) so
			// wave-by-wave work accumulates on it.
			repos, err = s.resolveEpicRepos(ctx, epicID)
		}
		// This path runs async (orchestration agent dispatching children via the
		// start_workspace MCP tool) with no originating HTTP request carrying the
		// user's UI language. Inherit it from the governing epic's own workspace —
		// the epic orchestration workspace is normally created via the regular
		// create flow, which captures the user's language — so children speak the
		// user's language too. Edge case: if the epic workspace itself was created
		// via an async path (or doesn't exist yet), its language is '' and the
		// child falls back to the generic "follow the user" directive.
		if epicWs, ok := s.activeWorkspaceForIssue(ctx, epicID); ok {
			language = epicWs.Language
		}
	} else {
		repos, err = s.resolveProjectRepos(ctx, issueID)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve project repos: %w", err)
	}
	var createdBy *int64
	if callerUserID > 0 {
		createdBy = &callerUserID
	}
	return s.wsCreator.Create(ctx, CreateWorkspaceInput{
		IssueID:   &issueID,
		Name:      issue.Title,
		Repos:     repos,
		OwnerType: ownerRow.ProjectOwnerType,
		OwnerID:   ownerRow.ProjectOwnerID,
		CreatedBy: createdBy,
		// Auto-created workspaces inherit the issue's project's default agent
		// (NOT NULL DEFAULT 'claude', so always populated). Empty would also
		// default to claude at the workspace SQL layer.
		CliType:  ownerRow.ProjectDefaultCliType,
		Language: language,
	})
}

// StartOutcome is the result of StartWorkspaceForIssue. Exactly one of
// WorkspaceID (started/reused) or Queued (deferred behind the per-owner
// concurrency cap) is the operative field. Warn carries a near-budget advisory
// the orchestration agent can act on (ask_user); SpentUSD/BudgetUSD expose the
// chain cost state behind a queue/warn/block decision.
type StartOutcome struct {
	WorkspaceID   int64
	Queued        bool
	QueuePosition int
	Warn          string
	SpentUSD      float64
	BudgetUSD     float64
}

// StartWorkspaceForIssue creates a workspace for an arbitrary issue (1:1). This
// is the engine surface behind the start_workspace MCP tool (mode B): an Epic
// orchestration agent calls it to dispatch a child issue's workspace on its own
// schedule, rather than relying on the mode-A wave loop.
//
// The orchestration cost guard (when wired) gates creation: an over-budget chain
// returns an error (so the agent stops / asks the user); an owner at the
// concurrency cap queues the issue (Queued=true) to start later when a slot
// frees. A near-budget chain still starts but surfaces Warn.
func (s *EpicExecutionService) StartWorkspaceForIssue(ctx context.Context, issueID int64) (StartOutcome, error) {
	// Idempotent: reuse the issue's existing non-archived workspace instead of
	// creating a duplicate (so re-dispatch / re-execute sticks to the associated
	// ws). Reuse is not new concurrency, so it bypasses admission.
	if existing, ok := s.activeWorkspaceForIssue(ctx, issueID); ok {
		slog.Info("start_workspace: reused existing workspace for issue", "issueID", issueID, "workspaceID", existing.ID)
		return StartOutcome{WorkspaceID: existing.ID}, nil
	}

	// Admission: chain cost budget (block when exhausted) + per-owner concurrency
	// (queue when at the cap). A nil guard always admits.
	dec, err := s.guard.AdmitWorkspace(ctx, issueID)
	if err != nil {
		return StartOutcome{}, err
	}
	switch dec.Action {
	case GuardBlock:
		slog.Warn("start_workspace: blocked by chain cost budget", "issueID", issueID, "spentUSD", dec.SpentUSD, "budgetUSD", dec.BudgetUSD)
		return StartOutcome{SpentUSD: dec.SpentUSD, BudgetUSD: dec.BudgetUSD}, fmt.Errorf("%s", dec.Reason)
	case GuardQueue:
		slog.Info("start_workspace: queued behind concurrency cap", "issueID", issueID, "position", dec.QueuePosition)
		return StartOutcome{Queued: true, QueuePosition: dec.QueuePosition, Warn: dec.Warn, SpentUSD: dec.SpentUSD, BudgetUSD: dec.BudgetUSD}, nil
	}

	res, err := s.createWorkspaceForIssue(ctx, issueID, 0)
	if err != nil {
		return StartOutcome{}, err
	}
	slog.Info("start_workspace: dispatched workspace for issue", "issueID", issueID, "workspaceID", res.Workspace.ID)
	return StartOutcome{WorkspaceID: res.Workspace.ID, Warn: dec.Warn, SpentUSD: dec.SpentUSD, BudgetUSD: dec.BudgetUSD}, nil
}

// StartQueuedWorkspace creates a workspace for a previously-queued issue. It is
// the guard's drain callback (WorkspaceStarter): admission already decided to
// start this issue, so it skips AdmitWorkspace (no re-queue). Idempotent: reuses
// an existing non-archived workspace.
func (s *EpicExecutionService) StartQueuedWorkspace(ctx context.Context, issueID int64) error {
	if existing, ok := s.activeWorkspaceForIssue(ctx, issueID); ok {
		slog.Info("start_workspace(drain): reused existing workspace", "issueID", issueID, "workspaceID", existing.ID)
		return nil
	}
	res, err := s.createWorkspaceForIssue(ctx, issueID, 0)
	if err != nil {
		return err
	}
	slog.Info("start_workspace(drain): dispatched queued workspace", "issueID", issueID, "workspaceID", res.Workspace.ID)
	return nil
}

// activeWorkspaceForIssue returns the issue's existing non-archived workspace, if
// any. The engine uses it to reuse a workspace on re-execute / re-dispatch rather
// than creating a duplicate (workspace<->issue is 1:1).
func (s *EpicExecutionService) activeWorkspaceForIssue(ctx context.Context, issueID int64) (store.Workspace, bool) {
	wss, err := s.q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: issueID, Valid: true})
	if err != nil {
		return store.Workspace{}, false
	}
	for _, w := range wss {
		if w.IsArchived == 0 {
			return w, true
		}
	}
	return store.Workspace{}, false
}

// NOTE: there is no separate "mode B" entrypoint. Orchestration is not a mode —
// it is what a workspace ON an Epic issue becomes. Creating such a workspace
// (manually via the normal create-workspace flow, or programmatically) triggers
// WorkspaceService's post-create hook -> OnWorkspaceCreated, which marks the epic
// running and kicks off the orchestration agent. The agent then dispatches
// children via the start_workspace MCP tool (StartWorkspaceForIssue).

// projectRepo is one repo attached to the issue's project, with its on-disk path
// and resolved default branch. Used to build RepoBranch entries and to ensure the
// epic feature branch exists per repo.
type projectRepo struct {
	repoID        int64
	path          string
	defaultBranch string
}

// listProjectReposForIssue resolves the repos attached to the issue's project,
// each with its on-disk path and resolved (project-default || repo-default)
// branch. Shared by the project-default-branch and epic-branch resolution paths.
func (s *EpicExecutionService) listProjectReposForIssue(ctx context.Context, issueID int64) ([]projectRepo, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListProjectRepositories(ctx, col.ProjectID)
	if err != nil {
		return nil, err
	}
	repos := make([]projectRepo, 0, len(rows))
	for _, r := range rows {
		branch := r.ProjectDefaultBranch
		if branch == "" && r.RepoDefaultBranch.Valid {
			branch = r.RepoDefaultBranch.String
		}
		pr := projectRepo{repoID: r.RepositoryID, defaultBranch: branch}
		if repo, gErr := s.q.GetRepository(ctx, r.RepositoryID); gErr == nil {
			pr.path = repo.Path
		} else {
			slog.Warn("epic: load repository for branch ops", "repoID", r.RepositoryID, "error", gErr)
		}
		repos = append(repos, pr)
	}
	return repos, nil
}

// resolveProjectRepos resolves the repos attached to the epic's project, mapped
// to RepoBranch entries on each repo's project default branch. (Used for
// standalone, non-Epic-managed workspaces.)
func (s *EpicExecutionService) resolveProjectRepos(ctx context.Context, epicID int64) ([]RepoBranch, error) {
	prs, err := s.listProjectReposForIssue(ctx, epicID)
	if err != nil {
		return nil, err
	}
	repos := make([]RepoBranch, 0, len(prs))
	for _, r := range prs {
		repos = append(repos, RepoBranch{RepoID: r.repoID, Branch: r.defaultBranch})
	}
	return repos, nil
}

// resolveEpicRepos resolves the epic's project repos mapped to RepoBranch
// entries based on the epic feature branch (epic/<epicID>). Used for the epic's
// CHILD workspaces so wave-by-wave work accumulates on the shared feature branch
// instead of the project default. The epic's OWN orchestration/review workspace
// instead bases on the project default (the real baseline) — see
// createWorkspaceForIssue — and pulls the feature branch in via syncEpicWorkspaceLocked.
func (s *EpicExecutionService) resolveEpicRepos(ctx context.Context, epicID int64) ([]RepoBranch, error) {
	prs, err := s.listProjectReposForIssue(ctx, epicID)
	if err != nil {
		return nil, err
	}
	branch := epicBranchName(epicID)
	repos := make([]RepoBranch, 0, len(prs))
	for _, r := range prs {
		repos = append(repos, RepoBranch{RepoID: r.repoID, Branch: branch})
	}
	return repos, nil
}

// resolveEpicControlRepos resolves the epic's project repos for the epic's OWN
// orchestration/review workspace: the worktree FORKS from the epic feature branch
// (epic/<epicID>) — so it immediately holds the accumulated child work and any review
// fixes land on a branch that merges back into the feature branch — yet its diff BASE
// is the project default branch (the real baseline). When a repo has no resolvable
// default branch, Base is left empty and falls back to the fork point (legacy behaviour).
func (s *EpicExecutionService) resolveEpicControlRepos(ctx context.Context, epicID int64) ([]RepoBranch, error) {
	prs, err := s.listProjectReposForIssue(ctx, epicID)
	if err != nil {
		return nil, err
	}
	branch := epicBranchName(epicID)
	repos := make([]RepoBranch, 0, len(prs))
	for _, r := range prs {
		repos = append(repos, RepoBranch{RepoID: r.repoID, Branch: branch, Base: r.defaultBranch})
	}
	return repos, nil
}

// ensureEpicBranch makes sure the epic feature branch (epic/<epicID>) exists in
// every project repo, created from that repo's default branch. Idempotent: a
// repo that already has the branch is left untouched. Best-effort per repo: a
// failure to create one repo's branch is logged but does not abort the others
// (creation may legitimately fail in tests where repos have no on-disk path).
func (s *EpicExecutionService) ensureEpicBranch(ctx context.Context, epicID int64) error {
	prs, err := s.listProjectReposForIssue(ctx, epicID)
	if err != nil {
		return err
	}
	name := epicBranchName(epicID)
	for _, r := range prs {
		if r.path == "" {
			continue // no on-disk path (tests / not-yet-cloned); nothing to do
		}
		if git.BranchExists(r.path, name) {
			continue
		}
		base := r.defaultBranch
		if base == "" {
			if cur, cErr := git.CurrentBranch(r.path); cErr == nil {
				base = cur
			}
		}
		if base == "" {
			slog.Warn("epic ensureEpicBranch: no base branch", "epicID", epicID, "repoID", r.repoID)
			continue
		}
		if cErr := git.CreateBranchFrom(r.path, name, base); cErr != nil {
			slog.Error("epic ensureEpicBranch: create branch", "epicID", epicID, "repoID", r.repoID, "branch", name, "base", base, "error", cErr)
			continue
		}
		slog.Info("epic ensureEpicBranch: created feature branch", "epicID", epicID, "repoID", r.repoID, "branch", name, "base", base)
	}
	return nil
}

// OnWorkspaceCreated is the post-creation hook wired into WorkspaceService.Create.
// It auto-links a freshly created workspace by its issue type — the single place
// that decides what (if anything) to send the new agent, so manual and automatic
// creation behave identically:
//   - Epic issue   -> this is an orchestration workspace: mark the epic running
//     and kick its agent off with an orchestration prompt (children by wave +
//     instruction to dispatch them via the start_workspace tool).
//   - Child of an epic -> kick off with the issue's title + description so the
//     sub-task's agent starts working (covers wave-engine dispatch,
//     start_workspace dispatch, and manual creation alike).
//   - Standalone issue -> nothing; the user drives it manually (Run button).
//
// Best-effort: it runs in its own goroutine (see WorkspaceService.Create) and
// only logs on failure.
func (s *EpicExecutionService) OnWorkspaceCreated(ctx context.Context, ws store.Workspace) {
	if !ws.IssueID.Valid {
		return
	}
	issue, err := s.q.GetIssue(ctx, ws.IssueID.Int64)
	if err != nil {
		slog.Warn("epic OnWorkspaceCreated: load issue", "workspaceID", ws.ID, "error", err)
		return
	}
	switch {
	case issue.IssueType == "epic":
		// Ensure the epic feature branch exists so the control workspace (and any
		// dispatched children) build on it. Best-effort.
		if err := s.ensureEpicBranch(ctx, issue.ID); err != nil {
			slog.Warn("epic OnWorkspaceCreated: ensure epic branch", "epicID", issue.ID, "error", err)
		}
		// Pull any work already on the epic feature branch into this freshly created
		// epic workspace (which bases on the real baseline). A no-op at orchestration
		// start (the branch == baseline); for a review workspace created after children
		// have landed, it brings the accumulated work in so the review agent sees it.
		s.mergeMu.Lock()
		s.syncEpicWorkspaceLocked(ctx, issue.ID)
		s.mergeMu.Unlock()
		children, err := s.kanban.ListChildIssues(ctx, issue.ID)
		if err != nil {
			slog.Warn("epic OnWorkspaceCreated: list children", "epicID", issue.ID, "error", err)
			return
		}
		// Review phase: the wave loop already finished and the engine put the epic
		// into 'reviewing' then created this control workspace. Run a review agent
		// (autohost) without resetting exec_status back to 'running'.
		if issue.ExecStatus == "reviewing" {
			s.enableAutohost(ctx, ws.ID, issue, composeReviewGoal(issue))
			s.injectBoardMenu(ctx, ws, issue)
			s.sendKickoff(ctx, ws, composeReviewPrompt(issue, children))
			slog.Info("epic review workspace linked", "epicID", issue.ID, "workspaceID", ws.ID, "children", len(children))
			return
		}
		// Orchestration workspace: the epic is now executing via its own agent.
		if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: issue.ID}); err != nil {
			slog.Warn("epic OnWorkspaceCreated: set epic running", "epicID", issue.ID, "error", err)
		}
		// Autohost for bypass permissions + error recovery, but with the
		// auto-continue budget OFF: the orchestration agent must NOT poll child
		// status in watchdog-injected continue turns (pure token burn while
		// children run for minutes/hours). It dispatches a wave, ends its turn,
		// and is woken event-driven by notifyEpicOfChildEvent each time a child
		// reaches a terminal state. OnAgentDone's pending-children guard keeps
		// these idle pauses from being mistaken for "orchestration finished".
		s.enableAutohost(ctx, ws.ID, issue, composeOrchestrationGoal(issue))
		if err := s.wsCreator.SetWorkspaceEnv(ctx, ws.ID, "NIUNIU_AUTOHOST_BUDGET", "0"); err != nil {
			slog.Warn("epic OnWorkspaceCreated: disable autohost continue budget", "workspaceID", ws.ID, "error", err)
		}
		s.injectBoardMenu(ctx, ws, issue)
		s.sendKickoff(ctx, ws, composeOrchestrationPrompt(issue, children))
		slog.Info("epic orchestration workspace linked", "epicID", issue.ID, "workspaceID", ws.ID, "children", len(children))
	case issue.ParentIssueID.Valid:
		// Child of an epic: run in autohost mode (loop until the sub-task's
		// goal_condition judge says done), then start it on its own issue content.
		s.enableAutohost(ctx, ws.ID, issue, composeIssueMarkdown(issue.Title, nullStringVal(issue.Description)))
		s.injectBoardMenu(ctx, ws, issue)
		s.sendKickoff(ctx, ws, composeIssueMarkdown(issue.Title, nullStringVal(issue.Description)))
	default:
		// Standalone issue — execution is left to the user (no autohost kickoff,
		// manual control). But still inject the board menu so a manually-opened
		// workspace's agent — which defaults to bypassPermissions and acts on its
		// own — knows the board's processing stages and can self-report progress
		// via advance_issue. Informational only: no kickoff, no exec_status change.
		s.injectBoardMenu(ctx, ws, issue)
	}
}

// enableAutohost puts an Epic workspace into autohost mode (the agent loops
// until the LLM goal-condition judge says the task is done, then terminally
// stops) and seeds the issue's goal_condition when it has none, so the judge has
// a target. Must be called BEFORE sendKickoff (the agent reads env + goal at
// spawn). Best-effort: failures are logged only.
func (s *EpicExecutionService) enableAutohost(ctx context.Context, workspaceID int64, issue store.Issue, goal string) {
	if err := s.wsCreator.SetWorkspaceEnv(ctx, workspaceID, "NIUNIU_PERMISSION_MODE", "autohost"); err != nil {
		slog.Warn("epic enableAutohost: set env", "workspaceID", workspaceID, "error", err)
	}
	if strings.TrimSpace(issue.GoalCondition) == "" && strings.TrimSpace(goal) != "" {
		g := goal
		if len(g) > 4000 {
			g = g[:4000]
		}
		if err := s.q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{GoalCondition: g, ID: issue.ID}); err != nil {
			slog.Warn("epic enableAutohost: set goal_condition", "issueID", issue.ID, "error", err)
		}
	}
}

// composeOrchestrationGoal is the goal_condition for an Epic orchestration
// workspace: the orchestration is done when every sub-task is complete.
func composeOrchestrationGoal(epic store.Issue) string {
	return "Epic「" + epic.Title + "」编排完成：其所有子任务均已开发完成并合入。"
}

// composeReviewGoal is the goal_condition for an Epic review workspace: the
// review is done when every sub-task's expected functionality is verified (and
// any gaps fixed) on the epic feature branch.
func composeReviewGoal(epic store.Issue) string {
	return "审核 Epic「" + epic.Title + "」：核对其所有子任务是否都已在 epic 分支 " +
		epicBranchName(epic.ID) + " 上实现了预期功能；如有缺失/问题请补全并提交。"
}

// composeReviewPrompt builds the kickoff message for an Epic review workspace:
// the epic goal, the feature branch to review, and its sub-tasks so the review
// agent can cross-check each one's expected functionality.
func composeReviewPrompt(epic store.Issue, children []store.Issue) string {
	var b strings.Builder
	b.WriteString("# 审核 Epic：")
	b.WriteString(epic.Title)
	b.WriteString("\n\n")
	if d := strings.TrimSpace(nullStringVal(epic.Description)); d != "" {
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "所有子任务的工作都已合入 epic 功能分支 `%s`。请逐一核对每个子任务是否都已在该分支上实现了预期功能；如有缺失或问题，请就地补全并提交。完成后报告审核结果。\n\n", epicBranchName(epic.ID))
	if len(children) == 0 {
		b.WriteString("当前该 Epic 没有子任务。\n")
		return b.String()
	}
	b.WriteString("## 子任务\n")
	for _, c := range children {
		fmt.Fprintf(&b, "- #%d %s (状态=%s)\n", c.ID, c.Title, c.LifecycleStatus)
	}
	return b.String()
}

// OnAgentDone is invoked when an agent session finishes cleanly (EventAgentDone).
// For an Epic-managed workspace running in autohost mode this is the terminal
// "task done" signal, so the workspace is auto-marked done (which sets the issue
// lifecycle completed + publishes workspace_completed -> the epic advances / the
// orchestration epic is marked done). Non-Epic workspaces are ignored so the
// normal human needs_review gate is preserved.
func (s *EpicExecutionService) OnAgentDone(ctx context.Context, workspaceID int64) {
	if s.completer == nil {
		return
	}
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil || !ws.IssueID.Valid {
		return
	}
	issue, err := s.q.GetIssue(ctx, ws.IssueID.Int64)
	if err != nil {
		return
	}
	// Only auto-complete Epic-managed workspaces (the epic orchestration ws, or a
	// child of an epic). Everything else keeps the human review gate.
	if issue.IssueType != "epic" && !issue.ParentIssueID.Valid {
		return
	}
	// For a CHILD of an epic, integrate its work into the epic feature branch
	// before marking it done so later waves build on it. A commit/merge failure
	// (e.g. a real merge conflict) is treated as the child failing per the run's
	// failure policy, and the child is NOT marked done — but it IS a terminal
	// child event, so the orchestration agent is notified to decide the next step.
	if issue.ParentIssueID.Valid {
		if !s.mergeChildIntoEpic(ctx, workspaceID, issue) {
			s.notifyEpicOfChildEvent(ctx, issue,
				fmt.Sprintf("子任务 #%d「%s」合并到 epic 分支失败（详见其 exec 状态与原因），未能合入。", issue.ID, issue.Title))
			return
		}
	}
	// The epic orchestration agent now runs event-driven (continue budget 0): its
	// agent_done merely means "this turn ended". While any child is still pending
	// that is an idle pause awaiting child-terminal notifications — NOT the
	// orchestration finishing — so skip the finalize/confirm path entirely.
	if issue.IssueType == "epic" && issue.ExecStatus == "running" {
		if pending := s.countPendingEpicChildren(ctx, issue.ID); pending > 0 {
			slog.Info("epic OnAgentDone: orchestration idle, awaiting child events",
				"epicID", issue.ID, "workspaceID", workspaceID, "pendingChildren", pending)
			return
		}
	}
	// Epic orchestration/review workspace: the epic is the unit of user intent, so
	// before auto-finalizing run a ONE-TIME human confirmation of the review outcome
	// (产出 vs goal_condition 对账, §22.7/§23.1). Children skip this (their correctness
	// rolls up to this epic review); standalone issues never reach the auto path. The
	// confirm blocks on the user, so it runs in its own goroutine — OnAgentDone is on
	// the epic worker path and must not stall. nil confirmer => auto-finalize.
	if issue.IssueType == "epic" && s.reviewConfirmer != nil {
		if s.beginEpicConfirm(issue.ID) {
			children, _ := s.kanban.ListChildIssues(ctx, issue.ID)
			go s.confirmEpicReviewThenComplete(context.WithoutCancel(ctx), ws, issue, children)
		}
		return
	}

	// Auto trigger: run the floor gate, then finalize on green. A blocking failure
	// is handled inside RequestWorkspaceCompletion (re-engage agent / escalate to
	// attention, §22.3) — it does NOT error here, so a blocked floor gate does not
	// look like a completion failure.
	res, err := s.completer.RequestWorkspaceCompletion(ctx, workspaceID, TriggerAuto)
	if err != nil {
		slog.Warn("epic OnAgentDone: request workspace completion", "workspaceID", workspaceID, "issueID", issue.ID, "error", err)
		return
	}
	slog.Info("epic OnAgentDone: requested completion of Epic-managed workspace",
		"workspaceID", workspaceID, "issueID", issue.ID, "status", res.Status)
}

// confirmEpicReviewThenComplete asks the user to confirm the epic review outcome and,
// only on an explicit confirm, requests completion through the floor-gate choke point
// (spec §22.7/§23.1). On decline / timeout / cancel / error the epic is left in
// review (needs_review) for the user to act on — never silently finalized. Runs in its
// own goroutine; the in-flight guard (begin/endEpicConfirm) prevents a duplicate ask.
func (s *EpicExecutionService) confirmEpicReviewThenComplete(ctx context.Context, ws store.Workspace, epic store.Issue, children []store.Issue) {
	defer s.endEpicConfirm(epic.ID)
	ok, err := s.reviewConfirmer.ConfirmEpicReview(ctx, ws, epic, children)
	if err != nil {
		slog.Warn("epic review confirm: ask failed (left in review)", "epicID", epic.ID, "workspaceID", ws.ID, "error", err)
		return
	}
	if !ok {
		slog.Info("epic review confirm: not confirmed; epic left for review", "epicID", epic.ID, "workspaceID", ws.ID)
		return
	}
	res, err := s.completer.RequestWorkspaceCompletion(ctx, ws.ID, TriggerAuto)
	if err != nil {
		slog.Warn("epic review confirm: request completion after confirm", "epicID", epic.ID, "workspaceID", ws.ID, "error", err)
		return
	}
	slog.Info("epic review confirm: confirmed -> requested completion",
		"epicID", epic.ID, "workspaceID", ws.ID, "status", res.Status)
}

// beginEpicConfirm marks an epic as having an in-flight review confirmation; returns
// false if one is already awaiting the user (caller should not launch a second).
func (s *EpicExecutionService) beginEpicConfirm(epicID int64) bool {
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()
	if s.confirmingEpics == nil {
		s.confirmingEpics = make(map[int64]struct{})
	}
	if _, ok := s.confirmingEpics[epicID]; ok {
		return false
	}
	s.confirmingEpics[epicID] = struct{}{}
	return true
}

func (s *EpicExecutionService) endEpicConfirm(epicID int64) {
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()
	delete(s.confirmingEpics, epicID)
}

// mergeChildIntoEpic commits and merges every worktree of a completed child
// workspace into the epic feature branch (epic/<epicID>). Returns true on
// success (or when there is no merger wired / no worktrees, e.g. tests). On
// failure it records the child as failed via the run's failure policy and
// returns false so the caller skips marking the child done.
//
// CommitWorktree may legitimately fail when the worktree has nothing to commit;
// that is not a hard error, so a commit failure is logged and the merge is still
// attempted. A merge failure (conflict) is the real failure signal.
func (s *EpicExecutionService) mergeChildIntoEpic(ctx context.Context, workspaceID int64, child store.Issue) (ok bool) {
	if s.merger == nil {
		return true // tests / no real git: skip integration
	}
	// Serialize with every other child->epic merge (worker goroutine AND the
	// advance_issue complete-column path) so two children never race git
	// operations against the same epic branch.
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	epicID := child.ParentIssueID.Int64
	branch := epicBranchName(epicID)
	worktrees, err := s.q.ListWorktrees(ctx, workspaceID)
	if err != nil {
		slog.Error("epic mergeChildIntoEpic: list worktrees", "workspaceID", workspaceID, "error", err)
		return s.failChildIntegration(ctx, epicID, child.ID)
	}
	msg := fmt.Sprintf("epic(%d): integrate #%d %s", epicID, child.ID, child.Title)
	// Multi-repo merge is NON-ATOMIC across worktrees (spec §23.3): each repo's
	// worktree merges into the same epic/<id> branch independently, with no
	// cross-repo transaction. If repo A merges then repo B conflicts, A is already
	// integrated and cannot be rolled back generically. Rather than silently mark
	// the child 'failed' (losing the "A is polluted, B is not" fact), a PARTIAL
	// failure (>=1 repo already merged) escalates to blocked-needs-human (§19) with
	// a reason naming the merged vs failed repos, so a human resolves the split.
	var merged []string
	for _, wt := range worktrees {
		name := worktreeNameFromPath(wt.WorktreePath)
		if name == "" {
			continue
		}
		if cErr := s.merger.CommitWorktree(ctx, workspaceID, name, msg); cErr != nil {
			// Likely "nothing to commit" — not fatal; merge still proceeds.
			slog.Info("epic mergeChildIntoEpic: commit (non-fatal)", "workspaceID", workspaceID, "worktree", name, "error", cErr)
		}
		if mErr := s.merger.MergeWorktree(ctx, workspaceID, name, branch); mErr != nil {
			slog.Error("epic mergeChildIntoEpic: merge into epic branch", "workspaceID", workspaceID, "worktree", name, "branch", branch, "error", mErr)
			if len(merged) > 0 {
				// Partial cross-repo failure: some repos already integrated. Land a
				// §19 terminal state for human resolution instead of a plain failure.
				return s.escalateMultiRepoMergeSplit(ctx, workspaceID, epicID, child, merged, name, mErr)
			}
			return s.failChildIntegration(ctx, epicID, child.ID)
		}
		merged = append(merged, name)
		slog.Info("epic mergeChildIntoEpic: merged child worktree into epic branch", "workspaceID", workspaceID, "worktree", name, "branch", branch)
	}
	// Bring the freshly integrated epic branch into the epic's own workspace so it
	// reflects the accumulated child work (diffed against the real baseline). Still
	// under mergeMu, so it never races a concurrent child merge on the same branch.
	s.syncEpicWorkspaceLocked(ctx, epicID)
	return true
}

// syncEpicWorkspaceLocked fast-forwards the epic's own active workspace worktree(s)
// to the epic feature branch (epic/<epicID>), so that workspace reflects the
// accumulated child work while still basing its diff on the real baseline. The
// caller MUST hold mergeMu (this touches the same epic branch / worktrees the child
// merges do). Best-effort and safe: a missing merger or no active epic workspace is a
// no-op, and because the sync is fast-forward-only (SyncBranchIntoWorktree) a worktree
// that has diverged — e.g. an orchestration/review agent committed local edits — is
// left untouched rather than risking a conflict, with the epic feature branch
// remaining the source of truth.
func (s *EpicExecutionService) syncEpicWorkspaceLocked(ctx context.Context, epicID int64) {
	if s.merger == nil {
		return
	}
	epicWs, ok := s.activeWorkspaceForIssue(ctx, epicID)
	if !ok || epicWs.ID == 0 {
		return // epic ran without an active workspace (e.g. headless); nothing to sync
	}
	branch := epicBranchName(epicID)
	worktrees, err := s.q.ListWorktrees(ctx, epicWs.ID)
	if err != nil {
		slog.Warn("epic syncEpicWorkspace: list worktrees", "epicID", epicID, "workspaceID", epicWs.ID, "error", err)
		return
	}
	for _, wt := range worktrees {
		name := worktreeNameFromPath(wt.WorktreePath)
		if name == "" {
			continue
		}
		if err := s.merger.SyncBranchIntoWorktree(ctx, epicWs.ID, name, branch); err != nil {
			slog.Warn("epic syncEpicWorkspace: merge epic branch into epic workspace (non-fatal)",
				"epicID", epicID, "workspaceID", epicWs.ID, "worktree", name, "branch", branch, "error", err)
			continue
		}
		slog.Info("epic syncEpicWorkspace: synced epic branch into epic workspace",
			"epicID", epicID, "workspaceID", epicWs.ID, "worktree", name, "branch", branch)
	}
}

// escalateMultiRepoMergeSplit lands a child whose multi-repo integration partially
// failed in the blocked-needs-human terminal state (spec §23.3/§19): repo merge is
// non-atomic, so when some repos merged and another conflicted, neither auto-rollback
// nor a clean retry is safe. The reason names the integrated vs failed repos, an
// exec-timeline event records it, and the workspace flips to attention. Returns false
// so the caller does NOT mark the child done.
func (s *EpicExecutionService) escalateMultiRepoMergeSplit(ctx context.Context, workspaceID, epicID int64, child store.Issue, merged []string, failedRepo string, mErr error) bool {
	reason := fmt.Sprintf("多仓合并部分失败(非原子): 已并入 epic 分支 [%s], 仓库 [%s] 冲突未并(%v); 需人工解决合并冲突或回退已并入的改动",
		strings.Join(merged, ", "), failedRepo, mErr)
	slog.Warn("epic mergeChildIntoEpic: partial multi-repo merge, escalating to blocked-needs-human",
		"workspaceID", workspaceID, "epicID", epicID, "childID", child.ID, "merged", merged, "failedRepo", failedRepo)
	if err := s.q.SetIssueExecStatusWithReason(ctx, store.SetIssueExecStatusWithReasonParams{
		ExecStatus:       "gate_blocked",
		ExecStatusReason: sql.NullString{String: reason, Valid: true},
		ID:               child.ID,
	}); err != nil {
		slog.Error("epic mergeChildIntoEpic: set child gate_blocked", "childID", child.ID, "error", err)
	}
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{IssueID: child.ID, WorkspaceID: workspaceID, Kind: "terminal", Summary: "blocked-needs-human：" + reason})
	}
	s.flipWorkspaceAttention(ctx, child.ID)
	// The child is parked in a terminal blocked-needs-human state; the orchestration
	// agent observes it and decides how to proceed. epicID is retained in the
	// signature for the log context above; no in-process epic failure policy applies.
	_ = epicID
	return false
}

// failChildIntegration records a child whose commit/merge failed as 'failed' so the
// orchestration agent (and the card projection) sees it did not integrate, and returns
// false so the caller does NOT mark it done. Failure strategy on the orchestration path
// is the agent's call; this only persists the terminal child state.
func (s *EpicExecutionService) failChildIntegration(ctx context.Context, epicID, childID int64) bool {
	_ = epicID
	s.applyChildFailure(ctx, childID)
	return false
}

// worktreeNameFromPath extracts the worktree directory name (the value
// CommitWorktree/MergeWorktree resolve against ListWorktreeGroups) from a stored
// worktree path. Worktrees live at <workspace>/.worktrees/<name>.
func worktreeNameFromPath(p string) string {
	p = strings.TrimRight(p, "/\\")
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// sendKickoff delivers message to the workspace agent. Queues if the session is
// running, starts a SendLoop otherwise. No-op when proxy not wired or workspace
// has no path.
func (s *EpicExecutionService) sendKickoff(ctx context.Context, ws store.Workspace, message string) {
	if s.agentProxy == nil || ws.ID == 0 || ws.Path == "" || message == "" {
		return
	}
	s.agentProxy.Deliver(ctx, ws.ID, ws.Path, message, "")
}

// composeIssueMarkdown renders an issue's title + description as the kickoff
// prompt: an H1 title followed by the description (omitted when empty).
func composeIssueMarkdown(title, description string) string {
	desc := strings.TrimSpace(description)
	if desc == "" {
		return "# " + title
	}
	return "# " + title + "\n\n" + desc
}

// composeOrchestrationPrompt builds the kickoff message for an Epic orchestration
// workspace: the epic goal plus its sub-tasks grouped by wave, and the
// event-driven orchestration contract — dispatch a wave, end the turn, and act
// again only when the system delivers a child-terminal notification (the
// workspace runs with the autohost continue budget disabled, so there is no
// watchdog poll loop to fall back on).
func composeOrchestrationPrompt(epic store.Issue, children []store.Issue) string {
	var b strings.Builder
	b.WriteString("# 编排执行 Epic：")
	b.WriteString(epic.Title)
	b.WriteString("\n\n")
	if d := strings.TrimSpace(nullStringVal(epic.Description)); d != "" {
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	b.WriteString("你是该 Epic 的编排者。请按波次(wave)推进它的子任务：同一波内可并行，跨波需串行(先把低波次的子任务都完成，再进入下一波)。\n")
	b.WriteString("对每个要启动的子任务，调用 MCP 工具 `start_workspace(issue_id)` 为它创建并启动独立工作空间(各子任务会用自己的标题+描述自动起跑)。\n")
	b.WriteString("派发完当前可启动的子任务后，直接结束本回合：每当一个子任务完成或失败，系统会自动发消息通知你并附上各子任务最新状态，届时再决定派发下一波、处理失败或收尾。不要轮询子任务状态，不要 sleep/循环等待。\n")
	b.WriteString("当所有子任务均已完成并合入 epic 分支后，在回复末尾单独输出一行：" + autohostDoneSentinel + "\n\n")
	if len(children) == 0 {
		b.WriteString("当前该 Epic 还没有子任务。\n")
		return b.String()
	}
	b.WriteString("## 子任务(按波次)\n")
	curWave := int64(-1)
	for _, c := range children {
		if c.ExecWave != curWave {
			curWave = c.ExecWave
			fmt.Fprintf(&b, "\n第 %d 波:\n", curWave)
		}
		status := c.LifecycleStatus
		fmt.Fprintf(&b, "- #%d %s (issue_id=%d, 状态=%s)\n", c.ID, c.Title, c.ID, status)
	}
	return b.String()
}

// applyChildFailure persists a child issue's terminal 'failed' exec_status (event
// 收口). It is a capability tool the integration path calls when a child cannot be
// merged; failure strategy (retry / abandon / continue) on the orchestration path is
// the orchestration agent's decision, not an in-process policy.
func (s *EpicExecutionService) applyChildFailure(ctx context.Context, childID int64) {
	if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{ExecStatus: "failed", ID: childID}); err != nil {
		slog.Error("epic applyChildFailure: set child failed", "error", err, "childID", childID)
	}
}

// terminateEpic writes the epic's terminal exec_status (done/failed). Used by
// OnWorkspaceCompleted's epic-收口 path when an orchestration workspace finishes.
func (s *EpicExecutionService) terminateEpic(ctx context.Context, epicID int64, status string) {
	if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{ExecStatus: status, ID: epicID}); err != nil {
		slog.Error("epic terminate: set exec_status", "error", err, "epicID", epicID, "status", status)
	}
	slog.Info("epic execution terminated", "epicID", epicID, "status", status)
}

// OnWorkspaceCompleted is the event-收口 entrypoint invoked when a workspace
// completes (the event goroutine calls it; tests call it directly). It is a
// capability tool — NOT a wave scheduler: it only persists the completing issue's
// terminal exec_status so the orchestration agent and the card projection see an
// accurate state. There is no wave advance; the orchestration agent drives what
// happens next.
//
//   - Epic issue (its orchestration/review workspace finished): write the epic's
//     terminal exec_status (done/failed) back — the §8 "事件收口" path.
//   - Child of an epic: record the child's terminal exec_status (done/failed) so it
//     is not left dangling 'running'.
//
// Failure signal: EventWorkspaceCompleted is published with success=true by
// WorkspaceOpsService.MarkWorkspaceDone (completed) and with success=false by
// WorkspaceService.Archive when an issue-linked workspace is torn down without
// completing (abandoned).
func (s *EpicExecutionService) OnWorkspaceCompleted(ctx context.Context, issueID int64, success bool) {
	child, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		slog.Error("epic OnWorkspaceCompleted: load child", "error", err, "issueID", issueID)
		return
	}
	// Orchestration-workspace path: the completing workspace's issue is itself an
	// Epic (its own orchestration agent finished). Write the result back to the
	// epic's exec_status so the orchestration capability is observable end-to-end.
	if child.IssueType == "epic" {
		status := "done"
		if !success {
			status = "failed"
		}
		s.terminateEpic(ctx, issueID, status)
		return
	}
	if !child.ParentIssueID.Valid {
		return // not a child of any epic
	}
	// Child of an epic: persist its terminal exec_status (收口). On failure mark it
	// 'failed'; on success mark it 'done'. Then notify the epic's orchestration
	// agent — this child-terminal event IS the orchestration check trigger (the
	// agent no longer polls): it wakes once, inspects the attached sibling status,
	// and dispatches the next wave / handles the failure / wraps up.
	if !success {
		s.applyChildFailure(ctx, issueID)
		s.notifyEpicOfChildEvent(ctx, child,
			fmt.Sprintf("子任务 #%d「%s」执行失败（工作空间未完成即被终止）。", child.ID, child.Title))
		return
	}
	if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{ExecStatus: "done", ID: issueID}); err != nil {
		slog.Error("epic OnWorkspaceCompleted: set child done", "error", err, "childID", issueID)
	}
	// Suppress only a duplicate SUCCESS notification (the child was already done);
	// failure notifications are never suppressed — a missed one stalls the epic,
	// a duplicate one costs a single cheap orchestration turn.
	if child.ExecStatus != "done" {
		s.notifyEpicOfChildEvent(ctx, child,
			fmt.Sprintf("子任务 #%d「%s」已完成，代码已合入 epic 分支 %s。", child.ID, child.Title, epicBranchName(child.ParentIssueID.Int64)))
	}
}

// childTerminalExecStatuses are the exec_status values that mean a child needs no
// further orchestration work (it finished, or it is parked for a human/agent
// decision). Anything else counts as pending for the OnAgentDone guard.
func isChildTerminalExecStatus(execStatus string) bool {
	switch execStatus {
	case "done", "failed", "gate_blocked", "abandoned":
		return true
	}
	return false
}

// countPendingEpicChildren counts the epic's children that are neither
// lifecycle-completed nor in a terminal exec_status — i.e. work the orchestration
// agent still has to drive. Used by OnAgentDone to tell "idle pause between child
// events" apart from "orchestration genuinely finished". Best-effort: a load error
// returns 0 so a broken read degrades to the legacy finalize path rather than
// stranding the epic forever.
func (s *EpicExecutionService) countPendingEpicChildren(ctx context.Context, epicID int64) int {
	children, err := s.kanban.ListChildIssues(ctx, epicID)
	if err != nil {
		slog.Warn("epic countPendingEpicChildren: list children", "epicID", epicID, "error", err)
		return 0
	}
	pending := 0
	for _, c := range children {
		if c.LifecycleStatus != "completed" && !isChildTerminalExecStatus(c.ExecStatus) {
			pending++
		}
	}
	return pending
}

// notifyEpicOfChildEvent delivers a child-terminal notice to the epic's active
// orchestration workspace agent (queues if its session is running, wakes it
// otherwise). This is the event-driven replacement for the watchdog continue-poll:
// the orchestration agent checks child state exactly once per child-terminal event
// instead of every idle turn. Best-effort: no proxy / no active epic workspace =>
// log only (the human can still drive the board).
func (s *EpicExecutionService) notifyEpicOfChildEvent(ctx context.Context, child store.Issue, headline string) {
	if s.agentProxy == nil || !child.ParentIssueID.Valid {
		return
	}
	epicID := child.ParentIssueID.Int64
	ws, ok := s.activeWorkspaceForIssue(ctx, epicID)
	if !ok || ws.Path == "" {
		slog.Info("epic notifyEpicOfChildEvent: no active orchestration workspace", "epicID", epicID, "childID", child.ID)
		return
	}
	epic, err := s.q.GetIssue(ctx, epicID)
	if err != nil {
		slog.Warn("epic notifyEpicOfChildEvent: load epic", "epicID", epicID, "error", err)
		return
	}
	children, err := s.kanban.ListChildIssues(ctx, epicID)
	if err != nil {
		slog.Warn("epic notifyEpicOfChildEvent: list children", "epicID", epicID, "error", err)
	}
	if _, _, err := s.agentProxy.Deliver(ctx, ws.ID, ws.Path, composeChildTerminalNotice(epic, children, headline), ""); err != nil {
		slog.Warn("epic notifyEpicOfChildEvent: deliver", "epicID", epicID, "workspaceID", ws.ID, "error", err)
		return
	}
	slog.Info("epic notifyEpicOfChildEvent: orchestration agent notified",
		"epicID", epicID, "workspaceID", ws.ID, "childID", child.ID)
}

// composeChildTerminalNotice builds the event-driven orchestration check prompt:
// the terminal-child headline, a fresh status list of ALL siblings (so the agent
// does not spend a tool call re-reading the board), and the decision menu. It
// restates the no-polling contract and the stop sentinel because the orchestration
// workspace runs with continue budget 0 — this message IS its continue prompt.
func composeChildTerminalNotice(epic store.Issue, children []store.Issue, headline string) string {
	var b strings.Builder
	b.WriteString("【子任务事件】")
	b.WriteString(headline)
	b.WriteString("\n\n")
	if len(children) > 0 {
		fmt.Fprintf(&b, "Epic「%s」当前各子任务状态:\n", epic.Title)
		curWave := int64(-1)
		for _, c := range children {
			if c.ExecWave != curWave {
				curWave = c.ExecWave
				fmt.Fprintf(&b, "第 %d 波:\n", curWave)
			}
			fmt.Fprintf(&b, "- #%d %s (issue_id=%d, 生命周期=%s, 执行=%s)\n", c.ID, c.Title, c.ID, c.LifecycleStatus, c.ExecStatus)
		}
		b.WriteString("\n")
	}
	b.WriteString("请基于上述状态推进编排（只做必要动作，控制 token 消耗）：\n")
	b.WriteString("- 若当前波次已全部完成且存在下一波，调用 `start_workspace(issue_id)` 派发下一波子任务；\n")
	b.WriteString("- 若有失败/阻塞的子任务，判断是重试（start_workspace 复用其工作空间）、还是放弃（abandon_issue）；\n")
	b.WriteString("- 若所有子任务均已完成并合入，在回复末尾单独输出一行：" + autohostDoneSentinel + "\n")
	b.WriteString("- 处理完后直接结束回合等待下一次通知；不要轮询子任务状态，不要 sleep 等待。\n")
	return b.String()
}

// autohostDoneSentinel mirrors agentproxy's [AUTOHOST_DONE] stop sentinel. The
// orchestration prompts must spell it out themselves because the orchestration
// workspace runs with the continue budget disabled — the watchdog never injects
// the condition prompt that normally carries it.
const autohostDoneSentinel = "[AUTOHOST_DONE]"

// drainAfterCompletion resolves the completing issue's owner and asks the
// orchestration guard to start any queued workspaces now that a slot has freed.
// nil-safe: no guard wired -> no-op.
func (s *EpicExecutionService) drainAfterCompletion(ctx context.Context, issueID int64) {
	if s.guard == nil {
		return
	}
	ownerRow, err := s.q.GetProjectAndLifecycleByIssueID(ctx, issueID)
	if err != nil {
		slog.Warn("epic drainAfterCompletion: resolve owner", "issueID", issueID, "error", err)
		return
	}
	s.guard.OnSlotFreed(ctx, ownerRow.ProjectOwnerType, ownerRow.ProjectOwnerID)
}

// GetEpicProgress derives execution progress for an epic from its children:
// done = count of lifecycle-completed children, total = child count. execStatus
// is the epic issue's current exec_status.
func (s *EpicExecutionService) GetEpicProgress(ctx context.Context, epicID int64) (done, total int, execStatus string, err error) {
	epic, err := s.q.GetIssue(ctx, epicID)
	if err != nil {
		return 0, 0, "", fmt.Errorf("load epic %d: %w", epicID, err)
	}
	children, err := s.kanban.ListChildIssues(ctx, epicID)
	if err != nil {
		return 0, 0, "", fmt.Errorf("list child issues: %w", err)
	}
	total = len(children)
	for _, c := range children {
		if c.LifecycleStatus == "completed" {
			done++
		}
	}
	return done, total, epic.ExecStatus, nil
}

// Start launches a single background goroutine that subscribes to the event bus
// and forwards workspace_completed events into OnWorkspaceCompleted. Nil-safe:
// when bus is nil (tests), it is a no-op. The subscription lives for the
// server's lifetime; Stop unsubscribes and ends the goroutine.
func (s *EpicExecutionService) Start() {
	if s.bus == nil {
		return
	}
	s.mu.Lock()
	if s.subCh != nil {
		s.mu.Unlock()
		return // already started
	}
	ch := s.bus.Subscribe()
	s.subCh = ch
	// Buffer comfortably exceeds any realistic burst of terminal events (children
	// per epic), so the consumer's send never blocks in practice and stays hot.
	work := make(chan epicJob, 256)
	s.workCh = work
	quit := make(chan struct{})
	s.quit = quit
	s.mu.Unlock()

	// Worker: runs the state-machine handlers (DB writes + slow git merges /
	// worktree creation) serially, OFF the bus goroutine.
	go func() {
		for {
			select {
			case <-quit:
				return
			case job := <-work:
				switch job.kind {
				case jobWorkspaceCompleted:
					s.OnWorkspaceCompleted(context.Background(), job.issueID, job.success)
					// A workspace finishing frees an owner slot: drain that owner's
					// start queue so a deferred start_workspace can now run.
					s.drainAfterCompletion(context.Background(), job.issueID)
				case jobAgentDone:
					// An agent finished cleanly. For an Epic-managed workspace in
					// autohost mode this is the terminal "task done" signal, so
					// integrate + auto-complete it (which publishes
					// workspace_completed -> advance).
					s.OnAgentDone(context.Background(), job.workspaceID)
				}
			}
		}
	}()

	// Bus consumer: stays hot. Only cheap decode + forward of the two terminal
	// event types; the high-frequency agent-output flood is skipped here without
	// blocking, so the 64-entry bus buffer never overflows and terminal events
	// are not dropped.
	go func() {
		for {
			select {
			case <-quit:
				return
			case ev, ok := <-ch:
				if !ok {
					return // bus closed
				}
				switch ev.Type {
				case event.EventWorkspaceCompleted:
					var p event.WorkspaceCompletedEvent
					if err := json.Unmarshal([]byte(ev.Content), &p); err != nil {
						slog.Error("epic engine: decode workspace_completed", "error", err)
						continue
					}
					if p.IssueID == 0 {
						continue
					}
					select {
					case work <- epicJob{kind: jobWorkspaceCompleted, issueID: p.IssueID, success: p.Success}:
					case <-quit:
						return
					}
				case event.EventAgentDone:
					if ev.WorkspaceId != 0 {
						select {
						case work <- epicJob{kind: jobAgentDone, workspaceID: ev.WorkspaceId}:
						case <-quit:
							return
						}
					}
				}
			}
		}
	}()
}

// Stop unsubscribes the event-bus channel, ending the Start goroutine. Safe to
// call when never started or when bus is nil.
func (s *EpicExecutionService) Stop() {
	s.mu.Lock()
	ch := s.subCh
	s.subCh = nil
	q := s.quit
	s.quit = nil
	s.workCh = nil
	s.mu.Unlock()
	if q != nil {
		close(q) // end consumer + worker goroutines
	}
	if ch != nil && s.bus != nil {
		s.bus.Unsubscribe(ch)
	}
}
