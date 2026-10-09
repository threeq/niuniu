package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	if cfg.Retry.Max == 0 && cfg.Retry.Base == 0 {
		cfg.Retry = DefaultRetryPolicy() // production default; tests set their own
	}
	return &anthropicModel{cfg: cfg, hc: &http.Client{Timeout: 5 * time.Minute}}
}

// Wire types. Block shapes mirror the neutral IR on purpose — except
// tool_result content, which is serialized in array-of-text-blocks form.
// The official API accepts both string and array content for tool_result,
// but Anthropic-compatible gateways (BigModel/GLM and friends) reliably
// handle the array form (what Claude Code itself emits), while the plain
// string form has been observed to silently arrive empty at the model.
// Prompt-cache breakpoints: the Anthropic family caches the longest prefix
// ending at a block/tool/system entry explicitly marked with cache_control
// (type "ephemeral").
//
// GLM 网关实测（open.bigmodel.cn/api/anthropic，GLM-5.3-Flash，2026-09）：
//   - 接受 cache_control，命中时 usage 回报 cache_read_input_tokens，语义与
//     Anthropic 官方一致（命中后 input_tokens 只含未命中尾部，上下文 =
//     input + cache_read + cache_creation）；
//   - 但从不回报 cache_creation_input_tokens（写侧无回执，恒缺省）——所以
//     本地 usage 的 CacheCreationTokens 在 GLM 上通常是 0，不是 bug；
//   - 网关同时存在隐式前缀缓存：完全不带标记的重复请求也能命中（实测第 3、
//     4 连发请求 cache_read=704）。显式断点的价值在「命中位置可预期」，
//     而非开启能力；
//   - 最小门槛低：约 700 token 的前缀即可命中（Anthropic 官方要求 1024+）。
//
// 对完全不认识该字段的兼容网关，多余字段会被忽略，不影响正确性。
type antCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

const ephemeralCache = "ephemeral"

type antBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`      // text / thinking (thinking for the "thinking" field on the wire)
	Signature string `json:"signature,omitempty"` // thinking only
	Thinking  string `json:"thinking,omitempty"`  // thinking only: the wire field name

	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Content   []antBlock      `json:"content,omitempty"` // tool_result only

	// image only
	Source *antImageSource `json:"source,omitempty"`

	CacheControl *antCacheControl `json:"cache_control,omitempty"`
}

type antImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

func antImage(media, mime string) antBlock {
	return antBlock{Type: "image", Source: &antImageSource{Type: "base64", MediaType: mime, Data: media}}
}

type antMessage struct {
	Role    string     `json:"role"`
	Content []antBlock `json:"content"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`

	CacheControl *antCacheControl `json:"cache_control,omitempty"`
}

type antRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    any          `json:"system,omitempty"` // string or []antBlock (array form carries cache_control)
	Messages  []antMessage `json:"messages"`
	Tools     []antTool    `json:"tools,omitempty"`
	// Thinking requests extended reasoning; nil = off. budget_tokens must
	// be strictly less than max_tokens, so the caller budget is bumped
	// above it when needed.
	Thinking *antThinking `json:"thinking,omitempty"`
	// Stream requests SSE (stream:true).
	Stream *bool `json:"stream,omitempty"`
	// ContextManagement enables the provider's server-side context editing:
	// stale tool_use/tool_result pairs are cleared automatically once the
	// trigger fires, with cache-friendly invalidation (edits apply at the
	// cached boundary, so re-cleaning an already-cleaned prefix is free).
	ContextManagement *antContextManagement `json:"context_management,omitempty"`
}

// antContextManagement / antContextEdit / antTokenLimit mirror the
// clear_tool_uses_20250919 strategy schema.
type antContextManagement struct {
	Edits []antContextEdit `json:"edits"`
}

type antContextEdit struct {
	Type            string        `json:"type"` // "clear_tool_uses_20250919"
	Trigger         *antTokenKind `json:"trigger,omitempty"`
	Keep            *antTokenKind `json:"keep,omitempty"`
	ClearAtLeast    *antTokenKind `json:"clear_at_least,omitempty"`
	ExcludeTools    []string      `json:"exclude_tools,omitempty"`
	ClearToolInputs bool          `json:"clear_tool_inputs,omitempty"`
}

type antTokenKind struct {
	Type  string `json:"type"` // "input_tokens" or "tool_uses"
	Value int    `json:"value"`
}

// defaultContextEditing returns the conservative editing profile: kick in at
// 50k input tokens, always keep the 3 most recent tool uses, and clear at
// least 10k tokens per edit so the server never thrashes on tiny clears.
// TodoWrite stays excluded — the task list must survive compaction.
func defaultContextEditing() *antContextManagement {
	return &antContextManagement{Edits: []antContextEdit{{
		Type:         "clear_tool_uses_20250919",
		Trigger:      &antTokenKind{Type: "input_tokens", Value: 50_000},
		Keep:         &antTokenKind{Type: "tool_uses", Value: 3},
		ClearAtLeast: &antTokenKind{Type: "input_tokens", Value: 10_000},
		ExcludeTools: []string{"TodoWrite"},
	}}}
}

type antThinking struct {
	Type         string `json:"type"` // always "enabled"
	BudgetTokens int    `json:"budget_tokens"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Prompt-cache breakdown; both 0 when the gateway does not report it.
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

type antResponse struct {
	Content    []antBlock `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      antUsage   `json:"usage"`
}

func (a *anthropicModel) Complete(ctx context.Context, req Request) (*Response, error) {
	if req.Stream != nil {
		return streamAnthropic(ctx, a, req, req.Stream)
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	bodyReq := antRequest{
		Model:     a.cfg.Model,
		MaxTokens: maxTok,
		System:    cacheableSystem(req.System),
		Messages:  toAntMessages(req.Messages),
	}
	if req.ContextEditing {
		bodyReq.ContextManagement = defaultContextEditing()
	}
	tc := req.Thinking
	if tc.BudgetTokens == 0 {
		tc = a.cfg.Thinking // session-wide default from NIUNIU_AGENT_THINKING
	}
	if tc.BudgetTokens > 0 {
		if bodyReq.MaxTokens <= tc.BudgetTokens {
			// The API requires max_tokens > budget_tokens.
			bodyReq.MaxTokens = req.Thinking.BudgetTokens + 1024
		}
		bodyReq.Thinking = &antThinking{Type: "enabled", BudgetTokens: tc.BudgetTokens}
	}
	for i, t := range req.Tools {
		tool := antTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
		if i == len(req.Tools)-1 {
			// Breakpoint 2 of 3: the tool list is the most stable prefix
			// segment — it changes only when the tool set changes.
			tool.CacheControl = &antCacheControl{Type: ephemeralCache}
		}
		bodyReq.Tools = append(bodyReq.Tools, tool)
	}
	payload, err := json.Marshal(bodyReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	buildReq := func() (*http.Request, error) {
		// Fresh body reader per attempt: the retry layer may replay the
		// request after a 429/5xx.
		return http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/v1/messages", bytes.NewReader(payload))
	}
	httpResp, body, err := doWithRetry(ctx, a.hc, a.cfg.Retry, a.cfg.BaseURL+"/v1/messages", func() (*http.Request, error) {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("anthropic-version", anthropicVersion)
		if a.cfg.AuthToken != "" {
			req.Header.Set("authorization", "Bearer "+a.cfg.AuthToken)
		} else {
			req.Header.Set("x-api-key", a.cfg.APIKey)
		}
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
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
		text := b.Text
		if b.Type == BlockThinking && text == "" {
			text = b.Thinking
		}
		msg.Blocks = append(msg.Blocks, Block{
			Type: b.Type, Text: text, ID: b.ID, Name: b.Name,
			Input: b.Input, ToolUseID: b.ToolUseID, IsError: b.IsError,
			Signature: b.Signature,
		})
	}
	return &Response{
		Message:    msg,
		StopReason: normalizeStop(parsed.StopReason),
		Usage: Usage{
			InputTokens:         parsed.Usage.InputTokens,
			OutputTokens:        parsed.Usage.OutputTokens,
			CacheReadTokens:     parsed.Usage.CacheReadTokens,
			CacheCreationTokens: parsed.Usage.CacheCreationTokens,
		},
	}, nil
}

func toAntMessages(msgs []Message) []antMessage {
	out := make([]antMessage, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]antBlock, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockToolResult:
				// Array content form: text first, then any images the tool
				// returned. Never send an empty string.
				text := b.Text
				if text == "" && b.Media == "" {
					text = "(no output)"
				}
				content := []antBlock{}
				if text != "" {
					content = append(content, antBlock{Type: BlockText, Text: text})
				}
				if b.Media != "" {
					content = append(content, antImage(b.Media, b.MIME))
				}
				blocks = append(blocks, antBlock{
					Type:      b.Type,
					ToolUseID: b.ToolUseID,
					IsError:   b.IsError,
					Content:   content,
				})
			case BlockThinking:
				// A text-less thinking block carries nothing to echo: GLM's
				// stream can open a thinking block without ever sending a
				// thinking_delta, and re-sending it serializes to
				// {"type":"thinking"} (thinking+signature both omitempty) —
				// the gateway rejects that with HTTP 422 "missing field
				// `thinking`", killing the whole run. Drop it. (The official
				// API never emits such a block, so this guard is inert there.)
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				// Echo thinking blocks back verbatim, signature included —
				// gateways verify it and reject altered thinking.
				blocks = append(blocks, antBlock{
					Type: BlockThinking, Thinking: b.Text, Signature: b.Signature,
				})
			case BlockImage:
				blocks = append(blocks, antImage(b.Media, b.MIME))
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
		if len(blocks) == 0 {
			// Every block was dropped (e.g. a lone empty thinking block that
			// carried no delta AND no other block followed). Serializing the
			// message would put "content":[] on the wire — the API requires
			// non-empty content, so skip the message entirely instead of
			// trading the 422 this guard fixes for a 400 on every later round.
			continue
		}
		out = append(out, antMessage{Role: m.Role, Content: blocks})
	}
	// Breakpoint 3 of 3 — the INCREMENTAL checkpoint rides the SECOND-TO-
	// LAST message (when there is one): its content is frozen by the time
	// the next round is built, so the breakpoint lands on identical bytes
	// every round and the read hits; only the trailing delta is written.
	// (Marking the last message instead would move the checkpoint onto
	// fresh bytes each round.) A single-message request degrades to
	// marking that message.
	if n := len(out); n > 0 {
		idx := n - 2
		if n < 2 {
			idx = n - 1
		}
		if last := out[idx].Content; len(last) > 0 {
			last[len(last)-1].CacheControl = &antCacheControl{Type: ephemeralCache}
		}
	}
	return out
}

// cacheableSystem renders the system prompt as a one-block array carrying
// breakpoint 1 of 3 (the string form cannot hold cache_control). Breakpoint
// budget: system + final tool + final message block = 3 ≤ 4.
func cacheableSystem(system string) any {
	if system == "" {
		return nil
	}
	return []antBlock{{
		Type:         BlockText,
		Text:         system,
		CacheControl: &antCacheControl{Type: ephemeralCache},
	}}
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
