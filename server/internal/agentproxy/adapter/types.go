// Package adapter encapsulates per-CLI parsing and spawn-argument concerns
// so the agentproxy session layer can treat Claude and Codex (and any future
// CLI) symmetrically. See docs/superpowers/specs/2026-05-22-codex-full-support-design.md
// §5 (CLIAdapter abstraction).
//
// Types defined here are re-exported via type aliases in agentproxy/parser.go
// and agentproxy/codex_parser.go so existing call sites keep working without
// changes. New code should prefer the adapter.* qualified names.
package adapter

import "encoding/json"

// ParsedEvent represents a parsed NDJSON line from a CLI's stream output.
// Both Claude (stream-json) and Codex (--json) parsers emit this shape so the
// session layer has one event handler.
type ParsedEvent struct {
	Type    string
	Subtype string

	SessionID string

	ParentToolUseId string

	TextBlocks     []TextBlock
	ToolUseBlocks  []ToolUseBlock
	ThinkingBlocks []ThinkingBlock

	ToolResults []ToolResultBlock

	StreamEventType string
	BlockIndex      int
	BlockType       string
	DeltaType       string
	DeltaText       string
	ToolUseName     string
	ToolUseId       string

	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int

	Result       string
	IsError      bool
	TotalCostUSD float64
	NumTurns     int
	DurationMs   int64

	RateLimitStatus   string
	RateLimitType     string
	RateLimitResetsAt int64
	RateLimitOverage  string
	RateLimitRawJSON  string

	RetryAttempt int
	RetryError   string

	// ApprovalRequest is populated when the adapter emits an event for a CLI
	// approval/permission prompt (codex only as of M2.5.1). The session layer
	// bridges this to PermissionService.Request.
	ApprovalRequest *ApprovalRequest
}

// ApprovalRequest is the codex approval/request payload. id is the JSON-RPC
// request id used to write back the approval/response on stdin.
type ApprovalRequest struct {
	ID   any            // JSON-RPC id (int or string per spec)
	Tool string         // codex tool name: "exec", "patch", "web", ...
	Args map[string]any // tool-specific arguments (file paths, commands, ...)
}

type TextBlock struct {
	Text string
}

type ToolUseBlock struct {
	Id    string
	Name  string
	Input string
}

type ThinkingBlock struct {
	Thinking string
}

type ToolResultBlock struct {
	ToolUseId string
	Content   string
	IsError   bool
}

// --- Claude stream-json internal types (moved from agentproxy/parser.go) ---

type rawLine struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	SessionID       string `json:"session_id"`
	ParentToolUseID string `json:"parent_tool_use_id"`

	Message struct {
		Role    string            `json:"role"`
		Content rawMessageContent `json:"content"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`

	Event *rawStreamEvent `json:"event"`

	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	DurationMs   int64   `json:"duration_ms"`

	Usage *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`

	RateLimitInfo       *json.RawMessage `json:"rate_limit_info"`
	RateLimitInfoParsed struct {
		Status                string `json:"status"`
		ResetsAt              int64  `json:"resetsAt"`
		RateLimitType         string `json:"rateLimitType"`
		OverageStatus         string `json:"overageStatus"`
		OverageDisabledReason string `json:"overageDisabledReason"`
		IsUsingOverage        bool   `json:"isUsingOverage"`
	}

	Attempt int    `json:"attempt"`
	Error   string `json:"error"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Id        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	Thinking  string          `json:"thinking"`
	ToolUseId string          `json:"tool_use_id"`
	Content   rawContent      `json:"content"`
	IsError   bool            `json:"is_error"`
}

type rawMessageContent struct {
	Blocks []contentBlock
	Text   string
}

func (r *rawMessageContent) UnmarshalJSON(data []byte) error {
	var blocks []contentBlock
	if err := json.Unmarshal(data, &blocks); err == nil {
		r.Blocks = blocks
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		r.Text = s
		return nil
	}
	return nil
}

type rawContent struct {
	Text string
}

func (r *rawContent) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		r.Text = s
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" {
				r.Text += b.Text
			}
		}
		return nil
	}
	r.Text = string(data)
	return nil
}

type rawStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Id   string `json:"id"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
	} `json:"delta"`
	Message *struct {
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
