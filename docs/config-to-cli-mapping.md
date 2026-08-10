# 配置如何注入 Claude CLI：完整映射

## Claude CLI 的 7 个注入面

```
claude -p \
  --output-format stream-json \
  --input-format stream-json \
  --model sonnet \                          ← ① CLI 参数
  --permission-mode full_auto \             ← ① CLI 参数
  --system-prompt "你是..." \               ← ① CLI 参数
  --allowedTools "Read,Write,Bash,Agent" \  ← ① CLI 参数
  --add-dir /path/to/worktree \             ← ① CLI 参数
  --mcp-config /path/.mcp.json              ← ① CLI 参数

工作目录：workspace root                     ← ② 工作目录
环境变量：NIUNIU_*, 自定义变量               ← ③ 环境变量
workspace/.mcp.json                         ← ④ MCP 工具注入
workspace/CLAUDE.md                         ← ⑤ 项目上下文
worktree/CLAUDE.md                          ← ⑤ 项目上下文
stdin: {"type":"user","message":{...}}      ← ⑥ 消息输入
stdout: stream-json events                  ← ⑦ 输出解析
```

---

## Agent Profile → CLI 映射

Agent Profile 是我们定义的"Agent 能力描述"：

```go
type AgentProfile struct {
    Name           string   // "frontend-dev"
    Model          string   // "sonnet"
    PermissionMode string   // "full_auto"
    AllowedTools   []string // ["Read","Write","Bash","Agent"]
    SystemPrompt   string   // "你是一个前端开发专家..."
    MCPServers     []string // ["blackboard","inbox"]
    MaxTurns       int      // 50
    Skills         []string // ["frontend-design"]
}
```

### 映射方式 1：Agent 作为 workspace 的主 Agent

当 Agent Profile 应用到 workspace 的主 Agent（AgentProxy Session）时：

```
AgentProfile.Model          →  ① NIUNIU_MODEL 环境变量 → --model 参数
AgentProfile.PermissionMode →  ① NIUNIU_PERMISSION_MODE 环境变量 → --permission-mode 参数
AgentProfile.AllowedTools   →  ① NIUNIU_ALLOWED_TOOLS 环境变量 → --allowedTools 参数
AgentProfile.SystemPrompt   →  ⑤ 写入 workspace/CLAUDE.md（追加到末尾）
AgentProfile.MCPServers     →  ④ 写入 workspace/.mcp.json
AgentProfile.MaxTurns       →  ① NIUNIU_AGENT_ARGS 环境变量 → --max-turns 参数
AgentProfile.Skills         →  ⑤ 写入 workspace/CLAUDE.md（技能指令块）
```

**已有代码路径：**
```go
// server/internal/agentproxy/proxy.go:571-604
// 环境变量注入已实现：
envVars := workspaceEnvService.List(ctx, wsID)
for _, e := range envVars {
    if e.Key == "NIUNIU_MODEL" {
        args = append(args, "--model", e.Value)
    }
    if e.Key == "NIUNIU_PERMISSION_MODE" {
        args = append(args, "--permission-mode", e.Value)
    }
    // ...
}
```

**补充说明（2026-05-01）：**

- `default`/`acceptEdits` modes also inject `--permission-prompt-tool mcp__niuniu__niuniu_permission_prompt` (chat-inline prompts, see spec 2026-05-01-chat-permission-prompt-design.md). Falls back gracefully if niuniu MCP is unavailable (warning log, no flag).
- Default safe tools (`Read,Glob,Grep,LS,NotebookRead,TodoRead`) are auto-merged into `--allowedTools` (skipped only when permission mode is `bypassPermissions`).

**需要新增：** 将 AgentProfile 转换为环境变量写入 workspace env store。

```go
// 新增：server/internal/service/agent_profile.go
func (s *AgentProfileService) ApplyToWorkspace(ctx context.Context, wsID int64, profile AgentProfile) error {
    // 写入环境变量
    s.envStore.Set(ctx, wsID, "NIUNIU_MODEL", profile.Model)
    s.envStore.Set(ctx, wsID, "NIUNIU_PERMISSION_MODE", profile.PermissionMode)
    if len(profile.AllowedTools) > 0 {
        s.envStore.Set(ctx, wsID, "NIUNIU_ALLOWED_TOOLS", strings.Join(profile.AllowedTools, ","))
    }
    if profile.MaxTurns > 0 {
        s.envStore.Set(ctx, wsID, "NIUNIU_AGENT_ARGS", fmt.Sprintf("--max-turns %d", profile.MaxTurns))
    }

    // 更新 .mcp.json
    s.mcpService.RegenerateForWorkspace(ctx, wsID, profile.MCPServers)

    // 更新 CLAUDE.md（追加 profile 指令）
    s.appendProfileToCLAUDEMD(ctx, wsID, profile)

    return nil
}
```

### 映射方式 2：Agent 作为 subagent（被 Agent tool 派发）

当主 Agent 用 Agent tool 派发子任务时，Profile 映射到 Agent tool 的参数：

```
AgentProfile.Model          →  Agent tool 的 model 参数
AgentProfile.SubagentType   →  Agent tool 的 subagent_type 参数
AgentProfile.Isolation      →  Agent tool 的 isolation 参数

但 Agent tool 没有这些参数：
AgentProfile.PermissionMode →  只能通过 prompt 描述
AgentProfile.AllowedTools   →  由 subagent_type 决定
AgentProfile.MCPServers     →  subagent 继承父 Agent 的 MCP 配置
AgentProfile.SystemPrompt   →  写入 Agent tool 的 prompt 参数
```

**关键约束：Agent tool 的参数有限。** 我们不能像控制主 Agent 一样精细控制 subagent。
能控制的只有：`model`、`subagent_type`、`isolation`、`run_in_background`、`prompt`。

**解决方案：把控制指令写入 prompt。**

```go
func BuildSubagentPrompt(profile AgentProfile, task string) string {
    var sb strings.Builder

    // 核心任务
    sb.WriteString(task)
    sb.WriteString("\n\n")

    // Profile 注入（通过 prompt 文字）
    if profile.SystemPrompt != "" {
        sb.WriteString("## 角色定义\n")
        sb.WriteString(profile.SystemPrompt)
        sb.WriteString("\n\n")
    }

    if len(profile.Skills) > 0 {
        sb.WriteString("## 可用技能\n")
        for _, s := range profile.Skills {
            sb.WriteString("- " + s + "\n")
        }
        sb.WriteString("\n")
    }

    // MCP 工具说明（subagent 继承父 Agent 的 MCP）
    sb.WriteString("## 可用 MCP 工具\n")
    sb.WriteString("你可以使用 blackboard 和 inbox MCP 工具与团队通信。\n")

    return sb.String()
}
```

然后主 Agent 的 Provisioning Prompt 里告诉它如何使用 Profile：

```markdown
## 团队成员 Agent 配置

当你用 Agent tool 派发任务时，按以下规则选择参数：

### frontend-dev
- model: "sonnet"
- isolation: "worktree"（修改文件时隔离）
- prompt 中包含：前端开发专家角色定义 + 技能列表

### code-reviewer  
- model: "haiku"（审查不需要最强模型）
- subagent_type: "feature-dev:code-reviewer"
- run_in_background: true

### architect
- model: "opus"（架构决策需要深度推理）
- subagent_type: "Plan"
```

---

## Team Definition → CLI 映射

Team 定义的是"哪些 Agent 参与协作"：

```go
type TeamDefinition struct {
    Name        string
    Description string
    Members     []TeamMember
}

type TeamMember struct {
    AgentProfile AgentProfile  // 引用 Agent Profile
    Role         string        // "coordinator", "worker", "reviewer"
    Capabilities string        // "负责前端开发"
}
```

### Team 不直接配置 CLI

Team 是一个逻辑概念，它通过以下方式间接生效：

```
Team Definition
  │
  ├── 写入主 Agent 的 CLAUDE.md ← ⑤ 项目上下文
  │     "你有以下团队成员可以调度：
  │      - frontend-dev (sonnet, worktree): 前端开发
  │      - backend-dev (sonnet, worktree): 后端开发
  │      - reviewer (haiku): 代码审查"
  │
  ├── 写入主 Agent 的 .mcp.json ← ④ MCP 工具
  │     添加 inbox MCP server（团队通信工具）
  │
  └── 通过 stdin 消息注入 ← ⑥ 消息输入
        当用户启动团队任务时，发送包含 Team 配置的 prompt
```

**具体实现：**

```go
// server/internal/service/team_prompt.go

func BuildTeamSection(team TeamDefinition) string {
    var sb strings.Builder
    sb.WriteString("## 团队协作\n\n")
    sb.WriteString("你可以使用 Agent tool 派发子任务给团队成员。\n")
    sb.WriteString("使用 inbox MCP 工具与他们通信。\n\n")
    sb.WriteString("### 成员列表\n\n")

    for _, m := range team.Members {
        sb.WriteString(fmt.Sprintf("**%s** (%s)\n", m.AgentProfile.Name, m.Role))
        sb.WriteString(fmt.Sprintf("- 能力：%s\n", m.Capabilities))
        sb.WriteString(fmt.Sprintf("- 推荐模型：%s\n", m.AgentProfile.Model))
        if m.AgentProfile.Isolation != "" {
            sb.WriteString(fmt.Sprintf("- 隔离模式：%s\n", m.AgentProfile.Isolation))
        }
        sb.WriteString("\n")
    }

    sb.WriteString("### 派发规则\n\n")
    sb.WriteString("- 独立任务可并行：run_in_background: true\n")
    sb.WriteString("- 修改同一仓库：isolation: \"worktree\"\n")
    sb.WriteString("- 审查类任务用 haiku 模型节省成本\n")

    return sb.String()
}
```

### 注入时机

| 场景 | 注入方式 | 时机 |
|------|---------|------|
| workspace 绑定了 Team | 写入 CLAUDE.md | workspace 创建/更新时 |
| 用户说"用团队模式处理" | stdin 消息 | 用户发送时动态注入 |
| Harness 模板里定义了 Team | stdin 消息（Harness prompt 的一部分） | Harness run 启动时 |

---

## Harness Template → CLI 映射

Harness 定义的是"Pipeline 流程"：

```go
type HarnessTemplate struct {
    Name             string
    Phases           []Phase
    MaxRounds        int
    MaxReviewRetries int
    PromptTemplate   string
}

type Phase struct {
    Name        string
    Label       string
    Position    int
    AutoAdvance bool
    Agents      []PhaseAgent  // 执行和审查 agent
    Gates       []PhaseGate   // 质量门禁
}
```

### Harness 通过三个面注入

```
Harness Template
  │
  ├── ④ .mcp.json：注入 phase/gate MCP 工具
  │     {
  │       "mcpServers": {
  │         "niuniu-workspace": {
  │           "command": "niuniu-mcp-workspace",
  │           "env": {
  │             "NIUNIU_WORKSPACE_ID": "123",
  │             "NIUNIU_HARNESS_RUN_ID": "456"  ← 激活 phase/gate 工具
  │           }
  │         }
  │       }
  │     }
  │
  ├── ⑤ CLAUDE.md：写入 Harness 工程规范
  │     "## Engineering Standards (Harness)
  │      - [MUST] conventional-commits (error)
  │      - [SHOULD] test-coverage >= 80% (warning)"
  │
  └── ⑥ stdin：发送编排 prompt
        包含完整的 Phase 定义 + 团队成员 + 工作指令
```

### Harness Prompt（通过 stdin 注入的核心内容）

```go
func BuildHarnessPrompt(harness HarnessTemplate, team TeamDefinition, goal string) string {
    return fmt.Sprintf(`
## 任务目标
%s

## Pipeline 流程

你需要按以下阶段推进工作。每个阶段完成后，使用 MCP 工具管理流程。

%s

## 工具使用

### 阶段管理
- phase_current() — 获取当前阶段信息
- phase_complete(phase_name, summary) — 完成当前阶段
- phase_advance() — 推进到下一阶段
- harness_report(status_json) — 向用户报告进度

### 质量检查
- gate_run() — 执行当前阶段的质量门禁检查
- gate_results() — 查看检查结果

### 工作流程
1. 调用 phase_current() 了解当前阶段要求
2. 分解任务，使用 Agent tool 派发给团队成员
3. 收集结果（通过 blackboard 和 inbox）
4. 调用 gate_run() 检查质量
5. 如果检查通过，调用 phase_complete() + phase_advance()
6. 如果检查失败，修复后重试（最多 %d 轮）
7. 如果超过重试次数，向用户报告请求帮助

%s
`,
    goal,
    buildPhaseList(harness.Phases),
    harness.MaxRounds,
    BuildTeamSection(team),
    )
}

func buildPhaseList(phases []Phase) string {
    var sb strings.Builder
    for _, p := range phases {
        sb.WriteString(fmt.Sprintf("### Phase %d: %s (%s)\n", p.Position, p.Label, p.Name))
        if p.AutoAdvance {
            sb.WriteString("自动推进：是\n")
        }
        if len(p.Agents) > 0 {
            sb.WriteString("参与 Agent：\n")
            for _, a := range p.Agents {
                sb.WriteString(fmt.Sprintf("  - %s (%s)\n", a.AgentName, a.Role))
            }
        }
        if len(p.Gates) > 0 {
            sb.WriteString("质量门禁：\n")
            for _, g := range p.Gates {
                sb.WriteString(fmt.Sprintf("  - %s [%s]\n", g.SpecName, g.Severity))
            }
        }
        sb.WriteString("\n")
    }
    return sb.String()
}
```

---

## 统一 MCP Server：niuniu-mcp-workspace

### 核心设计

将现有的 `niuniu-mcp`（项目工具）、`niuniu-mcp-blackboard`（黑板）合并为一个 MCP server，
并根据运行时状态动态启用/禁用工具：

```go
// cmd/niuniu-mcp-workspace/main.go

func main() {
    wsID := os.Getenv("NIUNIU_WORKSPACE_ID")
    dbPath := os.Getenv("NIUNIU_DB_PATH")
    runID := os.Getenv("NIUNIU_HARNESS_RUN_ID")  // 可选

    server := mcp.NewServer("niuniu-workspace")

    // ── 始终可用的工具 ──

    // Blackboard（共享知识库）
    server.AddTool("blackboard_read", ...)
    server.AddTool("blackboard_write", ...)
    server.AddTool("blackboard_list", ...)

    // Inbox（团队通信）
    server.AddTool("inbox_send", ...)
    server.AddTool("inbox_read", ...)
    server.AddTool("inbox_list_members", ...)

    // Team 状态上报
    server.AddTool("team_register_member", ...)
    server.AddTool("team_update_status", ...)
    server.AddTool("team_report_progress", ...)

    // ── Harness Run 激活时才可用 ──

    if runID != "" {
        server.AddTool("phase_current", ...)
        server.AddTool("phase_list", ...)
        server.AddTool("phase_complete", ...)
        server.AddTool("phase_advance", ...)
        server.AddTool("gate_run", ...)
        server.AddTool("gate_results", ...)
        server.AddTool("harness_report", ...)
    }

    server.Run()
}
```

### .mcp.json 生成

```go
// server/internal/service/mcp.go

func (s *MCPService) GenerateForWorkspace(ctx context.Context, ws Workspace) error {
    config := MCPConfig{
        MCPServers: map[string]MCPServerEntry{},
    }

    // 统一 workspace MCP server
    entry := MCPServerEntry{
        Command: s.findBinary("niuniu-mcp-workspace"),
        Env: map[string]string{
            "NIUNIU_WORKSPACE_ID": fmt.Sprintf("%d", ws.ID),
            "NIUNIU_DB_PATH":      s.cfg.DBPath,
            "NIUNIU_INBOX_DIR":    filepath.Join(ws.Path, ".team", "inboxes"),
        },
    }

    // 如果有活跃的 Harness Run，注入 run ID
    activeRun, err := s.q.GetActiveHarnessRun(ctx, ws.ID)
    if err == nil && activeRun.ID > 0 {
        entry.Env["NIUNIU_HARNESS_RUN_ID"] = fmt.Sprintf("%d", activeRun.ID)
    }

    config.MCPServers["niuniu-workspace"] = entry

    return writeJSON(filepath.Join(ws.Path, ".mcp.json"), config)
}
```

### Harness 启动时更新 .mcp.json

```go
func (pr *PipelineRunner) StartRun(ctx context.Context, wsID int64, harnessID int64, goal string) error {
    // 1. 创建 run 记录
    run, _ := pr.q.CreateHarnessRun(ctx, ...)

    // 2. 重新生成 .mcp.json（这次带 NIUNIU_HARNESS_RUN_ID）
    pr.mcpService.RegenerateForWorkspace(ctx, ws)

    // 3. 构建 Harness prompt
    harness, _ := pr.q.GetHarnessDetail(ctx, harnessID)
    team := pr.resolveTeam(ctx, harness)
    prompt := BuildHarnessPrompt(harness, team, goal)

    // 4. 发送给 Agent（通过现有的 AgentProxy Session）
    session := pr.proxyManager.GetSession(wsID)
    session.Send(ctx, ws.Path, prompt, "")

    return nil
}
```

**关键：不需要重启 Agent 进程。** 
MCP server 是按需启动的（每次 Agent 调用 MCP 工具时 spawn 新进程）。
更新 .mcp.json 后，下次 Agent 调用 MCP 工具时自然会带上新的环境变量。

但如果 Agent 已经缓存了 MCP 工具列表，需要通知它刷新。
可以通过 stdin 消息提醒：

```go
session.Send(ctx, ws.Path, 
    "MCP 工具已更新，你现在可以使用 phase_current、phase_complete 等 Harness 工具了。请用 phase_current() 查看当前阶段。",
    "")
```

---

## 完整注入流程图

```
用户创建 Workspace
  │
  ├── 生成 workspace/.mcp.json
  │   └── niuniu-workspace MCP (blackboard + inbox, 无 phase/gate)
  │
  ├── 生成 workspace/CLAUDE.md
  │   └── 仓库结构 + worktree 引用
  │
  └── AgentProxy Session 启动
      └── claude -p --output-format stream-json
            --add-dir .worktrees/repo1
            工作目录: workspace/

═══════════════════════════════════════════════
用户在 Chat 里正常对话（Layer 0）

  Agent 有：内置工具 + niuniu-workspace MCP
  Agent 可以自主决定是否用 Agent tool
  不需要任何额外配置

═══════════════════════════════════════════════
用户设置了 Team（Layer 1）

  │
  ├── 更新 workspace/CLAUDE.md
  │   └── 追加团队成员描述 + 派发规则
  │
  └── 生成 .team/inboxes/ 目录
      └── inbox MCP 工具现在有实际的 inbox 目录可操作

═══════════════════════════════════════════════
用户启动 Harness Run（Layer 2）

  │
  ├── 更新 workspace/.mcp.json
  │   └── 添加 NIUNIU_HARNESS_RUN_ID 环境变量
  │       └── phase/gate MCP 工具被激活
  │
  ├── 更新 workspace/CLAUDE.md
  │   └── 追加工程规范（Gate 定义）
  │
  └── 发送 Harness Prompt（stdin 消息）
      └── 完整的阶段定义 + 团队 + 目标 + 工作指令

═══════════════════════════════════════════════
Harness Run 完成/取消

  │
  ├── 更新 workspace/.mcp.json
  │   └── 移除 NIUNIU_HARNESS_RUN_ID
  │       └── phase/gate MCP 工具不再可用
  │
  └── Agent 继续运行，回到 Layer 0/1 状态
      └── 用户可以继续正常对话
```

---

## 每层的配置注入总结

| 配置项 | 注入面 | 时机 | 是否需要重启 Agent |
|--------|--------|------|-------------------|
| **Agent Profile** | | |
| └ Model | ① CLI `--model` via env var | workspace 创建时 | 是（进程参数） |
| └ PermissionMode | ① CLI `--permission-mode` via env var | workspace 创建时 | 是 |
| └ AllowedTools | ① CLI `--allowedTools` via env var | workspace 创建时 | 是 |
| └ SystemPrompt | ⑤ CLAUDE.md 追加 | workspace 创建/更新时 | 否（Agent 会重新读） |
| └ MCPServers | ④ .mcp.json | workspace 创建/更新时 | 否（MCP 按需加载） |
| └ Skills | ⑤ CLAUDE.md 追加 | workspace 创建/更新时 | 否 |
| **Team Definition** | | |
| └ 成员列表 | ⑤ CLAUDE.md 追加 | Team 绑定/更新时 | 否 |
| └ 派发规则 | ⑤ CLAUDE.md 追加 | Team 绑定/更新时 | 否 |
| └ Inbox 配置 | ④ .mcp.json env | Team 绑定时 | 否 |
| **Harness Template** | | |
| └ Phase 定义 | ⑥ stdin prompt | Harness run 启动时 | 否 |
| └ Gate 检查定义 | ⑤ CLAUDE.md + ⑥ stdin | 创建时/启动时 | 否 |
| └ Phase/Gate MCP 工具 | ④ .mcp.json env | Harness run 启动时 | 否 |
| └ 编排指令 | ⑥ stdin prompt | Harness run 启动时 | 否 |
| └ 团队+目标 | ⑥ stdin prompt | Harness run 启动时 | 否 |

**关键发现：除了 Agent Profile 的 model/permission 需要重启进程，其他所有配置都可以热注入。**

---

## 需要重启 vs 热注入的处理

### 需要重启的配置（进程级）

Model 和 PermissionMode 是 CLI 启动参数，改变需要重启：

```go
func (s *AgentProfileService) ApplyToWorkspace(ctx context.Context, wsID int64, profile AgentProfile) error {
    needsRestart := false

    currentModel := s.envStore.Get(ctx, wsID, "NIUNIU_MODEL")
    if profile.Model != "" && profile.Model != currentModel {
        s.envStore.Set(ctx, wsID, "NIUNIU_MODEL", profile.Model)
        needsRestart = true
    }

    currentPerm := s.envStore.Get(ctx, wsID, "NIUNIU_PERMISSION_MODE")
    if profile.PermissionMode != "" && profile.PermissionMode != currentPerm {
        s.envStore.Set(ctx, wsID, "NIUNIU_PERMISSION_MODE", profile.PermissionMode)
        needsRestart = true
    }

    // 热注入的部分
    s.mcpService.RegenerateForWorkspace(ctx, wsID, profile.MCPServers)
    s.appendProfileToCLAUDEMD(ctx, wsID, profile)

    if needsRestart {
        s.proxyManager.RestartSession(ctx, wsID)
    }

    return nil
}
```

### 热注入的配置（运行时）

MCP、CLAUDE.md、stdin 都可以运行时改变，不需要重启：

```go
func (pr *PipelineRunner) StartRun(ctx context.Context, ...) error {
    // 这些都不需要重启 Agent
    pr.mcpService.RegenerateForWorkspace(ctx, ws)  // 更新 .mcp.json
    pr.updateCLAUDEMD(ctx, ws, harness)             // 更新 CLAUDE.md
    session.Send(ctx, prompt)                        // 发送 prompt
}
```
