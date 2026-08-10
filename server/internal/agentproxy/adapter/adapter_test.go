package adapter

import (
	"testing"
)

func TestFor_RoutesByType(t *testing.T) {
	if For(TypeClaude).Type() != TypeClaude {
		t.Errorf("For(TypeClaude) should return ClaudeAdapter")
	}
	if For(TypeCodex).Type() != TypeCodex {
		t.Errorf("For(TypeCodex) should return CodexAdapter")
	}
	if For(Type("")).Type() != TypeClaude {
		t.Errorf("For empty/unknown type should default to Claude")
	}
	if For(Type("aider")).Type() != TypeClaude {
		t.Errorf("For unknown type should default to Claude")
	}
}

func TestClaudeAdapter_ParseLine_Result(t *testing.T) {
	line := `{"type":"result","is_error":false,"total_cost_usd":0.0123,"num_turns":3,"duration_ms":42000}`
	events, err := ClaudeAdapter{}.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "result" {
		t.Errorf("Type=%q want result", ev.Type)
	}
	if ev.TotalCostUSD != 0.0123 {
		t.Errorf("TotalCostUSD=%v want 0.0123", ev.TotalCostUSD)
	}
}

func TestCodexAdapter_ParseLine_TurnCompleted(t *testing.T) {
	line := `{"type":"turn_completed","input_tokens":100,"output_tokens":200,"total_cost_usd":0.5}`
	events, err := CodexAdapter{}.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "result" {
		t.Errorf("Type=%q want result", events[0].Type)
	}
	if events[0].TotalCostUSD != 0.5 {
		t.Errorf("cost=%v want 0.5", events[0].TotalCostUSD)
	}
}

func TestCodexAdapter_ParseLine_ApprovalRequest(t *testing.T) {
	// codex's app-server emits approval/request as a JSON-RPC notification
	// when approval_policy=on-request. We parse it into a ParsedEvent with
	// ApprovalRequest set so the session layer can bridge to PermissionService.
	line := `{"jsonrpc":"2.0","id":42,"method":"approval/request","params":{"tool":"exec","args":{"command":"rm -rf /tmp/foo"}}}`
	events, err := CodexAdapter{}.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event for approval/request, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "approval_request" {
		t.Errorf("Type=%q want approval_request", ev.Type)
	}
	if ev.ApprovalRequest == nil {
		t.Fatalf("ApprovalRequest payload missing")
	}
	if ev.ApprovalRequest.Tool != "exec" {
		t.Errorf("Tool=%q want exec", ev.ApprovalRequest.Tool)
	}
	if cmd, _ := ev.ApprovalRequest.Args["command"].(string); cmd != "rm -rf /tmp/foo" {
		t.Errorf("Args.command=%q want 'rm -rf /tmp/foo'", cmd)
	}
}

func TestCodexAdapter_ParseLine_AppServerThreadStarted(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"thread/started","params":{"thread":{"id":"thread-123","sessionId":"legacy-session"}}}`
	events, err := CodexAdapter{}.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(events) != 1 || events[0].Type != "system" || events[0].SessionID != "thread-123" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestCodexAdapter_ParseLine_AppServerAgentMessageDelta(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"hello"}}`
	events, err := CodexAdapter{}.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(events) != 1 || events[0].Type != "stream_event" || events[0].DeltaText != "hello" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestCodexAdapter_ParseLine_AppServerCommandItems(t *testing.T) {
	start := `{"jsonrpc":"2.0","method":"item/started","params":{"item":{"id":"cmd-1","type":"commandExecution","command":"echo hi","cwd":"C:/tmp","status":"inProgress"}}}`
	events, err := CodexAdapter{}.ParseLine(start)
	if err != nil {
		t.Fatalf("ParseLine start: %v", err)
	}
	if len(events) != 3 || events[0].Type != "stream_event" || events[0].ToolUseName != "Bash" {
		t.Fatalf("unexpected start events: %#v", events)
	}

	done := `{"jsonrpc":"2.0","method":"item/completed","params":{"item":{"id":"cmd-1","type":"commandExecution","command":"echo hi","aggregatedOutput":"hi\n","exitCode":0,"status":"completed"}}}`
	events, err = CodexAdapter{}.ParseLine(done)
	if err != nil {
		t.Fatalf("ParseLine completed: %v", err)
	}
	if len(events) != 1 || events[0].Type != "user" || len(events[0].ToolResults) != 1 || events[0].ToolResults[0].Content != "hi\n" {
		t.Fatalf("unexpected completed events: %#v", events)
	}
}

func TestCodexAdapter_ParseLine_AppServerTurnCompletedFailure(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn-1","status":"failed","durationMs":123,"error":{"message":"boom"}}}}`
	events, err := CodexAdapter{}.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(events) != 1 || events[0].Type != "result" || !events[0].IsError || events[0].Result != "boom" || events[0].DurationMs != 123 {
		t.Fatalf("unexpected events: %#v", events)
	}
}
