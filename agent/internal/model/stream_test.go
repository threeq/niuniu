package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropic SSE：text/thinking 增量回调 + tool_use 分片聚合 + 最终 Response。
func TestAnthropicStreamSSE(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":12}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一下"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-stream-1"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"你好"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"，世界"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_1","name":"LS"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"th\":\".\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":2}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":34}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var deltas []StreamDelta
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{Provider: ProviderAnthropic, BaseURL: ts.URL, AuthToken: "t", Model: "m"})
	resp, err := m.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		Stream:   func(d StreamDelta) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("Complete(stream): %v", err)
	}

	// 增量回调序：thinking → text×2。signature_delta 不是模型输出，不得产生回调。
	if len(deltas) != 3 ||
		deltas[0].Kind != StreamThinking || deltas[0].Text != "想一下" ||
		deltas[1].Kind != StreamText || deltas[1].Text != "你好" ||
		deltas[2].Text != "，世界" {
		t.Fatalf("deltas = %+v", deltas)
	}

	// 最终 Response 与整块等价：thinking + text + 聚合后的 tool_use。
	blocks := resp.Message.Blocks
	if len(blocks) != 3 ||
		blocks[0].Type != BlockThinking || blocks[0].Text != "想一下" ||
		blocks[1].Type != BlockText || blocks[1].Text != "你好，世界" {
		t.Fatalf("blocks = %+v", blocks)
	}
	tu := blocks[2]
	if tu.Type != BlockToolUse || tu.ID != "tu_1" || tu.Name != "LS" || string(tu.Input) != `{"path":"."}` {
		t.Fatalf("aggregated tool_use = %+v", tu)
	}
	// signature_delta 必须落在 thinking 块上：网关在回放工具循环历史时校验签名，
	// 流式路径丢掉它会让整轮请求被拒（非流式路径从整块里读到，故只有流式受影响）。
	if blocks[0].Signature != "sig-stream-1" {
		t.Fatalf("streamed thinking lost its signature: %+v", blocks[0])
	}
	echoed := toAntMessages([]Message{resp.Message})
	if len(echoed) != 1 || len(echoed[0].Content) != 3 ||
		echoed[0].Content[0].Signature != "sig-stream-1" {
		t.Fatalf("echoed history lost the signature: %+v", echoed)
	}
	if resp.StopReason != StopToolUse || resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 34 {
		t.Fatalf("resp meta = %+v", resp)
	}
}

// openai chunk：content/reasoning 增量 + tool_calls 按 index 聚合。
func TestOpenAIStreamChunks(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"想一想"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"he"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"llo"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"LS","arguments":"{\"pa"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\".\"}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	var deltas []StreamDelta
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer ts.Close()

	m := NewOpenAI(Config{Provider: ProviderOpenAI, BaseURL: ts.URL, APIKey: "k", Model: "m"})
	resp, err := m.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		Stream:   func(d StreamDelta) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("Complete(stream): %v", err)
	}
	if len(deltas) != 3 ||
		deltas[0].Kind != StreamThinking || deltas[0].Text != "想一想" ||
		deltas[1].Text != "he" || deltas[2].Text != "llo" {
		t.Fatalf("deltas = %+v", deltas)
	}
	blocks := resp.Message.Blocks
	if len(blocks) != 3 || blocks[0].Type != BlockThinking || blocks[1].Type != BlockText || blocks[1].Text != "hello" {
		t.Fatalf("blocks = %+v", blocks)
	}
	uses := resp.Message.ToolUses()
	if len(uses) != 1 || uses[0].ID != "call_1" || uses[0].Name != "LS" || string(uses[0].Input) != `{"path":"."}` {
		t.Fatalf("aggregated tool_calls = %+v", uses)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
}
