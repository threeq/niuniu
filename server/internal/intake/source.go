// Package intake is the task-intake capability (Epic #755): external events
// (IM messages, GitHub comments, CI results, ...) are normalized into Triggers
// and run through one fixed pipeline:
//
//	dedupe -> authorize -> resolve task -> decide -> record -> execute
//
// The IM bot is the first Source adapter (Phase 1): its inbound handler
// normalizes an admitted chat message into a Trigger and runs it through this
// pipeline, while IM-only behaviour (pairing, slash commands, conversation
// switching, permission callbacks) stays in the IM adapter. Later phases add
// GitHub / CI / webhook sources on the same pipeline.
//
// External text is data, never instructions: authorization is decided by code
// (Policy.Evaluate) and cannot be changed by message content.
package intake

import (
	"context"
	"encoding/json"
	"time"
)

// Source kinds. One adapter per kind implements Source.
const (
	SourceKindIMBot   = "im_bot"
	SourceKindGitHub  = "github"
	SourceKindCI      = "ci"
	SourceKindEmail   = "email"
	SourceKindWebhook = "webhook"
)

// Event kinds are adapter-defined (message, issue_comment, pull_request_review,
// check_run, ...). The intake pipeline does not branch on them.
const (
	EventKindMessage           = "message"
	EventKindIssueComment      = "issue_comment"
	EventKindReview            = "review"
	EventKindReviewComment     = "pull_request_review_comment"
	EventKindCheckRun          = "check_run"
	EventKindWorkflowRun       = "workflow_run"
	EventKindPullRequestClosed = "pull_request_closed"
	// EventKindProposal is work a source derived from observation rather than a
	// message addressed to it (the IM employee's proactive task verdict).
	EventKindProposal = "proposal"

	// CI event kinds (design §5.1). A CI adapter maps provider statuses onto
	// these four values and drops everything else, so the pipeline never sees a
	// provider-specific state such as "running" or "on_hold".
	EventKindCIFailed    = "ci_failed"
	EventKindCISucceeded = "ci_succeeded"
	EventKindCIRecovered = "ci_recovered"
	EventKindCICanceled  = "ci_canceled"
)

// Attachment is an external resource referenced by an inbound event. Bytes are
// not inlined; adapters fetch them only when a task actually needs them.
type Attachment struct {
	Kind      string // image | file | audio | video
	Name      string
	URL       string
	MIMEType  string
	SizeBytes int64
}

// ThreadRef identifies the conversation a task is bound to.
//
// ThreadExtID is the platform's own id (IM thread, PR number, issue number).
// IssueID / WorkspaceID are set once the thread has been bound to a task; both
// are zero for an unbound thread.
type ThreadRef struct {
	ThreadExtID string
	IssueID     int64
	WorkspaceID int64
}

// ActorRef identifies who produced an event. IsBot is a fact reported by the
// adapter, not an authorization decision.
type ActorRef struct {
	ExtID string
	Name  string
	IsBot bool
}

// Reply is an outbound message sent back to a Source thread.
type Reply struct {
	Text string
}

// Trigger is the platform-neutral shape every adapter produces.
type Trigger struct {
	ProjectID   int64
	SourceKind  string // im_bot | github | ci | email | webhook
	SourceRef   string // channel id / repo full name / mailbox
	EventID     string // platform event id, used for idempotent dedupe
	EventKind   string // message | issue_comment | review | check_run ...
	Thread      ThreadRef
	Actor       ActorRef
	Text        string
	Attachments []Attachment
	Raw         json.RawMessage
	ReceivedAt  time.Time

	// SourceURL is the upstream permalink for the event (a PR/comment URL).
	// Executors copy it into issues.external_url for provenance.
	SourceURL string

	// ConversationExtID is the source's conversation container — for IM, the
	// group/DM chat id. Thread.ThreadExtID is the narrower topic inside it.
	// Adapters that have no such split leave it empty and use SourceRef.
	ConversationExtID string

	// Explicit marks a request the source has already admitted: a message from
	// a participant in an approved, project-bound conversation. It is set by
	// adapter code from that admission decision — never derived from message
	// text — so a crafted message cannot promote itself. Source admission makes
	// the request explicit only while no actor allow list is configured; once
	// one exists, Policy.Evaluate requires the actor to be on it.
	Explicit bool

	// ReceiveAll marks a conversation the source is reading in full (IM
	// observe mode) rather than only acting on when addressed. Unlisted actors
	// there become inferred candidates instead of being denied — they can be
	// calibrated or, in auto mode, run inside a matching rule.
	ReceiveAll bool

	// ForceNew asks for a new task even when the conversation is already bound
	// to one. Adapters set it from an explicit user control (IM `/new`).
	ForceNew bool

	// Mode overrides the project+source policy's mode for this trigger. A source
	// with a finer-grained mode (an IM chat's own intake_mode) sets it; empty
	// means "use the policy's mode as stored".
	Mode Mode

	// RequireBound marks an event that is only meaningful as a signal to an
	// existing task — CI success/recovery/cancel events say "the pipeline the
	// task was fixing now passes". When the thread is not bound to a task the
	// pipeline records an ignore decision and creates nothing: a success event
	// must never spawn work of its own.
	RequireBound bool
}

// Source is one inbound adapter. Normalize must be pure: it maps a platform
// payload to Triggers without side effects or authorization checks.
type Source interface {
	Kind() string
	Normalize(ctx context.Context, raw any) ([]Trigger, error)
	Reply(ctx context.Context, thread ThreadRef, msg Reply) error
}
