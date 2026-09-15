package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// usageResp 构造带 usage 的助手回复。
func usageResp(blocks []model.Block, stop string, ctxTokens int) *model.Response {
	return &model.Response{
		Message:    model.Message{Role: model.RoleAssistant, Blocks: blocks},
		StopReason: stop,
		Usage:      model.Usage{InputTokens: ctxTokens},
	}
}

// 大上下文压力：第一条 Prompt 正常结束后，第二条 Prompt 中途超过阈值，
// 触发摘要压缩；压缩后第三轮请求应以摘要消息开头，且 tool_use/tool_result
// 配对完整，回合继续到最终回答。
func TestCompactReplacesEarlyMessagesWithSummary(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		// 第一次 Prompt：普通问答结束。
		usageResp([]model.Block{{Type: model.BlockText, Text: "first answer"}}, model.StopEndTurn, 100),
		// 第二次 Prompt 轮1：tool_use，上下文超阈值。
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "tu_2", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		// 压缩摘要调用（compact 内部发起）。
		usageResp([]model.Block{{Type: model.BlockText, Text: "SUMMARY-OF-EARLY"}}, model.StopEndTurn, 50),
		// 第二次 Prompt 轮2：压缩后的正常回答。
		usageResp([]model.Block{{Type: model.BlockText, Text: "final answer"}}, model.StopEndTurn, 300),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")

	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2}
	if _, err := s.Prompt(context.Background(), "first question", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	res, err := s.Prompt(context.Background(), "second question", opts)
	if err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	if res.Text != "final answer" {
		t.Errorf("final = %q", res.Text)
	}

	// 四次模型调用：prompt1、prompt2 轮1、摘要、prompt2 轮2。
	if fm.calls != 4 {
		t.Fatalf("model calls = %d, want 4", fm.calls)
	}
	// 摘要请求应使用压缩专用 system。
	sum := fm.reqs[2]
	if !strings.Contains(sum.System, "compress") && !strings.Contains(sum.System, "summary") {
		t.Errorf("summarizer system = %q, want compact-specific prompt", sum.System)
	}
	// 压缩后的轮2请求：[摘要 user, 第二问 user, assistant(tool_use), user(tool_result)]。
	after := fm.reqs[3].Messages
	if len(after) != 4 {
		t.Fatalf("post-compact messages = %d, want 4: %+v", len(after), after)
	}
	if !strings.Contains(after[0].Blocks[0].Text, "SUMMARY-OF-EARLY") {
		t.Errorf("message[0] = %+v, want summary user message", after[0])
	}
	if after[1].Blocks[0].Text != "second question" {
		t.Errorf("message[1] = %+v, want the second user question", after[1])
	}
	if after[2].Blocks[0].Type != model.BlockToolUse || after[3].Blocks[0].Type != model.BlockToolResult {
		t.Errorf("tool pair not preserved: %+v / %+v", after[2], after[3])
	}
}

func TestCompactCutLandsOnSafeBoundary(t *testing.T) {
	textUser := model.Message{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockText, Text: "q"}}}
	toolResultUser := model.Message{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockToolResult, ToolUseID: "x"}}}
	assistant := model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockToolUse, ID: "x", Name: "Echo"}}}

	msgs := []model.Message{textUser, assistant, toolResultUser, textUser, assistant, toolResultUser}
	// keep=2 → 期望切点落在 index 3（text user），绝不能落在 tool_result 之上。
	if got := compactCut(msgs, 2); got != 3 {
		t.Errorf("compactCut = %d, want 3", got)
	}
	// 没有更早的 text user 时退到 0（不压缩）。
	if got := compactCut(msgs[3:], 2); got != 0 {
		t.Errorf("compactCut(no earlier text user) = %d, want 0", got)
	}
}

func TestCompactSummarizerFailureIsNotFatal(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "first answer"}}, model.StopEndTurn, 100),                                          // idx0: prompt1
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "tu_2", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000), // idx1: prompt2 轮1
		nil, // idx2: 摘要调用（被 errs[2] 打断，占位）
		usageResp([]model.Block{{Type: model.BlockText, Text: "recovered answer"}}, model.StopEndTurn, 400), // idx3: 轮2 收尾
	}}
	fm.errs = map[int]error{2: errors.New("summarizer down")} // 第3次调用（摘要）失败
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")

	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	res, err := s.Prompt(context.Background(), "q2", opts)
	if err != nil {
		t.Fatalf("summarizer failure must not abort the turn: %v", err)
	}
	if res.Text != "recovered answer" {
		t.Errorf("final = %q, want the turn to finish on the full history", res.Text)
	}
	// 未压缩：最终请求保留完整历史（q1、a1、q2、tool_use、tool_result）。
	last := fm.reqs[len(fm.reqs)-1]
	if len(last.Messages) != 5 {
		t.Errorf("history unexpectedly rewritten: %d messages, want 5", len(last.Messages))
	}
}

func TestCompactDisabledWithNegativeThreshold(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "a"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "t", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 999_999),
		usageResp([]model.Block{{Type: model.BlockText, Text: "b"}}, model.StopEndTurn, 999_999),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	opts := Options{CompactThresholdTokens: -1}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}
	// prompt1 一轮回答 + prompt2 轮1 tool_use + 轮2 回答 = 3 次调用，无摘要调用。
	if fm.calls != 3 {
		t.Errorf("model calls = %d, want 3 (no summarizer call)", fm.calls)
	}
}
