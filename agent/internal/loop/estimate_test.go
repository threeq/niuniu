package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// 估算器：英文 ~4 chars/token、CJK ~1 rune/token，混合文本在合理区间。
func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("hello world, this is a test"); got < 5 || got > 10 {
		t.Errorf("english = %d, want 5..10", got)
	}
	if got := EstimateTokens("这是一段中文测试文本，用于验证估算。"); got < 15 || got > 25 {
		t.Errorf("cjk = %d, want 15..25 (≈rune count)", got)
	}
	if EstimateTokens("") != 0 {
		t.Error("empty must be 0")
	}
	// 消息级：system + 多条消息的总量大于任一部分。
	sys := "you are an agent"
	msgs := []model.Message{
		{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockText, Text: "hello"}}},
		{Role: model.RoleAssistant, Blocks: []model.Block{
			{Type: model.BlockToolUse, ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockToolResult, ToolUseID: "t1", Text: "file1\nfile2"}}},
	}
	total := EstimateMessagesTokens(sys, msgs)
	if total <= EstimateTokens(sys) {
		t.Errorf("total = %d, must exceed system alone", total)
	}
}

// 网关不上报 usage 时（lastCtx 恒 0），本地估算接管 compact 触发：
// 上下文超过阈值即压缩，不再依赖 provider 数字。
func TestCompactFiresOnLocalEstimate(t *testing.T) {
	big := strings.Repeat("word ", 30_000) // ~7.5k tokens 估算，足够超小阈值
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: big}}, model.StopEndTurn, 0), // 0 = 网关不报
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 0),
		usageResp([]model.Block{{Type: model.BlockText, Text: `{"goal":"g","key_decisions":["k"],"open_items":["o"]}`}}, model.StopEndTurn, 0),
		usageResp([]model.Block{{Type: model.BlockText, Text: "done"}}, model.StopEndTurn, 0),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	opts := Options{CompactThresholdTokens: 1000, KeepRecentMessages: 2}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	// 压缩发生：s.state 被结构化路径填充（网关零上报仍触发）。
	if s.state == nil {
		t.Fatal("local-estimate fallback did not trigger compaction")
	}
}

// 工具失败 → tool_result 附 [system-reminder] 纠偏块；成功 → 不附。
func TestSystemReminderOnToolFailure(t *testing.T) {
	fm := &failOnceModel{}
	reg := tools.NewRegistry(&errTool{}) // 总是返回错误
	s := NewSession(fm, reg, "sys")
	if _, err := s.Prompt(context.Background(), "go", Options{MaxTurns: 2}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	found := false
	for _, m := range s.messages {
		for _, b := range m.Blocks {
			if b.Type == model.BlockToolResult && strings.Contains(b.Text, "[system-reminder]") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("error tool_result missing system-reminder")
	}
}

// ---- 测试辅助 ----

// failOnceModel：turn1 请求工具，turn2 结束。
type failOnceModel struct{ calls int }

func (f *failOnceModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	f.calls++
	if f.calls == 1 {
		return &model.Response{
			Message:    model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockToolUse, ID: "x1", Name: "Err", Input: json.RawMessage(`{}`)}}},
			StopReason: model.StopToolUse,
		}, nil
	}
	return &model.Response{
		Message:    model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockText, Text: "gave up"}}},
		StopReason: model.StopEndTurn,
	}, nil
}

// errTool：总是失败的工具。
type errTool struct{}

func (errTool) Def() model.ToolDef {
	return model.ToolDef{Name: "Err", Description: "always fails", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (errTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", context.DeadlineExceeded
}
