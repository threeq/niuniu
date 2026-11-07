package channel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

type fakeBrowserWorker struct {
	tasks  []browser.Task
	byName map[string]fakeBrowserResult
	def    fakeBrowserResult
}

type fakeBrowserResult struct {
	result browser.Result
	err    error
}

func (f *fakeBrowserWorker) Run(_ context.Context, task browser.Task) (browser.Result, error) {
	f.tasks = append(f.tasks, task)
	if r, ok := f.byName[task.Recipe]; ok {
		return r.result, r.err
	}
	return f.def.result, f.def.err
}

func (f *fakeBrowserWorker) lastTask(t *testing.T) browser.Task {
	t.Helper()
	if len(f.tasks) == 0 {
		t.Fatal("worker was not called")
	}
	return f.tasks[len(f.tasks)-1]
}

func xhsRecipeSet(names ...string) RecipeSet {
	set := make(RecipeSet, len(names))
	for _, name := range names {
		set[name] = packs.Recipe{Name: name}
	}
	return set
}

func newFakeXHSWorker() *fakeBrowserWorker {
	return &fakeBrowserWorker{
		byName: map[string]fakeBrowserResult{
			"login":         {},
			"publish":       {result: browser.Result{Values: map[string]string{"note_url": "https://www.xiaohongshu.com/explore/note-1"}}},
			"publish_video": {result: browser.Result{Values: map[string]string{"note_url": "https://www.xiaohongshu.com/explore/video-1"}}},
			"metrics": {
				result: browser.Result{Values: map[string]string{
					"view_num":    "1,234",
					"like_num":    "210",
					"collect_num": "98",
					"comment_num": "17",
					"share_num":   "6",
					"fans_add":    "12",
				}},
			},
			"engagements": {result: browser.Result{Values: map[string]string{
				"engagements": `[{"id":"c_1","kind":"comment","author":"用户A","text":"显白吗"},{"id":"d_2","kind":"dm","author":"用户B","text":"求链接"}]`,
			}}},
			"reply":  {result: browser.Result{Values: map[string]string{"receipt": "msg-1"}}},
			"health": {},
		},
	}
}

func newXHSAdapter(t *testing.T, worker *fakeBrowserWorker) *XHSAdapter {
	t.Helper()
	a := NewXHSAdapter(worker, xhsRecipeSet("login", "publish", "publish_video", "metrics", "engagements", "reply", "health"))
	if err := a.EnsureSession(context.Background(), "42"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	return a
}

func TestXHSCapabilities(t *testing.T) {
	a := NewXHSAdapter(newFakeXHSWorker(), xhsRecipeSet())
	caps := a.Capabilities()
	for _, capability := range []Capability{CapPublish, CapMetrics, CapEngagements, CapReply} {
		if !caps.Supports(capability) {
			t.Fatalf("xiaohongshu must support %q: %v", capability, caps)
		}
	}
}

func TestXHSEnsureSessionParsesAccountRef(t *testing.T) {
	worker := newFakeXHSWorker()
	a := NewXHSAdapter(worker, xhsRecipeSet("login"))

	if err := a.EnsureSession(context.Background(), " 42 "); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	task := worker.lastTask(t)
	if task.Recipe != "login" || task.BotID != 42 || task.Backend != browser.BackendLocal {
		t.Fatalf("login task = %+v, want login for bot 42 on local backend", task)
	}

	if err := a.EnsureSession(context.Background(), ""); asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("empty account ref code = %v", err)
	}
	if err := a.EnsureSession(context.Background(), "bot-42"); asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("non-numeric account ref code = %v", err)
	}
}

func TestXHSEnsureSessionReportsLoginExpired(t *testing.T) {
	worker := newFakeXHSWorker()
	worker.byName["login"] = fakeBrowserResult{err: browser.ErrLoginExpired}
	a := NewXHSAdapter(worker, xhsRecipeSet("login"))

	err := a.EnsureSession(context.Background(), "42")
	adapterErr := asAdapterError(t, err)
	if adapterErr.Code != CodeLoginExpired || adapterErr.Retryable {
		t.Fatalf("expired login mapped to %+v, want non-retryable login_expired", adapterErr)
	}
	if !errors.Is(err, browser.ErrLoginExpired) {
		t.Fatalf("error must keep the browser sentinel for relogin routing: %v", err)
	}
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("expired login must gate degradation: %v", err)
	}
}

func TestXHSPublishImageNoteMapsInputs(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	receipt, err := a.Publish(context.Background(), XHSPublishPayload{
		Title:      "秋冬显白穿搭",
		Body:       "今天分享一套显白的搭配",
		Images:     []string{"/tmp/a.png", "/tmp/b.png", "/tmp/c.png"},
		Tags:       []string{"穿搭", "显白"},
		Cover:      "/tmp/a.png",
		ImageRatio: "3:4",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	task := worker.lastTask(t)
	if task.Recipe != "publish" || task.BotID != 42 {
		t.Fatalf("task = %+v", task)
	}
	wantInput := map[string]string{
		"kind":   "note",
		"title":  "秋冬显白穿搭",
		"body":   "今天分享一套显白的搭配",
		"images": "/tmp/a.png,/tmp/b.png,/tmp/c.png",
		"tags":   "穿搭,显白",
		"cover":  "/tmp/a.png",
	}
	if !reflect.DeepEqual(task.Input, wantInput) {
		t.Fatalf("input = %v, want %v", task.Input, wantInput)
	}
	if receipt.URL != "https://www.xiaohongshu.com/explore/note-1" {
		t.Fatalf("receipt URL = %q", receipt.URL)
	}
}

func TestXHSPublishVideoNoteUsesVideoRecipe(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	_, err := a.Publish(context.Background(), XHSPublishPayload{
		Kind:        "video",
		Title:       "vlog 日常",
		Body:        "记录一天",
		VideoPath:   "/tmp/v.mp4",
		Cover:       "/tmp/cover.png",
		DurationSec: 60,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	task := worker.lastTask(t)
	if task.Recipe != "publish_video" {
		t.Fatalf("recipe = %q, want publish_video", task.Recipe)
	}
	if task.Input["kind"] != "video" || task.Input["video"] != "/tmp/v.mp4" || task.Input["cover"] != "/tmp/cover.png" {
		t.Fatalf("video input = %v", task.Input)
	}
}

func TestXHSPublishVideoFallsBackToPublishRecipe(t *testing.T) {
	worker := newFakeXHSWorker()
	a := NewXHSAdapter(worker, xhsRecipeSet("login", "publish"))
	if err := a.EnsureSession(context.Background(), "42"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	if _, err := a.Publish(context.Background(), XHSPublishPayload{
		Kind: "video", Title: "t", Body: "b", VideoPath: "/tmp/v.mp4",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if recipe := worker.lastTask(t).Recipe; recipe != "publish" {
		t.Fatalf("recipe = %q, want the generic publish recipe", recipe)
	}
}

func TestXHSPublishRejectsInvalidContent(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)
	base := func() XHSPublishPayload {
		return XHSPublishPayload{Title: "标题", Body: "正文", Images: []string{"/tmp/a.png"}}
	}

	cases := []struct {
		name string
		edit func(*XHSPublishPayload)
		code string
	}{
		{"empty title", func(p *XHSPublishPayload) { p.Title = "  " }, CodeInvalidPayload},
		{"title too long", func(p *XHSPublishPayload) { p.Title = strings.Repeat("字", 21) }, CodeInvalidPayload},
		{"body too long", func(p *XHSPublishPayload) { p.Body = strings.Repeat("字", 1001) }, CodeInvalidPayload},
		{"no images", func(p *XHSPublishPayload) { p.Images = nil }, CodeInvalidPayload},
		{"too many images", func(p *XHSPublishPayload) {
			p.Images = make([]string, 19)
			for i := range p.Images {
				p.Images[i] = "/tmp/a.png"
			}
		}, CodeInvalidPayload},
		{"wrong ratio", func(p *XHSPublishPayload) { p.ImageRatio = "9:16" }, CodeInvalidPayload},
		{"unknown kind", func(p *XHSPublishPayload) { p.Kind = "audio" }, CodeUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := base()
			tc.edit(&payload)
			_, err := a.Publish(context.Background(), payload)
			if got := asAdapterError(t, err).Code; got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
			if len(worker.tasks) != 1 {
				t.Fatalf("invalid payload must not run a task: %d", len(worker.tasks))
			}
		})
	}

	if _, err := a.Publish(context.Background(), "not-a-payload"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("unknown payload type code = %v", err)
	}

	_, err := a.Publish(context.Background(), XHSPublishPayload{Kind: "video", Title: "t", Body: "b", DurationSec: 901})
	if asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("over-long video code = %v", err)
	}
}

func TestXHSPublishRequiresSession(t *testing.T) {
	worker := newFakeXHSWorker()
	a := NewXHSAdapter(worker, xhsRecipeSet("publish"))

	_, err := a.Publish(context.Background(), XHSPublishPayload{Title: "t", Body: "b", Images: []string{"/tmp/a.png"}})
	if asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("publish without session code = %v", err)
	}
	if len(worker.tasks) != 0 {
		t.Fatal("worker must not run before EnsureSession")
	}
}

func TestXHSFetchMetricsMapsUnifiedKeys(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	got, err := a.FetchMetrics(context.Background(), "2026-11-01", "2026-11-06")
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	want := map[string]float64{
		"views":          1234,
		"likes":          210,
		"saves":          98,
		"comments":       17,
		"shares":         6,
		"follower_delta": 12,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metrics = %v, want %v", got, want)
	}
	if r := worker.lastTask(t).Input["range"]; r != "7d" {
		t.Fatalf("range = %q, want 7d", r)
	}
}

func TestXHSFetchMetricsFillsAbsentKeysWithZero(t *testing.T) {
	worker := newFakeXHSWorker()
	worker.byName["metrics"] = fakeBrowserResult{result: browser.Result{Values: map[string]string{"view_num": "1.2万"}}}
	a := newXHSAdapter(t, worker)

	got, err := a.FetchMetrics(context.Background(), "2026-11-01", "2026-11-06")
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	for _, key := range xhsUnifiedMetrics {
		if _, ok := got[key]; !ok {
			t.Fatalf("metric %q missing from %v", key, got)
		}
	}
	if got[MetricViews] != 12000 || got[MetricLikes] != 0 {
		t.Fatalf("metrics = %v", got)
	}
}

func TestXHSFetchMetricsRangeMapping(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	cases := []struct {
		from, to string
		want     string
	}{
		{"2026-11-01", "2026-11-07", "7d"},
		{"2026-11-01", "2026-11-30", "30d"},
		{"2026-09-02", "2026-11-30", "90d"},
	}
	for _, tc := range cases {
		if _, err := a.FetchMetrics(context.Background(), tc.from, tc.to); err != nil {
			t.Fatalf("fetch %s..%s: %v", tc.from, tc.to, err)
		}
		if got := worker.lastTask(t).Input["range"]; got != tc.want {
			t.Fatalf("range for %s..%s = %q, want %q", tc.from, tc.to, got, tc.want)
		}
	}

	for _, tc := range [][2]string{
		{"", "2026-11-06"},
		{"2026-11-07", "2026-11-01"},
		{"2026-01-01", "2026-11-01"},
	} {
		if _, err := a.FetchMetrics(context.Background(), tc[0], tc[1]); asAdapterError(t, err).Code != CodeInvalidPayload {
			t.Fatalf("range %s..%s code = %v", tc[0], tc[1], err)
		}
	}
}

func TestXHSListEngagementsParsesRecipeOutput(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	items, err := a.ListEngagements(context.Background(), "cursor-2")
	if err != nil {
		t.Fatalf("list engagements: %v", err)
	}
	want := []Engagement{
		{ExtID: "c_1", Kind: "comment", Author: "用户A", Text: "显白吗"},
		{ExtID: "d_2", Kind: "dm", Author: "用户B", Text: "求链接"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("engagements = %+v, want %+v", items, want)
	}
	if cursor := worker.lastTask(t).Input["cursor"]; cursor != "cursor-2" {
		t.Fatalf("cursor input = %q", cursor)
	}
}

func TestXHSListEngagementsParsesLineFormat(t *testing.T) {
	worker := newFakeXHSWorker()
	worker.byName["engagements"] = fakeBrowserResult{result: browser.Result{Values: map[string]string{
		"engagements": "c_1\tcomment\t用户A\t显白吗\nd_2|dm|用户B|求链接\n",
	}}}
	a := newXHSAdapter(t, worker)

	items, err := a.ListEngagements(context.Background(), "")
	if err != nil {
		t.Fatalf("list engagements: %v", err)
	}
	if len(items) != 2 || items[1].ExtID != "d_2" || items[1].Kind != "dm" || items[1].Text != "求链接" {
		t.Fatalf("engagements = %+v", items)
	}
}

func TestXHSListEngagementsEmptyOutput(t *testing.T) {
	worker := newFakeXHSWorker()
	worker.byName["engagements"] = fakeBrowserResult{result: browser.Result{Values: map[string]string{"engagements": "  "}}}
	a := newXHSAdapter(t, worker)

	items, err := a.ListEngagements(context.Background(), "")
	if err != nil {
		t.Fatalf("list engagements: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("engagements = %+v, want empty", items)
	}
}

func TestXHSReplySendsThroughRecipe(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	receipt, err := a.Reply(context.Background(), "c_1", "谢谢喜欢")
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	task := worker.lastTask(t)
	if task.Recipe != "reply" {
		t.Fatalf("recipe = %q", task.Recipe)
	}
	if task.Input["engagement_id"] != "c_1" || task.Input["reply_text"] != "谢谢喜欢" {
		t.Fatalf("input = %v", task.Input)
	}
	if receipt.ExternalID != "msg-1" {
		t.Fatalf("receipt = %+v", receipt)
	}

	if _, err := a.Reply(context.Background(), "", "hi"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("empty engagement code = %v", err)
	}
	if _, err := a.Reply(context.Background(), "c_1", "  "); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("empty reply text code = %v", err)
	}
}

func TestXHSHealthCheckRunsHealthRecipe(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if recipe := worker.lastTask(t).Recipe; recipe != "health" {
		t.Fatalf("recipe = %q, want health", recipe)
	}

	worker.byName["health"] = fakeBrowserResult{err: browser.ErrTimeout}
	if err := a.HealthCheck(context.Background()); asAdapterError(t, err).Code != CodeTransport {
		t.Fatalf("timeout code = %v", err)
	}
}

func TestXHSMapsRiskControls(t *testing.T) {
	worker := newFakeXHSWorker()
	a := newXHSAdapter(t, worker)

	worker.byName["publish"] = fakeBrowserResult{err: browser.ErrBlocked}
	_, err := a.Publish(context.Background(), XHSPublishPayload{Title: "t", Body: "b", Images: []string{"/tmp/a.png"}})
	blocked := asAdapterError(t, err)
	if blocked.Code != CodeRiskBlocked || blocked.Retryable {
		t.Fatalf("blocked publish mapped to %+v, want non-retryable risk_blocked", blocked)
	}

	worker.byName["reply"] = fakeBrowserResult{err: browser.ErrRateLimited}
	if _, err := a.Reply(context.Background(), "c_1", "hi"); asAdapterError(t, err).Code != CodeRateLimited {
		t.Fatalf("rate limit code = %v", err)
	}

	worker.byName["metrics"] = fakeBrowserResult{err: browser.ErrRecipeBroken}
	broken := asAdapterError(t, mustError(a.FetchMetrics(context.Background(), "2026-11-01", "2026-11-06")))
	if broken.Code != CodeRecipeBroken || broken.Retryable {
		t.Fatalf("recipe broken mapped to %+v, want non-retryable recipe_broken", broken)
	}
}

func TestXHSMissingRecipeReportsUnsupported(t *testing.T) {
	worker := newFakeXHSWorker()
	a := NewXHSAdapter(worker, xhsRecipeSet("login"))
	if err := a.EnsureSession(context.Background(), "42"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	_, err := a.Publish(context.Background(), XHSPublishPayload{Title: "t", Body: "b", Images: []string{"/tmp/a.png"}})
	if asAdapterError(t, err).Code != CodeUnsupported {
		t.Fatalf("missing publish recipe code = %v", err)
	}
}

func TestXHSParseMetricValue(t *testing.T) {
	cases := map[string]float64{
		"":      0,
		"1234":  1234,
		"1,234": 1234,
		"1.2万":  12000,
		"3w":    30000,
		"1.5亿":  150000000,
		" 12 ":  12,
	}
	for raw, want := range cases {
		got, err := parseXHSMetric(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if got != want {
			t.Fatalf("parse %q = %v, want %v", raw, got, want)
		}
	}
	if _, err := parseXHSMetric("n/a"); err == nil {
		t.Fatal("unparseable metric must error")
	}
}

func mustError(_ any, err error) error { return err }
