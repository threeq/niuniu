package adapter

import (
	"strings"
	"testing"
)

func TestForReturnsQwenAdapter(t *testing.T) {
	a := For(TypeQwen)
	if _, ok := a.(QwenAdapter); !ok {
		t.Fatalf("For(TypeQwen) = %T, want QwenAdapter", a)
	}
	if a.Type() != TypeQwen {
		t.Fatalf("Type() = %q, want %q", a.Type(), TypeQwen)
	}
}

func TestQwenProcessModeIsOneShot(t *testing.T) {
	if got := (QwenAdapter{}).ProcessMode(); got != ProcessOneShot {
		t.Fatalf("ProcessMode() = %q, want %q", got, ProcessOneShot)
	}
}

func TestQwenDisplayName(t *testing.T) {
	cases := map[string]string{
		"":               "qwen",
		"qwen":           "qwen",
		"/usr/bin/qwen":  "qwen",
		"C:\\bin\\qwen.exe": "qwen",
	}
	for cmd, want := range cases {
		if got := (QwenAdapter{}).DisplayName(cmd); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestQwenBuildSpawn(t *testing.T) {
	cmd, args := (QwenAdapter{}).BuildSpawn(SpawnOptions{
		Command:      "",
		SessionID:    "sess-123",
		Model:        "qwen3-coder-plus",
		WorktreeDirs: []string{"/ws/a", "/ws/b"},
		ExtraArgs:    []string{"--debug"},
	})
	if cmd != "qwen" {
		t.Fatalf("command = %q, want qwen", cmd)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--output-format stream-json",
		"--include-partial-messages",
		"--resume sess-123",
		"--model qwen3-coder-plus",
		"--include-directories /ws/a,/ws/b",
		"--debug",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %v missing %q", args, want)
		}
	}
	// The prompt is delivered on stdin (one-shot runner), so no -p / prompt arg.
	if strings.Contains(joined, "-p ") || strings.HasSuffix(joined, "-p") {
		t.Errorf("args %v should not contain a -p prompt flag (prompt is on stdin)", args)
	}
}

func TestQwenBuildSpawnNoSessionNoModel(t *testing.T) {
	_, args := (QwenAdapter{}).BuildSpawn(SpawnOptions{Command: "qwen"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--resume") {
		t.Errorf("no session id: args %v should not contain --resume", args)
	}
	if strings.Contains(joined, "--model") {
		t.Errorf("no model: args %v should not contain --model", args)
	}
	if strings.Contains(joined, "--include-directories") {
		t.Errorf("no worktrees: args %v should not contain --include-directories", args)
	}
}

func TestQwenPermissionArgs(t *testing.T) {
	cases := []struct {
		mode string
		want []string
	}{
		{AutohostMode, []string{"--yolo"}},
		{"bypassPermissions", []string{"--yolo"}},
		{"default", []string{"--yolo"}},
		{"acceptEdits", []string{"--yolo"}},
		{"", []string{"--yolo"}},
		{"plan", nil},
	}
	for _, c := range cases {
		got := (QwenAdapter{}).PermissionArgs(PermissionOptions{Mode: c.mode})
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("PermissionArgs(mode=%q) = %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestQwenInjectEnvPassesWorkspaceEnvAndStripsNiuniu(t *testing.T) {
	out := (QwenAdapter{}).InjectEnv([]string{"PATH=/bin"}, EnvOptions{
		WorkspaceEnv: []EnvVar{
			{Key: "OPENAI_API_KEY", Value: "sk-x"},
			{Key: "OPENAI_BASE_URL", Value: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
			{Key: "NIUNIU_PERMISSION_MODE", Value: "autohost"},
		},
		GitAuthorName:  "Tester",
		GitAuthorEmail: "t@example.com",
	})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "OPENAI_API_KEY=sk-x") {
		t.Errorf("provider key not passed through: %v", out)
	}
	if strings.Contains(joined, "NIUNIU_PERMISSION_MODE") {
		t.Errorf("NIUNIU_* control key leaked to CLI env: %v", out)
	}
	if !strings.Contains(joined, "GIT_AUTHOR_NAME=Tester") {
		t.Errorf("git identity not injected: %v", out)
	}
}

// --- ParseLine: the design's keystone claim is that Qwen's stream-json is the
// Anthropic shape, so ParseLine reuses the Claude parser. These cover both the
// Claude-Code-wrapped envelope and the raw top-level Anthropic shape. ---

func TestQwenParseSessionStartMapsToInit(t *testing.T) {
	line := `{"type":"system","subtype":"session_start","session_id":"abc-123"}`
	evs, err := (QwenAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "system" || ev.Subtype != "init" {
		t.Fatalf("got type=%q subtype=%q, want system/init", ev.Type, ev.Subtype)
	}
	if ev.SessionID != "abc-123" {
		t.Fatalf("SessionID = %q, want abc-123", ev.SessionID)
	}
}

func TestQwenParseWrappedStreamEvent(t *testing.T) {
	// Claude-Code-wrapped partial event (the expected Qwen shape).
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}}`
	evs, err := (QwenAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.Type != "stream_event" || ev.StreamEventType != "content_block_delta" {
		t.Fatalf("got type=%q stream=%q, want stream_event/content_block_delta", ev.Type, ev.StreamEventType)
	}
	if ev.DeltaText != "Hello" {
		t.Fatalf("DeltaText = %q, want Hello", ev.DeltaText)
	}
}

func TestQwenParseRawTopLevelPartialEvent(t *testing.T) {
	// Raw top-level Anthropic shape (unwrapped) — must be normalized to the
	// stream_event envelope so the shared parser reads it.
	line := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`
	evs, err := (QwenAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.Type != "stream_event" || ev.StreamEventType != "content_block_delta" {
		t.Fatalf("got type=%q stream=%q, want stream_event/content_block_delta", ev.Type, ev.StreamEventType)
	}
	if ev.DeltaText != "Hi" {
		t.Fatalf("DeltaText = %q, want Hi", ev.DeltaText)
	}
}

func TestQwenParseRawMessageStartUsage(t *testing.T) {
	line := `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":0,"cache_read_input_tokens":3}}}`
	evs, err := (QwenAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.StreamEventType != "message_start" {
		t.Fatalf("StreamEventType = %q, want message_start", ev.StreamEventType)
	}
	if ev.InputTokens != 12 || ev.CacheReadTokens != 3 {
		t.Fatalf("usage = in:%d cacheRead:%d, want 12/3", ev.InputTokens, ev.CacheReadTokens)
	}
}

func TestQwenParseAssistantToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	evs, err := (QwenAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.Type != "assistant" {
		t.Fatalf("Type = %q, want assistant", ev.Type)
	}
	if len(ev.TextBlocks) != 1 || ev.TextBlocks[0].Text != "ok" {
		t.Fatalf("TextBlocks = %+v", ev.TextBlocks)
	}
	if len(ev.ToolUseBlocks) != 1 || ev.ToolUseBlocks[0].Name != "Bash" {
		t.Fatalf("ToolUseBlocks = %+v", ev.ToolUseBlocks)
	}
}

func TestQwenParseResultSuccessAndError(t *testing.T) {
	ok := `{"type":"result","subtype":"success","total_cost_usd":0.0021,"num_turns":2,"duration_ms":1500,"usage":{"input_tokens":100,"output_tokens":40}}`
	evs, _ := (QwenAdapter{}).ParseLine(ok)
	ev := evs[0]
	if ev.Type != "result" || ev.IsError {
		t.Fatalf("success: type=%q isErr=%v, want result/false", ev.Type, ev.IsError)
	}
	if ev.TotalCostUSD != 0.0021 || ev.OutputTokens != 40 {
		t.Fatalf("success result fields cost=%v out=%d", ev.TotalCostUSD, ev.OutputTokens)
	}

	bad := `{"type":"result","subtype":"error","result":"boom"}`
	evs, _ = (QwenAdapter{}).ParseLine(bad)
	ev = evs[0]
	if ev.Type != "result" || !ev.IsError {
		t.Fatalf("error: type=%q isErr=%v, want result/true", ev.Type, ev.IsError)
	}
	if ev.Result != "boom" {
		t.Fatalf("error result = %q, want boom", ev.Result)
	}
}

func TestQwenParseBlankLine(t *testing.T) {
	evs, err := (QwenAdapter{}).ParseLine("   ")
	if err != nil {
		t.Fatalf("blank line err: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("blank line produced %d events, want 0", len(evs))
	}
}
