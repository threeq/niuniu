# 接活与完成：牛牛自主性双能力拆分设计

日期：2026-11-03 ｜ 状态：已确认（2026-11-03）｜ **已实现（2026-11-04，Epic #755）** ｜ 上线与运维见 [`docs/operations/intake-completion-rollout.md`](../../operations/intake-completion-rollout.md)

## 0. 一句话

把牛牛现有的「自动托管」拆成两个独立能力——**Intake（接活）** 决定哪些外部事件可以变成任务，**Completion（完成）** 决定一个已成立的任务如何被推到完成；两者独立配置、独立授权、独立指标，通过 **Task Brief** 交接。

> 术语约定：概念层与代码层用 **Intake / Completion**；面向用户的 UI 文案用「**任务来源 / 自动完成**」；正文中的「接活 / 完成」是简称。

---

## 1. 背景

牛牛当前已经具备两条相关链路，但它们在概念、配置和指标上没有被显式区分：

- **Completion**：`server/internal/agentproxy/autohost.go`。任务内续跑，靠 `goal_condition`、`[AUTOHOST_DONE]` 哨兵、`autohostMaybeContinue` / `autohostMaybeRecover`、不活动看门狗和预算，把任务推到完成。
- **Intake**：`server/internal/service/imbot_inbound.go` + `dispatch_service.go` + `imbot_employee.go`。IM 消息去重、chat 准入、thread/pinned/active 解析、`RouteInProject` 续接或新建、员工分身 observe 分析。

现状问题：

1. **概念混淆**：产品叙事和配置容易把两者合成一个「自主性」开关，导致只能「全手动」或「全自动」，没有中间态。
2. **Intake 只覆盖 IM**：GitHub PR 评论、CI 结果、邮件没有进入同一条准入管线。
3. **授权是 chat 级**：批准一个群之后，群内任何人都能驱动 agent；缺 actor 级 allow list、explicit/inferred 强度和策略版本。
4. **缺 dry-run**：员工分身当前在预算内会直接建任务或发消息，没有「只记录本来会做什么」的校准阶段。
5. **Task Brief 缺失**：`CreatePlanInProject` 把 `goal_condition` 直接设为原始 description，autohost 拿一句模糊需求当验收标准，容易出现提前宣布完成或反复续跑。
6. **指标混在一起**：没有区分「接活准不准」和「完成得好不好」。

本设计给出拆分方案与分阶段落地路径。**本期只做设计，不写实现。**

---

## 2. 现状盘点（代码映射）

| 能力 | 现有实现 | 说明 |
|---|---|---|
| IM 入站去重 | `imbot_inbound.go:31` `HandleInbound` | `channel + event_id` 幂等 |
| chat 准入/配对 | `imbot_inbound.go` + `im_bot_chats` | 批准的是 chat，不是 actor |
| 任务解析 | `imbot_inbound.go:750` `resolveTask` | thread > pinned > active pointer |
| 续接/新建 | `dispatch_service.go:91` `RouteInProject` | 可选 `PlanRouter` LLM 分类；新建 issue+workspace |
| Task 创建 | `dispatch_service.go` `CreatePlanInProject` | `goal_condition = description`，`PermissionMode=autohost` |
| 投递 agent | `imbot_inbound.go:259` `deliverToWorkspace` | 附件落盘、reaction、agentproxy.Deliver |
| 主动观察 | `imbot_observe.go` / `imbot_employee.go` | observe 模式、verdict 阶梯、每 chat 每天 4 次预算 |
| 出站通知 | `imbot_dispatch.go:146` | 只推生命周期/需人事件，按 chat/thread 精确投递 |
| 完成续跑 | `agentproxy/autohost.go` | 哨兵、continue/recover、看门狗、预算 |
| 执行轨迹 | `issue_exec_events` | 已有 `advance/gate/ask_user/terminal/cost/intervention` |
| 外部来源字段 | `issues.external_source/external_id/external_url` | 可作为 provenance 的落点 |

结论：**两条链路已经存在且架构上分开，缺的是显式的能力边界、统一的准入管线、dry-run 和交接契约。**

---

## 3. 目标与非目标

### 3.1 目标

- **G1 能力拆分**：Intake Policy 与 Completion Policy 独立配置、独立开关、独立指标。
- **G2 多源 Intake**：把 IM 入站管线泛化为 Source 管线，首批接入 GitHub（issue/PR 评论、review、CI）。
- **G3 Actor 级授权**：每个 Source 的 allow list、explicit/inferred 强度、policy version、可审计 provenance。
- **G4 Dry-run**：`observe` 模式只记录 would-do decision，不创建任务、不发消息；配 review 队列。
- **G5 Task Brief**：Intake 产出结构化目标/验收标准/范围/回投地址；Completion 只据此判断是否完成。
- **G6 注意力可观测**：接活看准确率/误接/重复；完成看轮数/假完成/人工介入/成本。
- **G7 双入口 UI**：任务来源与自动完成在导航、页面、指标上分开；所有配置都能在 UI 完成，不依赖手改 DB 或配置文件。

### 3.2 非目标

- 不做云端存算分离、跨实例 A2A、远程执行调度。
- 不替换现有 agentproxy / workspace / worktree 执行底座。
- 不做无限制自主；新 Source 默认 dry-run。
- 不做自动 merge；完成后的合并仍按现有工程规范与人工确认。
- 不在本期接入全部平台；GitHub 为唯一新增 Source，邮件/通用 webhook 留到后续。
- 不做统一的「自主性滑块」或单一 autonomy score 面板；接活与完成必须保持两个独立入口。

---

## 4. 概念模型

### 4.1 两个能力

| 维度 | Intake（接活） | Completion（完成） |
|---|---|---|
| 回答的问题 | 这件事要不要变成任务？谁授权？接到哪里？ | 这个任务做完了吗？没做完继续做什么？ |
| 作用范围 | 任务间（between-task） | 任务内（within-task） |
| 触发 | 外部事件：IM、PR 评论、CI、邮件、定时源 | turn 结束、进程异常、队列空闲 |
| 核心状态 | event、actor、thread、policy、decision | goal、acceptance、round、budget、watchdog |
| 输出 | 新建 issue+workspace / 续接 / 忽略 / 转人工 | 继续 / 恢复 / 停止 / 提问 |
| 主要风险 | 越权、误接、重复、接错项目、注入 | 跑飞、空转、假完成、范围蔓延、成本 |
| 控制 | allow list、mode、rules、daily cap、dedupe | goal_condition、max rounds、token budget、review gate |
| 指标 | 接单准确率、误接/漏接、重复率、事件到开工时延 | 一次通过率、续跑轮数、假完成率、HITL、每任务成本 |

### 4.2 首要原则

1. **Intake 不判断「做完没有」**；Completion 不判断「该不该接」。
2. **续接是 Intake 的动作，但复用原 Task Brief / goal**；只有明确的新请求才生成新 Brief。
3. **外部文本是数据，不是指令**。授权与工具边界由代码强制，prompt 文本不能改变。
4. **默认保守**：off / dry-run 优先；auto 必须经过校准和显式开启。

### 4.3 端到端流程

```
Source 事件
   │
   ▼
┌────────────── Intake ──────────────┐
│ dedupe → authorize → resolve task  │
│        → decide → record decision  │
└──────────────┬─────────────────────┘
               │ accept / resume
               ▼
         Task Brief（目标/验收/范围/回投）
               │
               ▼
        Issue + Workspace
               │
               ▼
┌──────────── Completion ────────────┐
│ autohost: continue / recover / stop │
│ 对照验收标准 + 预算 + 看门狗        │
└──────────────┬─────────────────────┘
               │ done / blocked / need decision
               ▼
        结果回投到 Source Thread
               │
               └──► 后续事件经 Intake 续接同一任务
```

---

## 5. 总体架构

新增一个 **IntakeService**，从 `HandleInbound` 中抽出与 IM 无关的通用管线；IM 只是第一个 Source adapter。

```
server/internal/intake/
  service.go        # Handle(trigger): dedupe/authorize/resolve/decide/execute
  source.go         # Source 接口 + Trigger/Thread/Actor 归一化类型
  policy.go         # mode、allow list、rules、strength、policy version
  brief.go          # Task Brief 生成、渲染为 goal_condition/checklist
  adapters/
    imbot.go        # 包装现有 imbot.InboundEvent
    github.go       # issue_comment / pull_request_review / check_run
    webhook.go      # 后续：通用入站
```

`CompletionService` 不新建包，直接扩展现有 `agentproxy` autohost：

```
server/internal/agentproxy/autohost.go
  + completion policy 读取
  + acceptance checklist 校验
  + stop-by-impact / max rounds / token budget
```

---

## 6. 详细设计

### 6.1 Source 抽象与 Trigger

```go
type Trigger struct {
    ProjectID   int64
    SourceKind  string   // im_bot | github | ci | email | webhook
    SourceRef   string   // channel id / repo full name / mailbox
    EventID     string   // 平台事件 id，用于幂等
    EventKind   string   // message | issue_comment | review | check_run ...
    Thread      ThreadRef
    Actor       ActorRef
    Text        string
    Attachments []Attachment
    Raw         json.RawMessage
    ReceivedAt  time.Time
}

type ThreadRef struct {
    ThreadExtID string // IM thread / PR number / issue number
    IssueID     int64  // 已绑定时非 0
    WorkspaceID int64
}

type ActorRef struct {
    ExtID  string
    Name   string
    IsBot  bool
}

type Source interface {
    Kind() string
    Normalize(ctx context.Context, raw any) ([]Trigger, error)
    Reply(ctx context.Context, thread ThreadRef, msg Reply) error
}
```

`IntakeService.Handle(ctx, trigger)` 的执行顺序固定：

1. **Dedupe**：`UNIQUE(source_kind, source_ref, event_ext_id)`，重复直接返回。
2. **Authorize**：读取该 project+source 的 policy，产出 `strength ∈ {explicit, inferred, denied}`。
3. **Resolve task**：thread > pinned > active > classifier；复用现有 `resolveTask` / `RouteInProject`。
4. **Decide**：`accept | resume | ignore | unavailable`。
5. **Record**：无论是否执行，都写 `intake_decisions`（含 dry-run 的 `would_do_json`）。
6. **Execute**（仅非 dry-run）：新建/续接、生成 Task Brief、绑定 thread、`deliverToWorkspace`。
7. **Emit**：事件与指标。

### 6.2 授权与 Provenance

每个 project + source 维护：

- `allow_list`：允许发起工作的 actor 列表；
- `bot_list`：允许产生 inferred candidate 的机器人；
- `rules`：自然语言规则 + 结构化条件（项目、路径、标签、关键词）；
- `mode`：`off | observe | auto`；
- `daily_cap`：每 source 每天自动创建任务上限。

强度判定：

| 情形 | 强度 |
|---|---|
| allow list 内的人直接 @ 或私聊 | explicit |
| allow list 内的人在已绑定 thread 中 follow-up | explicit |
| bot_list 内的机器人，或命中 rule 的人 | inferred |
| 未列名单的人，在开启接收全部消息的范围 | inferred（仅 auto/observe） |
| 其他 | denied |

规则：

- 只有 **explicit** 可以停止任务或触发高风险动作；
- **inferred** 只能在匹配的 rule 范围内创建任务；
- 每次 decision 记录 `policy_version`、matched rule ids、reason code；
- provenance 落到 `issues.external_source / external_id / external_url`，并关联 `intake_decisions.id`；
- 外部消息文本永远不参与权限判断。

### 6.3 Dry-run / Observe

`mode` 语义：

| mode | 显式请求 | 推断请求 | 行为 |
|---|---|---|---|
| `off` | 执行 | 不评估 | 只处理 explicit |
| `observe` | 执行 | 只评估 | 记录 would-do，不建任务、不发消息 |
| `auto` | 执行 | 在 rule 和 cap 内执行 | 真正接活 |

`intake_decisions` 中 `executed=0` 且 `decision=accept/resume` 的行就是 dry-run 结果，进入「待校准」队列：

- 显示：来源、actor、原文摘要、建议 Task Brief、命中的 rule；
- 操作：`Accept`（真正建任务）、`Ignore`（记录原因）、`Edit brief`；
- 统计：would-accept 认可率；连续 N 次认可后可提示切换到 `auto`。

> 已确认（方案 A）：现有 `agent_mode=observe` 的 chat 迁移为 `auto`，保留「预算内直接行动」的行为；新 chat 默认 `observe`（dry-run）。

### 6.4 Task Brief 契约

Intake 接受任务时生成，存于 `task_briefs`（1:N，追加式版本；当前版本 = `max(version)`）：

```go
type TaskBrief struct {
    Goal       string      // 一句话结果
    Acceptance []string    // 可验证的验收项
    ScopeIn    []string
    ScopeOut   []string
    Delivery   DeliverySpec
    Provenance Provenance
}

type DeliverySpec struct {
    SourceKind string
    ThreadExtID string
    ReplyMode  string // thread | chat | pr_comment
}

type Provenance struct {
    DecisionID    int64
    PolicyVersion int64
    Strength      string // explicit | inferred
    ActorExtID    string
    RuleIDs       []int64
}
```

渲染规则：

- `issues.goal_condition` 由当前 Brief 渲染而来，是**缓存**，不是第二份真相；
- `Acceptance` 物化为 `issue_checklists` 中 `kind='acceptance'` 的行，作为完成校验的依据；
- 手工进度项保留为 `kind='task'`，不参与完成判定；
- 生成时可调用现有 `goal_condition_suggest` 起草，但必须允许人工在 dry-run 队列中修改；
- 续接已有任务时默认复用原 Brief；若用户明确扩大范围，追加新版本，旧版本保留。

**与 issue_checklists 的关系（已确认保留）**

- **Task Brief 的 acceptance = 完成标准（definition of done）**：稳定、版本化、可审计；autohost 以它为准。
- **issue_checklists = 执行进度**：可增删、可勾选、可由 agent 或人维护；不因 Brief 变更而丢失。
- Brief 创建/更新时，在同一事务内把 acceptance 物化为 `kind='acceptance'`、带 `brief_id` 的 checklist 行；旧的 acceptance 行标记失效，手工 `kind='task'` 行完全不动。
- autohost 只校验 acceptance（或直接读当前 Brief），**不是**校验所有 checklist 项；否则一个“查日志”的进度项会错误阻塞完成。
- UI 上分区展示：Task Brief 卡片显示验收标准；Checklist 组件显示「验收标准 / 执行清单」两个 section，避免两份看起来一样的列表。

### 6.5 Completion Policy

Completion 独立配置，不读取 Intake 的 mode：

```go
type CompletionPolicy struct {
    AutohostEnabled bool
    MaxRounds       int
    TokenBudget     int64
    StopOnDecision  bool
    RequireReview   bool
}
```

**配置粒度（已确认 C：命名 profile + issue 指定）**

- 项目级 `completion_settings` 是默认；
- 项目可定义若干 `completion_profiles`，例如「标准 / 快速 / 深度 / 高风险需审查」；
- `issues.completion_profile_id` 可空；空 = 继承项目默认；
- 解析规则只有一条：**issue 指定的 profile > 项目默认**，不做逐字段 merge；
- 由单一 `ResolveCompletionPolicy(issue)` 函数计算，autohost 与 UI 共用，避免两处逻辑漂移；
- profile 变更与 issue 指定变更记录操作人与时间，变更在下一个 turn 生效。

作用点：

- **Autohost 循环**：现有 `autohostMaybeContinue` / `autohostMaybeRecover` 保持；新增读取 effective policy。
- **完成判定**：`[AUTOHOST_DONE]` 仍需模型输出，但服务端额外校验当前 Task Brief 的 acceptance（物化为 `kind='acceptance'` 的 checklist 行）是否全部完成；未完成则继续，达到 `MaxRounds` 后转 `attention` 并提一个结构化问题。
- **Stop by impact**：需要设计决策时，worker 输出 `human_action` 并停止当前 turn，不继续空转；`StopOnDecision=true` 时强制走这条路。
- **预算**：`TokenBudget` 到达后停止续跑，状态置 `attention`，保留上下文。
- **Review gate**：`RequireReview=true` 时完成态进入人工审查列，不直接 `done`。

Completion 不得创建新 issue/workspace；越界请求交给 Intake 或人。

### 6.6 指标与可观测

**Intake 指标**（来自 `intake_decisions`）：

- `would_accept` 数量与认可率（Accept / (Accept+Ignore)）；
- 误接率：auto 接受后 24h 内被人工关闭或重开；
- 重复率：同 thread 在短时间内产生的重复任务；
- 事件到开工时延（received_at → workspace started）；
- 每 source / 每 chat 每天的自动任务数。

**Completion 指标**（来自 `issue_exec_events` + token 统计）：

- 一次通过率（无人工介入直接完成）；
- 续跑轮数、卡死率、假完成率（完成后 7 天内被重开）；
- 每完成任务 token / 成本；
- 人工介入次数（`issue_exec_events.kind='intervention'`）。

**呈现**：Mission control 增两个独立面板「接活」「完成」，不合并成一个「自主性」分数。

### 6.7 UI 设计

#### 6.7.1 设计原则

1. **两个能力、两个入口**：任务来源与自动完成在导航上分开，不做单一「自主性」滑块。
2. **注意力优先**：页面先显示「需要人处理」的项（待校准、需要决策、失败）；健康状态不占空间。
3. **渐进披露**：日常只看到 mode、规则数和待校准数；allow list、rules、策略历史折叠在「配置」里。
4. **与现有页面同构**：复用项目设置的 sub-tab、Issue 详情面板、Mission control 卡片、IM chat row，不引入新的导航范式。
5. **强制遵循 `docs/design-system.md`**：只使用 warm token、shadcn 组件、lucide 图标、`t()` 文案、4px 间距、Light/Dark 平价；组件内禁止十六进制色值和任意间距。
6. **一页一个 primary CTA**：设置页 primary 是「保存」；待校准页 primary 是「采纳并创建任务」；其余操作走 secondary/ghost。

#### 6.7.2 入口与信息架构

| 入口 | 位置 | 内容 |
|---|---|---|
| 项目设置 · 任务来源 | `pages/project/settings-tab.tsx` 新增 section `intake` | Source 卡片、mode、allow list、rules、daily cap、策略版本、待校准入口 |
| 项目设置 · 自动完成 | 同文件新增 section `completion` | autohost、最大轮数、token 预算、需要决策时停止、完成需人工审查 |
| 待校准队列 | 新增路由 `/intake`；从 Mission control 与项目设置进入 | 全局 dry-run 决策列表 + 详情 + 采纳/忽略 |
| Issue 详情 | `KanbanIssueDetailPanel.tsx` / `pages/workspaces/panels/issue-panel.tsx` | 来源徽章、Task Brief 卡片、Completion Policy |
| IM chat 行 | `components/projects/imbot/chat-row.tsx` | 接活模式下拉、覆盖项目默认的徽章、待校准计数 |
| Mission control | `pages/workspaces/workspaces-overview.tsx` | 「接活」「完成」两个独立指标面板 |
| 首次开启自动 | 确认 Dialog | 影响范围、规则、每日上限、校准记录、随时可关 |

#### 6.7.3 项目设置 · 任务来源

```
┌ 任务来源 ──────────────────────────────────────────────────┐
│ 让牛牛在规则允许时自动接活。默认只观察，不执行。           │
│                                          [暂停接活] [保存] │
│                                                            │
│ 来源                                                       │
│ ┌────────────────────────────────────────────────────────┐ │
│ │ [Bot] IM 机器人        已连接   [关闭 | 观察 | 自动]   │ │
│ │ 每天最多 4 个 · 3 条规则 · 2 位允许成员                │ │
│ │ [配置]   [查看待校准 6]                                │ │
│ └────────────────────────────────────────────────────────┘ │
│ ┌────────────────────────────────────────────────────────┐ │
│ │ [GitHub] GitHub        未连接                   [接入] │ │
│ └────────────────────────────────────────────────────────┘ │
│                                                            │
│ ! 建议先观察至少 10 条决策，再开启自动。                    │
└────────────────────────────────────────────────────────────┘
```

**Source 卡片**

- 标题行：lucide 图标 + 来源名 + 连接状态 Badge（`success` 已连接 / `warm-muted` 未连接）。
- mode 桌面用分段控件、移动用 shadcn `Select`（已确认），每档下方一行说明：
  - 关闭：只响应明确 @ 牛牛的消息；
  - 观察：记录本来会接的任务，不自动执行；
  - 自动：在规则和每日上限内自动接活。
- 摘要行：`每天最多 N 个 · M 条规则 · K 位允许成员`，数字用 `tabular-nums`。
- 操作：`配置`（secondary）、`查看待校准 N`（ghost）。
- 未连接来源只显示 `接入`，不显示 mode（避免误以为已生效）。

**展开配置（Collapse / Dialog）**

| 区块 | 控件 | 说明 |
|---|---|---|
| 允许成员 | chips + 成员选择器 | 只有名单内的人可以显式发起工作 |
| 允许机器人 | chips + 选择器 | 机器人只能产生 inferred，不能停止任务 |
| 规则 | 表格：名称 / 条件摘要 / 启用 Switch / 编辑 | 行高 ≥40px；空态用 `EmptyState` |
| 每日上限 | 数字输入 | 0 = 不自动接活；默认 4 |
| 策略版本 | 文本 + 「查看历史」 | 打开只读 Dialog，显示版本号、变更时间、快照 diff |
| 危险区 | 「暂停接活」 | 仅停止 inferred/auto，显式请求仍工作 |

**规则编辑 Dialog**

- 字段：名称、说明（Textarea）、条件（项目路径 / 标签 / 关键词）、优先级、启用。
- 说明文案提示：规则只描述「哪类工作可以接」，不授予超出 allow list 的权限。
- 保存后生成新的 `policy_version`，页面顶部显示「策略已更新，下一次事件生效」。

**状态与校验**

- 保存中：按钮 spinner + 禁用。
- 校验失败：字段下方 `text-xs text-destructive`。
- 开启 auto 但未完成校准：显示 warning Banner，不阻塞保存。
- 所有更改仅在点击 `保存` 后生效；离开页面有未保存改动时弹确认 Dialog。

#### 6.7.4 项目设置 · 自动完成

```
┌ 自动完成 ──────────────────────────────────────────────────┐
│ 控制一个任务被推进到什么程度，以及什么时候停下来问人。     │
│                                                    [保存] │
│                                                            │
│ [Switch] 自动续跑直到完成                                  │
│          关闭后，每个 turn 结束即回到待处理。              │
│                                                            │
│ 最大续跑轮数        [ 20 ]                                 │
│ Token 预算          [ 0  ]  0 = 不限                       │
│                                                            │
│ [Switch] 需要设计决策时停止并提问                          │
│ [Switch] 完成后进入人工审查                                │
│                                                            │
│ ! 预算为 0 且轮数较高时，长任务可能产生较高成本。          │
└────────────────────────────────────────────────────────────┘
```

- 所有控件走 shadcn `Switch` / `Input`，label 与控件 `gap-1.5`，组间 `gap-4`。
- `最大续跑轮数` 与 `Token 预算` 使用 `tabular-nums`。
- 保存后提示「下一个 turn 生效」，不重启当前 agent。
- 与任务来源完全独立：修改自动完成不改变哪些任务会被接。
- 项目级默认 + 命名 profile（已确认 C）；Issue 级通过指定 profile 覆盖，不做逐字段覆盖。

**命名配置**

```
┌ 命名配置 ────────────────────────────────────────────────┐
│ [标准]        20 轮 · 预算不限 · 需决策停止 · 不需审查    │
│ [快速]         3 轮 · 预算 20K  · 需决策停止 · 不需审查    │
│ [深度]        60 轮 · 预算 200K · 需决策停止 · 不需审查    │
│ [高风险需审查] 20 轮 · 预算 100K · 需决策停止 · 需人工审查  │
│                                            [新建配置]     │
└───────────────────────────────────────────────────────────┘
```

- 列表列：名称、轮数、预算、停止条件、审查、当前引用 issue 数。
- `新建配置` / 行内编辑打开 Dialog；内置四个配置可编辑，至少保留一个默认。
- Issue 详情只选择一个 profile：`继承项目默认 / 标准 / 快速 / 深度 / 高风险需审查`。
- 解析与展示：`issue profile > 项目默认`；Issue 详情显示「来源：项目默认 / 快速」，避免隐藏覆盖。
- profile 删除前必须检查引用；有引用的 issue 需先改回继承或换 profile。

#### 6.7.5 待校准队列（核心新页面）

路由 `/intake`，桌面双栏 master-detail，移动端列表 + 底部 Drawer。

```
┌ 待校准 ────────────────────────────────────────────────────┐
│ 牛牛在观察模式下记录的工作。采纳后才会真正创建任务。       │
│ [全部] [建议接单] [建议续接] [已忽略]        [批量忽略]   │
├──────────────────────────┬─────────────────────────────────┤
│ [GitHub] alice · PR #42  │ 来源  GitHub · PR #42           │
│ 2 分钟前   inferred      │ 发起人 alice                    │
│ 修复登录接口 500         │ 强度  inferred                  │
│ ──────────────────────── │ 命中规则  「线上缺陷自动接单」  │
│ [IM] bob · 产品群        │                                 │
│ 8 分钟前   explicit      │ 原始消息                        │
│ 把首页文案改成新版       │ ┌─────────────────────────────┐ │
│ ──────────────────────── │ │ 登录接口又 500 了，麻烦看下  │ │
│ ...                      │ └─────────────────────────────┘ │
│                          │                                 │
│                          │ 建议任务简报                    │
│                          │ 目标  [修复登录接口 500      ]  │
│                          │ 验收  ☐ 复现并定位             │
│                          │       ☐ 修复并补测试           │
│                          │       [+ 添加验收项]           │
│                          │ 范围  in: api/auth  out: 前端   │
│                          │ 回投  PR #42 评论               │
│                          │                                 │
│                          │ [采纳并创建任务] [编辑] [忽略]  │
└──────────────────────────┴─────────────────────────────────┘
```

**列表**

- 行内容：来源图标、actor、thread、时间、强度 Badge（explicit = `brand-soft`，inferred = `warm-muted`）、一行摘要。
- 未读/新决策用左侧 brand 色条标记，不用红点。
- 支持多选 + 批量忽略；批量采纳不做（避免误操作）。
- 空态：`EmptyState`，标题「没有待校准的决策」，描述「开启观察模式后，牛牛会把本来会接的任务记录在这里」，操作为「去配置任务来源」。
- Loading：列表 skeleton；错误：行内重试按钮。

**详情**

- 原始消息：只读引用块，最大高度 + 滚动；附件显示文件名 chip。
- 建议 Task Brief：Goal、Acceptance、Scope、Delivery 可编辑；Acceptance 每行可增删，保存时同步到 checklist。
- 命中规则与 policy version：只读 chip + `?` popover 解释。
- 操作：
  - primary：`采纳并创建任务`（创建后 toast + 跳转 Issue）；
  - secondary：`编辑`（进入编辑态，出现 `保存草稿`）；
  - ghost：`忽略`，必须选择原因（不相关 / 重复 / 项目不对 / 其他）。
- dry-run 的 `resume` 决策显示目标 Issue/Workspace，主按钮为 `采纳后续接`。
- 采纳失败：保留编辑内容，toast error，可重试。

**批量操作**

- 列表多选后底部出现操作条：`已选 N 项` + `批量忽略`（destructive outline）。
- 批量忽略必须选原因；不做批量采纳。

#### 6.7.6 Issue 详情

在现有 `IssueWorkspace` 之后、`IssueDescription` 之前插入两个卡片：

**Task Brief 卡片**（`IssueBriefCard`）

```
┌ 任务简报 ─────────────────────────────── [GitHub] [来源] ┐
│ 目标  修复登录接口 500                                    │
│ 验收  ☑ 复现并定位   ☐ 修复并补测试                       │
│ 范围  in: api/auth   out: 前端                            │
│ 回投  PR #42 评论                                         │
│ [编辑简报]                                                │
└───────────────────────────────────────────────────────────┘
```

- 验收项来自当前 Task Brief，物化为 checklist 的 `kind='acceptance'` 行，勾选状态与 issue 同步。
- 手工执行项在 Checklist 的「执行清单」section 展示，不参与完成判定。
- 来源 Badge 点击打开 provenance popover：来源、actor、强度、命中规则、policy version、decision id、原始事件链接。
- `编辑简报` 走 secondary；保存后追加新版本、更新 `goal_condition`、刷新 acceptance 行。
- 非 intake 创建的任务不显示此卡片，避免干扰存量 issue；intake 任务的卡片默认展开（已确认）。

**自动完成配置卡片**

- 显示 effective policy 与来源：`继承项目默认` 或指定 profile 名称。
- 一个 `Select`：`继承项目默认 / 标准 / 快速 / 深度 / 高风险需审查`；不做逐字段编辑。
- 只读摘要：`自动续跑 开 · 最多 20 轮 · 预算 100K · 需决策时停止`。

**Execution Timeline**

- 复用现有 `ExecutionTimeline`；增加 `intake` 与 `intervention` 两类事件的文案映射，不新增视觉。

#### 6.7.7 IM chat 行

在 `chat-row.tsx` 现有操作区增加：

- 接活模式 `Select`：`继承项目 / 关闭 / 观察 / 自动`。
- 与项目默认不同时，显示 `已覆盖项目默认` Badge（`warm-muted`）。
- 观察模式下一行提示：`消息会被记录到待校准，不会自动行动`。
- `查看待校准 N` 链接到 `/intake?chat=<id>`。
- 所有文案走 `t()`，不出现 `mandate`、`trigger` 等内部术语。

#### 6.7.8 Mission control

在现有 `workspaces-overview.tsx` 中新增两个独立面板，复用 `MetricCard` 与 `AttentionIssuesPanel` 的视觉语言：

| 面板 | 指标 | 点击 |
|---|---|---|
| 接活 | 今日自动接单 / 待校准 / 误接率 / 重复率 | 跳 `/intake` 或按 source 过滤 |
| 完成 | 进行中 / 一次通过率 / 平均续跑轮数 / 人工介入 / 今日 token 成本 | 跳 Issue 列表或成本面板 |

- 两个面板之间留 `gap-4`，不合并成一个「自主性分数」。
- 面板顶部提供全局「暂停接活」开关（已确认）：只停止 inferred/auto，显式请求不受影响；恢复无需重新校准。
- 指标异常（误接率高、假完成率高）用 `warning` 色 + 文字，不只靠颜色。
- 无数据时显示 `—`，不显示 0 制造噪音。

#### 6.7.9 关键交互流程

1. **首次配置**：项目设置 → 任务来源 → 选择来源 → 接入 → mode 默认 `观察` → 保存。
2. **校准**：观察模式积累决策 → `/intake` 列表 → 逐条采纳/忽略 → 页面顶部显示「已校准 N 条，认可率 M%」。
3. **开启自动**：认可率达到阈值（默认 ≥80% 且 ≥10 条）后显示建议 → 点击 `开启自动` → 确认 Dialog 列出：影响的来源、allow list、规则、每日上限、可随时暂停 → 确认后生效。
4. **暂停/关闭**：`暂停接活` 立即停止 inferred/auto，显式请求不受影响；恢复无需重新校准。
5. **调整自动完成**：修改轮数/预算/停止条件 → 保存 → 下一次 turn 生效；当前 agent 不重启。
6. **Issue 纠偏**：在 Issue 详情编辑 Task Brief 或 Completion Policy；变更记录写入 provenance/审计。

#### 6.7.10 组件与文件

| 组件 / 文件 | 类型 | 说明 |
|---|---|---|
| `pages/project/settings-tab.tsx` | 修改 | 新增 `intake`、`completion` 两个 section |
| `components/projects/intake/IntakePolicyPanel.tsx` | 新增 | 任务来源主面板 |
| `components/projects/intake/SourceCard.tsx` | 新增 | 来源卡片 + mode |
| `components/projects/intake/AllowListEditor.tsx` | 新增 | 成员/机器人 chips |
| `components/projects/intake/RuleTable.tsx` / `RuleDialog.tsx` | 新增 | 规则列表与编辑 |
| `components/projects/intake/PolicyHistoryDialog.tsx` | 新增 | 策略版本历史 |
| `components/completion/CompletionPolicyPanel.tsx` | 新增 | 自动完成面板 |
| `pages/intake/IntakeReviewPage.tsx` | 新增 | 待校准队列页 |
| `components/intake/DecisionList.tsx` | 新增 | 左栏列表 |
| `components/intake/DecisionDetail.tsx` | 新增 | 右栏详情 |
| `components/intake/TaskBriefEditor.tsx` | 新增 | 简报编辑 |
| `components/issue/issue-brief-card.tsx` | 新增 | Issue 详情简报卡 |
| `components/issue/issue-completion-policy-card.tsx` | 新增（P2） | Issue 级自动完成 |
| `components/projects/imbot/chat-row.tsx` | 修改 | 接活模式下拉与覆盖徽章 |
| `pages/workspaces/workspaces-overview.tsx` | 修改 | 接活/完成两个指标面板 |
| `lib/intake-api.ts` | 新增 | 类型化 API 客户端 |
| `i18n/locales/{en,zh}/intake.json` | 新增 | 接活/完成文案；在 `i18n/resources.ts` 注册 |

#### 6.7.11 文案与 i18n

新增 `intake` 命名空间；Issue 相关文案放 `issues`。示例 key：

| key | 中文 | 英文 |
|---|---|---|
| `intake.title` | 任务来源 | Intake policy |
| `intake.mode.off` | 关闭 | Off |
| `intake.mode.observe` | 观察 | Observe |
| `intake.mode.auto` | 自动 | Auto |
| `intake.mode.off.description` | 只响应明确 @ 牛牛的消息 | Only act when addressed |
| `intake.mode.observe.description` | 记录本来会接的任务，不自动执行 | Record would-be work, do not act |
| `intake.mode.auto.description` | 在规则和每日上限内自动接活 | Accept work within rules and daily cap |
| `intake.pending.title` | 待校准 | Needs calibration |
| `intake.pending.empty` | 没有待校准的决策 | No decisions to calibrate |
| `intake.accept` | 采纳并创建任务 | Accept and create task |
| `intake.ignore` | 忽略 | Ignore |
| `intake.ignore.reason.duplicate` | 重复 | Duplicate |
| `intake.calibration.hint` | 建议先观察 10 条决策，再开启自动 | Observe at least 10 decisions before enabling auto |
| `completion.title` | 自动完成 | Completion policy |
| `completion.autohost` | 自动续跑直到完成 | Keep working until done |
| `completion.maxRounds` | 最大续跑轮数 | Max rounds |
| `completion.tokenBudget` | Token 预算 | Token budget |
| `completion.stopOnDecision` | 需要设计决策时停止并提问 | Stop and ask when a decision is needed |
| `completion.requireReview` | 完成后进入人工审查 | Require human review |
| `completion.profile.inherit` | 继承项目默认 | Inherit project default |
| `completion.profile.standard` | 标准 | Standard |
| `completion.profile.fast` | 快速 | Fast |
| `completion.profile.deep` | 深度 | Deep |
| `completion.profile.review` | 高风险需审查 | High-risk review |

**文案原则**：面向用户说「接活 / 完成 / 待校准」，不说 `mandate`、`trigger`、`policy version`；数字与状态用文字 + 颜色双重编码。

#### 6.7.12 响应式与无障碍

- **桌面**：待校准页双栏；设置页 `max-w-3xl`；Mission control 两列。
- **移动（<768px）**：待校准页变列表 + 底部 Drawer；设置页单列；规则表格变卡片列表。
- 所有交互可 Tab 到达，`:focus-visible` 可见；Switch/Select 有 `aria-label`。
- 状态 Badge 必须有文字，不允许只靠颜色。
- 尊重 `prefers-reduced-motion`；不用 `transition-all`。
- 首屏用 skeleton；局部刷新用顶部 progress；禁止整页 spinner 遮罩。
- 空态统一用 `EmptyState`，同一屏最多一个完整空态。

#### 6.7.13 设计系统合规清单

- [ ] 只用 warm token 与语义 token，无十六进制、无 `bg-[#...]`、无任意间距。
- [ ] 按钮走 shadcn `Button`；Dialog/Select/Switch/Table/Textarea 同样走 shadcn。
- [ ] 图标只用 `lucide-react`：`Inbox`、`Bot`、`Github`、`GitPullRequest`、`Eye`、`Zap`、`ZapOff`、`History`、`AlertTriangle`。
- [ ] 所有用户可见文案走 `t()`，中英文 key 同步。
- [ ] Light/Dark 两套 token 均验证；截图对比。
- [ ] 一页一个 primary CTA。
- [ ] 字重只用 400/500/600；数字 `tabular-nums`。
- [ ] 卡片有 `border` 或 `shadow-sm` 边界；阴影不超过 `shadow-lg`。

#### 6.7.14 UI 验收

1. 项目设置出现两个独立 section：任务来源、自动完成；修改一个不影响另一个。
2. 任务来源的 mode 三档有清晰说明；未连接来源不显示 mode。
3. 观察模式下，待校准页能列出 would-do 决策，且执行前后端无 issue/workspace/消息副作用。
4. 采纳决策后创建任务并跳转 Issue；忽略必须选原因。
5. 开启自动前出现确认 Dialog，展示来源、规则、每日上限与校准记录。
6. Issue 详情显示来源徽章、Task Brief、验收项完成度；provenance popover 可追溯。
7. IM chat 行显示接活模式与「已覆盖项目默认」徽章。
8. Mission control 显示「接活」「完成」两个独立面板，指标可点击下钻。
9. 移动端待校准页可用（列表 + Drawer），无横向滚动。
10. 键盘可完成全部主流程；Dark mode 下无对比度问题；无设计系统反模式。

### 6.8 数据模型

新增表（SQLite / PostgreSQL 双写，遵循现有 schema 约定；迁移列用 `addColumnIfNotExists`，不在迁移列上加 CHECK）：

```sql
-- 项目 + 来源的接活配置
CREATE TABLE IF NOT EXISTS intake_settings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_kind     TEXT NOT NULL,              -- im_bot | github | ci | email | webhook
    mode            TEXT NOT NULL DEFAULT 'observe', -- off | observe | auto
    daily_cap       INTEGER NOT NULL DEFAULT 4,
    allow_list_json TEXT NOT NULL DEFAULT '[]',
    bot_list_json   TEXT NOT NULL DEFAULT '[]',
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, source_kind)
);

-- 接活规则
CREATE TABLE IF NOT EXISTS intake_rules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_kind     TEXT NOT NULL,
    name            TEXT NOT NULL,
    instruction     TEXT NOT NULL,
    conditions_json TEXT NOT NULL DEFAULT '{}',
    priority        INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 策略版本：settings/rules 每次变更生成一版
CREATE TABLE IF NOT EXISTS intake_policy_versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_kind  TEXT NOT NULL,
    version      INTEGER NOT NULL,
    snapshot_json TEXT NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, source_kind, version)
);

-- 入站事件（幂等）
CREATE TABLE IF NOT EXISTS intake_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_kind  TEXT NOT NULL,
    source_ref   TEXT NOT NULL,
    event_ext_id TEXT NOT NULL,
    event_kind   TEXT NOT NULL,
    actor_ext_id TEXT NOT NULL DEFAULT '',
    actor_name   TEXT NOT NULL DEFAULT '',
    thread_ext_id TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    received_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(source_kind, source_ref, event_ext_id)
);
CREATE INDEX IF NOT EXISTS idx_intake_events_project ON intake_events(project_id, received_at);

-- 每个事件的决策与执行结果
CREATE TABLE IF NOT EXISTS intake_decisions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id          INTEGER NOT NULL REFERENCES intake_events(id) ON DELETE CASCADE,
    project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    mode_at_decision  TEXT NOT NULL,
    strength          TEXT NOT NULL,            -- explicit | inferred | denied
    decision          TEXT NOT NULL,            -- accept | resume | ignore | unavailable
    executed          INTEGER NOT NULL DEFAULT 0,
    reason_code       TEXT NOT NULL DEFAULT '',
    reason_text       TEXT NOT NULL DEFAULT '',
    matched_rule_ids_json TEXT NOT NULL DEFAULT '[]',
    policy_version    INTEGER NOT NULL DEFAULT 0,
    issue_id          INTEGER,
    workspace_id      INTEGER,
    would_do_json     TEXT NOT NULL DEFAULT '{}',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_intake_decisions_pending ON intake_decisions(project_id, executed, decision);

-- Task Brief（1:N，追加式版本；当前版本 = max(version)）
CREATE TABLE IF NOT EXISTS task_briefs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id          INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    version           INTEGER NOT NULL,
    goal              TEXT NOT NULL DEFAULT '',
    acceptance_json   TEXT NOT NULL DEFAULT '[]',
    scope_in_json     TEXT NOT NULL DEFAULT '[]',
    scope_out_json    TEXT NOT NULL DEFAULT '[]',
    delivery_json     TEXT NOT NULL DEFAULT '{}',
    provenance_json   TEXT NOT NULL DEFAULT '{}',
    intake_decision_id INTEGER,
    policy_version    INTEGER NOT NULL DEFAULT 0,
    created_by        INTEGER,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(issue_id, version)
);
CREATE INDEX IF NOT EXISTS idx_task_briefs_issue ON task_briefs(issue_id, version);

-- 自动完成（项目默认）
CREATE TABLE IF NOT EXISTS completion_settings (
    project_id       INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    autohost_enabled INTEGER NOT NULL DEFAULT 1,
    max_rounds       INTEGER NOT NULL DEFAULT 20,
    token_budget     INTEGER NOT NULL DEFAULT 0, -- 0 = 不限
    stop_on_decision INTEGER NOT NULL DEFAULT 1,
    require_review   INTEGER NOT NULL DEFAULT 0,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 自动完成命名配置（项目级，可被 issue 指定）
CREATE TABLE IF NOT EXISTS completion_profiles (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    autohost_enabled INTEGER NOT NULL DEFAULT 1,
    max_rounds       INTEGER NOT NULL DEFAULT 20,
    token_budget     INTEGER NOT NULL DEFAULT 0,
    stop_on_decision INTEGER NOT NULL DEFAULT 1,
    require_review   INTEGER NOT NULL DEFAULT 0,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);
```

复用/扩展现有表：

- `issues.external_source / external_id / external_url`：provenance；
- `issue_checklists`：执行清单与验收项物化；新增 `kind`（`task` / `acceptance`）和 `brief_id` 列；acceptance 由 Brief 写入，task 保持手工/agent 进度；
- `issues`：新增 `completion_profile_id` 可空列；空值继承项目默认；
- `issue_exec_events`：完成轨迹与人工介入；
- `im_bot_*`：IM Source 的配对、thread、观察记录；
- `workspaces`：执行单元，不改 1:1 issue 约束。

迁移注意：`issue_checklists.kind/brief_id`、`issues.completion_profile_id` 是加到既有表上的列，使用 `addColumnIfNotExists`，不在 schema 中为迁移列加 CHECK；SQLite/PostgreSQL 双写并跑 `make schema-diff`。

### 6.9 API 与 MCP

HTTP：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET/PUT | `/api/projects/:id/intake/settings` | 读/写接活配置 |
| GET | `/api/projects/:id/intake/decisions?executed=0&decision=accept` | 待校准队列 |
| POST | `/api/projects/:id/intake/decisions/:did/accept` | 采纳 dry-run 决策 |
| POST | `/api/projects/:id/intake/decisions/:did/ignore` | 忽略并记录原因 |
| GET/PUT | `/api/projects/:id/completion/settings` | 自动完成项目默认 |
| GET/POST | `/api/projects/:id/completion/profiles` | 列出/新建命名配置 |
| PATCH/DELETE | `/api/projects/:id/completion/profiles/:pid` | 编辑/删除命名配置 |
| GET | `/api/issues/:id/brief` | Task Brief |
| PATCH | `/api/issues/:id` | 扩展 `completion_profile_id`（空 = 继承） |
| PATCH | `/api/imbot/chats/:chatid` | 增加 `intake_mode` 字段 |
| POST | `/api/integrations/github/webhook/:binding` | 团队版 GitHub 入站（签名校验） |

MCP 工具：

- `intake_get_settings(project_id)`
- `intake_set_mode(project_id, source_kind, mode)`
- `intake_list_pending(project_id)`
- `intake_accept_decision(decision_id)`
- `intake_ignore_decision(decision_id, reason)`
- `completion_set_policy(project_id, ...)`
- `completion_list_profiles(project_id)`
- `completion_upsert_profile(project_id, ...)`
- `issue_set_completion_profile(issue_id, profile_id_or_null)`
- `issue_get_brief(issue_id)`

### 6.10 GitHub Source（首批新增）

入站事件：

- `issue_comment`：PR/Issue 评论；
- `pull_request_review` / `pull_request_review_comment`：review 与行内评论；
- `check_run` / `workflow_run`：CI 结果；
- `pull_request` closed/merged：结束 thread 订阅。

Thread 映射：

- PR number → `thread_ext_id`；
- 已绑定则续接同一 workspace；
- 未绑定则由 policy 决定是否新建任务。

传输方式（已确认）：

- **个人版 / 本地优先**：轮询。复用调度器按 `since`/ETag 拉取通知与 PR 事件，无需公网。
- **团队版**：Webhook，签名校验，事件幂等。
- 两种方式产出同一个 `Trigger`，进入同一条 Intake 管线。

权限：

- 使用现有 GitHub integration 凭证；
- 只读事件与评论；写回（回复 PR）需显式开启，且遵守现有 egress/授权规则。

---

## 7. 迁移与兼容

1. **现有 IM 行为不变**：
   - `agent_mode=command` → `intake_mode=off`（显式请求仍工作）；
   - `agent_mode=observe` → `intake_mode=auto`（保留当前“预算内行动”行为）；
   - 新 chat 默认 `observe`（dry-run）。
   - 这是本设计唯一的行为语义变化点，需审核确认（§12-1）。
2. **`im_bot_inbox` 与 `intake_events`**：Phase 1 双写；Phase 2 读切换到 `intake_events`；`im_bot_inbox` 保留一个版本后删除。
3. **`goal_condition`**：存量 issue 不回填、不改写，避免改变 autohost 行为；新建任务由 Task Brief 渲染。
4. **双数据库**：SQLite 与 PostgreSQL schema 同步修改，跑 `make schema-diff`；迁移使用现有 `addColumnIfNotExists` 约定。
5. **sqlc**：新增查询后跑 `make sqlc`，检查生成文件。

---

## 8. 分阶段落地

| 阶段 | 内容 | UI 交付 | 行为变化 |
|---|---|---|---|
| Phase 0 | 概念拆分、设置项、指标骨架、schema | 项目设置两个只读 section 骨架；Mission control 占位 | 无（只读/配置） |
| Phase 1 | 抽出 `IntakeService`、IM adapter、dry-run 队列 | 任务来源页、待校准队列页、IM chat 接活模式 | observe 可只记录 |
| Phase 2 | Task Brief（追加式版本）、Completion Policy、命名 profile、autohost 校验 | 自动完成页 + profile 管理、Issue 详情 Task Brief 卡片 + profile 选择、来源徽章 | 新任务完成质量提升 |
| Phase 3 | GitHub Source（轮询）、thread 绑定、actor 授权 | Source 接入卡、GitHub 事件详情、Mission control 两面板 | 多源接活 |
| Phase 4 | 团队版 webhook、邮件/通用 webhook、指标面板 | 策略历史、校准引导、批量忽略、移动端优化 | 扩大覆盖 |

每阶段独立可上线；Phase 0/1 不改变现有 agent 行为。UI 与后端同一阶段交付，不先上线「有 API 无入口」的半成品。

---

## 9. 测试

**单元**：

- Source 归一化：IM / GitHub 事件 → Trigger；
- 去重：同 event 重复投递只产生一条 `intake_events`；
- 授权：explicit / inferred / denied 三档，未列名单不产生任务；
- dry-run：`executed=0`，不创建 issue/workspace、不发消息；
- Task Brief 版本：追加 v2 后当前版本为 v2，v1 仍可查；acceptance 行刷新，手工 `kind='task'` 行不变；
- Task Brief 渲染：goal_condition 与 acceptance 一致；
- Completion：验收项未完成时不接受 `[AUTOHOST_DONE]`；达到 max rounds 转 attention；
- Completion profile 解析：issue 指定 > 项目默认；null 继承；变更下一个 turn 生效；删除被引用 profile 被拒绝；
- 迁移：command → off、observe → auto。

**集成**：

- IM 消息 → decide → issue+workspace → deliver；
- 同一 thread 后续消息 → 续接原 workspace，不新建；
- GitHub PR 评论 → 绑定 thread → 续接；
- 重启/重投 → 恰好一个任务；
- dry-run 队列 Accept → 创建任务并写 brief。

**端到端**：

1. IM 一句话 → 建任务 → autohost 完成 → 结果回投 IM；
2. PR 评论 → 续接同一 workspace → 完成后回复 PR；
3. observe 模式下自动分析只产生 would-do，不产生副作用；
4. 未授权 actor 的消息被记录为 denied，不产生任务。

**前端**：

- 设置页：任务来源/自动完成两个 section 独立渲染；mode 三档说明；桌面分段、移动 Select；未连接来源不显示 mode；全局暂停只停 inferred/auto；保存/校验/未保存离开确认。
- 待校准队列：列表筛选、强度 Badge、详情编辑、采纳后跳转、忽略必须选原因、批量忽略、空态/加载/错误。
- Issue 详情：Task Brief 卡片、验收项勾选同步、来源徽章与 provenance popover、非 intake 任务不显示卡片。
- IM chat 行：接活模式下拉、覆盖项目默认徽章、观察提示。
- Mission control：两个面板独立、指标下钻、异常态文字 + 颜色。
- 响应式：<768px 列表 + Drawer、设置页单列、无横向滚动。
- 无障碍：键盘走完主流程、`:focus-visible`、Badge 有文字。
- 设计系统：`pnpm lint` + `tsc -b`；Dark mode 截图对比；grep 确认无十六进制/任意间距/`transition-all`。

---

## 10. 验收

1. Intake 与 Completion 可独立配置，修改一个不影响另一个。
2. dry-run 决策产生零副作用，且可在队列中查看、采纳、忽略。
3. `off` 模式下，allow list 内 actor 的显式消息仍能创建/续接任务。
4. 未列名单 actor 的消息按 policy 记录为 inferred/denied，不越权建任务。
5. 已绑定 thread 的后续评论续接同一 workspace，不重新分诊。
6. Intake 创建的任务，`goal_condition` 来自 Task Brief，验收项与 checklist 的 acceptance 行一致；手工 task 行不受影响。
7. 验收项未完成时 autohost 不宣布完成；达到 max rounds 或 token budget 时转人工并提一个结构化问题。
8. 同一事件重复投递或服务重启后，只产生一个任务。
9. 接活与完成的指标分别可见：接单准确率/误接/重复 vs 轮数/假完成/HITL/成本。
10. 现有 IM command/observe 行为在迁移后保持不变。
11. 项目设置出现两个独立入口：任务来源、自动完成；修改一个不影响另一个（UI 验收详见 §6.7.14）。
12. 观察模式下待校准队列可完成「查看 → 采纳/忽略 → 进入 Issue」的完整闭环，且采纳前无任何副作用。
13. Issue 详情可追溯来源与 Task Brief；IM chat 行可查看并覆盖接活模式；Mission control 分开展示接活与完成指标。
14. 移动端、键盘操作、Dark mode 与设计系统合规检查全部通过。
15. Task Brief 每次编辑追加新版本，历史可查；旧 acceptance 行失效，手工执行清单保留。
16. Issue 指定 completion profile 生效，UI 显示「来源：项目默认 / 配置名」；删除被引用的 profile 被拒绝。
17. 待校准队列有全局 `/intake` 入口，项目设置与 Mission control 可跳转；桌面分段控件、移动 Select 均可切换 mode。
18. 全局「暂停接活」后自动任务停止、显式请求仍可创建/续接；恢复后无需重新校准。
19. intake 任务的 Issue 详情 Task Brief 卡默认展开；手工任务不显示该卡片。

---

## 11. 风险

| 风险 | 缓解 |
|---|---|
| 外部消息 prompt injection | 权限由代码强制；外部文本只作数据；高风险动作仅 explicit 可触发 |
| 事件风暴 / 重复任务 | 幂等 + daily cap + 批处理 + thread 绑定 |
| 过度自动化 / 噪音 | 新 Source 默认 observe；would-accept 认可率达标后再 auto |
| 成本失控 | Completion token budget；Intake daily cap；按 source 统计 |
| 双数据库迁移漂移 | `make schema-diff`；迁移列不用 CHECK；分阶段上线 |
| GitHub 轮询限流 | since/ETag、退避、只订阅需要的 repo 与事件 |
| 语义迁移误伤 | command→off、observe→auto 的映射需审核；提供回滚开关 |

---

## 12. 决策记录（已确认）

### 已确认

1. **`observe` 语义迁移**：方案 A。现有 `agent_mode=observe` 的 chat 迁移为 `auto`，保留「预算内直接行动」；新 chat 默认 `observe`（dry-run）。
2. **GitHub 入站传输**：个人版轮询（`since`/ETag，无需公网）；团队版 webhook。
3. **个人版是否暴露 `auto`**：暴露，但要求先完成 dry-run 校准（默认 ≥10 条、认可率 ≥80%）并二次确认；可随时暂停。
4. **命名**：面向用户用「任务来源 / 自动完成」；代码层用 Intake / Completion。
5. **Task Brief 存储**：方案 A。新建 `task_briefs`，1:N 追加式版本；`issues.goal_condition` 是渲染缓存；`issue_checklists` 保留，acceptance 物化为 `kind='acceptance'`，手工进度项为 `kind='task'`。
6. **Issue 级 Completion 覆盖**：方案 C。项目默认 + 命名 profile + issue 指定；解析规则 `issue profile > 项目默认`，不做逐字段 merge。

### UI 相关（已确认）

7. **待校准队列位置**：全局 `/intake` 页面 + 项目/Mission control 入口；项目设置内只放入口和计数，不重复完整队列。
8. **mode 控件形态**：桌面用分段控件，移动用 Select；两套共用同一组文案和状态。
9. **Task Brief 默认展开**：仅 intake 创建的任务默认展开；手工任务不显示该卡片。
10. **全局「暂停接活」开关**：v1 提供，放在项目设置顶部与 Mission control；只停止 inferred/auto，显式请求仍工作，恢复无需重新校准。

---

## 13. 明确不做

- 不做云端存算分离与远程容器执行。
- 不做跨实例 agent-to-agent。
- 不替换 agentproxy、workspace、worktree、kanban。
- 不做自动 merge、自动发布、自动对外沟通。
- 不做无上限自主；不把 Intake 和 Completion 合成单一 autonomy 开关。
- 不回溯改写存量 issue 的 goal_condition 或执行轨迹。

---

## 14. 实现状态与代码索引（2026-11-04）

> 本节记录设计与实现的对应关系，供后续维护与运维定位；不改变上文的设计结论。

### 14.1 分阶段状态

| 阶段 | 状态 | 主要落点 |
|---|---|---|
| Phase 0：schema / 契约骨架 / 指标 | 已实现 | `server/internal/store/schema.sql`、`schema_postgres.sql`、`store/migrate.go`；`server/internal/api/intake.go`、`completion.go` |
| Phase 1：IntakeService + IM adapter + dry-run 队列 | 已实现 | `server/internal/intake/`、`server/internal/service/imbot_intake.go`、`server/internal/api/intake.go`、`server/web/src/pages/intake/` |
| Phase 2：Task Brief + Completion Policy + profile | 已实现 | `server/internal/intake/brief.go`、`server/internal/completion/policy.go`、`server/internal/service/completion.go`、`server/internal/agentproxy/autohost.go` |
| Phase 3：GitHub Source（个人版轮询） | 已实现 | `server/internal/githubsource/`、`server/internal/service/github_source.go`、`github_reply.go` |
| Phase 4：团队版 webhook / 邮件 / 通用 webhook / 指标面板 | 已实现 | `server/internal/api/integration_webhook.go`、`server/internal/service/webhook_source.go`、`server/internal/service/mission_control.go`、`server/web/src/components/workspaces/mission-control-panels.tsx` |
| UI：任务来源 / 自动完成 / 待校准 / Issue Task Brief / Mission control | 已实现 | `server/web/src/components/projects/intake/`、`components/completion/`、`components/issue/issue-completion-policy-card.tsx`、`pages/project/settings-tab.tsx` |
| 测试 | 已实现 | `server/internal/service/intake_completion_e2e_test.go`（§9/§10 端到端）、`server/internal/agentproxy/autohost_policy_test.go`、`server/internal/store/migrate_intake_completion_test.go` |

### 14.2 与设计的已知差异 / 待办

1. **`im_bot_inbox` 读切换尚未执行**。本版本按 §7.2 做 Phase 1 双写，读路径仍是 `im_bot_inbox`（`server/internal/service/imbot_inbound.go` 的早期去重守卫）。读切换、保留一版、删除的步骤见运行手册 §5.2；必须在下一版本执行。
2. **来源配置默认行已自动回填**。服务启动时为缺少配置的项目写入每个来源的 `intake_settings` 默认行（`observe`、`daily_cap=4`、空名单），新建项目在创建时写入；界面显示的模式与 `imbotPolicy.LoadPolicy` 解析的实际模式因此一致（此前无存储行时 UI 显示 `observe`、运行期按 `off`，已修复）。
3. **团队版 GitHub `webhook_secret` 尚无表单入口**。SPA 的 GitHub 凭据对话框只渲染 PAT；团队版需通过 API 写入（见运行手册 §4.2）。后续可在凭据对话框补充可选 `webhook_secret` 字段。
4. **GitHub Enterprise BaseURL 未暴露配置**。个人版轮询固定 `https://api.github.com`；`GithubSourceService.SetPollerBaseURL` 目前仅供代码/测试使用。
5. **实现新增了 §6.8 未列出的 4 张运行期表**：`intake_project_state`（项目暂停开关）、`intake_daily_usage`（daily cap 原子占位，TOCTOU 修复）、`github_source_poll_state`（轮询游标/退避）、`github_thread_bindings`（PR/issue thread 绑定与续接）。它们不改变设计语义，运维说明见运行手册 §5.4。
6. **`im_bot_chats.intake_mode` 新增 `inherit` 值**：表示跟随项目+来源设置；不是迁移回填值，只由用户主动选择。

### 14.3 运维文档

- 上线、迁移、部署差异、策略版本、待校准、暂停、指标、回滚与验收清单：
  [`docs/operations/intake-completion-rollout.md`](../../operations/intake-completion-rollout.md)
- 团队版部署入口：`niuniu-enterprise` 仓库 `docs/ai-context/production-deploy.md`。
