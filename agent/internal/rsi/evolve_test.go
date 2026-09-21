package rsi

import (
	"os"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
	"github.com/niuniu-dev/niuniu/agent/internal/memory"
)

// think-first 提案解析：三段齐全通过，缺段拒绝。
func TestParseThinkFirstProposal(t *testing.T) {
	good := `MECHANISM: 在 system 里先给文件路径清单再改代码，减少盲目 Glob。
EXPECTED GAIN: 公开集 +10%（路径错误的失败消除）。
FALSIFICATION: 若公开集失败模式不含路径错误则本提案无效。`
	p, err := ParseThinkFirst(good)
	if err != nil {
		t.Fatalf("ParseThinkFirst: %v", err)
	}
	if !strings.Contains(p.Mechanism, "路径清单") || !strings.Contains(p.ExpectedGain, "+10%") ||
		!strings.Contains(p.Falsification, "无效") {
		t.Fatalf("proposal = %+v", p)
	}
	// 缺 FALSIFICATION → 拒绝。
	bad := "MECHANISM: x\nEXPECTED GAIN: y"
	if _, err := ParseThinkFirst(bad); err == nil || !strings.Contains(err.Error(), "FALSIFICATION") {
		t.Fatalf("err = %v, want missing-FALSIFICATION", err)
	}
	// 空段值 → 拒绝。
	empty := "MECHANISM:\nEXPECTED GAIN: y\nFALSIFICATION: z"
	if _, err := ParseThinkFirst(empty); err == nil {
		t.Fatal("empty mechanism must be rejected")
	}
}

func TestSplitTasksDeterministic(t *testing.T) {
	var tasks []eval.Task
	for i := 0; i < 10; i++ {
		tasks = append(tasks, eval.Task{Name: itoap(i)})
	}
	pub, priv := SplitTasks(tasks, 0.6)
	if len(pub)+len(priv) != 10 || len(priv) == 0 || len(pub) == 0 {
		t.Fatalf("pub=%d priv=%d", len(pub), len(priv))
	}
	// 确定性：同输入同拆分。
	pub2, priv2 := SplitTasks(tasks, 0.6)
	for i := range pub {
		if pub[i].Name != pub2[i].Name {
			t.Fatal("split not deterministic")
		}
	}
	for i := range priv {
		if priv[i].Name != priv2[i].Name {
			t.Fatal("private split not deterministic")
		}
	}
	// 公私不重叠。
	seen := map[string]bool{}
	for _, tsk := range pub {
		seen[tsk.Name] = true
	}
	for _, tsk := range priv {
		if seen[tsk.Name] {
			t.Fatalf("task %q in both sets", tsk.Name)
		}
	}
}

// 适应度门控：私有集新分 >= 旧分 → 采纳；低于 → 拒绝。
func TestFitnessGate(t *testing.T) {
	if !FitnessGate(0.8, 0.7) {
		t.Error("new >= old must adopt")
	}
	if FitnessGate(0.6, 0.7) {
		t.Error("new < old must reject")
	}
	if !FitnessGate(0.7, 0.7) {
		t.Error("equal must adopt (>=)")
	}
}

// 对抗复验：胜者换新求解重测均值仍胜 → 采纳；均值下降 → 拒绝。
func TestAdversarialRetest(t *testing.T) {
	// 新 prompt 两轮 0.9/0.8 均值 0.85 > 旧 0.6 → 采纳。
	if !AdversarialRetest([]float64{0.9, 0.8}, 0.6) {
		t.Error("winner should be crowned")
	}
	// 新 prompt 一轮 0.5 均值 0.5 < 旧 0.6 → 拒绝。
	if AdversarialRetest([]float64{0.5}, 0.6) {
		t.Error("loser should not be crowned")
	}
}

// 分体裁路由：RecallFor 对不同 hint 返回不同体裁经验（同题优先）。
func TestGenreRoutingRecall(t *testing.T) {
	s := memory.NewStoreDir(func() string {
		d, _ := os.MkdirTemp("", "rsi-genre")
		return d
	}())
	_, err := s.Save(memory.Entry{Title: "json-best-practice", Type: memory.TypePattern, Content: "JSON 修改前先解析校验", Tags: []string{"json"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(memory.Entry{Title: "deploy-runbook", Type: memory.TypeDecision, Content: "部署走灰度流程", Tags: []string{"deploy"}}); err != nil {
		t.Fatal(err)
	}
	outJSON, _ := s.RecallFor("JSON 配置解析", 1, 2048)
	if !strings.Contains(outJSON, "json-best-practice") {
		t.Errorf("genre routing missed json lesson: %s", outJSON)
	}
	outDeploy, _ := s.RecallFor("部署 灰度", 1, 2048)
	if !strings.Contains(outDeploy, "deploy-runbook") {
		t.Errorf("genre routing missed deploy lesson: %s", outDeploy)
	}
}
