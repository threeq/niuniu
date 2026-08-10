package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// memory_orchestrator.go drives automatic project-memory maintenance through an
// AGENT TASK rather than direct DB writes. Both the manual "run once" button and
// the per-project schedule funnel through the same flow:
//
//  1. create a maintenance issue (title + prompt) on the project's board;
//  2. start a STANDALONE workspace for that issue (autohost) and deliver the
//     prompt as the kickoff — this uses the kanban's standalone-issue run path
//     (EpicExecutionService.EnsureWorkspaceForIssue), so it needs NO special
//     column. The agent uses niuniu-mcp's reversible memory tools to evolve/
//     correct the library;
//  3. leave the issue + workspace in place (visible to the user — no auto
//     cleanup), and a lightweight background poll backfills the run log with the
//     workspace's token/cost consumption once it finishes.
//
// This replaces the former bespoke staleness engine (shallow-clone + claude -p
// judge writing the DB directly): every memory change goes through the agent and
// niuniu-mcp, so it is auditable and reversible. Projects without a repository are
// skipped (nothing to reconcile against); the per-project schedule defaults OFF
// (empty cron) — a user opts in per project, or triggers a single run manually.

// MemoryMaintIssueTitle is the title of the auto-created maintenance issue.
const MemoryMaintIssueTitle = "项目看板记忆库整理"

// memoryMaintPrompt is the issue detail / kickoff handed to the maintenance
// agent. It runs inside a workspace that already has the project's repos checked
// out and niuniu-mcp wired, so it can read the code and call the reversible
// memory tools directly.
const memoryMaintPrompt = `你是本项目的记忆库维护代理。请整理本看板项目的记忆库，使其与项目当前代码保持一致。

工作空间已检出本项目的全部仓库，并已接入 niuniu-mcp，可直接读写项目记忆库。

请按以下步骤执行：
1. 用 memory_search 列出本项目的现有记忆条目。
2. 对照工作空间中的实际代码逐条核对：
   - 记忆与当前代码矛盾、或其引用的文件/符号已不存在 → 用 memory_update 修正，或用 memory_delete 归档（软删除，可逆）。
   - 仍然成立的条目保持不变。
   - 发现值得记录的新模式/坑/决策，可用 memory_generate 适度补充。
3. 全部改动都通过 niuniu-mcp 工具完成；不要直接改数据库，也不要改业务代码。
4. 完成后简要说明本次修正了哪些条目。

务必保守：仅当当前代码明确与记忆冲突时才修改或删除；无法确认时保持原样。`

// MaintBoard is the minimal surface the memory-maintenance orchestrator drives.
// Production wires it to the real Kanban / Workspace / Epic-execution services
// (NewMemoryMaintBoard); tests inject a fake to exercise the full flow without a
// live agent.
type MaintBoard interface {
	// Columns returns the project's columns ordered by position (the issue is
	// created in the first one).
	Columns(ctx context.Context, projectID int64) ([]store.Column, error)
	// CreateIssue creates an issue in the given column and returns its id.
	CreateIssue(ctx context.Context, columnID int64, title, detail string) (int64, error)
	// StartIssueStandalone creates+runs a workspace for the issue in autohost mode
	// and delivers prompt as the kickoff, WITHOUT requiring any special column.
	// Returns the workspace id.
	StartIssueStandalone(ctx context.Context, issueID, callerUserID int64, prompt string) (int64, error)
	// HasRepository reports whether the project has at least one attached repo.
	HasRepository(ctx context.Context, projectID int64) (bool, error)
	// WorkspaceProgress reports a workspace's run state and accumulated
	// consumption: running (agent session active), cost (USD), turns and the
	// number of recorded interactions (0 until the agent has actually run).
	WorkspaceProgress(ctx context.Context, workspaceID int64) (running bool, costUSD float64, turns, interactions int64, err error)
}

// ErrNoRepository means the project has no attached repository, so there is no
// code to reconcile the memory against.
var ErrNoRepository = errors.New("project has no repository")

// memoryMaintBoard is the production MaintBoard, adapting the existing Kanban,
// Workspace and Epic-execution services plus a few read queries.
type memoryMaintBoard struct {
	kanban *KanbanService
	ws     *WorkspaceService
	q      *store.Queries
	epic   *EpicExecutionService
}

// NewMemoryMaintBoard builds the production MaintBoard.
func NewMemoryMaintBoard(kanban *KanbanService, ws *WorkspaceService, q *store.Queries, epic *EpicExecutionService) MaintBoard {
	return &memoryMaintBoard{kanban: kanban, ws: ws, q: q, epic: epic}
}

func (b *memoryMaintBoard) Columns(ctx context.Context, projectID int64) ([]store.Column, error) {
	return b.q.ListColumnsByProject(ctx, projectID)
}

func (b *memoryMaintBoard) CreateIssue(ctx context.Context, columnID int64, title, detail string) (int64, error) {
	d, err := b.kanban.CreateIssue(ctx, columnID, title, detail, 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	if err != nil {
		return 0, err
	}
	return d.ID, nil
}

func (b *memoryMaintBoard) StartIssueStandalone(ctx context.Context, issueID, callerUserID int64, prompt string) (int64, error) {
	ws, err := b.epic.StartStandaloneIssueWithPrompt(ctx, issueID, callerUserID, prompt)
	if err != nil {
		return 0, err
	}
	return ws.ID, nil
}

func (b *memoryMaintBoard) HasRepository(ctx context.Context, projectID int64) (bool, error) {
	repos, err := b.q.ListProjectRepositories(ctx, projectID)
	if err != nil {
		return false, err
	}
	return len(repos) > 0, nil
}

func (b *memoryMaintBoard) WorkspaceProgress(ctx context.Context, workspaceID int64) (bool, float64, int64, int64, error) {
	w, err := b.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return false, 0, 0, 0, err
	}
	running := w.SessionStatus.Valid && w.SessionStatus.String == "running"
	sum, err := b.q.GetWorkspaceCostSummary(ctx, workspaceID)
	if err != nil {
		return running, 0, 0, 0, err
	}
	return running, sum.TotalCostUsd, sum.TotalTurns, sum.TotalInteractions, nil
}

// MemoryMaintConfig tunes the background completion poll.
type MemoryMaintConfig struct {
	Poll    time.Duration // how often to re-check the workspace
	Timeout time.Duration // stop polling (consumption stays at last-seen) after this
}

// DefaultMemoryMaintConfig: check every 15s, give the agent up to 30 minutes.
func DefaultMemoryMaintConfig() MemoryMaintConfig {
	return MemoryMaintConfig{Poll: 15 * time.Second, Timeout: 30 * time.Minute}
}

// MemoryMaintResult summarizes one maintenance run; it is the JSON detail stored
// in the run log (and backfilled when the workspace finishes).
type MemoryMaintResult struct {
	IssueID     int64   `json:"issue_id"`
	WorkspaceID int64   `json:"workspace_id"`
	Completed   bool    `json:"completed"`
	TimedOut    bool    `json:"timed_out"`
	CostUSD     float64 `json:"cost_usd"`
	Turns       int64   `json:"turns"`
	Error       string  `json:"error,omitempty"`
}

// StartMemoryMaintenanceOnce is the manual "run once" trigger. trigger="manual".
func (s *MemoryService) StartMemoryMaintenanceOnce(ctx context.Context, board MaintBoard, projectID, callerUserID int64) (MemoryMaintResult, error) {
	return s.beginMaintenance(ctx, board, projectID, "manual", callerUserID)
}

// beginMaintenance runs the synchronous prelude — repo check, issue creation, run
// log — then spawns the standalone workspace + completion poll in the background.
// The synchronous part is fast (so "no repository" surfaces as an error and the
// issue appears immediately); the workspace start + poll outlive the caller. It
// never auto-deletes: the issue and workspace stay visible (A1).
func (s *MemoryService) beginMaintenance(ctx context.Context, board MaintBoard, projectID int64, trigger string, callerUserID int64) (MemoryMaintResult, error) {
	var res MemoryMaintResult

	hasRepo, err := board.HasRepository(ctx, projectID)
	if err != nil {
		return res, err
	}
	if !hasRepo {
		return res, ErrNoRepository
	}

	cols, err := board.Columns(ctx, projectID)
	if err != nil {
		return res, err
	}
	if len(cols) == 0 {
		return res, errors.New("project has no columns")
	}

	issueID, err := board.CreateIssue(ctx, cols[0].ID, MemoryMaintIssueTitle, memoryMaintPrompt)
	if err != nil {
		return res, err
	}
	res.IssueID = issueID
	runID := s.recordMaintRun(ctx, projectID, trigger, res)

	// Start the workspace + poll for completion out of band: a worktree can take
	// seconds to create and the agent runs for minutes. Detached context so the
	// run outlives the HTTP request.
	go s.driveMaintenance(context.Background(), board, projectID, issueID, runID, callerUserID, DefaultMemoryMaintConfig())
	return res, nil
}

// driveMaintenance starts the standalone workspace for the maintenance issue,
// then polls until the agent finishes (or the timeout), backfilling the run log
// with the workspace id and its token/cost consumption (B2). It is synchronous
// and side-effect-complete, so tests can call it directly.
func (s *MemoryService) driveMaintenance(ctx context.Context, board MaintBoard, projectID, issueID, runID, callerUserID int64, cfg MemoryMaintConfig) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("memory maintenance drive panicked", "projectID", projectID, "issueID", issueID, "recover", r)
		}
	}()

	res := MemoryMaintResult{IssueID: issueID}
	wsID, err := board.StartIssueStandalone(ctx, issueID, callerUserID, memoryMaintPrompt)
	if err != nil {
		slog.Warn("memory maintenance: start standalone workspace failed", "projectID", projectID, "issueID", issueID, "error", err)
		res.Error = err.Error()
		s.updateMaintRun(ctx, runID, res)
		return
	}
	res.WorkspaceID = wsID
	s.updateMaintRun(ctx, runID, res) // mark started (workspace visible)
	slog.Info("memory maintenance: started", "projectID", projectID, "issueID", issueID, "workspaceID", wsID)

	completed, cost, turns := pollWorkspaceDone(ctx, board, wsID, cfg)
	res.Completed = completed
	res.TimedOut = !completed
	res.CostUSD = cost
	res.Turns = turns
	s.updateMaintRun(ctx, runID, res)
	slog.Info("memory maintenance: finished", "projectID", projectID, "workspaceID", wsID, "completed", completed, "costUSD", cost)
}

// pollWorkspaceDone polls a workspace until its agent has run and gone idle
// (interactions>0 && !running) or the timeout elapses. It returns whether it
// completed and the last-seen consumption (so a timeout still reports cost).
func pollWorkspaceDone(ctx context.Context, board MaintBoard, wsID int64, cfg MemoryMaintConfig) (bool, float64, int64) {
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultMemoryMaintConfig().Poll
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultMemoryMaintConfig().Timeout
	}
	timeout := time.NewTimer(cfg.Timeout)
	defer timeout.Stop()
	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()
	var cost float64
	var turns int64
	for {
		running, c, t, interactions, err := board.WorkspaceProgress(ctx, wsID)
		if err == nil {
			cost, turns = c, t
			if !running && interactions > 0 {
				return true, cost, turns
			}
		}
		select {
		case <-ctx.Done():
			return false, cost, turns
		case <-timeout.C:
			return false, cost, turns
		case <-ticker.C:
		}
	}
}

// recordMaintRun appends a run to memory_sweep_runs (user-visible log) and returns
// its id for later backfill. The per-memory changes happen inside the agent
// (invisible here), so scanned/auto_deleted/queued stay 0; the outcome and
// consumption live in the JSON detail.
func (s *MemoryService) recordMaintRun(ctx context.Context, projectID int64, trigger string, res MemoryMaintResult) int64 {
	detail := ""
	if b, err := json.Marshal(res); err == nil {
		detail = string(b)
	}
	row, err := s.q.CreateMemorySweepRun(ctx, store.CreateMemorySweepRunParams{
		ProjectID:   projectID,
		Trigger:     trigger,
		Scanned:     0,
		AutoDeleted: 0,
		Queued:      0,
		Detail:      detail,
	})
	if err != nil {
		slog.Warn("memory maintenance: record run failed", "projectID", projectID, "error", err)
		return 0
	}
	return row.ID
}

// updateMaintRun backfills a run's JSON detail (workspace id + consumption).
func (s *MemoryService) updateMaintRun(ctx context.Context, runID int64, res MemoryMaintResult) {
	if runID == 0 {
		return
	}
	detail := ""
	if b, err := json.Marshal(res); err == nil {
		detail = string(b)
	}
	if err := s.q.UpdateMemorySweepRunDetail(ctx, store.UpdateMemorySweepRunDetailParams{Detail: detail, ID: runID}); err != nil {
		slog.Warn("memory maintenance: update run failed", "runID", runID, "error", err)
	}
}

// MemoryMaintEnabled reports whether automatic maintenance is enabled for a
// project. The schedule doubles as the on/off switch: a non-empty cron means
// enabled, empty means disabled (the default for new projects).
func (s *MemoryService) MemoryMaintEnabled(ctx context.Context, projectID int64) (bool, error) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(p.MemorySweepCron) != "", nil
}

// StartMemoryMaintenanceScheduler launches a background goroutine that, once an
// hour, runs maintenance for every active project whose own cron schedule is due
// (read fresh each cycle). "Due" is computed from the last recorded run, so it
// survives restarts. Returns immediately; stops when ctx is done. A nil board
// disables the scheduler.
func (s *MemoryService) StartMemoryMaintenanceScheduler(ctx context.Context, board MaintBoard) {
	if board == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			s.runDueMaintenance(ctx, board)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// runDueMaintenance runs one due-check cycle over all active projects.
func (s *MemoryService) runDueMaintenance(ctx context.Context, board MaintBoard) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("memory maintenance scheduler panicked", "recover", r)
		}
	}()
	projects, err := s.q.ListProjects(ctx, "active")
	if err != nil {
		slog.Warn("memory maintenance: list projects failed", "error", err)
		return
	}
	now := time.Now()
	for _, p := range projects {
		s.maybeRunProjectMaintenance(ctx, board, p, now)
	}
}

// maybeRunProjectMaintenance runs maintenance for one project if it is enabled,
// has a repository, and its schedule is due. Each project is isolated by its own
// panic recovery.
func (s *MemoryService) maybeRunProjectMaintenance(ctx context.Context, board MaintBoard, p store.Project, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("memory maintenance panicked for project", "projectID", p.ID, "recover", r)
		}
	}()

	expr := strings.TrimSpace(p.MemorySweepCron)
	if expr == "" {
		return // disabled (default OFF)
	}
	repos, err := s.q.ListProjectRepositories(ctx, p.ID)
	if err != nil || len(repos) == 0 {
		return // repo-less project: nothing to reconcile against
	}
	sched, perr := cron.ParseStandard(expr)
	if perr != nil {
		slog.Warn("memory maintenance: bad cron, skipping", "projectID", p.ID, "cron", expr, "error", perr)
		return
	}
	runs, rErr := s.q.ListMemorySweepRunsForProject(ctx, store.ListMemorySweepRunsForProjectParams{ProjectID: p.ID, Limit: 5})
	if rErr == nil && !cronDue(sched, runs, now) {
		return // not yet due per the project's schedule
	}
	if _, err := s.beginMaintenance(ctx, board, p.ID, "schedule", 0); err != nil {
		slog.Warn("scheduled memory maintenance failed", "projectID", p.ID, "error", err)
	}
}
