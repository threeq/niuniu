package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// 阈值未设置（Options 零值 0）时必须应用 DefaultCompactThreshold——
// 否则第二轮起「估算 > 0」恒真，每轮触发 compact：历史被反复压扁、
// 每轮多一次 summarizer 调用，宿主视角表现为「收不到后续消息」。
func TestCompactZeroThresholdAppliesDefault(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "answer one"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockText, Text: "answer two"}}, model.StopEndTurn, 200),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")

	// 零值阈值：普通体量的两轮对话不应触发任何 summarizer 调用。
	if _, err := s.Prompt(context.Background(), "q1", Options{CompactThresholdTokens: 0}); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", Options{CompactThresholdTokens: 0}); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	for i, r := range fm.reqs {
		if strings.Contains(r.System, "compress") || strings.Contains(r.System, "JSON object") {
			t.Errorf("round %d triggered an unexpected summarizer call (zero threshold must apply the default)", i)
		}
	}
	// 历史完整保留（q1/a1/q2/a2）。
	if len(s.messages) != 4 {
		t.Errorf("messages = %d, want 4 (no compaction)", len(s.messages))
	}
}

// 显式小阈值仍能正常触发 compact（默认值替换不破坏显式配置）。
func TestCompactExplicitThresholdStillFires(t *testing.T) {
	stateJSON := `{"goal":"g","key_decisions":["k"],"open_items":["o"]}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a1"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "a2"}}, model.StopEndTurn, 100),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	opts := Options{CompactThresholdTokens: 1000, KeepRecentMessages: 2}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatal(err)
	}
	if s.state == nil {
		t.Fatal("explicit threshold must still trigger compaction")
	}
}

// 负值仍是「禁用压缩」语义。
func TestCompactNegativeThresholdDisables(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a"}}, model.StopEndTurn, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: "b"}}, model.StopEndTurn, 500_000),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	if _, err := s.Prompt(context.Background(), "q1", Options{CompactThresholdTokens: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt(context.Background(), "q2", Options{CompactThresholdTokens: -1}); err != nil {
		t.Fatal(err)
	}
	for i, r := range fm.reqs {
		if strings.Contains(r.System, "JSON object") {
			t.Errorf("round %d: negative threshold must disable compaction", i)
		}
	}
}
