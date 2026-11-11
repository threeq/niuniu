package browser

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// newTestWorker builds a worker backed by an in-memory SQLite database with
// bot 1 registered. Jitter is disabled and retries default to one attempt so
// tests stay fast unless they opt in.
func newTestWorker(t *testing.T) (*Worker, *sql.DB) {
	t.Helper()
	store.Driver = "sqlite"

	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })

	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO ops_bots
		(id, owner_type, owner_id, role, pack_id, account_ref, display_name)
		VALUES (1, 'user', 2, 'account', 'xiaohongshu', 'acct-1', 'X')`)
	require.NoError(t, err)

	sessions := NewSessionManager(store.Wrap(raw).Queries(), t.TempDir())
	w := NewWorker(sessions)
	w.EvidenceDir = t.TempDir()
	w.JitterMin, w.JitterMax = 0, 0
	w.MaxAttempts = 1
	return w, raw
}

func addBot(t *testing.T, raw *sql.DB, id int64) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO ops_bots
		(id, owner_type, owner_id, role, pack_id, account_ref, display_name)
		VALUES (?, 'user', 2, 'account', 'xiaohongshu', ?, 'X')`,
		id, fmt.Sprintf("acct-%d", id))
	require.NoError(t, err)
}

func task() Task {
	return Task{BotID: 1, Backend: BackendLocal, Recipe: "publish", Input: map[string]string{"title": "t"}}
}

type fakeNotifier struct {
	mu   sync.Mutex
	reqs []ReloginRequest
}

func (f *fakeNotifier) RequestRelogin(_ context.Context, req ReloginRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func TestSessionEnsureCreatesProfileDir(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	sess, err := w.Sessions.Ensure(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, LoginUnknown, sess.LoginStatus)
	require.Equal(t, HealthUnknown, sess.Health)
	require.Equal(t, RiskNormal, sess.RiskState)
	require.NotEmpty(t, sess.ProfileDir)

	info, err := os.Stat(sess.ProfileDir)
	require.NoError(t, err)
	require.True(t, info.IsDir(), "profile dir must exist")

	again, err := w.Sessions.Ensure(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, sess.ID, again.ID, "Ensure must be idempotent")
	require.Equal(t, sess.ProfileDir, again.ProfileDir)
}

func TestSessionAcquireRejectsConcurrentLease(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()
	_, err := w.Sessions.Ensure(ctx, 1, BackendLocal)
	require.NoError(t, err)

	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "w1", time.Minute))
	require.ErrorIs(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "w2", time.Minute), ErrLeaseBusy,
		"a second operation on the same account must be rejected")

	require.ErrorIs(t, w.Sessions.Release(ctx, 1, BackendLocal, "w2"), ErrLeaseLost,
		"release must be owner-checked")

	require.NoError(t, w.Sessions.Release(ctx, 1, BackendLocal, "w1"))
	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "w2", time.Minute))

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, "w2", sess.LeaseOwner)
}

func TestSessionLeaseExpiresAllowsReacquire(t *testing.T) {
	w, raw := newTestWorker(t)
	ctx := context.Background()
	_, err := w.Sessions.Ensure(ctx, 1, BackendLocal)
	require.NoError(t, err)

	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "w1", time.Minute))

	// Force the stored expiry into the past; an abandoned worker's lease must
	// be reclaimable instead of wedging the account forever.
	_, err = raw.Exec(
		`UPDATE ops_browser_sessions SET lease_expires_at = ? WHERE bot_id = ? AND backend = ?`,
		time.Now().Add(-time.Minute), 1, BackendLocal)
	require.NoError(t, err)

	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "w2", time.Minute),
		"expired lease must be reclaimed")

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, "w2", sess.LeaseOwner)
}

func TestSessionAcquireWithoutSession(t *testing.T) {
	w, _ := newTestWorker(t)
	err := w.Sessions.Acquire(context.Background(), 99, BackendLocal, "w1", time.Minute)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestWorkerRunsTaskAndPersistsEvidence(t *testing.T) {
	w, _ := newTestWorker(t)
	var got Task
	w.Exec = func(_ context.Context, task Task) (Result, error) {
		got = task
		return Result{
			Values:     map[string]string{"url": "https://platform.example/note/1"},
			Receipt:    "note-1",
			Screenshot: []byte("png-bytes"),
		}, nil
	}

	res, err := w.Run(context.Background(), task())
	require.NoError(t, err)
	require.Equal(t, "publish", got.Recipe)
	require.Equal(t, "t", got.Input["title"])
	require.Equal(t, "note-1", res.Receipt)
	require.Equal(t, "https://platform.example/note/1", res.Values["url"])

	require.NotEmpty(t, res.EvidencePath)
	data, err := os.ReadFile(res.EvidencePath)
	require.NoError(t, err)
	require.Equal(t, []byte("png-bytes"), data)

	entries, err := os.ReadDir(filepath.Dir(res.EvidencePath))
	require.NoError(t, err)
	require.Len(t, entries, 2, "screenshot and receipt are both kept")

	sess, err := w.Sessions.State(context.Background(), 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, LoginValid, sess.LoginStatus)
	require.Equal(t, HealthHealthy, sess.Health)
	require.Equal(t, RiskNormal, sess.RiskState)
	require.Empty(t, sess.LastError)
	require.True(t, sess.LastLoginAt.Valid)

	require.NoError(t, w.Sessions.Acquire(context.Background(), 1, BackendLocal, "probe", time.Minute),
		"the lease must be released after a run")
}

func TestWorkerRejectsConcurrentLease(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()
	_, err := w.Sessions.Ensure(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "other-process", time.Minute))

	called := false
	w.Exec = func(context.Context, Task) (Result, error) {
		called = true
		return Result{}, nil
	}

	_, err = w.Run(ctx, task())
	require.ErrorIs(t, err, ErrLeaseBusy)
	require.False(t, called, "the recipe must not run while another lease is held")

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, "other-process", sess.LeaseOwner)
}

func TestWorkerMarksLoginExpired(t *testing.T) {
	w, _ := newTestWorker(t)
	notifier := &fakeNotifier{}
	w.Notifier = notifier
	var calls int32
	w.Exec = func(context.Context, Task) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{}, ErrLoginExpired
	}
	ctx := context.Background()

	_, err := w.Run(ctx, task())
	require.ErrorIs(t, err, ErrLoginExpired)

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, LoginExpired, sess.LoginStatus)
	require.Equal(t, HealthDegraded, sess.Health)
	require.Contains(t, sess.LastError, "login expired")
	require.Equal(t, 1, notifier.count(), "operator must be asked to re-login")
	require.Equal(t, int64(1), notifier.reqs[0].BotID)
	require.Equal(t, BackendLocal, notifier.reqs[0].Backend)

	// A subsequent run must not hammer the platform.
	_, err = w.Run(ctx, task())
	require.ErrorIs(t, err, ErrLoginExpired)
	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "expired login must not be retried")

	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "probe", time.Minute),
		"the lease must be released after a failed run")
}

func TestWorkerMarksRiskOnRateLimit(t *testing.T) {
	w, _ := newTestWorker(t)
	var calls int32
	w.Exec = func(context.Context, Task) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{}, fmt.Errorf("%w: 429 from platform", ErrRateLimited)
	}
	ctx := context.Background()

	_, err := w.Run(ctx, task())
	require.ErrorIs(t, err, ErrRateLimited)

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, RiskThrottled, sess.RiskState)
	require.Contains(t, sess.LastError, "429")

	_, err = w.Run(ctx, task())
	require.ErrorIs(t, err, ErrRateLimited)
	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "throttled accounts must pause automatic actions")
}

func TestWorkerMarksRecipeBroken(t *testing.T) {
	w, _ := newTestWorker(t)
	w.Exec = func(context.Context, Task) (Result, error) {
		return Result{}, fmt.Errorf("%w: selector .submit not found", ErrRecipeBroken)
	}
	ctx := context.Background()

	_, err := w.Run(ctx, task())
	require.ErrorIs(t, err, ErrRecipeBroken)

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, HealthDegraded, sess.Health)
	require.Contains(t, sess.LastError, "selector .submit")
}

func TestWorkerPausesOnBlocked(t *testing.T) {
	w, _ := newTestWorker(t)
	var calls int32
	w.Exec = func(context.Context, Task) (Result, error) {
		atomic.AddInt32(&calls, 1)
		return Result{}, ErrBlocked
	}
	ctx := context.Background()

	_, err := w.Run(ctx, task())
	require.ErrorIs(t, err, ErrBlocked)

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.Equal(t, RiskBlocked, sess.RiskState)
	require.Equal(t, HealthDown, sess.Health)

	_, err = w.Run(ctx, task())
	require.ErrorIs(t, err, ErrBlocked)
	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "blocked accounts must not act")
}

func TestWorkerSerializesSameAccount(t *testing.T) {
	w, _ := newTestWorker(t)
	var inFlight, maxInFlight int32
	w.Exec = func(context.Context, Task) (Result, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			prev := atomic.LoadInt32(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return Result{}, nil
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.Run(context.Background(), task())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&maxInFlight),
		"two operations on one account must not overlap")
}

func TestWorkerCapsGlobalConcurrency(t *testing.T) {
	w, raw := newTestWorker(t)
	addBot(t, raw, 2)
	w.MaxConcurrent = 1

	var inFlight, maxInFlight int32
	w.Exec = func(context.Context, Task) (Result, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			prev := atomic.LoadInt32(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return Result{}, nil
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for botID := int64(1); botID <= 2; botID++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, err := w.Run(context.Background(), Task{BotID: id, Backend: BackendLocal, Recipe: "publish"})
			errs <- err
		}(botID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&maxInFlight),
		"the global concurrency cap must be enforced")
}

func TestWorkerRetriesTransient(t *testing.T) {
	w, _ := newTestWorker(t)
	w.MaxAttempts = 3
	w.RetryBase = time.Millisecond
	var calls int32
	w.Exec = func(context.Context, Task) (Result, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return Result{}, fmt.Errorf("%w: navigation race", ErrTransient)
		}
		return Result{Receipt: "ok"}, nil
	}

	res, err := w.Run(context.Background(), task())
	require.NoError(t, err)
	require.Equal(t, "ok", res.Receipt)
	require.EqualValues(t, 3, atomic.LoadInt32(&calls))
}

func TestWorkerRejectsInvalidTask(t *testing.T) {
	w, _ := newTestWorker(t)
	w.Exec = func(context.Context, Task) (Result, error) { return Result{}, nil }

	_, err := w.Run(context.Background(), Task{BotID: 1, Backend: "mainframe"})
	require.ErrorIs(t, err, ErrInvalidSession)

	_, err = w.Run(context.Background(), Task{BotID: 0, Backend: BackendLocal})
	require.ErrorIs(t, err, ErrInvalidSession)
}

func TestWorkerReportsRiskSignalsToController(t *testing.T) {
	cases := []struct {
		name   string
		exec   error
		signal string
	}{
		{"login", ErrLoginExpired, "login_expired"},
		{"rate", fmt.Errorf("%w: 429", ErrRateLimited), "rate_limited"},
		{"blocked", ErrBlocked, "banned"},
		{"recipe", fmt.Errorf("%w: selector", ErrRecipeBroken), "recipe_broken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := newTestWorker(t)
			w.Exec = func(context.Context, Task) (Result, error) { return Result{}, tc.exec }

			var (
				mu      sync.Mutex
				signals []string
			)
			w.OnRisk = func(_ context.Context, botID int64, signal string) {
				require.Equal(t, int64(1), botID)
				mu.Lock()
				defer mu.Unlock()
				signals = append(signals, signal)
			}

			_, err := w.Run(context.Background(), task())
			require.Error(t, err)

			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{tc.signal}, signals)
		})
	}
}

func TestWorkerDoesNotReportRiskForCancellation(t *testing.T) {
	w, _ := newTestWorker(t)
	w.Exec = func(context.Context, Task) (Result, error) { return Result{}, context.Canceled }

	var called bool
	w.OnRisk = func(context.Context, int64, string) { called = true }
	_, err := w.Run(context.Background(), task())
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}

// A login task must still run when the stored session is expired: it is the
// only path that can recover the account. Every other task stays refused.
func TestWorkerRunsLoginRecipeOnExpiredSession(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()
	_, err := w.Sessions.Ensure(ctx, 1, BackendLocal)
	require.NoError(t, err)
	setExpired := func() {
		expired := LoginExpired
		_, err := w.Sessions.Update(ctx, 1, BackendLocal, StatePatch{LoginStatus: &expired})
		require.NoError(t, err)
	}
	setExpired()
	w.Exec = func(context.Context, Task) (Result, error) { return Result{}, nil }

	_, err = w.Run(ctx, Task{BotID: 1, Backend: BackendLocal, Recipe: "login", IsLogin: true})
	require.NoError(t, err)

	setExpired()
	_, err = w.Run(ctx, Task{BotID: 1, Backend: BackendLocal, Recipe: "publish"})
	require.ErrorIs(t, err, ErrLoginExpired)
}
