package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// memory_staleness.go holds the per-project maintenance SCHEDULE, the run log,
// and the human review queue. The actual memory evolution/correction is performed
// by an agent task (see memory_orchestrator.go) which goes through niuniu-mcp, so
// every change is auditable and reversible — there is no longer any direct-DB
// staleness engine here.
//
// The review queue (pending_review) is retained as a safety net: a human can
// restore (keep) or confirm any memory that was queued for review.

// DefaultSweepCron is the schedule applied when a user ENABLES automatic
// maintenance for a project (every month on the 3rd at 03:33). The feature is OFF
// by default: a project's projects.memory_sweep_cron is empty until the user opts
// in, and an empty value means "disabled". The schedule is edited in that
// project's settings — there is no global schedule.
const DefaultSweepCron = "33 3 3 * *"

// GetProjectSweepCron returns a project's maintenance schedule verbatim. An empty
// string means automatic maintenance is disabled (the default for new projects).
func (s *MemoryService) GetProjectSweepCron(ctx context.Context, projectID int64) (string, error) {
	proj, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(proj.MemorySweepCron), nil
}

// SetProjectSweepCron stores a project's maintenance schedule. An empty
// expression disables automatic maintenance. The caller is responsible for
// validating a non-empty expression.
func (s *MemoryService) SetProjectSweepCron(ctx context.Context, projectID int64, cronExpr string) error {
	return s.q.UpdateProjectMemorySweepCron(ctx, store.UpdateProjectMemorySweepCronParams{
		MemorySweepCron: strings.TrimSpace(cronExpr),
		ID:              projectID,
	})
}

// ListSweepRuns returns the recent automatic-maintenance execution log for a
// project (most recent first).
func (s *MemoryService) ListSweepRuns(ctx context.Context, projectID int64, limit int64) ([]store.MemorySweepRun, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.q.ListMemorySweepRunsForProject(ctx, store.ListMemorySweepRunsForProjectParams{
		ProjectID: projectID,
		Limit:     limit,
	})
}

// ClearSweepRuns deletes a project's automatic-maintenance run log.
func (s *MemoryService) ClearSweepRuns(ctx context.Context, projectID int64) error {
	return s.q.DeleteMemorySweepRunsForProject(ctx, projectID)
}

// ListPendingReview returns the review queue for a project: memories soft-deleted
// but flagged pending_review, awaiting a human keep (Restore) / delete decision.
func (s *MemoryService) ListPendingReview(ctx context.Context, projectID int64) ([]store.Memory, error) {
	return s.q.ListPendingReviewForProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
}

// KeepReviewedMemory resolves a queued entry as "keep": it clears the review
// flag/reason and restores the memory (un-soft-deletes it).
func (s *MemoryService) KeepReviewedMemory(ctx context.Context, id int64) error {
	if err := s.q.ResolveStaleReview(ctx, id); err != nil {
		return err
	}
	return s.q.RestoreMemory(ctx, id)
}

// ConfirmReviewedDeletion resolves a queued entry as "delete": it clears the
// review flag so the entry leaves the queue while staying soft-deleted (still
// reversible via Restore).
func (s *MemoryService) ConfirmReviewedDeletion(ctx context.Context, id int64) error {
	return s.q.ResolveStaleReview(ctx, id)
}

// cronDue reports whether a project is due for a scheduled run: true when no prior
// scheduled run exists, or the cron schedule has fired at least once since the
// most recent scheduled run. runs must be ordered newest-first (as
// ListMemorySweepRunsForProject returns them).
func cronDue(sched cron.Schedule, runs []store.MemorySweepRun, now time.Time) bool {
	for _, r := range runs {
		if r.Trigger == "schedule" {
			return !sched.Next(r.CreatedAt).After(now)
		}
	}
	return true // never run on schedule -> run now
}
