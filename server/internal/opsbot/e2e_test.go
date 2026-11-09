package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// This file is the end-to-end acceptance suite for the Ops Bot (design §17).
// Every scenario drives the real services over a real SQLite database and only
// replaces the platform boundary (channel adapter / browser worker / LLM), so a
// regression in wiring, ownership, budget, approval or risk handling fails here.

const (
	e2eOwnerUser = int64(2)
	e2eOwnerAlt  = int64(3)
)

// --- platform fakes ------------------------------------------------------

type e2eAdapter struct {
	mu sync.Mutex

	caps channel.CapabilitySet

	loginErr   error
	publishErr error
	metricsErr error
	replyErr   error
	listErr    error

	metrics     map[string]float64
	engagements []channel.Engagement
	receipt     channel.Receipt

	ensureCalls  int
	publishCalls int
	replyCalls   int
	listCalls    int
	lastPayload  any
	lastReplyTo  string
	lastReplyTxt string
}

func newE2EAdapter() *e2eAdapter {
	return &e2eAdapter{
		caps: channel.CapabilitySet{
			channel.CapPublish:     true,
			channel.CapMetrics:     true,
			channel.CapEngagements: true,
			channel.CapReply:       true,
		},
		metrics: map[string]float64{},
		receipt: channel.Receipt{ExternalID: "e2e-ext-1", URL: "https://platform.example.test/p/1"},
	}
}

func (a *e2eAdapter) Capabilities() channel.CapabilitySet { return a.caps }

func (a *e2eAdapter) EnsureSession(context.Context, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureCalls++
	return a.loginErr
}

func (a *e2eAdapter) Publish(_ context.Context, payload any) (channel.Receipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.publishCalls++
	a.lastPayload = payload
	if a.publishErr != nil {
		return channel.Receipt{}, a.publishErr
	}
	return a.receipt, nil
}

func (a *e2eAdapter) FetchMetrics(context.Context, string, string) (map[string]float64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.metricsErr != nil {
		return nil, a.metricsErr
	}
	out := make(map[string]float64, len(a.metrics))
	for k, v := range a.metrics {
		out[k] = v
	}
	return out, nil
}

func (a *e2eAdapter) ListEngagements(context.Context, string) ([]channel.Engagement, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listCalls++
	if a.listErr != nil {
		return nil, a.listErr
	}
	return append([]channel.Engagement(nil), a.engagements...), nil
}

func (a *e2eAdapter) Reply(_ context.Context, id, text string) (channel.Receipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.replyCalls++
	a.lastReplyTo, a.lastReplyTxt = id, text
	if a.replyErr != nil {
		return channel.Receipt{}, a.replyErr
	}
	return channel.Receipt{ExternalID: "reply-1"}, nil
}

func (a *e2eAdapter) HealthCheck(context.Context) error { return nil }

// e2eBrowserWorker is a fake recipe executor. It records every task and returns
// canned values per recipe name; the real XHS/Browser adapters still validate
// payloads, recipes and capability sets around it.
type e2eBrowserWorker struct {
	mu     sync.Mutex
	tasks  []browser.Task
	values map[string]browser.Result
	errs   map[string]error
}

func (w *e2eBrowserWorker) Run(_ context.Context, task browser.Task) (browser.Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tasks = append(w.tasks, task)
	if err := w.errs[task.Recipe]; err != nil {
		return browser.Result{}, err
	}
	return w.values[task.Recipe], nil
}

func (w *e2eBrowserWorker) recipe(name string) (browser.Task, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, task := range w.tasks {
		if task.Recipe == name {
			return task, true
		}
	}
	return browser.Task{}, false
}

// e2eOpsCaller stands in for the shared integration proxy: the Ops Bot reads
// and writes exclusively through provider-contract operations, so a channel
// test scripts operations instead of a channel-specific client.
type e2eOpsCaller struct {
	mu        sync.Mutex
	files     []string
	publishes []string
	replies   []string
	metrics   map[string]float64
	comments  []map[string]any
}

func (c *e2eOpsCaller) CallOperation(_ context.Context, call channel.OperationCall) (channel.OperationResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch call.Operation {
	case "publish":
		c.publishes = append(c.publishes, fmt.Sprint(call.Params["title"]))
		return channel.OperationResult{Status: 200, Body: map[string]any{
			"publish_id": "wechat-publish-e2e", "article_url": "https://mp.weixin.qq.com/s/e2e",
		}}, nil
	case "metrics":
		body := map[string]any{}
		for key, value := range c.metrics {
			body[key] = value
		}
		body["net_fans_count"] = 10.0
		return channel.OperationResult{Status: 200, Body: body}, nil
	case "comments":
		items := make([]any, 0, len(c.comments))
		for _, comment := range c.comments {
			items = append(items, comment)
		}
		return channel.OperationResult{Status: 200, Body: map[string]any{"comment": items}}, nil
	case "reply":
		c.replies = append(c.replies, fmt.Sprint(call.Params["content"]))
		return channel.OperationResult{Status: 200, Body: map[string]any{"errcode": 0}}, nil
	default:
		return channel.OperationResult{Status: 200, Body: map[string]any{}}, nil
	}
}

func e2eOpenAPIBindings() map[channel.Capability]packs.Binding {
	return map[channel.Capability]packs.Binding{
		channel.CapPublish: {Transport: "openapi", Provider: "wechat-mp", Operation: "publish"},
		channel.CapMetrics: {Transport: "openapi", Provider: "wechat-mp", Operation: "metrics",
			Params: map[string]string{"from": "begin_date", "to": "end_date"}},
		channel.CapEngagements: {Transport: "openapi", Provider: "wechat-mp", Operation: "comments",
			Items: "comment", Fields: map[string]string{"id": "user_comment_id", "author": "openid", "text": "content"}},
		channel.CapReply: {Transport: "openapi", Provider: "wechat-mp", Operation: "reply",
			Params: map[string]string{"id": "user_comment_id", "text": "content"}},
	}
}

func newE2EWeChatAdapter(caller *e2eOpsCaller) channel.ChannelAdapter {
	return channel.NewOpenAPIAdapter(caller, "wechat-mp", e2eOpenAPIBindings(),
		map[string]string{"read_count": "views", "share_count": "shares", "like_count": "likes", "net_fans_count": "follower_delta"})
}

// --- content fakes -------------------------------------------------------

type e2eCopyWriter struct{ title string }

func (c *e2eCopyWriter) Write(_ context.Context, _ content.CopyRequest) (content.CopyResult, error) {
	title := c.title
	if title == "" {
		title = "秋季护肤的三个关键步骤"
	}
	return content.CopyResult{
		Title:    title,
		Digest:   "换季只是换掉不合时宜的习惯",
		Hook:     "换季先别换整套护肤品",
		Body:     "先讲清换季为什么容易出问题。\n\n再给出三条今天就能做的行动建议。",
		CTA:      "你换季最头疼的是什么？",
		Tags:     []string{"秋季护肤", "换季", "护肤"},
		Sources:  []string{"品牌知识库"},
		Provider: "e2e-copy",
		CostUSD:  0.12,
	}, nil
}

type e2eImageGen struct{}

func (e2eImageGen) Generate(_ context.Context, req content.ImageRequest) (content.ImageResult, error) {
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return content.ImageResult{}, err
	}
	out := make([]content.ImageAsset, 0, len(req.Specs))
	for _, spec := range req.Specs {
		path := filepath.Join(req.OutDir, fmt.Sprintf("%s-%d.png", spec.Purpose, spec.Index))
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			return content.ImageResult{}, err
		}
		out = append(out, content.ImageAsset{
			Path: path, Purpose: spec.Purpose, Ratio: spec.Ratio,
			Width: spec.Width, Height: spec.Height,
		})
	}
	return content.ImageResult{Images: out, Provider: "e2e-images", CostUSD: 0.03}, nil
}

type e2eScriptWriter struct{}

func (e2eScriptWriter) WriteScript(context.Context, content.VideoScriptRequest) (content.VideoScriptResult, error) {
	return content.VideoScriptResult{
		Title:     "护肤三步",
		Hook:      "先别换整套护肤品",
		Narration: "第一步清洁，第二步补水，第三步防晒。",
		CTA:       "你换季最头疼的是什么？",
		Provider:  "e2e-script",
		CostUSD:   0.01,
	}, nil
}

type e2eStoryboard struct{}

func (e2eStoryboard) PlanStoryboard(context.Context, content.StoryboardRequest) (content.VideoStoryboard, error) {
	return content.VideoStoryboard{
		Shots: []content.VideoShot{
			{StartSec: 0, EndSec: 3, Visual: "hook", Narration: "先别换整套护肤品"},
			{StartSec: 3, EndSec: 15, Visual: "steps", Narration: "三步护理法"},
		},
		Provider: "e2e-storyboard",
		CostUSD:  0.01,
	}, nil
}

type e2eTTS struct{}

func (e2eTTS) Synthesize(_ context.Context, req content.TTSRequest) (content.TTSResult, error) {
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return content.TTSResult{}, err
	}
	path := filepath.Join(req.OutDir, "voice.mp3")
	if err := os.WriteFile(path, []byte("mp3"), 0o644); err != nil {
		return content.TTSResult{}, err
	}
	return content.TTSResult{AudioPath: path, DurationSec: 15, Provider: "e2e-tts", CostUSD: 0.02}, nil
}

type e2eFrames struct{}

func (e2eFrames) RenderFrames(_ context.Context, req content.FramesRequest) (content.FramesResult, error) {
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return content.FramesResult{}, err
	}
	frames := []content.VideoFrame{}
	cover := filepath.Join(req.OutDir, "cover.png")
	if err := os.WriteFile(cover, []byte("png"), 0o644); err != nil {
		return content.FramesResult{}, err
	}
	frames = append(frames, content.VideoFrame{
		Purpose: content.ImagePurposeCover, Path: cover,
		Ratio: req.Spec.Ratio, Width: req.Spec.Width, Height: req.Spec.Height,
	})
	for _, shot := range req.Shots {
		path := filepath.Join(req.OutDir, fmt.Sprintf("shot-%d.png", shot.Index))
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			return content.FramesResult{}, err
		}
		frames = append(frames, content.VideoFrame{
			Index: shot.Index, Purpose: content.VideoFramePurposeShot, Path: path,
			Ratio: req.Spec.Ratio, Width: req.Spec.Width, Height: req.Spec.Height,
		})
	}
	return content.FramesResult{Frames: frames, Provider: "e2e-frames", CostUSD: 0.03}, nil
}

type e2eSubtitles struct{}

func (e2eSubtitles) WriteSubtitles(_ context.Context, req content.SubtitleRequest) (content.SubtitleResult, error) {
	path := filepath.Join(req.OutDir, "subtitles.srt")
	if err := os.WriteFile(path, []byte("1\n00:00:00,000 --> 00:00:15,000\n三步护理法\n"), 0o644); err != nil {
		return content.SubtitleResult{}, err
	}
	return content.SubtitleResult{Path: path, Format: "srt", Provider: "e2e-subtitles", CostUSD: 0.005}, nil
}

type e2eComposer struct{}

func (e2eComposer) Compose(_ context.Context, req content.ComposeRequest) (content.ComposeResult, error) {
	final := filepath.Join(req.OutDir, "final.mp4")
	if err := os.WriteFile(final, []byte("mp4"), 0o644); err != nil {
		return content.ComposeResult{}, err
	}
	return content.ComposeResult{
		FinalPath:   final,
		CoverPath:   req.CoverPath,
		DurationSec: req.DurationSec,
		Width:       req.Spec.Width,
		Height:      req.Spec.Height,
		Ratio:       req.Spec.Ratio,
		Provider:    "e2e-compose",
		CostUSD:     0.1,
	}, nil
}

// --- director fakes ------------------------------------------------------

type e2eWeeklyPlanner struct {
	nextID int64
	calls  []int64
}

func (p *e2eWeeklyPlanner) PlanWeek(_ context.Context, ref BotRef, _ string) ([]int64, error) {
	p.calls = append(p.calls, ref.ID)
	p.nextID++
	return []int64{p.nextID}, nil
}

type e2eDirectorCalendar struct {
	entries     []CalendarEntry
	rescheduled []int64
}

func (c *e2eDirectorCalendar) List(_ context.Context, f CalendarFilter) ([]CalendarEntry, error) {
	allowed := map[int64]bool{}
	for _, id := range f.BotIDs {
		allowed[id] = true
	}
	var out []CalendarEntry
	for _, entry := range c.entries {
		if !allowed[entry.BotID] {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

func (c *e2eDirectorCalendar) Reschedule(_ context.Context, _ OwnerRef, issueID int64, startDate, dueDate string) error {
	for i := range c.entries {
		if c.entries[i].IssueID == issueID {
			c.entries[i].StartDate = startDate
			c.entries[i].DueDate = dueDate
		}
	}
	c.rescheduled = append(c.rescheduled, issueID)
	return nil
}

type e2eTrendPlanner struct{}

func (e2eTrendPlanner) Build(_ context.Context, in content.BriefInput) (content.OpsContentBrief, error) {
	return content.OpsContentBrief{
		Channel: in.PackID, AccountRef: in.AccountRef, Format: in.Format,
		Topic: in.Topic, IssueID: in.BotID*100 + 1, DueDate: in.DueDate, Week: in.Week,
	}, nil
}

type e2eOccupiedCalendar struct{ occupied map[int64][]string }

func (c *e2eOccupiedCalendar) OccupiedDates(_ context.Context, botID int64, _, _ string) ([]string, error) {
	return c.occupied[botID], nil
}

type e2eLinker struct{}

func (e2eLinker) IssueAccountLink(_ context.Context, _ OwnerRef, _ int64, _ string, accountRef string) (string, error) {
	return "https://authorize.example.test/one-time/" + accountRef, nil
}

type e2eNotice struct {
	Type   string
	BotID  int64
	Event  string
	Detail string
}

// --- harness -------------------------------------------------------------

type e2eHarness struct {
	t   *testing.T
	raw *sql.DB
	db  *store.DB
	q   *store.Queries

	dataDir  string
	registry *packs.Registry
	svc      *Service
	approval *Approval
	budget   *Budget
	risk     *Risk
	pub      *Publisher
	eng      *Engagement
	metrics  *Metrics
	manual   *channel.ManualAdapter
	sessions *browser.SessionManager

	adapters map[int64]channel.ChannelAdapter
	spend    map[int64]float64
	notices  []e2eNotice
	now      time.Time
}

func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()
	store.Driver = "sqlite"

	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	// A shared in-memory DB lives and dies with its connection.
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })

	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO users (id, username, password_hash) VALUES (7, 'operator', 'x')`)
	require.NoError(t, err)
	// Every ops bot belongs to a project (its control Epic lives there); the
	// onboarding flow therefore starts from a project.
	_, err = raw.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'ops-project', 'user', 7)`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO columns (id, project_id, name, position) VALUES (1, 1, 'todo', 0)`)
	require.NoError(t, err)

	db := store.Wrap(raw)
	q := db.Queries()

	dataDir := t.TempDir()
	registry := &packs.Registry{DataDir: dataDir}
	require.NoError(t, packs.InstallBuiltins(registry, "user", e2eOwnerUser))

	h := &e2eHarness{
		t:        t,
		raw:      raw,
		db:       db,
		q:        q,
		dataDir:  dataDir,
		registry: registry,
		adapters: map[int64]channel.ChannelAdapter{},
		spend:    map[int64]float64{},
		now:      time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local),
	}

	h.svc = NewService(q, FilePacks{registry})
	h.approval = NewApproval(db, ApprovalPolicy{})
	h.svc.Approve = h.approval
	h.sessions = browser.NewSessionManager(q, dataDir)
	h.manual = channel.NewManualAdapter(filepath.Join(dataDir, "ops", "manual"))

	h.budget = &Budget{
		Sum: func(_ context.Context, epicIssueID int64, _, _ time.Time) (float64, error) {
			return h.spend[epicIssueID], nil
		},
		Limit: func(ctx context.Context, botID int64) (float64, string, error) {
			bot, err := h.svc.Get(ctx, botID)
			if err != nil {
				return 0, "", err
			}
			cfg := e2eBudgetConfig(bot.BudgetJson)
			limit, _ := cfg["monthly_limit"].(float64)
			currency, _ := cfg["currency"].(string)
			return limit, currency, nil
		},
		SaveLimit: func(ctx context.Context, botID int64, limit float64, currency string) error {
			return h.updateBudget(ctx, botID, func(cfg map[string]any) {
				cfg["monthly_limit"] = limit
				cfg["currency"] = currency
			})
		},
		Pause: func(ctx context.Context, botID int64) error {
			if err := h.updateBudget(ctx, botID, func(cfg map[string]any) {
				cfg["paused_reason"] = "budget"
			}); err != nil {
				return err
			}
			_, err := h.svc.SetStatus(ctx, botID, StatusPaused)
			return err
		},
		Resume: func(ctx context.Context, botID int64) error {
			bot, err := h.svc.Get(ctx, botID)
			if err != nil {
				return err
			}
			if e2eBudgetConfig(bot.BudgetJson)["paused_reason"] != "budget" {
				return nil
			}
			if err := h.updateBudget(ctx, botID, func(cfg map[string]any) {
				delete(cfg, "paused_reason")
			}); err != nil {
				return err
			}
			if bot.Status == StatusPaused {
				_, err = h.svc.SetStatus(ctx, botID, StatusActive)
			}
			return err
		},
		Notify: func(_ context.Context, botID int64, reason string) error {
			h.notices = append(h.notices, e2eNotice{Type: "budget", BotID: botID, Detail: reason})
			return nil
		},
		Now: func() time.Time { return h.now },
	}

	h.risk = &Risk{
		Bots:        h.svc,
		Approvals:   h.approval,
		Engagements: q,
		Sessions:    h.sessions,
		Backend:     browser.BackendLocal,
	}
	h.risk.Notify = func(_ context.Context, botID int64, reason string) error {
		h.notices = append(h.notices, e2eNotice{Type: "risk", BotID: botID, Detail: reason})
		return nil
	}
	h.risk.VerifyLogin = func(context.Context, int64) error { return nil }

	h.pub = &Publisher{
		Q:          q,
		DB:         db,
		AdapterFor: h.adapterFor,
		Manual:     h.manual,
		Events:     q,
		CheckBudget: func(ctx context.Context, botID int64) error {
			bot, err := q.GetOpsBot(ctx, botID)
			if err != nil {
				return err
			}
			if !bot.EpicIssueID.Valid || bot.EpicIssueID.Int64 == 0 {
				return nil
			}
			return h.budget.Check(ctx, botID, bot.EpicIssueID.Int64, 0)
		},
		BotStatus: func(ctx context.Context, botID int64) (string, error) {
			bot, err := q.GetOpsBot(ctx, botID)
			if err != nil {
				return "", err
			}
			return bot.Status, nil
		},
		OnAdapterFailure: func(ctx context.Context, botID int64, err error) {
			if signal, ok := e2eRiskSignal(err); ok {
				_, _ = h.risk.Evaluate(ctx, botID, signal)
			}
		},
		Notify: func(_ context.Context, botID int64, event string, detail map[string]any) error {
			h.notices = append(h.notices, e2eNotice{
				Type: "publish", BotID: botID, Event: event, Detail: e2eMustJSON(detail),
			})
			return nil
		},
		Now:         func() time.Time { return h.now },
		Sleep:       func(context.Context, time.Duration) error { return nil },
		MaxAttempts: 2,
		Backoff:     time.Millisecond,
	}

	h.eng = &Engagement{
		Q:           q,
		DB:          db,
		AdapterFor:  h.adapterFor,
		Approvals:   h.approval,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		MaxAttempts: 2,
		Backoff:     time.Millisecond,
	}

	h.metrics = &Metrics{
		Fetch: func(ctx context.Context, botID int64, from, to string) ([]MetricsSample, error) {
			adapter, err := h.adapterFor(ctx, botID)
			if err != nil {
				return nil, err
			}
			if !adapter.Capabilities().Supports(channel.CapMetrics) {
				return nil, channel.ErrCapabilityUnavailable
			}
			values, err := adapter.FetchMetrics(ctx, from, to)
			if err != nil {
				if signal, ok := e2eRiskSignal(err); ok {
					_, _ = h.risk.Evaluate(ctx, botID, signal)
				}
				return nil, err
			}
			return []MetricsSample{{Date: to, Fields: values, Source: "platform"}}, nil
		},
		Store: q,
		Bots:  q,
	}
	return h
}

func (h *e2eHarness) adapterFor(_ context.Context, botID int64) (channel.ChannelAdapter, error) {
	if adapter, ok := h.adapters[botID]; ok {
		return adapter, nil
	}
	return nil, &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       "no_adapter",
		Err:        channel.ErrCapabilityUnavailable,
	}
}

func (h *e2eHarness) updateBudget(ctx context.Context, botID int64, mutate func(map[string]any)) error {
	bot, err := h.svc.Get(ctx, botID)
	if err != nil {
		return err
	}
	cfg := e2eBudgetConfig(bot.BudgetJson)
	mutate(cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	body := string(raw)
	_, err = h.svc.UpdateConfig(ctx, botID, ConfigPatch{BudgetJSON: &body})
	return err
}

func e2eBudgetConfig(raw string) map[string]any {
	cfg := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

func e2eMustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func e2eRiskSignal(err error) (string, bool) {
	var adapterErr *channel.AdapterError
	if !errors.As(err, &adapterErr) {
		return "", false
	}
	return RiskSignalForCode(adapterErr.Code)
}

// createAccount registers a bot and gives it the control workspace + Epic that
// provisioning would have produced.
func (h *e2eHarness) createAccount(owner OwnerRef, packID, accountRef, display, budgetJSON string) store.OpsBot {
	h.t.Helper()
	bot, err := h.svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner:              owner,
		PackID:             packID,
		AccountRef:         accountRef,
		DisplayName:        display,
		GoalsJSON:          `{"type":"growth","text":"涨粉"}`,
		CadenceJSON:        `{"posts_per_week":3,"hot_spot_boost":true}`,
		ApprovalPolicyJSON: `{"publish":true,"reply":true,"video":true}`,
		BudgetJSON:         budgetJSON,
	})
	require.NoError(h.t, err)
	return h.attach(bot)
}

func (h *e2eHarness) attach(bot store.OpsBot) store.OpsBot {
	h.t.Helper()
	wsID := bot.ID + 100
	_, err := h.raw.Exec(
		`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (?, ?, ?, ?, ?)`,
		wsID, "ops-control", fmt.Sprintf("/tmp/ops-%d", wsID), bot.OwnerType, bot.OwnerID,
	)
	require.NoError(h.t, err)
	epicID := bot.ID + 9000
	_, err = h.raw.Exec(
		`UPDATE ops_bots SET control_workspace_id = ?, epic_issue_id = ? WHERE id = ?`,
		wsID, epicID, bot.ID,
	)
	require.NoError(h.t, err)
	h.spend[epicID] = 0
	reloaded, err := h.svc.Get(context.Background(), bot.ID)
	require.NoError(h.t, err)
	return reloaded
}

func (h *e2eHarness) workspaceOf(botID int64) int64 {
	h.t.Helper()
	bot, err := h.svc.Get(context.Background(), botID)
	require.NoError(h.t, err)
	require.True(h.t, bot.ControlWorkspaceID.Valid)
	return bot.ControlWorkspaceID.Int64
}

func (h *e2eHarness) articlePipeline(t *testing.T) *content.Pipeline {
	return &content.Pipeline{
		Copy:   &e2eCopyWriter{},
		Images: e2eImageGen{},
		Store:  &content.FileArtifactStore{Root: t.TempDir()},
	}
}

func (h *e2eHarness) videoPipeline(t *testing.T) *content.VideoPipeline {
	return &content.VideoPipeline{
		Script:     e2eScriptWriter{},
		Storyboard: e2eStoryboard{},
		TTS:        e2eTTS{},
		Frames:     e2eFrames{},
		Subtitles:  e2eSubtitles{},
		Compose:    e2eComposer{},
		Store:      &content.FileArtifactStore{Root: t.TempDir()},
	}
}

func (h *e2eHarness) reviewFor(owner OwnerRef) *content.Review {
	return &content.Review{
		Pack: func(ctx context.Context, packID string, version int) (packs.Manifest, error) {
			return h.registry.Load(owner.Type, owner.ID, packID, version)
		},
	}
}

func (h *e2eHarness) createApproval(t *testing.T, bot store.OpsBot, typ ApprovalType, issueID, version int64, preview any) ApprovalRequest {
	t.Helper()
	req, err := h.approval.Create(context.Background(), ApprovalInput{
		BotID:          bot.ID,
		WorkspaceID:    h.workspaceOf(bot.ID),
		IssueID:        issueID,
		Type:           typ,
		ContentVersion: version,
		PreviewJSON:    e2eMustJSON(preview),
	})
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusPending, req.Status)
	return req
}

func (h *e2eHarness) decide(t *testing.T, req ApprovalRequest, action string) ApprovalRequest {
	t.Helper()
	got, err := h.approval.Decide(context.Background(), req.ID, Decision{
		OperatorID: 7, Action: action, Reason: "e2e decision",
	})
	require.NoError(t, err)
	return got
}

func e2eArticleArtifact(art content.Artifact, wsID int64) content.ReviewArtifact {
	return content.ReviewArtifact{
		IssueID:     art.IssueID,
		WorkspaceID: wsID,
		Version:     int64(art.Version),
		Format:      art.Format,
		Title:       "秋季护肤的三个关键步骤",
		Body:        "先讲清换季为什么容易出问题。\n\n再给出三条今天就能做的行动建议。",
		Tags:        []string{"秋季护肤", "换季", "护肤"},
		Cover:       firstPath(art.Images, content.ImagePurposeCover),
		Images:      allPaths(art.Images),
		CTA:         "你换季最头疼的是什么？",
		Sources:     []string{"品牌知识库"},
	}
}

func firstPath(images []content.ImageAsset, purpose string) string {
	for _, img := range images {
		if img.Purpose == purpose {
			return img.Path
		}
	}
	return ""
}

func allPaths(images []content.ImageAsset) []string {
	out := make([]string, 0, len(images))
	for _, img := range images {
		out = append(out, img.Path)
	}
	return out
}

func e2eVideoArtifact(art content.VideoArtifact, wsID int64) content.ReviewArtifact {
	return content.ReviewArtifact{
		IssueID:      art.IssueID,
		WorkspaceID:  wsID,
		Version:      int64(art.Version),
		Format:       art.Format,
		Title:        art.Title,
		Body:         "洁面、补水、防晒，三步完成日常护理。",
		Cover:        art.CoverPath,
		VideoRef:     art.FinalPath,
		DurationSec:  art.DurationSec,
		HasSubtitles: art.Subtitles != "",
	}
}

// --- scenario 1 ----------------------------------------------------------

func TestE2E01ConversationalWeChatPublishLoop(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}

	onboarding := NewOnboarding(h.svc)
	onboarding.InstallPacks = func(ctx context.Context, ownerType string, ownerID int64) error {
		return packs.InstallBuiltins(h.registry, ownerType, ownerID)
	}
	onboarding.Credentials = e2eLinker{}

	step := func(text string) (string, bool) {
		reply, done, err := onboarding.Handle(ctx, 1001, owner, 1, text)
		require.NoError(t, err)
		return reply, done
	}

	reply, _ := step("接入公众号")
	require.Contains(t, reply, "账号名称")
	reply, _ = step("品牌公众号")
	require.Contains(t, reply, "目标")
	reply, _ = step("涨粉与品牌")
	require.Contains(t, reply, "每周")
	reply, _ = step("每周3篇，要热点加更")
	require.Contains(t, reply, "预算")
	reply, _ = step("每月 500 元")
	require.Contains(t, reply, "确认")
	reply, _ = step("需要")
	require.Contains(t, reply, "一次性链接")
	reply, done := step("完成")
	require.True(t, done)
	require.Contains(t, reply, "品牌公众号")

	bots, err := h.svc.ListByOwner(ctx, owner)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	bot := h.attach(bots[0])
	require.Equal(t, "wechat_mp", bot.PackID)
	require.EqualValues(t, 1, bot.PackVersion)
	require.Contains(t, bot.BudgetJson, `"monthly_limit":500`)

	wechatClient := &e2eOpsCaller{metrics: map[string]float64{"read_count": 120}}
	h.adapters[bot.ID] = newE2EWeChatAdapter(wechatClient)

	brief := content.OpsContentBrief{
		Channel: bot.PackID, AccountRef: bot.AccountRef,
		Goal: "涨粉", Audience: "换季人群", Format: content.FormatArticle,
		Topic: "秋季护肤", Acceptance: []string{"有行动建议"},
	}
	art, err := h.articlePipeline(t).Run(ctx, content.BotRef{ID: bot.ID, PackID: bot.PackID}, brief, 501)
	require.NoError(t, err)
	require.NotEmpty(t, art.Images)
	require.NotEmpty(t, art.LayoutPath)

	var approvalReq ApprovalRequest
	review := h.reviewFor(owner)
	res, err := review.Gate(ctx, content.ReviewBotRef{
		ID: bot.ID, PackID: bot.PackID, PackVersion: int(bot.PackVersion),
	}, e2eArticleArtifact(art, h.workspaceOf(bot.ID)), brief, func(preview content.ReviewPreview) error {
		approvalReq = h.createApproval(t, bot, ApprovalPublish, 501, int64(art.Version), preview)
		return nil
	})
	require.NoError(t, err)
	require.False(t, res.Blocking)
	require.NotZero(t, approvalReq.ID)

	h.decide(t, approvalReq, ActionApproved)

	published, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 501, ContentVersion: int64(art.Version),
		Platform: bot.PackID, PackID: bot.PackID, AccountRef: bot.AccountRef,
		Payload: channel.OpenAPIPublishPayload{Fields: map[string]any{
			"title": "秋季护肤的三个关键步骤", "content": "先讲清换季为什么容易出问题。",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, published.Status)
	require.Len(t, wechatClient.publishes, 1)

	require.NoError(t, h.metrics.Sync(ctx, bot.ID, "2026-11-03", "2026-11-04"))
	summary, err := h.metrics.Summary(ctx, bot.ID, "2026-11-03", "2026-11-04")
	require.NoError(t, err)
	require.Equal(t, 120.0, summary.Totals["views"])
	require.Equal(t, 2, len(summary.Daily))
}

// --- scenario 2 ----------------------------------------------------------

func TestE2E02SecondAccountSharesPackWithIsolatedState(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}

	a := h.createAccount(owner, "wechat_mp", "acc-a", "账号 A", `{"currency":"USD","monthly_limit":50}`)
	b := h.createAccount(owner, "wechat_mp", "acc-b", "账号 B", `{"currency":"USD","monthly_limit":500}`)
	require.Equal(t, a.PackID, b.PackID)
	require.NotEqual(t, a.ID, b.ID)

	h.spend[a.EpicIssueID.Int64] = 60
	require.ErrorIs(t, h.budget.Check(ctx, a.ID, a.EpicIssueID.Int64, 0), ErrBudgetExceeded)
	paused, err := h.svc.Get(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPaused, paused.Status)
	require.NoError(t, h.budget.Check(ctx, b.ID, b.EpicIssueID.Int64, 0))
	active, err := h.svc.Get(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, active.Status)

	reqA := h.createApproval(t, a, ApprovalPublish, 601, 1, map[string]any{"title": "A"})
	reqB := h.createApproval(t, b, ApprovalPublish, 602, 1, map[string]any{"title": "B"})
	_, err = h.risk.Kill(ctx, a.ID, "账号 A 隔离验证")
	require.NoError(t, err)

	closed, err := h.approval.Get(ctx, reqA.ID)
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusCancelled, closed.Status)
	still, err := h.approval.Get(ctx, reqB.ID)
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusPending, still.Status)

	adapterA := newE2EAdapter()
	adapterA.metrics = map[string]float64{"views": 100}
	adapterB := newE2EAdapter()
	adapterB.metrics = map[string]float64{"views": 900}
	h.adapters[a.ID] = adapterA
	h.adapters[b.ID] = adapterB
	require.NoError(t, h.metrics.Sync(ctx, a.ID, "2026-11-04", "2026-11-04"))
	require.NoError(t, h.metrics.Sync(ctx, b.ID, "2026-11-04", "2026-11-04"))
	summaryA, err := h.metrics.Summary(ctx, a.ID, "2026-11-04", "2026-11-04")
	require.NoError(t, err)
	summaryB, err := h.metrics.Summary(ctx, b.ID, "2026-11-04", "2026-11-04")
	require.NoError(t, err)
	require.Equal(t, 100.0, summaryA.Totals["views"])
	require.Equal(t, 900.0, summaryB.Totals["views"])
}

// --- scenario 3 ----------------------------------------------------------

func TestE2E03XiaohongshuBrowserLoop(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}
	bot := h.createAccount(owner, "xiaohongshu", "xhs-brand", "小红书品牌号", `{"currency":"CNY","monthly_limit":800}`)

	manifest, err := h.registry.Load(owner.Type, owner.ID, "xiaohongshu", 1)
	require.NoError(t, err)
	root, err := h.registry.Root(owner.Type, owner.ID, "xiaohongshu", 1)
	require.NoError(t, err)
	recipes, err := loadPackRecipes(root, manifest)
	require.NoError(t, err)

	worker := &e2eBrowserWorker{
		values: map[string]browser.Result{
			"login": {},
			"publish": {Values: map[string]string{
				"url": "https://www.xiaohongshu.com/explore/e2e001",
			}},
			"metrics": {Values: map[string]string{
				"view_num": "1200", "like_num": "80", "collect_num": "20",
				"comment_num": "5", "share_num": "3", "fans_add": "10",
			}},
			"engagements": {Values: map[string]string{
				"engagements": `[{"id":"c-1","kind":"comment","author":"u1","text":"这个颜色真好看"}]`,
			}},
			"reply": {Values: map[string]string{"receipt": "reply-1"}},
		},
		errs: map[string]error{},
	}
	limits, metricMap := packLimits(manifest)
	xhs := channel.NewBrowserAdapter(worker, recipes, manifest.Capabilities, limits, metricMap)
	xhs.BindAccount(bot.ID)
	h.adapters[bot.ID] = xhs

	// 扫码登录：EnsureSession runs the pack's login recipe.
	published, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 701, Platform: "xiaohongshu", PackID: "xiaohongshu",
		AccountRef: fmt.Sprintf("%d", bot.ID),
		Payload: channel.BrowserPublishPayload{
			Kind: channel.KindNote, Title: "秋日护肤", Body: "三步护理法，简单好坚持。",
			Images: []string{"cover.png"}, ImageRatio: "3:4",
		},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, published.Status)
	require.Equal(t, "https://www.xiaohongshu.com/explore/e2e001", published.URL)
	_, ok := worker.recipe("login")
	require.True(t, ok)

	require.NoError(t, h.metrics.Sync(ctx, bot.ID, "2026-11-03", "2026-11-04"))
	summary, err := h.metrics.Summary(ctx, bot.ID, "2026-11-03", "2026-11-04")
	require.NoError(t, err)
	require.Equal(t, 1200.0, summary.Totals["views"])
	require.Equal(t, 80.0, summary.Totals["likes"])

	items, err := h.eng.Pull(ctx, BotRef{ID: bot.ID}, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, h.eng.Classify(ctx, BotRef{ID: bot.ID}, items[0].ID))
	require.NoError(t, h.eng.Suggest(ctx, BotRef{ID: bot.ID}, items[0].ID))

	// Fail-closed: a suggestion cannot be sent without an approval card.
	err = h.eng.Send(ctx, BotRef{ID: bot.ID}, items[0].ID, 0, Decision{Action: ActionApproved})
	require.ErrorIs(t, err, ErrApprovalRequired)
	_, replied := worker.recipe("reply")
	require.False(t, replied, "no reply may reach the platform before approval")

	req, err := h.eng.RequestApproval(ctx, BotRef{ID: bot.ID}, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, ApprovalStatusPending, req.Status)
	h.decide(t, req, ActionApproved)
	require.NoError(t, h.eng.Send(ctx, BotRef{ID: bot.ID}, items[0].ID, req.ID, Decision{Action: ActionApproved}))

	sent, err := h.eng.Get(ctx, BotRef{ID: bot.ID}, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, EngagementSent, sent.Status)
	require.Equal(t, "c-1", sent.PlatformEngagementID)
}

// --- scenario 4 ----------------------------------------------------------

func TestE2E04VideoArtifactApprovalAndPublish(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}
	bot := h.createAccount(owner, "wechat_mp", "video-brand", "视频号", `{"currency":"USD","monthly_limit":300}`)
	wechatClient := &e2eOpsCaller{metrics: map[string]float64{"read_count": 50}}
	h.adapters[bot.ID] = newE2EWeChatAdapter(wechatClient)

	brief := content.OpsContentBrief{
		Channel: bot.PackID, AccountRef: bot.AccountRef, Format: content.FormatVideo,
		Topic: "换季护肤", Goal: "涨粉",
	}
	art, err := h.videoPipeline(t).Run(ctx, content.BotRef{ID: bot.ID, PackID: bot.PackID}, brief, 801)
	require.NoError(t, err)
	require.Equal(t, content.FormatVideo, art.Format)
	require.NotEmpty(t, art.FinalPath)
	require.NotEmpty(t, art.CoverPath)

	var req ApprovalRequest
	res, err := h.reviewFor(owner).Gate(ctx, content.ReviewBotRef{
		ID: bot.ID, PackID: bot.PackID, PackVersion: int(bot.PackVersion),
	}, e2eVideoArtifact(art, h.workspaceOf(bot.ID)), brief, func(preview content.ReviewPreview) error {
		req = h.createApproval(t, bot, ApprovalVideo, 801, int64(art.Version), preview)
		return nil
	})
	require.NoError(t, err)
	require.False(t, res.Blocking)
	require.NotZero(t, req.ID)
	h.decide(t, req, ActionApproved)

	published, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 801, ContentVersion: int64(art.Version),
		Platform: bot.PackID, PackID: bot.PackID, AccountRef: bot.AccountRef,
		Payload: channel.OpenAPIPublishPayload{Fields: map[string]any{
			"kind": "video", "title": art.Title, "content": art.Title,
			"video_path": art.FinalPath, "cover_path": art.CoverPath,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, published.Status)
	require.Len(t, wechatClient.publishes, 1)
}

// --- scenario 5 ----------------------------------------------------------

func TestE2E05RejectRegenerateAndReapprove(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}
	bot := h.createAccount(owner, "wechat_mp", "rework-brand", "返工号", `{"currency":"USD","monthly_limit":300}`)

	brief := content.OpsContentBrief{
		Channel: bot.PackID, AccountRef: bot.AccountRef, Format: content.FormatVideo,
		Topic: "返工验证", Goal: "涨粉",
	}
	pipeline := h.videoPipeline(t)

	v1, err := pipeline.Run(ctx, content.BotRef{ID: bot.ID, PackID: bot.PackID}, brief, 802)
	require.NoError(t, err)
	first := h.createApproval(t, bot, ApprovalVideo, 802, int64(v1.Version), map[string]any{"title": v1.Title})
	require.Equal(t, ApprovalStatusAnswered, h.decide(t, first, ActionRejected).Status)

	v2, err := pipeline.Run(ctx, content.BotRef{ID: bot.ID, PackID: bot.PackID}, brief, 802)
	require.NoError(t, err)
	require.Greater(t, v2.Version, v1.Version)
	second := h.createApproval(t, bot, ApprovalVideo, 802, int64(v2.Version), map[string]any{"title": v2.Title})
	require.Equal(t, ApprovalStatusAnswered, h.decide(t, second, ActionRegenerate).Status)

	v3, err := pipeline.Run(ctx, content.BotRef{ID: bot.ID, PackID: bot.PackID}, brief, 802)
	require.NoError(t, err)
	require.Greater(t, v3.Version, v2.Version)
	third := h.createApproval(t, bot, ApprovalVideo, 802, int64(v3.Version), map[string]any{"title": v3.Title})
	final := h.decide(t, third, ActionApproved)
	require.Equal(t, ApprovalStatusAnswered, final.Status)
	require.Contains(t, final.MetadataJSON, `"action":"approved"`)

	pending, err := h.approval.ListPending(ctx, owner.Type, owner.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// --- scenario 6 ----------------------------------------------------------

func TestE2E06BudgetBreachPauseNotifyAndRecover(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}
	bot := h.createAccount(owner, "wechat_mp", "budget-brand", "预算号", `{"currency":"USD","monthly_limit":100}`)
	h.adapters[bot.ID] = newE2EAdapter()

	h.spend[bot.EpicIssueID.Int64] = 120
	err := h.budget.Check(ctx, bot.ID, bot.EpicIssueID.Int64, 0)
	require.ErrorIs(t, err, ErrBudgetExceeded)

	paused, err := h.svc.Get(ctx, bot.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPaused, paused.Status)
	require.Contains(t, paused.BudgetJson, `"paused_reason":"budget"`)
	require.True(t, e2eHasNotice(h.notices, "budget", bot.ID))

	blocked, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 901, Platform: bot.PackID, PackID: bot.PackID,
		Payload: map[string]string{"title": "blocked"},
	})
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.Zero(t, blocked.RecordID, "a budget-blocked publish writes no record")

	require.NoError(t, h.budget.SetLimit(ctx, bot.ID, 200, "USD"))
	recovered, err := h.svc.Get(ctx, bot.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, recovered.Status)
	require.NotContains(t, recovered.BudgetJson, "paused_reason")

	usage, err := h.budget.Usage(ctx, bot.ID, bot.EpicIssueID.Int64)
	require.NoError(t, err)
	require.Equal(t, 200.0, usage.Limit)
	require.Equal(t, 80.0, usage.Remaining)

	published, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 901, Platform: bot.PackID, PackID: bot.PackID,
		Payload: map[string]string{"title": "after raise"},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, published.Status)
}

func e2eHasNotice(notices []e2eNotice, typ string, botID int64) bool {
	for _, notice := range notices {
		if notice.Type == typ && notice.BotID == botID {
			return true
		}
	}
	return false
}

// --- scenario 7 ----------------------------------------------------------

func TestE2E07LoginExpiredAndRedesignDegrade(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}

	expired := h.createAccount(owner, "xiaohongshu", "expired-brand", "掉线号", `{"currency":"USD","monthly_limit":300}`)
	adapter := newE2EAdapter()
	adapter.publishErr = &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       channel.CodeLoginExpired,
		Err:        errors.New("creator session expired"),
	}
	h.adapters[expired.ID] = adapter

	result, err := h.pub.Publish(ctx, PublishInput{
		BotID: expired.ID, IssueID: 1001, Platform: "xiaohongshu", PackID: "xiaohongshu",
		AccountRef: expired.AccountRef, Title: "掉线降级", Body: "人工发布包内容",
		Payload: map[string]string{"title": "掉线降级"},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusManualPending, result.Status)
	require.NotEmpty(t, result.PackageDir)
	_, statErr := os.Stat(filepath.Join(result.PackageDir, "payload.json"))
	require.NoError(t, statErr)

	paused, err := h.svc.Get(ctx, expired.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPaused, paused.Status)
	require.True(t, e2eHasNotice(h.notices, "risk", expired.ID))

	h.risk.VerifyLogin = func(context.Context, int64) error { return errors.New("session still expired") }
	_, err = h.risk.Resume(ctx, expired.ID)
	require.ErrorIs(t, err, ErrReloginRequired)

	redesigned := h.createAccount(owner, "xiaohongshu", "redesign-brand", "改版号", `{"currency":"USD","monthly_limit":300}`)
	broken := newE2EAdapter()
	broken.publishErr = &channel.AdapterError{
		Capability: channel.CapPublish,
		Code:       channel.CodeRecipeBroken,
		Err:        errors.New("selector .publish-button not found"),
	}
	h.adapters[redesigned.ID] = broken
	verdict, err := h.risk.HandleAdapterError(ctx, redesigned.ID, broken.publishErr)
	require.NoError(t, err)
	require.Equal(t, RiskActionDegrade, verdict.Action)
	require.True(t, verdict.Notified)
	stillActive, err := h.svc.Get(ctx, redesigned.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, stillActive.Status)

	degraded, err := h.pub.Publish(ctx, PublishInput{
		BotID: redesigned.ID, IssueID: 1002, Platform: "xiaohongshu", PackID: "xiaohongshu",
		AccountRef: redesigned.AccountRef, Title: "改版降级", Body: "人工发布包内容",
		Payload: map[string]string{"title": "改版降级"},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusManualPending, degraded.Status)
	require.NotEmpty(t, degraded.PackageDir)
}

// --- scenario 8 ----------------------------------------------------------

func TestE2E08DirectorPlanHotspotAndCrossAccountDedupe(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}

	director, err := h.svc.CreateDirector(ctx, CreateDirectorInput{
		Owner: owner, DisplayName: "品牌总监", GoalsJSON: `{"type":"brand","text":"品牌增长"}`,
	})
	require.NoError(t, err)
	director = h.attach(director)

	a := h.createAccount(owner, "wechat_mp", "dir-acc-a", "总监账号 A", `{"currency":"USD","monthly_limit":300}`)
	b := h.createAccount(owner, "xiaohongshu", "dir-acc-b", "总监账号 B", `{"currency":"USD","monthly_limit":300}`)

	planner := &e2eWeeklyPlanner{}
	calendar := &e2eDirectorCalendar{entries: []CalendarEntry{
		{BotID: a.ID, IssueID: 2001, StartDate: "2026-11-02", DueDate: "2026-11-02", Title: "A-1"},
		{BotID: a.ID, IssueID: 2002, StartDate: "2026-11-02", DueDate: "2026-11-02", Title: "A-2"},
		{BotID: b.ID, IssueID: 2003, StartDate: "2026-11-03", DueDate: "2026-11-03", Title: "B-1"},
	}}
	d := &Director{
		Owner: owner, BotID: director.ID, OperatorID: 7,
		Bots:     &SQLDirectorAccounts{Q: h.q},
		Planner:  planner,
		Calendar: calendar,
		Budget:   h.budget,
	}
	plan, err := d.PlanAll(ctx, "2026-W45")
	require.NoError(t, err)
	require.Len(t, plan.Accounts, 2)
	require.Len(t, planner.calls, 2)
	require.NotEmpty(t, plan.Rescheduled, "same-account same-day collision is moved")
	require.Empty(t, plan.Conflicts)

	kept, dropped := d.DedupeTopics([]Topic{
		{BotID: a.ID, Title: "换季护肤三步法", GoalMatch: 0.6},
		{BotID: b.ID, Title: "换季护肤三步法（完整版）", GoalMatch: 0.9},
	}, 0.5)
	require.Len(t, kept, 1)
	require.Equal(t, b.ID, kept[0].BotID)
	require.Len(t, dropped, 1)
	require.Equal(t, kept[0].Title, dropped[0].DuplicateOf)

	trend := &Trend{
		Bots:     h.q,
		Calendar: &e2eOccupiedCalendar{occupied: map[int64][]string{}},
		Planner:  e2eTrendPlanner{},
		Budget:   h.budget,
		Now:      func() time.Time { return h.now },
	}
	decisions, err := trend.Evaluate(ctx, BotRef{ID: director.ID, Role: RoleDirector}, []Signal{{
		Title: "换季护肤新趋势", Heat: 95, Relevance: 0.95,
		Fit: map[int64]float64{a.ID: 0.9, b.ID: 0.2},
	}})
	require.NoError(t, err)
	require.NotEmpty(t, decisions)
	require.Equal(t, TrendAddNow, decisions[0].Action)
	require.Equal(t, a.ID, decisions[0].BotID)
	require.NotZero(t, decisions[0].IssueID)
}

// --- scenario 9 ----------------------------------------------------------

func TestE2E09FacebookPackPublishMetricsAndComments(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}
	bot := h.createAccount(owner, "facebook", "page-1", "FB Page", `{"currency":"USD","monthly_limit":300}`)

	caller := &e2eFacebookCaller{}
	fb := channel.NewOpenAPIAdapter(caller, "facebook-graph", e2eFacebookBindings(), map[string]string{
		"page_fans": "followers", "page_impressions": "views", "page_post_engagements": "likes",
	})
	fb.Account = bot.AccountRef
	h.adapters[bot.ID] = fb

	published, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 1101, Platform: "facebook", PackID: "facebook",
		AccountRef: "page-1",
		Payload:    channel.OpenAPIPublishPayload{Fields: map[string]any{"message": "秋季上新"}},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, published.Status)
	require.Equal(t, 1, caller.publishes)
	require.Equal(t, "page-1", caller.account)

	require.NoError(t, h.metrics.Sync(ctx, bot.ID, "2026-11-03", "2026-11-04"))
	summary, err := h.metrics.Summary(ctx, bot.ID, "2026-11-03", "2026-11-04")
	require.NoError(t, err)
	require.Equal(t, 3200.0, summary.Totals["views"])
	require.Equal(t, 5000.0/2, summary.Averages["followers"])

	items, err := h.eng.Pull(ctx, BotRef{ID: bot.ID}, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, h.eng.Classify(ctx, BotRef{ID: bot.ID}, items[0].ID))
	require.NoError(t, h.eng.Suggest(ctx, BotRef{ID: bot.ID}, items[0].ID))
	req, err := h.eng.RequestApproval(ctx, BotRef{ID: bot.ID}, items[0].ID)
	require.NoError(t, err)
	h.decide(t, req, ActionApproved)
	require.NoError(t, h.eng.Send(ctx, BotRef{ID: bot.ID}, items[0].ID, req.ID, Decision{Action: ActionApproved}))
	require.Equal(t, "c-9", caller.replyTo)
}

// e2eFacebookCaller scripts the Graph operations the Facebook pack declares.
type e2eFacebookCaller struct {
	publishes int
	account   string
	replyTo   string
}

func (c *e2eFacebookCaller) CallOperation(_ context.Context, call channel.OperationCall) (channel.OperationResult, error) {
	if account, ok := call.Params["account"].(string); ok && account != "" {
		c.account = account
	}
	switch call.Operation {
	case "publish_feed":
		c.publishes++
		return channel.OperationResult{Status: 200, Body: map[string]any{"id": "page-1_post_9"}}, nil
	case "page_metrics":
		return channel.OperationResult{Status: 200, Body: map[string]any{"data": []any{
			map[string]any{"name": "page_impressions", "values": []any{map[string]any{"value": 3200.0}}},
			map[string]any{"name": "page_fans", "values": []any{map[string]any{"value": 5000.0}}},
			map[string]any{"name": "page_post_engagements", "values": []any{map[string]any{"value": 180.0}}},
		}}}, nil
	case "page_comments":
		return channel.OperationResult{Status: 200, Body: map[string]any{"data": []any{
			map[string]any{"id": "c-9", "message": "非常喜欢这个系列"},
		}}}, nil
	default:
		c.replyTo = fmt.Sprint(call.Params["comment_id"])
		return channel.OperationResult{Status: 200, Body: map[string]any{"id": "reply-9"}}, nil
	}
}

func e2eFacebookBindings() map[channel.Capability]packs.Binding {
	return map[channel.Capability]packs.Binding{
		channel.CapPublish: {Transport: "openapi", Provider: "facebook-graph", Operation: "publish_feed"},
		channel.CapMetrics: {Transport: "openapi", Provider: "facebook-graph", Operation: "page_metrics",
			Params: map[string]string{"from": "since", "to": "until"}, Items: "data", ValueKey: "values"},
		channel.CapEngagements: {Transport: "openapi", Provider: "facebook-graph", Operation: "page_comments",
			Items: "data", Fields: map[string]string{"id": "id", "text": "message"}},
		channel.CapReply: {Transport: "openapi", Provider: "facebook-graph", Operation: "comment_reply",
			Params: map[string]string{"id": "comment_id", "text": "message"}},
	}
}

// --- scenario 10 ---------------------------------------------------------

func TestE2E10ZhihuPackSandboxAndPublish(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}

	manifest, err := h.registry.Load(owner.Type, owner.ID, "zhihu", 1)
	require.NoError(t, err)
	root, err := h.registry.Root(owner.Type, owner.ID, "zhihu", 1)
	require.NoError(t, err)

	readRecipe := func(name string) browser.Recipe {
		raw, err := os.ReadFile(filepath.Join(root, "browser", "recipes", name+".json"))
		require.NoError(t, err)
		var recipe browser.Recipe
		require.NoError(t, json.Unmarshal(raw, &recipe))
		return recipe
	}
	publishValues := map[string]string{
		"title": "如何系统评估一次技术选型", "body": "先结论后论证，给出适用边界与数据来源。",
		"cover": "fixtures/cover.png", "topics": "职场,技术管理",
	}
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{name: "login"},
		{name: "publish", values: publishValues},
	} {
		recipe := readRecipe(tc.name)
		sandbox := &browser.Sandbox{Root: root, AllowedHosts: manifest.AllowedHosts, Values: tc.values}
		require.NoError(t, sandbox.Run(ctx, recipe, recipe.Fixtures), "sandbox %s", tc.name)
	}

	bot := h.createAccount(owner, "zhihu", "zhihu-brand", "知乎品牌号", `{"currency":"CNY","monthly_limit":500}`)
	recipes, err := loadPackRecipes(root, manifest)
	require.NoError(t, err)
	worker := &e2eBrowserWorker{
		values: map[string]browser.Result{
			"publish": {Values: map[string]string{"url": "https://zhuanlan.zhihu.com/p/e2e"}},
		},
		errs: map[string]error{},
	}
	browserAdapter := channel.NewBrowserAdapter(worker, recipes, manifest.Capabilities, channel.BrowserLimits{
		MaxChars: 100000, MaxTitleChars: 100, MaxImages: 50, ImageRatio: "16:9",
	}, manifest.MetricsMap.Fields)
	browserAdapter.BindAccount(bot.ID)
	h.adapters[bot.ID] = browserAdapter

	published, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 1201, Platform: "zhihu", PackID: "zhihu",
		AccountRef: bot.AccountRef,
		Payload: channel.BrowserPublishPayload{
			Kind: "article", Title: "如何系统评估一次技术选型",
			Body: "先结论后论证，给出适用边界与数据来源。", Images: []string{"cover.png"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, PublishStatusPublished, published.Status)
	require.Equal(t, "https://zhuanlan.zhihu.com/p/e2e", published.URL)
}

// --- scenario 11 ---------------------------------------------------------

func TestE2E11PackExportImportAcrossOwners(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	source := OwnerRef{Type: "user", ID: e2eOwnerUser}
	target := OwnerRef{Type: "user", ID: e2eOwnerAlt}

	sourceBot := h.createAccount(source, "wechat_mp", "export-brand", "导出源账号", `{"currency":"USD","monthly_limit":100}`)
	require.Equal(t, "wechat_mp", sourceBot.PackID)

	zipPath := filepath.Join(t.TempDir(), "wechat_mp-v1.zip")
	require.NoError(t, h.registry.Export(source.Type, source.ID, "wechat_mp", 1, zipPath))
	_, err := os.Stat(zipPath)
	require.NoError(t, err)

	_, err = h.registry.Load(target.Type, target.ID, "wechat_mp", 1)
	require.Error(t, err, "the second owner has no pack before import")

	require.NoError(t, h.registry.Import(target.Type, target.ID, zipPath))
	manifest, err := h.registry.Load(target.Type, target.ID, "wechat_mp", 1)
	require.NoError(t, err)
	require.Equal(t, "wechat_mp", manifest.ID)

	imported, err := h.svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: target, PackID: "wechat_mp", AccountRef: "imported-brand", DisplayName: "导入账号",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, imported.PackVersion)
	require.Equal(t, "user", imported.OwnerType)
	require.Equal(t, target.ID, imported.OwnerID)

	sourceBots, err := h.svc.ListByOwner(ctx, source)
	require.NoError(t, err)
	require.Len(t, sourceBots, 1)
	targetBots, err := h.svc.ListByOwner(ctx, target)
	require.NoError(t, err)
	require.Len(t, targetBots, 1)
	require.NotEqual(t, sourceBots[0].ID, targetBots[0].ID)
}

// --- scenario 12 ---------------------------------------------------------

func TestE2E12KillSwitchStopsAllAutomaticActions(t *testing.T) {
	h := newE2EHarness(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: e2eOwnerUser}
	bot := h.createAccount(owner, "xiaohongshu", "kill-brand", "急停号", `{"currency":"USD","monthly_limit":300}`)

	adapter := newE2EAdapter()
	adapter.engagements = []channel.Engagement{{
		ExtID: "k-1", Kind: "comment", Author: "u1", Text: "这个颜色真好看",
	}}
	h.adapters[bot.ID] = adapter

	publishReq := h.createApproval(t, bot, ApprovalPublish, 1301, 1, map[string]any{"title": "待发布"})
	videoReq := h.createApproval(t, bot, ApprovalVideo, 1301, 1, map[string]any{"title": "待成片"})

	policy := `{"publish":true,"reply":false,"video":true}`
	_, err := h.svc.UpdateConfig(ctx, bot.ID, ConfigPatch{ApprovalPolicyJSON: &policy})
	require.NoError(t, err)
	items, err := h.eng.Pull(ctx, BotRef{ID: bot.ID}, "")
	require.NoError(t, err)
	require.NoError(t, h.eng.Classify(ctx, BotRef{ID: bot.ID}, items[0].ID))
	require.NoError(t, h.eng.Suggest(ctx, BotRef{ID: bot.ID}, items[0].ID))
	_, err = h.eng.RequestApproval(ctx, BotRef{ID: bot.ID}, items[0].ID)
	require.NoError(t, err)
	queued, err := h.eng.Get(ctx, BotRef{ID: bot.ID}, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, EngagementApproved, queued.Status)

	outcome, err := h.risk.Kill(ctx, bot.ID, "E2E 急停")
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, outcome.Status)
	require.Equal(t, 2, outcome.ApprovalsCancelled)
	require.Equal(t, 1, outcome.EngagementsCleared)
	require.True(t, outcome.Notified)

	disabled, err := h.svc.Get(ctx, bot.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, disabled.Status)

	for _, req := range []ApprovalRequest{publishReq, videoReq} {
		got, err := h.approval.Get(ctx, req.ID)
		require.NoError(t, err)
		require.Equal(t, ApprovalStatusCancelled, got.Status)
	}
	pending, err := h.approval.ListPending(ctx, owner.Type, owner.ID)
	require.NoError(t, err)
	require.Empty(t, pending)

	drained, err := h.eng.Get(ctx, BotRef{ID: bot.ID}, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, EngagementRejected, drained.Status)

	blocked, err := h.pub.Publish(ctx, PublishInput{
		BotID: bot.ID, IssueID: 1301, Platform: bot.PackID, PackID: bot.PackID,
		Payload: map[string]string{"title": "must not send"},
	})
	require.ErrorIs(t, err, ErrBotNotActive)
	require.Equal(t, PublishStatusPending, blocked.Status)
	require.Zero(t, adapter.publishCalls)

	_, err = h.approval.Decide(ctx, publishReq.ID, Decision{OperatorID: 7, Action: ActionApproved})
	require.ErrorIs(t, err, ErrAlreadyDecided)
}
