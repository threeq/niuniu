package githubsource

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func sign(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSignatureValid(t *testing.T) {
	body := []byte(`{"action":"created"}`)
	good := sign(t, "top-secret", body)

	cases := []struct {
		name   string
		secret string
		header string
		want   bool
	}{
		{"valid", "top-secret", good, true},
		{"wrong secret", "other", good, false},
		{"missing header", "top-secret", "", false},
		{"empty secret", "", good, false},
		{"sha1 shape", "top-secret", "sha1=abcd", false},
		{"garbage hex", "top-secret", "sha256=nothex", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SignatureValid(tc.secret, body, tc.header); got != tc.want {
				t.Fatalf("SignatureValid = %v, want %v", got, tc.want)
			}
		})
	}
	if SignatureValid("top-secret", []byte(`{"action":"deleted"}`), good) {
		t.Fatal("a signature for different bytes must not validate")
	}
}

func TestWebhookEventKind(t *testing.T) {
	want := map[string]string{
		"issue_comment":               EventIssueComment,
		"pull_request":                EventPullRequestClosed,
		"pull_request_review":         EventReview,
		"pull_request_review_comment": EventReviewComment,
		"check_run":                   EventCheckRun,
		"workflow_run":                EventWorkflowRun,
	}
	for eventType, kind := range want {
		got, ok := WebhookEventKind(eventType)
		if !ok || got != kind {
			t.Fatalf("WebhookEventKind(%q) = %q/%v, want %q", eventType, got, ok, kind)
		}
	}
	if _, ok := WebhookEventKind("push"); ok {
		t.Fatal("push must not be a handled webhook event")
	}
}

func TestNormalizeWebhookIssueCommentMatchesPolling(t *testing.T) {
	body := []byte(`{
	  "action":"created",
	  "issue":{"number":42},
	  "comment":{
	    "id":1001,"body":"please fix the build",
	    "html_url":"https://github.com/acme/foo/pull/42#issuecomment-1001",
	    "issue_url":"https://api.github.com/repos/acme/foo/issues/42",
	    "created_at":"2026-11-04T00:00:00Z","updated_at":"2026-11-04T00:00:00Z",
	    "user":{"login":"alice","type":"User"}
	  }
	}`)
	webhook, ok, err := NormalizeWebhook(7, "acme/foo", "issue_comment", body)
	if err != nil || !ok {
		t.Fatalf("normalize: ok=%v err=%v", ok, err)
	}

	// The polling adapter's Trigger for the same upstream comment.
	polled, pok := NormalizeIssueComment(7, "acme/foo", issueComment{
		ID: 1001, Body: "please fix the build",
		HTMLURL:  "https://github.com/acme/foo/pull/42#issuecomment-1001",
		IssueURL: "https://api.github.com/repos/acme/foo/issues/42",
		User:     user{Login: "alice", Type: "User"},
	})
	if !pok {
		t.Fatal("polling normalization rejected the comment")
	}

	if webhook.EventID != polled.EventID || webhook.EventID != "issue_comment:1001" {
		t.Fatalf("event id: webhook=%q polling=%q", webhook.EventID, polled.EventID)
	}
	if webhook.SourceKind != polled.SourceKind || webhook.SourceRef != polled.SourceRef ||
		webhook.EventKind != polled.EventKind || webhook.Thread != polled.Thread ||
		webhook.Actor != polled.Actor || webhook.Text != polled.Text ||
		webhook.SourceURL != polled.SourceURL || webhook.ReceiveAll != polled.ReceiveAll {
		t.Fatalf("webhook trigger != polling trigger\nwebhook=%+v\npolling=%+v", webhook, polled)
	}
}

func TestNormalizeWebhookReviewAndComment(t *testing.T) {
	review := []byte(`{
	  "action":"submitted",
	  "review":{"id":7,"body":"LGTM","state":"approved","html_url":"https://github.com/acme/foo/pull/42#pullrequestreview-7",
	    "pull_request_url":"https://api.github.com/repos/acme/foo/pulls/42",
	    "submitted_at":"2026-11-04T00:00:00Z","user":{"login":"bob","type":"User"}}
	}`)
	trig, ok, err := NormalizeWebhook(7, "acme/foo", "pull_request_review", review)
	if err != nil || !ok {
		t.Fatalf("review: ok=%v err=%v", ok, err)
	}
	if trig.EventID != "pull_request_review:7" || trig.Thread.ThreadExtID != "42" || trig.EventKind != EventReview {
		t.Fatalf("review trigger = %+v", trig)
	}
	if _, ok, _ := NormalizeWebhook(7, "acme/foo", "pull_request_review", []byte(`{"action":"dismissed"}`)); ok {
		t.Fatal("a dismissed review must not produce a trigger")
	}

	comment := []byte(`{
	  "action":"created",
	  "comment":{"id":9,"body":"nit","html_url":"https://github.com/acme/foo/pull/42#discussion_r9",
	    "pull_request_url":"https://api.github.com/repos/acme/foo/pulls/42","path":"a.go","line":3,
	    "created_at":"2026-11-04T00:00:00Z","updated_at":"2026-11-04T00:00:00Z",
	    "user":{"login":"bob","type":"User"}}
	}`)
	trig, ok, err = NormalizeWebhook(7, "acme/foo", "pull_request_review_comment", comment)
	if err != nil || !ok {
		t.Fatalf("review comment: ok=%v err=%v", ok, err)
	}
	if trig.EventID != "pull_request_review_comment:9" || trig.Thread.ThreadExtID != "42" {
		t.Fatalf("review comment trigger = %+v", trig)
	}
	if _, ok, _ := NormalizeWebhook(7, "acme/foo", "pull_request_review_comment",
		[]byte(`{"action":"deleted","comment":{"id":9}}`)); ok {
		t.Fatal("a deleted review comment must not produce a trigger")
	}
}

func TestNormalizeWebhookCheckRunAndWorkflowRun(t *testing.T) {
	check := []byte(`{
	  "action":"completed",
	  "check_run":{"id":5,"name":"ci","status":"completed","conclusion":"success",
	    "html_url":"https://github.com/acme/foo/runs/5","head_sha":"abcdef1234567",
	    "completed_at":"2026-11-04T00:00:00Z",
	    "app":{"name":"GitHub Actions","slug":"github-actions"},
	    "check_suite":{"pull_requests":[{"number":42}]}}
	}`)
	trig, ok, err := NormalizeWebhook(7, "acme/foo", "check_run", check)
	if err != nil || !ok {
		t.Fatalf("check run: ok=%v err=%v", ok, err)
	}
	if trig.EventID != "check_run:5" || trig.Thread.ThreadExtID != "42" || trig.EventKind != EventCheckRun {
		t.Fatalf("check run trigger = %+v", trig)
	}
	if !trig.Actor.IsBot || trig.Actor.ExtID != "github-actions[bot]" {
		t.Fatalf("check run actor = %+v", trig.Actor)
	}
	if _, ok, _ := NormalizeWebhook(7, "acme/foo", "check_run",
		[]byte(`{"action":"in_progress","check_run":{"id":5}}`)); ok {
		t.Fatal("an in-flight check run must not produce a trigger")
	}

	workflow := []byte(`{
	  "action":"completed",
	  "workflow_run":{"id":11,"name":"build","event":"push","status":"completed","conclusion":"failure",
	    "head_branch":"main","head_sha":"abcdef1","html_url":"https://github.com/acme/foo/actions/runs/11",
	    "created_at":"2026-11-04T00:00:00Z","run_started_at":"2026-11-04T00:00:01Z",
	    "actor":{"login":"alice","type":"User"},"pull_requests":[{"number":42}]}
	}`)
	trig, ok, err = NormalizeWebhook(7, "acme/foo", "workflow_run", workflow)
	if err != nil || !ok {
		t.Fatalf("workflow run: ok=%v err=%v", ok, err)
	}
	if trig.EventID != "workflow_run:11" || trig.Thread.ThreadExtID != "42" {
		t.Fatalf("workflow run trigger = %+v", trig)
	}
}

func TestNormalizeWebhookClosedPullRequestMatchesPolling(t *testing.T) {
	closed := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	merged := closed
	body := []byte(`{
	  "action":"closed",
	  "pull_request":{"number":42,"title":"Fix","body":"details","state":"closed","merged":true,
	    "merged_at":"2026-11-04T01:00:00Z","closed_at":"2026-11-04T01:00:00Z",
	    "updated_at":"2026-11-04T01:00:00Z","html_url":"https://github.com/acme/foo/pull/42",
	    "user":{"login":"alice","type":"User"},"head":{"sha":"abcdef1"}}
	}`)
	trig, ok, err := NormalizeWebhook(7, "acme/foo", "pull_request", body)
	if err != nil || !ok {
		t.Fatalf("closed PR: ok=%v err=%v", ok, err)
	}
	polled, pok := NormalizeClosedPullRequest(7, "acme/foo", pullRequest{
		Number: 42, Title: "Fix", Body: "details", State: "closed",
		Merged: true, MergedAt: &merged, ClosedAt: &closed,
		HTMLURL: "https://github.com/acme/foo/pull/42", User: user{Login: "alice", Type: "User"},
	})
	if !pok {
		t.Fatal("polling normalization rejected the closed PR")
	}
	if trig.EventID != polled.EventID || trig.EventID != "pull_request_closed:42:"+strconv.FormatInt(closed.Unix(), 10) {
		t.Fatalf("event id: webhook=%q polling=%q", trig.EventID, polled.EventID)
	}

	if _, ok, _ := NormalizeWebhook(7, "acme/foo", "pull_request",
		[]byte(`{"action":"opened","pull_request":{"number":42,"state":"open"}}`)); ok {
		t.Fatal("an opened PR must not produce a close trigger")
	}
}

func TestNormalizeWebhookIgnoresUnhandled(t *testing.T) {
	if _, ok, err := NormalizeWebhook(7, "acme/foo", "push", []byte(`{}`)); ok || err != nil {
		t.Fatalf("push: ok=%v err=%v", ok, err)
	}
	if _, ok, err := NormalizeWebhook(7, "acme/foo", "ping", []byte(`{}`)); ok || err != nil {
		t.Fatalf("ping: ok=%v err=%v", ok, err)
	}
	if _, _, err := NormalizeWebhook(7, "acme/foo", "issue_comment", []byte(`{`)); err == nil {
		t.Fatal("malformed JSON must error")
	}
}
