// Package acp implements the Agent Client Protocol (ACP) server side:
// JSON-RPC 2.0 over newline-delimited stdio, so a client (niuniu, Zed, any
// ACP editor) can drive the agent: initialize → session/new → session/prompt,
// with progress arriving as session/update notifications and write-tool
// approvals surfaced via session/request_permission.
//
// Only the subset niuniu needs is implemented; unknown client requests are
// answered with a method-not-found error so newer clients degrade cleanly.
package acp

import (
	"encoding/json"
	"fmt"
)

// protocolVersion is the ACP version this server speaks.
const protocolVersion = 1

// JSON-RPC framing. One struct covers both requests (method+params) and
// responses (result/error), since the server's read loop must accept both.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
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

func (e *rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// JSON-RPC error codes used here.
const (
	errParse          = -32700
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// --- client → agent methods ---

type initializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
}

type agentCapabilities struct {
	LoadSession bool `json:"loadSession"`
}

type sessionNewParams struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

type sessionPromptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptBlock `json:"prompt"`
}

// promptBlock is one user-content block; ACP text blocks carry the prompt.
type promptBlock struct {
	Type string `json:"type"` // "text" | "image" | "resource_link"
	Text string `json:"text,omitempty"`
}

type sessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// --- agent → client notifications (session/update) ---

type sessionUpdateParams struct {
	SessionID string     `json:"sessionId"`
	Update    updateBody `json:"update"`
}

type updateBody struct {
	SessionUpdate string       `json:"sessionUpdate"` // "agent_message_chunk" | "tool_call" | "tool_call_update"
	Content       *contentBody `json:"content,omitempty"`
	ToolCallID    string       `json:"toolCallId,omitempty"`
	Title         string       `json:"title,omitempty"`
	Kind          string       `json:"kind,omitempty"` // "read" | "edit" | "delete" | "move" | "search" | "execute" | "think" | "other"
	Status        string       `json:"status,omitempty"`
	Output        *contentBody `json:"output,omitempty"`
	RawInput      any          `json:"rawInput,omitempty"`
}

type contentBody struct {
	Type string `json:"type"` // "text"
	Text string `json:"text,omitempty"`
}

// --- agent → client requests (session/request_permission) ---

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
	Kind     string `json:"kind"` // "allow_once" | "allow_always" | "reject_once" | "reject_always"
}

type requestPermissionResult struct {
	Outcome struct {
		Outcome  string `json:"outcome"` // "selected" | "cancelled"
		OptionID string `json:"optionId,omitempty"`
	} `json:"outcome"`
}
