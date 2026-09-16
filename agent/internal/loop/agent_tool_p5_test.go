package loop

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// p5 fakeModel：可控延迟，验证并行。
type slowModel struct {
	mu       sync.Mutex
	entered  chan struct{}
	release  chan struct{}
	enteredN int
}

func (m *slowModel) Complete(_ context.Context, _ model.Request) (*model.Response, error) {
	m.mu.Lock()
	m.enteredN++
	if m.enteredN == 2 {
		close(m.entered) // 第二个子 agent 进入时通知
	}
	m.mu.Unlock()
	<-m.release
	return textRespUsage("ok", 1, 1), nil
}

// —— 共享侧 ——

// cwd 共享：子 agent 与父会话同进程同 cwd（含 session chdir 后）。
func TestAgentToolSharesCwd(t *testing.T) {
	cwdCapture := &cwdCaptureModel{}
	f := newAgentFactory(cwdCapture)
	f.NewChildRegistry = func() *tools.Registry { return tools.NewRegistry(&stubTool{}) }
	tool := NewAgentTool(f, 0)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`)); err != nil {
		t.Fatal(err)
	}
	parentCwd, _ := os.Getwd()
	if cwdCapture.cwd != parentCwd {
		t.Errorf("child cwd = %q, parent cwd = %q — subagent must share the parent process cwd", cwdCapture.cwd, parentCwd)
	}
}

type cwdCaptureModel struct{ cwd string }

func (m *cwdCaptureModel) Complete(_ context.Context, _ model.Request) (*model.Response, error) {
	m.cwd, _ = os.Getwd()
	return textRespUsage("ok", 0, 0), nil
}

// ContextPreamble（factory 自动附带）与 context 入参都叠加进子 prompt。
func TestAgentToolContextPreambleAndContext(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{textRespUsage("ok", 0, 0)}}
	f := newAgentFactory(fm)
	f.ContextPreamble = "父会话背景：正在修 kanban bug。"
	tool := NewAgentTool(f, 0)

	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"prompt":"具体任务","context":"关键发现：repro 在 advance_issue"}`)); err != nil {
		t.Fatal(err)
	}
	req := fm.reqs[0]
	msg := req.Messages[0].Text()
	// 叠加顺序：preamble → context → prompt。
	iPreamble := strings.Index(msg, "kanban bug")
	iContext := strings.Index(msg, "repro 在 advance_issue")
	iPrompt := strings.Index(msg, "具体任务")
	if iPreamble < 0 || iContext < 0 || iPrompt < 0 || !(iPreamble < iContext && iContext < iPrompt) {
		t.Errorf("preamble/context/prompt not stacked in order: %q", msg)
	}
}

// —— 隔离侧 ——

// 子会话完整往返不进父上下文：窗口隔离钉住（父历史只含 tool_use/result 对）。
func TestAgentToolWindowIsolation(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		agentToolUse("tu_a", "任务"),
		textRespUsage("child answer", 50, 5),
		textRespUsage("parent final", 100, 10),
	}}
	f := newAgentFactory(fm)
	f.NewChildRegistry = func() *tools.Registry { return tools.NewRegistry(&stubTool{}) }
	reg := tools.NewRegistry(&stubTool{}, NewAgentTool(f, 0))

	sess := NewSession(fm, reg, "sys")
	if _, err := sess.Prompt(context.Background(), "go", Options{}); err != nil {
		t.Fatal(err)
	}
	// 父历史 4 条：user, asst(tool_use), user(tool_result), asst(final)。
	// 子会话的多轮往返（如有）绝不能混进来。
	if len(sess.messages) != 4 {
		t.Fatalf("parent history = %d messages, want 4 (window isolation)", len(sess.messages))
	}
}

// compact 设置继承：子会话用 factory 的阈值/近窗。
func TestAgentToolCompactInherited(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{textRespUsage("ok", 0, 0)}}
	f := newAgentFactory(fm)
	f.CompactThresholdTokens = 100
	f.KeepRecentMessages = 3
	tool := NewAgentTool(f, 0)

	// 经 AgentFactory 落到子 Prompt Options（用 hook 捕获）。
	var gotOpts Options
	f.childOptsHook = func(o Options) { gotOpts = o }
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if gotOpts.CompactThresholdTokens != 100 || gotOpts.KeepRecentMessages != 3 {
		t.Errorf("child compact opts = %+v, want 100/3", gotOpts)
	}
}

// 排除清单生效：子注册表移除指定工具（状态隔离）。
func TestAgentToolExcludedTools(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{textRespUsage("ok", 0, 0)}}
	f := newAgentFactory(fm)
	f.NewChildRegistry = func() *tools.Registry {
		return tools.NewRegistry(&stubTool{}, exclProbe{})
	}
	f.ExcludedTools = []string{"TodoWrite", "Bash"}
	tool := NewAgentTool(f, 0)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`)); err != nil {
		t.Fatal(err)
	}
	for _, td := range fm.reqs[0].Tools {
		if td.Name == "TodoWrite" || td.Name == "Bash" {
			t.Errorf("excluded tool %q still visible to child", td.Name)
		}
	}
}

type exclProbe struct{}

func (exclProbe) Def() model.ToolDef {
	return model.ToolDef{Name: "TodoWrite", Description: "todo", InputSchema: json.RawMessage(`{}`)}
}
func (exclProbe) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }

// 子报告超长截断为摘要。
func TestAgentToolReportTruncation(t *testing.T) {
	big := strings.Repeat("R", 40<<10)
	fm := &fakeModel{script: []*model.Response{textRespUsage(big, 0, 0)}}
	tool := NewAgentTool(newAgentFactory(fm), 0)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= 40<<10 {
		t.Errorf("report not truncated (%d bytes)", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("truncation not noted: ...%s", out[len(out)-200:])
	}
}

// —— 并行 ——

// overlapModel 记录并发进入 Complete 的最大重叠数。
type overlapModel struct {
	mu  sync.Mutex
	cur int
	max int
}

func (m *overlapModel) Complete(_ context.Context, _ model.Request) (*model.Response, error) {
	m.mu.Lock()
	m.cur++
	if m.cur > m.max {
		m.max = m.cur
	}
	m.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	m.mu.Lock()
	m.cur--
	m.mu.Unlock()
	return textRespUsage("ok", 0, 0), nil
}

// 同一回合多个 Agent tool_use 并行执行：两个子会话在模型侧时间重叠
// （串行执行时 max 并发恒为 1）。
func TestAgentToolParallelInOneRound(t *testing.T) {
	om := &overlapModel{}
	f := &AgentFactory{
		Model:            om,
		System:           "child",
		NewChildRegistry: func() *tools.Registry { return tools.NewRegistry() },
		MaxDepth:         1,
	}
	tool := NewAgentTool(f, 0)
	reg := tools.NewRegistry(tool)

	round := &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
		{Type: model.BlockToolUse, ID: "a1", Name: "Agent", Input: json.RawMessage(`{"prompt":"t1"}`)},
		{Type: model.BlockToolUse, ID: "a2", Name: "Agent", Input: json.RawMessage(`{"prompt":"t2"}`)},
	}}, StopReason: model.StopToolUse}
	fm := &fakeModel{script: []*model.Response{round, textRespUsage("done", 0, 0)}}

	sess := NewSession(fm, reg, "sys")
	if _, err := sess.Prompt(context.Background(), "go", Options{}); err != nil {
		t.Fatal(err)
	}
	om.mu.Lock()
	defer om.mu.Unlock()
	if om.max < 2 {
		t.Errorf("max concurrent subagent rounds = %d, want >= 2 (parallel execution)", om.max)
	}
	// 结果仍按原序回填。
	tr := fm.reqs[1].Messages[2]
	if len(tr.Blocks) != 2 || tr.Blocks[0].ToolUseID != "a1" || tr.Blocks[1].ToolUseID != "a2" {
		t.Errorf("backfill order = %+v", tr.Blocks)
	}
}
