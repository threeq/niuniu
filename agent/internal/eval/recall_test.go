package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recallBody must surface sandbox memory fixtures on the real injection
// path — lifecycle filtering included — because the memory-semantics
// eval tasks (过期停引 / open_item 回访) judge exactly that behavior.
func TestRecallBodySandboxFixtures(t *testing.T) {
	task := Task{Fixtures: map[string]string{
		".niuniu-agent/memory/live-item.md": "---\ntitle: live-item\ntype: user\ndomain: open_item\nlifecycle: open\nexpires_at: 2099-12-31T00:00:00Z\ncreated: 2026-09-20T09:00:00Z\nupdated: 2026-09-20T09:00:00Z\n---\n答应帮朋友把 CLI 工具的 README 翻译成英文。",
		".niuniu-agent/memory/stale-item.md": "---\ntitle: stale-item\ntype: user\ndomain: open_item\nlifecycle: open\nexpires_at: 2026-01-05T10:00:00Z\ncreated: 2026-01-01T09:00:00Z\nupdated: 2026-01-01T09:00:00Z\n---\n用户约了钢琴调音师上门。",
	}}
	dir, err := task.PrepareSandbox()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	body := recallBody("", dir)
	if !strings.Contains(body, "live-item") {
		t.Errorf("live open_item fixture missing from recall body:\n%s", body)
	}
	if !strings.Contains(body, "[open, due 2099-12-31]") {
		t.Errorf("live open_item fixture should carry a due annotation:\n%s", body)
	}
	if strings.Contains(body, "stale-item") || strings.Contains(body, "调音") {
		t.Errorf("expired fixture must be filtered from recall (lazy expiry):\n%s", body)
	}

	// 无 memory fixtures 的普通任务 → 空 body，Section 整段省略。
	plain := Task{Fixtures: map[string]string{"notes.txt": "hello"}}
	dir2, err := plain.PrepareSandbox()
	if err != nil {
		t.Fatal(err)
	}
	if body := recallBody("", dir2); strings.TrimSpace(body) != "" {
		t.Errorf("plain sandbox should recall nothing, got:\n%s", body)
	}
}

// file-count 原语：目录直接子文件数判定。记忆语义用例靠它做「写入探测」
// （临时状态不得落进记忆层），比内容子串判定更稳——模型的解释性文字不会
// 误伤，且对「多存了一条」这种回归是确定性的。
func TestRunChecksFileCount(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, ".niuniu-agent", "memory")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := Task{Checks: []Check{
		{Kind: "file-count", File: ".niuniu-agent/memory", Value: "1"},
		{Kind: "file-count", File: ".niuniu-agent/memory", Value: "2"},
		{Kind: "file-count", File: ".niuniu-agent/missing", Value: "0"},
	}}
	res := RunChecks(task, dir, "")
	if !res[0].Pass {
		t.Errorf("count 1: %+v", res[0])
	}
	if res[1].Pass || !strings.Contains(res[1].Detail, "holds 1 files, want 2") {
		t.Errorf("count 2 should fail with detail: %+v", res[1])
	}
	if res[2].Pass {
		t.Errorf("missing dir must not pass a zero-count check: %+v", res[2])
	}

	// 解析：file-count 走「dir, N」双参数形式。
	parsed, err := parseTask("---\nname: fc\n---\n## Task\nx\n## Checks\n- file-count: .niuniu-agent/memory, 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Checks[0].File != ".niuniu-agent/memory" || parsed.Checks[0].Value != "1" {
		t.Fatalf("parsed = %+v", parsed.Checks[0])
	}
}
