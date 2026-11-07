package opsbot

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type dispatchFixture struct {
	dispatcher *EngagementDispatcher
	q          *store.Queries

	mu   sync.Mutex
	sent []dispatchCall
	send func(call dispatchCall) error
}

type dispatchCall struct {
	botID          int64
	engagementID   int64
	approvalCardID int64
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)
	for _, stmt := range []string{
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, pack_id, account_ref, display_name, status)
		 VALUES (1, 'user', 2, 'account', 'xiaohongshu', 'brand', '活跃号', 'active')`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, pack_id, account_ref, display_name, status)
		 VALUES (2, 'user', 2, 'account', 'xiaohongshu', 'paused', '暂停号', 'paused')`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	f := &dispatchFixture{q: store.NewQueries(raw)}
	f.dispatcher = &EngagementDispatcher{
		Q: f.q,
		Send: func(ctx context.Context, botID, id, approvalID int64, d Decision) error {
			call := dispatchCall{botID: botID, engagementID: id, approvalCardID: approvalID}
			f.mu.Lock()
			f.sent = append(f.sent, call)
			f.mu.Unlock()
			if f.send != nil {
				return f.send(call)
			}
			return nil
		},
	}
	return f
}

func (f *dispatchFixture) seed(t *testing.T, botID int64, status string, seq int) store.OpsEngagement {
	t.Helper()
	ctx := context.Background()
	row, err := f.q.UpsertOpsEngagement(ctx, store.UpsertOpsEngagementParams{
		BotID:                botID,
		PlatformEngagementID: fmt.Sprintf("d-%d-%d", botID, seq),
		Kind:                 "comment",
		Author:               "u1",
		Text:                 "hi",
	})
	require.NoError(t, err)
	if status == EngagementNew {
		return row
	}
	row, err = f.q.UpdateOpsEngagementStatus(ctx, store.UpdateOpsEngagementStatusParams{
		Status:            status,
		Category:          row.Category,
		RiskLevel:         row.RiskLevel,
		SuggestedReply:    row.SuggestedReply,
		ApprovalRequestID: row.ApprovalRequestID,
		ReplyText:         row.ReplyText,
		Receipt:           row.Receipt,
		DecidedBy:         row.DecidedBy,
		DecidedAt:         row.DecidedAt,
		ID:                row.ID,
		Status_2:          row.Status,
	})
	require.NoError(t, err)
	return row
}

func TestDispatcherSendsApprovedAndSkipsInactive(t *testing.T) {
	f := newDispatchFixture(t)
	f.seed(t, 1, EngagementApproved, 1)
	f.seed(t, 1, EngagementApproved, 2)
	f.seed(t, 1, EngagementSent, 3)
	f.seed(t, 2, EngagementApproved, 4)
	f.send = func(call dispatchCall) error {
		if call.botID == 2 {
			return fmt.Errorf("%w: paused", ErrBotNotActive)
		}
		return nil
	}

	result, err := f.dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, result.Scanned)
	require.Equal(t, 2, result.Sent)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, result.Failed)
	require.Len(t, f.sent, 2, "an inactive account is never handed to Send")
}

func TestDispatcherRateLimitStopsBatch(t *testing.T) {
	f := newDispatchFixture(t)
	f.seed(t, 1, EngagementApproved, 1)
	f.seed(t, 1, EngagementApproved, 2)
	f.send = func(dispatchCall) error { return ErrRateLimited }

	result, err := f.dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.RateLimited)
	require.Zero(t, result.Sent)
	require.Len(t, f.sent, 1, "the window being full stops the batch")
}

func TestDispatcherRecordsFailedSend(t *testing.T) {
	f := newDispatchFixture(t)
	f.seed(t, 1, EngagementApproved, 1)
	f.send = func(dispatchCall) error { return fmt.Errorf("%w: changed", ErrEngagementStateChanged) }

	var observed int
	f.dispatcher.OnError = func(_ context.Context, _ int64, _ error) { observed++ }

	result, err := f.dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, 1, observed)
}

func TestDispatcherStartStop(t *testing.T) {
	f := newDispatchFixture(t)
	f.dispatcher.Interval = 20 * time.Millisecond

	require.False(t, f.dispatcher.Running())
	require.NoError(t, f.dispatcher.Start(context.Background()))
	require.True(t, f.dispatcher.Running())
	require.NoError(t, f.dispatcher.Start(context.Background()), "start is idempotent")

	f.dispatcher.Stop()
	require.False(t, f.dispatcher.Running())
	f.dispatcher.Stop() // safe when already stopped
}

func TestDispatcherRequiresDependencies(t *testing.T) {
	var d EngagementDispatcher
	_, err := d.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrEngagementNotConfigured)
	require.ErrorIs(t, d.Start(context.Background()), ErrEngagementNotConfigured)
}

// TestDispatcherRespectsKillSwitch proves the kill switch stops interaction
// sends end to end: the kill drains approved replies, and a row approved after
// the kill is refused because the account is disabled.
func TestDispatcherRespectsKillSwitch(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	adapter := &fakeEngagementAdapter{}
	engagement := &Engagement{Q: f.db.Queries(), DB: f.db, Adapter: adapter, Approvals: f.approval}
	dispatcher := &EngagementDispatcher{Q: f.db.Queries(), Send: func(ctx context.Context, botID, id, approvalID int64, d Decision) error {
		return engagement.Send(ctx, BotRef{ID: botID}, id, approvalID, d)
	}}

	f.seedEngagement(t, EngagementApproved)
	f.seedEngagement(t, EngagementApproved)

	_, err := f.risk.Kill(ctx, 1, "manual")
	require.NoError(t, err)

	result, err := dispatcher.RunOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, result.Scanned, "the kill switch drained queued replies")
	require.Zero(t, result.Sent)
	require.Zero(t, adapter.calls)

	// A late approval on the disabled account is still refused.
	f.seedEngagement(t, EngagementApproved)
	result, err = dispatcher.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, adapter.calls)
}

// TestDispatcherSkipsPausedAccount proves a risk pause holds approved replies
// without marking them failed; they stay queued for after the resume.
func TestDispatcherSkipsPausedAccount(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	adapter := &fakeEngagementAdapter{}
	engagement := &Engagement{Q: f.db.Queries(), DB: f.db, Adapter: adapter, Approvals: f.approval}
	dispatcher := &EngagementDispatcher{Q: f.db.Queries(), Send: func(ctx context.Context, botID, id, approvalID int64, d Decision) error {
		return engagement.Send(ctx, BotRef{ID: botID}, id, approvalID, d)
	}}

	row := f.seedEngagement(t, EngagementApproved)
	_, err := f.risk.Evaluate(ctx, 1, RiskSignalRateLimited)
	require.NoError(t, err)

	result, err := dispatcher.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, adapter.calls)

	after, err := f.db.Queries().GetOpsEngagement(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementApproved, after.Status, "a paused account keeps the reply queued")
}
