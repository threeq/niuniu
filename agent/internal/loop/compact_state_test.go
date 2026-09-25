package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// 结构化状态解析：裸 JSON 与代码围栏 JSON 均可，malformed 报错。
func TestParseStateJSON(t *testing.T) {
	raw := `{"goal":"修复登录bug","key_decisions":["改用JWT"],"open_items":["补单测"]}`
	st, err := parseStateJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Goal != "修复登录bug" || len(st.KeyDecisions) != 1 || len(st.OpenItems) != 1 {
		t.Fatalf("state = %+v", st)
	}

	fenced := "```json\n" + raw + "\n```"
	if _, err := parseStateJSON(fenced); err != nil {
		t.Fatalf("fenced parse: %v", err)
	}

	if _, err := parseStateJSON("不是 JSON 的摘要"); err == nil {
		t.Fatal("plain text must not parse as state")
	}
	// 缺 open_items 数组 → 无效。
	if _, err := parseStateJSON(`{"goal":"x"}`); err == nil {
		t.Fatal("missing open_items must be rejected")
	}
}

// 合并：决策/文件累积去重，Open items 用最新替换，Compactions 递增。
func TestMergeState(t *testing.T) {
	old := &CompactState{
		Goal:         "旧目标",
		KeyDecisions: []string{"决策A", "决策B"},
		FilesTouched: []string{"a.go"},
		OpenItems:    []string{"旧待办"},
		Compactions:  1,
	}
	nw := &CompactState{
		Goal:         "新目标",
		KeyDecisions: []string{"决策B", "决策C"},
		FilesTouched: []string{"a.go", "b.go"},
		OpenItems:    []string{"新待办1", "新待办2"},
	}
	got := mergeState(old, nw)
	if got.Goal != "新目标" {
		t.Errorf("goal = %q, want new", got.Goal)
	}
	if len(got.KeyDecisions) != 3 {
		t.Errorf("decisions = %v, want A/B/C deduped", got.KeyDecisions)
	}
	if len(got.FilesTouched) != 2 {
		t.Errorf("files = %v", got.FilesTouched)
	}
	if strings.Join(got.OpenItems, ",") != "新待办1,新待办2" {
		t.Errorf("open items = %v, want replaced by newest", got.OpenItems)
	}
	if got.Compactions != 2 {
		t.Errorf("compactions = %d, want 2", got.Compactions)
	}

	// 上限：决策超 cap 截断，最旧的被丢弃（保留最新的）。
	big := &CompactState{KeyDecisions: make([]string, 0, 30)}
	for i := 0; i < 25; i++ {
		big.KeyDecisions = append(big.KeyDecisions, "决策"+strings.Repeat("x", i))
	}
	capped := mergeState(&CompactState{KeyDecisions: big.KeyDecisions}, &CompactState{OpenItems: []string{"z"}})
	if len(capped.KeyDecisions) != maxKeyDecisions {
		t.Errorf("capped decisions = %d, want %d", len(capped.KeyDecisions), maxKeyDecisions)
	}
}

// compact 产出结构化状态：合并进 s.state、消息含状态文本、并落盘。
func TestCompactProducesStructuredState(t *testing.T) {
	stateJSON := `{"goal":"修复解析器","constraints":["零第三方依赖"],"key_decisions":["切点选纯文本user消息"],"files_touched":["loop.go"],"open_items":["补回归测试"]}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "first answer"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "tu_2", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "final answer"}}, model.StopEndTurn, 300),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	statePath := filepath.Join(t.TempDir(), ".niuniu-agent", "session-state.json")

	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2, CompactStatePath: statePath}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}

	// 内存状态已合并。
	if s.state == nil || s.state.Goal != "修复解析器" || len(s.state.KeyDecisions) != 1 {
		t.Fatalf("session state = %+v", s.state)
	}
	// 落盘文件与内存状态一致。
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var disk CompactState
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatalf("state file json: %v", err)
	}
	if disk.Goal != "修复解析器" || disk.Compactions != 1 {
		t.Errorf("disk state = %+v", disk)
	}
	// 上下文消息含结构化渲染 + 落盘指针。
	sum := fm.reqs[3].Messages[0].Blocks[0].Text
	if !strings.Contains(sum, "修复解析器") || !strings.Contains(sum, "切点选纯文本user消息") {
		t.Errorf("summary message missing structured content: %q", sum)
	}
	if !strings.Contains(sum, "session-state.json") {
		t.Errorf("summary message missing state-file pointer: %q", sum)
	}
}

// 连锁压缩不丢旧决策：第二次 compact 时旧 KeyDecisions 保留（防「摘要的
// 摘要」失真的核心断言），且 summarizer 收到先前状态供参考。
func TestChainedCompactionPreservesDecisions(t *testing.T) {
	first := `{"goal":"g","key_decisions":["决策A"],"open_items":["待办1"]}`
	second := `{"goal":"g2","key_decisions":["决策B"],"open_items":["待办2"]}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: first}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t2", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: second}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a3"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2}

	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q3", opts); err != nil {
		t.Fatalf("prompt3: %v", err)
	}

	// 两次压缩后：决策A（第一次）与决策B（第二次）都在。
	if s.state == nil {
		t.Fatal("no state after two compactions")
	}
	joined := strings.Join(s.state.KeyDecisions, "|")
	if !strings.Contains(joined, "决策A") || !strings.Contains(joined, "决策B") {
		t.Errorf("chained compaction lost decisions: %v", s.state.KeyDecisions)
	}
	if s.state.Compactions != 2 {
		t.Errorf("compactions = %d, want 2", s.state.Compactions)
	}
	// 第二次 summarizer 请求带上了先前状态（供模型保留旧决策）。
	secondSum := fm.reqs[5]
	foundPrev := false
	for _, m := range secondSum.Messages {
		if m.Role == model.RoleUser && strings.Contains(m.Blocks[0].Text, "决策A") {
			foundPrev = true
		}
	}
	if !foundPrev {
		t.Error("second summarizer call did not include previous state")
	}
}

// 模型不配合输出 JSON 时回退纯文本摘要（现有行为不回归）。
func TestCompactFallsBackToPlainText(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: "PLAIN-SUMMARY-TEXT"}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	if s.state != nil {
		t.Errorf("state should stay nil on fallback, got %+v", s.state)
	}
	sum := fm.reqs[3].Messages[0].Blocks[0].Text
	if !strings.Contains(sum, "PLAIN-SUMMARY-TEXT") {
		t.Errorf("fallback summary missing: %q", sum)
	}
}
