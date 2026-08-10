# UI 重构：Lead Agent 交互模式设计

## 一、当前问题

### Chat 和 Team 是两个割裂的世界

```
当前架构：

┌─────────────────────────────┬──────────────────────────────┐
│        Chat Panel           │        Team Panel            │
│                             │                              │
│  User ↔ AgentProxy Session  │  Workers (只读展示)           │
│  (单独的 Claude CLI 进程)    │  Blackboard (只读展示)        │
│                             │  Pipeline Progress           │
│  完全独立，不知道 Team 的    │                              │
│  存在                        │  Intervene 按钮（很少用）    │
│                             │  没有对话历史                 │
└─────────────────────────────┴──────────────────────────────┘

用户困惑：
- "我在 Chat 里说的话，Team 的 Agent 能看到吗？" → 不能
- "Team Agent 的输出在哪里看？" → 看不到，只有 Blackboard
- "我怎么知道 Lead 在做什么？" → 不知道，没有可见性
```

### 新架构下的统一

```
目标架构：

┌─────────────────────────────┬──────────────────────────────┐
│    Chat Panel (与 Lead 对话)  │    Team Panel (团队状态)      │
│                              │                              │
│  User ↔ Lead Agent           │  Subagent 列表 + 状态         │
│  (唯一的 Claude CLI 进程)     │  Pipeline Progress           │
│                              │  Inbox 消息流                 │
│  用户说的每句话 Lead 都能     │  Blackboard                  │
│  看到，Lead 用 Agent tool     │  Agent 活动日志              │
│  派发子任务                   │                              │
│                              │  点击 Agent → 查看详情        │
│  Lead 的输出直接在 Chat 里    │  Intervene → 写入 Inbox      │
└─────────────────────────────┴──────────────────────────────┘

Lead Agent = Chat Panel 的 Agent = Team 的协调者
一个进程，两个视角
```

---

## 二、核心交互模式

### 2.1 用户 → Lead Agent：通过 Chat Panel 自然对话

用户在 Chat 里说的每句话都直接发给 Lead Agent。Lead 根据上下文决定：

| 用户输入 | Lead 行为 | 用户看到 |
|---------|----------|---------|
| "帮我实现用户认证" | Lead 分析目标 → 用 Agent tool 派发子任务 | Chat 里看到 Lead 的思考和 Agent tool 调用 |
| "认证模块进展如何？" | Lead 读取 Blackboard/Inbox | Chat 里看到 Lead 的汇报 |
| "告诉 worker-1 改用 JWT" | Lead 写入 worker-1 Inbox | Chat 里看到 Lead 确认已发送 |
| "暂停所有工作" | Lead 不再派发新任务 | Chat 里看到 Lead 确认 |
| "gate 检查结果怎样？" | Lead 调用 gate_results MCP | Chat 里看到检查结果 |
| "推进到下一个阶段" | Lead 调用 phase_advance MCP | Chat 里看到阶段切换 |

**这是最大的改变：用户不再需要区分"跟谁说话"。所有指令发给 Lead，Lead 负责路由。**

### 2.2 Lead Agent 的输出展示

Lead 的 stdout 是 stream-json 格式，包含：

```
普通文本 → Chat 里的 Assistant 消息
tool_use(Agent tool) → Chat 里显示"正在派发子任务..."
tool_result(Agent tool) → Chat 里显示子任务完成结果
tool_use(MCP blackboard) → Chat 里折叠显示 Blackboard 操作
tool_use(MCP phase_control) → Chat 里显示阶段控制操作
```

### 2.3 子 Agent 的可见性：通过 Team Panel

子 Agent 是 Lead 的 subagent，它们的输出不直接出现在 Chat 里（Claude Code Agent tool 的设计：subagent 结果作为 tool_result 返回给父 Agent）。

用户通过 Team Panel 获得子 Agent 的可见性：

```
Team Panel
├── SubagentList          ← 替代原来的 WorkerGrid
│   ├── SubagentCard: worker-1
│   │   ├── 状态：running (正在实现认证模块)
│   │   ├── 模型：sonnet
│   │   ├── 隔离：worktree (分支: agent/worker-1)
│   │   ├── 最后活跃：2s ago
│   │   └── [查看详情] [发送消息]
│   ├── SubagentCard: worker-2
│   │   ├── 状态：completed
│   │   └── 结果摘要：已完成 API 端点实现
│   └── SubagentCard: reviewer-1
│       ├── 状态：waiting (等待 worker-1 完成)
│       └── 类型：feature-dev:code-reviewer
│
├── InboxStream           ← 新增：实时消息流
│   ├── [worker-1 → Lead] "认证模块基本完成，但有个 edge case..."
│   ├── [Lead → worker-1] "请处理 token 过期的情况"
│   ├── [worker-2 → Lead] "API 端点全部通过测试"
│   └── [用户输入框] "发消息给..."  ← 选择目标 Agent
│
├── PipelineProgress      ← 保留
│   └── Design ✓ → Implement ● → Review ○ → Done ○
│
└── BlackboardList        ← 保留
    ├── auth-task-spec (plan) by lead
    ├── auth-impl-result (result) by worker-1
    └── api-test-result (result) by worker-2
```

---

## 三、具体 UI 组件设计

### 3.1 Chat Panel 改动

#### 3.1.1 Agent Tool 调用卡片

当 Lead 使用 Agent tool 派发子任务时，Chat 里显示一个可展开的卡片：

```
┌─ 🤖 Dispatching Agent ──────────────────────────────────┐
│                                                          │
│  worker-1 (general-purpose, sonnet)                      │
│  "实现用户认证模块，包括登录、注册、JWT token 管理"         │
│                                                          │
│  ⚙️ isolation: worktree  |  📡 background: true          │
│                                                          │
│  Status: ● running (3m 22s)                              │
│                                                          │
│  [▼ 展开结果]                                             │
└──────────────────────────────────────────────────────────┘
```

当 subagent 完成时，卡片更新：

```
┌─ ✅ Agent Completed ─────────────────────────────────────┐
│                                                          │
│  worker-1 (general-purpose, sonnet)                      │
│  "实现用户认证模块"                                       │
│                                                          │
│  Duration: 5m 12s  |  Cost: $0.0342                      │
│                                                          │
│  [▼ 展开结果]                                             │
│  ┌──────────────────────────────────────────────────────┐│
│  │ 已完成以下文件修改：                                   ││
│  │ - server/internal/api/auth.go (新增)                  ││
│  │ - server/internal/service/auth.go (新增)              ││
│  │ - server/internal/store/queries/users.sql (修改)      ││
│  │ 所有测试通过 (12/12)                                   ││
│  └──────────────────────────────────────────────────────┘│
└──────────────────────────────────────────────────────────┘
```

**实现方式：** 
Chat Panel 已有 `tool_use` + `tool_result` 的展示逻辑。
只需为 `toolName === "Agent"` 的 tool_use 事件定制一个 `AgentDispatchCard` 组件。

#### 3.1.2 Phase 切换提示

当 Lead 调用 `phase_complete` 或 `phase_advance` MCP 工具时，Chat 里显示：

```
┌─ 📋 Phase Transition ───────────────────────────────────┐
│                                                          │
│  ✅ Phase "Implement" completed                          │
│  Gate checks: 3 passed, 0 failed                         │
│                                                          │
│  → Advancing to Phase "Review"                           │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

#### 3.1.3 Harness 启动入口

保留 ChatInput 里的 Harness selector。用户选择 Harness 模板 → 输入目标 → 发送。

后端改动：StartRun 创建 harness_run 记录后，不再走旧的 Pipeline 状态机，
而是启动 TeamEngine（Lead Agent），将 Harness 模板信息注入 Provisioning Prompt。

### 3.2 Team Panel 改动

#### 3.2.1 SubagentList（替代 WorkerGrid）

```tsx
interface SubagentInfo {
  name: string;
  description: string;        // Agent tool 的 description 参数
  subagentType: string;        // "general-purpose", "Explore", etc.
  model: string;               // "sonnet", "haiku", "opus"
  isolation: string;           // "", "worktree"
  runInBackground: boolean;
  status: 'dispatching' | 'running' | 'completed' | 'failed';
  startedAt: number;
  completedAt?: number;
  resultSummary?: string;      // subagent 返回的摘要
  currentTask?: string;        // 当前在做什么
}
```

数据来源：
- Lead Agent 调用 `team_register_member` MCP → 推送 SSE → 前端更新
- 或者解析 Lead 的 `tool_use(Agent)` 事件，提取 Agent 调用参数

#### 3.2.2 InboxStream（新增）

实时展示 Agent 间的消息流：

```tsx
interface InboxMessage {
  id: string;
  from: string;       // "lead", "worker-1", "user"
  to: string;         // "lead", "worker-1", "all"
  text: string;
  timestamp: string;
  read: boolean;
}

function InboxStream({ messages, onSend }: Props) {
  return (
    <div className="flex flex-col h-full">
      {/* 消息列表 */}
      <div className="flex-1 overflow-y-auto">
        {messages.map(msg => (
          <InboxMessageBubble key={msg.id} message={msg} />
        ))}
      </div>

      {/* 发送区域 */}
      <div className="border-t p-2 flex gap-2">
        <Select placeholder="发送给...">
          <option value="lead">Lead</option>
          {subagents.map(a => (
            <option value={a.name}>{a.name}</option>
          ))}
        </Select>
        <Input placeholder="输入消息..." />
        <Button>发送</Button>
      </div>
    </div>
  );
}
```

用户在 InboxStream 里发送消息 → API → 写入目标 Agent 的 Inbox 文件。

#### 3.2.3 AgentActivityLog（新增）

展示 Lead 的编排决策历史：

```
[10:30:12] Lead 分析任务，分解为 3 个子任务
[10:30:15] Lead 派发 worker-1 (sonnet, worktree): "实现认证模块"
[10:30:16] Lead 派发 worker-2 (sonnet): "实现 API 端点"
[10:30:17] Lead 派发 reviewer (haiku, background): "等待并审查"
[10:35:22] worker-2 完成，结果写入 Blackboard
[10:38:45] worker-1 完成，结果写入 Blackboard
[10:38:50] Lead 触发 Gate Check: 3 pass, 0 fail
[10:38:52] Lead 调用 phase_complete("implement")
[10:38:55] reviewer 开始审查...
```

数据来源：Lead 的 SSE 事件流中的 `tool_use` 事件 + MCP 调用事件。

---

## 四、交互流程详解

### 流程 1：启动 Harness Run

```
用户操作：
1. Chat Panel → 选择 Harness 模板 "Full Pipeline"
2. 输入目标："实现用户认证系统，支持 JWT"
3. 点击发送

系统行为：
1. POST /api/workspaces/:id/harness-runs { harness_id, goal }
2. 创建 harness_run 记录
3. TeamEngine.Start() 启动 Lead Agent (唯一 CLI 进程)
   - Lead 的 System Prompt 包含 Harness 模板 + 目标
   - .mcp.json 配置了 blackboard + inbox + harness MCP
4. Lead Agent 开始工作，输出流入 Chat Panel

Chat Panel 显示：
┌─ 🤖 Niuniu Lead ─────────────────────────────────────────┐
│ 我将协调团队完成"用户认证系统"的实现。                       │
│                                                           │
│ 当前阶段：Design                                          │
│ 我先探索现有代码库，了解项目结构...                          │
│                                                           │
│ [🔧 Agent: explore-codebase]                              │
│   Explore agent 分析代码库结构...                           │
│   结果：项目使用 Gin + SQLite，已有 auth 目录但为空          │
│                                                           │
│ 基于分析，我将任务分解为：                                  │
│ 1. API Handler + Service 层 (worker-1)                    │
│ 2. 数据库 Schema + Store 层 (worker-2)                    │
│ 3. JWT 中间件 (worker-3)                                  │
│                                                           │
│ [🤖 Dispatching: worker-1 (sonnet, worktree)]             │
│ [🤖 Dispatching: worker-2 (sonnet, worktree)]             │
│ [🤖 Dispatching: worker-3 (sonnet, worktree)]             │
└───────────────────────────────────────────────────────────┘

Team Panel 同步更新：
- SubagentList 出现 3 个新卡片
- PipelineProgress 显示 "Design ✓ → Implement ●"
```

### 流程 2：用户中途干预

```
用户操作：
在 Chat Panel 输入："worker-1 的认证方式改成 OAuth2，不要用 JWT"

Lead Agent 行为：
1. 读取用户消息
2. 通过 Inbox 发消息给 worker-1："请改用 OAuth2 而非 JWT"
3. 如果 worker-1 正在运行中：消息写入 inbox，worker-1 下次检查时看到
4. 如果 worker-1 已完成：Lead 决定是否重新派发

Chat Panel 显示：
┌─ 🤖 Niuniu Lead ──────────────────────────────────────────┐
│ 好的，我已通知 worker-1 改用 OAuth2。                       │
│                                                            │
│ [📨 Inbox: Lead → worker-1]                                │
│ "请将认证方式从 JWT 改为 OAuth2，需要支持..."                │
│                                                            │
│ 我会监控 worker-1 的调整进度。                              │
│ 同时 worker-2 的数据库层不受影响，继续进行。                 │
└────────────────────────────────────────────────────────────┘

替代方式（通过 Team Panel 的 InboxStream）：
用户也可以直接在 InboxStream 选择 worker-1 → 发送消息
这绕过 Lead，直接写入 worker-1 的 Inbox
```

### 流程 3：查看子 Agent 详情

```
用户操作：
Team Panel → 点击 worker-1 卡片的 [查看详情]

显示 AgentDetailDrawer（右侧抽屉或弹窗）：
┌──────────────────────────────────────────────────────────┐
│ worker-1                                                  │
│                                                          │
│ 模型：sonnet  |  类型：general-purpose  |  隔离：worktree  │
│ 分支：agent/worker-1-auth                                 │
│ 状态：● running (8m 15s)                                  │
│ 当前任务：实现 OAuth2 认证流程                              │
│                                                          │
│ ─── Inbox 消息 ──────────────────────────                 │
│ [Lead → worker-1] 请实现用户认证模块...                    │
│ [Lead → worker-1] 请改用 OAuth2 而非 JWT                   │
│ [worker-1 → Lead] 收到，正在调整实现方案                    │
│                                                          │
│ ─── Blackboard 贡献 ─────────────────                     │
│ auth-impl-progress (status): "OAuth2 flow 80% 完成"       │
│ auth-impl-result (code): [点击查看]                       │
│                                                          │
│ ─── 操作 ────────────────────────────                     │
│ [发送消息] [请求 Lead 重新派发] [查看 worktree diff]        │
└──────────────────────────────────────────────────────────┘
```

### 流程 4：Gate Check 失败处理

```
Lead Agent 在 Chat 里的输出：
┌─ 🤖 Niuniu Lead ──────────────────────────────────────────┐
│ 所有 worker 完成了 Implement 阶段的工作。                    │
│ 我现在执行 Gate Check...                                   │
│                                                            │
│ [🔧 MCP: gate_run(phase="implement")]                      │
│                                                            │
│ ❌ Gate Check 结果：                                        │
│ ├── ✅ conventional-commits: pass                          │
│ ├── ✅ linter: pass                                        │
│ └── ❌ test-coverage: fail (coverage 42%, 需要 80%)        │
│                                                            │
│ test-coverage 检查失败。我将指导 worker-1 补充测试用例。      │
│                                                            │
│ [📨 Inbox: Lead → worker-1]                                │
│ "测试覆盖率不足(42%)，请为 OAuth2 flow 补充单元测试..."     │
│                                                            │
│ [🤖 Dispatching: worker-1 (sonnet)]                        │
│ "补充 auth 模块的测试用例，目标覆盖率 80%+"                 │
└────────────────────────────────────────────────────────────┘
```

### 流程 5：需要人工介入

```
Lead Agent 超过 max_rounds 或遇到无法解决的问题时：

Chat Panel 显示：
┌─ 🤖 Niuniu Lead ──────────────────────────────────────────┐
│ ⚠️ 我在 Implement 阶段遇到了困难：                         │
│                                                            │
│ worker-1 已尝试 3 次修复 test-coverage 问题但仍未达标。      │
│ 当前覆盖率 67%，目标 80%。                                  │
│                                                            │
│ 主要未覆盖的代码路径：                                      │
│ - OAuth2 callback 错误处理 (auth.go:120-145)               │
│ - Token 刷新竞态条件 (token.go:80-95)                      │
│                                                            │
│ 请问：                                                     │
│ 1. 是否可以降低覆盖率要求到 65%？                           │
│ 2. 或者您能指导如何测试 OAuth2 callback 的边缘情况？         │
│ 3. 还是跳过此 gate，继续推进到 Review 阶段？                │
└────────────────────────────────────────────────────────────┘

用户在 Chat 里回复：
"降低到 65% 吧，OAuth2 callback 的 mock 太复杂了"

Lead Agent 继续：
- 调用 phase_complete("implement") 
- 调用 phase_advance()
- 推进到 Review 阶段
```

---

## 五、API 调整

### 5.1 统一 Chat 和 Team 的消息入口

**之前**：两个独立的消息路径

```
POST /api/workspaces/:id/messages      → AgentProxy Session（Chat）
POST /api/workspaces/:id/team/message  → TeamEngine Coordinator（Team）
POST /api/workspaces/:id/team/intervene → TeamEngine Worker（Team）
```

**之后**：统一到一个路径

```
POST /api/workspaces/:id/messages      → Lead Agent（唯一进程）

# Team 的消息通过 Inbox API
POST /api/workspaces/:id/team/inbox    → 写入指定 Agent 的 Inbox 文件
GET  /api/workspaces/:id/team/inbox    → 读取 user.json（Agent 给用户的消息）
```

### 5.2 Lead Agent 进程 = 之前的 AgentProxy Session

关键改动：**Lead Agent 由 TeamEngine 启动后，注册为该 workspace 的 AgentProxy Session**。

```go
func (e *TeamEngine) Start(ctx context.Context) error {
    // ...启动 Lead Agent...

    // 将 Lead 的 stdout 事件流接入 AgentProxy 的 SSE 通道
    go e.forwardLeadEventsToSSE(ctx)
}

func (e *TeamEngine) forwardLeadEventsToSSE(ctx context.Context) {
    for evt := range e.leadDriver.Receive(ctx) {
        // 转发到 Event Bus → SSE → 前端 Chat Panel
        e.bus.Publish(event.OutputEvent{
            Type:        evt.Type,   // "text", "tool_use", "tool_result", etc.
            Content:     evt.Content,
            WorkspaceId: e.config.WorkspaceID,
            Ts:          time.Now().UnixMilli(),
        })
    }
}
```

这样前端的 Chat Panel **无需任何改动**就能展示 Lead 的输出——
因为 SSE 事件格式和现有的 AgentProxy 输出完全一致。

### 5.3 新增 Team API

```
GET  /api/workspaces/:id/team/subagents   → 子 Agent 列表（从 Blackboard/状态文件）
GET  /api/workspaces/:id/team/inbox       → Inbox 消息流
POST /api/workspaces/:id/team/inbox       → 发送 Inbox 消息
GET  /api/workspaces/:id/team/activity    → Agent 活动日志
```

---

## 六、SSE 事件流设计

### 6.1 Chat Panel 消费的事件（不变）

```
text, tool_use, tool_result, thinking, done, error, system_info
harness_confirm, harness_status, task_update
```

全部来自 Lead Agent 的 stdout，通过 `forwardLeadEventsToSSE` 转发。

### 6.2 Team Panel 消费的新事件

```
team:subagent_dispatched   → Lead 调用 Agent tool 时触发
  payload: { name, description, model, isolation, subagentType, status }

team:subagent_completed    → Agent tool 返回结果时触发
  payload: { name, duration, resultSummary }

team:subagent_failed       → Agent tool 执行失败时触发
  payload: { name, error }

team:inbox_message         → Inbox 文件变化时触发
  payload: { from, to, text, timestamp }

team:phase_update          → Lead 调用 phase_control MCP 时触发
  payload: { phase, status, summary }
```

### 6.3 事件提取方式

Lead 的 stdout 是 stream-json 格式。当检测到特定 tool_use 事件时，提取并转发额外的 team 事件：

```go
func (e *TeamEngine) forwardLeadEventsToSSE(ctx context.Context) {
    for evt := range e.leadDriver.Receive(ctx) {
        // 1. 原样转发到 SSE（Chat Panel 用）
        e.bus.Publish(toOutputEvent(evt))

        // 2. 提取 team 相关事件
        if evt.Type == "tool_use" {
            var toolCall struct {
                Name  string          `json:"name"`
                Input json.RawMessage `json:"input"`
            }
            json.Unmarshal(evt.Raw, &toolCall)

            if toolCall.Name == "Agent" {
                // 提取 Agent tool 参数 → 发布 team:subagent_dispatched
                e.publishSubagentDispatched(toolCall.Input)
            }
        }

        if evt.Type == "tool_result" && isAgentToolResult(evt) {
            // Agent tool 完成 → 发布 team:subagent_completed
            e.publishSubagentCompleted(evt)
        }
    }
}
```

---

## 七、组件改动清单

### 前端新增组件

| 组件 | 位置 | 用途 |
|------|------|------|
| `AgentDispatchCard` | `components/chat/` | Chat 里展示 Agent tool 调用 |
| `PhaseTransitionCard` | `components/chat/` | Chat 里展示阶段切换 |
| `SubagentList` | `components/team/` | 替代 WorkerGrid，展示 subagent |
| `SubagentCard` | `components/team/` | 替代 WorkerCard，更丰富的信息 |
| `InboxStream` | `components/team/` | Inbox 消息流 + 发送 |
| `AgentActivityLog` | `components/team/` | Lead 编排决策历史 |
| `AgentDetailDrawer` | `components/team/` | 子 Agent 详情抽屉 |

### 前端修改组件

| 组件 | 改动 |
|------|------|
| `ChatPanel` | 增加对 Agent tool_use 的特殊渲染 |
| `TeamPanel` | 替换子组件，增加 InboxStream 和 ActivityLog |
| `TeamStatusBar` | 数据源改为 Lead 上报 |
| `PipelineProgress` | 数据源改为 Lead 的 phase_control 调用 |

### 前端删除组件

| 组件 | 原因 |
|------|------|
| `WorkerGrid` | 被 SubagentList 替代 |
| `WorkerCard` | 被 SubagentCard 替代 |

### Store 改动

| Store | 改动 |
|-------|------|
| `team-store.ts` | 增加 subagents、inboxMessages、agentActivity 状态 |
| `agent-sse-store.ts` | 不变（通用 SSE 路由） |

### 后端改动

| 文件 | 改动 |
|------|------|
| `team/engine.go` | Lead stdout → SSE 转发 + team 事件提取 |
| `api/team.go` | 新增 inbox API、subagent API |
| `api/agentproxy.go` | SendMessage 路由到 Lead Agent |
