package service

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ExecEvent is one entry in an issue's execution timeline (spec
// 2026-06-05-ai-native-board-execution-design.md §23.7): advance moves, gate
// results, ask_user round-trips, terminal transitions, and cost accumulation.
type ExecEvent struct {
	IssueID     int64
	WorkspaceID int64  // 0 when not workspace-scoped
	Kind        string // advance | gate | ask_user | terminal | cost | intervention
	Summary     string
	DetailJSON  string  // optional machine-readable payload
	CostUSD     float64 // 0 except for kind=cost
}

// ExecEventService appends to and reads the per-issue execution timeline.
// Recording is best-effort (a logging failure must never break the execution
// path), so Record swallows + logs errors rather than returning them.
type ExecEventService struct {
	db *store.DB
	q  *store.Queries
}

// NewExecEventService wraps the DB once (store.Wrap(nil) is nil-safe for tests).
func NewExecEventService(db *sql.DB) *ExecEventService {
	wrapped := store.Wrap(db)
	return &ExecEventService{db: wrapped, q: store.New(wrapped)}
}

// Record appends an event. Best-effort: errors are logged, not returned. A nil
// receiver / missing issue id / empty kind is a no-op so callers can guard loosely.
func (s *ExecEventService) Record(ctx context.Context, e ExecEvent) {
	if s == nil || s.q == nil || e.IssueID == 0 || e.Kind == "" {
		return
	}
	var ws sql.NullInt64
	if e.WorkspaceID != 0 {
		ws = sql.NullInt64{Int64: e.WorkspaceID, Valid: true}
	}
	var detail sql.NullString
	if e.DetailJSON != "" {
		detail = sql.NullString{String: e.DetailJSON, Valid: true}
	}
	if err := s.q.InsertIssueExecEvent(ctx, store.InsertIssueExecEventParams{
		IssueID:     e.IssueID,
		WorkspaceID: ws,
		Kind:        e.Kind,
		Summary:     e.Summary,
		DetailJson:  detail,
		CostUsd:     e.CostUSD,
	}); err != nil {
		slog.Warn("exec-event: record failed", "issueID", e.IssueID, "kind", e.Kind, "error", err)
	}
}

// List returns the issue's full execution timeline, oldest first.
func (s *ExecEventService) List(ctx context.Context, issueID int64) ([]store.IssueExecEvent, error) {
	if s == nil || s.q == nil {
		return nil, nil
	}
	return s.q.ListIssueExecEvents(ctx, issueID)
}

// TotalCost returns the issue's cumulative recorded cost.
func (s *ExecEventService) TotalCost(ctx context.Context, issueID int64) (float64, error) {
	if s == nil || s.q == nil {
		return 0, nil
	}
	return s.q.SumIssueExecCost(ctx, issueID)
}
