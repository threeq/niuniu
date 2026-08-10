package service

import (
	"context"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type IssueActivityService struct {
	q *store.Queries
}

func NewIssueActivityService(q *store.Queries) *IssueActivityService {
	return &IssueActivityService{q: q}
}

func (s *IssueActivityService) ListByIssue(ctx context.Context, issueID int64) ([]store.IssueActivity, error) {
	return s.q.ListActivitiesByIssue(ctx, issueID)
}

func (s *IssueActivityService) LogCreated(ctx context.Context, issueID int64, author string) {
	if err := s.q.CreateActivity(ctx, store.CreateActivityParams{
		IssueID:  issueID,
		Action:   "created",
		Field:    toNullString(""),
		OldValue: toNullString(""),
		NewValue: toNullString(""),
		Author:   toNullString(author),
	}); err != nil {
		slog.Warn("failed to log issue activity", "issueID", issueID, "action", "created", "error", err)
	}
}

func (s *IssueActivityService) LogFieldChange(ctx context.Context, issueID int64, field, oldVal, newVal, author string) {
	if oldVal == newVal {
		return
	}
	if err := s.q.CreateActivity(ctx, store.CreateActivityParams{
		IssueID:  issueID,
		Action:   "updated",
		Field:    toNullString(field),
		OldValue: toNullString(oldVal),
		NewValue: toNullString(newVal),
		Author:   toNullString(author),
	}); err != nil {
		slog.Warn("failed to log issue activity", "issueID", issueID, "field", field, "error", err)
	}
}

func (s *IssueActivityService) LogMoved(ctx context.Context, issueID int64, oldColumn, newColumn, author string) {
	if err := s.q.CreateActivity(ctx, store.CreateActivityParams{
		IssueID:  issueID,
		Action:   "moved",
		Field:    toNullString("column"),
		OldValue: toNullString(oldColumn),
		NewValue: toNullString(newColumn),
		Author:   toNullString(author),
	}); err != nil {
		slog.Warn("failed to log issue activity", "issueID", issueID, "action", "moved", "error", err)
	}
}

func (s *IssueActivityService) LogLifecycleChange(ctx context.Context, issueID int64, oldStatus, newStatus, author string) {
	if err := s.q.CreateActivity(ctx, store.CreateActivityParams{
		IssueID:  issueID,
		Action:   "lifecycle_changed",
		Field:    toNullString("lifecycle_status"),
		OldValue: toNullString(oldStatus),
		NewValue: toNullString(newStatus),
		Author:   toNullString(author),
	}); err != nil {
		slog.Warn("failed to log issue activity", "issueID", issueID, "action", "lifecycle_changed", "error", err)
	}
}
