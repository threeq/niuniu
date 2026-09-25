package rsi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// —— 飞轮测试基建 ——

// gatedModel 模拟「经验驱动行为」的 Actor：
//   - curriculum 调用（prompt 含 "practice tasks"）→ 回放课程脚本
//   - actor 调用 → 仅当 system 含经验召回（"Relevant prior lessons" 或
//     "# Memory"）时按子命令写文件；否则什么都不做（模拟经验缺失/无效）
//   - 作弊模式（cheatOnVisible）：把 prompt 中 'expected:' 后的文本直接
//     抄进目标文件（对可见 contains 判定作弊；hidden 任务无 expected
//     提示 → 无法作弊 → 暴露过拟合）
type gatedModel struct {
	calls      int
	curriculum string // broad 课程脚本
	cheatOnVis bool   // 对可见任务作弊（抄 expected）
	memorySeen int    // actor 请求中带经验召回的次数
	actorCalls int
	mu         struct{ calls int }
}

func (m *gatedModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	sys := req.System
	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Text()
	}

	// Curriculum（出题）。
	if strings.Contains(prompt, "practice tasks") {
		m.calls++
		return resp(m.curriculum), nil
	}
	// Distill（经验提炼）。
	if strings.Contains(prompt, "Distill ONE") {
		m.calls++
		return resp("TITLE: gated-lesson\nTYPE: pattern\n---\nA verified lesson."), nil
	}

	// Actor（practice sandbox）：总会做——curriculum 任务设计为可解。
	if strings.Contains(sys, "practice sandbox") {
		m.actorCalls++
		m.execWrites(prompt)
		return resp("done"), nil
	}

	// 闸门/控制 eval：行为取决于是否有召回经验（这是被测的效应通道）。
	hasMemory := strings.Contains(sys, "# Memory") && strings.Contains(sys, "rsi-")

	// 作弊模式：只对带 expected 提示的任务照抄（可见集）；hidden 任务没有
	// 该提示 → 无法作弊 → 失败暴露过拟合。
	if m.cheatOnVis {
		var file, val string
		for _, ln := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(ln, "expected-file:") {
				file = strings.TrimSpace(strings.TrimPrefix(ln, "expected-file:"))
			}
			if strings.HasPrefix(ln, "expected:") {
				val = strings.TrimSpace(strings.TrimPrefix(ln, "expected:"))
			}
		}
		if file != "" && val != "" {
			m.execWritesPrompt(file + " = " + val)
			return resp("done (cheated)"), nil
		}
		m.actorCalls++
		return resp("cannot do this one"), nil
	}

	if hasMemory {
		m.memorySeen++
		m.actorCalls++
		m.execWrites(prompt)
		return resp("done correctly (with lessons)"), nil
	}
	// 无经验：任务失败。
	m.actorCalls++
	return resp("I do not know how to do this without lessons."), nil
}

// execWrites 执行 prompt 中 "write file = content" 子命令。
func (m *gatedModel) execWrites(prompt string) {
	for _, line := range strings.Split(prompt, "\n") {
		if rest, ok := strings.CutPrefix(line, "write "); ok {
			if file, content, ok2 := strings.Cut(rest, " = "); ok2 {
				dir, _ := os.Getwd()
				full := filepath.Join(dir, file)
				_ = os.MkdirAll(filepath.Dir(full), 0o755)
				_ = os.WriteFile(full, []byte(content), 0o644)
			}
		}
	}
}

func (m *gatedModel) execWritesPrompt(s string) {
	if file, content, ok := strings.Cut(s, " = "); ok {
		dir, _ := os.Getwd()
		full := filepath.Join(dir, file)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
}

func resp(text string) *model.Response {
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
		{Type: model.BlockText, Text: text}}}}
}

// writeTaskDoc 在 dir 下写一个 eval 任务 md。
func writeTaskDoc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func visibleTaskDoc() string {
	return `---
name: vis-write
description: visible task
---
## Task
write result.txt = correct-output
expected: correct-output
expected-file: result.txt
## Fixtures
- in.txt: seed
## Checks
- file-exists: result.txt
- contains: result.txt, correct-output`
}

func hiddenTaskDoc() string {
	return `---
name: hid-transform
description: hidden held-out task
---
## Task
write out/final.txt = transformed-value
## Fixtures
- src.txt: raw
## Checks
- file-exists: out/final.txt
- contains: out/final.txt, transformed-value`
}

// —— 闸门单元 ——

func TestDriftGate(t *testing.T) {
	dir := t.TempDir()
	store := memory.NewStoreDir(filepath.Join(dir, "mem"))
	// 既有 2 条。
	_, _ = store.Save(memory.Entry{Title: "existing-1", Type: memory.TypePattern, Content: "c1", Tags: []string{"rsi"}})
	_, _ = store.Save(memory.Entry{Title: "existing-2", Type: memory.TypePattern, Content: "c2", Tags: []string{"rsi"}})

	candidates := []memory.Entry{
		{Title: "new-a", Type: memory.TypePattern, Content: "n1", Tags: []string{"rsi"}},
		{Title: "new-b", Type: memory.TypePattern, Content: "n2", Tags: []string{"rsi"}},
	}
	// 数量边界：允许 3 条新增。
	if g := driftGate(store, candidates, 3); !g.Pass {
		t.Fatalf("drift gate = %+v, want pass", g)
	}
	// 数量边界收紧为 1 → 拒绝。
	if g := driftGate(store, candidates, 1); g.Pass {
		t.Fatalf("drift gate passed, want reject (3 candidates > 1)")
	}
	// 重复边界：候选内容与既有完全相同 → 重复拒绝。
	dup := []memory.Entry{
		{Title: "existing-1-copy", Type: memory.TypePattern, Content: "c1", Tags: []string{"rsi"}},
	}
	if g := driftGate(store, dup, 10); g.Pass {
		t.Errorf("duplicate candidate must be rejected: %+v", g)
	}
}

func TestRegressionGateToleratesZeroDeltaOnly(t *testing.T) {
	good := evalSummaryP(20, 20)
	same := evalSummaryP(20, 20)
	if g := regressionGate(good, same); !g.Pass {
		t.Fatalf("zero delta must pass: %+v", g)
	}
	worse := evalSummaryP(20, 18)
	if g := regressionGate(good, worse); g.Pass {
		t.Fatalf("regression must be rejected: %+v", g)
	}
}

func evalSummaryP(total, passed int) *eval.Summary {
	s := &eval.Summary{GeneratedAt: time.Now(), Total: total, Passed: passed, Failed: total - passed}
	for i := 0; i < total; i++ {
		s.Results = append(s.Results, eval.Result{Name: itoap(i), Pass: i < passed})
	}
	return s
}

func itoap(i int) string {
	names := []string{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9",
		"t10", "t11", "t12", "t13", "t14", "t15", "t16", "t17", "t18", "t19"}
	if i < len(names) {
		return names[i]
	}
	return "tn"
}

// —— 飞轮端到端（fake model 三角色 + 全闸门）——

func gatedFlywheel(t *testing.T, m model.Model) (*Flywheel, string) {
	t.Helper()
	root := t.TempDir()
	visible := filepath.Join(root, "tasks")
	hidden := filepath.Join(root, "hidden")
	writeTaskDoc(t, visible, "vis-write", visibleTaskDoc())
	writeTaskDoc(t, hidden, "hid-transform", hiddenTaskDoc())
	memDir := filepath.Join(root, "mem")
	_ = os.MkdirAll(memDir, 0o755)
	staging := filepath.Join(root, "staging")
	_ = os.MkdirAll(staging, 0o755)
	out := filepath.Join(root, "out")

	fw := &Flywheel{
		Model:       m,
		Reg:         tools.NewRegistry(tools.LS{}, tools.Read{}, tools.Write{}),
		Store:       memory.NewStoreDir(memDir),
		Cwd:         root,
		VisibleDir:  visible,
		HiddenDir:   hidden,
		OutDir:      out,
		StagingDir:  staging,
		Broad:       1,
		Deep:        0,
		DriftMaxNew: 5,
	}
	return fw, memDir
}

// 良性候选：actor 在有经验时正确完成任务 → 闸门全过 → 经验落地。
func TestFlywheelLandsVerifiedLessons(t *testing.T) {
	cur := `---
name: rsi-write-result
description: write result
---
## Task
write result.txt = good-value
## Fixtures
- in.txt: seed
## Checks
- file-exists: result.txt`
	m := &gatedModel{curriculum: cur}
	fw, memDir := gatedFlywheel(t, m)

	report, err := fw.Spin(context.Background())
	if err != nil {
		t.Fatalf("Spin: %v", err)
	}
	for _, g := range report.Gates {
		t.Logf("gate %s: pass=%v detail=%s", g.Name, g.Pass, g.Detail)
		if !g.Pass {
			t.Fatalf("gate %s failed: %s", g.Name, g.Detail)
		}
	}
	t.Logf("landed=%v attempts=%d", report.Landed, report.Attempts)
	if len(report.Landed) == 0 {
		t.Fatal("no lessons landed")
	}
	data, err := os.ReadFile(filepath.Join(memDir, report.Landed[0]+".md"))
	if err != nil {
		t.Fatalf("landed lesson missing: %v", err)
	}
	if !strings.Contains(string(data), "rsi") {
		t.Errorf("landed entry missing rsi tag: %s", data)
	}
}

// 过拟合/有害候选：actor 只能靠抄 expected 通过可见任务，hidden 全挂
// → 隐藏闸门拒绝；落地为零，作弊经验不进生产记忆。
func TestFlywheelRejectsOverfitCandidates(t *testing.T) {
	cur := `---
name: rsi-cheat
description: cheat
---
## Task
write result.txt = cheat-value
expected: cheat-value
expected-file: result.txt
## Fixtures
- in.txt: seed
## Checks
- file-exists: result.txt`
	m := &gatedModel{curriculum: cur, cheatOnVis: true}
	fw, memDir := gatedFlywheel(t, m)

	report, err := fw.Spin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Landed) != 0 {
		t.Fatalf("landed = %v, want none (overfit rejected)", report.Landed)
	}
	rejected := false
	for _, g := range report.Gates {
		if !g.Pass {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("at least one gate must have rejected the overfit candidates")
	}
	entries, _ := memory.NewStoreDir(memDir).Search("rsi")
	for _, e := range entries {
		if strings.Contains(e.Content, "cheat") {
			t.Errorf("cheat lesson leaked to production memory: %+v", e)
		}
	}
}
