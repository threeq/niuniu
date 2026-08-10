# 统一 Agent 设计：能力注入而非模式切换

## 一、核心原则

**永远只有一个 Agent，没有"模式切换"。**

```
错误的思路：
  普通模式 ──(启动Team)──→ Lead 模式 ──(停止Team)──→ 普通模式
  两个不同进程，两套通信方式，需要切换

正确的思路：
  Agent 始终在那里
  ├── 基础能力：Read, Write, Bash, Grep, ... （始终可用）
  ├── 协作能力：Agent tool, Blackboard, Inbox   （始终可用）
  └── 编排能力：Phase/Gate MCP tools            （Harness 启动时注入）

  用户说"帮我改个 bug" → Agent 自己改
  用户说"帮我实现整个功能" → Agent 自主决定是否用 Agent tool 拆分
  用户启动 Harness → 注入 Phase/Gate 工具 + 团队配置，Agent 按 pipeline 流程走
```

---

## 二、三层能力架构

### Layer 0：单 Agent 工作（当前已有，不变）

```
用户 ↔ Agent（AgentProxy Session）

Agent 有：
  - Claude Code 内置工具（Read, Write, Edit, Bash, Grep, Glob, Agent tool, ...）
  - workspace 的 .mcp.json 里配置的 MCP servers
  - CLAUDE.md 里的项目指令

用户说什么，Agent 就做什么。
这是 90% 日常使用场景。
```

**不需要任何改动。**

### Layer 1：协作能力（始终可用的 MCP 工具）

```
用户 ↔ Agent
         │
         ├── Agent tool（Claude Code 内置，始终可用）
         │     └── 可以派发 subagent 做子任务
         │
         └── MCP: niuniu-workspace
               ├── blackboard_read / blackboard_write / blackboard_list
               ├── inbox_send / inbox_read / inbox_list_members
               └── team_status / team_register_member
```

**关键设计：Blackboard 和 Inbox MCP 工具始终在 workspace 的 .mcp.json 里。**

Agent 不需要"启动 Team"就能用 Agent tool 派发子任务。
Blackboard 和 Inbox 是可选工具——Agent 自己判断是否需要用。

```
场景 1：简单任务
  用户："帮我修复这个 bug"
  Agent：直接修，不用 Agent tool，不碰 Blackboard/Inbox
  跟现在完全一样

场景 2：中等任务
  用户："重构 auth 模块并更新所有测试"
  Agent：自主决定用 Agent tool 并行处理
    Agent(prompt: "重构 auth 模块", isolation: "worktree")
    Agent(prompt: "更新测试用例", run_in_background: true)
  不需要 Harness，不需要 Team Panel
  Chat Panel 里展示 Agent tool 调用过程

场景 3：复杂任务
  用户："帮我实现完整的支付系统，包括设计、开发、测试、审查"
  Agent：自主决定使用多轮协作
    先用 Explore agent 分析
    再用 Plan agent 设计
    再并行派发多个 general-purpose agent
    用 blackboard 共享中间结果
    用 code-reviewer agent 审查
  同样不需要 Harness
```

**改动点：**
- `niuniu-mcp-blackboard` 合并到一个统一的 `niuniu-mcp-workspace` MCP server
- 新增 Inbox 能力到同一个 MCP server
- workspace 创建时自动在 `.mcp.json` 里配置这个 MCP server

### Layer 2：流程编排（Harness 启动时注入）

```
用户 ↔ Agent
         │
         ├── Agent tool（始终可用）
         ├── MCP: niuniu-workspace（始终可用）
         │     ├── blackboard / inbox（同上）
         │     ├── phase_current / phase_list       ← Harness 启动后可用
         │     ├── phase_complete / phase_advance    ← Harness 启动后可用
         │     ├── gate_run / gate_results           ← Harness 启动后可用
         │     └── harness_status / harness_report   ← Harness 启动后可用
         │
         └── System Prompt 注入：
               ├── Harness 模板定义（phases, agents, gates）
               ├── 团队成员描述（capabilities, model preferences）
               └── 编排规则（何时 gate check, 何时推进, 何时求助）
```

**Harness 不改变 Agent，只改变 Agent 的上下文：**

```go
// Harness 启动时：
func (pr *PipelineRunner) StartRun(ctx, wsID, harnessID, goal) {
    // 1. 创建 harness_run 记录
    run := pr.q.CreateHarnessRun(ctx, ...)

    // 2. 注入 Phase/Gate MCP 工具到 workspace MCP server
    //    （这些工具只有 run active 时才返回有效数据）
    pr.activateHarnessTools(wsID, run.ID)

    // 3. 构建上下文注入 prompt
    prompt := buildHarnessPrompt(harness, goal)

    // 4. 发送给当前 workspace session（就是 Chat 的那个 Agent）
    session := pr.getSession(wsID)
    session.Send(ctx, prompt)
    // Agent 收到后，按照 prompt 里的流程指导，开始用 Agent tool 和 MCP 工作
}

// Harness 停止/完成时：
func (pr *PipelineRunner) StopRun(ctx, wsID, runID) {
    // 1. 停用 Phase/Gate MCP 工具
    pr.deactivateHarnessTools(wsID)

    // 2. 更新 run 记录
    pr.q.UpdateHarnessRunStatus(ctx, ...)

    // 3. Agent 继续存在，回到普通对话模式
    //    用户可以继续跟 Agent 聊天
}
```

**Agent 感知到的变化：**
- MCP 工具列表多了几个 phase/gate 工具
- System 消息里注入了 Harness 流程指导
- 其他一切不变

---

## 三、与当前架构的对比

### 当前：三个独立系统

```
┌─────────────┐     ┌──────────────┐     ┌──────────────┐
│ AgentProxy   │     │ TeamEngine   │     │ Pipeline     │
│ Session      │     │              │     │ Runner       │
│              │     │ N 个 Driver  │     │              │
│ 1 个 CLI     │     │ N 个 CLI     │     │ 状态机逻辑   │
│ 进程         │     │ 进程         │     │ onSessionIdle│
│              │     │              │     │ 消息入队     │
│ Chat Panel ← │     │ Team Panel ← │     │ auto-continue│
└──────────────┘     └──────────────┘     └──────────────┘
互不相通               独立进程池            深度耦合 Session
```

### 目标：一个 Agent + 可选能力注入

```
┌──────────────────────────────────────────────────────────┐
│                  AgentProxy Session                       │
│                  (1 个 CLI 进程)                           │
│                                                          │
│  始终有：                                                 │
│    Claude Code 内置工具（包括 Agent tool）                 │
│    niuniu-mcp-workspace（blackboard + inbox）             │
│                                                          │
│  Harness 启动时额外注入：                                  │
│    phase/gate MCP 工具 + 编排 prompt                      │
│                                                          │
│  ← Chat Panel（始终连接）                                  │
│  ← Team Panel（有 subagent 活动时自动展示）                │
└──────────────────────────────────────────────────────────┘

不存在 TeamEngine 管理多个进程
不存在 PipelineRunner 做状态机
不存在模式切换
```

---

## 四、TeamEngine 的命运：拆解重组

### 删除：TeamEngine + Manager

不再需要 TeamEngine 管理多个 Driver。Agent tool 就是 subagent 管理器。

### 保留并提升：Blackboard + Inbox → niuniu-mcp-workspace

合并为一个 MCP server，提供：

```
# 始终可用
blackboard_read(key)
blackboard_write(key, type, content)
blackboard_list(type?)
inbox_send(to, text)
inbox_read()
inbox_list_members()

# Harness 启动时可用（否则返回 "no active harness run"）
phase_current()
phase_list()
phase_complete(phase_name, summary)
phase_advance(feedback?)
gate_run(phase_name?)
gate_results(phase_name?)
```

### 保留并简化：PipelineRunner

不再是状态机，变成纯 CRUD + 事件发布：

```go
type PipelineRunner struct {
    q   *store.Queries
    bus *event.Bus
    cr  *CheckRunner
}

// Agent 通过 MCP 调用这些方法
func (pr *PipelineRunner) GetCurrentPhase(runID int64) (*PhaseInfo, error)
func (pr *PipelineRunner) CompletePhase(runID int64, phase string, summary string) error
func (pr *PipelineRunner) AdvancePhase(runID int64) error
func (pr *PipelineRunner) RunGateChecks(runID int64, phase string) ([]CheckResult, error)

// 用户从 UI 启动
func (pr *PipelineRunner) StartRun(ctx, wsID, harnessID, goal) error {
    run := createRunRecord(...)
    prompt := buildHarnessPrompt(...)
    session.Send(prompt)  // 发给 workspace 的 Agent
}
```

删除：`onSessionIdle`, `handlePhaseComplete`, `autoAdvance`, `Continue`,
`ContinueWithFeedback`, `reviewPending`, `autoContCount`, `controlQueue`。
~600 行 → ~100 行。

---

## 五、前端：Team Panel 何时展示

### 规则：有 subagent 活动时自动展示，否则隐藏

```
Team Panel 展示条件：
  1. Harness Run 正在进行（activeRun !== null）
  2. 或者 Agent 正在使用 Agent tool（检测到 tool_use(Agent) 事件）
  3. 或者 Blackboard 有内容（entries.length > 0）
  4. 或者 Inbox 有消息

任何条件满足 → 自动打开 Team Panel
所有条件消失 → Team Panel 可关闭（不强制隐藏）
```

### 前端组件

```
workspace-page.tsx
├── ChatPanel（始终显示，不变）
│   ├── 用户消息
│   ├── Agent 回复
│   ├── AgentDispatchCard（当 Agent 使用 Agent tool 时展示）
│   ├── PhaseTransitionCard（当 Harness phase 变化时展示）
│   └── GateCheckCard（当 Gate 检查完成时展示）
│
└── Team Panel（有活动时自动展示）
    ├── HarnessBar（仅 Harness 运行时展示）
    │   ├── Phase 进度条
    │   └── 当前 phase + round 信息
    │
    ├── SubagentList
    │   └── SubagentCard × N
    │       ├── 名称、类型、模型、状态
    │       └── 点击展开详情
    │
    ├── InboxStream（有 Inbox 消息时展示）
    │   ├── 消息流
    │   └── 发送区域
    │
    └── BlackboardList（有条目时展示）
```

### Chat 里的 Agent tool 展示

Agent 使用 Agent tool 时，Chat 里自然展示为 tool_use 卡片。
我们只需定制 `toolName === "Agent"` 的渲染：

```tsx
function ToolUseCard({ toolName, toolInput, toolResult, status }) {
  if (toolName === 'Agent') {
    return <AgentDispatchCard input={toolInput} result={toolResult} status={status} />;
  }
  // 其他工具的默认展示...
}
```

不需要额外的 SSE 事件——Chat Panel 已经接收所有 tool_use/tool_result 事件。

---

## 六、场景走查

### 场景 A：普通对话（无 Team）

```
用户："帮我修复 login 页面的 CSS 问题"

Agent：直接修复，整个过程在 Chat 里展示
├── 读文件
├── 修改 CSS
└── 完成

Team Panel：不展示（无 subagent 活动）
跟现在完全一样，零改动体验
```

### 场景 B：Agent 自主使用 subagent（无 Harness）

```
用户："重构整个 auth 模块，拆分成独立的 service 层"

Agent：判断任务复杂度，自主决定用 Agent tool
├── [text] "这个重构涉及多个文件，我会拆分处理..."
├── [Agent tool] Explore agent → 分析依赖关系
├── [Agent tool] worker-1 → 重构 service 层 (background, worktree)
├── [Agent tool] worker-2 → 更新测试 (background)
├── [text] "两个子任务都已完成，让我检查结果..."
└── [text] "重构完成，所有测试通过"

Chat Panel：
  展示 Agent 的文字 + AgentDispatchCard（worker-1, worker-2 的卡片）

Team Panel：
  自动展示 → SubagentList 出现 worker-1, worker-2 卡片
  用户可以点击查看详情
  任务完成后，Team Panel 可关闭
```

### 场景 C：启动 Harness（正式 Pipeline）

```
用户：选择 Harness 模板 "Full Pipeline" → 输入目标 → 发送

系统：
1. 创建 harness_run 记录
2. 激活 phase/gate MCP 工具
3. 构建 Harness prompt 发给 Agent

Agent 收到 prompt：
├── [text] "开始执行 Pipeline，当前阶段：Design"
├── [MCP: phase_current] 获取阶段定义
├── [Agent tool] Explore → 代码库分析
├── [Agent tool] Plan → 设计方案
├── [MCP: blackboard_write] 保存设计文档
├── [MCP: gate_run] 执行 Design 阶段检查 → 全部通过
├── [MCP: phase_complete("design")] 完成 Design
├── [MCP: phase_advance] 推进到 Implement
├── [text] "进入 Implement 阶段，派发开发任务..."
├── [Agent tool] worker-1 (sonnet, worktree) → 模块 A
├── [Agent tool] worker-2 (sonnet, worktree) → 模块 B
├── ... (等待完成)
├── [MCP: gate_run] 执行 Implement 阶段检查
│   └── ❌ test-coverage 失败
├── [text] "测试覆盖率不足，指导 worker-1 补充..."
├── [Agent tool] worker-1 → 补充测试
├── [MCP: gate_run] 重新检查 → 全部通过
├── [MCP: phase_complete("implement")]
├── [MCP: phase_advance] → Review
├── [Agent tool] reviewer (haiku) → 代码审查
├── ...
├── [MCP: phase_complete("review")]
├── [MCP: phase_advance] → 无下一阶段
└── [text] "Pipeline 全部完成！"

Chat Panel：完整展示 Agent 的思考过程和每个操作
Team Panel：
  HarnessBar 展示 phase 进度
  SubagentList 动态更新
  BlackboardList 展示中间产物
```

### 场景 D：Harness 运行中用户干预

```
Harness 正在运行，Agent 在 Implement 阶段...

用户在 Chat 里说："等一下，API 设计需要改一下，加个分页参数"

Agent（就是在跑 Harness 的那个 Agent）：
├── [text] "好的，我会调整 API 设计并通知相关 worker"
├── [MCP: inbox_send("worker-2", "API 需要加分页参数...")]
├── [text] "已通知 worker-2 调整 API 设计"
└── (继续 Harness 流程)

用户体验：
  跟普通聊天一样，直接在 Chat 里说
  不需要切换到 Team Panel
  不需要找 Intervene 按钮
```

### 场景 E：Harness 完成后继续对话

```
Harness Pipeline 完成...

Agent：[text] "Pipeline 全部完成！认证系统已实现。"

Harness 状态：completed
Phase/Gate MCP 工具：返回 "no active run"（但不报错，Agent 自然不再调用）
Team Panel：可关闭
Blackboard/Inbox：数据保留，Agent 仍可查阅

用户："帮我看一下 worker-1 写的代码质量怎么样"

Agent：直接阅读文件并给出评价（普通对话）
或者：用 Agent(subagent_type: "feature-dev:code-reviewer") 做审查

无缝衔接，没有"退出 Team 模式"的概念
```

---

## 七、架构改动总结

### 删除

| 模块 | 原因 |
|------|------|
| `TeamEngine` | Agent tool 取代了多进程管理 |
| `TeamEngineManager` | 不再需要 per-workspace engine |
| `driver.AgentDriver` 接口 | 保留 ClaudeCLIDriver 供 AgentProxy 使用，但不再用于 Team |
| PipelineRunner 状态机 (~600 行) | 编排逻辑移入 Agent prompt |
| `TeamHandler.SendMessage` | 统一走 Agent 消息接口 |
| `TeamHandler.Intervene` | 改为 Inbox API |

### 新增

| 模块 | 作用 |
|------|------|
| `niuniu-mcp-workspace` | 统一 MCP server（blackboard + inbox + phase/gate） |
| `HarnessPromptBuilder` | 构建 Harness 注入 prompt |
| `InboxService` | Inbox 文件读写 + 文件监控 |
| `PhaseService` | Phase/Gate 的 CRUD 接口（被 MCP 调用） |
| `AgentDispatchCard` | Chat 里展示 Agent tool 调用 |
| `InboxStream` | Team Panel 的消息流 |

### 修改

| 模块 | 改动 |
|------|------|
| `PipelineRunner` | 700 行 → 100 行，纯 CRUD + 事件 |
| `AgentProxy Session` | 增加 Harness prompt 注入能力 |
| `TeamPanel` | 重新组织子组件，自动展示/隐藏 |
| `workspace .mcp.json` | 增加 niuniu-mcp-workspace 配置 |

### 不变

| 模块 | 原因 |
|------|------|
| `AgentProxy Session` 核心 | 始终是 1 个 CLI 进程 |
| `ChatPanel` 核心 | 始终接收 SSE 事件 |
| `agent-sse-store` | 通用 SSE 路由不变 |
| `harness_runs` 表 | 记录结构不变 |
| `workspace_agents` 表 | 保留，用于记录 subagent 活动 |
| `blackboard_entries` 表 | 保留 |

---

## 八、这个设计为什么更好

### 1. 零概念负担

用户不需要理解"普通模式"和"Team 模式"的区别。
就像用 Claude Code 一样——你不需要知道 Agent tool 的存在也能正常使用，
但当任务复杂时，Agent 会自动用它。

### 2. 渐进式复杂度

```
简单任务：Agent 直接做          （零开销）
中等任务：Agent 用 Agent tool    （自动，无需配置）
复杂任务：Harness + 流程化      （显式启动，注入编排规则）
```

### 3. 不可能出现"模式不一致"

因为没有模式。永远是同一个 Agent，同一个 Chat，同一个 SSE 流。
Team Panel 是 Chat 的补充视角，不是独立系统。

### 4. 实现量更小

不需要实现 TeamEngine、多进程管理、进程间通信、状态同步。
核心工作量：
- 1 个 MCP server（niuniu-mcp-workspace）
- 1 个 Prompt builder
- 几个前端组件
- PipelineRunner 大幅删减
