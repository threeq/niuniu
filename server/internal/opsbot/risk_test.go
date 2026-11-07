package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type riskNotifyRecord struct {
	botID  int64
	reason string
}

type riskFixture struct {
	risk     *Risk
	db       *store.DB
	raw      *sql.DB
	approval *Approval
	sessions *browser.SessionManager

	notifies  []riskNotifyRecord
	notifyErr error
	verifyErr error
}

func newTestRisk(t *testing.T) *riskFixture {
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
		`INSERT INTO ops_bots (
			id, owner_type, owner_id, role, pack_id, account_ref, display_name,
			control_workspace_id, status
		) VALUES (1, 'user', 2, 'account', 'xiaohongshu', 'brand', '品牌号', 10, 'active')`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	db := store.Wrap(raw)
	svc := NewService(db.Queries(), nil)
	approval := NewApproval(db, ApprovalPolicy{})
	sessions := browser.NewSessionManager(db.Queries(), t.TempDir())
	_, err = sessions.Ensure(context.Background(), 1, browser.BackendLocal)
	require.NoError(t, err)

	f := &riskFixture{db: db, raw: raw, approval: approval, sessions: sessions}
	f.risk = &Risk{
		Bots:        svc,
		Approvals:   approval,
		Engagements: db.Queries(),
		Sessions:    sessions,
		Backend:     browser.BackendLocal,
	}
	f.risk.Notify = func(_ context.Context, botID int64, reason string) error {
		f.notifies = append(f.notifies, riskNotifyRecord{botID: botID, reason: reason})
		return f.notifyErr
	}
	f.risk.VerifyLogin = func(context.Context, int64) error { return f.verifyErr }
	return f
}

func (f *riskFixture) bot(t *testing.T) store.OpsBot {
	t.Helper()
	bot, err := f.db.Queries().GetOpsBot(context.Background(), 1)
	require.NoError(t, err)
	return bot
}

func (f *riskFixture) session(t *testing.T) store.OpsBrowserSession {
	t.Helper()
	sess, err := f.sessions.State(context.Background(), 1, browser.BackendLocal)
	require.NoError(t, err)
	return sess
}

func (f *riskFixture) addPendingApproval(t *testing.T) ApprovalRequest {
	t.Helper()
	req, err := f.approval.Create(context.Background(), ApprovalInput{
		BotID:       1,
		WorkspaceID: 10,
		Type:        ApprovalPublish,
		PreviewJSON: `{"title":"t","summary":"s"}`,
	})
	require.NoError(t, err)
	return req
}

func (f *riskFixture) seedEngagement(t *testing.T, status string) store.OpsEngagement {
	t.Helper()
	ctx := context.Background()
	row, err := f.db.Queries().UpsertOpsEngagement(ctx, store.UpsertOpsEngagementParams{
		BotID:                1,
		PlatformEngagementID: fmt.Sprintf("risk-%s-%d", status, len(f.notifies)),
		Kind:                 "comment",
		Author:               "u1",
		Text:                 "多少钱？",
	})
	require.NoError(t, err)
	if status == EngagementNew {
		return row
	}
	row, err = f.db.Queries().UpdateOpsEngagementStatus(ctx, store.UpdateOpsEngagementStatusParams{
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

func (f *riskFixture) pendingApprovals(t *testing.T) int {
	t.Helper()
	var n int
	err := f.raw.QueryRow(
		`SELECT COUNT(*) FROM agent_ask_user_requests WHERE kind = ? AND status = ?`,
		opsApprovalKind, ApprovalStatusPending,
	).Scan(&n)
	require.NoError(t, err)
	return n
}

func TestRiskKillDisablesAndCancelsQueuedActions(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	f.addPendingApproval(t)
	approved := f.seedEngagement(t, EngagementApproved)
	sent := f.seedEngagement(t, EngagementSent)

	ev, err := f.risk.Kill(ctx, 1, "manual")
	require.NoError(t, err)

	require.Equal(t, StatusDisabled, f.bot(t).Status)
	require.Equal(t, 1, ev.ApprovalsCancelled)
	require.Equal(t, 1, ev.EngagementsCleared)
	require.True(t, ev.Notified)
	require.Equal(t, 0, f.pendingApprovals(t))
	require.Len(t, f.notifies, 1)
	require.Equal(t, int64(1), f.notifies[0].botID)

	approvedRow, err := f.db.Queries().GetOpsEngagement(ctx, approved.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementRejected, approvedRow.Status)

	// Already-sent replies are historical and must not be rewritten.
	sentRow, err := f.db.Queries().GetOpsEngagement(ctx, sent.ID)
	require.NoError(t, err)
	require.Equal(t, EngagementSent, sentRow.Status)
}

func TestRiskSignalsPauseAccount(t *testing.T) {
	cases := []struct {
		signal      string
		riskState   string
		loginStatus string
		health      string
	}{
		{RiskSignalLoginExpired, browser.RiskNormal, browser.LoginExpired, browser.HealthDegraded},
		{RiskSignalRateLimited, browser.RiskThrottled, browser.LoginUnknown, browser.HealthDegraded},
		{RiskSignalCaptcha, browser.RiskBlocked, browser.LoginUnknown, browser.HealthDegraded},
		{RiskSignalBanned, browser.RiskBlocked, browser.LoginExpired, browser.HealthDown},
	}
	for _, tc := range cases {
		t.Run(tc.signal, func(t *testing.T) {
			f := newTestRisk(t)
			ev, err := f.risk.Evaluate(context.Background(), 1, tc.signal)
			require.NoError(t, err)
			require.Equal(t, StatusPaused, f.bot(t).Status)
			require.True(t, ev.Paused)
			require.True(t, ev.Notified)
			require.Equal(t, tc.riskState, ev.RiskState)
			require.Len(t, f.notifies, 1)

			sess := f.session(t)
			require.Equal(t, tc.riskState, sess.RiskState)
			require.Equal(t, tc.loginStatus, sess.LoginStatus)
			require.Equal(t, tc.health, sess.Health)
		})
	}
}

func TestRiskSignalNotifiesOncePerIncident(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()

	first, err := f.risk.Evaluate(ctx, 1, RiskSignalLoginExpired)
	require.NoError(t, err)
	require.True(t, first.Notified)
	require.Len(t, f.notifies, 1)

	again, err := f.risk.Evaluate(ctx, 1, RiskSignalLoginExpired)
	require.NoError(t, err)
	require.False(t, again.Notified)
	require.Len(t, f.notifies, 1)

	// A different signal is a different incident and must page the operator.
	other, err := f.risk.Evaluate(ctx, 1, RiskSignalRateLimited)
	require.NoError(t, err)
	require.True(t, other.Notified)
	require.Len(t, f.notifies, 2)

	// A restarted process must not re-page for an incident it already reported:
	// the marker is persisted on the browser session, not just in memory.
	restarted := &Risk{
		Bots:        f.risk.Bots,
		Approvals:   f.approval,
		Engagements: f.db.Queries(),
		Sessions:    f.sessions,
		Backend:     browser.BackendLocal,
		Notify: func(_ context.Context, botID int64, reason string) error {
			f.notifies = append(f.notifies, riskNotifyRecord{botID: botID, reason: reason})
			return nil
		},
	}
	persisted, err := restarted.Evaluate(ctx, 1, RiskSignalRateLimited)
	require.NoError(t, err)
	require.False(t, persisted.Notified)
	require.Len(t, f.notifies, 2)
}

func TestRiskEvaluateWithoutBrowserSession(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	// API-only packs never create a browser session row; the pause must still
	// be applied and de-duplicated in memory.
	_, err := f.raw.Exec(`DELETE FROM ops_browser_sessions WHERE bot_id = 1`)
	require.NoError(t, err)

	first, err := f.risk.Evaluate(ctx, 1, RiskSignalLoginExpired)
	require.NoError(t, err)
	require.True(t, first.Notified)
	require.Equal(t, StatusPaused, f.bot(t).Status)

	again, err := f.risk.Evaluate(ctx, 1, RiskSignalLoginExpired)
	require.NoError(t, err)
	require.False(t, again.Notified)
	require.Len(t, f.notifies, 1)
}

func TestRiskSignalKeepsKillSwitch(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	_, err := f.risk.Kill(ctx, 1, "manual")
	require.NoError(t, err)
	require.Len(t, f.notifies, 1)

	ev, err := f.risk.Evaluate(ctx, 1, RiskSignalBanned)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, f.bot(t).Status)
	require.False(t, ev.Notified)
	require.Len(t, f.notifies, 1)
}

func TestRiskInvalidSignal(t *testing.T) {
	f := newTestRisk(t)
	_, err := f.risk.Evaluate(context.Background(), 1, "meteor_strike")
	require.ErrorIs(t, err, ErrInvalidRiskSignal)
	require.Equal(t, StatusActive, f.bot(t).Status)
}

func TestRiskHandleAdapterErrorRoutes(t *testing.T) {
	ctx := context.Background()

	t.Run("retryable is retried", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1,
			&channel.AdapterError{Capability: channel.CapPublish, Code: channel.CodeTransport, Retryable: true})
		require.NoError(t, err)
		require.Equal(t, RiskActionRetry, v.Action)
		require.Positive(t, v.RetryAfter)
		require.Equal(t, StatusActive, f.bot(t).Status)
		require.Empty(t, f.notifies)
	})

	t.Run("plain error is retried", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1, errors.New("socket closed"))
		require.NoError(t, err)
		require.Equal(t, RiskActionRetry, v.Action)
		require.Equal(t, StatusActive, f.bot(t).Status)
	})

	t.Run("rate limit pauses even though retryable", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1,
			&channel.AdapterError{Capability: channel.CapPublish, Code: channel.CodeRateLimited, Retryable: true})
		require.NoError(t, err)
		require.Equal(t, RiskActionPause, v.Action)
		require.Equal(t, RiskSignalRateLimited, v.Signal)
		require.Equal(t, StatusPaused, f.bot(t).Status)
		require.Len(t, f.notifies, 1)
	})

	t.Run("login expired pauses", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1,
			&channel.AdapterError{Capability: channel.CapEngagements, Code: channel.CodeLoginExpired})
		require.NoError(t, err)
		require.Equal(t, RiskActionPause, v.Action)
		require.Equal(t, RiskSignalLoginExpired, v.Signal)
		require.Equal(t, StatusPaused, f.bot(t).Status)
	})

	t.Run("recipe broken degrades to manual package", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1,
			&channel.AdapterError{Capability: channel.CapPublish, Code: channel.CodeRecipeBroken})
		require.NoError(t, err)
		require.Equal(t, RiskActionDegrade, v.Action)
		require.True(t, v.Notified)
		require.Equal(t, StatusActive, f.bot(t).Status)
		require.Len(t, f.notifies, 1)
	})

	t.Run("non-retryable failure pauses", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1,
			&channel.AdapterError{Capability: channel.CapPublish, Code: channel.CodeInvalidPayload})
		require.NoError(t, err)
		require.Equal(t, RiskActionPause, v.Action)
		require.Equal(t, RiskSignalAdapterFailed, v.Signal)
		require.Equal(t, StatusPaused, f.bot(t).Status)
	})

	t.Run("cancellation stops without pausing", func(t *testing.T) {
		f := newTestRisk(t)
		v, err := f.risk.HandleAdapterError(ctx, 1, context.Canceled)
		require.NoError(t, err)
		require.Equal(t, RiskActionStop, v.Action)
		require.Equal(t, StatusActive, f.bot(t).Status)
		require.Empty(t, f.notifies)
	})

	t.Run("nil error is rejected", func(t *testing.T) {
		f := newTestRisk(t)
		_, err := f.risk.HandleAdapterError(ctx, 1, nil)
		require.ErrorIs(t, err, ErrInvalidInput)
	})
}

func TestRiskResumeRequiresReloginVerification(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	_, err := f.risk.Evaluate(ctx, 1, RiskSignalLoginExpired)
	require.NoError(t, err)
	require.Len(t, f.notifies, 1)

	f.verifyErr = errors.New("still logged out")
	_, err = f.risk.Resume(ctx, 1)
	require.ErrorIs(t, err, ErrReloginRequired)
	require.Equal(t, StatusPaused, f.bot(t).Status)
	require.Equal(t, browser.LoginExpired, f.session(t).LoginStatus)

	// Verification passing is mandatory before the account may resume.
	f.verifyErr = nil
	bot, err := f.risk.Resume(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, StatusActive, bot.Status)
	require.Equal(t, browser.LoginValid, f.session(t).LoginStatus)
	require.Equal(t, browser.RiskNormal, f.session(t).RiskState)
	require.Empty(t, f.session(t).LastError)
	// Resuming is not a new incident, so it pages nobody.
	require.Len(t, f.notifies, 1)

	// The next occurrence of the same signal is a new incident.
	ev, err := f.risk.Evaluate(ctx, 1, RiskSignalLoginExpired)
	require.NoError(t, err)
	require.True(t, ev.Notified)
	require.Len(t, f.notifies, 2)
}

func TestRiskResumeFallsBackToSessionLoginState(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	f.risk.VerifyLogin = nil

	_, err := f.risk.Evaluate(ctx, 1, RiskSignalRateLimited)
	require.NoError(t, err)

	// A freshly created session has never logged in, so a resume is refused.
	_, err = f.risk.Resume(ctx, 1)
	require.ErrorIs(t, err, ErrReloginRequired)
	require.Equal(t, StatusPaused, f.bot(t).Status)

	valid := browser.LoginValid
	_, err = f.sessions.Update(ctx, 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &valid})
	require.NoError(t, err)

	bot, err := f.risk.Resume(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, StatusActive, bot.Status)
}

func TestRiskSignalPreservesSessionErrorDetail(t *testing.T) {
	f := newTestRisk(t)
	ctx := context.Background()
	detail := "429 from platform: request throttled"
	_, err := f.sessions.Update(ctx, 1, browser.BackendLocal, browser.StatePatch{LastError: &detail})
	require.NoError(t, err)

	_, err = f.risk.Evaluate(ctx, 1, RiskSignalRateLimited)
	require.NoError(t, err)

	sess := f.session(t)
	require.Contains(t, sess.LastError, "risk:rate_limited")
	require.Contains(t, sess.LastError, detail, "the diagnostic text survives the marker")

	// A restarted controller must still recognize the incident from the
	// marker-with-detail form.
	restarted := &Risk{
		Bots:        f.risk.Bots,
		Approvals:   f.approval,
		Engagements: f.db.Queries(),
		Sessions:    f.sessions,
		Backend:     browser.BackendLocal,
		Notify: func(context.Context, int64, string) error {
			t.Fatal("a repeated incident must not notify")
			return nil
		},
	}
	ev, err := restarted.Evaluate(ctx, 1, RiskSignalRateLimited)
	require.NoError(t, err)
	require.False(t, ev.Notified)
}
