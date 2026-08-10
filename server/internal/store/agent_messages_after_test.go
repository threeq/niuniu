package store_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ListAgentMessagesAfter is the cheap incremental cursor used by the mobile
// useWorkspaceSSE poll loop: a 2 s tick now passes ?after=<lastSeenId>
// instead of re-shipping the whole history. The query mirrors
// ListAgentMessagesBefore (same composite (created_at, id) tiebreaker)
// but in the opposite direction and with ASC ordering at the source so
// no outer SELECT wrapper is needed.
//
// This test pins the contract:
//   1. Strictly newer than the cursor — equal timestamp tiebreaks on id, the
//      cursor row itself is not returned.
//   2. Empty result when the cursor is already the latest message.
//   3. ASC ordering by (created_at, id) — the mobile dedup expects this.
//   4. Limit caps the result (the handler relies on `limit + 1` to detect
//      `hasMore`; the query itself just honours the bound).
func TestListAgentMessagesAfter(t *testing.T) {
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	ctx := context.Background()

	// Workspace fixture — agent_messages.workspace_id has FK to workspaces(id).
	res, err := db.Exec(
		`INSERT INTO workspaces (name, path, status, agent_status) VALUES (?, ?, ?, ?)`,
		"ws", t.TempDir(), "active", "idle",
	)
	require.NoError(t, err)
	wsID, err := res.LastInsertId()
	require.NoError(t, err)

	// Five messages with explicit timestamps so we can exercise the
	// equal-timestamp tiebreaker (msg-3 and msg-4 share a second).
	type row struct {
		id, ts string
	}
	rows := []row{
		{"msg-1", "2026-05-01 10:00:00"},
		{"msg-2", "2026-05-01 10:00:01"},
		{"msg-3", "2026-05-01 10:00:02"},
		{"msg-4", "2026-05-01 10:00:02"}, // same second as msg-3
		{"msg-5", "2026-05-01 10:00:03"},
	}
	for _, r := range rows {
		_, err := db.Exec(
			`INSERT INTO agent_messages (id, workspace_id, role, content, message_id, event_type, is_error, created_at)
			 VALUES (?, ?, 'assistant', '', ?, 'text', 0, ?)`,
			r.id, wsID, r.id, r.ts,
		)
		require.NoError(t, err)
	}

	// (1) After msg-3 returns msg-4 (equal ts, id > 'msg-3') and msg-5.
	got, err := q.ListAgentMessagesAfter(ctx, store.ListAgentMessagesAfterParams{
		WorkspaceID: wsID,
		ID:          "msg-3",
		ID_2:        "msg-3",
		ID_3:        "msg-3",
		Limit:       100,
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "msg-4", got[0].ID)
	assert.Equal(t, "msg-5", got[1].ID)

	// (2) After msg-5 returns nothing.
	got, err = q.ListAgentMessagesAfter(ctx, store.ListAgentMessagesAfterParams{
		WorkspaceID: wsID,
		ID:          "msg-5",
		ID_2:        "msg-5",
		ID_3:        "msg-5",
		Limit:       100,
	})
	require.NoError(t, err)
	assert.Empty(t, got)

	// (3) After msg-1 returns the rest in ASC order.
	got, err = q.ListAgentMessagesAfter(ctx, store.ListAgentMessagesAfterParams{
		WorkspaceID: wsID,
		ID:          "msg-1",
		ID_2:        "msg-1",
		ID_3:        "msg-1",
		Limit:       100,
	})
	require.NoError(t, err)
	require.Len(t, got, 4)
	for i, want := range []string{"msg-2", "msg-3", "msg-4", "msg-5"} {
		assert.Equal(t, want, got[i].ID)
	}

	// (4) Limit caps the result.
	got, err = q.ListAgentMessagesAfter(ctx, store.ListAgentMessagesAfterParams{
		WorkspaceID: wsID,
		ID:          "msg-1",
		ID_2:        "msg-1",
		ID_3:        "msg-1",
		Limit:       2,
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "msg-2", got[0].ID)
	assert.Equal(t, "msg-3", got[1].ID)
}
