# 牛牛运营 Bot：账号级 AI 运营团队设计

日期：2026-11-06 ｜ 状态：待审核（Draft for review）
参考：Grok Bot ｜ 第一个需求：小红书 + 微信公众号自动化运营

## 0. 一句话

在牛牛现有 Epic / Workspace / IM Bot / Intake / Integration 之上，增加一层 **Ops Bot（运营 Bot）**：运营总监 Bot 管策略与日历，每个平台账号一个账号 Bot 管生产、发布、互动建议与复盘；主 UI 是对话，像对接 IM 一样完成配置和审批。

---

## 1. 背景与目标

### 1.1 现有底座

牛牛已经具备：

- **场景**：`wechat-mp`（公众号官方 API 闭环）、`social-ops`（内容日历/排期/周报/舆情）、`content-marketing`（选题/文案/审校/发布清单/复盘）。
- **编排**：通用运营闭环（A 采集选题 / B 内容 / C 复盘回流）、Epic、`exec_wave`、autohost、agent-to-agent 消息、blackboard。
- **交互**：IM Bot 渠道（飞书/钉钉/企微/微信/Telegram）、chat 配对、卡片回调、Intake/待校准、注意力收件箱。
- **外部能力**：integration provider / MCP / 数据源 / 凭据代理 / egress 审计。
- **治理**：workspace/worktree 隔离、completion profile、harness 门禁、Mission control、token/成本分析。

### 1.2 参考 Grok Bot

Grok Bot 的强项是：常驻 AI 同事、每 Bot 一台云端电脑、computer use、多 Bot 团队协作、跟岗学习、开箱集成。牛牛不复制其云端重资产模式，而是用现有本地优先 + 多引擎 + 工程治理底座，做**账号级 AI 运营团队**。

### 1.3 目标

- **G1 双平台闭环**：微信公众号 + 小红书，选题、生产、审校、发布、互动建议、复盘全流程。
- **G2 账号级 Bot**：每个账号一个独立 Bot，凭据、浏览器会话、人设、预算、指标互相隔离。
- **G3 运营总监**：跨账号选题、日历、预算、复盘协调。
- **G4 内容全形态**：文案 + 封面/配图 + 短视频自动生成。
- **G5 品牌一致性**：品牌知识库 + 历史内容/爆款库 + 实时热点作为创作依据。
- **G6 安全默认**：发布、互动回复、视频成片三类动作默认人工确认；合规门禁先于人工确认。
- **G7 对话式 UI**：像对接 IM 一样，通过聊天完成 Bot 创建、账号授权、策略/预算配置和审批。
- **G8 可治理**：账号级预算、审批审计、风险暂停、跨账号隔离。

### 1.4 非目标

- 不做新的聊天系统；复用 imbot。
- 不做新的执行引擎；复用 workspace/agentproxy/Epic。
- 不代持平台账号密码；不把密钥放进对话或 prompt。
- 不做批量关注/点赞/私信等高风险互动。
- 不承诺小红书官方 API；XHS 走用户自有账号的浏览器自动化。
- 不做自动通过审批；默认必须人工确认。
- 不做模型训练。

---

## 2. 已确认决策

| # | 决策 | 结论 |
|---|---|---|
| 1 | 自动化深度 | 全自动运营 + 发布前人工确认开关 |
| 2 | 互动范围 | 发布可由系统自动执行（默认仍走人工确认）；评论/私信只生成建议回复，人工确认后发送 |
| 3 | Bot 形态 | C：账号级专家 Bot + 运营总监 Bot |
| 4 | 内容生产 | 文案 + 封面/配图 + 短视频全自动生成 |
| 5 | 内容依据 | 品牌知识库 + 历史内容/爆款库 + 实时热点 |
| 6 | 浏览器 | 个人版本地；团队版云端服务器；按账号隔离 |
| 7 | 审批默认 | 发布、回复、视频成片三类默认全部人工确认 |
| 8 | 账号规模 | N 个账号，按需添加 |
| 9 | 节奏 | 固定每周 1-2 篇 + 热点/活动驱动，两种并存 |
| 10 | 成功指标 | 按账号配置目标（涨粉/转化/品牌） |
| 11 | 审批人 | 每账号绑定运营人确认；总监可代审并记录 |
| 12 | 预算 | 每账号月度总预算上限，超限暂停并通知 |

> “全自动”指系统具备端到端执行能力；默认审批策略仍要求发布、回复、视频成片三类人工确认。运营人可按账号关闭某类确认开关，关闭后系统自动执行。

---

## 3. 领域模型与边界

### 3.1 三个概念

| 概念 | 定义 | 不承担 |
|---|---|---|
| **Ops Bot（运营 Bot）** | 一个平台账号的运营身份：账号、平台、人设、凭据引用、目标、节奏、审批策略、预算、知识范围 | 不是聊天渠道，不是执行引擎 |
| **IM Bot（现有）** | 消息渠道与交互面：收发、配对、卡片、回调 | 不持有平台账号，不做运营决策 |
| **Platform Account** | 公众号 / 小红书账号本身 | 不是 Bot |

### 3.2 四层职责

```
IM 消息 / 对话
    │
    ▼
IM Bot（imbot）—— 只负责“在哪对话”
    │  HandleInbound → IntakeService → 路由
    ▼
Ops Bot（新）—— 负责“谁在运营”
    │
    ├── Epic / Workspace（现有）—— 负责“在哪干活”
    │
    └── PlatformAdapter
          ├── WeChatOfficialAdapter（integration / MCP）
          └── XhsBrowserAdapter（本地 / 团队版云端浏览器）
```

### 3.3 和 imbot 的关系

- **imbot 是前门，Ops Bot 是身份。** 不新建聊天系统。
- 一个 Ops Bot 可绑定一个或多个 IM chat/thread；一个 chat 可按 thread/topic 路由到不同 Ops Bot。
- 复用 `HandleInbound`、`IntakeService`、`RouteInProject`、`imbot_dispatch`、`ask_user` 卡片。
- 对话式配置：用户说“接入公众号账号 A”“每周发 2 篇”“发布前要我确认”，Bot 用结构化问题补全并创建 Ops Bot。
- 对话式审批：发布/回复/视频成片以卡片形式推送到 IM，运营人点“通过 / 驳回 / 修改”。
- 命名：对外叫「运营 Bot」「公众号 Bot」「小红书 Bot」；现有 imbot 叫「消息渠道 / IM 渠道」。

### 3.4 和外部系统集成、数据集成的关系

- Ops Bot **不持有平台密钥，不直接写 HTTP**；只保存 `credential_ref` 和能力引用。
- **外部系统集成（动作）**：公众号发布/草稿/素材/用户分析走现有 integration provider 或 `wechat-mp` MCP；外发经过 egress/授权/审计。
- **数据集成（数据）**：热点、竞品、平台指标走现有 data source / knowledge base / `run_data_query`，复用 scope 与读写权限。
- **小红书浏览器**：新增 `PlatformAdapter` 的浏览器实现，不是新集成框架；它是执行后端，账号级隔离 profile/cookie。
- **统一抽象**：`PlatformAdapter` 下两个实现；后续加抖音/B站只新增 adapter。

---

## 4. 总体架构

### 4.1 组件

```
server/internal/opsbot/
  service.go        # OpsBot CRUD、绑定、暂停/恢复
  director.go       # 总监：日历、选题池、跨账号协调、预算分配
  account.go        # 账号 Bot：单账号流水线
  approval.go       # 审批状态机、卡片、超时
  budget.go         # 月度预算、消耗、暂停
  calendar.go       # 固定节奏 + 热点驱动
  metrics.go        # 账号指标归一化
  adapters/
    adapter.go      # PlatformAdapter 接口
    wechat.go       # 官方 API / MCP
    xhs.go          # 浏览器 Worker
  browser/
    worker.go       # 本地/云端 Worker 管理
    session.go      # 账号级 profile / 登录态
```

### 4.2 关键流程

```
IM 对话
  → imbot HandleInbound
  → IntakeService（去重、授权、路由）
  → OpsBotService（识别目标 Ops Bot）
  → Director / Account Epic + control workspace
  → 内容 issue + workspace
  → ContentPipeline（文案/图片/视频/合规）
  → ApprovalService（等待运营人）
  → PlatformAdapter（发布/回复/取数）
  → OpsMetrics / blackboard / 知识库回流
```

### 4.3 与现有原语的映射

| 新能力 | 现有原语 |
|---|---|
| 总监 Bot / 账号 Bot | Epic + 控制工作空间 |
| 内容任务 | issue + workspace + autohost |
| Brief / 验收 | `task_briefs` + `issue_checklists` |
| 完成策略 | completion profile |
| 流水线阶段 | 看板列 + `advance_issue` |
| 合规门禁 | harness `ai_judge` |
| 固定节奏 | scheduler / cron |
| 热点驱动 | info-radar / social-ops / Intake |
| 跨账号协作 | `send_workspace_message` + blackboard |
| 审批 | imbot `ask_user` / permission 卡片 |
| 指标 | Mission control |

---

## 5. 运营总监 + 账号 Bot

### 5.1 总监 Bot

- 输入：品牌目标、账号矩阵、总预算、品牌知识库。
- 维护：共享内容日历、选题池、爆款库、跨账号去重。
- 决策：固定节奏 vs 热点加更；选题分配；预算分配。
- 复盘：汇总各账号指标，更新爆款库和下一轮选题。
- 协调：通过 `send_workspace_message` 和 blackboard 与账号 Bot 通信。

### 5.2 账号 Bot

- 账号身份、平台、人设、受众、平台语气。
- 账号级凭据引用、浏览器会话、平台适配。
- 单账号内容生产、发布、互动建议、数据复盘。
- 账号级预算与审批。
- 向总监汇报进展和异常。

### 5.3 两种节奏

| 模式 | 触发 | 总监动作 |
|---|---|---|
| 固定节奏 | scheduler / cron | 按账号目标生成本周选题和内容任务 |
| 热点驱动 | info-radar / social-ops 监测 | 评估相关性、账号适配度、预算；决定加更或并入下周 |

两种模式共用同一日历和选题池；总监负责去重和优先级。

### 5.4 预算分配

- 总监持有总预算，按账号活跃度/目标分配月度额度。
- 账号 Bot 在视频生成前、发布前检查额度。
- 超限暂停该账号自动动作，通知运营人；总监可调整。
- 预算消耗按文本/图片/视频分项记录。

---

## 6. 内容生产流水线

每篇内容是账号 Epic 下的子 issue，走现有看板列：

```
选题/Brief → 文案 → 视觉（封面/配图）→ 视频 → 合规审校
→ 运营人确认 → 发布 → 互动建议 → 数据复盘 → 回流
```

| 阶段 | 产出 | 复用能力 |
|---|---|---|
| 选题/Brief | Task Brief：目标、平台、受众、形式、验收标准 | `task_briefs` + 品牌 KB + 爆款库 + 热点 |
| 文案 | 公众号长文 / 小红书笔记（钩子、正文、标签、CTA） | 现有写作/内容场景 |
| 视觉 | 封面、配图、平台尺寸适配 | media-studio / 图片生成 |
| 视频 | 脚本、分镜、TTS、画面、字幕、合成 | 现有视频流水线 / 可选 video-gen MCP |
| 合规审校 | 事实溯源、广告法、平台红线、版权 | harness `ai_judge` 门禁 |
| 运营人确认 | 发布/回复/视频成片卡片 | imbot `ask_user` |
| 发布 | 公众号官方 API；小红书浏览器 Worker | `PlatformAdapter` |
| 互动建议 | 评论/私信分类 + 建议回复 | 浏览器 Worker / 官方 API |
| 数据复盘 | 阅读、互动、涨粉、转化 | 官方 API / 浏览器后台 / 数据源 |
| 回流 | 爆款拆解、选题机会、受众反馈 | blackboard / 知识库 / 记忆 |

### 6.1 Task Brief

```go
type OpsContentBrief struct {
    Platform   string   // wechat_mp | xiaohongshu
    AccountID  string
    Goal       string   // 涨粉 | 转化 | 品牌
    Audience   string
    Format     string   // article | note | video
    Topic      string
    Acceptance []string
    SourceRefs []string // 知识库/热点/爆款引用
}
```

- Brief 由总监或账号 Bot 生成，运营人可在对话中修改。
- Brief 落到 `task_briefs`，验收项同步到 `issue_checklists`。
- 视频任务额外记录时长、比例、字幕、封面要求。

### 6.2 质量门禁

1. **事实/合规门禁**：无来源的关键断言、广告法绝对化用语、平台红线、版权风险 → 阻断。
2. **品牌一致性**：语气、术语、视觉规范。
3. **平台适配**：尺寸、时长、标题长度、标签数量。
4. **人工确认**：三类动作默认停下等人。

---

## 7. PlatformAdapter 与浏览器 Worker

### 7.1 接口

```go
type PlatformAdapter interface {
    Capabilities() CapabilitySet
    EnsureSession(ctx, account) error
    Publish(ctx, payload) (Receipt, error)
    FetchMetrics(ctx, range) (Metrics, error)
    ListEngagements(ctx, cursor) ([]Engagement, error)
    Reply(ctx, engagementID, text) (Receipt, error)
    HealthCheck(ctx) error
}
```

### 7.2 WeChatOfficialAdapter

- 走现有 integration / `wechat-mp` MCP。
- 能力：草稿、素材、发布/定时、用户分析、图文统计。
- 凭据：用户自有公众号的 AppID/AppSecret 或访问自有 MCP 的 token。
- 发布前走合规门禁 + 人工确认。

### 7.3 XhsBrowserAdapter

- 浏览器 Worker：个人版本机；团队版云端服务器。
- 能力：发布图文/视频笔记、读取创作者后台数据、读取评论/私信、发送经审批的建议回复。
- 账号级隔离：
  - 个人版：每账号独立 browser profile 目录。
  - 团队版：每账号独立容器/VM + 租户隔离。
- 约束：
  - 同一账号同一时间只允许一个操作租约。
  - 独立 cookie/localStorage/指纹；不共享登录态。
  - 任务队列 + 频率限制 + 随机化节奏。
  - 发布/回复后保存截图或回执证据。
  - 登录态失效时生成“重新登录”任务，通知运营人扫码。
  - UI 改版导致 selector 失效时，降级为发布包 + 人工发布，并告警。

### 7.4 本地 / 云端对比

| 维度 | 个人版 | 团队版 |
|---|---|---|
| 运行位置 | 本机 | 云端服务器 |
| 隔离单位 | 每账号 profile 目录 | 每账号容器/VM |
| 登录态 | 本机加密存储 | 加密存储 + 租户隔离 |
| 出网 | 本机网络 | 固定出口 + 域名 allowlist |
| 并发 | 本机资源 | 账号配额 |
| 审计 | 本地 | 租户级 |

---

## 8. 凭据、安全与合规

- Ops Bot 只存 `credential_ref`，不存明文。
- 公众号：一次性安全链接提交 AppID/AppSecret 或 MCP token。
- 小红书：不收集密码；运营人扫码登录，会话加密保存。
- 对话中永不出现密钥；Bot 只说“请点链接 / 请扫码”。
- 凭据按账号隔离，不能被其他账号 Bot 引用。
- 所有外发动作经 egress/授权/审计；外部文本只作数据，不构成指令。
- 小红书启用时明确提示 ToS/封号风险，默认关闭，运营人显式开启。
- 账号级 kill switch：一键暂停该账号所有自动动作。
- 异常检测：登录态频繁失效、发布失败率、限流、封禁提示 → 立即暂停并通知。

---

## 9. 审批模型

三类审批，默认全部开启：

| 类型 | 卡片内容 | 操作 |
|---|---|---|
| 发布 | 标题、正文摘要、封面/视频预览、平台、时间、合规结论 | 通过 / 驳回 / 修改 |
| 互动回复 | 原评论/私信、分类、建议回复、风险提示 | 通过 / 修改后发送 / 驳回 |
| 视频成片 | 成片预览、时长、字幕、封面、平台尺寸 | 通过 / 驳回 / 重新生成 |

- 审批人 = 账号绑定的运营人；总监可代审，记录代审人。
- 超时提醒；不自动通过。
- 审批记录进入审计：内容版本、审批人、时间、操作、理由。
- 审批点与合规门禁独立：合规先过，人才确认。

---

## 10. 数据模型

新增表（SQLite/PostgreSQL 双写；既有表迁移列用 `addColumnIfNotExists`，不加 CHECK）：

```sql
CREATE TABLE IF NOT EXISTS ops_bots (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type           TEXT NOT NULL DEFAULT 'user',
    owner_id             INTEGER NOT NULL DEFAULT 0,
    role                 TEXT NOT NULL DEFAULT 'account', -- director | account
    platform             TEXT NOT NULL DEFAULT '',        -- wechat_mp | xiaohongshu
    account_ref          TEXT NOT NULL DEFAULT '',
    display_name         TEXT NOT NULL DEFAULT '',
    persona_json         TEXT NOT NULL DEFAULT '{}',
    credential_ref       TEXT NOT NULL DEFAULT '',
    knowledge_scope_json TEXT NOT NULL DEFAULT '{}',
    goals_json           TEXT NOT NULL DEFAULT '{}',
    cadence_json         TEXT NOT NULL DEFAULT '{}',
    approval_policy_json TEXT NOT NULL DEFAULT '{}',
    budget_json          TEXT NOT NULL DEFAULT '{}',
    status               TEXT NOT NULL DEFAULT 'active',  -- active | paused | disabled
    director_bot_id      INTEGER,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ops_bot_links (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id         INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    im_chat_id     INTEGER,
    epic_issue_id  INTEGER,
    workspace_id   INTEGER,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ops_browser_workers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id          INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    backend         TEXT NOT NULL DEFAULT 'local', -- local | cloud
    endpoint        TEXT NOT NULL DEFAULT '',
    container_ref   TEXT NOT NULL DEFAULT '',
    profile_ref     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'offline',
    last_heartbeat  TIMESTAMP,
    risk_state      TEXT NOT NULL DEFAULT 'normal',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ops_calendar_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id      INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    planned_at  TIMESTAMP NOT NULL,
    topic       TEXT NOT NULL DEFAULT '',
    format      TEXT NOT NULL DEFAULT 'note',
    source      TEXT NOT NULL DEFAULT 'fixed', -- fixed | trend
    status      TEXT NOT NULL DEFAULT 'planned',
    issue_id    INTEGER,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ops_publish_records (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id         INTEGER NOT NULL,
    bot_id           INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    platform         TEXT NOT NULL,
    content_version  INTEGER NOT NULL DEFAULT 1,
    idempotency_key  TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    receipt          TEXT NOT NULL DEFAULT '',
    url              TEXT NOT NULL DEFAULT '',
    screenshot_path  TEXT NOT NULL DEFAULT '',
    published_at     TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(bot_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS ops_approvals (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id          INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    issue_id        INTEGER,
    type            TEXT NOT NULL, -- publish | reply | video
    payload_json    TEXT NOT NULL DEFAULT '{}',
    preview_json    TEXT NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending', -- pending | approved | rejected | expired
    operator_id     INTEGER,
    decided_at      TIMESTAMP,
    reason          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ops_engagement_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id           INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    platform         TEXT NOT NULL,
    type             TEXT NOT NULL, -- comment | dm
    ext_id           TEXT NOT NULL,
    author           TEXT NOT NULL DEFAULT '',
    content          TEXT NOT NULL DEFAULT '',
    classification   TEXT NOT NULL DEFAULT '',
    suggested_reply  TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'new',
    approval_id      INTEGER,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ops_metrics_daily (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id        INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    platform      TEXT NOT NULL,
    date          TEXT NOT NULL,
    metrics_json  TEXT NOT NULL DEFAULT '{}',
    source        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(bot_id, platform, date)
);

CREATE TABLE IF NOT EXISTS ops_budget_usage (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id      INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    period      TEXT NOT NULL,
    text_cost   REAL NOT NULL DEFAULT 0,
    image_cost  REAL NOT NULL DEFAULT 0,
    video_cost  REAL NOT NULL DEFAULT 0,
    total_cost  REAL NOT NULL DEFAULT 0,
    limit_cost  REAL NOT NULL DEFAULT 0,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(bot_id, period)
);
```

复用：

- 内容任务：`issues`、`task_briefs`、`issue_checklists`
- 执行：`workspaces`、`agentproxy`
- 完成策略：completion profiles
- 知识/爆款库：knowledge base + memory + artifacts
- 审批卡片：imbot `ask_user` / permission
- 调度：scheduler
- 指标：Mission control

迁移注意：新表走 `CREATE TABLE IF NOT EXISTS`；如需给现有表加列（如 `imbot_chats.ops_bot_id`），用 `addColumnIfNotExists`，不在迁移列上加 CHECK；SQLite/PostgreSQL 双写并跑 `make schema-diff`。

---

## 11. API 与 MCP

对话优先，MCP 工具是主入口：

- `opsbot_create` / `opsbot_list` / `opsbot_update` / `opsbot_pause`
- `opsbot_bind_account` / `opsbot_bind_chat`
- `opsbot_request_credential_link` / `opsbot_browser_login`
- `opsbot_set_goals` / `opsbot_set_cadence` / `opsbot_set_budget` / `opsbot_set_approval`
- `ops_approval_list` / `ops_approval_approve` / `ops_approval_reject`
- `ops_publish` / `ops_engagement_reply` / `ops_metrics`

HTTP 仅保留极简控制台：

- `GET /api/ops/bots`
- `GET /api/ops/bots/:id`
- `PATCH /api/ops/bots/:id`（pause/resume/config）
- `GET /api/ops/approvals?status=pending`
- `POST /api/ops/approvals/:id/approve|reject`
- `GET /api/ops/bots/:id/metrics`
- `PATCH /api/imbot/chats/:id` 增加 `ops_bot_id`

---

## 12. 对话式 UI

### 12.1 接入流程

```
用户：接入公众号
Bot：账号名称是？
用户：品牌 A 公众号
Bot：主要目标是涨粉、转化还是品牌？
用户：涨粉
Bot：每周发几篇？要热点加更吗？
用户：每周 2 篇，要热点加更
Bot：发布前需要你确认吗？
用户：需要
Bot：请点这个一次性链接完成公众号授权。
用户：[完成]
Bot：已创建「品牌 A 公众号 Bot」，预算默认每月 ¥X，可随时在对话里调整。
```

- 每步只问一个问题，支持自然语言和快捷选项。
- 凭据走一次性安全链接 / 扫码，不在对话里传密钥。
- 创建完成返回配置摘要卡：账号、目标、节奏、审批、预算。
- 后续配置全部在对话完成：
  - “把预算调到每月 ¥3000”
  - “这周三加一篇热点”
  - “以后视频成片不用我确认”
  - “暂停这个账号”
- 审批在对话完成：卡片预览 + 通过/驳回/修改。

### 12.2 极简控制台

只做列表和状态，不做复杂表单：

- 账号 Bot、平台、运营人、预算余量、待审批数、风险状态。
- 点击进入对话；高级配置通过对话或可选 Dialog。
- 遵循 `docs/design-system.md`：warm token、shadcn、lucide、`t()`、Dark mode、4px 网格。

### 12.3 命令

`/bots`、`/bot <name>`、`/approvals`、`/pause`、`/resume`、`/budget`、`/calendar`。

---

## 13. 指标

### 13.1 账号级

- 内容产出量、发布成功率、审批通过率、平均审批时长
- 阅读/曝光、互动率、涨粉、转化
- 单篇成本、月度预算消耗
- 登录失效率、限流次数、风险事件

### 13.2 总监级

- 账号矩阵总产出、总预算、跨账号爆款率
- 选题复用率、同质内容比例
- 各账号目标达成度

### 13.3 呈现

Mission control 增加「运营 Bot」面板，按账号下钻；不合并成单一分数。

---

## 14. 错误处理与风险

| 场景 | 处理 |
|---|---|
| 登录态失效 | 生成“重新登录”任务，通知运营人扫码；不重试爆破 |
| 平台 UI 改版 | selector 失效 → 降级发布包 + 人工发布，告警 |
| 发布失败 | 退避重试；仍失败则保留上下文，生成具体问题 |
| 重复发布 | `idempotency_key` 去重；发布前检查回执 |
| 预算超限 | 暂停该账号自动生成/发布，通知运营人 |
| 审批超时 | 提醒；不自动通过；可升级总监 |
| 浏览器崩溃 | Worker 重启，保留任务队列；账号租约释放 |
| 风控/限流 | 降低频率、暂停账号、通知运营人 |
| 凭据泄露风险 | 凭据只存 credstore；日志/对话/卡片不出现明文 |

---

## 15. 分阶段落地

| 阶段 | 内容 | 验收 |
|---|---|---|
| Phase 0 | schema、Ops Bot CRUD、对话式接入、账号绑定、预算/审批配置 | 能在 IM 里完整创建一个公众号 Bot，不执行内容 |
| Phase 1 | 公众号官方 API 流水线：选题→文案→图片→审校→审批→发布→指标 | 一个公众号账号跑通图文闭环 |
| Phase 2 | 小红书本地浏览器 Worker：发布、数据、互动建议、图片笔记 | 一个小红书账号跑通图文闭环 |
| Phase 3 | 视频流水线、团队版云端浏览器、多账号总监协调 | 双平台 + 视频 + 多账号 |
| Phase 4 | 热点驱动、爆款回流、第三方数据、进阶指标 | 两种节奏并存，反馈闭环生效 |

实现计划按阶段拆分，先完成 Phase 0 并验收，再进入 Phase 1；每个阶段独立可上线。

---

## 16. 测试与验收

### 16.1 测试

**单元：**

- Ops Bot 配置、预算计算、审批状态机。
- 账号级隔离：凭据/profile/预算不串号。
- PlatformAdapter 归一化：公众号/小红书发布结果、指标、互动。
- 浏览器 Worker 租约、登录态失效、降级路径。
- 预算超限暂停。

**集成：**

- IM 对话 → Ops Bot 创建 → 账号 Epic/workspace 创建。
- 内容 issue → Brief → 合规门禁 → 审批 → mock 发布 → 回执。
- 审批驳回/修改 → 重新生成 → 再次审批。
- 总监生成日历 → 账号 Bot 接单 → 内容任务。
- 多账号隔离与总监协调。

**端到端：**

1. 对话创建公众号 Bot → 授权 → 发布一篇图文（测试账号/mock）。
2. 对话创建小红书 Bot → 扫码登录 → 发布一篇图文笔记（测试账号）。
3. 视频任务 → 成片 → 审批 → 发布。
4. 互动建议 → 审批 → 发送。
5. 预算超限 → 暂停 → 通知 → 调整预算恢复。
6. 登录失效/UI 改版 → 降级 → 告警。

### 16.2 验收

1. 能在 IM 对话中完成 Ops Bot 创建、账号授权、目标/节奏/预算/审批配置。
2. 公众号图文闭环可跑通（官方 API）。
3. 小红书图文闭环可跑通（本地浏览器，测试账号）。
4. 视频成片可生成并进入审批。
5. 发布、回复、视频成片默认都需人工确认；开关可按账号调整。
6. 互动只生成建议，不自动发送。
7. 每账号预算独立，超限暂停并通知。
8. 总监可跨账号排期、去重、分配预算。
9. 账号凭据、浏览器会话、指标严格隔离。
10. 所有外发有审批与审计记录。

---

## 17. 明确不做

- 不新建聊天系统；复用 imbot。
- 不新建执行引擎；复用 workspace/agentproxy/Epic。
- 不代持账号密码；不把密钥放进对话或 prompt。
- 不做批量关注/点赞/私信等高风险互动。
- 不假设小红书有官方开放 API。
- 不自动通过审批；不绕过平台频次/人工复核。
- 不默认接入第三方 SaaS 代发。
- 不做模型训练。

---

## 18. 待实现前确认

以下不影响本设计成立，进入实现计划前需要定：

1. **视频生成工具链**：优先复用现有 video-gen MCP，还是引入新的 TTS/视频模型；按成本与质量决定。
2. **团队版云端浏览器的部署形态**：独立容器 vs 共享池 + 账号级隔离；取决于企业版基础设施。
3. **预算币种与计价**：人民币/美元；按 token、图片张数、视频秒数分别计价。
4. **小红书数据补充**：是否接入第三方合规数据源（竞品/行业），还是只用创作者后台数据。
5. **账号接入方式**：公众号 AppID/AppSecret 与 MCP token 两种方式的首选；小红书扫码登录的会话保活策略。
