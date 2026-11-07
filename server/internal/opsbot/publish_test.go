package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

const (
	testPublishBotID   = 1
	testPublishIssueID = 2
)

// testAdapter is a scripted ChannelAdapter. errs is consumed one entry per
// Publish call; once exhausted Publish succeeds. always overrides errs.
type testAdapter struct {
	calls        int
	ensureCalls  int
	caps         channel.CapabilitySet
	errs         []error
	always       error
	receipt      channel.Receipt
	ensureErr    error
	publishCalls []any
}

func (a *testAdapter) Capabilities() channel.CapabilitySet {
	if a.caps == nil {
		return channel.CapabilitySet{channel.CapPublish: true}
	}
	return a.caps
}

func (a *testAdapter) EnsureSession(context.Context, string) error {
	a.ensureCalls++
	return a.ensureErr
}

func (a *testAdapter) Publish(_ context.Context, payload any) (channel.Receipt, error) {
	a.calls++
	a.publishCalls = append(a.publishCalls, payload)
	if a.always != nil {
		return channel.Receipt{}, a.always
	}
	if len(a.errs) > 0 {
		err := a.errs[0]
		a.errs = a.errs[1:]
		if err != nil {
			return channel.Receipt{}, err
		}
	}
	if a.receipt.ExternalID == "" && a.receipt.URL == "" {
		return channel.Receipt{ExternalID: "wx-1001", URL: "https://mp.weixin.qq.com/s/abc"}, nil
	}
	return a.receipt, nil
}

func (a *testAdapter) FetchMetrics(context.Context, string, string) (map[string]float64, error) {
	return nil, channel.ErrCapabilityUnavailable
}

func (a *testAdapter) ListEngagements(context.Context, string) ([]channel.Engagement, error) {
	return nil, channel.ErrCapabilityUnavailable
}

func (a *testAdapter) Reply(context.Context, string, string) (channel.Receipt, error) {
	return channel.Receipt{}, channel.ErrCapabilityUnavailable
}

func (a *testAdapter) HealthCheck(context.Context) error { return nil }

// failingAdapter always fails, as in the implementation plan.
type failingAdapter struct{ testAdapter }

type problemRecorder struct {
	problems []PublishProblem
	err      error
}

func (r *problemRecorder) OpenPublishProblem(_ context.Context, p PublishProblem) (int64, error) {
	r.problems = append(r.problems, p)
	if r.err != nil {
		return 0, r.err
	}
	return 700 + int64(len(r.problems)), nil
}

type notifyRecorder struct {
	events  []string
	details []map[string]any
}

func (r *notifyRecorder) record(_ context.Context, _ int64, event string, detail map[string]any) error {
	r.events = append(r.events, event)
	r.details = append(r.details, detail)
	return nil
}

type publishEnv struct {
	t        *testing.T
	db       *sql.DB
	q        *store.Queries
	problems *problemRecorder
	notify   *notifyRecorder
	manual   string
	sleeps   []time.Duration
	now      time.Time
}

func newPublishEnv(t *testing.T) *publishEnv {
	t.Helper()
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'ops', 'user', 1)`,
		`INSERT INTO columns (id, project_id, name) VALUES (1, 1, 'todo')`,
		`INSERT INTO issues (id, column_id, title) VALUES (2, 1, 'content')`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, pack_id, account_ref, display_name)
		 VALUES (1, 'user', 1, 'account', 'wechat_mp', 'acc-1', 'Bot A')`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	env := &publishEnv{
		t:        t,
		db:       raw,
		q:        store.New(raw),
		problems: &problemRecorder{},
		notify:   &notifyRecorder{},
		manual:   t.TempDir(),
		now:      time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC),
	}
	return env
}

func (e *publishEnv) publisher(adapter channel.ChannelAdapter) *Publisher {
	p := &Publisher{
		Q:        e.q,
		DB:       store.Wrap(e.db),
		Adapter:  adapter,
		Manual:   channel.NewManualAdapter(e.manual),
		Events:   e.q,
		Problems: e.problems,
		Notify:   e.notify.record,
		Now:      func() time.Time { return e.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			e.sleeps = append(e.sleeps, d)
			return nil
		},
	}
	return p
}

func (e *publishEnv) record(t *testing.T, botID int64, key string) store.OpsPublishRecord {
	t.Helper()
	rec, err := e.q.GetOpsPublishRecord(context.Background(), store.GetOpsPublishRecordParams{
		BotID: botID, IdempotencyKey: key,
	})
	require.NoError(t, err)
	return rec
}

func basePublishInput() PublishInput {
	return PublishInput{
		BotID:          testPublishBotID,
		IssueID:        testPublishIssueID,
		ContentVersion: 1,
		Platform:       "wechat_mp",
		AccountRef:     "acc-1",
		Title:          "标题",
		Body:           "正文",
		MediaPaths:     []string{"/tmp/cover.png"},
		Params:         map[string]string{"author": "运营"},
	}
}

func TestPublishIsIdempotent(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{}
	p := env.publisher(adapter)

	in := PublishInput{BotID: testPublishBotID, IssueID: testPublishIssueID, ContentVersion: 1, IdempotencyKey: "k1"}
	first, err := p.Publish(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, first.Status)

	second, err := p.Publish(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, first.RecordID, second.RecordID)
	require.True(t, second.Duplicate)
	require.Equal(t, 1, adapter.calls, "adapter must be called once")
	require.Equal(t, PublishStatusPublished, second.Status)
}

func TestPublishDerivedKeyIsStableAndUniquePerVersion(t *testing.T) {
	in := basePublishInput()
	first := PublishIdempotencyKey(in)
	require.Equal(t, first, PublishIdempotencyKey(in))
	require.Len(t, first, 64)

	other := in
	other.ContentVersion = 2
	require.NotEqual(t, first, PublishIdempotencyKey(other))

	scheduled := in
	at := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	scheduled.ScheduledAt = &at
	require.NotEqual(t, first, PublishIdempotencyKey(scheduled))
}

func TestPublishFallsBackToManualPackage(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &failingAdapter{testAdapter{always: &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       channel.CodeUnsupported,
		Err:        errors.New("wechat: publish api missing"),
	}}}
	p := env.publisher(adapter)

	res, err := p.Publish(context.Background(), basePublishInput())
	require.NoError(t, err)
	require.Equal(t, PublishModeManualPackage, res.Mode)
	require.Equal(t, PublishStatusManualPending, res.Status)
	require.NotEmpty(t, res.PackageDir)
	require.Equal(t, 1, adapter.calls, "unsupported is not retryable")

	payloadPath := filepath.Join(res.PackageDir, "payload.json")
	raw, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var pkg channel.ManualPublishPayload
	require.NoError(t, json.Unmarshal(raw, &pkg))
	require.Equal(t, "标题", pkg.Title)
	require.Equal(t, "wechat_mp", pkg.Platform)
	require.NotEmpty(t, pkg.IdempotencyKey, "manual package carries the idempotency key")

	rec := env.record(t, testPublishBotID, pkg.IdempotencyKey)
	require.Equal(t, PublishStatusManualPending, rec.Status)
	require.False(t, rec.PublishedAt.Valid, "a manual hand-off is not published")
	var stored publishRecordDetail
	require.NoError(t, json.Unmarshal([]byte(rec.Receipt), &stored))
	require.Equal(t, res.PackageDir, stored.PackageDir)
	require.Equal(t, channel.CodeUnsupported, stored.Code)

	require.Contains(t, env.notify.events, "publish_manual_pending")

	// Re-publishing the same key must not write a second package or re-call.
	again, err := p.Publish(context.Background(), basePublishInput())
	require.NoError(t, err)
	require.Equal(t, res.RecordID, again.RecordID)
	require.Equal(t, 1, adapter.calls)
	require.Equal(t, PublishModeManualPackage, again.Mode)
}

func TestPublishSkipsAdapterWhenPublishCapabilityMissing(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{caps: channel.CapabilitySet{channel.CapMetrics: true}}
	p := env.publisher(adapter)

	res, err := p.Publish(context.Background(), basePublishInput())
	require.NoError(t, err)
	require.Equal(t, PublishModeManualPackage, res.Mode)
	require.Zero(t, adapter.calls, "the adapter is never called when it cannot publish")
	require.NotEmpty(t, res.PackageDir)
}

func TestPublishNonRetryableErrorOpensProblem(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{always: &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       channel.CodeCredentialInvalid,
		Retryable:  false,
		Err:        errors.New("invalid appsecret"),
	}}
	p := env.publisher(adapter)

	res, err := p.Publish(context.Background(), basePublishInput())
	require.Error(t, err)
	require.Equal(t, PublishStatusFailed, res.Status)
	require.Equal(t, PublishModeFailed, res.Mode)
	require.NotZero(t, res.ProblemIssueID)
	require.Equal(t, 1, adapter.calls, "a non-retryable error is not retried")

	require.Len(t, env.problems.problems, 1)
	problem := env.problems.problems[0]
	require.Equal(t, channel.CodeCredentialInvalid, problem.Code)
	require.False(t, problem.Retryable)
	require.Equal(t, int64(testPublishIssueID), problem.IssueID)
	require.Equal(t, 1, problem.Attempts)

	rec := env.record(t, testPublishBotID, PublishIdempotencyKey(basePublishInput()))
	require.Equal(t, PublishStatusFailed, rec.Status)
	require.False(t, rec.PublishedAt.Valid)
	require.Contains(t, rec.Receipt, channel.CodeCredentialInvalid)
	require.Contains(t, env.notify.events, "publish_failed")

	// The failed record is terminal for this key: a retry must not hit the
	// platform again.
	_, err = p.Publish(context.Background(), basePublishInput())
	require.NoError(t, err)
	require.Equal(t, 1, adapter.calls)
	require.Len(t, env.problems.problems, 1)
}

func TestPublishRetriesRetryableErrorThenSucceeds(t *testing.T) {
	env := newPublishEnv(t)
	rateLimited := &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       channel.CodeRateLimited,
		Retryable:  true,
		Err:        errors.New("45009 rate limit"),
	}
	adapter := &testAdapter{errs: []error{rateLimited, rateLimited, nil}}
	p := env.publisher(adapter)

	res, err := p.Publish(context.Background(), basePublishInput())
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, res.Status)
	require.Equal(t, 3, res.Attempts)
	require.Equal(t, 3, adapter.calls)
	require.Len(t, env.sleeps, 2)
	require.Equal(t, defaultPublishBackoff, env.sleeps[0], "first backoff is the base delay")
	require.Equal(t, 2*defaultPublishBackoff, env.sleeps[1], "backoff is exponential")
}

func TestPublishRetriesExhaustedThenFailsWithProblem(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{always: &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       channel.CodeRateLimited,
		Retryable:  true,
		Err:        errors.New("45009 rate limit"),
	}}
	p := env.publisher(adapter)

	res, err := p.Publish(context.Background(), basePublishInput())
	require.Error(t, err)
	require.Equal(t, PublishStatusFailed, res.Status)
	require.Equal(t, defaultPublishAttempts, res.Attempts)
	require.Equal(t, defaultPublishAttempts, adapter.calls)
	require.NotZero(t, res.ProblemIssueID)
	require.True(t, env.problems.problems[0].Retryable)
}

func TestPublishPersistsReceiptURLScreenshotAndEvent(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{receipt: channel.Receipt{
		ExternalID: "wx-9001",
		URL:        "https://mp.weixin.qq.com/s/receipt",
		Raw:        `{"media_id":"wx-9001"}`,
	}}
	p := env.publisher(adapter)
	p.CaptureScreenshot = func(_ context.Context, botID int64, receipt channel.Receipt) (string, error) {
		require.Equal(t, int64(testPublishBotID), botID)
		require.Equal(t, "wx-9001", receipt.ExternalID)
		return "/evidence/wx-9001.png", nil
	}

	res, err := p.Publish(context.Background(), basePublishInput())
	require.NoError(t, err)
	require.Equal(t, "https://mp.weixin.qq.com/s/receipt", res.URL)
	require.Equal(t, "/evidence/wx-9001.png", res.ScreenshotPath)
	require.NotNil(t, res.PublishedAt)

	rec := env.record(t, testPublishBotID, PublishIdempotencyKey(basePublishInput()))
	require.Equal(t, PublishStatusPublished, rec.Status)
	require.Equal(t, "https://mp.weixin.qq.com/s/receipt", rec.Url)
	require.Equal(t, "/evidence/wx-9001.png", rec.ScreenshotPath)
	require.True(t, rec.PublishedAt.Valid)

	var receipt publishRecordDetail
	require.NoError(t, json.Unmarshal([]byte(rec.Receipt), &receipt))
	require.Equal(t, "wx-9001", receipt.ExternalID)
	require.Equal(t, "https://mp.weixin.qq.com/s/receipt", receipt.URL)
	require.Equal(t, `{"media_id":"wx-9001"}`, receipt.Raw)

	var (
		events  int
		summary string
		detail  sql.NullString
	)
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM issue_exec_events WHERE issue_id = ? AND kind = 'terminal'`,
		testPublishIssueID).Scan(&events))
	require.Equal(t, 1, events, "a published record writes one terminal event")
	require.NoError(t, env.db.QueryRow(
		`SELECT summary, detail_json FROM issue_exec_events WHERE issue_id = ? AND kind = 'terminal'`,
		testPublishIssueID).Scan(&summary, &detail))
	require.Contains(t, summary, "wx-9001")
	require.Contains(t, detail.String, "wx-9001")
}

func TestPublishScheduledDefersUntilSchedulerFires(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{}
	p := env.publisher(adapter)

	type armed struct {
		at  time.Time
		key string
		run func(context.Context)
	}
	var armedPublishes []armed
	p.Schedule = func(_ context.Context, at time.Time, key string, run func(context.Context)) error {
		armedPublishes = append(armedPublishes, armed{at: at, key: key, run: run})
		return nil
	}

	at := env.now.Add(2 * time.Hour)
	in := basePublishInput()
	in.ScheduledAt = &at

	res, err := p.Publish(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, PublishModeScheduled, res.Mode)
	require.Equal(t, PublishStatusPending, res.Status)
	require.Zero(t, adapter.calls, "a scheduled publish does not call the adapter now")
	require.Len(t, armedPublishes, 1)
	require.Equal(t, at, armedPublishes[0].at)
	require.Equal(t, PublishIdempotencyKey(in), armedPublishes[0].key)

	rec := env.record(t, testPublishBotID, armedPublishes[0].key)
	require.Equal(t, PublishStatusPending, rec.Status)
	require.False(t, rec.PublishedAt.Valid)
	var detail publishRecordDetail
	require.NoError(t, json.Unmarshal([]byte(rec.Receipt), &detail))
	require.NotNil(t, detail.ScheduledAt)
	require.True(t, detail.ScheduledAt.Equal(at))

	// A second request before the fire time must not arm another dispatch.
	duplicate, err := p.Publish(context.Background(), in)
	require.NoError(t, err)
	require.True(t, duplicate.Duplicate)
	require.Len(t, armedPublishes, 1)
	require.Zero(t, adapter.calls)

	// At the fire time the scheduler runs the armed callback once.
	p.Now = func() time.Time { return at }
	armedPublishes[0].run(context.Background())
	require.Equal(t, 1, adapter.calls)
	require.Equal(t, PublishStatusPublished, env.record(t, testPublishBotID, armedPublishes[0].key).Status)

	// Firing again (e.g. a duplicated timer) must not publish twice.
	armedPublishes[0].run(context.Background())
	require.Equal(t, 1, adapter.calls)
}

func TestPublishScheduledRequiresScheduler(t *testing.T) {
	env := newPublishEnv(t)
	p := env.publisher(&testAdapter{})

	at := env.now.Add(time.Hour)
	in := basePublishInput()
	in.ScheduledAt = &at

	_, err := p.Publish(context.Background(), in)
	require.ErrorIs(t, err, ErrScheduleUnavailable)

	var count int
	require.NoError(t, env.db.QueryRow(`SELECT COUNT(*) FROM ops_publish_records`).Scan(&count))
	require.Zero(t, count, "no record is written when the schedule cannot be armed")
}

func TestPublishBudgetBlockStopsBeforeRecord(t *testing.T) {
	env := newPublishEnv(t)
	adapter := &testAdapter{}
	p := env.publisher(adapter)
	p.CheckBudget = func(context.Context, int64) error { return ErrBudgetExceeded }

	_, err := p.Publish(context.Background(), basePublishInput())
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.Zero(t, adapter.calls)

	var count int
	require.NoError(t, env.db.QueryRow(`SELECT COUNT(*) FROM ops_publish_records`).Scan(&count))
	require.Zero(t, count)
}

func TestPublishRejectsInvalidInput(t *testing.T) {
	env := newPublishEnv(t)
	p := env.publisher(&testAdapter{})

	_, err := p.Publish(context.Background(), PublishInput{IssueID: 2})
	require.ErrorIs(t, err, ErrInvalidPublishInput)

	_, err = p.Publish(context.Background(), PublishInput{BotID: 1})
	require.ErrorIs(t, err, ErrInvalidPublishInput)
}
