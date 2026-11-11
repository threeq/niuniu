package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type fakeLoginAdapter struct {
	hasLogin  bool
	loginWait time.Duration
	loginErr  error
	verifyErr error
	onLogin   func(context.Context)

	loginCalls  int32
	verifyCalls int32
}

func (a *fakeLoginAdapter) Capabilities() channel.CapabilitySet {
	return channel.CapabilitySet{channel.CapPublish: true}
}

func (a *fakeLoginAdapter) EnsureSession(ctx context.Context, ref string) error {
	_, err := a.Login(ctx, ref)
	return err
}

func (a *fakeLoginAdapter) Publish(context.Context, any) (channel.Receipt, error) {
	return channel.Receipt{}, nil
}

func (a *fakeLoginAdapter) FetchMetrics(context.Context, string, string) (map[string]float64, error) {
	return nil, nil
}

func (a *fakeLoginAdapter) ListEngagements(context.Context, string) ([]channel.Engagement, error) {
	return nil, nil
}

func (a *fakeLoginAdapter) Reply(context.Context, string, string) (channel.Receipt, error) {
	return channel.Receipt{}, nil
}

func (a *fakeLoginAdapter) HealthCheck(context.Context) error { return nil }

func (a *fakeLoginAdapter) HasLoginRecipe() bool { return a.hasLogin }

func (a *fakeLoginAdapter) Login(ctx context.Context, _ string) (browser.Result, error) {
	atomic.AddInt32(&a.loginCalls, 1)
	if a.loginWait > 0 {
		select {
		case <-time.After(a.loginWait):
		case <-ctx.Done():
			return browser.Result{}, ctx.Err()
		}
	}
	if a.loginErr == nil && a.onLogin != nil {
		a.onLogin(ctx)
	}
	return browser.Result{EvidencePath: "/tmp/qr.png"}, a.loginErr
}

func (a *fakeLoginAdapter) VerifySession(context.Context, string) error {
	atomic.AddInt32(&a.verifyCalls, 1)
	return a.verifyErr
}

func newLoginFixture(t *testing.T, adapter channel.ChannelAdapter) (*LoginManager, *browser.SessionManager) {
	t.Helper()
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO ops_bots
		(id, owner_type, owner_id, role, pack_id, account_ref, display_name, status)
		VALUES (1, 'user', 2, 'account', 'xiaohongshu', 'brand', '品牌号', 'active')`)
	require.NoError(t, err)

	q := store.Wrap(raw).Queries()
	sessions := browser.NewSessionManager(q, t.TempDir())
	_, err = sessions.Ensure(context.Background(), 1, browser.BackendLocal)
	require.NoError(t, err)

	m := NewLoginManager(func(context.Context, int64) (channel.ChannelAdapter, error) {
		return adapter, nil
	}, q, sessions)
	m.LoginTimeout = 3 * time.Second
	m.VerifyTimeout = 300 * time.Millisecond
	return m, sessions
}

func waitForLoginState(t *testing.T, m *LoginManager, want LoginState) LoginStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, err := m.Status(context.Background(), 1)
		require.NoError(t, err)
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("login state = %q, want %q (last_error %q)", st.State, want, st.LastError)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLoginManagerStartIsAsync(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true, loginWait: 250 * time.Millisecond}
	m, _ := newLoginFixture(t, adapter)

	started, err := m.Start(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginStateRunning, started.State)
	require.Equal(t, LoginActionLogin, started.NextAction)

	done := waitForLoginState(t, m, LoginStateSucceeded)
	require.Equal(t, "/tmp/qr.png", done.EvidencePath)
	require.Equal(t, int32(1), atomic.LoadInt32(&adapter.loginCalls))
}

func TestLoginManagerStartIsIdempotentWhileRunning(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true, loginWait: 300 * time.Millisecond}
	m, _ := newLoginFixture(t, adapter)

	_, err := m.Start(context.Background(), 1)
	require.NoError(t, err)
	again, err := m.Start(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginStateRunning, again.State)

	waitForLoginState(t, m, LoginStateSucceeded)
	require.Equal(t, int32(1), atomic.LoadInt32(&adapter.loginCalls))
}

func TestLoginManagerStartNotRequired(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: false}
	m, _ := newLoginFixture(t, adapter)

	got, err := m.Start(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginStateNotRequired, got.State)
	require.Equal(t, LoginActionNone, got.NextAction)
	require.Equal(t, int32(0), atomic.LoadInt32(&adapter.loginCalls))
}

func TestLoginManagerStartSkipsValidSession(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true}
	m, sessions := newLoginFixture(t, adapter)
	valid := browser.LoginValid
	_, err := sessions.Update(context.Background(), 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &valid})
	require.NoError(t, err)

	_, err = m.Start(context.Background(), 1)
	require.NoError(t, err)
	waitForLoginState(t, m, LoginStateSucceeded)
	require.Equal(t, int32(0), atomic.LoadInt32(&adapter.loginCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&adapter.verifyCalls))
}

func TestLoginManagerStartRecoversExpiredSession(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true, verifyErr: errors.New("still expired")}
	m, sessions := newLoginFixture(t, adapter)
	expired := browser.LoginExpired
	_, err := sessions.Update(context.Background(), 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &expired})
	require.NoError(t, err)
	// The real BrowserAdapter's Worker marks the session valid on success.
	adapter.onLogin = func(ctx context.Context) {
		valid := browser.LoginValid
		_, _ = sessions.Update(ctx, 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &valid})
	}

	_, err = m.Start(context.Background(), 1)
	require.NoError(t, err)
	waitForLoginState(t, m, LoginStateSucceeded)
	// An expired row goes straight to the interactive login: no spurious probe.
	require.Equal(t, int32(1), atomic.LoadInt32(&adapter.loginCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&adapter.verifyCalls))
}

func TestLoginManagerVerifyUsesSessionVerifier(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true, verifyErr: errors.New("expired")}
	m, _ := newLoginFixture(t, adapter)

	err := m.Verify(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&adapter.verifyCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&adapter.loginCalls))
}

func TestLoginManagerStatusNextAction(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true}
	m, sessions := newLoginFixture(t, adapter)

	st, err := m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, browser.LoginUnknown, st.LoginStatus)
	require.Equal(t, LoginActionLogin, st.NextAction)

	valid := browser.LoginValid
	_, err = sessions.Update(context.Background(), 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &valid})
	require.NoError(t, err)
	st, err = m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginActionNone, st.NextAction)

	expired := browser.LoginExpired
	_, err = sessions.Update(context.Background(), 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &expired})
	require.NoError(t, err)
	st, err = m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginActionRelogin, st.NextAction)

	adapter.hasLogin = false
	st, err = m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginActionNone, st.NextAction)
}

type slowVerifyAdapter struct {
	fakeLoginAdapter
	delay time.Duration
}

func (a *slowVerifyAdapter) VerifySession(ctx context.Context, _ string) error {
	select {
	case <-time.After(a.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A resume/onboarding probe must be bounded: it can never wait for a QR scan.
func TestLoginManagerVerifyBoundsTheProbe(t *testing.T) {
	adapter := &slowVerifyAdapter{
		fakeLoginAdapter: fakeLoginAdapter{hasLogin: true},
		delay:            2 * time.Second,
	}
	m, _ := newLoginFixture(t, adapter)
	m.VerifyTimeout = 50 * time.Millisecond

	start := time.Now()
	err := m.Verify(context.Background(), 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
}

func TestLoginManagerStatusDropsStaleSuccess(t *testing.T) {
	adapter := &fakeLoginAdapter{hasLogin: true}
	m, sessions := newLoginFixture(t, adapter)
	_, err := m.Start(context.Background(), 1)
	require.NoError(t, err)
	waitForLoginState(t, m, LoginStateSucceeded)

	expired := browser.LoginExpired
	_, err = sessions.Update(context.Background(), 1, browser.BackendLocal, browser.StatePatch{LoginStatus: &expired})
	require.NoError(t, err)

	st, err := m.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, LoginStateIdle, st.State)
	require.Equal(t, LoginActionRelogin, st.NextAction)
}
