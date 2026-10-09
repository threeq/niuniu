package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// scriptedModel 执行任务时按指令写出文件（模拟 agent 行为）。
type scriptedModel struct {
	dir      string
	behavior string // "correct" | "wrong"
}

func (m *scriptedModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	if m.dir == "" {
		m.dir, _ = os.Getwd()
	}
	if m.behavior == "wrong" {
		return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
			{Type: model.BlockText, Text: "I did the wrong thing."}}}}, nil
	}
	// 从任务指令里取 "write <file> = <content>" 形式的子命令（测试用）。
	for _, blk := range req.Messages[0].Blocks {
		if blk.Type != model.BlockText {
			continue
		}
		for _, line := range strings.Split(blk.Text, "\n") {
			if rest, ok := strings.CutPrefix(line, "write "); ok {
				file, content, ok2 := strings.Cut(rest, " = ")
				if ok2 && strings.TrimSpace(file) != "" {
					_ = os.WriteFile(filepath.Join(m.dir, file), []byte(content), 0o644)
				}
			}
		}
	}
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
		{Type: model.BlockText, Text: "done MAGICMARKER"}}}}, nil
}

func writeTask(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndRunTasks(t *testing.T) {
	tasksDir := t.TempDir()
	writeTask(t, tasksDir, "01-fix-typo", `---
name: fix-typo
description: fix the typo
---
## Task
write fixed.txt = all good now
## Fixtures
- notes.txt: sample input
## Checks
- file-exists: fixed.txt
- contains: fixed.txt, all good
- contains: notes.txt, sample input
`)
	writeTask(t, tasksDir, "02-output", `---
name: output-check
description: answer must contain marker
---
## Task
reply with the word MAGICMARKER
## Checks
- output-contains: MAGICMARKER
`)
	tasks, err := LoadTasks(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Name != "fix-typo" || tasks[1].Name != "output-check" {
		t.Fatalf("tasks = %+v", tasks)
	}

	// 正确行为：两任务全过。
	m := &scriptedModel{}
	reg := tools.NewRegistry(tools.LS{}, tools.Read{}, tools.Write{})
	var results []Result
	for _, tk := range tasks {
		results = append(results, RunTask(context.Background(), m, reg, tk, time.Minute, ""))
	}
	if !results[0].Pass || !results[1].Pass {
		t.Fatalf("results = %+v / %+v", results[0], results[1])
	}
	if results[0].Usage.OutputTokens != 0 && results[0].Output == "" {
		t.Error("output should be captured")
	}

	// 错误行为：file-exists 失败。
	mm := &scriptedModel{behavior: "wrong"}
	bad := RunTask(context.Background(), mm, reg, tasks[0], time.Minute, "")
	if bad.Pass {
		t.Fatal("wrong behavior must fail")
	}
	if !strings.Contains(bad.Error, "file not found") {
		t.Errorf("error = %q", bad.Error)
	}
}

func TestSummaryJSONBaselineAndMarkdown(t *testing.T) {
	s := Summary{GeneratedAt: time.Now(), Total: 2, Passed: 1, Failed: 1, TotalDuration: "2s"}
	s.Results = append(s.Results, Result{Name: "a", Pass: true}, Result{Name: "b", Error: "check failed: x"})

	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "summary.json")
	if err := s.WriteJSON(jsonPath); err != nil {
		t.Fatal(err)
	}
	// JSON 可读回（baseline 对比）。
	line, err := s.CompareAgainst(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "1/2 passed (50%)") {
		t.Errorf("compare line = %q", line)
	}

	mdPath := filepath.Join(dir, "report.md")
	if err := s.WriteMarkdown(mdPath); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mdPath)
	for _, want := range []string{"# niuniu-agent eval report", "| a | ✅", "| b | ❌", "50% pass rate"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("report missing %q", want)
		}
	}
}

func TestParseTaskValidation(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "no-checks", "---\nname: x\n---\n## Task\nhello\n")
	if _, err := LoadTasks(dir); err == nil || !strings.Contains(err.Error(), "no ## Checks") {
		t.Fatalf("err = %v, want missing-checks error", err)
	}
	// JSON 摘要与 Result 结构可序列化（落盘前提）。
	b, _ := json.Marshal(Result{Name: "x"})
	if !strings.Contains(string(b), `"name":"x"`) {
		t.Errorf("marshal = %s", b)
	}
}

// Windows checkouts (core.autocrlf) hand task files over with CRLF line
// endings; parsing must not depend on the checkout's EOL convention.
func TestParseTaskCRLF(t *testing.T) {
	crlf := "---\r\nname: crlf-task\r\ndescription: windows checkout\r\n---\r\n## Task\r\nwrite out.txt = done\r\n## Checks\r\n- file-exists: out.txt\r\n"
	task, err := parseTask(crlf)
	if err != nil {
		t.Fatalf("parseTask CRLF: %v", err)
	}
	if task.Name != "crlf-task" || !strings.Contains(task.Prompt, "write out.txt = done") {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Checks) != 1 || task.Checks[0].Kind != "file-exists" {
		t.Fatalf("checks = %+v", task.Checks)
	}
}

// writeMemoryEntry 在 dir 下写一条最小可召回的记忆条目（与任务 fixture 同格式）。
func writeMemoryEntry(t *testing.T, dir, slug, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntitle: " + slug + "\ntype: pattern\ndomain: decision\nlifecycle: open\n" +
		"created: 2026-10-01T08:00:00Z\nupdated: 2026-10-01T08:00:00Z\n---\n" + content + "\n"
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 记忆语义任务必须能真正调用 MemorySave/MemorySearch，且两者绑定在各自任务的
// 沙箱层：写进开发机真实记忆会污染用户数据，绑在共享 registry 上则会让后续任务
// 读写上一个任务的沙箱。
func TestSandboxToolsBindMemoryToTheSandbox(t *testing.T) {
	base := tools.NewRegistry(tools.LS{}, tools.Read{}, tools.Write{})
	sandbox := t.TempDir()
	reg := sandboxTools(base, sandbox)

	for _, name := range []string{"MemorySave", "MemorySearch"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("eval tool surface is missing %s", name)
		}
	}
	if _, ok := base.Lookup("MemorySave"); ok {
		t.Fatal("sandboxTools mutated the caller's shared registry")
	}

	save, _ := reg.Lookup("MemorySave")
	if _, err := save.Execute(context.Background(), json.RawMessage(
		`{"title":"americano-preference","type":"user","domain":"preference","content":"The user drinks americano now."}`)); err != nil {
		t.Fatalf("MemorySave: %v", err)
	}
	path := filepath.Join(sandbox, ".niuniu-agent", "memory", "americano-preference.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved entry did not land in the sandbox: %v", err)
	}
	search, _ := reg.Lookup("MemorySearch")
	out, err := search.Execute(context.Background(), json.RawMessage(`{"query":"americano"}`))
	if err != nil {
		t.Fatalf("MemorySearch: %v", err)
	}
	if !strings.Contains(out, "americano") {
		t.Errorf("search did not read the sandbox entry back: %q", out)
	}
}

// memoryToolModel 第一轮用 MemorySave 落库（任务 22 期望的当轮纠错动作），
// 第二轮收尾——用来验证 RunTask 交给会话的工具面里真的有记忆工具。
type memoryToolModel struct{ called bool }

func (m *memoryToolModel) Complete(_ context.Context, _ model.Request) (*model.Response, error) {
	if m.called {
		return &model.Response{
			Message:    model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockText, Text: "done"}}},
			StopReason: model.StopEndTurn,
		}, nil
	}
	m.called = true
	return &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{
			Type: model.BlockToolUse, ID: "tu_1", Name: "MemorySave",
			Input: json.RawMessage(`{"title":"americano-preference","type":"user","domain":"preference","content":"The user drinks americano now."}`),
		}}},
		StopReason: model.StopToolUse,
	}, nil
}

// 端到端：记忆任务只能通过 MemorySave 达成检查（fixture 文件由工具写入沙箱），
// 工具面缺了它时该任务必失败——这正是「eval 注册表未含 MemorySave/Search」的后果。
func TestRunTaskGivesMemoryTasksTheMemoryTools(t *testing.T) {
	tasksDir := t.TempDir()
	writeTask(t, tasksDir, "correction", `---
name: correction
description: same-turn correction through the memory tools
---
## Task
record the corrected coffee preference
## Checks
- file-exists: .niuniu-agent/memory/americano-preference.md
- contains: .niuniu-agent/memory/americano-preference.md, americano
`)
	tasks, err := LoadTasks(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	reg := tools.NewRegistry(tools.LS{}, tools.Read{}, tools.Write{})
	res := RunTask(context.Background(), &memoryToolModel{}, reg, tasks[0], time.Minute, "")
	if !res.Pass {
		t.Fatalf("task did not pass through MemorySave: %+v", res)
	}
}

// eval 必须是密封的：开发机 ~/.niuniu-agent/memory 的个人记忆既不进提示词（隐私，
// 且同一任务在两台机器上会得出不同分数），宿主侧只保留项目层（RSI 效果通道）。
func TestRecallBodyExcludesHostUserLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 的 os.UserHomeDir 读 USERPROFILE
	writeMemoryEntry(t, filepath.Join(home, ".niuniu-agent", "memory"), "personal-note", "the developer's private note")

	sandbox := t.TempDir()
	writeMemoryEntry(t, filepath.Join(sandbox, ".niuniu-agent", "memory"), "sandbox-fixture", "task fixture text")
	hostProject := t.TempDir()
	writeMemoryEntry(t, hostProject, "rsi-lesson", "distilled lesson text")

	body := recallBody(hostProject, sandbox)
	if !strings.Contains(body, "task fixture text") {
		t.Fatalf("sandbox fixture missing from recall: %q", body)
	}
	if !strings.Contains(body, "distilled lesson text") {
		t.Fatalf("host project layer (RSI channel) missing from recall: %q", body)
	}
	if strings.Contains(body, "private note") {
		t.Fatalf("host user layer leaked into the eval prompt: %q", body)
	}
}
