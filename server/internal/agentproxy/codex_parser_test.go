package agentproxy

import (
	"encoding/json"
	"testing"
)

func TestParseCodexJSONLine_TextDelta(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"agent_message_delta","delta":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events len=%d, want 1", len(events))
	}
	ev := events[0]
	if ev.Type != "stream_event" || ev.StreamEventType != "content_block_delta" || ev.DeltaType != "text_delta" || ev.DeltaText != "hello" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestParseCodexJSONLine_SessionConfigured(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"session_configured","session_id":"abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "system" || events[0].Subtype != "init" || events[0].SessionID != "abc" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestParseCodexJSONLine_CommandLifecycle(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"exec_command_begin","call_id":"call-1","command":"go test ./..."}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events len=%d, want 3", len(events))
	}
	if events[0].Type != "stream_event" || events[0].BlockType != "tool_use" || events[0].ToolUseName != "Bash" || events[0].ToolUseId != "call-1" {
		t.Fatalf("unexpected start event: %+v", events[0])
	}
	if events[1].DeltaType != "input_json_delta" || events[2].StreamEventType != "content_block_stop" {
		t.Fatalf("unexpected command events: %+v", events)
	}

	events, err = ParseCodexJSONLine(`{"type":"exec_command_end","call_id":"call-1","exit_code":1,"output":"failed"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "user" || len(events[0].ToolResults) != 1 {
		t.Fatalf("unexpected result events: %+v", events)
	}
	tr := events[0].ToolResults[0]
	if tr.ToolUseId != "call-1" || tr.Content != "failed" || !tr.IsError {
		t.Fatalf("unexpected tool result: %+v", tr)
	}
}

func TestParseCodexJSONLine_DottedItemCompletedAgentMessage(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"hello"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events len=%d, want 1", len(events))
	}
	if events[0].Type != "assistant" || len(events[0].TextBlocks) != 1 || events[0].TextBlocks[0].Text != "hello" {
		t.Fatalf("unexpected assistant event: %+v", events[0])
	}
}

func TestParseCodexJSONLine_DottedCommandExecutionItem(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"git status","aggregated_output":"clean","exit_code":0,"status":"completed"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "user" || len(events[0].ToolResults) != 1 {
		t.Fatalf("unexpected result events: %+v", events)
	}
	if events[0].ToolResults[0].Content != "clean" || events[0].ToolResults[0].IsError {
		t.Fatalf("unexpected command result: %+v", events[0].ToolResults[0])
	}
}

func TestParseCodexJSONLine_DottedTurnFailedNestedError(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"turn.failed","error":{"message":"missing auth"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "result" || !events[0].IsError || events[0].Result != "missing auth" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestParseCodexJSONLine_DottedTurnCompletedUsage(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"turn.completed","usage":{"input_tokens":17002,"cached_input_tokens":12160,"output_tokens":5,"reasoning_output_tokens":0}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "result" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[0].InputTokens != 17002 || events[0].OutputTokens != 5 {
		t.Fatalf("unexpected usage: input=%d output=%d", events[0].InputTokens, events[0].OutputTokens)
	}
}

func TestParseCodexJSONLine_JSONRPCOutputDeltaBase64(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"method":"command/exec/outputDelta","params":{"delta":"aGVsbG8="}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].DeltaText != "hello" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestParseCodexJSONLine_PlanUpdateAsTodoWrite(t *testing.T) {
	events, err := ParseCodexJSONLine(`{"type":"plan_update","plan":[{"step":"Inspect parser","status":"completed"},{"step":"Add tests","status":"in-progress"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events len=%d, want 4: %+v", len(events), events)
	}
	if events[0].Type != "stream_event" || events[0].BlockType != "tool_use" || events[0].ToolUseName != "TodoWrite" {
		t.Fatalf("unexpected start event: %+v", events[0])
	}
	if events[1].DeltaType != "input_json_delta" {
		t.Fatalf("unexpected input event: %+v", events[1])
	}
	var input struct {
		Todos []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(events[1].DeltaText), &input); err != nil {
		t.Fatalf("unmarshal TodoWrite input: %v", err)
	}
	if len(input.Todos) != 2 {
		t.Fatalf("todos len=%d, want 2", len(input.Todos))
	}
	if input.Todos[0].Content != "Inspect parser" || input.Todos[0].Status != "completed" {
		t.Fatalf("unexpected first todo: %+v", input.Todos[0])
	}
	if input.Todos[1].Content != "Add tests" || input.Todos[1].Status != "in_progress" {
		t.Fatalf("unexpected second todo: %+v", input.Todos[1])
	}
	if events[3].Type != "user" || len(events[3].ToolResults) != 1 || events[3].ToolResults[0].Content != "plan updated" {
		t.Fatalf("unexpected result event: %+v", events[3])
	}
}
