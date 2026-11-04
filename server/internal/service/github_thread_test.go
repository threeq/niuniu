package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/intake"
)

// newGithubThreadFixture wires a GitHub source service over the shared IM
// fixture DB/project. The actor is allow-listed in mode off, so comments are
// explicit requests that execute without the auto-mode calibration gate.
func newGithubThreadFixture(t *testing.T) (*imbotFixture, *GithubSourceService, githubIntakeSink) {
	t.Helper()
	f := newIMBotFixture(t)
	settings := NewIntakeService(f.db, f.q)
	if _, err := settings.UpdateSettings(context.Background(), f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindGitHub,
			Mode:       "off",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	svc := NewGithubSourceService(f.db, f.q, nil)
	svc.SetInbound(f.router, f.deliverer)
	return f, svc, githubIntakeSink{svc: svc}
}

func closedTrigger(base intake.Trigger, number, state string) intake.Trigger {
	trig := base
	trig.Thread.ThreadExtID = number
	trig.EventKind = intake.EventKindPullRequestClosed
	trig.EventID = "pull_request_closed:" + number + ":1700000000"
	trig.Raw = json.RawMessage(fmt.Sprintf(`{"number":%s,"state":%q}`, number, state))
	return trig
}

// TestGithubThreadBinding_Lifecycle covers the full subscription: an unbound PR
// comment creates a task and binds the thread; a follow-up resumes that exact
// workspace without re-triage; close/merge ends the subscription and a later
// comment is recorded but never executed.
func TestGithubThreadBinding_Lifecycle(t *testing.T) {
	f, svc, sink := newGithubThreadFixture(t)
	ctx := context.Background()
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})

	first := githubTestTrigger(f.projectID)
	if err := sink.Handle(ctx, first); err != nil {
		t.Fatalf("handle first: %v", err)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("first comment: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	binding, ok, err := svc.threads.Get(ctx, f.projectID, "acme/foo", "42")
	if err != nil || !ok {
		t.Fatalf("binding: ok=%v err=%v", ok, err)
	}
	if binding.State != githubThreadOpen || binding.IssueID != f.issueID ||
		binding.WorkspaceID != f.wsID || binding.LastEventID != first.EventID {
		t.Fatalf("binding = %+v", binding)
	}

	second := first
	second.EventID = "issue_comment:2"
	second.Text = "follow-up please"
	if err := sink.Handle(ctx, second); err != nil {
		t.Fatalf("handle follow-up: %v", err)
	}
	if f.router.calls != 1 {
		t.Fatalf("follow-up re-triaged: router calls = %d", f.router.calls)
	}
	if len(f.deliverer.calls) != 2 || f.deliverer.calls[1].workspaceID != f.wsID {
		t.Fatalf("follow-up deliveries = %+v", f.deliverer.calls)
	}
	binding, _, err = svc.threads.Get(ctx, f.projectID, "acme/foo", "42")
	if err != nil {
		t.Fatal(err)
	}
	if binding.LastEventID != second.EventID {
		t.Fatalf("read position = %q, want %q", binding.LastEventID, second.EventID)
	}

	// Merged PR -> subscription ends; nothing is created or delivered.
	closed := closedTrigger(first, "42", "merged")
	if err := sink.Handle(ctx, closed); err != nil {
		t.Fatalf("handle closed: %v", err)
	}
	binding, _, err = svc.threads.Get(ctx, f.projectID, "acme/foo", "42")
	if err != nil {
		t.Fatal(err)
	}
	if binding.State != githubThreadEnded || binding.EndedReason != "merged" {
		t.Fatalf("closed binding = %+v", binding)
	}
	if binding.IssueID != f.issueID {
		t.Fatalf("close lost the task binding: %+v", binding)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 2 {
		t.Fatalf("close created work: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}

	// A comment after close is recorded (audit) but never resumes the task.
	third := first
	third.EventID = "issue_comment:3"
	res, err := svc.intake.Handle(ctx, third)
	if err != nil {
		t.Fatalf("handle post-close: %v", err)
	}
	if res.Decision != intake.DecisionIgnore || res.ReasonCode != intake.ReasonThreadEnded || res.Executed {
		t.Fatalf("post-close result = %+v", res)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 2 {
		t.Fatalf("post-close executed work: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
}

// TestGithubThreadBinding_CloseBeforeAnyTask proves a close event for a PR that
// never produced a task writes a tombstone, so the thread cannot be picked up
// afterwards.
func TestGithubThreadBinding_CloseBeforeAnyTask(t *testing.T) {
	f, svc, sink := newGithubThreadFixture(t)
	ctx := context.Background()

	closed := closedTrigger(githubTestTrigger(f.projectID), "77", "closed")
	if err := sink.Handle(ctx, closed); err != nil {
		t.Fatalf("handle closed: %v", err)
	}
	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("unbound close executed work: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	binding, ok, err := svc.threads.Get(ctx, f.projectID, "acme/foo", "77")
	if err != nil || !ok {
		t.Fatalf("tombstone: ok=%v err=%v", ok, err)
	}
	if binding.State != githubThreadEnded || binding.IssueID != 0 || binding.EndedReason != "closed" {
		t.Fatalf("tombstone = %+v", binding)
	}

	comment := githubTestTrigger(f.projectID)
	comment.Thread.ThreadExtID = "77"
	comment.EventID = "issue_comment:9"
	res, err := svc.intake.Handle(ctx, comment)
	if err != nil {
		t.Fatalf("handle late comment: %v", err)
	}
	if res.Decision != intake.DecisionIgnore || res.ReasonCode != intake.ReasonThreadEnded {
		t.Fatalf("late comment result = %+v", res)
	}
	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("late comment created work: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
}

// TestGithubThreadBinding_RestartNoReplay rebuilds the service over the same
// database and re-delivers an already-handled event: the persisted event
// dedupe makes it a no-op and the binding (including its read position)
// survives.
func TestGithubThreadBinding_RestartNoReplay(t *testing.T) {
	f, _, sink := newGithubThreadFixture(t)
	ctx := context.Background()
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})

	first := githubTestTrigger(f.projectID)
	if err := sink.Handle(ctx, first); err != nil {
		t.Fatalf("handle first: %v", err)
	}

	restarted := NewGithubSourceService(f.db, f.q, nil)
	router := &fakeRouter{q: f.q}
	deliverer := &fakeDeliverer{}
	restarted.SetInbound(router, deliverer)

	res, err := restarted.intake.Handle(ctx, first)
	if err != nil {
		t.Fatalf("handle after restart: %v", err)
	}
	if !res.Duplicate {
		t.Fatalf("restart replayed the event: %+v", res)
	}
	if router.calls != 0 || len(deliverer.calls) != 0 {
		t.Fatalf("restart executed work: router=%d deliveries=%d", router.calls, len(deliverer.calls))
	}

	binding, ok, err := restarted.threads.Get(ctx, f.projectID, "acme/foo", "42")
	if err != nil || !ok {
		t.Fatalf("binding after restart: ok=%v err=%v", ok, err)
	}
	if binding.IssueID != f.issueID || binding.WorkspaceID != f.wsID || binding.LastEventID != first.EventID {
		t.Fatalf("binding after restart = %+v", binding)
	}
}

// TestGithubThreadBinding_PollerE2E drives the real poller (HTTP -> normalize ->
// sink -> intake) over an httptest GitHub API, then restarts the service. The
// second poll re-reads the same comment inside the poller's overlap window; the
// persisted event dedupe and poll cursor must keep it from creating a second
// task.
func TestGithubThreadBinding_PollerE2E(t *testing.T) {
	f, svc, _ := newGithubThreadFixture(t)
	ctx := context.Background()

	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	commentTime := now.Add(-time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/foo/issues/comments" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, "["+issueCommentBody(1, 42, "please fix the build", commentTime)+"]")
	}))
	defer srv.Close()

	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	cfg := githubsource.RepoConfig{
		ProjectID: f.projectID, Repo: "acme/foo",
		Events: []string{githubsource.EventIssueComment},
	}
	svc.SetPollerBaseURL(srv.URL)
	svc.poller.Clock = func() time.Time { return now }
	svc.poller.InitialLookback = time.Hour
	if err := svc.poller.PollRepo(ctx, cfg); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("first poll: router=%d deliveries=%d", f.router.calls, len(f.deliverer.calls))
	}
	binding, ok, err := svc.threads.Get(ctx, f.projectID, "acme/foo", "42")
	if err != nil || !ok || binding.IssueID != f.issueID {
		t.Fatalf("binding after poll: ok=%v err=%v %+v", ok, err, binding)
	}

	// Restart: new service, same DB (poll cursor + intake dedupe persisted).
	restarted := NewGithubSourceService(f.db, f.q, nil)
	router := &fakeRouter{q: f.q}
	deliverer := &fakeDeliverer{}
	restarted.SetInbound(router, deliverer)
	restarted.SetPollerBaseURL(srv.URL)
	restarted.poller.Clock = func() time.Time { return now }
	if err := restarted.poller.PollRepo(ctx, cfg); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if router.calls != 0 || len(deliverer.calls) != 0 {
		t.Fatalf("restart replayed: router=%d deliveries=%d", router.calls, len(deliverer.calls))
	}
}

func issueCommentBody(id int64, number int, body string, updated time.Time) string {
	return fmt.Sprintf(`{"id":%d,"body":%q,"issue_url":"https://api.github.com/repos/acme/foo/issues/%d","html_url":"https://github.com/acme/foo/pull/%d#issuecomment-%d","updated_at":%q,"created_at":%q,"user":{"login":"alice","type":"User"}}`,
		id, body, number, number, id, updated.Format(time.RFC3339), updated.Format(time.RFC3339))
}
