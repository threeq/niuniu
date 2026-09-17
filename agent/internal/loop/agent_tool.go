package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// DefaultSubagentTimeout bounds one synchronous subagent run when
// AgentFactory.Timeout is unset.
const DefaultSubagentTimeout = 10 * time.Minute

// Subagent isolation caps. ContextPreamble/Context are capped so a chatty
// parent cannot balloon the child's window; the final report is capped so a
// verbose child cannot balloon the parent's.
const (
	DefaultContextPreambleMaxBytes = 8 << 10
	DefaultContextMaxBytes         = 4 << 10
	DefaultReportMaxBytes          = 16 << 10
)

// AgentFactory configures how the Agent tool spawns child sessions.
//
// Sharing: the child runs in the SAME process, so it shares the parent's
// cwd (including any session chdir) and the wiring-provided System — which
// must be built from prompt.BuildSession(cwd) so project context / host
// capabilities / memory recall reach the child too. ContextPreamble (plus
// the tool's optional context argument) carries task background into the
// child prompt.
//
// Isolation: the child has its own conversation — only the final report
// ever enters the parent context. Compact settings are inherited so long
// child tasks stay bounded; oversized reports are truncated. ExcludedTools
// strips state-changing tools (TodoWrite, external-state MCP tools) from
// the child; memory reflection is never run for children (only the top
// level reflects), and wiring should halve the child's recall.
type AgentFactory struct {
	Model model.Model
	// System is the child's system prompt (typically prompt.BuildSession
	// plus a subagent preamble, recall halved).
	System string
	// NewChildRegistry builds the child's tool registry on each spawn.
	// Nil → an empty registry (child can only answer from its own knowledge).
	NewChildRegistry func() *tools.Registry
	// ContextPreamble is prepended to every child prompt (task background /
	// recent key findings from the parent). Capped at ~8KB.
	ContextPreamble string
	// Timeout bounds one synchronous child run. 0 → DefaultSubagentTimeout.
	Timeout time.Duration
	// Perms gates the child's tool execution; nil approves all (same
	// semantics as Options.Perms). Wiring passes the parent's policy so a
	// subagent cannot bypass the user's write-tool choice.
	Perms perm.Checker
	// MaxDepth is the nesting limit: an Agent tool at depth >= MaxDepth
	// refuses to spawn. 1 (the sane default) = children cannot delegate.
	MaxDepth int
	// CompactThresholdTokens / KeepRecentMessages are inherited by the
	// child's Prompt options so long child tasks compact on their own.
	CompactThresholdTokens int
	KeepRecentMessages     int
	// ExcludedTools are removed from every child registry (state isolation:
	// the child must not mutate shared task lists or external state).
	ExcludedTools []string
	// ReportMaxBytes caps the report fed back to the parent. 0 → 16KB.
	ReportMaxBytes int
	// Types is the subagent-type registry (nil → BuiltinAgentTypes).
	// Wiring appends LoadAgentTypes(<cwd>/.niuniu-agent/agents) for
	// declarative user types.
	Types []AgentType
	// ModelFor resolves a typed subagent's model tier (nil → shared Model).
	ModelFor func(tier string) model.Model

	// childOptsHook observes the child's Prompt options (diagnostics/tests).
	childOptsHook func(Options)

	mu    sync.Mutex
	tasks map[string]*subagentTask
	seq   int
}

// subagentTask is one background subagent run.
type subagentTask struct {
	id     string
	prompt string
	done   chan struct{}
	text   string
	err    error
	result TurnResult
}

// AgentTool is the subagent tool: it runs a child Session on an independent
// conversation and returns the child's final answer (plus usage) as the
// tool result. Synchronous by default; {"background": true} starts the run
// and returns a task id for AgentResult polling. Multiple synchronous Agent
// calls in ONE assistant round execute in parallel (the loop parallelizes
// Agent tool_uses specifically — child sessions are independent, so this
// is safe, and a fan-out round should not serialize).
type AgentTool struct {
	f     *AgentFactory
	depth int
}

// NewAgentTool builds the Agent tool at the given nesting depth (0 = top
// level). The same factory can serve several depths by re-wrapping.
func NewAgentTool(f *AgentFactory, depth int) *AgentTool {
	return &AgentTool{f: f, depth: depth}
}

// Def implements tools.Tool.
func (a *AgentTool) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Agent",
		Description: "Run a focused sub-task in an isolated sub-agent with its own conversation and the same tools " +
			"(except Agent itself and state-changing tools). Use for self-contained chunks of work — a targeted " +
			"investigation, a summary, a mechanical refactor — and take back the final answer. The sub-agent cannot " +
			"see this conversation; write a complete, standalone prompt. Synchronous by default and returns the " +
			"sub-agent's final report; set background=true to run it in the background and poll AgentResult.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"prompt":{"type":"string","description":"Complete, self-contained task for the sub-agent"},` +
			`"context":{"type":"string","description":"Optional task background the sub-agent needs (kept short)"},` +
			`"background":{"type":"boolean","description":"Run in the background; poll AgentResult with the returned task id"},` +
			`"subagent_type":{"type":"string","description":"Subagent preset: explore / plan / worker / reviewer (or a custom type from .niuniu-agent/agents)"}},` +
			`"required":["prompt"]}`),
	}
}

// agentResultTool polls a background subagent task.
type agentResultTool struct{ f *AgentFactory }

// Def implements tools.Tool.
func (agentResultTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "AgentResult",
		Description: "Wait for and fetch the result of a background sub-agent started with Agent(background=true). Returns the sub-agent's final report plus its usage accounting.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"task_id":{"type":"string","description":"Task id returned by Agent"}},` +
			`"required":["task_id"]}`),
	}
}

// Execute implements tools.Tool.
func (t agentResultTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.TaskID) == "" {
		return "", fmt.Errorf("task_id is required")
	}
	t.f.mu.Lock()
	task := t.f.tasks[in.TaskID]
	t.f.mu.Unlock()
	if task == nil {
		return "", fmt.Errorf("unknown task %q", in.TaskID)
	}
	select {
	case <-task.done:
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for %s: %w", in.TaskID, ctx.Err())
	}
	if task.err != nil {
		return "", task.err
	}
	return task.text, nil
}

// Execute implements tools.Tool: spawn the child, wait (or start in the
// background), backfill text+usage.
func (a *AgentTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if a.depth >= a.f.MaxDepth {
		return "", fmt.Errorf("subagent depth limit (%d) reached; do the work directly instead", a.f.MaxDepth)
	}
	var in struct {
		Prompt       string `json:"prompt"`
		Context      string `json:"context"`
		Background   bool   `json:"background"`
		SubagentType string `json:"subagent_type"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	prompt := composeChildPrompt(a.f.ContextPreamble, in.Context, in.Prompt)

	if in.Background {
		return a.startBackground(prompt, in.SubagentType), nil
	}
	task := a.f.spawn(ctx, prompt, in.SubagentType)
	select {
	case <-task.done:
	case <-ctx.Done():
		return "", fmt.Errorf("subagent: %w", ctx.Err())
	}
	if task.err != nil {
		return "", task.err
	}
	return task.text, nil
}

// spawn runs one child session to completion, filling the task fields.
func (a *AgentFactory) spawn(ctx context.Context, prompt, typeName string) *subagentTask {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultSubagentTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	task := &subagentTask{done: make(chan struct{})}

	go func() {
		defer cancel()
		defer close(task.done)
		reg := tools.NewRegistry()
		if a.NewChildRegistry != nil {
			reg = a.NewChildRegistry()
		}
		for _, name := range a.ExcludedTools {
			reg.Remove(name)
		}
		system := a.System
		m := a.Model
		if typeName != "" {
			sys, typed, err := a.applyType(reg, a.System, typeName)
			if err != nil {
				task.err = err
				return
			}
			system, m = sys, typed
		}
		child := NewSession(m, reg, system)
		opts := Options{
			Perms:                  a.Perms,
			CompactThresholdTokens: a.CompactThresholdTokens,
			KeepRecentMessages:     a.KeepRecentMessages,
		}
		if a.childOptsHook != nil {
			a.childOptsHook(opts)
		}
		res, err := child.Prompt(ctx, prompt, opts)
		if err != nil {
			task.err = fmt.Errorf("subagent: %w", err)
			return
		}
		task.result = res
		task.text = fmt.Sprintf("%s\n\n[subagent usage: rounds=%d input=%d output=%d cache-read=%d]",
			truncateReport(res.Text, a.reportMaxBytes()),
			res.Rounds, res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens)
	}()
	return task
}

// startBackground launches a child run and returns the polling hint.
func (a *AgentTool) startBackground(prompt, typeName string) string {
	a.f.mu.Lock()
	if a.f.tasks == nil {
		a.f.tasks = map[string]*subagentTask{}
	}
	a.f.seq++
	id := fmt.Sprintf("sub-%d", a.f.seq)
	a.f.tasks[id] = a.f.spawn(context.Background(), prompt, typeName)
	a.f.mu.Unlock()
	return fmt.Sprintf("background subagent started (task_id: %s) — fetch with the AgentResult tool", id)
}

// composeChildPrompt stacks preamble → context → prompt with caps.
func composeChildPrompt(preamble, context, prompt string) string {
	var b strings.Builder
	if p := strings.TrimSpace(preamble); p != "" {
		b.WriteString(capBytes(p, DefaultContextPreambleMaxBytes))
		b.WriteString("\n\n")
	}
	if c := strings.TrimSpace(context); c != "" {
		b.WriteString(capBytes(c, DefaultContextMaxBytes))
		b.WriteString("\n\n")
	}
	b.WriteString(prompt)
	return b.String()
}

func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(context capped)"
}

func (a *AgentFactory) reportMaxBytes() int {
	if a.ReportMaxBytes > 0 {
		return a.ReportMaxBytes
	}
	return DefaultReportMaxBytes
}

func truncateReport(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[report truncated at the byte cap — re-run the sub-agent with a narrower task for details]"
}

// NewAgentResultTool builds the AgentResult polling tool over the same
// factory as the Agent tool. Register it alongside Agent when background
// subagents are enabled.
func NewAgentResultTool(f *AgentFactory) tools.Tool { return agentResultTool{f: f} }
