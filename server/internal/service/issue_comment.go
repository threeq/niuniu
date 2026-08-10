package service

import (
	"context"
	"errors"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type IssueCommentService struct {
	q *store.Queries
}

// NewIssueCommentService constructs the comment service. The OutboxPublisher
// + tx-wrapping path that used to atomicize comment insert with writeback
// enqueue was removed when the M2/M3 writeback subsystem was deleted —
// external sync now happens via the AI proxy (/mcp/external-proxy/*), not
// a server-side outbox.
func NewIssueCommentService(q *store.Queries) *IssueCommentService {
	return &IssueCommentService{q: q}
}

func (s *IssueCommentService) ListByIssue(ctx context.Context, issueID int64) ([]store.IssueComment, error) {
	return s.q.ListIssueComments(ctx, issueID)
}

func (s *IssueCommentService) Get(ctx context.Context, id int64) (store.IssueComment, error) {
	return s.q.GetIssueComment(ctx, id)
}

// Create inserts a new comment. callerUserID is kept on the signature for
// future actor tracking but is currently unused.
func (s *IssueCommentService) Create(ctx context.Context, issueID int64, author string, callerUserID int64, content string) (store.IssueComment, error) {
	_ = callerUserID
	if content == "" {
		return store.IssueComment{}, errors.New("comment content cannot be empty")
	}
	return s.q.CreateIssueComment(ctx, store.CreateIssueCommentParams{
		IssueID: issueID,
		Author:  author,
		Content: content,
	})
}

func (s *IssueCommentService) Update(ctx context.Context, id int64, content string) (store.IssueComment, error) {
	if content == "" {
		return store.IssueComment{}, errors.New("comment content cannot be empty")
	}
	return s.q.UpdateIssueComment(ctx, store.UpdateIssueCommentParams{
		ID:      id,
		Content: content,
	})
}

func (s *IssueCommentService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteIssueComment(ctx, id)
}
