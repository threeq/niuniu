// Package cursor implements niuniu's agentbackend contract on top of Cursor
// CLI's ACP mode (`agent acp`).
//
// Why ACP and not the headless `-p` path: Cursor's CLI documents a first-class
// ACP (Agent Client Protocol) server — JSON-RPC 2.0 over stdio, newline framed —
// which is the same protocol family the Goose backend already speaks. Going
// through ACP buys four things the one-shot `cursor-agent -p` path cannot offer:
//
//  1. a real permission gate (`session/request_permission`), so niuniu renders
//     approval cards instead of blanket --force;
//  2. multi-turn on one process (`session/prompt` per turn), so context is not
//     re-established per message;
//  3. cooperative cancellation (`session/cancel`) instead of killing the process;
//  4. MCP servers passed explicitly at `session/new`, sidestepping the reported
//     "MCP tools are not injected in -p mode" defect entirely.
//
// This file holds the wire types. Two deliberate differences from the sibling
// goose/acp.go, which speaks a Goose-flavored ACP variant:
//
//   - `session/update` here is the STANDARD ACP shape: a flat, discriminated
//     `update.sessionUpdate` string ("agent_message_chunk", "tool_call",
//     "tool_call_update", ...). Goose instead wraps a {status, events[]} envelope.
//   - `session/request_permission` here is a REQUEST (it carries an `id` and the
//     client must answer with a JSON-RPC *response* selecting an optionId).
//     In the Goose variant it arrives as a notification answered by a separate
//     `session/reply` request.
//
// Getting either of those wrong means a silently hung turn, so they are pinned
// by tests against the documented shapes.
package cursor

import "encoding/json"

// --- JSON-RPC envelopes ---

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// --- Host→agent requests ---

// initializeParams is the ACP handshake. Cursor's documented minimal client
// sends protocolVersion as the NUMBER 1 (not the "v1" string the Goose variant
// uses), so this field is an int.
type initializeParams struct {
	ProtocolVersion    int        `json:"protocolVersion"`
	ClientCapabilities clientCaps `json:"clientCapabilities"`
	ClientInfo         clientInfo `json:"clientInfo"`
}

type clientCaps struct {
	FS       fsCaps `json:"fs"`
	Terminal bool   `json:"terminal"`
}

// fsCaps declares whether the CLIENT exposes filesystem operations for the
// agent to call back into. niuniu declares false for both: the agent runs
// directly inside the workspace worktree and uses its own file tools, so there
// is no reason to proxy reads/writes through the host.
type fsCaps struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// authenticateParams selects the ACP auth method. Cursor advertises
// "cursor_login"; credentials themselves come from a prior `agent login` or the
// CURSOR_API_KEY / CURSOR_AUTH_TOKEN env vars.
type authenticateParams struct {
	MethodID string `json:"methodId"`
}

// newSessionParams creates a fresh session rooted at cwd. mcpServers is the
// MCP-collaboration edge: niuniu-mcp is handed over here so the cursor agent can
// call the kanban / data / memory / document tools as an MCP client.
type newSessionParams struct {
	Cwd        string `json:"cwd"`
	McpServers []any  `json:"mcpServers"`
}

// promptParams delivers one user turn as ACP content blocks.
type promptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    []any  `json:"prompt"`
}

// cancelParams asks the agent to abort the in-flight turn (notification).
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// --- Agent→host notifications ---

// sessionUpdateNotification is the `session/update` payload. The agent streams
// these during a turn.
type sessionUpdateNotification struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

// sessionUpdate is the standard ACP discriminated update. SessionUpdate names
// the variant; only the fields relevant to it are populated.
type sessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// agent_message_chunk / agent_thought_chunk
	Content *contentBlock `json:"content"`

	// tool_call / tool_call_update
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// requestPermissionParams is the `session/request_permission` payload. Unlike
// the Goose variant this arrives as a REQUEST carrying a JSON-RPC id; the client
// answers with a response whose result selects one of the offered options.
type requestPermissionParams struct {
	SessionID string              `json:"sessionId"`
	ToolCall  *permissionToolCall `json:"toolCall"`
	Options   []permissionOption  `json:"options"`
}

type permissionToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	RawInput   json.RawMessage `json:"rawInput"`
}

// permissionOption is one selectable decision. Cursor documents the optionIds
// "allow-once", "allow-always" and "reject-once"; the kind field ("allow_once",
// "reject_once", ...) is the spec's machine-readable classifier.
type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Documented option ids. Preferred over kind-sniffing because Cursor's docs name
// these exact strings, but selection falls back to kind/prefix matching so a
// renamed id cannot hang the turn (see chooseOption).
const (
	optAllowOnce  = "allow-once"
	optRejectOnce = "reject-once"
)

// permissionOutcome is the client's answer. "selected" carries the chosen
// optionId; "cancelled" aborts the request.
type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

// promptResult is the `session/prompt` response. stopReason is the terminal
// signal for the turn ("end_turn", "cancelled", "refusal", "max_tokens", ...).
type promptResult struct {
	StopReason string `json:"stopReason"`
}
