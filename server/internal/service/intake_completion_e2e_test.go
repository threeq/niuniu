package service

// End-to-end acceptance tests for the Intake / Completion split (design
// 2026-11-03-intake-completion-autonomy-design.md §9 "端到端" and §10).
//
// The suite drives the product path at the service boundary: real intake
// pipeline + real store + real event bus + the two outbound dispatchers, with
// only the external platforms (IM adapter, GitHub HTTP API) and the agent CLI
// replaced by fixtures. The autohost *decision* branch itself is covered by
// internal/agentproxy (autohost_policy_test.go); the Task Brief acceptance rows
// it reads are produced here by the real TaskBriefWriter, so the two halves
// share the same `kind='acceptance'` contract.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/completion"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Scenario 1 (§9 E2E #1, §10.6/7): one IM sentence becomes a task with a Task
// Brief; the acceptance gate's inputs are materialized; the finished turn is
// pushed back to the same chat.
// ---------------------------------------------------------------------------

func TestE2E_IMOneLinerToBriefToChatReply(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	f.activeChat(t, "oc_e2e")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_e2e", Text: "帮我实现首页暗色模式",
		Kind: "message", EventID: "e2e-im-1", MessageExtID: "m1", ActorExtID: "alice",
	})

	// One message -> exactly one task and one delivery.
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("intake side effects: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	if f.deliverer.calls[0].workspaceID != f.wsID {
		t.Fatalf("delivery workspace = %d, want %d", f.deliverer.calls[0].workspaceID, f.wsID)
	}

	// The decision is recorded and marked executed (§10.8/§10.2 groundwork).
	ev := f.intakeEvent(t, "oc_e2e", "e2e-im-1")
	decs, err := f.q.ListIntakeDecisionsByEvent(ctx, ev.ID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("decisions = %v (err %v)", decs, err)
	}
	if decs[0].Executed != 1 || decs[0].Decision != string(intake.DecisionAccept) {
		t.Fatalf("decision = %+v", decs[0])
	}

	// The chat learns the task id through the anchored marker.
	f.adapter.mu.Lock()
	replies := append([]replyCall(nil), f.adapter.replies...)
	f.adapter.mu.Unlock()
	if len(replies) != 1 || !strings.Contains(replies[0].text, "#"+itoa(f.issueID)) {
		t.Fatalf("task marker replies = %+v", replies)
	}

	// The accepted trigger seeded Task Brief v1 (§10.6).
	briefs := NewTaskBriefService(f.q, store.Wrap(f.db))
	view, err := briefs.GetIssueBrief(ctx, f.issueID)
	if err != nil {
		t.Fatalf("brief v1: %v", err)
	}
	if view.Brief == nil || view.Brief.Version != 1 || len(view.Versions) != 1 {
		t.Fatalf("brief v1 = %+v", view)
	}
	if view.GoalCondition != "帮我实现首页暗色模式" {
		t.Fatalf("goal_condition v1 = %q", view.GoalCondition)
	}

	// A manual progress item exists before the brief is edited; it must survive.
	manual, err := f.q.CreateChecklist(ctx, store.CreateChecklistParams{
		IssueID: f.issueID, Title: "手工进度", IssueID_2: f.issueID,
	})
	if err != nil {
		t.Fatalf("manual checklist: %v", err)
	}

	// Editing the brief appends v2; v1 stays queryable and acceptance is
	// materialized while manual rows are untouched (§10.15).
	view, err = briefs.PutIssueBrief(ctx, f.issueID, TaskBriefInput{
		Goal:       "帮我实现首页暗色模式",
		Acceptance: []string{"单元测试通过", "文档已更新"},
	}, 1)
	if err != nil {
		t.Fatalf("brief v2: %v", err)
	}
	if view.Brief.Version != 2 || len(view.Versions) != 2 {
		t.Fatalf("brief versions = %+v", view.Versions)
	}
	sawV1 := false
	for _, v := range view.Versions {
		if v.Version == 1 {
			sawV1 = true
		}
	}
	if !sawV1 {
		t.Fatalf("v1 missing after append: %+v", view.Versions)
	}
	if !strings.Contains(view.GoalCondition, "验收标准（全部满足才算完成）") {
		t.Fatalf("goal_condition not rendered from acceptance: %q", view.GoalCondition)
	}

	acceptance, err := f.q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: f.issueID, Kind: "acceptance",
	})
	if err != nil || len(acceptance) != 2 {
		t.Fatalf("acceptance rows = %v (err %v)", acceptance, err)
	}
	manualRows, err := f.q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: f.issueID, Kind: "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	foundManual := false
	for _, r := range manualRows {
		if r.ID == manual.ID {
			foundManual = true
		}
	}
	if !foundManual {
		t.Fatalf("manual row %d disappeared after brief edit", manual.ID)
	}

	// This is exactly what autohost reads: acceptance rows still open mean the
	// sentinel must not be accepted (§10.7).
	unmet := 0
	for _, r := range acceptance {
		if r.IsCompleted == 0 {
			unmet++
		}
	}
	if unmet != 2 {
		t.Fatalf("unmet acceptance before ticking = %d, want 2", unmet)
	}
	for _, r := range acceptance {
		if _, err := f.q.UpdateChecklist(ctx, store.UpdateChecklistParams{
			Title: r.Title, IsCompleted: 1, ID: r.ID,
		}); err != nil {
			t.Fatalf("tick acceptance %d: %v", r.ID, err)
		}
	}
	acceptance, err = f.q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: f.issueID, Kind: "acceptance",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range acceptance {
		if r.IsCompleted == 0 {
			t.Fatalf("acceptance %q still open after ticking", r.Title)
		}
	}

	// The finished turn is delivered back to the same chat (§9 E2E #1).
	bus := event.NewBus()
	disp := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	disp.Start()
	defer disp.Stop()

	bus.Publish(event.OutputEvent{
		Type: event.EventAgentDone, Content: "暗色模式已完成。", WorkspaceId: f.wsID, Ts: 1700000000000,
	})
	pushes := waitForPushes(f.adapter, 1)
	if len(pushes) != 1 {
		t.Fatalf("chat pushes = %d, want 1", len(pushes))
	}
	if pushes[0].ChatExtID != "oc_e2e" {
		t.Fatalf("reply chat = %q, want oc_e2e", pushes[0].ChatExtID)
	}
	if !strings.Contains(pushes[0].Text, "暗色模式已完成。") || !strings.Contains(pushes[0].Text, "#"+itoa(f.issueID)) {
		t.Fatalf("reply text = %q", pushes[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Scenario 2 (§9 E2E #2, §10.5): a PR comment creates a task and binds the
// thread; a later comment after a restart resumes the same workspace without
// re-triage; the finished turn is posted back to the PR once.
// ---------------------------------------------------------------------------

type e2eGithubFixture struct {
	*imbotFixture
	svc      *GithubSourceService
	server   *httptest.Server
	creds    *ExternalCredentialService
	mu       sync.Mutex
	comments []string
	posted   []e2ePostedComment
	now      time.Time
}

type e2ePostedComment struct {
	Path string
	Body string
	Auth string
}

func (fx *e2eGithubFixture) postedComments() []e2ePostedComment {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]e2ePostedComment(nil), fx.posted...)
}

func (fx *e2eGithubFixture) waitPosted(n int) []e2ePostedComment {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := fx.postedComments(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fx.postedComments()
}

func newE2EGithubFixture(t *testing.T) *e2eGithubFixture {
	t.Helper()
	f := newIMBotFixture(t)
	ctx := context.Background()

	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindGitHub,
			Mode:       "off",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("github intake settings: %v", err)
	}

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	creds := NewExternalCredentialService(f.q, f.db, kr, integration.NewRegistry())
	cred, err := creds.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider: integration.ProviderGitHub, Alias: "gh",
		RawConfig: map[string]any{"token": "ghp_e2e"},
	})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO external_providers (name, api_base_url, enabled)
		VALUES ('github', 'https://api.github.com', 1)`); err != nil {
		t.Fatalf("provider: %v", err)
	}
	var providerID int64
	if err := f.db.QueryRowContext(ctx,
		`SELECT id FROM external_providers WHERE name = 'github'`).Scan(&providerID); err != nil {
		t.Fatalf("provider id: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO external_api_write_prefs (owner_type, owner_id, provider_id, enabled)
		VALUES ('user', 1, ?, 1)`, providerID); err != nil {
		t.Fatalf("write prefs: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
		VALUES (?, 'github', 'acme/foo', ?)`, f.projectID, cred.ID); err != nil {
		t.Fatalf("repo binding: %v", err)
	}

	fx := &e2eGithubFixture{
		imbotFixture: f,
		creds:        creds,
		now:          time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
	}
	fx.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/foo/issues/comments":
			fx.mu.Lock()
			body := "[" + strings.Join(fx.comments, ",") + "]"
			fx.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(raw, &body)
			fx.mu.Lock()
			fx.posted = append(fx.posted, e2ePostedComment{
				Path: r.URL.Path, Body: body.Body, Auth: r.Header.Get("Authorization"),
			})
			fx.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":9001,"html_url":"https://github.com/acme/foo/pull/90#issuecomment-9001"}`)
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fx.server.Close)

	fx.setService(NewGithubSourceService(f.db, f.q, creds))
	return fx
}

// setService points the fixture's GitHub service at the fake API and the
// current test clock. Kept separate so a test can rebuild the service over the
// same database to prove restart behaviour.
func (fx *e2eGithubFixture) setService(svc *GithubSourceService) {
	fx.svc = svc
	svc.SetPollerBaseURL(fx.server.URL)
	svc.poller.Clock = func() time.Time { return fx.now }
	svc.poller.InitialLookback = time.Hour
}

func (fx *e2eGithubFixture) poll(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := fx.svc.poller.PollRepo(ctx, githubsource.RepoConfig{
		ProjectID: fx.projectID, Repo: "acme/foo",
		Events: []string{githubsource.EventIssueComment},
	}); err != nil {
		t.Fatalf("poll: %v", err)
	}
}

func TestE2E_GithubPRCommentResumesWorkspaceAndReplies(t *testing.T) {
	fx := newE2EGithubFixture(t)
	ctx := context.Background()
	fx.router.queue = []PlanTarget{{IssueID: fx.issueID, WorkspaceID: fx.wsID, ProjectID: fx.projectID}}
	fx.svc.SetInbound(fx.router, fx.deliverer)

	firstTime := fx.now.Add(-time.Minute)
	fx.mu.Lock()
	fx.comments = []string{issueCommentBody(1001, 90, "登录页点不动，修一下", firstTime)}
	fx.mu.Unlock()

	fx.poll(t, ctx)
	if fx.router.calls != 1 || len(fx.deliverer.calls) != 1 {
		t.Fatalf("first comment: router=%d deliveries=%d", fx.router.calls, len(fx.deliverer.calls))
	}
	binding, ok, err := fx.svc.threads.Get(ctx, fx.projectID, "acme/foo", "90")
	if err != nil || !ok {
		t.Fatalf("binding: ok=%v err=%v", ok, err)
	}
	if binding.IssueID != fx.issueID || binding.WorkspaceID != fx.wsID {
		t.Fatalf("binding = %+v", binding)
	}

	// Restart the service over the same DB, then poll again with a follow-up
	// comment. The persisted cursor/dedupe must not replay the first comment and
	// the bound thread must resume without a second triage (§10.5/§10.8).
	fx.now = fx.now.Add(3 * time.Minute)
	fx.mu.Lock()
	fx.comments = append(fx.comments, issueCommentBody(1002, 90, "顺带加上记住密码", fx.now.Add(-time.Minute)))
	fx.mu.Unlock()

	router := &fakeRouter{q: fx.q}
	deliverer := &fakeDeliverer{}
	restarted := NewGithubSourceService(fx.db, fx.q, fx.creds)
	fx.setService(restarted)
	restarted.SetInbound(router, deliverer)
	fx.poll(t, ctx)

	if router.calls != 0 {
		t.Fatalf("follow-up re-triaged: router calls = %d", router.calls)
	}
	if len(deliverer.calls) != 1 || deliverer.calls[0].workspaceID != fx.wsID {
		t.Fatalf("follow-up deliveries = %+v", deliverer.calls)
	}

	// The finished turn goes back to the PR thread exactly once; the internal
	// completion sentinel never leaks to the public comment (§9 E2E #2).
	bus := event.NewBus()
	disp := NewGithubReplyDispatcher(bus, restarted)
	disp.Start()
	defer disp.Stop()

	bus.Publish(event.OutputEvent{
		Type: event.EventAgentDone, WorkspaceId: fx.wsID, Ts: 1700000000123,
		Content: "登录页已修复，已记住密码。 [AUTOHOST_DONE]",
	})
	posted := fx.waitPosted(1)
	if len(posted) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(posted))
	}
	if posted[0].Path != "/repos/acme/foo/issues/90/comments" {
		t.Fatalf("posted to %q", posted[0].Path)
	}
	if posted[0].Auth != "Bearer ghp_e2e" {
		t.Fatalf("posted auth = %q", posted[0].Auth)
	}
	if strings.Contains(posted[0].Body, "[AUTOHOST_DONE]") {
		t.Fatalf("sentinel leaked to PR: %q", posted[0].Body)
	}
	if !strings.Contains(posted[0].Body, "登录页已修复") {
		t.Fatalf("posted body = %q", posted[0].Body)
	}

	// A replayed bus event (same turn) is not posted twice.
	bus.Publish(event.OutputEvent{
		Type: event.EventAgentDone, WorkspaceId: fx.wsID, Ts: 1700000000123,
		Content: "登录页已修复，已记住密码。",
	})
	time.Sleep(150 * time.Millisecond)
	if n := len(fx.postedComments()); n != 1 {
		t.Fatalf("replayed turn posted %d comments", n)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3 (§9 E2E #3, §10.2): observe mode analyzes but changes nothing;
// the queued decision can still be accepted later and only then creates work.
// ---------------------------------------------------------------------------

func TestE2E_ObserveDryRunZeroSideEffectsThenAccept(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_observe")
	// A chat that reads every message (legacy observe) plus the migrated intake
	// mode observe: unlisted actors are inferred candidates, and observe records
	// a would-do instead of executing.
	f.setChatAgentMode(t, chat, AgentModeObserve)
	f.setChatIntakeMode(t, chat, IntakeModeObserve)
	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       "observe",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}

	issuesBefore := countRows(t, f.db, "issues")
	workspacesBefore := countRows(t, f.db, "workspaces")
	briefsBefore := countRows(t, f.db, "task_briefs")

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_observe", Text: "建议给导出加 CSV",
		Kind: "message", EventID: "e2e-observe-1", ThreadExtID: "t-observe", ActorExtID: "bob",
	})

	// Zero side effects: no task, no workspace, no delivery, no chat message.
	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("observe executed work: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	f.adapter.mu.Lock()
	replies, pushes := len(f.adapter.replies), len(f.adapter.pushes)
	f.adapter.mu.Unlock()
	if replies != 0 || pushes != 0 {
		t.Fatalf("observe produced chat output: replies=%d pushes=%d", replies, pushes)
	}
	if got := countRows(t, f.db, "issues"); got != issuesBefore {
		t.Fatalf("observe created issues: %d -> %d", issuesBefore, got)
	}
	if got := countRows(t, f.db, "workspaces"); got != workspacesBefore {
		t.Fatalf("observe created workspaces: %d -> %d", workspacesBefore, got)
	}
	if got := countRows(t, f.db, "task_briefs"); got != briefsBefore {
		t.Fatalf("observe wrote briefs: %d -> %d", briefsBefore, got)
	}

	// The decision is visible in the calibration queue with its would-do brief.
	ev := f.intakeEvent(t, "oc_observe", "e2e-observe-1")
	decs, err := f.q.ListIntakeDecisionsByEvent(ctx, ev.ID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("decisions = %v (err %v)", decs, err)
	}
	d := decs[0]
	if d.Executed != 0 || d.Decision != string(intake.DecisionAccept) || d.Strength != string(intake.StrengthInferred) {
		t.Fatalf("dry-run decision = %+v", d)
	}
	var wouldDo intake.WouldDo
	if err := json.Unmarshal([]byte(d.WouldDoJson), &wouldDo); err != nil {
		t.Fatalf("would_do json = %q: %v", d.WouldDoJson, err)
	}
	if wouldDo.Brief == nil || wouldDo.Brief.Goal != "建议给导出加 CSV" {
		t.Fatalf("would-do brief = %+v", wouldDo.Brief)
	}

	// Accepting from the queue is the only point side effects appear (§10.2).
	issueID, wsID, err := f.svc.AcceptDecision(ctx, d)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if issueID != f.issueID || wsID != f.wsID {
		t.Fatalf("accepted target = issue %d ws %d", issueID, wsID)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("accept side effects: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	view, err := NewTaskBriefService(f.q, store.Wrap(f.db)).GetIssueBrief(ctx, issueID)
	if err != nil || view.Brief == nil {
		t.Fatalf("accepted brief = %+v (err %v)", view, err)
	}
}

// ---------------------------------------------------------------------------
// Scenario 4 (§10.8): a redelivered event after a service restart produces
// exactly one task/decision.
// ---------------------------------------------------------------------------

func TestE2E_IMRestartRedeliveryExactlyOnce(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_restart")
	_ = chat
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}

	ev := imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_restart", Text: "重启后不要重复建任务",
		Kind: "message", EventID: "e2e-restart-1", MessageExtID: "m-restart", ActorExtID: "alice",
	}
	f.svc.HandleInbound(ctx, ev)
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("first delivery: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}

	// Same database, fresh service instance (process restart).
	router := &fakeRouter{q: f.q}
	deliverer := &fakeDeliverer{}
	restarted := NewIMBotService(f.q, f.db, nil, nil, f.svc.adapters)
	restarted.SetInbound(router, deliverer, &fakePerm{})
	restarted.SetAskUserDecider(&fakeAskUser{})

	restarted.HandleInbound(ctx, ev)
	if router.calls != 0 || len(deliverer.calls) != 0 {
		t.Fatalf("restart replayed: router=%d deliveries=%d", router.calls, len(deliverer.calls))
	}

	row := f.intakeEvent(t, "oc_restart", "e2e-restart-1")
	decs, err := f.q.ListIntakeDecisionsByEvent(ctx, row.ID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("decisions after replay = %v (err %v)", decs, err)
	}
	if n := countRows(t, f.db, "issues"); n != 1 {
		t.Fatalf("replay created tasks: issues=%d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Scenario 5 (§9 E2E #4, §10.4): an unlisted actor is recorded as denied and
// cannot create a task.
// ---------------------------------------------------------------------------

func TestE2E_UnlistedActorDenied(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_denied")

	// auto (not observe) means the chat does not "receive all", so an unlisted
	// actor has no inferred path either.
	f.setChatIntakeMode(t, chat, IntakeModeAuto)
	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       "auto",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}
	issuesBefore := countRows(t, f.db, "issues")

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_denied", Text: "帮我删库",
		Kind: "message", EventID: "e2e-denied-1", ActorExtID: "mallory",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("denied actor executed work: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	if got := countRows(t, f.db, "issues"); got != issuesBefore {
		t.Fatalf("denied actor created issues: %d -> %d", issuesBefore, got)
	}
	ev := f.intakeEvent(t, "oc_denied", "e2e-denied-1")
	decs, err := f.q.ListIntakeDecisionsByEvent(ctx, ev.ID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("decisions = %v (err %v)", decs, err)
	}
	if decs[0].Decision != string(intake.DecisionIgnore) || decs[0].Strength != string(intake.StrengthDenied) || decs[0].Executed != 0 {
		t.Fatalf("denied decision = %+v", decs[0])
	}
}

// ---------------------------------------------------------------------------
// Scenario 6 (§10.1/§10.16): completion profile resolution (issue > project,
// no restart) and its guard rails, plus the intake/completion independence
// promised by the split.
// ---------------------------------------------------------------------------

func TestE2E_CompletionPolicyResolutionRegression(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()

	if err := completion.EnsureProjectDefaults(ctx, store.Wrap(f.db), f.projectID); err != nil {
		t.Fatalf("seed completion defaults: %v", err)
	}
	svc := NewCompletionService(f.db, f.q)

	// No override -> project default.
	eff, err := svc.ResolveIssuePolicy(ctx, f.issueID)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Source != completion.SourceProjectDefault || eff.MaxRounds != 20 {
		t.Fatalf("default resolution = %+v", eff)
	}

	// A settings change is visible on the next resolve, without a restart.
	if _, err := svc.UpdateSettings(ctx, f.projectID, 1, CompletionSettingsInput{MaxRounds: ptr(int64(7))}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	eff, _ = svc.ResolveIssuePolicy(ctx, f.issueID)
	if eff.MaxRounds != 7 {
		t.Fatalf("project default change not applied: %+v", eff)
	}

	// Issue profile wins over the project default.
	profiles, err := svc.ListProfiles(ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	var deep CompletionProfileDTO
	for _, p := range profiles {
		if p.Name == "深度" {
			deep = p
		}
	}
	if deep.ID == 0 {
		t.Fatalf("builtin 深度 profile missing: %+v", profiles)
	}
	if _, err := svc.SetIssueProfile(ctx, f.issueID, 1, ptr(deep.ID)); err != nil {
		t.Fatalf("set issue profile: %v", err)
	}
	eff, _ = svc.ResolveIssuePolicy(ctx, f.issueID)
	if eff.Source != completion.SourceIssueProfile || eff.ProfileName != "深度" || eff.MaxRounds != 60 {
		t.Fatalf("issue profile resolution = %+v", eff)
	}

	// Editing the project default must not affect the issue override.
	if _, err := svc.UpdateSettings(ctx, f.projectID, 1, CompletionSettingsInput{MaxRounds: ptr(int64(9))}); err != nil {
		t.Fatal(err)
	}
	eff, _ = svc.ResolveIssuePolicy(ctx, f.issueID)
	if eff.MaxRounds != 60 || eff.Source != completion.SourceIssueProfile {
		t.Fatalf("override disturbed by project change: %+v", eff)
	}

	// A referenced profile cannot be deleted.
	if err := svc.DeleteProfile(ctx, f.projectID, deep.ID, 1); !errors.Is(err, ErrCompletionProfileInUse) {
		t.Fatalf("delete referenced profile err = %v", err)
	}

	// Clearing the override falls back to the (new) project default.
	if _, err := svc.SetIssueProfile(ctx, f.issueID, 1, nil); err != nil {
		t.Fatalf("clear profile: %v", err)
	}
	eff, _ = svc.ResolveIssuePolicy(ctx, f.issueID)
	if eff.Source != completion.SourceProjectDefault || eff.MaxRounds != 9 {
		t.Fatalf("inherit resolution = %+v", eff)
	}

	// Intake and completion are independent: changing intake settings leaves the
	// completion resolution untouched (§10.1).
	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot, Mode: "observe", AllowList: []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("intake settings change: %v", err)
	}
	eff, _ = svc.ResolveIssuePolicy(ctx, f.issueID)
	if eff.MaxRounds != 9 || eff.Source != completion.SourceProjectDefault {
		t.Fatalf("intake change disturbed completion: %+v", eff)
	}
}

// newConcurrentIMBotFixture uses a file-backed database with several
// connections so the daily-cap test really races the reserve (an in-memory
// database pinned to one connection would serialize every writer).
func newConcurrentIMBotFixture(t *testing.T, maxConns int) *imbotFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "e2e-intake.db")
	// modernc ignores the mattn-style _journal_mode/_busy_timeout params; the
	// _pragma form plus _txlock=immediate is what store.Open uses in production.
	db, err := sql.Open("sqlite", path+
		"?_pragma=journal_mode(WAL)"+
		"&_pragma=busy_timeout(5000)"+
		"&_pragma=synchronous(NORMAL)"+
		"&_pragma=foreign_keys(1)"+
		"&_txlock=immediate")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(maxConns)
	t.Cleanup(func() { _ = db.Close() })
	return seedIMBotFixture(t, db)
}

// TestE2E_ConcurrentInferredEventsRespectDailyCap drives the real IM pipeline
// with 20 simultaneous inferred events (design §10.2 / #761). The atomic
// daily-slot claim must let exactly cap events execute and record the rest as
// daily_cap — no count-then-execute overshoot.
func TestE2E_ConcurrentInferredEventsRespectDailyCap(t *testing.T) {
	const (
		events = 20
		capN   = int64(2)
	)
	f := newConcurrentIMBotFixture(t, 8)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_cap")
	f.setChatAgentMode(t, chat, AgentModeObserve)
	f.setChatIntakeMode(t, chat, IntakeModeAuto)

	intakeSvc := NewIntakeService(f.db, f.q)
	if _, err := intakeSvc.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       string(intake.ModeAuto),
			DailyCap:   ptr(capN),
			// An allow list keeps "bob" off the explicit path, so these events are
			// inferred and therefore subject to the cap.
			AllowList: []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, err := intakeSvc.CreateRule(ctx, f.projectID, UpsertIntakeRuleInput{
		SourceKind: intake.SourceKindIMBot,
		Name:       "部署",
		Conditions: intake.RuleConditions{Keywords: []string{"deploy"}},
	}); err != nil {
		t.Fatalf("rule: %v", err)
	}
	for i := 0; i < events; i++ {
		f.router.queue = append(f.router.queue, PlanTarget{
			IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
		})
	}

	var wg sync.WaitGroup
	for i := 0; i < events; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f.svc.HandleInbound(ctx, imbot.InboundEvent{
				ChannelID: f.channelID, ChatExtID: "oc_cap", Text: "please deploy now",
				Kind: "message", EventID: fmt.Sprintf("cap-%d", i), ActorExtID: "bob",
			})
		}(i)
	}
	wg.Wait()

	rows, err := f.db.QueryContext(ctx,
		`SELECT decision, reason_code, executed FROM intake_decisions WHERE project_id = ?`, f.projectID)
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	defer rows.Close()
	executed, capped := 0, 0
	for rows.Next() {
		var decision, reason string
		var isExec int
		if err := rows.Scan(&decision, &reason, &isExec); err != nil {
			t.Fatalf("scan decision: %v", err)
		}
		if isExec == 1 {
			if decision != string(intake.DecisionAccept) && decision != string(intake.DecisionResume) {
				t.Fatalf("unexpected executed decision %q", decision)
			}
			executed++
		}
		if reason == intake.ReasonDailyCap {
			capped++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if executed != int(capN) {
		t.Fatalf("executed = %d, want exactly %d", executed, capN)
	}
	if capped != events-int(capN) {
		t.Fatalf("daily_cap decisions = %d, want %d", capped, events-int(capN))
	}
	if len(f.deliverer.calls) != int(capN) {
		t.Fatalf("deliveries = %d, want %d", len(f.deliverer.calls), capN)
	}
}

// TestE2E_BotIsInferredAndCannotStopTask pins the absolute bot rule (§10.4 /
// design §6.2): a bot on the bot list is inferred even when it is the source of
// an admitted message, and it can never stop a running task.
func TestE2E_BotIsInferredAndCannotStopTask(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_bot")
	f.setChatAgentMode(t, chat, AgentModeObserve)
	f.setChatIntakeMode(t, chat, IntakeModeAuto)
	f.svc.setActiveIssue(ctx, chat, f.issueID)

	intakeSvc := NewIntakeService(f.db, f.q)
	if _, err := intakeSvc.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       string(intake.ModeAuto),
			AllowList:  []string{"alice"},
			BotList:    []string{"bot_release"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, err := intakeSvc.CreateRule(ctx, f.projectID, UpsertIntakeRuleInput{
		SourceKind: intake.SourceKindIMBot,
		Name:       "部署",
		Conditions: intake.RuleConditions{Keywords: []string{"deploy"}},
	}); err != nil {
		t.Fatalf("rule: %v", err)
	}
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}

	// A matching bot message is inferred — never explicit — and can execute.
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_bot", Text: "please deploy",
		Kind: "message", EventID: "bot-1", ActorExtID: "bot_release",
	})
	ev := f.intakeEvent(t, "oc_bot", "bot-1")
	decs, err := f.q.ListIntakeDecisionsByEvent(ctx, ev.ID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("decisions = %v (err %v)", decs, err)
	}
	if decs[0].Strength != string(intake.StrengthInferred) {
		t.Fatalf("bot strength = %q, want inferred", decs[0].Strength)
	}

	// /stop from the same bot is denied even though it is on the bot list.
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_bot", Text: "/stop",
		Kind: "message", EventID: "bot-2", ActorExtID: "bot_release",
	})
	f.deliverer.mu.Lock()
	stopped := append([]int64(nil), f.deliverer.stopped...)
	f.deliverer.mu.Unlock()
	if len(stopped) != 0 {
		t.Fatalf("bot stopped the task: %+v", stopped)
	}
}

// TestE2E_TaskBriefAcceptanceRowReplacement pins the acceptance "refresh"
// contract autohost relies on: the newest brief's acceptance list replaces the
// old acceptance rows, while a hand-written task row keeps its completion state.
func TestE2E_TaskBriefAcceptanceRowReplacement(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	briefs := NewTaskBriefService(f.q, store.Wrap(f.db))

	if _, err := briefs.PutIssueBrief(ctx, f.issueID, TaskBriefInput{
		Goal: "第一版目标", Acceptance: []string{"旧验收 A", "旧验收 B"},
	}, 1); err != nil {
		t.Fatalf("v1: %v", err)
	}
	manual, err := f.q.CreateChecklist(ctx, store.CreateChecklistParams{
		IssueID: f.issueID, Title: "手工任务", IssueID_2: f.issueID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.UpdateChecklist(ctx, store.UpdateChecklistParams{
		Title: "手工任务", IsCompleted: 1, ID: manual.ID,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := briefs.PutIssueBrief(ctx, f.issueID, TaskBriefInput{
		Goal: "第二版目标", Acceptance: []string{"新验收"},
	}, 1); err != nil {
		t.Fatalf("v2: %v", err)
	}

	acceptance, err := f.q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: f.issueID, Kind: "acceptance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(acceptance) != 1 || acceptance[0].Title != "新验收" {
		t.Fatalf("acceptance after v2 = %+v", acceptance)
	}
	manualRows, err := f.q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: f.issueID, Kind: "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	var kept *store.IssueChecklist
	for i := range manualRows {
		if manualRows[i].ID == manual.ID {
			kept = &manualRows[i]
		}
	}
	if kept == nil || kept.IsCompleted != 1 {
		t.Fatalf("manual row not preserved with its state: %+v", manualRows)
	}
	issue, err := f.q.GetIssue(ctx, f.issueID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.GoalCondition != "第二版目标\n\n验收标准（全部满足才算完成）：\n- 新验收" {
		t.Fatalf("goal_condition = %q", issue.GoalCondition)
	}
}
