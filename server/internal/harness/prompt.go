package harness

import (
	"fmt"
	"strings"
)

// PhasePromptInfo holds phase data for prompt building.
type PhasePromptInfo struct {
	Name        string
	Label       string
	Position    int
	AutoAdvance bool
	Agents      []PhaseAgentInfo
	Gates       []PhaseGateInfo
}

// PhaseAgentInfo describes an agent assigned to a phase.
type PhaseAgentInfo struct {
	Name string
	Role string // "execute" or "review"
}

// PhaseGateInfo describes a gate check for a phase.
type PhaseGateInfo struct {
	SpecName string
	Severity string // "error", "warning", "info"
}

// HarnessPromptConfig holds all data needed to build a harness prompt.
type HarnessPromptConfig struct {
	UserGoal         string
	Phases           []PhasePromptInfo
	MaxRounds        int
	MaxReviewRetries int
}

// BuildHarnessPrompt constructs the prompt injected into the workspace Agent
// when a harness run starts. This prompt instructs the Agent to use MCP tools
// (phase_current, phase_complete, phase_advance, gate_run) to drive the
// pipeline, and Agent tool to dispatch subagents for parallel work.
func BuildHarnessPrompt(cfg HarnessPromptConfig) string {
	var sb strings.Builder

	sb.WriteString("## Harness Pipeline 任务\n\n")
	sb.WriteString(fmt.Sprintf("**目标：** %s\n\n", cfg.UserGoal))

	// Phase definitions
	sb.WriteString("## Pipeline 阶段\n\n")
	sb.WriteString("按以下顺序推进每个阶段。使用 MCP 工具管理流程。\n\n")
	for _, p := range cfg.Phases {
		sb.WriteString(fmt.Sprintf("### Phase %d: %s (`%s`)\n", p.Position, p.Label, p.Name))
		if p.AutoAdvance {
			sb.WriteString("- 自动推进：是（gate 通过后自动进入下一阶段）\n")
		}
		if len(p.Agents) > 0 {
			sb.WriteString("- 参与 Agent：")
			names := make([]string, len(p.Agents))
			for i, a := range p.Agents {
				names[i] = fmt.Sprintf("%s(%s)", a.Name, a.Role)
			}
			sb.WriteString(strings.Join(names, ", "))
			sb.WriteString("\n")
		}
		if len(p.Gates) > 0 {
			sb.WriteString("- 质量门禁：")
			specs := make([]string, len(p.Gates))
			for i, g := range p.Gates {
				specs[i] = fmt.Sprintf("%s[%s]", g.SpecName, g.Severity)
			}
			sb.WriteString(strings.Join(specs, ", "))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// MCP tools usage
	sb.WriteString("## 可用 MCP 工具\n\n")
	sb.WriteString("### 阶段管理\n")
	sb.WriteString("- `phase_current()` — 获取当前阶段信息\n")
	sb.WriteString("- `phase_list()` — 列出所有阶段和状态\n")
	sb.WriteString("- `phase_complete(phase_name, summary)` — 标记当前阶段完成\n")
	sb.WriteString("- `phase_advance(feedback?)` — 推进到下一阶段\n\n")
	sb.WriteString("### 质量检查\n")
	sb.WriteString("- `gate_run()` — 执行当前阶段的质量门禁检查\n")
	sb.WriteString("- `gate_results()` — 查看最近检查结果\n\n")
	sb.WriteString("### 共享数据\n")
	sb.WriteString("- `blackboard_write(key, type, content)` — 写入共享黑板\n")
	sb.WriteString("- `blackboard_read(key)` — 从黑板读取\n")
	sb.WriteString("- `blackboard_list(type_filter?)` — 列出黑板条目\n\n")
	sb.WriteString("### 团队通信\n")
	sb.WriteString("- `inbox_send(to, text)` — 发消息给团队成员\n")
	sb.WriteString("- `inbox_read()` — 读取自己的未读消息\n\n")

	// Workflow
	sb.WriteString("## 工作流程\n\n")
	sb.WriteString("1. 调用 `phase_current()` 了解当前阶段要求\n")
	sb.WriteString("2. 分解任务，使用 **Agent tool** 派发给团队成员（可并行）\n")
	sb.WriteString("3. 通过 blackboard 和 inbox 收集结果\n")
	sb.WriteString("4. 调用 `gate_run()` 执行质量检查\n")
	sb.WriteString("5. 如果检查通过，调用 `phase_complete()` + `phase_advance()`\n")
	sb.WriteString("6. 如果检查失败，修复后重试\n")
	sb.WriteString("7. 重复直到所有阶段完成\n\n")

	// Constraints
	sb.WriteString("## 约束\n\n")
	sb.WriteString(fmt.Sprintf("- 每阶段最多 %d 轮迭代\n", cfg.MaxRounds))
	sb.WriteString(fmt.Sprintf("- Review 最多重试 %d 次\n", cfg.MaxReviewRetries))
	sb.WriteString("- 超限时请向用户报告并请求指导\n\n")

	// Agent dispatch strategy
	sb.WriteString("## Agent 派发策略\n\n")
	sb.WriteString("使用 Agent tool 时的参数选择：\n\n")
	sb.WriteString("| 场景 | model | isolation | run_in_background |\n")
	sb.WriteString("|------|-------|-----------|-------------------|\n")
	sb.WriteString("| 代码探索 | haiku | - | false |\n")
	sb.WriteString("| 架构设计 | opus | - | false |\n")
	sb.WriteString("| 功能开发 | sonnet | worktree | true |\n")
	sb.WriteString("| 代码审查 | haiku | - | true |\n")
	sb.WriteString("| 安全审查 | sonnet | - | true |\n\n")

	return sb.String()
}
