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
	if gotBody.Model != "test-model" || gotBody.System != "sys" || gotBody.MaxTokens != DefaultMaxTokens {
		t.Errorf("request body = %+v", gotBody)
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
