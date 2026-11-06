# 牛牛运营 Bot：账号级 AI 运营团队设计

日期：2026-11-06 ｜ 状态：待审核（Draft for review）
参考：Grok Bot ｜ 第一个需求：小红书 + 微信公众号自动化运营

## 0. 一句话

在牛牛现有 Epic / Workspace / IM Bot / Intake / Integration 之上，增加一层 **Ops Bot（运营 Bot）**：运营总监 Bot 管策略与日历，每个平台账号一个账号 Bot 管生产、发布、互动建议与复盘。平台差异收敛到 **ChannelAdapter + Platform Pack**，新增 Facebook、知乎等平台以配置为主，不改核心；主 UI 是对话，像对接 IM 一样完成配置和审批。

---

## 1. 背景与目标

### 1.1 现有底座

- **场景**：`wechat-mp`（公众号官方 API 闭环）、`social-ops`（内容日历/排期/周报/舆情）、`content-marketing`（选题/文案/审校/发布清单/复盘）、`media-studio`（图文/视频素材）。
- **编排**：通用运营闭环（A 采集选题 / B 内容 / C 复盘回流）、Epic、`exec_wave`、autohost、agent-to-agent 消息、blackboard。
- **交互**：IM Bot 渠道（飞书/钉钉/企微/微信/Telegram）、chat 配对、卡片回调、Intake/待校准。
- **外部能力**：integration provider / MCP / 数据源 / 凭据代理 / egress 审计。
- **治理**：workspace/worktree 隔离、completion profile、harness 门禁、Mission control、token/成本分析。

### 1.2 参考 Grok Bot

Grok Bot 的强项是常驻 AI 同事、每 Bot 一台云端电脑、computer use、多 Bot 团队协作、跟岗学习、开箱集成。牛牛不复制其云端重资产模式，而是用本地优先 + 多引擎 + 工程治理底座，做**账号级 AI 运营团队**。

### 1.3 目标

- **G1 双平台闭环**：微信公众号 + 小红书，选题、生产、审校、发布、互动建议、复盘全流程。
- **G2 账号级 Bot**：每个账号一个独立 Bot，凭据、浏览器会话、人设、预算、指标互相隔离。
- **G3 运营总监**：跨账号、跨渠道的选题、日历、预算、复盘协调。
- **G4 内容全形态**：文案 + 封面/配图 + 短视频自动生成。
- **G5 品牌一致性**：品牌知识库 + 历史内容/爆款库 + 实时热点作为创作依据。
- **G6 安全默认**：发布、互动回复、视频成片三类动作默认人工确认；合规门禁先于人工确认。
- **G7 对话式 UI**：像对接 IM 一样，通过聊天完成 Bot 创建、账号授权、策略/预算配置和审批。
- **G8 平台可扩展**：新增渠道以 Platform Pack 配置为主，不改 Ops Bot 核心；有 OpenAPI 的平台优先走 API。
- **G9 多账号**：同一渠道支持任意多个账号，账号级隔离、预算、审批、指标。

### 1.4 非目标

- 不做新的聊天系统；复用 imbot。
- 不做新的执行引擎；复用 workspace/agentproxy/Epic。
- 不代持平台账号密码；不把密钥放进对话或 prompt。
- 不做批量关注/点赞/私信等高风险互动。
- 不假设所有平台都有官方 API；没有 API 的平台走浏览器 recipe。
- 不自动通过审批；默认必须人工确认。
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
| 13 | 平台接入 | 有 OpenAPI 优先走 API；没有则 MCP / 浏览器 recipe |
| 14 | 平台扩展 | ChannelAdapter + Platform Pack；新增平台以配置为主 |
| 15 | Pack 存储 | 数据库注册表 + 客户环境文件；对话生成，永久保存 |
| 16 | 多账号 | 一个渠道一个 Pack，一个账号一个 Ops Bot；账号级隔离 |

> “全自动”指系统具备端到端执行能力；默认审批策略仍要求发布、回复、视频成片三类人工确认。运营人可按账号关闭某类确认开关，关闭后系统自动执行。

---

## 3. 领域模型与边界

### 3.1 四个概念

| 概念 | 定义 | 不承担 |
|---|---|---|
| **Channel（渠道）** | 平台类型：公众号、小红书、Facebook、知乎 | 不持有账号 |
| **Platform Pack** | 一个渠道的声明式蓝图：能力、内容 schema、合规、提示词、指标映射、浏览器 recipe、能力绑定 | 不持有密钥，不绑定具体账号 |
| **Account（账号）** | 渠道上的一个具体身份：公众号 A、小红书号 1、Facebook Page 2 | 不是 Bot |
| **Ops Bot（运营 Bot）** | 一个账号的运营实例：账号、Pack 版本、人设、凭据引用、目标、节奏、审批策略、预算、知识范围 | 不是聊天渠道，不是执行引擎 |

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
    └── ChannelAdapter
          ├── OpenAPI / MCP / 数据源
          └── Browser Worker + recipe
```

### 3.3 多账号模型

```
Platform Pack: wechat_mp
├── Ops Bot: 公众号 A
├── Ops Bot: 公众号 B
└── Ops Bot: 公众号 C

Platform Pack: xiaohongshu
├── Ops Bot: 小红书号 1
└── Ops Bot: 小红书号 2
```

**共享（Channel 级）：**

- Platform Pack：内容 schema、合规规则、提示词、能力绑定、browser recipe；
- 总监 Bot、品牌知识库、跨账号内容日历；
- 可选的 app 级凭据（如 Facebook App、公众号 MCP 端点）。

**独立（Account 级）：**

- `account_ref`、凭据引用、浏览器 profile / 登录态；
- 人设、受众、目标、节奏、预算、审批运营人；
- 内容记忆、指标、风险状态、发布回执；
- 可固定 Pack 版本，避免 Pack 升级影响所有账号。

**唯一约束：** `(owner_type, owner_id, pack_id, account_ref)` 唯一。

**凭据分两层：**

| 层级 | 例子 | 归属 |
|---|---|---|
| App credential | Facebook App ID/Secret、公众号 MCP token | owner/org，可跨账号共享 |
| Account credential | Page access token、公众号 AppID/Secret、小红书登录态 | 每个账号独立 |

Pack 只写 `credential_ref`，不写密钥；Adapter 按账号解析对应凭据。

**限流与隔离：**

- 限流 key = account + app；Facebook Page 和公众号都可能同时有 app 级/账号级限制。
- 浏览器 profile 按账号隔离，不共享 cookie。
- 预算、审批、指标、风险状态全部按账号独立。
- 总监跨账号聚合、分配预算、去重选题；审批默认是账号运营人，总监代审要记录。

### 3.4 和 imbot 的关系

- **imbot 是前门，Ops Bot 是身份。** 不新建聊天系统。
- 一个 Ops Bot 可绑定一个或多个 IM chat/thread；一个 chat 可按 thread/topic 路由到不同 Ops Bot。
- 复用 `HandleInbound`、`IntakeService`、`RouteInProject`、`imbot_dispatch`、`ask_user` 卡片。
- 对话式配置：用户说“接入公众号账号 A”“每周发 2 篇”“发布前要我确认”，Bot 用结构化问题补全并创建 Ops Bot。
- 对话式审批：发布/回复/视频成片以卡片形式推送到 IM，运营人点“通过 / 驳回 / 修改”。
- 命名：对外叫「运营 Bot」「公众号 Bot」「小红书 Bot」；现有 imbot 叫「消息渠道 / IM 渠道」。

### 3.5 和外部系统集成、数据集成的关系

- Ops Bot **不持有平台密钥，不直接写 HTTP**；只保存 `credential_ref` 和能力引用。
- **外部系统集成（动作）**：发布/回复/素材/数据走 Pack 的能力绑定；有 OpenAPI 优先走 integration provider，没有则 MCP 或浏览器 recipe。
- **数据集成（数据）**：热点、竞品、平台指标走现有 data source / knowledge base / `run_data_query`，复用 scope 与读写权限。
- **浏览器**：ChannelAdapter 的一个执行后端，不是新集成框架。
- 所有外发经过 egress / 授权 / 审计；外部文本只作数据，不构成指令。

---

## 4. 总体架构

### 4.1 组件

```
server/internal/opsbot/
  service.go        # OpsBot CRUD、配置、暂停/恢复/Kill switch
  provision.go      # 账号/总监 Epic + 控制工作空间
  onboarding.go     # 对话式接入与配置
  approval.go       # 审批状态机、卡片、超时、代审
  budget.go         # 月度预算、分项成本、暂停与调额
  calendar.go       # 日历 + 固定节奏
  trend.go          # 热点驱动
  director.go       # 跨账号选题池、去重、预算、代审、复盘
  publish.go        # 发布编排：幂等、回执、重试、降级
  engagement.go     # 评论/私信状态机
  metrics.go        # 账号指标归一化
  feedback.go       # 周报与爆款回流
  risk.go           # 风控、登录失效、限流、降级
  content/
    brief.go        # Task Brief
    pipeline.go     # 图文流水线
    media.go        # 封面/配图/排版
    video.go        # 视频流水线
    review.go       # 合规与质量门禁
  channel/
    adapter.go      # ChannelAdapter 接口
    manual.go       # 人工发布包
    wechat.go       # 公众号官方 API/MCP
    xhs.go          # 小红书浏览器
    facebook.go     # Graph API
  browser/
    worker.go       # 任务执行、队列、限速、证据
    session.go      # profile、登录态、租约
    recipe.go       # Recipe DSL 解释器
    sandbox.go      # 沙箱测试
    backend.go      # 本地/云端后端契约
  packs/
    types.go        # Manifest / Binding / schema
    validate.go     # 校验
    registry.go     # 文件系统版本、发布、导入导出
    generator.go    # OpenAPI/文档生成 Pack 草稿
    builtin/        # 内置 Pack
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
  → ChannelAdapter（发布/回复/取数）
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

## 5. ChannelAdapter 与 Platform Pack

### 5.1 ChannelAdapter 契约

```go
type ChannelAdapter interface {
    Capabilities() CapabilitySet
    EnsureSession(ctx, account) error
    Publish(ctx, payload) (Receipt, error)
    FetchMetrics(ctx, range) (Metrics, error)
    ListEngagements(ctx, cursor) ([]Engagement, error)
    Reply(ctx, engagementID, text) (Receipt, error)
    HealthCheck(ctx) error
}
```

Ops Bot 只依赖这个契约，不感知平台。

### 5.2 Platform Pack 结构

```
packs/<pack_id>/<version>/
  manifest.json        # id、版本、状态、能力列表、能力绑定、认证层级
  content_schema.json  # 图文/笔记/视频的字段、尺寸、字数、标签规则
  compliance.json      # 平台红线、广告法、版权、审核清单
  metrics_map.json     # 平台指标 → 统一指标
  prompts/             # 平台语气、选题、排版、视频脚本提示词
  browser/recipes/     # 登录、发布、读取、回复的声明式步骤
  fixtures/            # 沙箱测试样例
```

`manifest.json` 示例：

```json
{
  "id": "facebook",
  "version": 1,
  "status": "published",
  "display_name": "Facebook Page",
  "capabilities": ["publish", "metrics", "engagements", "reply"],
  "bindings": {
    "publish":     { "transport": "openapi", "provider": "facebook-graph" },
    "metrics":     { "transport": "openapi", "provider": "facebook-graph" },
    "engagements": { "transport": "openapi", "provider": "facebook-graph" },
    "reply":       { "transport": "openapi", "provider": "facebook-graph" }
  },
  "credential_slots": {
    "app":     { "required": true, "scope": "owner" },
    "account": { "required": true, "scope": "account" }
  }
}
```

### 5.3 API 优先的能力级绑定

`bindings.yaml` 按能力绑定，不是整个平台一个接入方式：

```yaml
capabilities:
  publish:
    preferred: openapi
    binding: facebook-graph
  metrics:
    preferred: openapi
    binding: facebook-graph
  engagements:
    preferred: openapi
    binding: facebook-graph
  reply:
    preferred: openapi
    binding: facebook-graph
```

解析顺序：

1. **官方 OpenAPI**：有规范就优先走 integration 草稿流水线（`draft_integration` → 预检 → 用户确认 → `publish_provider`）。
2. **现有 MCP / provider**：没有 OpenAPI 但有现成 MCP，直接绑定。
3. **浏览器 recipe**：API 覆盖不到的能力才走 Browser Worker。
4. **人工**：以上都不可用，生成发布包，人工执行。

一个平台可以混合：Facebook 发布/指标/回复全走 Graph API；知乎可能指标走 API、发布走浏览器；小红书发布/互动走浏览器。**只有能力缺失时才降级，不是整个平台降级。**

### 5.4 Pack 存储与生命周期

**不建 Pack 注册表**，客户环境文件系统就是唯一真相：

```
~/.niuniu/users/<uid>/ops/packs/<pack_id>/<version>/
  manifest.json          # id/version/status/capabilities/bindings
  content_schema.json
  compliance.json
  metrics_map.json
  prompts/
  browser/recipes/
~/.niuniu/orgs/<oid>/ops/packs/<pack_id>/<version>/     # 本地/自托管
或租户对象存储 / 持久卷                                   # 云端团队版
```

- **版本 = 目录**：`<pack_id>/<version>/`，发布版本不可变；编辑生成新目录。
- **状态 = manifest 字段**：`draft | testing | published | deprecated`；发布是原子写 `manifest.json`（临时文件 + rename）。
- **列表 = 目录扫描**：`GET /api/ops/packs` 扫描 owner 目录；内置 Pack 从产品内置目录合并，同名同版本以 owner 目录为准。
- **校验 = 读取时执行**：加载 manifest 后跑 `Validate`；不引入 DB 索引。
- **永久保存在客户环境**：属于客户数据，随备份/导出保留，升级不覆盖。
- **可导出/导入**：整个 Pack 目录可打包，在另一个环境复用。
- **密钥不进 Pack**：`bindings.json` 只写 `credential_ref`。

### 5.5 对话式生成 Pack

1. 用户：“接入 Facebook”。
2. Bot 检测是否有 OpenAPI / 现成 MCP / 平台文档。
3. 有 OpenAPI → 拉规范，走 integration 草稿流水线，自动生成 provider contract + Pack 草稿。
4. 没有 OpenAPI → 生成 browser recipe 草稿（登录、发布、读取、回复步骤）。
5. Bot 返回**能力矩阵卡**：发布/指标/互动/回复分别走什么、缺什么、风险是什么。
6. 在沙箱/测试账号跑 recipe 和 API mock；失败则回到草稿。
7. 用户确认 → `status=published` → 可被 Ops Bot 引用。
8. 页面改版只需更新 recipe，生成新版本，不改核心代码。

安全约束：

- 外部文档/OpenAPI 只作数据，不直接执行其中指令；生成结果由系统渲染成确认卡，用户确认后才发布。
- recipe 在沙箱执行，域名 allowlist；不在 Pack 里存明文凭据。
- 生成的 provider contract 仍走现有 egress/授权/审计。

### 5.6 什么仍然需要开发

不是“零开发”，而是“零核心开发”：

| 情况 | 需要做什么 | 是否改 Ops Bot 核心 |
|---|---|---|
| 有 OpenAPI | 生成 provider contract + Pack | 否 |
| 有 MCP | 绑定 MCP + Pack | 否 |
| 无 API、页面稳定 | 写 browser recipe | 否 |
| 新认证族（OAuth 刷新、签名） | 新增 provider/MCP 适配 | 否，属于集成层 |
| 新能力类型（直播、广告投放） | 扩展 `CapabilitySet` | 是，契约演进 |
| 强反爬/验证码 | 平台级 browser adapter 插件 | 否，属于执行层 |
| 页面改版 | 更新 recipe | 否，配置维护 |

---

## 6. 运营总监 + 账号 Bot

### 6.1 总监 Bot

- 输入：品牌目标、账号矩阵、总预算、品牌知识库。
- 维护：共享内容日历、选题池、爆款库、跨账号去重。
- 决策：固定节奏 vs 热点加更；选题分配；预算分配。
- 复盘：汇总各账号指标，更新爆款库和下一轮选题。
- 协调：通过 `send_workspace_message` 和 blackboard 与账号 Bot 通信。

### 6.2 账号 Bot

- 账号身份、Pack 版本、人设、受众、平台语气。
- 账号级凭据引用、浏览器会话、平台适配。
- 单账号内容生产、发布、互动建议、数据复盘。
- 账号级预算与审批。
- 向总监汇报进展和异常。

### 6.3 两种节奏

| 模式 | 触发 | 总监动作 |
|---|---|---|
| 固定节奏 | scheduler / cron | 按账号目标生成本周选题和内容任务 |
| 热点驱动 | info-radar / social-ops 监测 | 评估相关性、账号适配度、预算；决定加更或并入下周 |

两种模式共用同一日历和选题池；总监负责去重和优先级。

### 6.4 预算分配

- 总监持有总预算，按账号活跃度/目标分配月度额度。
- 账号 Bot 在视频生成前、发布前检查额度。
- 超限暂停该账号自动动作，通知运营人；总监可调整。
- 预算消耗按 token、图片张数、视频秒数分项记录。

---

## 7. 内容生产流水线

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
| 发布 | 按 Pack 绑定走 OpenAPI / MCP / 浏览器 | `ChannelAdapter` |
| 互动建议 | 评论/私信分类 + 建议回复 | 浏览器 Worker / 官方 API |
| 数据复盘 | 阅读、互动、涨粉、转化 | 官方 API / 浏览器后台 / 数据源 |
| 回流 | 爆款拆解、选题机会、受众反馈 | blackboard / 知识库 / 记忆 |

### 7.1 Task Brief

```go
type OpsContentBrief struct {
    Channel    string   // pack_id
    AccountRef string
    Goal       string   // 涨粉 | 转化 | 品牌
    Audience   string
    Format     string   // article | note | video
    Topic      string
    Acceptance []string
    SourceRefs []string
}
```

- Brief 由总监或账号 Bot 生成，运营人可在对话中修改。
- Brief 落到 `task_briefs`，验收项同步到 `issue_checklists`。
- 视频任务额外记录时长、比例、字幕、封面要求。

### 7.2 质量门禁

1. **事实/合规门禁**：无来源的关键断言、广告法绝对化用语、平台红线、版权风险 → 阻断。
2. **品牌一致性**：语气、术语、视觉规范。
3. **平台适配**：尺寸、时长、标题长度、标签数量。
4. **人工确认**：三类动作默认停下等人。

### 7.3 互动处理（评论 / 私信）

```
拉取互动 → 去重入库 → 分类 → 生成建议回复 → 审批卡片
   → 通过后发送 → 记录回执 → 失败/高风险升级
```

| 状态 | 含义 | 后续 |
|---|---|---|
| `new` | 刚拉取，未处理 | 进入分类 |
| `classified` | 已分类（咨询/好评/投诉/负面/垃圾/商机） | 生成建议回复 |
| `suggested` | 有建议回复，等待审批 | 推送审批卡片 |
| `approved` | 运营人通过 | 调用 Adapter 发送 |
| `sent` | 发送成功 | 记录回执，关闭 |
| `rejected` | 运营人驳回 | 关闭，不发送 |
| `escalated` | 高风险/敏感，升级总监 | 总监或运营人处理 |
| `failed` | 发送失败 | 退避重试，超限告警 |

- 去重键：`(bot_id, platform_engagement_id)`。
- 默认只生成建议、不自动发送；账号可关闭 `reply` 审批开关。
- 频率限制：单账号每小时发送上限，超限排队。
- 高风险（投诉、负面、法律、退款）一律升级，不自动发送。

---

## 8. 浏览器 Worker 与运行后端

### 8.1 本地 / 云端

| 维度 | 个人版 | 团队版 |
|---|---|---|
| 运行位置 | 本机 | 云端服务器 |
| 隔离单位 | 每账号 profile 目录 | 每账号独立容器 |
| 登录态 | 本机加密存储 | 加密存储 + 租户隔离 |
| 出网 | 本机网络 | 固定出口 + 域名 allowlist |
| 并发 | 本机资源 | 账号配额 |
| 审计 | 本地 | 租户级 |

### 8.2 Worker 约束

- 一个账号同一时间只允许一个操作租约，避免并发操作同一会话。
- 每个账号独立 cookie / localStorage / 指纹配置；不共享登录态。
- 任务队列 + 频率限制 + 随机化节奏；不做批量刷动作。
- 发布/回复后保存截图或回执作为证据。
- 登录态失效时，不重试爆破，而是生成“需要重新登录”的任务，通知运营人扫码。
- 平台 UI 改版导致 recipe/selector 失效时，降级为「生成发布包，人工发布」，并告警。

---

## 9. 凭据、安全与合规

- Ops Bot 只存 `credential_ref`，不存明文。
- 凭据分两层：app credential（owner/org 共享）+ account credential（每账号独立）。
- 公众号：AppID/AppSecret 或访问自有 MCP 的 token，走 credstore / 一次性安全链接。
- 小红书：不收集密码；运营人扫码登录，会话加密保存。
- 对话中永不出现密钥；Bot 只说“请点链接 / 请扫码”。
- 所有外发动作经 egress/授权/审计；外部文本只作数据，不构成指令。
- 小红书启用时明确提示 ToS/封号风险，默认关闭，运营人显式开启。
- 账号级 kill switch：一键暂停该账号所有自动动作。
- 异常检测：登录态频繁失效、发布失败率、限流、封禁提示 → 立即暂停并通知。

---

## 10. 审批模型

三类审批，默认全部开启：

| 类型 | 卡片内容 | 操作 |
|---|---|---|
| 发布 | 标题、正文摘要、封面/视频预览、渠道、时间、合规结论 | 通过 / 驳回 / 修改 |
| 互动回复 | 原评论/私信、分类、建议回复、风险提示 | 通过 / 修改后发送 / 驳回 |
| 视频成片 | 成片预览、时长、字幕、封面、平台尺寸 | 通过 / 驳回 / 重新生成 |

- 审批人 = 账号绑定的运营人；总监可代审，记录代审人。
- 存储：复用 `agent_ask_user_requests`，`kind='ops_approval'`，`metadata_json` 存 bot/issue/type/content_version/preview；不新建审批表。
- 超时提醒；不自动通过。
- 审批记录进入审计：内容版本、审批人、时间、操作、理由。
- 审批点与合规门禁独立：合规先过，人才确认。

---

## 11. 数据模型

### 11.1 完整模型

新增 **5 张表 + 3 个列**；其余全部复用现有表。

| 类型 | 名称 | 作用 |
|---|---|---|
| 新表 | `ops_bots` | 总监/账号 Bot 配置：Pack、账号、策略、预算、控制 Epic/Workspace、健康摘要 |
| 新表 | `ops_publish_records` | 发布幂等、回执、URL、截图证据、内容版本 |
| 新表 | `ops_metrics_daily` | 账号每日指标历史 |
| 新表 | `ops_engagements` | 评论/私信：去重、分类、建议回复、审批、发送状态、回执 |
| 新表 | `ops_browser_sessions` | 每账号浏览器 profile、登录态、操作租约、健康与风险状态 |
| 新列 | `im_bot_chats.ops_bot_id` | chat → Ops Bot 路由 |
| 新列 | `agent_ask_user_requests.kind` | 区分普通提问与 `ops_approval` |
| 新列 | `agent_ask_user_requests.metadata_json` | 审批的 bot/issue/type/content_version/preview |

**复用现有表：**

| 需求 | 现有表 |
|---|---|
| 内容任务 / 日历 | `issues`（`start_date`/`due_date` + 看板列） |
| 内容 Brief / 验收 | `task_briefs` + `issue_checklists` |
| 发布/成本事件 | `issue_exec_events`（`cost` / `terminal`，detail_json 存回执摘要） |
| 审批 | `agent_ask_user_requests`（`kind='ops_approval'`） |
| 预算 | `workspace_costs` + `issue_exec_events.cost_usd` 聚合 |
| Platform Pack | 客户环境 `manifest.json`，不建表 |

### 11.2 DDL

```sql
CREATE TABLE IF NOT EXISTS ops_bots (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type           TEXT NOT NULL DEFAULT 'user',
    owner_id             INTEGER NOT NULL DEFAULT 0,
    role                 TEXT NOT NULL DEFAULT 'account', -- director | account
    pack_id              TEXT NOT NULL DEFAULT '',
    pack_version         INTEGER NOT NULL DEFAULT 0,       -- 0 = follow latest
    account_ref          TEXT NOT NULL DEFAULT '',
    display_name         TEXT NOT NULL DEFAULT '',
    persona_json         TEXT NOT NULL DEFAULT '{}',
    credential_refs_json TEXT NOT NULL DEFAULT '{}',       -- {app, account}
    knowledge_scope_json TEXT NOT NULL DEFAULT '{}',
    goals_json           TEXT NOT NULL DEFAULT '{}',
    cadence_json         TEXT NOT NULL DEFAULT '{}',
    approval_policy_json TEXT NOT NULL DEFAULT '{}',
    budget_json          TEXT NOT NULL DEFAULT '{}',
    runtime_json         TEXT NOT NULL DEFAULT '{}',       -- health/risk summary; sessions in ops_browser_sessions
    epic_issue_id        INTEGER,
    control_workspace_id INTEGER,
    status               TEXT NOT NULL DEFAULT 'active',   -- active | paused | disabled
    director_bot_id      INTEGER,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, pack_id, account_ref)
);

CREATE TABLE IF NOT EXISTS ops_publish_records (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id         INTEGER NOT NULL,
    bot_id           INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    pack_id          TEXT NOT NULL DEFAULT '',
    account_ref      TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS ops_metrics_daily (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id        INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    date          TEXT NOT NULL,
    metrics_json  TEXT NOT NULL DEFAULT '{}',
    source        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(bot_id, date)
);

CREATE TABLE IF NOT EXISTS ops_engagements (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id                 INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    issue_id               INTEGER,                          -- 关联内容 issue，可空
    platform_engagement_id TEXT NOT NULL,                    -- 平台评论/私信 ID
    kind                   TEXT NOT NULL DEFAULT 'comment',  -- comment | dm
    author                 TEXT NOT NULL DEFAULT '',
    text                   TEXT NOT NULL DEFAULT '',
    category               TEXT NOT NULL DEFAULT '',         -- inquiry|praise|complaint|negative|spam|lead
    risk_level             TEXT NOT NULL DEFAULT 'low',      -- low | medium | high
    suggested_reply        TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT 'new',
        -- new|classified|suggested|approved|sent|rejected|escalated|failed
    approval_request_id    INTEGER,                          -- agent_ask_user_requests.id
    reply_text             TEXT NOT NULL DEFAULT '',
    receipt                TEXT NOT NULL DEFAULT '',
    decided_by             INTEGER,
    decided_at             TIMESTAMP,
    created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(bot_id, platform_engagement_id)
);
CREATE INDEX IF NOT EXISTS idx_ops_engagements_bot_status ON ops_engagements(bot_id, status);

CREATE TABLE IF NOT EXISTS ops_browser_sessions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id           INTEGER NOT NULL REFERENCES ops_bots(id) ON DELETE CASCADE,
    backend          TEXT NOT NULL DEFAULT 'local',  -- local | cloud
    profile_dir      TEXT NOT NULL DEFAULT '',
    login_status     TEXT NOT NULL DEFAULT 'unknown',-- unknown | valid | expired
    last_login_at    TIMESTAMP,
    lease_owner      TEXT NOT NULL DEFAULT '',       -- 持有操作租约的 worker
    lease_expires_at TIMESTAMP,
    health           TEXT NOT NULL DEFAULT 'unknown',-- unknown | healthy | degraded | down
    risk_state       TEXT NOT NULL DEFAULT 'normal', -- normal | throttled | blocked
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(bot_id, backend)
);
```

既有表新增列（fresh DB 写进 `schema.sql` / `schema_postgres.sql`，存量 DB 用 `addColumnIfNotExists`）：

```sql
ALTER TABLE im_bot_chats ADD COLUMN ops_bot_id INTEGER;
ALTER TABLE agent_ask_user_requests ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent_question';
ALTER TABLE agent_ask_user_requests ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
```

### 11.3 复用与取舍

**不新建表的部分：**

| 需求 | 复用方式 | 理由 |
|---|---|---|
| Platform Pack | 客户环境目录 + `manifest.json` | 用户要求“对话生成、客户环境永久保存”；文件即真相，版本=目录，避免 Pack 内容与 DB 双写 |
| chat → Bot | `im_bot_chats.ops_bot_id` 列 | 一对一绑定，不需要独立映射表 |
| 日历 | `issues.start_date` / `due_date` + 看板列 | 内容任务本身就是 issue，日历是视图不是新实体 |
| 审批 | `agent_ask_user_requests` + `kind` + `metadata_json` | 已有状态机、决定人、超时、审计 |
| 预算 | `workspace_costs` + `issue_exec_events.cost_usd` 聚合 | 成本已经按 workspace/issue 记账，额度在 `ops_bots.budget_json` |

**保留 5 张新表的理由：**

- `ops_bots`：账号/总监配置与账号级隔离的唯一载体，没有现成表。
- `ops_publish_records`：发布需要幂等唯一键、回执、URL、截图和内容版本；`issue_exec_events` 无唯一键，不能防重复发布。
- `ops_metrics_daily`：按账号按天的指标历史，现有表无对应结构。
- `ops_engagements`：每条评论/私信有独立状态机（分类→建议→审批→发送→回执）、去重键和审批关联；借 `intake_events` 会混淆“外部任务信号”与“互动回复状态”。
- `ops_browser_sessions`：登录态、profile 目录和操作租约需要在多实例/云端之间用行级条件更新协调；塞进 `runtime_json` 无法做事务性租约，也无法按账号查询健康状态。

### 11.4 迁移注意

- 新表走 `CREATE TABLE IF NOT EXISTS`，索引与建表同批；新列用 `addColumnIfNotExists`，不在迁移列上加 CHECK。
- `agent_ask_user_requests.kind` 存量行默认为 `agent_question`；`metadata_json` 默认 `{}`。
- SQLite/PostgreSQL 双写并跑 `make schema-diff`；sqlc 重新生成。
- Pack 目录不参与 DB 迁移；内置 Pack 首次使用时复制到 owner 目录。
- `ops_browser_sessions` 的租约用条件更新：`UPDATE ... SET lease_owner=?, lease_expires_at=? WHERE bot_id=? AND (lease_owner='' OR lease_expires_at < CURRENT_TIMESTAMP)`，影响 0 行即租约被占用。

---

## 12. API 与 MCP

对话优先，MCP 工具是主入口：

**Ops Bot / 账号：**

- `opsbot_create` / `opsbot_list` / `opsbot_get` / `opsbot_update`
- `opsbot_pause` / `opsbot_resume` / `opsbot_kill`
- `opsbot_bind_chat`
- `opsbot_request_credential_link` / `opsbot_browser_login`
- `opsbot_set_goals` / `opsbot_set_cadence` / `opsbot_set_budget` / `opsbot_set_approval`

**Pack：**

- `ops_pack_list` / `ops_pack_generate` / `ops_pack_validate`
- `ops_pack_test` / `ops_pack_publish`
- `ops_pack_export` / `ops_pack_import`

**审批 / 执行 / 指标：**

- `ops_approval_list` / `ops_approval_approve` / `ops_approval_reject`
- `ops_publish` / `ops_engagement_reply` / `ops_metrics` / `ops_calendar`

HTTP 仅保留极简控制台：

- `GET/POST /api/ops/bots`
- `GET/PATCH /api/ops/bots/:id`
- `POST /api/ops/bots/:id/pause|resume|disable`
- `GET /api/ops/packs`、`POST /api/ops/packs/import`、`GET /api/ops/packs/:id/:version/export`
- `GET /api/ops/approvals?status=pending`
- `POST /api/ops/approvals/:id/approve|reject|modify`
- `GET /api/ops/bots/:id/metrics`
- `GET /api/ops/bots/:id/engagements`
- `GET /api/ops/bots/:id/budget`
- `PATCH /api/imbot/chats/:id` 增加 `ops_bot_id`

---

## 13. 对话式 UI

### 13.1 接入已有 Pack 的账号

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
- 创建完成返回配置摘要卡：账号、Pack、目标、节奏、审批、预算。

### 13.2 接入新渠道

```
用户：接入 Facebook
Bot：检测到 Facebook Graph API。我会先生成一个 Facebook Pack 草稿，
     包括发布、指标、评论、回复四个能力，全部走官方 API。
     是否继续？
用户：继续
Bot：[能力矩阵卡] 发布 API / 指标 API / 互动 API / 回复 API
     风险：需要 Page 权限与 App 审核。
     是否在测试账号上跑一遍？
用户：跑
Bot：测试通过。发布 Pack v1？
用户：发布
Bot：已发布。现在要绑定哪个 Page？
```

- 有 OpenAPI：自动生成 provider contract + Pack 草稿。
- 无 OpenAPI：生成 browser recipe 草稿，测试账号跑通后发布。
- Pack 发布后，同渠道多账号只需重复“绑定账号”。

### 13.3 多账号

```
用户：再接入一个公众号
Bot：使用现有 wechat_mp Pack v3。账号名称是？
用户：品牌 B 公众号
Bot：请完成授权。
用户：[完成]
Bot：已创建「品牌 B 公众号 Bot」，独立预算与审批人。
```

- 同渠道复用 Pack；每个账号独立 Ops Bot。
- 可分别设置目标、节奏、预算、审批人、Pack 版本。

### 13.4 审批与控制

- 审批卡片：发布 / 回复 / 视频成片；通过/驳回/修改。
- 对话命令：`/bots`、`/bot <name>`、`/approvals`、`/pause`、`/resume`、`/budget`、`/calendar`。
- 极简控制台只做列表和状态：账号、Pack、运营人、预算余量、待审批数、风险状态。
- 遵循 `docs/design-system.md`：warm token、shadcn、lucide、`t()`、Dark mode、4px 网格。

---

## 14. 指标

### 14.1 账号级

- 内容产出量、发布成功率、审批通过率、平均审批时长
- 阅读/曝光、互动率、涨粉、转化
- 单篇成本、月度预算消耗
- 登录失效率、限流次数、风险事件

### 14.2 总监级

- 账号矩阵总产出、总预算、跨账号爆款率
- 选题复用率、同质内容比例
- 各账号目标达成度

### 14.3 呈现

Mission control 增加「运营 Bot」面板，按渠道/账号下钻；不合并成单一分数。

---

## 15. 错误处理与风险

| 场景 | 处理 |
|---|---|
| 登录态失效 | 生成“重新登录”任务，通知运营人扫码；不重试爆破 |
| 平台 UI 改版 | recipe/selector 失效 → 降级发布包 + 人工发布，告警 |
| 发布失败 | 退避重试；仍失败则保留上下文，生成具体问题 |
| 重复发布 | `idempotency_key` 去重；发布前检查回执 |
| 预算超限 | 暂停该账号自动生成/发布，通知运营人 |
| 审批超时 | 提醒；不自动通过；可升级总监 |
| 浏览器崩溃 | Worker 重启，保留任务队列；账号租约释放 |
| 风控/限流 | 降低频率、暂停账号、通知运营人 |
| 凭据泄露风险 | 凭据只存 credstore；日志/对话/卡片不出现明文 |
| Pack 升级 | 发布新版本；账号可固定旧版本或显式升级 |

---

## 16. 完整能力清单

本期一次交付以下全部能力，不设“先骨架、后补能力”的分期。

| # | 能力 | 完整交付内容 | 验收 |
|---|---|---|---|
| C1 | Bot 与账号管理 | 总监 Bot + 账号 Bot；同渠道任意多账号；账号级目标/节奏/预算/审批人；暂停/恢复/Kill switch | 可创建多个账号 Bot，配置互不串号 |
| C2 | 对话式接入 | IM 内完成选渠道、建账号、授权、设目标/节奏/预算/审批；改配置；处理审批 | 全流程不离开对话即可完成 |
| C3 | Platform Pack | `manifest/content_schema/compliance/metrics_map/prompts/recipes/fixtures`；版本=目录；内置 `wechat_mp`、`xiaohongshu`；导入/导出 | 新渠道可复用同一 Pack 绑定多账号 |
| C4 | Pack 生成 | 检测 OpenAPI/MCP/文档 → 生成 provider contract + Pack 草稿 → 能力矩阵卡 → 沙箱测试 → 发布 | Facebook 由生成流程产出，不改核心 |
| C5 | 图文生产 | Brief（品牌 KB + 爆款库 + 热点）→ 公众号长文/小红书笔记 → 封面/配图 → 排版草稿 | 两个平台各产出可发布图文 |
| C6 | 视频生产 | 脚本 → 分镜 → TTS → 画面 → 字幕 → 合成 → 平台尺寸/时长适配 | 产出成片并进入审批 |
| C7 | 合规与质量门禁 | 事实溯源、广告法、平台红线、版权、品牌一致性、平台适配 | 不合规内容在审批前被阻断 |
| C8 | 发布 | 幂等键、回执、URL、截图证据、退避重试、人工发布包降级 | 不重复发布；失败可追溯 |
| C9 | 指标 | 平台指标归一化 → `ops_metrics_daily`；账号目标达成度 | 每账号每日指标可查询、可下钻 |
| C10 | 互动 | 评论/私信拉取、去重、分类、建议回复、审批、发送、限速、高风险升级 | 只发人工确认后的回复 |
| C11 | 浏览器运行时 | 本地/云端后端；每账号 profile/登录态/租约；队列/限速/风控；UI 改版降级 | 同账号不并发；失效可恢复 |
| C12 | 总监协调 | 固定节奏 + 热点驱动；跨账号选题池、去重、预算分配、代审 | 多账号日历不冲突、内容不重复 |
| C13 | 复盘回流 | 周报、爆款拆解、知识库/记忆更新、下一轮选题引用 | 复盘结论进入下一轮 Brief |
| C14 | 可观测与治理 | Mission control 运营面板；审批/发布/成本/风险审计；e2e 验收 | 全链路可审计、可暂停 |

---

## 17. 测试与验收

### 17.1 测试

**单元：**

- ChannelAdapter 契约、能力级 binding 解析、OpenAPI/MCP/browser 选择。
- Platform Pack 校验：manifest、content schema、bindings、recipe。
- Ops Bot 配置、预算计算、审批状态机。
- 多账号唯一约束与账号级隔离（凭据/profile/预算不串号）。
- Pack 版本固定/跟随最新。

**集成：**

- IM 对话 → Ops Bot 创建 → 账号 Epic/workspace 创建。
- 同渠道第二个账号复用 Pack，独立创建 Ops Bot。
- 内容 issue → Brief → 合规门禁 → 审批 → mock 发布 → 回执。
- 审批驳回/修改 → 重新生成 → 再次审批。
- 总监生成日历 → 账号 Bot 接单 → 内容任务。
- Pack 生成 → 沙箱测试 → 发布 → 账号绑定。

**端到端：**

1. 对话创建公众号 Bot → 授权 → 发布一篇图文（测试账号/mock）。
2. 同渠道创建第二个公众号 Bot，复用 Pack，独立预算/审批。
3. 对话创建小红书 Bot → 扫码登录 → 发布一篇图文笔记（测试账号）。
4. 视频任务 → 成片 → 审批 → 发布。
5. 互动建议 → 审批 → 发送。
6. 预算超限 → 暂停 → 通知 → 调整预算恢复。
7. 登录失效/UI 改版 → 降级 → 告警。
8. 新渠道 Pack 生成 → 测试 → 发布 → 绑定账号。

### 17.2 验收

1. 能在 IM 对话中完成 Ops Bot 创建、账号授权、目标/节奏/预算/审批配置。
2. 同一渠道可创建多个账号 Bot，Pack 复用，账号级隔离。
3. 有 OpenAPI 的平台优先走 API；没有则 MCP 或浏览器 recipe。
4. 新增渠道通过 Pack 完成，不改 Ops Bot 核心。
5. 公众号图文闭环可跑通（官方 API）。
6. 小红书图文闭环可跑通（本地浏览器，测试账号）。
7. 视频成片可生成并进入审批。
8. 发布、回复、视频成片默认都需人工确认；开关可按账号调整。
9. 互动只生成建议，不自动发送。
10. 每账号预算独立，超限暂停并通知。
11. 总监可跨渠道/跨账号排期、去重、分配预算。
12. 所有外发有审批与审计记录；凭据不出现在对话/日志。

---

## 18. 明确不做

- 不新建聊天系统；复用 imbot。
- 不新建执行引擎；复用 workspace/agentproxy/Epic。
- 不代持账号密码；不把密钥放进对话、Pack 或 prompt。
- 不做批量关注/点赞/私信等高风险互动。
- 不假设所有平台都有官方 API。
- 不自动通过审批；不绕过平台频次/人工复核。
- 不为每个平台写核心适配器；平台差异收敛到 Pack。
- 不默认接入第三方 SaaS 代发。
- 不做模型训练。

---

## 19. 已确定的技术选型

1. **视频生成工具链**：默认复用现有 video-gen MCP；用户可在对话中自行选择引入新的 TTS 或视频模型（走 integration / MCP 配置），系统不固定供应商。
2. **团队版云端浏览器**：每账号独立容器。
3. **预算与计价**：按 token、图片张数、视频秒数分别计价；`budget_json` 保存月度上限与币种，成本分项写 `issue_exec_events.detail_json`。
4. **小红书数据补充**：是否接入第三方合规数据源（竞品/行业）由用户通过对话决定；默认只用创作者后台数据，接入时走 `data source` 配置与 scope 授权。
5. **Pack DSL 边界**：recipe 固定动作集 `goto/wait/click/fill/upload/extract/screenshot/assert/loop/branch`；超出动作集或强反爬平台用平台级 browser adapter 插件，不改 Ops Bot 核心。
6. **Pack 分发**：指 Pack 如何到达并使用——内置 Pack 随产品发布；用户对话生成的 Pack 保存在客户环境；同一 owner/org 下的账号共享同一 Pack；跨环境通过目录导出/导入迁移；不做公共 Pack 市场。
