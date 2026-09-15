package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// anthropicVersion is the Anthropic API version header value.
const anthropicVersion = "2023-06-01"

// anthropicModel speaks the Anthropic /v1/messages protocol. It also serves
// the many Anthropic-compatible gateways (GLM, and friends) that accept the
// same wire format — possibly with Bearer auth instead of x-api-key.
type anthropicModel struct {
	cfg Config
	hc  *http.Client
}

// NewAnthropic returns a Model speaking the Anthropic /v1/messages protocol.
func NewAnthropic(cfg Config) Model {
	return &anthropicModel{cfg: cfg, hc: &http.Client{Timeout: 5 * time.Minute}}
}

// Wire types. Block shapes mirror the neutral IR on purpose — except
// tool_result content, which is serialized in array-of-text-blocks form.
// The official API accepts both string and array content for tool_result,
// but Anthropic-compatible gateways (BigModel/GLM and friends) reliably
// handle the array form (what Claude Code itself emits), while the plain
// string form has been observed to silently arrive empty at the model.
type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Content   []antBlock      `json:"content,omitempty"` // tool_result only
}

type antMessage struct {
	Role    string     `json:"role"`
	Content []antBlock `json:"content"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type antRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []antMessage `json:"messages"`
	Tools     []antTool    `json:"tools,omitempty"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type antResponse struct {
	Content    []antBlock `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      antUsage   `json:"usage"`
}

func (a *anthropicModel) Complete(ctx context.Context, req Request) (*Response, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	bodyReq := antRequest{
		Model:     a.cfg.Model,
		MaxTokens: maxTok,
		System:    req.System,
		Messages:  toAntMessages(req.Messages),
	}
	for _, t := range req.Tools {
		bodyReq.Tools = append(bodyReq.Tools, antTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	payload, err := json.Marshal(bodyReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if a.cfg.AuthToken != "" {
		httpReq.Header.Set("authorization", "Bearer "+a.cfg.AuthToken)
	} else {
		httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	}
	httpResp, err := a.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("anthropic: read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", httpResp.StatusCode, truncate(string(body), 500))
	}
	var parsed antResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	msg := Message{Role: RoleAssistant}
	for _, b := range parsed.Content {
		msg.Blocks = append(msg.Blocks, Block{
			Type: b.Type, Text: b.Text, ID: b.ID, Name: b.Name,
			Input: b.Input, ToolUseID: b.ToolUseID, IsError: b.IsError,
		})
	}
	return &Response{
		Message:    msg,
		StopReason: normalizeStop(parsed.StopReason),
		Usage:      Usage{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens},
	}, nil
}

func toAntMessages(msgs []Message) []antMessage {
	out := make([]antMessage, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]antBlock, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockToolResult:
				// Array-of-text content form; never send an empty string.
				text := b.Text
				if text == "" {
					text = "(no output)"
				}
				blocks = append(blocks, antBlock{
					Type:      b.Type,
					ToolUseID: b.ToolUseID,
					IsError:   b.IsError,
					Content:   []antBlock{{Type: BlockText, Text: text}},
				})
			default:
				input := b.Input
				if b.Type == BlockToolUse && len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, antBlock{
					Type: b.Type, Text: b.Text, ID: b.ID, Name: b.Name,
					Input: input, ToolUseID: b.ToolUseID, IsError: b.IsError,
				})
			}
		}
		out = append(out, antMessage{Role: m.Role, Content: blocks})
	}
	return out
}

// normalizeStop maps provider stop reasons onto the neutral constants,
// passing anything unknown through verbatim.
func normalizeStop(stop string) string {
	switch stop {
	case "end_turn", "stop":
		return StopEndTurn
	case "tool_use", "tool_calls":
		return StopToolUse
	default:
		return stop
	}
}
