package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

var testPNG = "iVBORw0KGgoAAAANSUhEUg" // truncated base64 for wire assertions

func imgBlock() Block {
	return Block{Type: BlockImage, MIME: "image/png", Media: testPNG}
}

func TestAnthropicImageWire(t *testing.T) {
	var gotBody antRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"seen"}],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, AuthToken: "t", Model: "m"})
	// user 消息带图 + tool_result 带图，两处线格式都要正确。
	_, err := m.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "看图"}, imgBlock()}},
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ID: "tu_1", Name: "Read"}}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "tu_1", Text: "截图", Media: testPNG, MIME: "image/png"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// user content: text + image{source{base64}}。
	u0 := gotBody.Messages[0].Content
	if len(u0) != 2 || u0[1].Type != "image" || u0[1].Source == nil ||
		u0[1].Source.Type != "base64" || u0[1].Source.MediaType != "image/png" || u0[1].Source.Data != testPNG {
		t.Fatalf("user image wire = %+v", u0)
	}
	// tool_result content: text + image。
	tr := gotBody.Messages[2].Content[0]
	if tr.Type != BlockToolResult || len(tr.Content) != 2 ||
		tr.Content[0].Type != BlockText || tr.Content[1].Type != "image" ||
		tr.Content[1].Source == nil || tr.Content[1].Source.Data != testPNG {
		t.Fatalf("tool_result image wire = %+v", tr)
	}
}

func TestOpenAIImageWire(t *testing.T) {
	var gotBody oaRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"seen"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer ts.Close()

	m := NewOpenAI(Config{Provider: ProviderOpenAI, BaseURL: ts.URL, APIKey: "k", Model: "m"})
	_, err := m.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "看图"}, imgBlock()}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// user content 变数组：text + image_url（data URL）。
	c, ok := gotBody.Messages[0].Content.([]any)
	if !ok {
		t.Fatalf("content = %#v, want array", gotBody.Messages[0].Content)
	}
	if len(c) != 2 {
		t.Fatalf("content len = %d", len(c))
	}
	img := c[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Fatalf("img = %v", img)
	}
	url := img["image_url"].(map[string]any)["url"].(string)
	if url != "data:image/png;base64,"+testPNG {
		t.Fatalf("data url = %q", url)
	}
}
