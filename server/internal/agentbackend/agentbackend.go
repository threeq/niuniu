// Package agentbackend defines a reusable, runtime-agnostic contract for
// plugging an external agent runtime ("agent backend") into niuniu's
// structured proxy-chat abstraction.
//
// A backend owns exactly one workspace's agent process and speaks that
// runtime's protocol over stdio (or any transport it hides). niuniu drives it
// with a prompt, receives a stream of normalized events (text, thinking, tool
// use/result, turn completion with cost), and answers runtime→host permission
// / UI requests surfaced to the user as cards.
//
// This interface is intentionally backend-neutral: oh-my-pi (omp) implements
// it in [./omp], and the Goose integration (a sibling issue) reuses the same
// contract. Host responsibilities (SSE fan-out, DB persistence, cost
// accounting, permission dialogs) live in the caller, not the backend — a
// backend only emits neutral events and asks permission questions.
package agentbackend

import (
	"context"
)

// Backend is the contract a hosted agent runtime implements. It is the
// abstraction the omp and goose integrations share.
//
// Lifecycle: [Backend.Start] launches the process and performs the protocol
// handshake; [Backend.Prompt] drives one user turn and streams events back;
// [Backend.Abort] cancels an in-flight turn; [Backend.Close] tears the process
// down. Implementations must be safe for one in-flight Prompt at a time (the
// host serializes turns).
type Backend interface {
	// Start launches the agent process and completes the transport handshake
	// (e.g. omp's `ready` frame). It is called once before the first Prompt and
	// is idempotent: calling it again while already started is a no-op.
	Start(ctx context.Context) error

	// Prompt sends a user message and streams the resulting turn's events on
	// the returned channel. Exactly one Prompt may be in flight at a time.
	//
	// The returned channel delivers zero or more [Event]s and is closed when
	// the turn completes — either by a terminal [EventDone]/[EventError] from
	// the runtime, by ctx cancellation, or by [Backend.Abort]. The caller must
	// drain it until close.
	Prompt(ctx context.Context, req PromptRequest) (<-chan Event, error)

	// ResolvePermission is the host bridge for runtime→host UI/permission
	// requests (omp's `extension_ui_request`). The host surfaces the request to
	// the user and returns their decision, which the backend writes back to the
	// runtime. It blocks until a decision is available.
	ResolvePermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error)

	// Abort cancels the in-flight turn (omp `abort`). The Prompt channel is
	// closed by the backend once the runtimes settles.
	Abort(ctx context.Context) error

	// Close tears down the process and releases the transport. Safe to call
	// more than once.
	Close(ctx context.Context) error
}

// PromptRequest is a single user turn delivered to the backend.
type PromptRequest struct {
	// Message is the user's text prompt.
	Message string

	// Images are optional inline images (base64-encoded data URIs) attached to
	// the prompt. May be nil.
	Images []Image

	// StreamingBehavior is honored only when the runtime is already mid-turn
	// (omp: "steer" queues a steering/interrupt message, "followUp" queues a
	// post-turn follow-up). Empty when starting a fresh turn.
	StreamingBehavior string
}

// Image is an attachment to a prompt. Data is a base64 data URI or raw bytes.
type Image struct {
	Data string
}

// EventType identifies the kind of a normalized [Event].
type EventType string

const (
	// EventText is streaming or final assistant text.
	EventText EventType = "text"
	// EventThinking is a model thinking/scratchpad block.
	EventThinking EventType = "thinking"
	// EventToolUse is a tool invocation by the agent.
	EventToolUse EventType = "tool_use"
	// EventToolResult is the outcome of a tool call.
	EventToolResult EventType = "tool_result"
	// EventDone marks the successful end of a turn/cost summary.
	EventDone EventType = "done"
	// EventError marks a failed turn.
	EventError EventType = "error"
)

// Event is a normalized, backend-neutral event emitted during a turn. Only the
// fields relevant to each [EventType] are populated; the host maps them onto
// its own persisted/SSE model (niuniu: event.OutputEvent).
type Event struct {
	Type EventType

	// Text/Thinking/Error content.
	Text     string
	Thinking string
	Error    string

	// Tool use/result fields.
	ToolName  string
	ToolInput string // JSON-encoded tool arguments
	ToolUseID string
	IsError   bool

	// Turn summary (EventDone only).
	CostUSD      float64
	NumTurns     int
	DurationMs   int64
	InputTokens  int
	OutputTokens int
	// CacheReadTokens is the cached portion of the prompt (OpenAI-compatible
	// `cached_input_tokens` / ACP `cachedInputTokens`) when the backend reports
	// it; 0 otherwise. InputTokens+CacheReadTokens approximates one request's
	// full prompt size — the live context occupancy signal.
	CacheReadTokens int
}

// PermissionRequest is a runtime→host UI/permission request (omp
// `extension_ui_request`). The host renders it as a card and returns a
// [PermissionDecision].
type PermissionRequest struct {
	// ID is the runtime's request id, echoed back in the decision write.
	ID string

	// Method is the interaction kind: "confirm", "input", "select", "editor",
	// "cancel", "notify", "setStatus", "setWidget", "setTitle".
	Method string

	// Title and Message describe the request to the user.
	Title   string
	Message string

	// Options are the selectable choices for a "select" request.
	Options []string

	// TimeoutMS is the runtime's suggested timeout; 0 means none.
	TimeoutMS int64
}

// PermissionDecision is the host's answer to a [PermissionRequest].
type PermissionDecision struct {
	// Confirmed is true for "confirm" requests that were accepted.
	Confirmed bool

	// Value is the free-text answer for "input"/"editor", or the chosen option
	// for "select".
	Value string

	// Cancelled lets the host cancel the request (the runtime resolves to its
	// default or aborts).
	Cancelled bool

	// TimedOut reports that the host auto-resolved on timeout.
	TimedOut bool
}
