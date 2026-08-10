package agentproxy

import (
	"strings"
	"testing"
	"time"
)

func TestParseBashBackground_True(t *testing.T) {
	input := `{"command":"bash relay/deploy/deploy.sh","run_in_background":true,"description":"deploy"}`
	bg, title, ok := parseBashBackground(input)
	if !ok {
		t.Fatal("expected ok=true for run_in_background")
	}
	if !bg {
		t.Fatal("expected bg=true")
	}
	if title != "bash relay/deploy/deploy.sh" {
		t.Fatalf("unexpected title: %q", title)
	}
}

func TestParseBashBackground_False(t *testing.T) {
	input := `{"command":"ls","run_in_background":false}`
	_, _, ok := parseBashBackground(input)
	if ok {
		t.Fatal("expected ok=false for non-bg bash")
	}
}

func TestParseBashBackground_Missing(t *testing.T) {
	input := `{"command":"ls"}`
	_, _, ok := parseBashBackground(input)
	if ok {
		t.Fatal("expected ok=false when run_in_background absent")
	}
}

func TestParseSubagentDescription(t *testing.T) {
	input := `{"description":"Run code review","subagent_type":"reviewer","prompt":"review this"}`
	desc, ok := parseSubagentDescription(input)
	if !ok || desc != "Run code review" {
		t.Fatalf("expected description, got desc=%q ok=%v", desc, ok)
	}
}

func TestParseSubagentDescription_FallbackPrompt(t *testing.T) {
	input := `{"prompt":"Investigate something"}`
	desc, ok := parseSubagentDescription(input)
	if !ok || desc == "" {
		t.Fatalf("expected fallback to prompt, got desc=%q ok=%v", desc, ok)
	}
}

func TestParseScheduleWakeup(t *testing.T) {
	input := `{"delaySeconds":600,"reason":"check build progress","prompt":"keep looping"}`
	delay, reason, ok := parseScheduleWakeup(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if delay != 600*time.Second {
		t.Fatalf("expected 600s, got %v", delay)
	}
	if reason != "check build progress" {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestParseScheduleWakeup_BadInput(t *testing.T) {
	if _, _, ok := parseScheduleWakeup(`{"foo":"bar"}`); ok {
		t.Fatal("expected ok=false for missing delaySeconds")
	}
	if _, _, ok := parseScheduleWakeup(`not json`); ok {
		t.Fatal("expected ok=false for non-JSON")
	}
}

// newSessionForTest returns a session with the minimum fields populated for
// recordBgTask* tests. parent is left nil so emitBgTaskNotify is a no-op (we
// test tracker state, not notify fan-out, which is covered by
// TestBgTaskDebouncer_*).
func newSessionForTest() *WorkspaceSession {
	return &WorkspaceSession{
		inflight: NewInflightTracker(),
	}
}

func TestRecordBgTaskUse_BashBg(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Bash", "tu_b", `{"command":"sleep 600","run_in_background":true}`)
	got := s.BgTasks()
	if len(got) != 1 || got[0].Kind != BgTaskBash || got[0].ToolUseID != "tu_b" {
		t.Fatalf("expected one bash entry, got %+v", got)
	}
}

func TestRecordBgTaskUse_BashForeground_Skipped(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Bash", "tu_b", `{"command":"ls","run_in_background":false}`)
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected no entries, got %+v", got)
	}
}

func TestRecordBgTaskUse_Subagent(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Task", "tu_t", `{"description":"Review code","subagent_type":"reviewer","prompt":"x"}`)
	got := s.BgTasks()
	if len(got) != 1 || got[0].Kind != BgTaskSubagent || got[0].Title != "Review code" {
		t.Fatalf("expected subagent entry, got %+v", got)
	}
}

func TestRecordBgTaskUse_Wakeup(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("ScheduleWakeup", "tu_w", `{"delaySeconds":300,"reason":"checking deploy"}`)
	got := s.BgTasks()
	if len(got) != 1 || got[0].Kind != BgTaskWakeup {
		t.Fatalf("expected wakeup entry, got %+v", got)
	}
}

func TestRecordBgTaskResult_SubagentRemoved(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Task", "tu_t", `{"description":"Review code","subagent_type":"reviewer","prompt":"x"}`)
	s.recordBgTaskResult("tu_t", "subagent done.", false)
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected empty after subagent tool_result, got %+v", got)
	}
}

func TestRecordBgTaskResult_BashKept_CapturesBashID(t *testing.T) {
	// The Bash[bg] tool_result is a spawn ack, not a completion. The entry
	// must persist so the sidebar reflects the running command. The shell_id
	// embedded in the ack is captured into BashID for later KillBash matching.
	s := newSessionForTest()
	s.recordBgTaskUse("Bash", "tu_b", `{"command":"sleep 600","run_in_background":true}`)
	s.recordBgTaskResult("tu_b", "Command running in background with ID: b5kcffa1v. Output is being written to: /tmp/x", false)
	got := s.BgTasks()
	if len(got) != 1 {
		t.Fatalf("expected entry to persist after bg tool_result, got %+v", got)
	}
	if got[0].BashID != "b5kcffa1v" {
		t.Fatalf("expected BashID captured, got %q", got[0].BashID)
	}
}

func TestRecordBgTaskResult_BashError_Removed(t *testing.T) {
	// A failed Bash[bg] spawn (permission denied, invalid command, etc.)
	// never produces a real bg shell. Don't leave a 1h zombie in the sidebar.
	s := newSessionForTest()
	s.recordBgTaskUse("Bash", "tu_b", `{"command":"sleep 600","run_in_background":true}`)
	s.recordBgTaskResult("tu_b", "Tool 'Bash' was denied", true)
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected entry removed on error tool_result, got %+v", got)
	}
}

func TestRecordBgTaskResult_WakeupKept(t *testing.T) {
	// ScheduleWakeup tool_result is the schedule ack, not the firing. Entry
	// must persist; GCStale removes once ScheduledFor passes.
	s := newSessionForTest()
	s.recordBgTaskUse("ScheduleWakeup", "tu_w", `{"delaySeconds":300,"reason":"checking deploy"}`)
	s.recordBgTaskResult("tu_w", "scheduled at 2026-05-09T17:30:00Z", false)
	got := s.BgTasks()
	if len(got) != 1 {
		t.Fatalf("expected wakeup entry to persist, got %+v", got)
	}
	// Document the invariant the kept-state depends on: ScheduledFor is in
	// the future. If it weren't, GCStale would remove it on the next sweep
	// and this test's "kept" assertion would be coincidence, not contract.
	if !got[0].ScheduledFor.After(time.Now()) {
		t.Fatalf("expected ScheduledFor in the future, got %v", got[0].ScheduledFor)
	}
}

func TestRecordBgTaskResult_WakeupError_Removed(t *testing.T) {
	// A rejected schedule (invalid delaySeconds, permission denied) means
	// no wakeup will fire. Drop the entry so the sidebar's wakeup_count
	// reflects reality, not a queued-but-never-firing schedule.
	s := newSessionForTest()
	s.recordBgTaskUse("ScheduleWakeup", "tu_w", `{"delaySeconds":300,"reason":"x"}`)
	s.recordBgTaskResult("tu_w", "Invalid delaySeconds", true)
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected wakeup entry removed on error, got %+v", got)
	}
}

func TestRecordBgTaskUse_KillBash_RemovesByBashID(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Bash", "tu_b", `{"command":"sleep 600","run_in_background":true}`)
	s.recordBgTaskResult("tu_b", "Command running in background with ID: b5kcffa1v.", false)
	s.recordBgTaskUse("KillBash", "tu_kill", `{"shell_id":"b5kcffa1v"}`)
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected empty after KillBash, got %+v", got)
	}
}

func TestRecordBgTaskUse_KillBash_NoMatch_NoOp(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Bash", "tu_b", `{"command":"sleep 600","run_in_background":true}`)
	s.recordBgTaskResult("tu_b", "Command running in background with ID: b5kcffa1v.", false)
	s.recordBgTaskUse("KillBash", "tu_kill", `{"shell_id":"unrelated_id"}`)
	if got := s.BgTasks(); len(got) != 1 {
		t.Fatalf("KillBash with unmatched shell_id must not affect tracker, got %+v", got)
	}
}

func TestParseBashSpawnResult(t *testing.T) {
	id, ok := parseBashSpawnResult("Command running in background with ID: b5kcffa1v. Output is being written to: /tmp/foo")
	if !ok || id != "b5kcffa1v" {
		t.Fatalf("expected b5kcffa1v, got %q ok=%v", id, ok)
	}
	if _, ok := parseBashSpawnResult("just some output"); ok {
		t.Fatal("expected ok=false for non-spawn content")
	}
}

func TestParseKillBashInput(t *testing.T) {
	id, ok := parseKillBashInput(`{"shell_id":"abc123"}`)
	if !ok || id != "abc123" {
		t.Fatalf("expected abc123 from shell_id, got %q ok=%v", id, ok)
	}
	if _, ok := parseKillBashInput(`{"foo":"bar"}`); ok {
		t.Fatal("expected ok=false when shell_id absent")
	}
	if _, ok := parseKillBashInput(`{"shell_id":""}`); ok {
		t.Fatal("expected ok=false for empty shell_id")
	}
	if _, ok := parseKillBashInput(`not json`); ok {
		t.Fatal("expected ok=false for non-JSON")
	}
}

func TestRecordBgTaskUse_UnknownTool_NoOp(t *testing.T) {
	s := newSessionForTest()
	s.recordBgTaskUse("Read", "tu_r", `{"file_path":"/tmp/x"}`)
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestTruncateTitle_UTF8(t *testing.T) {
	// 50 Chinese characters; each is 3 UTF-8 bytes (150 bytes total).
	in := strings.Repeat("中", 50)
	got := truncateTitle(in, 30)
	// must be valid UTF-8 (no replacement runes)
	if strings.ContainsRune(got, '�') {
		t.Fatalf("truncated string contains replacement rune: %q", got)
	}
	// must end with the ellipsis
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected trailing ellipsis, got: %q", got)
	}
	// rune count must be exactly n (we kept n-1 runes + ellipsis)
	if rc := len([]rune(got)); rc != 30 {
		t.Fatalf("expected 30 runes, got %d (%q)", rc, got)
	}
}

func TestTruncateTitle_Short(t *testing.T) {
	// no truncation when within limit
	if got := truncateTitle("hello", 10); got != "hello" {
		t.Fatalf("expected unchanged, got %q", got)
	}
}
