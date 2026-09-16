// Package model defines the neutral message/conversation types shared by the
// agent loop and the provider adapters, plus the Model contract every
// provider backend implements.
//
// The neutral IR is deliberately Anthropic-shaped (tool results live as
// blocks inside user messages); adapters for other wire formats re-map. Two
// adapter families cover the ecosystem: Anthropic /v1/messages and
// OpenAI /chat/completions — GLM, Qwen, DeepSeek and friends expose one of
// the two.
package model

import (
	"context"
	"encoding/json"
	"strings"
)

// Block types.
const (
	BlockText       = "text"
	BlockThinking   = "thinking"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Stop reasons, normalized across providers.
const (
	StopEndTurn = "end_turn"
	StopToolUse = "tool_use"
)

// DefaultMaxTokens is the per-request output budget when Request.MaxTokens
// is unset.
const DefaultMaxTokens = 8192

// Block is one content block inside a message. Only the fields relevant to
// its Type are populated.
type Block struct {
	Type string `json:"type"`

	// BlockText.
	Text string `json:"text,omitempty"`

	// BlockToolUse.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// BlockToolResult.
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// BlockThinking: provider signature proving the thinking content is
	// unmodified. Anthropic REQUIRES it echoed back verbatim in tool-loop
	// history; keep it intact or the gateway rejects the request.
	Signature string `json:"signature,omitempty"`
}

// ThinkingConfig carries the reasoning budget/effort for one request.
// Anthropic family uses BudgetTokens (thinking: {type: enabled, budget_tokens});
// OpenAI family uses Effort (reasoning_effort: low|medium|high). Zero value
// = provider default (thinking off for Anthropic unless configured).
type ThinkingConfig struct {
	BudgetTokens int
	Effort       string
}

// Message is one conversation turn. Tool results are carried as
// BlockToolResult blocks inside a RoleUser message; adapters re-map as the
// wire format requires.
type Message struct {
	Role   string  `json:"role"`
	Blocks []Block `json:"blocks"`
}

// Text returns the concatenation of the message's text blocks.
func (m Message) Text() string {
	var b strings.Builder
	for _, blk := range m.Blocks {
		if blk.Type == BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// ToolUses returns the message's tool_use blocks.
func (m Message) ToolUses() []Block {
	var out []Block
	for _, blk := range m.Blocks {
		if blk.Type == BlockToolUse {
			out = append(out, blk)
		}
	}
	return out
}

// ToolDef describes a tool to the model; InputSchema is a JSON Schema object
// describing the tool input.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Request is one completion request.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	Thinking  ThinkingConfig
}

// Usage is token accounting for one request.
//
// CacheReadTokens/CacheCreationTokens carry the provider's prompt-cache
// breakdown when reported (Anthropic: cache_read_input_tokens /
// cache_creation_input_tokens; OpenAI-compatible: prompt_tokens_details.
// cached_tokens). Anthropic's input_tokens EXCLUDES cached tokens, so one
// request's context size is InputTokens+CacheReadTokens+CacheCreationTokens.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// ContextTokens approximates the conversation size this request saw.
func (u Usage) ContextTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// Response is one completion result.
type Response struct {
	Message    Message
	StopReason string
	Usage      Usage
}

// Model is the contract every provider adapter implements. Complete performs
// one non-streaming request; streaming arrives in P1.
type Model interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

// truncate shortens s to at most n bytes for error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
