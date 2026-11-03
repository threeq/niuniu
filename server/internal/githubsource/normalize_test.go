package githubsource

import (
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

func TestNormalizeIssueComment(t *testing.T) {
	trig, ok := NormalizeIssueComment(7, "acme/foo", issueComment{
		ID:       11,
		Body:     "  please fix the build  ",
		IssueURL: "https://api.github.com/repos/acme/foo/issues/42",
		HTMLURL:  "https://github.com/acme/foo/pull/42#issuecomment-11",
		User:     user{Login: "alice", Type: "User"},
	})
	if !ok {
		t.Fatal("expected a trigger")
	}
	if trig.EventID != "issue_comment:11" || trig.EventKind != intake.EventKindIssueComment {
		t.Fatalf("unexpected identity: %+v", trig)
	}
	if trig.Thread.ThreadExtID != "42" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.SourceKind != intake.SourceKindGitHub || trig.SourceRef != "acme/foo" {
		t.Fatalf("source = %+v", trig)
	}
	if trig.Text != "please fix the build" {
		t.Fatalf("text = %q", trig.Text)
	}
	if trig.Actor.ExtID != "alice" || trig.Actor.IsBot {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if !trig.ReceiveAll || trig.Explicit {
		t.Fatalf("polled events must be receive-all, not source-admitted: %+v", trig)
	}
	if trig.SourceURL == "" || len(trig.Raw) == 0 {
		t.Fatalf("missing provenance")
	}
}

func TestNormalizeIssueComment_Bot(t *testing.T) {
	trig, ok := NormalizeIssueComment(1, "acme/foo", issueComment{
		ID: 1, Body: "x", IssueURL: "https://api.github.com/repos/acme/foo/issues/3",
		User: user{Login: "dependabot[bot]", Type: "Bot"},
	})
	if !ok || !trig.Actor.IsBot {
		t.Fatalf("bot not recognized: %+v", trig.Actor)
	}
}

func TestNormalizeReview_SkipsPending(t *testing.T) {
	if _, ok := NormalizeReview(1, "acme/foo", pullRequestReview{
		ID: 5, State: "PENDING", PullRequestURL: "https://api.github.com/repos/acme/foo/pulls/9",
	}); ok {
		t.Fatal("pending review must be skipped")
	}
	submitted := time.Now()
	trig, ok := NormalizeReview(1, "acme/foo", pullRequestReview{
		ID: 5, State: "APPROVED", Body: "lgtm",
		SubmittedAt:    &submitted,
		PullRequestURL: "https://api.github.com/repos/acme/foo/pulls/9",
		HTMLURL:        "https://github.com/acme/foo/pull/9#pullrequestreview-5",
		User:           user{Login: "bob"},
	})
	if !ok || trig.EventKind != intake.EventKindReview || trig.Thread.ThreadExtID != "9" {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
	if trig.Text != "lgtm" || trig.EventID != "pull_request_review:5" {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
}

func TestNormalizeReviewComment(t *testing.T) {
	trig, ok := NormalizeReviewComment(1, "acme/foo", pullRequestReviewComment{
		ID: 6, Body: "nit", Path: "a.go",
		PullRequestURL: "https://api.github.com/repos/acme/foo/pulls/12",
		User:           user{Login: "carol"},
	})
	if !ok || trig.EventKind != intake.EventKindReviewComment || trig.Thread.ThreadExtID != "12" {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
}

func TestNormalizeCheckRun_OnlyCompleted(t *testing.T) {
	running := checkRun{ID: 1, Conclusion: "", Status: "in_progress"}
	if _, ok := NormalizeCheckRun(1, "acme/foo", running); ok {
		t.Fatal("in-flight check run must be skipped")
	}
	done := time.Now()
	run := checkRun{
		ID: 2, Name: "ci", Conclusion: "success", Status: "completed",
		HeadSHA: "abcdef1234567", CompletedAt: &done,
		CheckSuite: struct {
			PullRequests []pullRef `json:"pull_requests"`
		}{PullRequests: []pullRef{{Number: 44}}},
	}
	trig, ok := NormalizeCheckRun(1, "acme/foo", run)
	if !ok || trig.Thread.ThreadExtID != "44" || trig.EventKind != intake.EventKindCheckRun {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
	if !strings.Contains(trig.Text, "success") || !strings.Contains(trig.Text, "abcdef1") {
		t.Fatalf("text = %q", trig.Text)
	}
	if !trig.Actor.IsBot {
		t.Fatalf("check runs are produced by apps (bots): %+v", trig.Actor)
	}
}

func TestNormalizeWorkflowRun(t *testing.T) {
	pending := workflowRun{ID: 1, Status: "in_progress"}
	if _, ok := NormalizeWorkflowRun(1, "acme/foo", pending); ok {
		t.Fatal("in-flight workflow run must be skipped")
	}
	trig, ok := NormalizeWorkflowRun(1, "acme/foo", workflowRun{
		ID: 2, Name: "build", Status: "completed", Conclusion: "failure",
		HeadBranch: "main", Actor: &user{Login: "alice"},
	})
	if !ok || trig.EventKind != intake.EventKindWorkflowRun || trig.Thread.ThreadExtID != "" {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
	if !strings.Contains(trig.Text, "failure") || !strings.Contains(trig.Text, "main") {
		t.Fatalf("text = %q", trig.Text)
	}
}

func TestNormalizeClosedPullRequest_Merged(t *testing.T) {
	closed := time.Unix(1_700_000_000, 0)
	trig, ok := NormalizeClosedPullRequest(1, "acme/foo", pullRequest{
		Number: 8, Title: "Add feature", State: "closed",
		Merged: true, MergedAt: &closed, ClosedAt: &closed,
		User: user{Login: "dave"},
	})
	if !ok || trig.EventKind != intake.EventKindPullRequestClosed || trig.Thread.ThreadExtID != "8" {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
	if !strings.Contains(trig.Text, "merged") {
		t.Fatalf("text = %q", trig.Text)
	}
	if trig.EventID != "pull_request_closed:8:1700000000" {
		t.Fatalf("event id = %q", trig.EventID)
	}
}

func TestNormalizeSearchClosedPullRequest(t *testing.T) {
	closed := time.Unix(1_700_000_100, 0)
	it := searchIssue{
		Number: 9, Title: "Fix", State: "closed", ClosedAt: &closed,
		Pull: &searchPull{},
	}
	trig, ok := NormalizeSearchClosedPullRequest(1, "acme/foo", it)
	if !ok || trig.Thread.ThreadExtID != "9" || !strings.Contains(trig.Text, "closed") {
		t.Fatalf("unexpected trigger: %+v", trig)
	}
}

func TestNumberFromURL(t *testing.T) {
	cases := map[string]struct {
		num int
		ok  bool
	}{
		"https://api.github.com/repos/o/r/issues/42":           {42, true},
		"https://api.github.com/repos/o/r/pulls/7":             {7, true},
		"https://api.github.com/repos/o/r/issues/42/comments":  {42, true},
		"https://api.github.com/repos/o/r/issues/comments/999": {0, false},
		"": {0, false},
	}
	for raw, want := range cases {
		n, ok := NumberFromURL(raw, "/issues/")
		if raw == "https://api.github.com/repos/o/r/pulls/7" {
			n, ok = NumberFromURL(raw, "/pulls/")
		}
		if raw == "https://api.github.com/repos/o/r/issues/comments/999" {
			n, ok = NumberFromURL(raw, "/issues/")
		}
		if n != want.num || ok != want.ok {
			t.Fatalf("%q -> (%d,%v), want (%d,%v)", raw, n, ok, want.num, want.ok)
		}
	}
}

func TestEnabledEvents(t *testing.T) {
	got := EnabledEvents([]string{EventCheckRun, "bogus", EventIssueComment, EventCheckRun})
	if len(got) != 2 || got[0] != EventIssueComment || got[1] != EventCheckRun {
		t.Fatalf("enabled events = %v", got)
	}
	if all := EnabledEvents(nil); len(all) != len(KnownEvents) {
		t.Fatalf("empty selection should mean all, got %v", all)
	}
}
