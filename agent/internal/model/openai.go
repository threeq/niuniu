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
}

type oaResponse struct {
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *openaiModel) Complete(ctx context.Context, req Request) (*Response, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	bodyReq := oaRequest{Model: o.cfg.Model, Messages: toOAMessages(req), MaxTokens: maxTok}
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
		Usage:      Usage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens},
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
		var toolCalls []oaToolCall
		var toolResults []Block
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				texts = append(texts, b.Text)
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
		for _, tr := range toolResults {
			out = append(out, oaMessage{Role: "tool", ToolCallID: tr.ToolUseID, Content: tr.Text})
		}
	}
	return out
}
