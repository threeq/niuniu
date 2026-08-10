package store

import (
	"context"
	"fmt"
)

type UpdateAgentMessageContentParams struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func (q *Queries) UpdateAgentMessageContent(ctx context.Context, arg UpdateAgentMessageContentParams) error {
	_, err := q.db.ExecContext(ctx, `
UPDATE agent_messages
SET content = ?
WHERE id = ?`, arg.Content, arg.ID)
	if err != nil {
		return fmt.Errorf("update agent message content: %w", err)
	}
	return nil
}
