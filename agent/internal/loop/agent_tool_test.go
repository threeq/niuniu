package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// —— Agent 工具（subagent）——

func agentToolUse(id, prompt string) *model.Response {
	return &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
			{Type: model.BlockToolUse, ID: id, Name: "Agent",
				Input: json.RawMessage(`{"prompt":` + jsonString(prompt) + `}`)}},
		},
		StopReason: model.StopToolUse,
		Usage:      model.Usage{InputTokens: 100, OutputTokens: 10},
	}
}

func textRespUsage(s string, in, out int) *model.Response {
	return &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
			{Type: model.BlockText, Text: s}}},
		StopReason: model.StopEndTurn,
		Usage:      model.Usage{InputTokens: in, OutputTokens: out},
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newAgentFactory 装配一个子注册表里没有 Agent 工具的工厂。
func newAgentFactory(m model.Model) *AgentFactory {
	return &AgentFactory{
		Model:  m,
		System: "child-system",
		NewChildRegistry: func() *tools.Registry {
			return tools.NewRegistry(&stubTool{})
		},
		Timeout:  30 * time.Second,
		MaxDepth: 1,
	}
}

func TestAgentToolRunsChildAndBackfills(t *testing.T) {
	// 脚本顺序：父轮1(tool_use Agent) → 子轮(直接终答) → 父轮2(终答)。
	fm := &fakeModel{script: []*model.Response{
		agentToolUse("tu_a", "summarize the tree"),
		textRespUsage("child answer", 50, 5),
		textRespUsage("parent final", 100, 10),
	}}
	tool := NewAgentTool(newAgentFactory(fm), 0)
	reg := tools.NewRegistry(&stubTool{}, tool)

	res, err := Run(context.Background(), fm, reg, "parent-system", "go", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "parent final" {
		t.Errorf("final = %q", res.Text)
	}

	// 子会话以独立对话运行：child system + 自己的 prompt，无父历史。
	childReq := fm.reqs[1]
	if childReq.System != "child-system" {
		t.Errorf("child system = %q", childReq.System)
	}
	if len(childReq.Messages) != 1 || childReq.Messages[0].Text() != "summarize the tree" {
		t.Errorf("child messages = %+v, want single user prompt", childReq.Messages)
	}
	// 子注册表不得再含 Agent 工具（递归深度限 1）。
	for _, td := range childReq.Tools {
		if td.Name == "Agent" {
			t.Errorf("child registry must not contain the Agent tool")
		}
	}

	// 回填：tool_result 含子文本与 usage 摘要。
	tr := fm.reqs[2].Messages[2].Blocks[0]
	if tr.Type != model.BlockToolResult || tr.ToolUseID != "tu_a" {
		t.Fatalf("tool_result = %+v", tr)
	}
	if !strings.Contains(tr.Text, "child answer") || !strings.Contains(tr.Text, "input=50") {
		t.Errorf("backfill = %q, want child answer + usage line", tr.Text)
	}

	// usage 归属：父 TurnResult 只累计父轮（轮1 100/10 + 轮2 100/10），
	// 子会话的 50/5 不得泄漏进来。
	if res.Usage.InputTokens != 200 || res.Usage.OutputTokens != 20 {
		t.Errorf("parent usage = %+v, want 200/20 (child usage must not leak)", res.Usage)
	}
}

func TestAgentToolDepthLimit(t *testing.T) {
	fm := &fakeModel{}
	tool := NewAgentTool(newAgentFactory(fm), 1) // 已在深度 1（子 agent 里）
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("err = %v, want depth-limit error", err)
	}
}

func TestAgentToolTimeout(t *testing.T) {
	blocking := &blockingModel{release: make(chan struct{})}
	f := newAgentFactory(nil)
	f.Model = blocking
	f.Timeout = 30 * time.Millisecond
	tool := NewAgentTool(f, 0)
	defer close(blocking.release) // 让测试能退出

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("err = %v, want deadline-exceeded error", err)
	}
}

func TestAgentToolInputValidation(t *testing.T) {
	tool := NewAgentTool(newAgentFactory(&fakeModel{}), 0)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("want error for missing prompt")
	}
}

// blockingModel 挂起直到 ctx 取消或 release 关闭。
type blockingModel struct{ release chan struct{} }

func (b *blockingModel) Complete(ctx context.Context, _ model.Request) (*model.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return textRespUsage("never", 0, 0), nil
	}
}
