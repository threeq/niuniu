package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Streaming: when Request.Stream is set, the adapter POSTs with
// stream:true and drives the callback with every text/thinking delta as it
// arrives, while accumulating the identical final Response the non-stream
// path returns — callers need no branching. tool_use input JSON arrives in
// shards and is aggregated inside the adapter.

// StreamDelta kinds.
const (
	StreamText     = "text"
	StreamThinking = "thinking"
)

// StreamDelta is one incremental piece of assistant output.
type StreamDelta struct {
	Kind string
	Text string
}

// postSSE sends the payload and invokes onEvent for every SSE data frame
// (event name + raw JSON). Returns on stream end or transport error.
func postSSE(ctx context.Context, hc *http.Client, endpoint string, headers map[string]string, payload []byte, onEvent func(event, data string) error) error {
	// Retry the Do phase (429/5xx/network) exactly like the non-stream path:
	// the onEvent callback cannot be replayed, so retries only happen before
	// the first successful response — a stream that already delivered deltas
	// is never restarted.
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		for k, v := range headers {
			httpReq.Header.Set(k, v)
		}
		resp, err := hc.Do(httpReq)
		if err != nil {
			if attempt < 2 && ctx.Err() == nil {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
				continue
			}
			return err
		}
		if retryableStatus(resp.StatusCode) && attempt < 2 && ctx.Err() == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return fmt.Errorf("POST %s: HTTP %d: %s", endpoint, resp.StatusCode, truncate(string(data), 500))
		}
		return scanSSE(ctx, resp.Body, func() { resp.Body.Close() }, onEvent)
	}
}

func retryableStatus(status int) bool {
	return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
}

func scanSSE(ctx context.Context, body io.Reader, closeBody func(), onEvent func(event, data string) error) error {
	// Event-level idle guard: gateways keep SSE alive with heartbeat bytes
	// (pings / blank lines) even when the MODEL stopped producing — a
	// byte-level idle timeout would be fed forever. The deadline resets only
	// on a real data frame; when it fires, closeBody unblocks the scanner's
	// socket read and the stream fails with an error, letting the turn
	// settle instead of parking the session as "running" forever.
	idle := streamIdleTimeout()
	lastData := time.Now()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if time.Since(lastData) > idle {
					if closeBody != nil {
						closeBody()
					}
					return
				}
			}
		}
	}()

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			// Protocol heartbeats (Anthropic ping frames) keep the socket
			// alive but are NOT model output — they must not feed the idle
			// deadline, or a gateway that stalls the model while pinging
			// would never be detected.
			if strings.Contains(data, `{"type":"ping"}`) {
				continue
			}
			lastData = time.Now()
			if err := onEvent(event, data); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		slog.Warn("stream: scanner ended with error", "err", err)
		return err
	}
	return nil
}

// —— anthropic SSE ——

type antStreamDelta struct {
	Type string `json:"type"`
	// text_delta / thinking_delta
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
	// signature_delta — arrives whole (never sharded), after the block's
	// thinking_delta run.
	Signature string `json:"signature"`
	// input_json_delta
	PartialJSON string `json:"partial_json"`
	// message_delta
	StopReason string `json:"stop_reason"`
}

type antStreamFrame struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	// content_block_start
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *antStreamDelta `json:"delta"`
	// message_start / message_delta
	Message *struct {
		Usage antUsage `json:"usage"`
	} `json:"message"`
	Usage      *antUsage `json:"usage"`
	StopReason string    `json:"stop_reason"`
}

// streamAnthropic runs the SSE conversation and assembles the final
// Response. Accumulation mirrors the non-stream decoder exactly.
func streamAnthropic(ctx context.Context, a *anthropicModel, req Request, onDelta func(StreamDelta)) (*Response, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	bodyReq := antRequest{
		Model: a.cfg.Model, MaxTokens: maxTok, System: cacheableSystem(req.System),
		Messages: toAntMessages(req.Messages), Stream: boolPtr(true),
	}
	for i, t := range req.Tools {
		tool := antTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
		if i == len(req.Tools)-1 {
			tool.CacheControl = &antCacheControl{Type: ephemeralCache}
		}
		bodyReq.Tools = append(bodyReq.Tools, tool)
	}
	payload, err := json.Marshal(bodyReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode stream request: %w", err)
	}
	headers := map[string]string{"Anthropic-Version": anthropicVersion}
	if a.cfg.AuthToken != "" {
		headers["Authorization"] = "Bearer " + a.cfg.AuthToken
	} else {
		headers["X-Api-Key"] = a.cfg.APIKey
	}

	var (
		blocks  []Block
		input   int
		output  int
		stop    string
		pending map[int]*strings.Builder // tool_use index → partial JSON
	)
	pending = map[int]*strings.Builder{}

	err = postSSE(ctx, a.hc, a.cfg.BaseURL+"/v1/messages", headers, payload, func(event, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var f antStreamFrame
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return nil // tolerate keep-alives / unknown frames
		}
		switch f.Type {
		case "message_start":
			if f.Message != nil {
				input = f.Message.Usage.InputTokens
			}
		case "content_block_start":
			if f.ContentBlock == nil {
				return nil
			}
			cb := f.ContentBlock
			blk := Block{Type: cb.Type, ID: cb.ID, Name: cb.Name}
			if cb.Type == BlockToolUse {
				pending[f.Index] = &strings.Builder{}
			}
			for len(blocks) <= f.Index {
				blocks = append(blocks, Block{Type: BlockText})
			}
			blocks[f.Index] = blk
		case "content_block_delta":
			if f.Delta == nil || f.Index >= len(blocks) {
				return nil
			}
			switch f.Delta.Type {
			case "text_delta":
				blocks[f.Index].Text += f.Delta.Text
				if onDelta != nil {
					onDelta(StreamDelta{Kind: StreamText, Text: f.Delta.Text})
				}
			case "thinking_delta":
				blocks[f.Index].Text += f.Delta.Thinking
				if onDelta != nil {
					onDelta(StreamDelta{Kind: StreamThinking, Text: f.Delta.Thinking})
				}
			case "signature_delta":
				// The signature rides its own delta and is NOT streamed to the
				// UI (no onDelta — it is verification data, not output). It
				// must still land on the block: Anthropic verifies it when the
				// thinking block is echoed back in tool-loop history, so
				// dropping it here made streamed turns fail where the
				// non-stream decoder (which reads it off the whole block)
				// succeeded.
				blocks[f.Index].Signature = f.Delta.Signature
			case "input_json_delta":
				if b := pending[f.Index]; b != nil {
					b.WriteString(f.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if f.Index < len(blocks) && blocks[f.Index].Type == BlockToolUse {
				if b := pending[f.Index]; b != nil {
					args := strings.TrimSpace(b.String())
					if args == "" {
						args = "{}"
					}
					blocks[f.Index].Input = json.RawMessage(args)
				}
			}
		case "message_delta":
			if f.Delta != nil && f.Delta.StopReason != "" {
				stop = f.Delta.StopReason
			}
			if f.Usage != nil {
				if f.Usage.OutputTokens > 0 {
					output = f.Usage.OutputTokens
				}
				if f.Usage.InputTokens > 0 {
					input = f.Usage.InputTokens
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Response{
		Message:    Message{Role: RoleAssistant, Blocks: blocks},
		StopReason: normalizeStop(stop),
		Usage:      Usage{InputTokens: input, OutputTokens: output},
	}, nil
}

func boolPtr(b bool) *bool { return &b }

// —— openai SSE ——

type oaStreamFrame struct {
	Usage *struct {
		PromptTokens       int `json:"prompt_tokens"`
		CompletionTokens   int `json:"completion_tokens"`
		PromptTokensDetail struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Choices []struct {
		Delta struct {
			Content   *string `json:"content"`
			Reasoning string  `json:"reasoning_content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// streamOpenAI runs the chunked conversation and assembles the final
// Response (tool_calls aggregated by their stream index).
func streamOpenAI(ctx context.Context, o *openaiModel, req Request, onDelta func(StreamDelta)) (*Response, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	bodyReq := oaRequest{Model: o.cfg.Model, Messages: toOAMessages(req), MaxTokens: maxTok, Stream: boolPtr(true), StreamOptions: &oaStreamOptions{IncludeUsage: true}}
	if effort := req.Thinking.Effort; effort == "" {
		effort = o.cfg.Thinking.Effort
		bodyReq.ReasoningEffort = effort
	} else {
		bodyReq.ReasoningEffort = effort
	}
	for _, t := range req.Tools {
		bodyReq.Tools = append(bodyReq.Tools, oaTool{Type: "function", Function: oaFunctionDef{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema,
		}})
	}
	payload, err := json.Marshal(bodyReq)
	if err != nil {
		return nil, fmt.Errorf("openai: encode stream request: %w", err)
	}

	var (
		blocks    []Block
		texts     strings.Builder
		stop      string
		usage     Usage
		toolOrder []int
		toolMeta  = map[int]*Block{}
		toolArgs  = map[int]*strings.Builder{}
	)
	flushText := func() {
		if texts.Len() > 0 {
			blocks = append(blocks, Block{Type: BlockText, Text: texts.String()})
			texts.Reset()
		}
	}

	err = postSSE(ctx, o.hc, o.cfg.BaseURL+"/chat/completions", map[string]string{
		"Authorization": "Bearer " + o.cfg.APIKey,
	}, payload, func(event, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var f oaStreamFrame
		if json.Unmarshal([]byte(data), &f) != nil || len(f.Choices) == 0 {
			return nil
		}
		d := f.Choices[0].Delta
		if d.Reasoning != "" {
			// Reasoning arrives before the answer; flush text to keep order.
			flushText()
			blocks = append(blocks, Block{Type: BlockThinking, Text: d.Reasoning})
			if onDelta != nil {
				onDelta(StreamDelta{Kind: StreamThinking, Text: d.Reasoning})
			}
		}
		if d.Content != nil && *d.Content != "" {
			texts.WriteString(*d.Content)
			if onDelta != nil {
				onDelta(StreamDelta{Kind: StreamText, Text: *d.Content})
			}
		}
		for _, tc := range d.ToolCalls {
			flushText()
			b, ok := toolMeta[tc.Index]
			if !ok {
				b = &Block{Type: BlockToolUse}
				toolMeta[tc.Index] = b
				toolArgs[tc.Index] = &strings.Builder{}
				toolOrder = append(toolOrder, tc.Index)
			}
			if tc.ID != "" {
				b.ID = tc.ID
			}
			if tc.Function.Name != "" {
				b.Name = tc.Function.Name
			}
			toolArgs[tc.Index].WriteString(tc.Function.Arguments)
		}
		if fr := f.Choices[0].FinishReason; fr != "" {
			stop = fr
		}
		if f.Usage != nil {
			usage = Usage{
				InputTokens:     f.Usage.PromptTokens,
				OutputTokens:    f.Usage.CompletionTokens,
				CacheReadTokens: f.Usage.PromptTokensDetail.CachedTokens,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	flushText()
	for _, idx := range toolOrder {
		b := toolMeta[idx]
		args := strings.TrimSpace(toolArgs[idx].String())
		if args == "" {
			args = "{}"
		}
		b.Input = json.RawMessage(args)
		blocks = append(blocks, *b)
	}
	return &Response{
		Message:    Message{Role: RoleAssistant, Blocks: blocks},
		StopReason: normalizeStop(stop),
		Usage:      usage,
	}, nil
}
