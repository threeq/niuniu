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

// compact 刷新印象 round-trip：summarizer 输出 impression 字段 → 落盘
// impression.md（经 LoadImpression 可读回）→ 结构化状态文件同步携带。
func TestCompactRefreshesImpression(t *testing.T) {
	impression := "技术栈: Go 多模块\n关键决策: 状态落盘用户目录\n用户脾气: 讨厌冗长汇报\n当前阶段: P4 记忆升级"
	stateJSON := `{"goal":"修复解析器","key_decisions":["切点选纯文本user消息"],"open_items":["补回归测试"],` +
		`"impression":"` + strings.ReplaceAll(impression, "\n", "\\n") + `"}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	dir := t.TempDir()
	opts := Options{
		CompactThresholdTokens: 100_000,
		KeepRecentMessages:     2,
		CompactStatePath:       filepath.Join(dir, "session-state.json"),
		ImpressionPath:         filepath.Join(dir, "impression.md"),
	}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}

	// 印象落盘且 round-trip 一致。
	got, ok := tools.LoadImpression(opts.ImpressionPath)
	if !ok {
		t.Fatal("impression file missing after compact")
	}
	if got != impression {
		t.Errorf("impression round-trip:\n got %q\nwant %q", got, impression)
	}
	// 结构化状态文件携带同一印象。
	data, err := os.ReadFile(opts.CompactStatePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var disk CompactState
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatalf("state file json: %v", err)
	}
	if disk.Impression != impression {
		t.Errorf("state file impression = %q", disk.Impression)
	}
	// 印象属于跨 session 氛围：不渲染进本轮 [auto-compacted] 消息。
	sum := fm.reqs[3].Messages[0].Blocks[0].Text
	if strings.Contains(sum, "用户脾气") {
		t.Errorf("impression leaked into compact message: %q", sum)
	}
}

// 硬约束贯穿生成链路：模型输出超长印象 → 落盘被截到 200 rune。
func TestCompactImpressionCappedAt200Runes(t *testing.T) {
	big := strings.Repeat("印", tools.MaxImpressionRunes+80)
	stateJSON := `{"goal":"g","key_decisions":["k"],"open_items":["o"],"impression":"` + big + `"}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	dir := t.TempDir()
	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2, ImpressionPath: filepath.Join(dir, "impression.md")}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	got, ok := tools.LoadImpression(opts.ImpressionPath)
	if !ok {
		t.Fatal("impression file missing")
	}
	if n := len([]rune(got)); n != tools.MaxImpressionRunes {
		t.Errorf("impression = %d runes, want exactly %d", n, tools.MaxImpressionRunes)
	}
}

// 跨 session 承接：磁盘已有印象 + 全新 session（s.state 为空，SessionState
// 不持久化 state）的首次 compact——旧印象必须被喂给 summarizer 并在其省略
// impression 字段时原样保留，而不是被本会话的一次性猜测覆盖。
func TestFirstCompactSeedsImpressionFromDisk(t *testing.T) {
	existing := "技术栈: Rust\n关键决策: 侧车内嵌\n用户脾气: 简洁\n当前阶段: 联调"
	stateJSON := `{"goal":"g","key_decisions":["k"],"open_items":["o"]}` // 未输出 impression
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys") // 全新 session：s.state == nil
	dir := t.TempDir()
	imp := filepath.Join(dir, "impression.md")
	if err := tools.WriteImpression(imp, existing); err != nil {
		t.Fatal(err)
	}
	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2, ImpressionPath: imp}
	// q1 单轮；q2 的工具轮（usage 500k）越过压缩阈值 → 触发本 session 的首次
	// compact（summarize 即第 3 个请求）。
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}

	// summarizer 收到了磁盘上的旧印象（种子进入 Previous state 指令）。
	req := fm.reqs[2]
	instr := req.Messages[len(req.Messages)-1].Blocks[0].Text
	if !strings.Contains(instr, "Previous state") || !strings.Contains(instr, "技术栈") {
		t.Errorf("summarizer did not receive the on-disk impression seed:\n%s", instr)
	}
	// 磁盘印象原样保留（summarizer 未输出 impression → 沿用种子）。
	got, ok := tools.LoadImpression(imp)
	if !ok || got != existing {
		t.Errorf("cross-session impression overwritten on first compact: %q,%v", got, ok)
	}
}

// mergeState 是印象上限的唯一施加点：链式 "Previous state"、session-state.json
// 与印象文件三处必须字节一致，不能出现"文件截断、链上未截断"的双视图。
func TestMergeStateCapsImpression(t *testing.T) {
	big := strings.Repeat("印", tools.MaxImpressionRunes+50)
	out := mergeState(nil, &CompactState{Impression: big})
	if n := len([]rune(out.Impression)); n > tools.MaxImpressionRunes {
		t.Errorf("merged impression = %d runes, want capped at %d", n, tools.MaxImpressionRunes)
	}
}

// 连锁压缩：第二次 summarizer 未输出 impression 字段 → 沿用上一版印象，
// 文件不被清空（更新不能静默丢内容）。
func TestCompactChainedImpressionCarriedForward(t *testing.T) {
	impression := "技术栈: Go\n关键决策: 决策A\n用户脾气: 直接\n当前阶段: 调试"
	first := `{"goal":"g1","key_decisions":["决策A"],"open_items":["待办1"],"impression":"` +
		strings.ReplaceAll(impression, "\n", "\\n") + `"}`
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
	dir := t.TempDir()
	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2, ImpressionPath: filepath.Join(dir, "impression.md")}
	for _, q := range []string{"q1", "q2", "q3"} {
		if _, err := s.Prompt(context.Background(), q, opts); err != nil {
			t.Fatalf("prompt %s: %v", q, err)
		}
	}
	if s.state == nil || s.state.Impression != impression {
		t.Errorf("chained state impression = %+v, want carried forward", s.state)
	}
	got, ok := tools.LoadImpression(opts.ImpressionPath)
	if !ok || got != impression {
		t.Errorf("impression file after chained compact = %q,%v; want original kept", got, ok)
	}
	// 第二次更新：新印象覆盖旧印象。
	impression2 := "技术栈: Go\n关键决策: 决策B\n用户脾气: 直接\n当前阶段: 收尾"
	third := `{"goal":"g3","key_decisions":["决策C"],"open_items":["待办3"],"impression":"` +
		strings.ReplaceAll(impression2, "\n", "\\n") + `"}`
	fm.script = []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a4"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t3", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: third}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a5"}}, model.StopEndTurn, 100),
	}
	fm.calls = 0 // 重设脚本须同时重置消费索引
	if _, err := s.Prompt(context.Background(), "q4", opts); err != nil {
		t.Fatalf("prompt4: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q5", opts); err != nil {
		t.Fatalf("prompt5: %v", err)
	}
	got, ok = tools.LoadImpression(opts.ImpressionPath)
	if !ok || got != impression2 {
		t.Errorf("impression not superseded: %q,%v", got, ok)
	}
}

// 生成失败不影响会话：印象路径不可写（父目录是文件）时 Prompt 照常完成。
func TestCompactImpressionWriteFailureTolerated(t *testing.T) {
	stateJSON := `{"goal":"g","key_decisions":["k"],"open_items":["o"],"impression":"技术栈: Go"}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("a file, not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		CompactThresholdTokens: 100_000,
		KeepRecentMessages:     2,
		ImpressionPath:         filepath.Join(blocker, "impression.md"), // MkdirAll 必失败
	}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("impression write failure must not fail the prompt: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
}
