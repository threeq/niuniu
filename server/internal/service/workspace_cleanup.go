package service

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// workspace_cleanup.go implements a per-project workspace auto-cleanup policy.
//
// A project can opt in (default OFF) to have an hourly background sweeper remove
// workspaces — together with their linked issue — that are no longer useful:
// those whose issue is either已完成 (completed) or未开始 (not started) AND that
// have had no activity for at least N days. This is a GC-style maintenance pass
// that operates ON workspaces from the outside; it never runs anything inside a
// workspace, so it does not conflict with the "no global workspace-executed
// sweep" rule in docs/architecture/workspace-model.md.
//
// The design mirrors the per-project memory-maintenance scheduler
// (memory_orchestrator.go): the config lives as columns on the projects table,
// and an hourly ticker re-reads each active project's policy fresh every cycle.

// Cleanup status categories. These are the closed set stored (comma-separated) in
// projects.cleanup_statuses.
const (
	// CleanupCategoryCompleted covers issues that reached a terminal done state:
	// lifecycle_status == 'completed', or exec_status in {'done','abandoned'}.
	CleanupCategoryCompleted = "completed"
	// CleanupCategoryNotStarted covers issues that were never picked up:
	// lifecycle_status == 'created' AND exec_status == 'idle'.
	CleanupCategoryNotStarted = "not_started"
)

// DefaultCleanupStatuses is the policy applied to new projects at the schema
// level (projects.cleanup_statuses default). Both categories are targeted.
var DefaultCleanupStatuses = []string{CleanupCategoryCompleted, CleanupCategoryNotStarted}

// CleanupPolicy is a project's workspace auto-cleanup configuration.
type CleanupPolicy struct {
	// Enabled is the master switch. When false the sweeper skips the project.
	Enabled bool `json:"enabled"`
	// InactiveDays is the N in "no activity for the last N days". Must be > 0 for
	// the policy to do anything.
	InactiveDays int `json:"inactive_days"`
	// Statuses is the subset of {completed, not_started} the policy targets.
	Statuses []string `json:"statuses"`
}

// Active reports whether the policy will actually clean anything. A policy with a
// non-positive InactiveDays or no target statuses is inert even if Enabled.
func (p CleanupPolicy) Active() bool {
	return p.Enabled && p.InactiveDays > 0 && len(p.Statuses) > 0
}

// Targets reports whether the policy targets the given cleanup category.
func (p CleanupPolicy) Targets(category string) bool {
	for _, s := range p.Statuses {
		if s == category {
			return true
		}
	}
	return false
}

// WorkspaceCleanupService owns the per-project auto-cleanup policy and the
// background sweeper. Destructive actions (workspace teardown, issue deletion)
// are injected so the service is unit-testable without git/filesystem side
// effects; NewWorkspaceCleanupService wires them to the real services.
type WorkspaceCleanupService struct {
	q  *store.Queries
	db *sql.DB

	// deleteWorkspace tears down a workspace (git worktree + on-disk dir + row).
	deleteWorkspace func(ctx context.Context, workspaceID int64) error
	// deleteIssue removes the linked issue after its workspace is gone.
	deleteIssue func(ctx context.Context, issueID int64) error
	// hasChanges reports whether a workspace has uncommitted git changes; such
	// workspaces are skipped so auto-cleanup never destroys unsaved work.
	hasChanges func(ctx context.Context, workspaceID int64) (bool, error)

	// now is overridable in tests.
	now func() time.Time
}

// NewWorkspaceCleanupService wires the cleanup service against the real
// workspace and kanban services. Either may be nil in tests, in which case the
// corresponding destructive step is skipped.
func NewWorkspaceCleanupService(q *store.Queries, db *sql.DB, ws *WorkspaceService, kb *KanbanService) *WorkspaceCleanupService {
	s := &WorkspaceCleanupService{q: q, db: db, now: time.Now}
	if ws != nil {
		s.deleteWorkspace = ws.Delete
		s.hasChanges = func(ctx context.Context, id int64) (bool, error) {
			changes, err := ws.CheckWorkspaceChanges(ctx, id)
			if err != nil {
				return false, err
			}
			return len(changes) > 0, nil
		}
	}
	if kb != nil {
		s.deleteIssue = kb.DeleteIssue
	}
	return s
}

// GetPolicy returns a project's workspace auto-cleanup policy.
func (s *WorkspaceCleanupService) GetPolicy(ctx context.Context, projectID int64) (CleanupPolicy, error) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return CleanupPolicy{}, err
	}
	return CleanupPolicy{
		Enabled:      p.CleanupEnabled != 0,
		InactiveDays: int(p.CleanupInactiveDays),
		Statuses:     parseCleanupStatuses(p.CleanupStatuses),
	}, nil
}

// SetPolicy stores a project's workspace auto-cleanup policy. Invalid or unknown
// status tokens are dropped; a negative InactiveDays is clamped to 0.
func (s *WorkspaceCleanupService) SetPolicy(ctx context.Context, projectID int64, pol CleanupPolicy) error {
	days := pol.InactiveDays
	if days < 0 {
		days = 0
	}
	enabled := int64(0)
	if pol.Enabled {
		enabled = 1
	}
	return s.q.UpdateProjectCleanupPolicy(ctx, store.UpdateProjectCleanupPolicyParams{
		CleanupEnabled:      enabled,
		CleanupInactiveDays: int64(days),
		CleanupStatuses:     strings.Join(normalizeCleanupStatuses(pol.Statuses), ","),
		ID:                  projectID,
	})
}

// parseCleanupStatuses parses the stored comma-separated column into a validated
// slice (unknown tokens dropped, order preserved, duplicates removed).
func parseCleanupStatuses(raw string) []string {
	return normalizeCleanupStatuses(strings.Split(raw, ","))
}

// normalizeCleanupStatuses keeps only known categories, trims whitespace, and
// de-duplicates while preserving first-seen order.
func normalizeCleanupStatuses(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != CleanupCategoryCompleted && s != CleanupCategoryNotStarted {
			continue
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// classifyIssue maps an issue's status pair to a cleanup category, or "" when the
// issue is in-progress (and therefore must never be auto-cleaned). Completed wins
// over not-started so a done issue is never mistaken for idle.
func classifyIssue(lifecycleStatus, execStatus string) string {
	switch {
	case lifecycleStatus == "completed" || execStatus == "done" || execStatus == "abandoned":
		return CleanupCategoryCompleted
	case lifecycleStatus == "created" && execStatus == "idle":
		return CleanupCategoryNotStarted
	default:
		return ""
	}
}

// cleanupCandidate is a workspace that qualified for deletion under a policy.
type cleanupCandidate struct {
	WorkspaceID  int64
	IssueID      int64
	Category     string
	LastActivity time.Time
}

// evaluateCandidate applies a policy to one workspace row. The bool is false when
// the workspace must be kept (actively running, wrong status, or still recent).
func evaluateCandidate(row store.ListProjectWorkspacesForCleanupRow, pol CleanupPolicy, now time.Time) (cleanupCandidate, bool) {
	// Never touch a workspace whose agent/session is currently running.
	if isRunning(row.AgentStatus) || isRunning(row.SessionStatus) {
		return cleanupCandidate{}, false
	}
	category := classifyIssue(row.LifecycleStatus, row.ExecStatus)
	if category == "" || !pol.Targets(category) {
		return cleanupCandidate{}, false
	}
	last := lastActivity(row)
	cutoff := now.Add(-time.Duration(pol.InactiveDays) * 24 * time.Hour)
	if last.After(cutoff) {
		return cleanupCandidate{}, false // still active within the window
	}
	return cleanupCandidate{
		WorkspaceID:  row.WorkspaceID,
		IssueID:      row.IssueID,
		Category:     category,
		LastActivity: last,
	}, true
}

// lastActivity resolves the best "last active" timestamp for a workspace: the
// stats row's last_activity_at when present, else the workspace's updated_at
// (which for a never-touched workspace tracks its creation time).
func lastActivity(row store.ListProjectWorkspacesForCleanupRow) time.Time {
	if row.LastActivityAt.Valid {
		return row.LastActivityAt.Time
	}
	return row.UpdatedAt
}

func isRunning(s sql.NullString) bool {
	return s.Valid && strings.EqualFold(strings.TrimSpace(s.String), "running")
}

// CleanupResult summarizes one project sweep.
type CleanupResult struct {
	ProjectID      int64   `json:"project_id"`
	Scanned        int     `json:"scanned"`
	Deleted        []int64 `json:"deleted"`
	SkippedChanges int     `json:"skipped_changes"`
	Errors         int     `json:"errors"`
}

// SweepProject scans a single project's live workspaces and deletes the ones that
// qualify under its cleanup policy (workspace teardown followed by issue removal).
// It is a no-op when the policy is inert. Errors on individual workspaces are
// counted and logged; the sweep continues.
func (s *WorkspaceCleanupService) SweepProject(ctx context.Context, projectID int64) (CleanupResult, error) {
	res := CleanupResult{ProjectID: projectID, Deleted: []int64{}}
	pol, err := s.GetPolicy(ctx, projectID)
	if err != nil {
		return res, err
	}
	if !pol.Active() {
		return res, nil
	}
	rows, err := s.q.ListProjectWorkspacesForCleanup(ctx, projectID)
	if err != nil {
		return res, err
	}
	now := s.now()
	for _, row := range rows {
		res.Scanned++
		cand, ok := evaluateCandidate(row, pol, now)
		if !ok {
			continue
		}
		// Safety net: never destroy a workspace with uncommitted changes.
		if s.hasChanges != nil {
			changed, err := s.hasChanges(ctx, cand.WorkspaceID)
			if err != nil {
				res.Errors++
				slog.Warn("workspace cleanup: change check failed", "workspaceID", cand.WorkspaceID, "error", err)
				continue
			}
			if changed {
				res.SkippedChanges++
				continue
			}
		}
		if err := s.cleanupOne(ctx, cand); err != nil {
			res.Errors++
			slog.Warn("workspace cleanup: delete failed", "workspaceID", cand.WorkspaceID, "issueID", cand.IssueID, "error", err)
			continue
		}
		res.Deleted = append(res.Deleted, cand.WorkspaceID)
	}
	if len(res.Deleted) > 0 {
		slog.Info("workspace cleanup: swept project", "projectID", projectID, "deleted", len(res.Deleted), "scanned", res.Scanned)
	}
	return res, nil
}

// cleanupOne deletes a candidate's workspace and then its issue. The workspace is
// removed first (git/dir teardown); the issue delete is best-effort afterwards —
// its FK is ON DELETE SET NULL so a failed issue delete only orphans the issue,
// never the workspace.
func (s *WorkspaceCleanupService) cleanupOne(ctx context.Context, cand cleanupCandidate) error {
	if s.deleteWorkspace != nil {
		if err := s.deleteWorkspace(ctx, cand.WorkspaceID); err != nil {
			return err
		}
	}
	if s.deleteIssue != nil && cand.IssueID != 0 {
		if err := s.deleteIssue(ctx, cand.IssueID); err != nil {
			return err
		}
	}
	return nil
}

// StartCleanupScheduler launches a background goroutine that, once an hour, sweeps
// every active project whose cleanup policy is enabled (policy re-read fresh each
// cycle). Returns immediately; stops when ctx is done.
func (s *WorkspaceCleanupService) StartCleanupScheduler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			s.runDueCleanup(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// runDueCleanup runs one sweep cycle over all active projects. Each project is
// isolated by its own panic recovery inside SweepProject's caller loop.
func (s *WorkspaceCleanupService) runDueCleanup(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("workspace cleanup scheduler panicked", "recover", r)
		}
	}()
	projects, err := s.q.ListProjects(ctx, "active")
	if err != nil {
		slog.Warn("workspace cleanup: list projects failed", "error", err)
		return
	}
	for _, p := range projects {
		if p.CleanupEnabled == 0 || p.CleanupInactiveDays <= 0 {
			continue // disabled or inert (default OFF)
		}
		if _, err := s.SweepProject(ctx, p.ID); err != nil {
			slog.Warn("workspace cleanup: sweep failed", "projectID", p.ID, "error", err)
		}
	}
}
