package event

import "time"

// OutputEvent type constants — match Claude CLI's visual elements.
const (
	EventText                  = "text"                    // streaming text content
	EventToolUse               = "tool_use"                // tool invocation (name + input)
	EventToolResult            = "tool_result"             // tool execution output
	EventThinking              = "thinking"                // model thinking content
	EventSystemInfo            = "system_info"             // system messages (rate limit, retry, etc.)
	EventDone                  = "done"                    // execution complete
	EventError                 = "error"                   // error
	EventIdle                  = "idle"                    // session idle, waiting for user input
	EventPing                  = "ping"                    // SSE keepalive
	EventTaskUpdate            = "task_update"             // workspace task state change
	EventAgentDone             = "agent_done"              // agent session completed (success or failure)
	EventAgentFailed           = "agent_failed"            // agent session failed with error
	EventHealthChanged         = "health_changed"          // backend health status changed
	EventQueueUpdate           = "queue_update"            // workspace queue changed
	EventClaudeStatusChanged   = "claude_status_changed"   // rate-limit/usage status changed — nudge clients to refetch /claude-status
	EventScheduleTrigger       = "schedule_trigger"        // scheduled task triggered
	EventKanbanUpdate          = "kanban_update"           // kanban board state changed (column/lifecycle)
	EventHarnessConfirm        = "harness_confirm"         // harness phase completion awaiting user confirmation
	EventHarnessStatus         = "harness_status"          // harness run status update
	EventTeamBlackboardUpdated = "team:blackboard_updated" // team blackboard entry changed
	// Agent Tool 监控
	EventTeamAgentDispatched = "team:agent_dispatched"
	EventTeamAgentCompleted  = "team:agent_completed"
	EventTeamAgentActivity   = "team:agent_activity"
	EventTeamInboxSent       = "team:inbox_sent"
	// Permission prompt (claude code cli --permission-prompt-tool)
	EventPermissionRequest = "permission_request"
	EventPermissionDecided = "permission_decided"
	// Ask-user-question (niuniu_ask_user_question MCP tool — substitute for
	// Claude Code's built-in AskUserQuestion which has no TTY in niuniu chat).
	EventAskUserRequest = "ask_user_request"
	EventAskUserDecided = "ask_user_decided"
	// Column-based Harness phase lifecycle events (Phase 2 Task 3+)
	EventRunPhaseStarted = "run_phase_started"
	EventRunPhaseSkipped = "run_phase_skipped"
	EventRunPhaseAborted = "run_phase_aborted"
	EventGateStarted     = "gate_started"
	// Agent lifecycle events (Phase 2 Task 4)
	EventAgentLifecycle = "agent_lifecycle"
	// Gate execution progress events (Phase 2 Task 5)
	EventGateProgress = "gate_progress"
	EventGateDone     = "gate_done"
	// Workspace completion (Executable Epic E4 — drives the event-driven Epic
	// execution engine's wave advance). Published by WorkspaceOpsService.
	EventWorkspaceCompleted = "workspace_completed"
)

// OutputEvent is sent to SSE subscribers and persisted to DB.
type OutputEvent struct {
	Type                string              `json:"type"`      // see Event* constants
	Content             string              `json:"content"`   // text/result/error content
	MessageId           string              `json:"messageId"` // groups events within one assistant turn
	Role                string              `json:"role"`      // "user" | "assistant" | "system"
	Ts                  int64               `json:"ts"`        // unix ms
	WorkspaceId         int64               `json:"workspaceId"`
	CostUsd             float64             `json:"costUsd,omitempty"`             // total cost (done events)
	ToolName            string              `json:"toolName,omitempty"`            // tool name (tool_use/tool_result events)
	ToolInput           string              `json:"toolInput,omitempty"`           // tool input summary (tool_use events)
	ToolUseId           string              `json:"toolUseId,omitempty"`           // links tool_use to tool_result
	IsError             bool                `json:"isError,omitempty"`             // true if tool_result is an error
	NumTurns            int                 `json:"numTurns,omitempty"`            // total turns (done events)
	DurationMs          int64               `json:"durationMs,omitempty"`          // total duration (done events)
	InputTokens         int                 `json:"inputTokens,omitempty"`         // context input tokens (message_start)
	OutputTokens        int                 `json:"outputTokens,omitempty"`        // output tokens (message_delta)
	CacheCreationTokens int                 `json:"cacheCreationTokens,omitempty"` // cache write tokens (done)
	CacheReadTokens     int                 `json:"cacheReadTokens,omitempty"`     // cache read tokens (done)
	CliType             string              `json:"cliType,omitempty"`             // CLI identity e.g. "claude", "codex" (system_info init)
	Attachments         string              `json:"attachments,omitempty"`         // JSON-serialized []ChatAttachment for user echo events (queue-dequeued / scheduler / harness sends)
	RateLimit           *RateLimitData      `json:"rateLimit,omitempty"`
	TaskData            *TaskData           `json:"taskData,omitempty"`
	HarnessConfirm      *HarnessConfirmData `json:"harnessConfirm,omitempty"`
	HarnessStatus       *HarnessStatusData  `json:"harnessStatus,omitempty"`
	// Agent Tool 监控 payloads
	AgentDispatched *AgentDispatchedData `json:"agentDispatched,omitempty"`
	AgentCompleted  *AgentCompletedData  `json:"agentCompleted,omitempty"`
	AgentActivity   *AgentActivityData   `json:"agentActivity,omitempty"`
	InboxSent       *InboxSentData       `json:"inboxSent,omitempty"`
	// Permission prompt payloads
	PermissionRequest *PermissionRequestData `json:"permissionRequest,omitempty"`
	PermissionDecided *PermissionDecidedData `json:"permissionDecided,omitempty"`
	// Ask-user-question payloads
	AskUserRequest *AskUserRequestData `json:"askUserRequest,omitempty"`
	AskUserDecided *AskUserDecidedData `json:"askUserDecided,omitempty"`
	// Column-based Harness phase lifecycle payloads (Phase 2 Task 3+)
	RunPhase *RunPhasePayload `json:"runPhase,omitempty"`
	GateJob  *GateJobPayload  `json:"gateJob,omitempty"`
	// Agent lifecycle payload (Phase 2 Task 4)
	AgentLifecycle *AgentLifecyclePayload `json:"agentLifecycle,omitempty"`
	// Gate execution progress payloads (Phase 2 Task 5)
	GateProgress *GateProgressPayload `json:"gateProgress,omitempty"`
	GateDone     *GateDonePayload     `json:"gateDone,omitempty"`
}

// RateLimitData carries rate limit info from the Claude CLI rate_limit_event.
type RateLimitData struct {
	Status   string `json:"status"`        // "allowed", "allowed_warning", "rejected"
	Type     string `json:"rateLimitType"` // "five_hour"
	ResetsAt int64  `json:"resetsAt"`      // unix timestamp (seconds)
	Overage  string `json:"overageStatus"` // "rejected", etc.
}

// HarnessConfirmData is the payload for harness_confirm events.
type HarnessConfirmData struct {
	RunID        string `json:"runId"`
	Phase        string `json:"phase"`
	Summary      string `json:"summary"`
	Reviewed     bool   `json:"reviewed"`
	CurrentRound int    `json:"currentRound"`
	MaxRounds    int    `json:"maxRounds"`
}

// HarnessStatusData is the payload for harness_status events.
type HarnessStatusData struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Phase  string `json:"phase"`
	Round  int    `json:"round"`
}

// WorkspaceCompletedEvent is the JSON payload for workspace_completed events.
// Emitted when a workspace transitions to 'completed'; the Epic execution
// engine consumes it to mark the linked child issue done and advance waves.
type WorkspaceCompletedEvent struct {
	WorkspaceID int64 `json:"workspace_id"`
	IssueID     int64 `json:"issue_id"`
	Success     bool  `json:"success"`
}

// KanbanUpdateEvent payload for kanban board state changes.
type KanbanUpdateEvent struct {
	ProjectID int64  `json:"projectId"`
	IssueID   int64  `json:"issueId"`
	Action    string `json:"action"` // "moved" | "lifecycle_changed"
}

// TaskData is sent when a workspace task is created or updated.
type TaskData struct {
	ID          int64  `json:"id"`
	AgentTaskId string `json:"agentTaskId"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"activeForm"`
	Status      string `json:"status"`
	Phase       string `json:"phase"`
	MessageId   string `json:"messageId"`
	BatchId     string `json:"batchId"`
	StartedAt   *int64 `json:"startedAt,omitempty"`
	CompletedAt *int64 `json:"completedAt,omitempty"`
}

// AgentDispatchedData — team:agent_dispatched 事件负载
type AgentDispatchedData struct {
	WorkspaceAgentID    int64  `json:"workspaceAgentId"`
	ParentAgentID       int64  `json:"parentAgentId,omitempty"`
	SubagentType        string `json:"subagentType,omitempty"`
	SubagentDescription string `json:"subagentDescription,omitempty"`
	DispatchToolUseID   string `json:"dispatchToolUseId,omitempty"`
}

type AgentCompletedData struct {
	WorkspaceAgentID int64  `json:"workspaceAgentId"`
	IsError          bool   `json:"isError"`
	ExitedAt         int64  `json:"exitedAt"`
	LastError        string `json:"lastError,omitempty"`
}

type AgentActivityData struct {
	WorkspaceAgentID int64          `json:"workspaceAgentId"`
	Activity         *ActivityEntry `json:"activity"`
}

type ActivityEntry struct {
	Kind        string `json:"kind"`
	ToolName    string `json:"toolName,omitempty"`
	Target      string `json:"target,omitempty"`
	TextPreview string `json:"textPreview,omitempty"`
	OccurredAt  int64  `json:"occurredAt"`
}

type InboxSentData struct {
	WorkspaceID int64  `json:"workspaceId"`
	FromAgentID int64  `json:"fromAgentId"`
	ToAgentID   int64  `json:"toAgentId"`
	Subject     string `json:"subject,omitempty"`
}

// PermissionRequestData is the payload for permission_request events. Sent
// when an agent triggers --permission-prompt-tool and a row is inserted in
// agent_permission_requests with status='pending'.
type PermissionRequestData struct {
	RequestID   int64          `json:"requestId"`
	SessionID   string         `json:"sessionId"`
	ToolName    string         `json:"toolName"`
	ToolInput   map[string]any `json:"toolInput"`
	RequestedAt int64          `json:"requestedAt"` // unix ms
	ExpiresAt   int64          `json:"expiresAt"`   // unix ms
}

// PermissionDecidedData is the payload for permission_decided events. Sent
// when a pending permission request reaches a terminal state (allowed,
// denied, timeout, cancelled).
type PermissionDecidedData struct {
	RequestID      int64  `json:"requestId"`
	Status         string `json:"status"` // allowed | denied | timeout | cancelled
	DecidedByID    int64  `json:"decidedById,omitempty"`
	DecisionSource string `json:"decisionSource"` // user | allowlist | timeout | session_end
}

// AskUserQuestion mirrors one entry of the AskUserQuestion tool's questions
// array — surfaced to the chat UI verbatim so the rendered card matches the
// agent's intent.
type AskUserQuestion struct {
	Question string `json:"question"`
	// Header is an optional short legend (≤12 chars) shown above the
	// question. Marked omitempty so Claude can skip it when not useful.
	Header      string                  `json:"header,omitempty"`
	MultiSelect bool                    `json:"multiSelect"`
	Options     []AskUserQuestionOption `json:"options"`
}

// AskUserQuestionOption is a single selectable choice. Description is
// optional explanatory text shown beside the label. Preview carries
// optional renderable content (mockup, code snippet, comparison) Claude
// wants the user to see when this option is focused — mirrors the
// official AskUserQuestion schema's `preview?: string` field.
//
// Recommended marks the option Claude suggests as the default — the SPA
// renders a star + accent ring. Locale-agnostic: Claude sets this field
// explicitly rather than us regex-parsing "(Recommended)" suffixes off
// the label string.
type AskUserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// AskUserRequestData is the payload for ask_user_request events. Sent when
// the niuniu_ask_user_question MCP tool inserts a row into
// agent_ask_user_requests with status='pending'.
type AskUserRequestData struct {
	RequestID   int64             `json:"requestId"`
	SessionID   string            `json:"sessionId"`
	Questions   []AskUserQuestion `json:"questions"`
	RequestedAt int64             `json:"requestedAt"` // unix ms
	ExpiresAt   int64             `json:"expiresAt"`   // unix ms
}

// AskUserDecidedData is the payload for ask_user_decided events. Sent when
// a pending ask-user request reaches a terminal state (answered, timeout,
// cancelled).
type AskUserDecidedData struct {
	RequestID      int64  `json:"requestId"`
	Status         string `json:"status"` // answered | timeout | cancelled
	DecidedByID    int64  `json:"decidedById,omitempty"`
	DecisionSource string `json:"decisionSource"` // user | timeout | session_end
}

// RunPhasePayload is emitted on run_phase_started / _skipped / _aborted events.
// All field names are camelCase matching the project event convention (commit 169150bd).
type RunPhasePayload struct {
	RunID      int64  `json:"runId"`
	ColumnID   int64  `json:"columnId"`
	ColumnName string `json:"columnName"`
	Round      int    `json:"round,omitempty"`
	Source     string `json:"source,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// GateJobPayload is emitted on gate_started events when an async gate is enqueued.
type GateJobPayload struct {
	RunID     int64 `json:"runId"`
	JobID     int64 `json:"jobId"`
	ColumnID  int64 `json:"columnId"`
	SpecCount int   `json:"specCount"`
}

// AgentLifecyclePayload is emitted on agent_lifecycle events (Phase 2 Task 4).
// Action is "start_for_column" | "stop_for_run". Status is "ok" | "skipped" | "warn".
type AgentLifecyclePayload struct {
	RunID       int64  `json:"runId"`
	WorkspaceID int64  `json:"workspaceId"`
	ColumnID    int64  `json:"columnId,omitempty"`
	Action      string `json:"action"`
	AgentName   string `json:"agentName,omitempty"`
	Status      string `json:"status"` // ok | skipped | warn
	Message     string `json:"message,omitempty"`
}

// GateProgressPayload is emitted on gate_progress events (one per spec execution).
type GateProgressPayload struct {
	JobID  int64  `json:"jobId"`
	RunID  int64  `json:"runId"`
	SpecID int64  `json:"specId"`
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Passed bool   `json:"passed"`
	Output string `json:"output,omitempty"`
}

// GateDonePayload is emitted on gate_done events when all gate specs finish or fail-fast.
type GateDonePayload struct {
	JobID        int64 `json:"jobId"`
	RunID        int64 `json:"runId"`
	Passed       bool  `json:"passed"`
	FailureCount int   `json:"failureCount"`
	DurationMs   int64 `json:"durationMs"`
}

// NewOutputEvent creates an OutputEvent with current timestamp.
func NewOutputEvent(typ, content, msgId, role string, workspaceId int64) OutputEvent {
	return OutputEvent{
		Type:        typ,
		Content:     content,
		MessageId:   msgId,
		Role:        role,
		Ts:          time.Now().UnixMilli(),
		WorkspaceId: workspaceId,
	}
}
