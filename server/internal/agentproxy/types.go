package agentproxy

import (
	"github.com/niuniu-dev/niuniu/internal/event"
)

// SessionStatus represents the current state of a workspace session.
type SessionStatus string

const (
	StatusRunning SessionStatus = "running"
	StatusIdle    SessionStatus = "idle"
	StatusCrashed SessionStatus = "crashed"
)

// Re-export event constants for backwards compatibility within agentproxy.
const (
	EventText                = event.EventText
	EventToolUse             = event.EventToolUse
	EventToolResult          = event.EventToolResult
	EventThinking            = event.EventThinking
	EventSystemInfo          = event.EventSystemInfo
	EventDone                = event.EventDone
	EventError               = event.EventError
	EventIdle                = event.EventIdle
	EventPing                = event.EventPing
	EventTaskUpdate          = event.EventTaskUpdate
	EventTeamAgentActivity   = event.EventTeamAgentActivity
	EventClaudeStatusChanged = event.EventClaudeStatusChanged
)

// OutputEvent is a type alias for event.OutputEvent.
// This ensures SessionHub implicitly satisfies loop.EventBroadcaster via Go structural typing.
type OutputEvent = event.OutputEvent

// Type aliases for Agent Tool activity payloads so agentproxy code can
// reference them without the `event.` prefix.
type (
	AgentActivityData = event.AgentActivityData
	ActivityEntry     = event.ActivityEntry
)

// NewOutputEvent delegates to event.NewOutputEvent.
var NewOutputEvent = event.NewOutputEvent
