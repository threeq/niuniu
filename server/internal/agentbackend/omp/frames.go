package omp

// This file declares the omp `--mode rpc` NDJSON wire protocol (protocol
// version 1). Canonical contract: docs/rpc.md at can1357/oh-my-pi.
//
// stdout frames (server → host):
//   - ready
//   - response
//   - AgentSessionEvent (agent_start / message_update / tool_execution_* /
//     agent_end / ...)
//   - extension_ui_request
//   - host_tool_call / host_tool_cancel
//   - host_uri_request / host_uri_cancel
//   - extension_error
//   - available_commands_update
//
// stdin commands (host → server):
//   - prompt / abort / steer / follow_up
//   - extension_ui_response
//   - host_tool_result / host_tool_update
//   - host_uri_result
//
// Only the subset niuniu uses is modeled here; unknown/non-critical frames are
// skipped.

import "encoding/json"

// readyFrame is the first stdout frame a running omp RPC server emits.
type readyFrame struct {
	Type                      string `json:"type"`
	ProtocolVersion           int    `json:"protocolVersion"`
	SupportedProtocolVersions []int  `json:"supportedProtocolVersions,omitempty"`
	MaxFrameBytes             int64  `json:"maxFrameBytes,omitempty"`
	MaxReassembledFrameBytes  int64  `json:"maxReassembledFrameBytes,omitempty"`
}

// rpcResponse correlates to a command by id.
type rpcResponse struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"` // "response"
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`
}

// sessionEvent is any AgentSessionEvent. Type discriminates.
type sessionEvent struct {
	Type string `json:"type"`

	// message_update streaming deltas.
	AssistantMessageEvent *struct {
		Type  string `json:"type"` // text_delta | thinking_delta | tool_use_delta | ...
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent,omitempty"`

	// message_end / message_update carry the accumulated assistant message.
	Message *struct {
		ID        string            `json:"id"`
		Role      string            `json:"role"`
		Content   json.RawMessage   `json:"content"`
		ToolCalls []json.RawMessage `json:"tool_calls,omitempty"`
	} `json:"message,omitempty"`

	// tool_execution_* .
	ToolExecution *struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"toolUseId"`
		// Output/result (tool_execution_end).
		Output  json.RawMessage `json:"output,omitempty"`
		IsError bool            `json:"isError"`
	} `json:"toolExecution,omitempty"`

	// agent_end telemetry.
	Messages   []json.RawMessage `json:"messages,omitempty"`
	IsTerminal *bool             `json:"isTerminal,omitempty"`
	Telemetry  *struct {
		CostUSD      *float64 `json:"costUsd,omitempty"`
		NumTurns     *int     `json:"numTurns,omitempty"`
		DurationMS   *int64   `json:"durationMs,omitempty"`
		InputTokens  *int     `json:"inputTokens,omitempty"`
		OutputTokens *int     `json:"outputTokens,omitempty"`
	} `json:"telemetry,omitempty"`
	Error string `json:"error,omitempty"`
}

// extensionUIRequest is a runtime→host UI/permission request.
type extensionUIRequest struct {
	Type        string   `json:"type"` // "extension_ui_request"
	ID          string   `json:"id"`
	Method      string   `json:"method"` // confirm|input|select|editor|cancel|notify|setStatus|setWidget|setTitle|open_url
	Title       string   `json:"title,omitempty"`
	Message     string   `json:"message,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
	Timeout     int64    `json:"timeout,omitempty"`
}

// rpcChunk is a protocol-v2 reassembly segment. Not negotiated by v1 clients;
// kept for completeness.
type rpcChunk struct {
	Type       string `json:"type"` // "rpc_chunk"
	ChunkID    string `json:"chunkId"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	ByteLength int    `json:"byteLength"`
	Data       string `json:"data"` // base64
}

// Inbound command frames (host → server).

// promptCommand schedules a user turn.
type promptCommand struct {
	ID                string   `json:"id"`
	Type              string   `json:"type"` // "prompt"
	Message           string   `json:"message"`
	Images            []string `json:"images,omitempty"`
	StreamingBehavior string   `json:"streamingBehavior,omitempty"`
}

// extensionUIResponse resolves a previously offered extension_ui_request.
// Exactly one of value / confirmed / cancelled is set per the protocol.
type extensionUIResponse struct {
	Type      string `json:"type"` // "extension_ui_response"
	ID        string `json:"id"`
	Value     string `json:"value,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
	TimedOut  bool   `json:"timedOut,omitempty"`
}

// abortCommand cancels the in-flight turn.
type abortCommand struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"` // "abort"
}
