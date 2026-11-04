package githubsource

// Team-edition inbound half of the GitHub Source (Epic #755 / design §6.10):
// GitHub POSTs events to /api/integrations/github/webhook/:binding instead of
// the personal edition polling. Both transports MUST produce the same
// intake.Trigger — the authentication is different, the normalized event is
// not — so the shared Intake pipeline's dedupe key
// (source_kind, source_ref, event_ext_id) makes an event delivered by both
// transports land exactly once.
//
// This file owns two pure functions: HMAC verification and payload
// normalization. Binding lookup, credential loading and pipeline execution live
// in the service layer.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// Webhook event types (the X-GitHub-Event header values) this adapter handles.
const (
	webhookEventIssueComment     = "issue_comment"
	webhookEventPullRequest      = "pull_request"
	webhookEventReview           = "pull_request_review"
	webhookEventReviewComment    = "pull_request_review_comment"
	webhookEventCheckRun         = "check_run"
	webhookEventWorkflowRun      = "workflow_run"
	webhookSignaturePrefixSHA256 = "sha256="
)

// WebhookEventKind maps an X-GitHub-Event value to the intake event kind used
// by the polling adapter. ok is false for events this Source does not consume.
func WebhookEventKind(eventType string) (string, bool) {
	switch strings.TrimSpace(eventType) {
	case webhookEventIssueComment:
		return EventIssueComment, true
	case webhookEventPullRequest:
		return EventPullRequestClosed, true
	case webhookEventReview:
		return EventReview, true
	case webhookEventReviewComment:
		return EventReviewComment, true
	case webhookEventCheckRun:
		return EventCheckRun, true
	case webhookEventWorkflowRun:
		return EventWorkflowRun, true
	}
	return "", false
}

// webhookEnvelope keeps the shared fields only; the event-specific object is
// decoded separately because `comment` means a different shape for
// issue_comment and pull_request_review_comment.
type webhookEnvelope struct {
	Action      string          `json:"action"`
	Issue       *webhookIssue   `json:"issue"`
	Comment     json.RawMessage `json:"comment"`
	Review      json.RawMessage `json:"review"`
	CheckRun    json.RawMessage `json:"check_run"`
	WorkflowRun json.RawMessage `json:"workflow_run"`
	PullRequest json.RawMessage `json:"pull_request"`
}

type webhookIssue struct {
	Number int `json:"number"`
}

// NormalizeWebhook maps a GitHub webhook delivery to the same Trigger the
// polling adapter produces for that event. ok is false for deliveries that are
// not actionable (an unhandled event type, a deleted comment, a non-final check
// run, a non-closed pull request) — the caller acknowledges them without
// recording anything, exactly as polling would skip them.
func NormalizeWebhook(projectID int64, repo, eventType string, payload []byte) (intake.Trigger, bool, error) {
	if _, known := WebhookEventKind(eventType); !known {
		return intake.Trigger{}, false, nil
	}
	var p webhookEnvelope
	if err := json.Unmarshal(payload, &p); err != nil {
		return intake.Trigger{}, false, fmt.Errorf("githubsource: decode %s webhook: %w", eventType, err)
	}

	switch strings.TrimSpace(eventType) {
	case webhookEventIssueComment:
		// Polling lists comments by updated_at, so a created OR edited comment is
		// an event it would read; a deleted one is gone from the listing.
		if p.Action != "created" && p.Action != "edited" {
			return intake.Trigger{}, false, nil
		}
		var c issueComment
		if err := json.Unmarshal(p.Comment, &c); err != nil || c.ID == 0 {
			return intake.Trigger{}, false, nil
		}
		if c.IssueURL == "" && p.Issue != nil && p.Issue.Number > 0 {
			c.IssueURL = fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", repo, p.Issue.Number)
		}
		t, ok := NormalizeIssueComment(projectID, repo, c)
		return t, ok, nil

	case webhookEventPullRequest:
		if p.Action != "closed" {
			return intake.Trigger{}, false, nil
		}
		var pr pullRequest
		if err := json.Unmarshal(p.PullRequest, &pr); err != nil || pr.Number == 0 {
			return intake.Trigger{}, false, nil
		}
		t, ok := NormalizeClosedPullRequest(projectID, repo, pr)
		return t, ok, nil

	case webhookEventReview:
		// Pending reviews are invisible to everyone; only a submitted review is
		// an event the polling adapter can see.
		if p.Action != "submitted" {
			return intake.Trigger{}, false, nil
		}
		var r pullRequestReview
		if err := json.Unmarshal(p.Review, &r); err != nil || r.ID == 0 {
			return intake.Trigger{}, false, nil
		}
		t, ok := NormalizeReview(projectID, repo, r)
		return t, ok, nil

	case webhookEventReviewComment:
		if p.Action != "created" && p.Action != "edited" {
			return intake.Trigger{}, false, nil
		}
		var c pullRequestReviewComment
		if err := json.Unmarshal(p.Comment, &c); err != nil || c.ID == 0 {
			return intake.Trigger{}, false, nil
		}
		t, ok := NormalizeReviewComment(projectID, repo, c)
		return t, ok, nil

	case webhookEventCheckRun:
		if p.Action != "completed" {
			return intake.Trigger{}, false, nil
		}
		var c checkRun
		if err := json.Unmarshal(p.CheckRun, &c); err != nil || c.ID == 0 {
			return intake.Trigger{}, false, nil
		}
		t, ok := NormalizeCheckRun(projectID, repo, c)
		return t, ok, nil

	case webhookEventWorkflowRun:
		if p.Action != "completed" {
			return intake.Trigger{}, false, nil
		}
		var w workflowRun
		if err := json.Unmarshal(p.WorkflowRun, &w); err != nil || w.ID == 0 {
			return intake.Trigger{}, false, nil
		}
		t, ok := NormalizeWorkflowRun(projectID, repo, w)
		return t, ok, nil
	}
	return intake.Trigger{}, false, nil
}

// SignatureValid verifies GitHub's X-Hub-Signature-256 header against the raw
// request body. It fails closed: an empty secret or a missing/malformed header
// is never valid. Comparison is constant-time.
func SignatureValid(secret string, payload []byte, header string) bool {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return false
	}
	header = strings.TrimSpace(header)
	if len(header) < len(webhookSignaturePrefixSHA256) ||
		!strings.EqualFold(header[:len(webhookSignaturePrefixSHA256)], webhookSignaturePrefixSHA256) {
		return false
	}
	got, err := hex.DecodeString(header[len(webhookSignaturePrefixSHA256):])
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hmac.Equal(got, mac.Sum(nil))
}
