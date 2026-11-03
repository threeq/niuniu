package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func githubTestTrigger(projectID int64) intake.Trigger {
	return intake.Trigger{
		ProjectID:  projectID,
		SourceKind: intake.SourceKindGitHub,
		SourceRef:  "acme/foo",
		EventID:    "issue_comment:1",
		EventKind:  intake.EventKindIssueComment,
		Thread:     intake.ThreadRef{ThreadExtID: "42"},
		Actor:      intake.ActorRef{ExtID: "alice", Name: "alice"},
		Text:       "please fix the build",
		SourceURL:  "https://github.com/acme/foo/pull/42#issuecomment-1",
		ReceivedAt: time.Now(),
		ReceiveAll: true,
	}
}

// TestGithubSource_DuplicateDeliveryOnlyOnce proves the poller's overlapping
// since window cannot create two tasks: the shared Intake pipeline records the
// event once and the second delivery is a duplicate no-op.
func TestGithubSource_DuplicateDeliveryOnlyOnce(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()

	settings := NewIntakeService(f.db, f.q)
	if _, err := settings.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindGitHub,
			Mode:       "off",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}

	svc := NewGithubSourceService(f.db, f.q, nil)
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	svc.SetInbound(f.router, f.deliverer)

	trig := githubTestTrigger(f.projectID)
	first, err := svc.intake.Handle(ctx, trig)
	if err != nil {
		t.Fatalf("first handle: %v", err)
	}
	if !first.Executed || first.Decision != intake.DecisionAccept {
		t.Fatalf("first result = %+v", first)
	}
	second, err := svc.intake.Handle(ctx, trig)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("second delivery was not deduped: %+v", second)
	}
	if f.router.calls != 1 {
		t.Fatalf("router calls = %d, want 1", f.router.calls)
	}

	var events, decisions int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM intake_events WHERE source_kind = 'github'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM intake_decisions WHERE project_id = ?`, f.projectID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if events != 1 || decisions != 1 {
		t.Fatalf("events=%d decisions=%d, want 1/1", events, decisions)
	}

	// Provenance is written on the created task.
	var source, extID, externalURL string
	if err := f.db.QueryRow(
		`SELECT external_source, external_id, external_url FROM issues WHERE id = ?`, f.issueID,
	).Scan(&source, &extID, &externalURL); err != nil {
		t.Fatal(err)
	}
	if source != "github" || extID != "acme/foo#42" || externalURL != trig.SourceURL {
		t.Fatalf("provenance = %q/%q/%q", source, extID, externalURL)
	}

	// The event body and its upstream link were delivered to the workspace.
	if len(f.deliverer.calls) != 1 {
		t.Fatalf("deliveries = %d", len(f.deliverer.calls))
	}
	if !strings.Contains(f.deliverer.calls[0].content, "please fix the build") ||
		!strings.Contains(f.deliverer.calls[0].content, trig.SourceURL) {
		t.Fatalf("delivered content = %q", f.deliverer.calls[0].content)
	}
}

// TestGithubResolver_ResumesBoundThread proves a second event on the same PR
// resolves back to the existing task/workspace instead of opening a new one.
func TestGithubResolver_ResumesBoundThread(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(
		`UPDATE issues SET external_source = 'github', external_id = 'acme/foo#42' WHERE id = ?`, f.issueID,
	); err != nil {
		t.Fatal(err)
	}

	resolver := githubResolver{db: store.Wrap(f.db)}
	trig := githubTestTrigger(f.projectID)
	res, err := resolver.Resolve(ctx, trig)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.Bound || res.IssueID != f.issueID || res.WorkspaceID != f.wsID {
		t.Fatalf("resolution = %+v", res)
	}

	trig.Thread.ThreadExtID = "99"
	res, err = resolver.Resolve(ctx, trig)
	if err != nil {
		t.Fatalf("resolve unbound: %v", err)
	}
	if res.Bound || res.IssueID != 0 {
		t.Fatalf("unbound resolution = %+v", res)
	}
}

// TestGithubSource_ConfigEvents covers the repo/event configuration surface:
// repo bindings come from project_external_sources, event selection round-trips,
// and an unbound repo is rejected.
func TestGithubSource_ConfigEvents(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	if _, err := f.q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    f.projectID,
		Provider:     "github",
		SourceKey:    "acme/foo",
		CredentialID: sql.NullInt64{},
		Config:       "{}",
	}); err != nil {
		t.Fatalf("bind repo: %v", err)
	}

	svc := NewGithubSourceService(f.db, f.q, nil)
	view, err := svc.ListGitHubConfig(ctx, f.projectID)
	if err != nil {
		t.Fatalf("list config: %v", err)
	}
	if len(view.Repos) != 1 || view.Repos[0].SourceKey != "acme/foo" {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Repos[0].Events) != len(githubsource.KnownEvents) {
		t.Fatalf("default events = %v", view.Repos[0].Events)
	}

	want := []string{githubsource.EventIssueComment, githubsource.EventCheckRun}
	view, err = svc.UpdateGitHubConfig(ctx, f.projectID, UpdateGithubConfigRequest{
		Repos: []GithubRepoEventsInput{{SourceKey: "acme/foo", Events: want}},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if got := view.Repos[0].Events; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %v", got)
	}

	bindings, err := svc.repoBindings(ctx)
	if err != nil {
		t.Fatalf("bindings: %v", err)
	}
	if len(bindings) != 1 || len(bindings[0].cfg.Events) != 2 {
		t.Fatalf("bindings = %+v", bindings)
	}

	if _, err := svc.UpdateGitHubConfig(ctx, f.projectID, UpdateGithubConfigRequest{
		Repos: []GithubRepoEventsInput{{SourceKey: "acme/other", Events: want}},
	}); err == nil {
		t.Fatal("expected an unbound repo to be rejected")
	}
	if _, err := svc.UpdateGitHubConfig(ctx, f.projectID, UpdateGithubConfigRequest{
		Repos: []GithubRepoEventsInput{{SourceKey: "acme/foo", Events: []string{"not_an_event"}}},
	}); err == nil {
		t.Fatal("expected an empty event selection to be rejected")
	}
}
