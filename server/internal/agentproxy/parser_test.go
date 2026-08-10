package agentproxy

import (
	"testing"
)

func TestParseStreamLine(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		check   func(t *testing.T, got ParsedEvent)
		wantErr bool
	}{
		{
			name:  "init event extracts session_id",
			input: `{"type":"system","subtype":"init","session_id":"abc-123","cwd":"/tmp"}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "system")
				assertEqual(t, "Subtype", got.Subtype, "init")
				assertEqual(t, "SessionID", got.SessionID, "abc-123")
			},
		},
		{
			name:  "system api_retry event",
			input: `{"type":"system","subtype":"api_retry","attempt":2,"error":"rate_limit","session_id":"abc"}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "system")
				assertEqual(t, "Subtype", got.Subtype, "api_retry")
				if got.RetryAttempt != 2 {
					t.Errorf("RetryAttempt = %d, want 2", got.RetryAttempt)
				}
				assertEqual(t, "RetryError", got.RetryError, "rate_limit")
			},
		},
		{
			name:  "assistant event extracts text blocks",
			input: `{"type":"assistant","message":{"content":[{"type":"text","text":"hello world"}]},"session_id":"abc-123"}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "assistant")
				assertEqual(t, "SessionID", got.SessionID, "abc-123")
				if len(got.TextBlocks) != 1 || got.TextBlocks[0].Text != "hello world" {
					t.Errorf("TextBlocks = %v, want [{hello world}]", got.TextBlocks)
				}
			},
		},
		{
			name:  "assistant event extracts tool_use blocks",
			input: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/foo.go"}},{"type":"text","text":"done"}]},"session_id":"abc-123"}`,
			check: func(t *testing.T, got ParsedEvent) {
				if len(got.ToolUseBlocks) != 1 {
					t.Fatalf("ToolUseBlocks len = %d, want 1", len(got.ToolUseBlocks))
				}
				assertEqual(t, "ToolUse.Name", got.ToolUseBlocks[0].Name, "Read")
				assertEqual(t, "ToolUse.Id", got.ToolUseBlocks[0].Id, "toolu_1")
				if len(got.TextBlocks) != 1 || got.TextBlocks[0].Text != "done" {
					t.Errorf("TextBlocks = %v, want [{done}]", got.TextBlocks)
				}
			},
		},
		{
			name:  "assistant event extracts thinking blocks",
			input: `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"let me think..."},{"type":"text","text":"answer"}]},"session_id":"abc"}`,
			check: func(t *testing.T, got ParsedEvent) {
				if len(got.ThinkingBlocks) != 1 || got.ThinkingBlocks[0].Thinking != "let me think..." {
					t.Errorf("ThinkingBlocks = %v", got.ThinkingBlocks)
				}
				if len(got.TextBlocks) != 1 || got.TextBlocks[0].Text != "answer" {
					t.Errorf("TextBlocks = %v", got.TextBlocks)
				}
			},
		},
		{
			name:  "user event extracts tool_result",
			input: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents here","is_error":false}]}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "user")
				if len(got.ToolResults) != 1 {
					t.Fatalf("ToolResults len = %d, want 1", len(got.ToolResults))
				}
				assertEqual(t, "ToolUseId", got.ToolResults[0].ToolUseId, "toolu_1")
				assertEqual(t, "Content", got.ToolResults[0].Content, "file contents here")
			},
		},
		{
			name:  "stream_event content_block_start text",
			input: `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "stream_event")
				assertEqual(t, "StreamEventType", got.StreamEventType, "content_block_start")
				assertEqual(t, "BlockType", got.BlockType, "text")
			},
		},
		{
			name:  "stream_event content_block_delta text",
			input: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "StreamEventType", got.StreamEventType, "content_block_delta")
				assertEqual(t, "DeltaType", got.DeltaType, "text_delta")
				assertEqual(t, "DeltaText", got.DeltaText, "hello")
			},
		},
		{
			name:  "stream_event content_block_start tool_use",
			input: `{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash"}}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "BlockType", got.BlockType, "tool_use")
				assertEqual(t, "ToolUseName", got.ToolUseName, "Bash")
				assertEqual(t, "ToolUseId", got.ToolUseId, "toolu_1")
			},
		},
		{
			name:  "result success event",
			input: `{"type":"result","subtype":"success","is_error":false,"result":"final answer","total_cost_usd":0.05,"num_turns":3,"duration_ms":5000,"session_id":"abc-123"}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "result")
				assertEqual(t, "Result", got.Result, "final answer")
				if got.IsError {
					t.Error("IsError should be false")
				}
				if got.TotalCostUSD != 0.05 {
					t.Errorf("TotalCostUSD = %v, want 0.05", got.TotalCostUSD)
				}
				if got.NumTurns != 3 {
					t.Errorf("NumTurns = %d, want 3", got.NumTurns)
				}
			},
		},
		{
			name:  "result error event",
			input: `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"max turns","session_id":"abc-123"}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "result")
				if !got.IsError {
					t.Error("IsError should be true")
				}
				assertEqual(t, "Subtype", got.Subtype, "error_max_turns")
			},
		},
		{
			name:  "rate_limit_event",
			input: `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "rate_limit_event")
				assertEqual(t, "RateLimitStatus", got.RateLimitStatus, "allowed_warning")
			},
		},
		{
			name:  "stream_event message_start with input_tokens",
			input: `{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":12345,"output_tokens":0}}}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "StreamEventType", got.StreamEventType, "message_start")
				if got.InputTokens != 12345 {
					t.Errorf("InputTokens = %d, want 12345", got.InputTokens)
				}
			},
		},
		{
			name:  "stream_event message_delta with output_tokens",
			input: `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":678}}}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "StreamEventType", got.StreamEventType, "message_delta")
				if got.OutputTokens != 678 {
					t.Errorf("OutputTokens = %d, want 678", got.OutputTokens)
				}
			},
		},
		{
			name:  "result event with usage tokens",
			input: `{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.1,"num_turns":2,"duration_ms":3000,"usage":{"input_tokens":50000,"output_tokens":2000}}`,
			check: func(t *testing.T, got ParsedEvent) {
				if got.InputTokens != 50000 {
					t.Errorf("InputTokens = %d, want 50000", got.InputTokens)
				}
				if got.OutputTokens != 2000 {
					t.Errorf("OutputTokens = %d, want 2000", got.OutputTokens)
				}
			},
		},
		{
			name:    "invalid JSON returns error",
			input:   `not json at all`,
			wantErr: true,
		},
		{
			name:  "empty object returns empty event",
			input: `{}`,
			check: func(t *testing.T, got ParsedEvent) {
				assertEqual(t, "Type", got.Type, "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStreamLine(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseStreamLine() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", field, got, want)
	}
}

func TestParseStreamLine_ParentToolUseId(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantPtid string
	}{
		{
			name:     "assistant message with parent_tool_use_id",
			line:     `{"type":"assistant","parent_tool_use_id":"toolu_abc123","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
			wantPtid: "toolu_abc123",
		},
		{
			name:     "stream_event with parent_tool_use_id",
			line:     `{"type":"stream_event","parent_tool_use_id":"toolu_xyz","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
			wantPtid: "toolu_xyz",
		},
		{
			name:     "no parent (top-level)",
			line:     `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
			wantPtid: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseStreamLine(tc.line)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if ev.ParentToolUseId != tc.wantPtid {
				t.Errorf("ParentToolUseId = %q, want %q", ev.ParentToolUseId, tc.wantPtid)
			}
		})
	}
}
