package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func newTestApproval(t *testing.T) *Approval {
	t.Helper()
	store.Driver = "sqlite"

	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)

	// workspace 10 is the owner anchor; users 7 and 9 satisfy the decided_by FK.
	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash) VALUES (7, 'operator', 'x')`,
		`INSERT INTO users (id, username, password_hash) VALUES (9, 'director', 'x')`,
		`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (10, 'ws', '/tmp/ws', 'user', 2)`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}
	return NewApproval(store.Wrap(raw), ApprovalPolicy{})
}

func TestApprovalLifecycle(t *testing.T) {
	a := newTestApproval(t)
	ctx := context.Background()
	req, err := a.Create(ctx, ApprovalInput{
		BotID: 1, WorkspaceID: 10, IssueID: 20, Type: ApprovalPublish,
		ContentVersion: 3, PreviewJSON: `{"title":"t","summary":"s"}`,
	})
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusPending, req.Status)
	require.Contains(t, req.MetadataJSON, `"bot_id":1`)
	require.Contains(t, req.MetadataJSON, `"issue_id":20`)
	require.Contains(t, req.MetadataJSON, `"type":"publish"`)
	require.Contains(t, req.MetadataJSON, `"content_version":3`)
	require.Contains(t, req.MetadataJSON, `"preview":{"title":"t","summary":"s"}`)

	pending, err := a.ListPending(ctx, "user", 2)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, req.ID, pending[0].ID)

	got, err := a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: ActionApproved, Reason: "ok"})
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusAnswered, got.Status)
	require.Equal(t, "user", got.DecisionSource)

	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: ActionRejected, Reason: "late"})
	require.ErrorIs(t, err, ErrAlreadyDecided)

	pending, err = a.ListPending(ctx, "user", 2)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestProxyApprovalIsRecorded(t *testing.T) {
	a := newTestApproval(t)
	req, err := a.Create(context.Background(), ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalReply, PreviewJSON: `{}`,
	})
	require.NoError(t, err)
	got, err := a.Decide(context.Background(), req.ID, Decision{
		OperatorID: 9, Action: ActionApproved, ProxyFor: 7,
	})
	require.NoError(t, err)
	require.Equal(t, "proxy", got.DecisionSource)
	require.Equal(t, int64(9), got.DecidedBy)
	require.Contains(t, got.MetadataJSON, `"proxy_for":7`)
	require.Contains(t, got.MetadataJSON, `"approved_by":9`)

	var meta map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(got.MetadataJSON), &meta))
	require.JSONEq(t, `{"bot_id":1,"issue_id":0,"type":"reply","content_version":0,"preview":{}}`,
		string(mustMarshalWithoutAudit(t, meta)))
}

func TestTimeoutNeverAutoApproves(t *testing.T) {
	a := newTestApproval(t)
	ctx := context.Background()
	callbackRan := false
	a.OnDecided = func(context.Context, ApprovalRequest, Decision) error {
		callbackRan = true
		return nil
	}
	req, err := a.Create(ctx, ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalVideo, TTL: time.Millisecond,
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, a.SweepExpired(ctx))
	got, err := a.Get(ctx, req.ID)
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status)
	require.False(t, callbackRan, "timeout must never publish")
	require.Empty(t, got.AnswersJSON, "timeout must not fabricate an approval")

	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: ActionApproved})
	require.ErrorIs(t, err, ErrAlreadyDecided)
}

func TestApprovalKillSwitchCancelsByBot(t *testing.T) {
	a := newTestApproval(t)
	ctx := context.Background()
	var botOneIDs []int64
	for _, botID := range []int64{1, 1, 2} {
		req, err := a.Create(ctx, ApprovalInput{
			BotID: botID, WorkspaceID: 10, Type: ApprovalPublish, PreviewJSON: `{}`,
		})
		require.NoError(t, err)
		if botID == 1 {
			botOneIDs = append(botOneIDs, req.ID)
		}
	}
	n, err := a.CancelByBot(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	pending, err := a.ListPending(ctx, "user", 2)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	req, err := a.Get(ctx, pending[0].ID)
	require.NoError(t, err)
	require.Contains(t, req.MetadataJSON, `"bot_id":2`)

	cancelled, err := a.Get(ctx, botOneIDs[0])
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusCancelled, cancelled.Status)
	require.Equal(t, "kill_switch", cancelled.DecisionSource)
}

func TestApprovalReviewHardening(t *testing.T) {
	a := newTestApproval(t)
	ctx := context.Background()

	// A card-specific action set rejects actions that belong to another card.
	req, err := a.Create(ctx, ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalPublish, PreviewJSON: `{}`,
	})
	require.NoError(t, err)
	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: ActionRegenerate})
	require.ErrorIs(t, err, ErrInvalidDecision)
	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: "bogus"})
	require.ErrorIs(t, err, ErrInvalidDecision)
	// The rejected attempts did not consume the pending state.
	got, err := a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: ActionApproved})
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusAnswered, got.Status)

	// A fresh card survives a sweep.
	fresh, err := a.Create(ctx, ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalReply, PreviewJSON: `{}`,
	})
	require.NoError(t, err)
	require.NoError(t, a.SweepExpired(ctx))
	got, err = a.Get(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusPending, got.Status)
}

func TestExpiredApprovalIsNotDecidable(t *testing.T) {
	a := newTestApproval(t)
	ctx := context.Background()
	req, err := a.Create(ctx, ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalPublish, TTL: time.Millisecond,
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	// Even before the sweeper runs, an expired card must not be approvable.
	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: ActionApproved})
	require.ErrorIs(t, err, ErrAlreadyDecided)

	got, err := a.Get(ctx, req.ID)
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusTimeout, got.Status)
	require.Empty(t, got.AnswersJSON)
}

func TestApprovalRejectsDisabledTypeAndBadJSON(t *testing.T) {
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (10, 'ws', '/tmp/ws', 'user', 2)`)
	require.NoError(t, err)

	a := NewApproval(store.Wrap(raw), ApprovalPolicy{Enabled: map[ApprovalType]bool{ApprovalPublish: true}})
	_, err = a.Create(context.Background(), ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalReply, PreviewJSON: `{}`,
	})
	require.ErrorIs(t, err, ErrApprovalDisabled)

	_, err = a.Create(context.Background(), ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: "bogus",
	})
	require.ErrorIs(t, err, ErrInvalidApprovalType)

	_, err = a.Create(context.Background(), ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalPublish, PreviewJSON: `{oops`,
	})
	require.Error(t, err)
}

func mustMarshalWithoutAudit(t *testing.T, meta map[string]json.RawMessage) []byte {
	t.Helper()
	delete(meta, "action")
	delete(meta, "approved_by")
	delete(meta, "proxy_for")
	b, err := json.Marshal(meta)
	require.NoError(t, err)
	return b
}
