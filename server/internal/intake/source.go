// Package intake is the task-intake capability (Epic #755): external events
// (IM messages, GitHub comments, CI results, ...) are normalized into Triggers
// and run through one fixed pipeline:
//
//	dedupe -> authorize -> resolve task -> decide -> record -> execute
//
// This package currently holds the Phase 1 skeleton only: the abstractions,
// policy evaluation and Task Brief rendering, plus a service that is wired into
// DI but disabled by default and not called from any inbound path. Existing IM
// and autohost behaviour is therefore unchanged.
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
	EventKindMessage      = "message"
	EventKindIssueComment = "issue_comment"
	EventKindReview       = "review"
	EventKindCheckRun     = "check_run"
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
}

// Source is one inbound adapter. Normalize must be pure: it maps a platform
// payload to Triggers without side effects or authorization checks.
type Source interface {
	Kind() string
	Normalize(ctx context.Context, raw any) ([]Trigger, error)
	Reply(ctx context.Context, thread ThreadRef, msg Reply) error
}
