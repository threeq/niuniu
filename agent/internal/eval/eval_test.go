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
