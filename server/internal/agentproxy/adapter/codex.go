package adapter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// CodexAdapter implements Adapter for `codex exec --json`. Behavior is
// bit-identical to the pre-M2.5.1 agentproxy.ParseCodexJSONLine.
type CodexAdapter struct{}

func (CodexAdapter) Type() Type { return TypeCodex }

func (CodexAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	return ParseCodexJSONLine(line)
}

// EncodeApprovalResponse builds the JSON-RPC `approval/response` payload for
// writing back to codex's stdin. Codex correlates the response to the
// originating request via the JSON-RPC `id` field, which we round-trip
// untouched from the request through ApprovalRequest.ID.
//
// Codex's app-server protocol accepts a "decision" enum in the result body:
// "approved", "approved_for_session", "denied". We collapse the niuniu
// PermissionService boolean decision (approved or not) to this enum:
// true -> "approved", false -> "denied". `approved_for_session` (sticky
// allowlist) is a future extension; M2.5.1 routes that through the existing
// niuniu permission_allowlist instead so claude/codex share one store.
//
// CAVEAT (M2.5.1): the exact wire shape (top-level `result.decision`,
// `result.approved` boolean, or a separate `method:"approval/response"`
// envelope) varies across codex CLI 0.x releases. The shape encoded here
// matches the codex 0.75-ish app-server schema documented when this work
// was written. If codex changes the contract, this function and
// codex_approval_test.go are the only places to update. The agentproxy
// session layer treats unrecognized approval payloads as auto-deny (safe
// default) so a wire-shape regression fails closed rather than open. The
// `codex-approval.sh` smoke (TODO M2.5.2) should exercise a live codex
// before this is relied upon in production.
func EncodeApprovalResponse(id any, approved bool) ([]byte, error) {
	decision := "denied"
	if approved {
		decision = "approved"
	}
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"decision": decision},
	})
}

// ParseCodexJSONLine parses one JSONL event emitted by `codex exec --json`.
// Codex has changed event names across releases, so this parser accepts both
// the current type-based events and the app-server JSON-RPC style notifications
// referenced by the original integration design.
func ParseCodexJSONLine(line string) ([]ParsedEvent, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, err
	}
	if method, _ := raw["method"].(string); method != "" {
		return parseCodexJSONRPC(method, raw["id"], asMap(raw["params"])), nil
	}
	typ, _ := raw["type"].(string)
	if typ == "" {
		return nil, nil
	}
	return parseCodexTypeEvent(typ, raw), nil
}

func parseCodexJSONRPC(method string, id any, params map[string]any) []ParsedEvent {
	switch method {
	case "session/configured", "session/started":
		return []ParsedEvent{{Type: "system", Subtype: "init", SessionID: firstString(params, "session_id", "sessionId", "id")}}
	case "thread/started":
		thread := asMap(params["thread"])
		sessionID := firstString(thread, "id", "sessionId")
		if sessionID == "" {
			sessionID = firstString(params, "threadId", "thread_id", "id")
		}
		return []ParsedEvent{{Type: "system", Subtype: "init", SessionID: sessionID}}
	case "item/agentMessage/delta", "agentMessage/delta":
		text := firstString(params, "delta", "text")
		if text == "" {
			return nil
		}
		return textDeltaEvents(text)
	case "command/exec/outputDelta", "process/outputDelta":
		text := firstString(params, "delta", "text", "output")
		if decoded := maybeBase64(text); decoded != "" {
			text = decoded
		}
		if text == "" {
			return nil
		}
		return textDeltaEvents(text)
	case "process/exited":
		code := intValue(params["exit_code"])
		if code == 0 {
			return []ParsedEvent{{Type: "result", Result: firstString(params, "message", "result")}}
		}
		msg := firstString(params, "message", "error")
		if msg == "" {
			msg = fmt.Sprintf("codex process exited with code %d", code)
		}
		return []ParsedEvent{{Type: "result", IsError: true, Result: msg}}
	case "error":
		return []ParsedEvent{{Type: "result", IsError: true, Result: firstString(params, "message", "error")}}
	case "plan/update", "update_plan", "plan_update":
		return codexPlanEvents(params)
	case "turn/plan/updated":
		return codexPlanEvents(params)
	case "item/started":
		return codexItemEvents(asMap(params["item"]), false)
	case "item/completed":
		return codexItemEvents(asMap(params["item"]), true)
	case "turn/completed":
		turn := asMap(params["turn"])
		status := firstString(turn, "status")
		if status == "failed" {
			msg := firstString(asMap(turn["error"]), "message", "additionalDetails")
			if msg == "" {
				msg = "codex turn failed"
			}
			return []ParsedEvent{{Type: "result", IsError: true, Result: msg, DurationMs: int64Value(turn["durationMs"])}}
		}
		return []ParsedEvent{{Type: "result", DurationMs: int64Value(turn["durationMs"])}}
	case "approval/request", "approval.request":
		// Codex requests approval for a privileged action (exec / patch / web).
		// Session layer bridges this to niuniu PermissionService via
		// the ApprovalRequest payload. Args is the tool-specific input which
		// the SPA renders for the user to inspect before approving.
		tool := firstString(params, "tool", "kind", "type")
		args := asMap(params["args"])
		if args == nil {
			args = asMap(params["input"])
		}
		if args == nil {
			args = params // fallback: stash entire params as args
		}
		return []ParsedEvent{{
			Type: "approval_request",
			ApprovalRequest: &ApprovalRequest{
				ID:   id,
				Tool: tool,
				Args: args,
			},
		}}
	default:
		return nil
	}
}

func parseCodexTypeEvent(typ string, raw map[string]any) []ParsedEvent {
	switch typ {
	case "session_configured", "session_started", "thread.started":
		return []ParsedEvent{{Type: "system", Subtype: "init", SessionID: firstString(raw, "session_id", "sessionId", "thread_id", "id")}}
	case "agent_message_delta", "message_delta", "response.output_text.delta":
		text := firstString(raw, "delta", "text")
		if text == "" {
			return nil
		}
		return textDeltaEvents(text)
	case "agent_message", "final_message", "message", "response.output_text.done":
		text := firstString(raw, "message", "text", "content")
		if text == "" {
			return nil
		}
		return []ParsedEvent{{Type: "assistant", TextBlocks: []TextBlock{{Text: text}}}}
	case "exec_command_begin", "exec_command_start", "command_started":
		return codexToolUseEvents(raw, false)
	case "exec_command_end", "exec_command_finished", "command_finished":
		return codexToolResultEvents(raw)
	case "exec_command_output_delta", "command_output_delta":
		text := firstString(raw, "delta", "text", "output")
		if decoded := maybeBase64(text); decoded != "" {
			text = decoded
		}
		if text == "" {
			return nil
		}
		return []ParsedEvent{{
			Type: "user",
			ToolResults: []ToolResultBlock{{
				ToolUseId: firstString(raw, "call_id", "tool_use_id", "id"),
				Content:   text,
			}},
		}}
	case "plan_update", "update_plan":
		return codexPlanEvents(raw)
	case "item_started", "item.started":
		return codexToolUseEvents(asMap(raw["item"]), true)
	case "item_completed", "item.completed":
		item := asMap(raw["item"])
		if len(item) == 0 {
			item = raw
		}
		if isCommandLike(item) {
			return codexToolResultEvents(item)
		}
		text := firstString(item, "text", "message", "content")
		if text != "" {
			return []ParsedEvent{{Type: "assistant", TextBlocks: []TextBlock{{Text: text}}}}
		}
		return nil
	case "turn_completed", "turn.completed", "task_complete", "result", "done":
		usage := asMap(raw["usage"])
		return []ParsedEvent{{
			Type:         "result",
			Result:       firstString(raw, "result", "message", "text"),
			InputTokens:  firstNonZeroInt(raw["input_tokens"], usage["input_tokens"]),
			OutputTokens: firstNonZeroInt(raw["output_tokens"], usage["output_tokens"]),
			// codex does not report cache_creation; best-effort map cached read.
			CacheReadTokens: firstNonZeroInt(raw["cached_input_tokens"], usage["cached_input_tokens"], usage["cache_read_input_tokens"]),
			TotalCostUSD:    floatValue(raw["total_cost_usd"]),
			NumTurns:        intValue(raw["num_turns"]),
			DurationMs:      int64Value(raw["duration_ms"]),
		}}
	case "turn_failed", "turn.failed", "error":
		msg := firstString(raw, "message", "error", "result")
		if msg == "" {
			msg = firstString(asMap(raw["error"]), "message", "error")
		}
		if msg == "" {
			msg = "codex turn failed"
		}
		return []ParsedEvent{{Type: "result", IsError: true, Result: msg}}
	default:
		return nil
	}
}

func codexItemEvents(item map[string]any, completed bool) []ParsedEvent {
	if len(item) == 0 {
		return nil
	}
	switch firstString(item, "type") {
	case "agentMessage":
		text := firstString(item, "text", "message", "content")
		if text == "" {
			return nil
		}
		return []ParsedEvent{{Type: "assistant", TextBlocks: []TextBlock{{Text: text}}}}
	case "plan":
		return codexPlanEvents(map[string]any{"items": []any{item}})
	case "commandExecution":
		if completed {
			return codexToolResultEvents(item)
		}
		return codexToolUseEvents(item, true)
	case "mcpToolCall", "dynamicToolCall":
		return codexMCPToolEvents(item, completed)
	case "fileChange":
		return codexFileChangeEvents(item, completed)
	default:
		return nil
	}
}

func textDeltaEvents(text string) []ParsedEvent {
	return []ParsedEvent{{
		Type:            "stream_event",
		StreamEventType: "content_block_delta",
		BlockIndex:      0,
		DeltaType:       "text_delta",
		DeltaText:       text,
	}}
}

func codexPlanEvents(raw map[string]any) []ParsedEvent {
	items := firstArray(raw, "plan", "todos", "items")
	if len(items) == 0 {
		return nil
	}
	type todo struct {
		ID         string `json:"id,omitempty"`
		Content    string `json:"content"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm,omitempty"`
	}
	todos := make([]todo, 0, len(items))
	for i, item := range items {
		m := asMap(item)
		content := firstString(m, "step", "content", "text", "title")
		if content == "" {
			continue
		}
		status := normalizeCodexPlanStatus(firstString(m, "status", "state"))
		if status == "" {
			status = "pending"
		}
		id := firstString(m, "id", "step_id")
		if id == "" {
			id = fmt.Sprintf("codex-plan-%d", i+1)
		}
		todos = append(todos, todo{
			ID:         id,
			Content:    content,
			Status:     status,
			ActiveForm: firstString(m, "activeForm", "active_form", "active"),
		})
	}
	if len(todos) == 0 {
		return nil
	}
	inputJSON, _ := json.Marshal(map[string]any{"todos": todos})
	id := firstString(raw, "call_id", "tool_use_id", "id")
	if id == "" {
		id = "codex-plan"
	}
	return []ParsedEvent{
		{Type: "stream_event", StreamEventType: "content_block_start", BlockIndex: 1001, BlockType: "tool_use", ToolUseName: "TodoWrite", ToolUseId: id},
		{Type: "stream_event", StreamEventType: "content_block_delta", BlockIndex: 1001, DeltaType: "input_json_delta", DeltaText: string(inputJSON)},
		{Type: "stream_event", StreamEventType: "content_block_stop", BlockIndex: 1001},
		{Type: "user", ToolResults: []ToolResultBlock{{ToolUseId: id, Content: "plan updated"}}},
	}
}

func normalizeCodexPlanStatus(status string) string {
	switch status {
	case "completed", "complete", "done", "success":
		return "completed"
	case "in_progress", "in-progress", "running", "active":
		return "in_progress"
	case "pending", "todo", "queued":
		return "pending"
	default:
		return status
	}
}

func codexToolUseEvents(raw map[string]any, fromItem bool) []ParsedEvent {
	if len(raw) == 0 || !isCommandLike(raw) {
		return nil
	}
	id := firstString(raw, "call_id", "tool_use_id", "id")
	if id == "" {
		id = "codex-command"
	}
	input := map[string]any{"command": commandString(raw)}
	if cwd := firstString(raw, "cwd", "workdir", "working_dir"); cwd != "" {
		input["cwd"] = cwd
	}
	inputJSON, _ := json.Marshal(input)
	name := "Bash"
	if fromItem {
		name = firstString(raw, "name", "tool_name")
		if name == "" {
			name = "Bash"
		}
		if firstString(raw, "type") == "commandExecution" {
			name = "Bash"
		}
	}
	return []ParsedEvent{
		{Type: "stream_event", StreamEventType: "content_block_start", BlockIndex: 1000, BlockType: "tool_use", ToolUseName: name, ToolUseId: id},
		{Type: "stream_event", StreamEventType: "content_block_delta", BlockIndex: 1000, DeltaType: "input_json_delta", DeltaText: string(inputJSON)},
		{Type: "stream_event", StreamEventType: "content_block_stop", BlockIndex: 1000},
	}
}

func codexToolResultEvents(raw map[string]any) []ParsedEvent {
	id := firstString(raw, "call_id", "tool_use_id", "id")
	if id == "" {
		id = "codex-command"
	}
	content := firstString(raw, "output", "aggregated_output", "aggregatedOutput", "text", "message", "result")
	code := firstNonZeroInt(raw["exit_code"], raw["exitCode"])
	if content == "" && code != 0 {
		content = fmt.Sprintf("command exited with code %d", code)
	}
	if content == "" {
		content = "command completed"
	}
	return []ParsedEvent{{
		Type: "user",
		ToolResults: []ToolResultBlock{{
			ToolUseId: id,
			Content:   content,
			IsError:   code != 0,
		}},
	}}
}

func isCommandLike(raw map[string]any) bool {
	if firstString(raw, "command", "cmd") != "" {
		return true
	}
	name := firstString(raw, "name", "tool_name", "type")
	return name == "exec_command" || name == "command_execution" || name == "commandExecution" || name == "shell" || name == "bash" || name == "Bash"
}

func commandString(raw map[string]any) string {
	if s := firstString(raw, "command", "cmd"); s != "" {
		return s
	}
	if arr, ok := raw["command"].([]any); ok {
		b, _ := json.Marshal(arr)
		return string(b)
	}
	return ""
}

func codexMCPToolEvents(raw map[string]any, completed bool) []ParsedEvent {
	id := firstString(raw, "id", "call_id", "tool_use_id")
	if id == "" {
		id = "codex-tool"
	}
	name := firstString(raw, "tool", "name")
	if name == "" {
		name = firstString(raw, "type")
	}
	if !completed {
		inputJSON, _ := json.Marshal(raw["arguments"])
		if len(inputJSON) == 0 || string(inputJSON) == "null" {
			inputJSON, _ = json.Marshal(raw)
		}
		return []ParsedEvent{
			{Type: "stream_event", StreamEventType: "content_block_start", BlockIndex: 1002, BlockType: "tool_use", ToolUseName: name, ToolUseId: id},
			{Type: "stream_event", StreamEventType: "content_block_delta", BlockIndex: 1002, DeltaType: "input_json_delta", DeltaText: string(inputJSON)},
			{Type: "stream_event", StreamEventType: "content_block_stop", BlockIndex: 1002},
		}
	}
	content := firstString(raw, "result", "output")
	if content == "" {
		if b, err := json.Marshal(raw["result"]); err == nil && string(b) != "null" {
			content = string(b)
		}
	}
	if content == "" {
		content = "tool completed"
	}
	status := firstString(raw, "status")
	return []ParsedEvent{{Type: "user", ToolResults: []ToolResultBlock{{ToolUseId: id, Content: content, IsError: status == "failed"}}}}
}

func codexFileChangeEvents(raw map[string]any, completed bool) []ParsedEvent {
	id := firstString(raw, "id")
	if id == "" {
		id = "codex-file-change"
	}
	if !completed {
		inputJSON, _ := json.Marshal(map[string]any{"changes": raw["changes"]})
		return []ParsedEvent{
			{Type: "stream_event", StreamEventType: "content_block_start", BlockIndex: 1003, BlockType: "tool_use", ToolUseName: "apply_patch", ToolUseId: id},
			{Type: "stream_event", StreamEventType: "content_block_delta", BlockIndex: 1003, DeltaType: "input_json_delta", DeltaText: string(inputJSON)},
			{Type: "stream_event", StreamEventType: "content_block_stop", BlockIndex: 1003},
		}
	}
	status := firstString(raw, "status")
	content := "file changes applied"
	if status == "failed" || status == "declined" {
		content = "file changes " + status
	}
	return []ParsedEvent{{Type: "user", ToolResults: []ToolResultBlock{{ToolUseId: id, Content: content, IsError: status == "failed" || status == "declined"}}}}
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			return v
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func firstArray(m map[string]any, keys ...string) []any {
	for _, k := range keys {
		if arr, ok := m[k].([]any); ok {
			return arr
		}
	}
	return nil
}

func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func firstNonZeroInt(values ...any) int {
	for _, v := range values {
		if n := intValue(v); n != 0 {
			return n
		}
	}
	return 0
}

func int64Value(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func floatValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func maybeBase64(s string) string {
	if s == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}
