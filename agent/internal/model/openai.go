package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openaiModel speaks the OpenAI /chat/completions protocol, which covers
// OpenAI itself plus the wide family of OpenAI-compatible endpoints.
//
// Prompt caching on this family is IMPLICIT prefix caching: there is no
// cache_control analogue to send. Hits happen automatically when successive
// requests share a long identical prefix, so the adapter keeps the prompt
// layout stable — system message first, tools in registry (sorted) order,
// history strictly appended — and Usage surfaces the provider's
// prompt_tokens_details.cached_tokens when it reports one (OpenAI does;
// some compatible gateways don't — those simply report 0 here).
type openaiModel struct {
	cfg Config
	hc  *http.Client
}

// NewOpenAI returns a Model speaking the OpenAI /chat/completions protocol.
func NewOpenAI(cfg Config) Model {
	return &openaiModel{cfg: cfg, hc: &http.Client{Timeout: 5 * time.Minute}}
}

type oaFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"` // always "function"
	Function oaFunctionCall `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content,omitempty"` // string, or absent for pure tool_calls turns
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	// ReasoningContent is DECODE-only (assistant responses): reasoning
	// models expose their chain-of-thought here. It is deliberately NOT
	// echoed back in history — most OpenAI-compatible endpoints reject a
	// client-supplied reasoning field.
	Reasoning string `json:"reasoning_content,omitempty"`
}

type oaFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaTool struct {
	Type     string        `json:"type"` // "function"
	Function oaFunctionDef `json:"function"`
}

type oaRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	Tools     []oaTool    `json:"tools,omitempty"`
	MaxTokens int         `json:"max_tokens,omitempty"`
	// ReasoningEffort: low | medium | high; empty = provider default.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Stream requests SSE chunks (stream:true).
	Stream *bool `json:"stream,omitempty"`
	// StreamOptions asks the server to append a final usage chunk.
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaResponse struct {
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens       int `json:"prompt_tokens"`
		CompletionTokens   int `json:"completion_tokens"`
		PromptTokensDetail struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (o *openaiModel) Complete(ctx context.Context, req Request) (*Response, error) {
	if req.Stream != nil {
		return streamOpenAI(ctx, o, req, req.Stream)
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	bodyReq := oaRequest{Model: o.cfg.Model, Messages: toOAMessages(req), MaxTokens: maxTok}
	effort := req.Thinking.Effort
	if effort == "" {
		effort = o.cfg.Thinking.Effort // session-wide default
	}
	if effort != "" {
		bodyReq.ReasoningEffort = effort
	}
	for _, t := range req.Tools {
		bodyReq.Tools = append(bodyReq.Tools, oaTool{Type: "function", Function: oaFunctionDef{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema,
		}})
	}
	payload, err := json.Marshal(bodyReq)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+o.cfg.APIKey)
	httpResp, err := o.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: HTTP %d: %s", httpResp.StatusCode, truncate(string(body), 500))
	}
	var parsed oaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("openai: response has no choices: %s", truncate(string(body), 500))
	}
	choice := parsed.Choices[0]
	msg := Message{Role: RoleAssistant}
	// Reasoning models: chain-of-thought arrives before the answer.
	if choice.Message.Reasoning != "" {
		msg.Blocks = append(msg.Blocks, Block{Type: BlockThinking, Text: choice.Message.Reasoning})
	}
	if s, ok := choice.Message.Content.(string); ok && s != "" {
		msg.Blocks = append(msg.Blocks, Block{Type: BlockText, Text: s})
	}
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		msg.Blocks = append(msg.Blocks, Block{
			Type: BlockToolUse, ID: tc.ID, Name: tc.Function.Name,
			Input: json.RawMessage(args),
		})
	}
	return &Response{
		Message:    msg,
		StopReason: normalizeStop(choice.FinishReason),
		Usage: Usage{
			InputTokens:     parsed.Usage.PromptTokens,
			OutputTokens:    parsed.Usage.CompletionTokens,
			CacheReadTokens: parsed.Usage.PromptTokensDetail.CachedTokens,
		},
	}, nil
}

// toOAMessages maps the neutral conversation onto OpenAI's wire shape:
// tool_use blocks become assistant tool_calls, and tool_result blocks inside
// a user message become individual role:"tool" messages.
func toOAMessages(req Request) []oaMessage {
	var out []oaMessage
	if req.System != "" {
		out = append(out, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		var texts []string
		var images []Block
		var toolCalls []oaToolCall
		var toolResults []Block
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				texts = append(texts, b.Text)
			case BlockImage:
				images = append(images, b)
			case BlockToolUse:
				args := string(b.Input)
				if len(b.Input) == 0 {
					args = "{}"
				}
				toolCalls = append(toolCalls, oaToolCall{
					ID: b.ID, Type: "function",
					Function: oaFunctionCall{Name: b.Name, Arguments: args},
				})
			case BlockToolResult:
				toolResults = append(toolResults, b)
			}
		}
		if m.Role == RoleAssistant {
			om := oaMessage{Role: "assistant", ToolCalls: toolCalls}
			if s := strings.Join(texts, "\n"); s != "" {
				om.Content = s
			}
			out = append(out, om)
			continue
		}
		if s := strings.Join(texts, "\n"); s != "" {
			out = append(out, oaMessage{Role: "user", Content: s})
		}
		// User images ride a multipart content array on the last user text
		// (or their own message when the turn is image-only).
		if len(images) > 0 {
			var parts []map[string]any
			if s := strings.Join(texts, "\n"); s != "" {
				parts = append(parts, map[string]any{"type": "text", "text": s})
			}
			for _, im := range images {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + im.MIME + ";base64," + im.Media},
				})
			}
			if len(out) > 0 && out[len(out)-1].Role == "user" {
				out[len(out)-1].Content = parts
			} else {
				out = append(out, oaMessage{Role: "user", Content: parts})
			}
		}
		for _, tr := range toolResults {
			var content any = tr.Text
			if tr.Media != "" {
				arr := []map[string]any{{"type": "text", "text": tr.Text}}
				arr = append(arr, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + tr.MIME + ";base64," + tr.Media},
				})
				content = arr
			}
			out = append(out, oaMessage{Role: "tool", ToolCallID: tr.ToolUseID, Content: content})
		}
	}
	return out
}
