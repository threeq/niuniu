package opsbot

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type fakeEngagementAdapter struct {
	items    []channel.Engagement
	failErr  error
	calls    int
	lastID   string
	lastText string
}

func (a *fakeEngagementAdapter) Capabilities() channel.CapabilitySet {
	return channel.CapabilitySet{channel.CapEngagements: true, channel.CapReply: true}
}
func (a *fakeEngagementAdapter) EnsureSession(context.Context, string) error { return nil }
func (a *fakeEngagementAdapter) Publish(context.Context, any) (channel.Receipt, error) {
	return channel.Receipt{}, channel.ErrCapabilityUnavailable
}
func (a *fakeEngagementAdapter) FetchMetrics(context.Context, string, string) (map[string]float64, error) {
	return nil, channel.ErrCapabilityUnavailable
}
func (a *fakeEngagementAdapter) ListEngagements(context.Context, string) ([]channel.Engagement, error) {
	return a.items, nil
}
func (a *fakeEngagementAdapter) Reply(_ context.Context, id, text string) (channel.Receipt, error) {
	a.calls++
	a.lastID, a.lastText = id, text
	if a.failErr != nil {
		return channel.Receipt{}, a.failErr
	}
	return channel.Receipt{ExternalID: "receipt-1"}, nil
}
func (a *fakeEngagementAdapter) HealthCheck(context.Context) error { return nil }

func newEngagementFixture(t *testing.T, policyJSON string) (*Engagement, *fakeEngagementAdapter) {
	t.Helper()
	store.Driver = "sqlite"

	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	// A shared in-memory DB lives and dies with its connection.
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash) VALUES (7, 'operator', 'x')`,
		`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (10, 'ws', '/tmp/ws', 'user', 2)`,
		fmt.Sprintf(`INSERT INTO ops_bots (
			id, owner_type, owner_id, role, pack_id, account_ref, display_name,
			approval_policy_json, control_workspace_id, status
		) VALUES (1, 'user', 2, 'account', 'xiaohongshu', 'brand', '品牌', '%s', 10, 'active')`, policyJSON),
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	db := store.Wrap(raw)
	adapter := &fakeEngagementAdapter{items: []channel.Engagement{{
		ExtID: "p-1", Kind: "comment", Author: "u1", Text: "请问这个多少钱？",
	}}}
	e := &Engagement{
		Q:         db.Queries(),
		DB:        db,
		Adapter:   adapter,
		Approvals: NewApproval(db, ApprovalPolicy{}),
	}
	return e, adapter
}

func newTestEngagement(t *testing.T) *Engagement {
	t.Helper()
	e, _ := newEngagementFixture(t, `{"reply":true,"reply_hourly_limit":20}`)
	return e
}

var seedSeq atomic.Int64

func seedEngagement(t *testing.T, e *Engagement, text string) store.OpsEngagement {
	t.Helper()
	row, err := e.Q.UpsertOpsEngagement(context.Background(), store.UpsertOpsEngagementParams{
		BotID:                1,
		PlatformEngagementID: fmt.Sprintf("seed-%d", seedSeq.Add(1)),
		Kind:                 "comment",
		Author:               "u-seed",
		Text:                 text,
	})
	require.NoError(t, err)
	return row
}

// approveAndSend drives one item from new to a send attempt.
func approveAndSend(t *testing.T, e *Engagement, id int64) error {
	t.Helper()
	ctx := context.Background()
	ref := BotRef{ID: 1}
	require.NoError(t, e.Classify(ctx, ref, id))
	require.NoError(t, e.Suggest(ctx, ref, id))
	req, err := e.RequestApproval(ctx, ref, id)
	require.NoError(t, err)
	return e.Send(ctx, id, req.ID, Decision{Action: ActionApproved})
}

func TestEngagementFullLifecycle(t *testing.T) {
	e := newTestEngagement(t)
	ctx := context.Background()

	items, err := e.Pull(ctx, BotRef{ID: 1}, "")
	require.NoError(t, err)
	require.Len(t, items, 1)

	require.NoError(t, e.Classify(ctx, BotRef{ID: 1}, items[0].ID))
	require.NoError(t, e.Suggest(ctx, BotRef{ID: 1}, items[0].ID))
	req, err := e.RequestApproval(ctx, BotRef{ID: 1}, items[0].ID)
	require.NoError(t, err)
	require.NotZero(t, req.ID)

	require.NoError(t, e.Send(ctx, items[0].ID, req.ID, Decision{Action: ActionApproved}))
	got, err := e.Get(ctx, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, EngagementSent, got.Status)
	require.NotEmpty(t, got.Receipt)
	require.Equal(t, CategoryLead, got.Category)
	require.NotEmpty(t, got.ReplyText)
}

func TestHighRiskEscalates(t *testing.T) {
	e := newTestEngagement(t)
	ctx := context.Background()
	item := seedEngagement(t, e, "投诉：要求退款")

	require.NoError(t, e.Classify(ctx, BotRef{ID: 1}, item.ID))
	got, err := e.Get(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementEscalated, got.Status)
	require.Equal(t, RiskHigh, got.RiskLevel)

	// High-risk items are never auto-sent, even with an approval decision.
	err = e.Send(ctx, item.ID, 0, Decision{Action: ActionApproved})
	require.ErrorIs(t, err, ErrHighRiskEscalated)
}

func TestEngagementDedupeSamePlatformID(t *testing.T) {
	e := newTestEngagement(t)
	ctx := context.Background()

	first, err := e.Pull(ctx, BotRef{ID: 1}, "")
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := e.Pull(ctx, BotRef{ID: 1}, "")
	require.NoError(t, err)
	require.Len(t, second, 1, "a replayed platform id must not duplicate")
	require.Equal(t, first[0].ID, second[0].ID)

	stored, err := e.Q.ListOpsEngagementsByStatus(ctx, store.ListOpsEngagementsByStatusParams{
		BotID: 1, Status: EngagementNew, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, stored, 1)
}

func TestEngagementRateLimitQueues(t *testing.T) {
	e, adapter := newEngagementFixture(t, `{"reply":true,"reply_hourly_limit":1}`)
	ctx := context.Background()

	first := seedEngagement(t, e, "你好")
	require.NoError(t, approveAndSend(t, e, first.ID))

	second := seedEngagement(t, e, "在吗")
	err := approveAndSend(t, e, second.ID)
	require.ErrorIs(t, err, ErrRateLimited)

	got, err := e.Get(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementApproved, got.Status, "over-limit item waits for the next window")
	require.Equal(t, 1, adapter.calls, "no second platform send")
}

func TestEngagementRejectedClosesWithoutSending(t *testing.T) {
	e, adapter := newEngagementFixture(t, `{"reply":true,"reply_hourly_limit":20}`)
	ctx := context.Background()
	ref := BotRef{ID: 1}
	item := seedEngagement(t, e, "你好")

	require.NoError(t, e.Classify(ctx, ref, item.ID))
	require.NoError(t, e.Suggest(ctx, ref, item.ID))
	req, err := e.RequestApproval(ctx, ref, item.ID)
	require.NoError(t, err)

	require.NoError(t, e.Send(ctx, item.ID, req.ID, Decision{Action: ActionRejected}))
	got, err := e.Get(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementRejected, got.Status)
	require.Zero(t, adapter.calls)
}

func TestEngagementSendFailureRecordsFailed(t *testing.T) {
	e, adapter := newEngagementFixture(t, `{"reply":true,"reply_hourly_limit":20}`)
	ctx := context.Background()
	adapter.failErr = &channel.AdapterError{Capability: channel.CapReply, Code: "boom"}

	item := seedEngagement(t, e, "你好")
	require.Error(t, approveAndSend(t, e, item.ID))

	got, err := e.Get(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementFailed, got.Status)
	require.Equal(t, 1, adapter.calls)
}

func TestClassifyCategories(t *testing.T) {
	cases := []struct {
		text     string
		category string
		risk     string
	}{
		{"请问怎么买？", CategoryLead, RiskLow},
		{"太好用了，感谢分享", CategoryPraise, RiskLow},
		{"加我微信领优惠券", CategorySpam, RiskLow},
		{"这东西太差了", CategoryComplaint, RiskMedium},
		{"投诉：要求退款", CategoryComplaint, RiskHigh},
	}
	for _, tc := range cases {
		category, risk := classifyEngagement(tc.text)
		require.Equal(t, tc.category, category, tc.text)
		require.Equal(t, tc.risk, risk, tc.text)
	}
}
