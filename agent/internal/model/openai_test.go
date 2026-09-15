package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompleteToolCalls(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody oaRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",` +
			`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"LS","arguments":"{\"path\":\".\"}"}}]},` +
			`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer ts.Close()

	m := NewOpenAI(Config{Provider: ProviderOpenAI, BaseURL: ts.URL, APIKey: "k", Model: "m1"})
	resp, err := m.Complete(context.Background(), Request{
		System:   "sys",
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		Tools:    []ToolDef{{Name: "LS", Description: "list", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody.Model != "m1" || gotBody.MaxTokens != DefaultMaxTokens {
		t.Errorf("request body = %+v", gotBody)
	}
	// System message maps to role:"system".
	if len(gotBody.Messages) != 2 || gotBody.Messages[0].Role != "system" || gotBody.Messages[1].Role != "user" {
		t.Errorf("messages = %+v", gotBody.Messages)
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Function.Name != "LS" {
		t.Errorf("tools = %+v", gotBody.Tools)
	}

	uses := resp.Message.ToolUses()
	if resp.StopReason != StopToolUse || len(uses) != 1 || uses[0].Name != "LS" || uses[0].ID != "call_1" {
		t.Fatalf("response = %+v", resp)
	}
	if string(uses[0].Input) != `{"path":"."}` {
		t.Errorf("tool input = %s", uses[0].Input)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIToolResultMapping(t *testing.T) {
	var gotBody oaRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()

	m := NewOpenAI(Config{Provider: ProviderOpenAI, BaseURL: ts.URL, APIKey: "k", Model: "m"})
	resp, err := m.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockText, Text: "let me look"},
				{Type: BlockToolUse, ID: "call_1", Name: "LS", Input: json.RawMessage(`{"path":"."}`)},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", Text: "a.txt"}}},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Neutral user-with-tool_result must become: assistant(tool_calls) + role:"tool".
	if len(gotBody.Messages) != 2 {
		t.Fatalf("messages = %+v", gotBody.Messages)
	}
	asst := gotBody.Messages[0]
	if asst.Role != "assistant" || asst.Content != "let me look" || len(asst.ToolCalls) != 1 ||
		asst.ToolCalls[0].Function.Arguments != `{"path":"."}` {
		t.Errorf("assistant message = %+v", asst)
	}
	tool := gotBody.Messages[1]
	if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Content != "a.txt" {
		t.Errorf("tool message = %+v", tool)
	}
	if resp.StopReason != StopEndTurn || resp.Message.Text() != "recovered" {
		t.Errorf("response = %+v", resp)
	}
}

func TestOpenAIHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer ts.Close()

	m := NewOpenAI(Config{Provider: ProviderOpenAI, BaseURL: ts.URL, APIKey: "k", Model: "m"})
	_, err := m.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("want error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to mention HTTP status", err)
	}
}

func TestOpenAICacheUsageParsing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":10,` +
			`"prompt_tokens_details":{"cached_tokens":1234}}}`))
	}))
	defer ts.Close()

	m := NewOpenAI(Config{Provider: ProviderOpenAI, BaseURL: ts.URL, APIKey: "k", Model: "m"})
	resp, err := m.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheReadTokens != 1234 {
		t.Errorf("usage = %+v, want cache read 1234", resp.Usage)
	}
}
