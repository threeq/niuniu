package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// DefaultSubagentTimeout bounds one subagent run when AgentFactory.Timeout
// is unset.
const DefaultSubagentTimeout = 10 * time.Minute

// AgentFactory configures how the Agent tool spawns child sessions. The
// child reuses the parent's model but gets its own registry (via
// NewChildRegistry — build it WITHOUT the Agent tool so recursion ends at
// the configured depth) and its own conversation.
type AgentFactory struct {
	Model model.Model
	// System is the child's system prompt (typically the parent's plus a
	// short subagent preamble).
	System string
	// NewChildRegistry builds the child's tool registry on each spawn.
	// Nil → an empty registry (child can only answer from its own knowledge).
	NewChildRegistry func() *tools.Registry
	// Timeout bounds one child run. 0 → DefaultSubagentTimeout.
	Timeout time.Duration
	// Perms gates the child's tool execution; nil approves all (same
	// semantics as Options.Perms). Wiring passes the parent's policy so a
	// subagent cannot bypass the user's write-tool choice.
	Perms perm.Checker
	// MaxDepth is the nesting limit: an Agent tool at depth >= MaxDepth
	// refuses to spawn. 1 (the sane default) = children cannot delegate.
	MaxDepth int
}

// AgentTool is the subagent tool: it runs a synchronous in-process child
// Session on an independent conversation and returns the child's final
// answer (plus its usage accounting) as the tool result.
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
			"(except Agent itself). Use for self-contained chunks of work — a targeted investigation, a summary, " +
			"a mechanical refactor — and take back the final answer. The sub-agent cannot see this conversation; " +
			"write a complete, standalone prompt. Runs synchronously and returns the sub-agent's final report.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"prompt":{"type":"string","description":"Complete, self-contained task for the sub-agent"}},` +
			`"required":["prompt"]}`),
	}
}

// Execute implements tools.Tool: spawn the child, wait, backfill text+usage.
func (a *AgentTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if a.depth >= a.f.MaxDepth {
		return "", fmt.Errorf("subagent depth limit (%d) reached; do the work directly instead", a.f.MaxDepth)
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}

	timeout := a.f.Timeout
	if timeout <= 0 {
		timeout = DefaultSubagentTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reg := tools.NewRegistry()
	if a.f.NewChildRegistry != nil {
		reg = a.f.NewChildRegistry()
	}
	child := NewSession(a.f.Model, reg, a.f.System)
	res, err := child.Prompt(ctx, in.Prompt, Options{Perms: a.f.Perms})
	if err != nil {
		return "", fmt.Errorf("subagent: %w", err)
	}
	return res.Text + fmt.Sprintf("\n\n[subagent usage: rounds=%d input=%d output=%d cache-read=%d]",
		res.Rounds, res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens), nil
}
