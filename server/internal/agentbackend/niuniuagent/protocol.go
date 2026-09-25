// Wire types for the niuniu-agent ACP dialect (standard ACP JSON-RPC over
// newline-delimited stdio). Kept separate from the backend logic; shapes are
// matched to what agent/internal/acp actually emits.
package niuniuagent

import "encoding/json"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type newSessionParams struct {
	CWD string `json:"cwd"`
}

type promptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptBlock `json:"prompt"`
}

type promptBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// promptResult is the session/prompt response payload. Usage carries the
// turn's token accounting when the agent reports it (absent → zeros).
type promptResult struct {
	StopReason string     `json:"stopReason"`
	Usage      *usageBody `json:"usage,omitempty"`
}

type usageBody struct {
	InputTokens     int `json:"inputTokens"`
	OutputTokens    int `json:"outputTokens"`
	CacheReadTokens int `json:"cacheReadTokens"`
}

// sessionUpdate is one `session/update` notification payload.
type sessionUpdate struct {
	SessionID string     `json:"sessionId"`
	Update    updateBody `json:"update"`
}

type updateBody struct {
	SessionUpdate string       `json:"sessionUpdate"` // agent_message_chunk | tool_call | tool_call_update
	Content       *contentBody `json:"content,omitempty"`
	ToolCallID    string       `json:"toolCallId,omitempty"`
	Title         string       `json:"title,omitempty"`
	Status        string       `json:"status,omitempty"`
	Output        *contentBody `json:"output,omitempty"`
}

type contentBody struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// requestPermissionParams is the payload of the agent's
// `session/request_permission` request; the host answers with an outcome.
type requestPermissionParams struct {
	SessionID  string          `json:"sessionId"`
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Options    []permissionOpt `json:"options"`
}

type permissionOpt struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type cancelParams struct {
	SessionID string `json:"sessionId"`
}
