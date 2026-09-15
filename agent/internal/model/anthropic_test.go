package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicCompleteToolUse(t *testing.T) {
	var gotPath, gotAuth, gotKey, gotVersion string
	var gotBody antRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"tu_1","name":"LS","input":{"path":"."}}],` +
			`"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, AuthToken: "tok", Model: "test-model"})
	resp, err := m.Complete(context.Background(), Request{
		System:   "sys",
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		Tools:    []ToolDef{{Name: "LS", Description: "list", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok (auth-token gateways)", gotAuth)
	}
	if gotKey != "" {
		t.Errorf("x-api-key = %q, want empty when auth token is set", gotKey)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q", gotVersion)
	}
	if gotBody.Model != "test-model" || gotBody.MaxTokens != DefaultMaxTokens {
		t.Errorf("request body = %+v", gotBody)
	}
	// System rides as a one-block array (cache_control needs the array form).
	sysRaw, _ := json.Marshal(gotBody.System)
	var sysBlocks []antBlock
	if json.Unmarshal(sysRaw, &sysBlocks) != nil || len(sysBlocks) != 1 || sysBlocks[0].Text != "sys" {
		t.Errorf("system = %#v, want [text:sys]", gotBody.System)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != RoleUser || gotBody.Messages[0].Content[0].Text != "hi" {
		t.Errorf("messages = %+v", gotBody.Messages)
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Name != "LS" || string(gotBody.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("tools = %+v", gotBody.Tools)
	}

	uses := resp.Message.ToolUses()
	if resp.StopReason != StopToolUse || len(uses) != 1 || uses[0].Name != "LS" || uses[0].ID != "tu_1" {
		t.Fatalf("response = %+v", resp)
	}
	if string(uses[0].Input) != `{"path":"."}` {
		t.Errorf("tool input = %s", uses[0].Input)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestAnthropicToolResultRoundTrip(t *testing.T) {
	var gotBody antRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, APIKey: "k", Model: "m"})
	resp, err := m.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ID: "tu_1", Name: "LS"}}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "tu_1", Text: "a.txt", IsError: true}}},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(gotBody.Messages) != 2 {
		t.Fatalf("messages = %+v", gotBody.Messages)
	}
	// A tool_use with no input must serialize as an empty JSON object.
	if got := string(gotBody.Messages[0].Content[0].Input); got != `{}` {
		t.Errorf("tool_use input = %s, want {}", got)
	}
	tr := gotBody.Messages[1].Content[0]
	if tr.Type != BlockToolResult || tr.ToolUseID != "tu_1" || !tr.IsError {
		t.Errorf("tool_result block = %+v", tr)
	}
	// Content must be the array-of-text form — gateways drop the string form.
	if len(tr.Content) != 1 || tr.Content[0].Type != BlockText || tr.Content[0].Text != "a.txt" {
		t.Errorf("tool_result content = %+v", tr.Content)
	}
	if resp.StopReason != StopEndTurn || resp.Message.Text() != "done" {
		t.Errorf("response = %+v", resp)
	}
}

func TestAnthropicHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, APIKey: "bad", Model: "m"})
	_, err := m.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("want error on HTTP 401")
	}
}

func TestAnthropicCacheUsageParsing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":100,"output_tokens":10,` +
			`"cache_read_input_tokens":1234,"cache_creation_input_tokens":56}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, AuthToken: "t", Model: "m"})
	resp, err := m.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheReadTokens != 1234 || resp.Usage.CacheCreationTokens != 56 {
		t.Errorf("usage = %+v, want cache read 1234 / creation 56", resp.Usage)
	}
}

func TestAnthropicCacheControlBreakpoints(t *testing.T) {
	var gotBody antRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, AuthToken: "t", Model: "m"})
	_, err := m.Complete(context.Background(), Request{
		System: "sys",
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "q1"}}},
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "a1"}}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "q2"}}},
		},
		Tools: []ToolDef{
			{Name: "A", Description: "a", InputSchema: json.RawMessage(`{}`)},
			{Name: "B", Description: "b", InputSchema: json.RawMessage(`{}`)},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	breakpoints := 0
	// system：数组形式，块上带 cache_control（字符串形式无法挂断点）。
	sysRaw, _ := json.Marshal(gotBody.System)
	var sysBlocks []antBlock
	if json.Unmarshal(sysRaw, &sysBlocks) != nil || len(sysBlocks) != 1 || sysBlocks[0].Text != "sys" {
		t.Fatalf("system = %#v, want one text block with sys", gotBody.System)
	}
	if sysBlocks[0].CacheControl == nil {
		t.Error("system block missing cache_control")
	} else {
		breakpoints++
	}
	// tools：仅最后一个工具带 cache_control（工具列表是最稳定的前缀）。
	if gotBody.Tools[0].CacheControl != nil {
		t.Error("non-final tool must not carry cache_control")
	}
	if gotBody.Tools[1].CacheControl == nil {
		t.Error("final tool missing cache_control")
	} else {
		breakpoints++
	}
	// messages：仅最后一条消息的最后一个块带 cache_control（增量断点）。
	for i, msg := range gotBody.Messages {
		last := i == len(gotBody.Messages)-1
		for j, blk := range msg.Content {
			isLastBlock := last && j == len(msg.Content)-1
			if blk.CacheControl != nil && !isLastBlock {
				t.Errorf("messages[%d].content[%d] carries unexpected cache_control", i, j)
			}
			if blk.CacheControl != nil {
				breakpoints++
			}
		}
	}
	if gotBody.Messages[2].Content[0].CacheControl == nil {
		t.Error("final message missing incremental cache_control breakpoint")
	}
	if breakpoints > 4 {
		t.Errorf("cache_control breakpoints = %d, want <= 4", breakpoints)
	}
}
