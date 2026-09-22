package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Request.ContextEditing 在 anthropic 协议注入服务端 context_management
// （clear_tool_uses_20250919），由 provider 自动清理旧工具结果；关闭时
// 请求体不含该字段（openai 族无此特性，本地逐出兜底）。
func TestAnthropicContextEditingInjected(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{BaseURL: ts.URL, APIKey: "k", Model: "m"})
	if m == nil {
		t.Fatal("nil model")
	}

	// 开启：请求体含 context_management。
	if _, err := m.Complete(context.Background(), Request{
		Messages:       []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		MaxTokens:      64,
		ContextEditing: true,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	cm, ok := got["context_management"].(map[string]any)
	if !ok {
		t.Fatalf("no context_management in request: %v", got)
	}
	edits, _ := cm["edits"].([]any)
	if len(edits) != 1 {
		t.Fatalf("edits = %v", cm["edits"])
	}
	e0, _ := edits[0].(map[string]any)
	if e0["type"] != "clear_tool_uses_20250919" {
		t.Errorf("edit type = %v", e0["type"])
	}
	if !strings.Contains(e0["type"].(string), "clear_tool_uses") {
		t.Errorf("unexpected edit type %v", e0["type"])
	}

	// 关闭：不含该字段。
	got = nil
	if _, err := m.Complete(context.Background(), Request{
		Messages:  []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		MaxTokens: 64,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, exists := got["context_management"]; exists {
		t.Error("context_management must be absent when disabled")
	}
}
