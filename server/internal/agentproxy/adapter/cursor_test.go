package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestForReturnsCursorAdapter(t *testing.T) {
	a := For(TypeCursor)
	if _, ok := a.(CursorAdapter); !ok {
		t.Fatalf("For(TypeCursor) = %T, want CursorAdapter", a)
	}
	if a.Type() != TypeCursor {
		t.Fatalf("Type() = %q, want %q", a.Type(), TypeCursor)
	}
}

func TestCursorProcessModeIsOneShot(t *testing.T) {
	if got := (CursorAdapter{}).ProcessMode(); got != ProcessOneShot {
		t.Fatalf("ProcessMode() = %q, want %q", got, ProcessOneShot)
	}
}

func TestCursorDisplayName(t *testing.T) {
	cases := map[string]string{
		"":                            "cursor-agent",
		"cursor-agent":                "cursor-agent",
		"/usr/local/bin/cursor-agent": "cursor-agent",
		"C:\\bin\\cursor-agent.exe":   "cursor-agent",
	}
	for cmd, want := range cases {
		if got := (CursorAdapter{}).DisplayName(cmd); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestCursorBuildSpawn(t *testing.T) {
	cmd, args := (CursorAdapter{}).BuildSpawn(SpawnOptions{
		Command:   "",
		WorkDir:   "/ws/root",
		SessionID: "chat-abc",
		Model:     "sonnet-4",
		ExtraArgs: []string{"--trust", "--force"},
	})
	if cmd != "cursor-agent" {
		t.Fatalf("command = %q, want cursor-agent", cmd)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-p",
		"--output-format stream-json",
		"--workspace /ws/root",
		"--resume chat-abc",
		"--model sonnet-4",
		"--trust --force",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %v missing %q", args, want)
		}
	}
	// --stream-partial-output must NOT be passed: it makes cursor emit three
	// flavors of assistant event (delta / pre-tool flush / end-of-turn flush)
	// that are only distinguishable via timestamp_ms + model_call_id, and the
	// duplicates would double-print the reply.
	if strings.Contains(joined, "--stream-partial-output") {
		t.Errorf("args %v must not enable --stream-partial-output", args)
	}
}

func TestCursorBuildSpawnNoSessionNoModel(t *testing.T) {
	_, args := (CursorAdapter{}).BuildSpawn(SpawnOptions{Command: "cursor-agent"})
	joined := strings.Join(args, " ")
	for _, absent := range []string{"--resume", "--model", "--workspace"} {
		if strings.Contains(joined, absent) {
			t.Errorf("args %v should not contain %s", args, absent)
		}
	}
}

func TestCursorPermissionArgs(t *testing.T) {
	cases := []struct {
		mode string
		want []string
	}{
		{AutohostMode, []string{"--trust", "--force"}},
		{"bypassPermissions", []string{"--trust", "--force"}},
		{"default", []string{"--trust", "--force"}},
		{"acceptEdits", []string{"--trust", "--force"}},
		{"", []string{"--trust", "--force"}},
		{"plan", []string{"--trust", "--mode", "plan"}},
	}
	for _, c := range cases {
		got := (CursorAdapter{}).PermissionArgs(PermissionOptions{Mode: c.mode})
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("PermissionArgs(mode=%q) = %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestCursorInjectEnvPassesWorkspaceEnvAndStripsNiuniu(t *testing.T) {
	out := (CursorAdapter{}).InjectEnv([]string{"PATH=/bin"}, EnvOptions{
		WorkspaceEnv: []EnvVar{
			{Key: "CURSOR_API_KEY", Value: "key_live_x"},
			{Key: "NIUNIU_PERMISSION_MODE", Value: "autohost"},
		},
		// A stray AccountConfigDir must not synthesize a malformed "=value"
		// entry: cursor has no niuniu-managed account dir, so accountKey is "".
		AccountConfigDir: "/should/be/ignored",
		GitAuthorName:    "Tester",
		GitAuthorEmail:   "t@example.com",
	})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "CURSOR_API_KEY=key_live_x") {
		t.Errorf("provider key not passed through: %v", out)
	}
	if strings.Contains(joined, "NIUNIU_PERMISSION_MODE") {
		t.Errorf("NIUNIU_* control key leaked to CLI env: %v", out)
	}
	if !strings.Contains(joined, "GIT_AUTHOR_NAME=Tester") {
		t.Errorf("git identity not injected: %v", out)
	}
	for _, e := range out {
		if strings.HasPrefix(e, "=") {
			t.Errorf("malformed env entry %q in %v", e, out)
		}
	}
}

// --- ParseLine: cursor-agent emits its OWN stream-json schema (not the
// Anthropic shape Qwen mirrors), so these lock the hand-written mapping. Lines
// below follow the documented schema at cursor.com/docs/cli/reference/output-format. ---

func TestCursorParseSystemInitCapturesSession(t *testing.T) {
	line := `{"type":"system","subtype":"init","apiKeySource":"login","cwd":"/p","session_id":"c6b62c6f","model":"Claude 4 Sonnet","permissionMode":"default"}`
	evs, err := (CursorAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	// handleEvent captures session_id only on subtype=="init".
	if ev.Type != "system" || ev.Subtype != "init" {
		t.Fatalf("got type=%q subtype=%q, want system/init", ev.Type, ev.Subtype)
	}
	if ev.SessionID != "c6b62c6f" {
		t.Fatalf("SessionID = %q, want c6b62c6f", ev.SessionID)
	}
}

func TestCursorParseSystemSessionStartNormalizedToInit(t *testing.T) {
	// Defensive: a future spelling must still seed the resume id.
	line := `{"type":"system","subtype":"session_start","session_id":"s1"}`
	evs, _ := (CursorAdapter{}).ParseLine(line)
	if len(evs) != 1 || evs[0].Subtype != "init" || evs[0].SessionID != "s1" {
		t.Fatalf("got %+v, want one system/init event with SessionID s1", evs)
	}
}

func TestCursorParseUserEchoIsDropped(t *testing.T) {
	// The SPA already rendered the user's prompt optimistically; forwarding
	// cursor's echo would paint it twice.
	line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]},"session_id":"s1"}`
	evs, err := (CursorAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("user echo produced %d events, want 0", len(evs))
	}
}

func TestCursorParseAssistantText(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll read the file"}]},"session_id":"s1"}`
	evs, err := (CursorAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.Type != "assistant" {
		t.Fatalf("Type = %q, want assistant", ev.Type)
	}
	if len(ev.TextBlocks) != 1 || ev.TextBlocks[0].Text != "I'll read the file" {
		t.Fatalf("TextBlocks = %+v", ev.TextBlocks)
	}
}

func TestCursorParseAssistantEmptyContentDropped(t *testing.T) {
	// A content-less flush around a tool call would append a blank bubble.
	line := `{"type":"assistant","message":{"role":"assistant","content":[]},"session_id":"s1"}`
	evs, _ := (CursorAdapter{}).ParseLine(line)
	if len(evs) != 0 {
		t.Fatalf("empty assistant produced %d events, want 0", len(evs))
	}
}

func TestCursorParseToolCallStarted(t *testing.T) {
	line := `{"type":"tool_call","subtype":"started","call_id":"toolu_1","tool_call":{"readToolCall":{"args":{"path":"README.md"}}},"session_id":"s1"}`
	evs, err := (CursorAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.Type != "assistant" {
		t.Fatalf("Type = %q, want assistant (tool_use rides an assistant event)", ev.Type)
	}
	if len(ev.ToolUseBlocks) != 1 {
		t.Fatalf("ToolUseBlocks = %+v", ev.ToolUseBlocks)
	}
	b := ev.ToolUseBlocks[0]
	// The typed key must map to the Claude tool name the SPA renders.
	if b.Name != "Read" {
		t.Errorf("tool name = %q, want Read", b.Name)
	}
	if b.Id != "toolu_1" {
		t.Errorf("tool id = %q, want toolu_1", b.Id)
	}
	// Args must be unwrapped from the {"<tool>ToolCall":{"args":{…}}} nesting so
	// the SPA sees the same flat input object it gets for Claude.
	var input map[string]any
	if err := json.Unmarshal([]byte(b.Input), &input); err != nil {
		t.Fatalf("Input %q is not valid JSON: %v", b.Input, err)
	}
	if input["path"] != "README.md" {
		t.Errorf("Input = %q, want the unwrapped args with path=README.md", b.Input)
	}
}

func TestCursorParseToolCallShellMapsToBash(t *testing.T) {
	line := `{"type":"tool_call","subtype":"started","call_id":"c2","tool_call":{"shellToolCall":{"args":{"command":"ls -la"}}}}`
	evs, _ := (CursorAdapter{}).ParseLine(line)
	if len(evs) != 1 || len(evs[0].ToolUseBlocks) != 1 {
		t.Fatalf("got %+v", evs)
	}
	if got := evs[0].ToolUseBlocks[0].Name; got != "Bash" {
		t.Errorf("shellToolCall mapped to %q, want Bash", got)
	}
}

func TestCursorParseToolCallUnknownKeyFallsBackToTrimmedName(t *testing.T) {
	// A tool added by a future cursor release must still show a sensible label
	// rather than a blank one.
	line := `{"type":"tool_call","subtype":"started","call_id":"c3","tool_call":{"fancyNewToolCall":{"args":{}}}}`
	evs, _ := (CursorAdapter{}).ParseLine(line)
	if len(evs) != 1 || len(evs[0].ToolUseBlocks) != 1 {
		t.Fatalf("got %+v", evs)
	}
	if got := evs[0].ToolUseBlocks[0].Name; got != "fancyNew" {
		t.Errorf("unknown tool name = %q, want fancyNew", got)
	}
}

func TestCursorParseToolCallCompletedSuccess(t *testing.T) {
	line := `{"type":"tool_call","subtype":"completed","call_id":"toolu_1","tool_call":{"readToolCall":{"args":{"path":"a"}},"result":{"success":{"content":"file body"}}},"session_id":"s1"}`
	evs, err := (CursorAdapter{}).ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if len(ev.ToolResults) != 1 {
		t.Fatalf("ToolResults = %+v", ev.ToolResults)
	}
	r := ev.ToolResults[0]
	if r.ToolUseId != "toolu_1" {
		t.Errorf("ToolUseId = %q, want toolu_1", r.ToolUseId)
	}
	if r.IsError {
		t.Errorf("a {\"success\":…} result must not be flagged as an error")
	}
	if !strings.Contains(r.Content, "file body") {
		t.Errorf("Content = %q, want it to carry the result payload", r.Content)
	}
}

func TestCursorParseToolCallCompletedError(t *testing.T) {
	// Anything that is not {"success":…} is a failure, so the SPA paints it red.
	line := `{"type":"tool_call","subtype":"completed","call_id":"c9","tool_call":{"shellToolCall":{"args":{}},"result":{"error":{"message":"exit 1"}}}}`
	evs, _ := (CursorAdapter{}).ParseLine(line)
	if len(evs) != 1 || len(evs[0].ToolResults) != 1 {
		t.Fatalf("got %+v", evs)
	}
	if !evs[0].ToolResults[0].IsError {
		t.Errorf("an {\"error\":…} result must be flagged IsError")
	}
}

func TestCursorParseResultSuccessAndError(t *testing.T) {
	ok := `{"type":"result","subtype":"success","duration_ms":1234,"duration_api_ms":1000,"is_error":false,"result":"all done","session_id":"s1"}`
	evs, err := (CursorAdapter{}).ParseLine(ok)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	ev := evs[0]
	if ev.Type != "result" || ev.IsError {
		t.Fatalf("success: type=%q isErr=%v, want result/false", ev.Type, ev.IsError)
	}
	if ev.Result != "all done" {
		t.Errorf("Result = %q, want 'all done'", ev.Result)
	}
	if ev.DurationMs != 1234 {
		t.Errorf("DurationMs = %d, want 1234", ev.DurationMs)
	}

	bad := `{"type":"result","subtype":"error","is_error":true,"result":"boom"}`
	evs, _ = (CursorAdapter{}).ParseLine(bad)
	if !evs[0].IsError || evs[0].Result != "boom" {
		t.Fatalf("error result = %+v, want IsError with Result boom", evs[0])
	}
}

func TestCursorParseBlankAndUnknownLines(t *testing.T) {
	for _, line := range []string{"   ", `{"type":"thinking","text":"hmm"}`} {
		evs, err := (CursorAdapter{}).ParseLine(line)
		if err != nil {
			t.Fatalf("ParseLine(%q) err: %v", line, err)
		}
		if len(evs) != 0 {
			t.Fatalf("ParseLine(%q) produced %d events, want 0", line, len(evs))
		}
	}
}

func TestCursorParseMalformedJSONReturnsError(t *testing.T) {
	if _, err := (CursorAdapter{}).ParseLine(`{"type":`); err == nil {
		t.Fatal("malformed JSON should return an error so the runner can log the raw line")
	}
}
