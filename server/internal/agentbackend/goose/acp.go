package goose

// This file models the Agent Client Protocol (ACP) subset that niuniu's goose
// backend drives over stdio (`goose acp`). Canonical contract: the
// agent-client-protocol crate (agentclientprotocol.org) as implemented by
// Block's Goose `goose acp` command.
//
// Framing: JSON-RPC 2.0 over newline-delimited stdio. Each line is one JSON
// object. The client sends requests (with an `id`) and notifications (no `id`);
// the agent answers requests with a response carrying the same `id` and emits
// notifications such as `session/update` and `session/request_permission`.
//
// Only the subset niuniu uses is modeled here; unknown frames are skipped.

import "encoding/json"

// ---- JSON-RPC envelopes ---------------------------------------------------

// rpcRequest is a host→agent request (or notification when ID is 0/missing).
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc,omitempty"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is an agent→host answer to a request, keyed by ID.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcNotification is an agent→host notification (method, no id).
type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ---- Host→agent requests --------------------------------------------------

// initializeParams is the ACP handshake. protocolVersion "v1".
type initializeParams struct {
	ProtocolVersion    string `json:"protocolVersion"`
	ClientCapabilities any    `json:"clientCapabilities"`
	ClientInfo         struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// newSessionParams creates a fresh agent session rooted at cwd.
type newSessionParams struct {
	Cwd        string `json:"cwd"`
	Name       string `json:"name,omitempty"`
	McpServers []any  `json:"mcpServers,omitempty"`
}

// promptParams delivers one user turn. Prompt is an array of content blocks
// ([{type:"text", text:"..."}]).
type promptParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode,omitempty"`
	Prompt    []any  `json:"prompt"`
}

// replyParams resolves an earlier session/request_permission (or in-update
// permission_request) notification. Each zone carries the decided state.
type replyParams struct {
	SessionID        string      `json:"sessionId"`
	TurnID           string      `json:"turnId,omitempty"`
	InteractionZones []zoneReply `json:"interactionZones"`
}

type zoneReply struct {
	Type        string   `json:"type"`
	ToolCallIds []string `json:"toolCallIds,omitempty"`
	State       string   `json:"state"` // allow | deny | edit | provide
}

// cancelParams tells the agent to abort the in-flight turn (notification).
type cancelParams struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId,omitempty"`
}

// closeParams shuts down a session.
type closeParams struct {
	SessionID string `json:"sessionId"`
}

// ---- Agent→host notifications --------------------------------------------

// sessionUpdateNotification is the payload of a `session/update` notification.
// The agent streams these during a turn; the terminal `status` ends it.
type sessionUpdateNotification struct {
	Update sessionUpdate `json:"update"`
}

type sessionUpdate struct {
	SessionID string            `json:"sessionId"`
	TurnID    string            `json:"turnId,omitempty"`
	Status    string            `json:"status,omitempty"` // running|awaiting_input|completed|error|cancelled
	Events    []json.RawMessage `json:"events,omitempty"`
}

// requestPermissionParams is the payload of a `session/request_permission`
// notification. The host must reply with a `session/reply` request.
type requestPermissionParams struct {
	SessionID        string            `json:"sessionId"`
	TurnID           string            `json:"turnId,omitempty"`
	InteractionZones []interactionZone `json:"interactionZones"`
}

// interactionZone describes one permission target (tool call group, system
// action, elicitation input, ...). Legacy confirm fields are tolerated.
type interactionZone struct {
	Type        string          `json:"type"` // tools|system|elicitation|...
	ToolCallIds []string        `json:"toolCallIds,omitempty"`
	ToolCalls   json.RawMessage `json:"toolCalls,omitempty"`
	Method      string          `json:"method,omitempty"`
	Description string          `json:"description,omitempty"`
	ID          string          `json:"id,omitempty"`
	Title       string          `json:"title,omitempty"`
	Message     string          `json:"message,omitempty"`
}

// ---- session/update events ------------------------------------------------

// sessionEvent is any element of sessionUpdate.Events, discriminated by Type.
type sessionEvent struct {
	Type string `json:"type"`

	// content_update: streaming text / tool-call deltas.
	Content   *contentUpdate `json:"content,omitempty"`
	MessageID string         `json:"messageId,omitempty"`
	PartID    string         `json:"partId,omitempty"`

	// tool_call: the agent invoked a tool.
	ToolCall *toolCall `json:"toolCall,omitempty"`

	// tool_call_result: the tool's outcome.
	ToolCallResult *toolCallResult `json:"toolCallResult,omitempty"`

	// usage_update: token/cost accounting so far this turn.
	Usage *usage `json:"usage,omitempty"`

	// status_update: turn lifecycle transition.
	Status     string `json:"status,omitempty"`
	StopReason string `json:"stopReason,omitempty"`

	// permission_request: in-update permission/elicitation request.
	RequestPermission *requestPermission `json:"requestPermission,omitempty"`
}

type contentUpdate struct {
	Type string `json:"type"` // text | tool_call_delta | ...
	Text string `json:"text,omitempty"`
}

type toolCall struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`
	Status string          `json:"status,omitempty"`
}

type toolCallResult struct {
	ID         string            `json:"id"`
	ToolCallID string            `json:"toolCallId"`
	Status     string            `json:"status,omitempty"`
	IsError    bool              `json:"isError,omitempty"`
	Content    []json.RawMessage `json:"content,omitempty"`
}

type usage struct {
	CostUsd      float64 `json:"costUsd,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
}

// requestPermission mirrors the payload of a permission_request event.
type requestPermission struct {
	InteractionZones []interactionZone `json:"interactionZones"`
	Method           string            `json:"method,omitempty"`
	Description      string            `json:"description,omitempty"`
	ID               string            `json:"id,omitempty"`
	Title            string            `json:"title,omitempty"`
	Message          string            `json:"message,omitempty"`
}