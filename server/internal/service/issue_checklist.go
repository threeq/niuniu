package service

import (
	"context"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type IssueChecklistService struct {
	q *store.Queries
}

func NewIssueChecklistService(q *store.Queries) *IssueChecklistService {
	return &IssueChecklistService{q: q}
}

func (s *IssueChecklistService) ListByIssue(ctx context.Context, issueID int64) ([]store.IssueChecklist, error) {
	return s.q.ListChecklistsByIssue(ctx, issueID)
}

func (s *IssueChecklistService) Get(ctx context.Context, id int64) (store.IssueChecklist, error) {
	return s.q.GetChecklist(ctx, id)
}

func (s *IssueChecklistService) Create(ctx context.Context, issueID int64, title string) (store.IssueChecklist, error) {
	return s.q.CreateChecklist(ctx, store.CreateChecklistParams{
		IssueID:   issueID,
		Title:     title,
		IssueID_2: issueID,
	})
}

func (s *IssueChecklistService) Update(ctx context.Context, id int64, title string, isCompleted int64) (store.IssueChecklist, error) {
	return s.q.UpdateChecklist(ctx, store.UpdateChecklistParams{
		ID:          id,
		Title:       title,
		IsCompleted: isCompleted,
	})
}

func (s *IssueChecklistService) UpdatePosition(ctx context.Context, id int64, position int64) error {
	return s.q.UpdateChecklistPosition(ctx, store.UpdateChecklistPositionParams{
		ID:       id,
		Position: position,
	})
}

func (s *IssueChecklistService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteChecklist(ctx, id)
}

func (s *IssueChecklistService) GetStats(ctx context.Context, issueID int64) (store.GetChecklistStatsRow, error) {
	return s.q.GetChecklistStats(ctx, issueID)
}
