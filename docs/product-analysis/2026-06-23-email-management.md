# 邮件管理功能需求分析：牛牛是否需要支持？

> 状态：分析结论（issue #454）｜日期：2026-06-23
> 结论：**不把"邮件管理"做成原生核心功能；办公场景的邮件需求通过"邮件 MCP 场景化接入"承接（条件性、最小可行）。**

## 1. 前提：牛牛的产品定位与扩展模型

牛牛是一个**本地优先的 AI Agent 编排工作站**——核心价值是"并行驱动多个 Claude/Codex 会话 + 工作空间隔离 + 看板/定时托管"。它扩展能力的方式**不是往 Go 核心堆功能**，而是通过 **scenes（场景：curated MCP server + plugins/skills）** 注入。

两个易混淆点需先排除：

- **现有 `inbox`（`server/internal/service/inbox.go`、MCP `inbox_send/inbox_read`）不是电子邮件**，而是 **agent 之间**的内部消息（per-workspace JSON 文件，用于多 agent 协作）。
- 用户表的 `email` 字段只是**账户登录身份**，与邮件功能无关。

因此"是否支持邮件管理"是一项**全新能力**的取舍，不能用现有 inbox/email 字段充数。

## 2. 需求侧：办公场景确有邮件需求（成立）

牛牛已明确向办公场景延伸——内置 `office-doc`（Word/Excel/PPT/PDF 生成）、`office-design`、`writing-studio`、`data-analysis`、`market-research` 等场景，并为非技术办公用户提供"办公助手沟通风格"。

在该定位下，邮件是办公工作流中**最高频的相邻场景**：

- 收件箱摘要 / 快速三连看（重要、待回、可归档）；
- 起草与润色回复；
- 按规则分类、打标、归档；
- 从邮件抽取待办、转成文档/周报（与 `office-doc` 天然联动）。

**需求真实存在。**

## 3. 供给侧：原生自建邮件管理是错误做法（否决）

| 维度 | 为什么不该原生自建 |
|---|---|
| 定位错配 | 邮件客户端（收件箱 UI、账户、IMAP/SMTP、推送同步）是另一个产品品类，自建会稀释"agent 编排工作站"的焦点 |
| 架构冲突 | 牛牛扩展正路是 scenes + MCP，而非往核心 Go server 塞协议栈；邮件作为外部 SaaS（Gmail/Outlook/Exchange）天然适合 MCP 接入 |
| 成本/合规 | 自建需长期维护 IMAP/SMTP/OAuth、增量同步、附件、多账户、反垃圾；把真实邮箱凭证与邮件正文落进 SQLite/PG，在本地优先 + 多租户下是重大安全与合规面，ROI 低 |

## 4. 推荐方案（条件性、最小可行、符合架构）

**不在核心做邮件子系统。** 当用户调研确认办公用户确有"让 agent 处理邮件"的强需求时，按场景化方式承接。落地形态与 `office-doc.yaml` 完全同构，几乎零核心改动：

1. **新增内置场景 `office-mail`**：projection 一个邮件 MCP server（Gmail / Outlook / IMAP 类）；
2. 凭证走**既有加密通道**（`credstore` / `external_provider` + AES-GCM），不新建邮件存储；
3. 配套 `quick_actions` + `prompts`：收件箱摘要、起草回复、分类打标、抽取待办→联动 `office-doc` 出文档；
4. **明确不做**：常驻邮件同步守护进程、完整收件箱 UI、核心层邮件持久化。

## 5. 一句话结论

邮件需求成立，但答案是"**通过邮件 MCP 场景接入**"，而**不是**让牛牛自己变成一个邮件客户端。后续是否落地 `office-mail` 场景，取决于办公用户对"agent 处理邮件"的真实强度——建议先以一次用户调研/小流量场景验证为前置条件，再决定排期。

## 6. MCP 工具选型（通用 IMAP/SMTP）

聚焦不绑定厂商的通用 IMAP/SMTP 方案，主力两个候选：

| 维度 | codefuturist/email-mcp | nikolausm/imap-mcp-server |
|---|---|---|
| 工具面 | 47 个（含日历/提醒/标签/IDLE 监听） | 收发/回复/转发/文件夹/附件/批量删，克制 |
| 账户模型 | **多账户同进程** | 单账户（env 驱动） |
| 配置来源 | **全局 TOML `~/.config/email-mcp/config.toml`** + env | 纯 env / 自带 AES-256 加密存储 |
| 后台行为 | **常驻 IMAP IDLE watcher**（长连接守护） | 无常驻守护，调用即用 |
| 对团队版含义 | 多账户+全局 TOML+常驻连接 = 跨租户状态/泄漏风险 | 单账户+env+无状态 = 贴合 per-workspace 投射 |

~~**结论：团队版选 `nikolausm/imap-mcp-server`。**~~（**v3 已 supersede，见下"选型修订"**）它单账户、纯 env 驱动、无常驻守护，能"一个 workspace 一个进程、只见自己注入的 env"；工具面更小，对非技术用户的误操作面也更小。
email-mcp 的多账户/IDLE/日历在多租户里恰是负债（多账户同进程 = 多人邮箱挤一个进程；全局 TOML = 多租户共享一个文件；IDLE 守护 = 临时 workspace 里挂长连接），更适合**个人版/单用户本地**重度玩家。

### 6.2 选型修订（v3，落地核实 + 多账户需求）

落地前对 GitHub 上"读自有邮箱的开源 IMAP MCP"逐个核实源码，**改选 `ai-zerolab/mcp-email-server`**：

| 库 | ⭐ | 运行时 | 配置 | 认证 | 工具 | 备注 |
|---|---|---|---|---|---|---|
| **ai-zerolab/mcp-email-server**（选定） | 263 | Python/uvx | **env（单账户）+ TOML `[[emails]]`（多账户）**，`CONFIG_PATH` 可覆盖 | IMAP basic | 11 | 文档化、活跃、配置路径可指向 per-workspace 文件 |
| nikolausm/imap-mcp-server（原选） | 45 | Node/npx | 加密 accounts.json+.key（无路径覆盖） | IMAP basic | 36 | 须复刻 AES + 劫持 HOME，耦合脆弱 |
| marlinjai/email-mcp | 14 | Node | 交互向导 + 机器密钥 | **OAuth(Gmail/Outlook)** | 25 | headless 无法物化，太新 |
| GongRzhe/Gmail-MCP-Server | 1.1k | Node | OAuth 文件 | OAuth Google | 18 | 已归档 + 仅 Gmail |
| Mailtrap/Mailgun/Brevo/SES 等 | — | 托管 | API key | — | — | 只发信 SaaS，不读收件箱 |

**为何 ai-zerolab 胜出**：(1) **多账户/不同域名**经 TOML `[[emails]]` 原生支持，正是本期需求；(2) env 仅单账户，多账户须落 TOML，但 ai-zerolab 用**明文 TOML + 官方 `MCP_EMAIL_SERVER_CONFIG_PATH`**（指向 per-workspace 文件即隔离），而 nikolausm 须**复刻其 AES-256-CBC 加密 + 劫持 HOME**——多账户场景两者都物化，但 ai-zerolab 物化更轻更稳、无内部耦合；(3) 263⭐ vs 45⭐ 维护度更高、配置文档化。**代价**：运行时由 node 变 **uv（更重，须探测门禁）**、工具较少（11 vs 36，缺转发/批量/垃圾管理）。

落地机制：投射时把用户绑定的所有 imap 凭证解密 → 写每 workspace `config.toml`（`[[emails]]`，明文 0600，`incoming`=IMAP、`outgoing`=SMTP）→ 注入 `MCP_EMAIL_SERVER_CONFIG_PATH`；写操作由 `external_write_prefs(provider=imap)` 门禁（关=不写 outgoing 只读 + `permissions.deny` 禁删/移/标记）。

### 6.1 认证现实矩阵（重要，影响目标人群覆盖）

"通用 IMAP/SMTP + 用户名密码"对**主流办公邮箱并不开箱可用**，这是定位级风险，必须诚实标注：

| 邮箱类型 | basic-auth(IMAP 密码) 现状 | v1 是否可用 |
|---|---|---|
| 自建/企业 IMAP（dovecot、Zimbra 等） | 通常允许 | ✅ 可用 |
| Gmail / Google Workspace | 关闭"低安全应用"，需开 **2FA + 应用专用密码**，否则仅 OAuth | ⚠️ 开 app password 才可用 |
| Outlook.com / Microsoft 365 | 微软已弃用 Exchange Online basic-auth，默认**关闭** IMAP basic-auth，需 OAuth2(XOAUTH2) | ❌ v1 不支持（除非管理员显式开 IMAP） |
| 仍允许 basic-auth 的服务商（部分 IMAP 提供商） | 允许 | ✅ 可用 |

**结论修正**：（**v3：选 `ai-zerolab/mcp-email-server`，认证现实不变——同为 IMAP basic-auth**）必须把 **OAuth2 / XOAUTH2 列为明确的后续里程碑**，并在绑定表单提供 app-password 引导。面向"自建/企业 IMAP + 开了 app password 的 Gmail"；M365/默认 Gmail OAuth-only 场景明示暂不支持，不能假装 basic-auth 能覆盖主流办公邮箱。

## 7. 团队版隔离设计

复用牛牛现成的多租户链路，分四层：

1. **凭证层**：邮箱凭证经 `ExternalCredentialService` 存入 `external_provider_credentials`，键为 `(owner_type, owner_id, user, provider, alias)`，**AES-GCM 加密、仅投射时解密**。凭证绝不写进 scene yaml / 全局文件。个人邮箱 `owner=user`；客服 `support@` 共享邮箱 `owner=org`。
2. **场景层**：`office-mail.yaml` 用 `required_credentials: [{alias: mailbox, provider: imap}]` 只声明"需要凭证"，不含值。投射时 `findMissingCredentials` 检测缺失并提示该用户绑定自己的邮箱。
3. **进程/文件层**：每 workspace 独立 per-owner 目录 + 独立 `.mcp.json` + 独立 claude home overlay；邮件 MCP 进程 per-workspace 启动。**v3：凭证经投射物化到每 workspace `config.toml`（`MCP_EMAIL_SERVER_CONFIG_PATH` 指向之，0600，明文）**，只能看到本 workspace 的账户。无宿主机级共享配置 → 不存在跨租户读到别人邮箱。
4. **注销联动**：`OrgService.RemoveMember` 已会终止 agent / 断 WS / 清 authz 缓存。员工离职即自动失去公司邮箱场景，零额外开发。

**红线**：所选 MCP 必须能 100% 由 per-workspace 配置（env 或 `CONFIG_PATH` 指向的 per-workspace 文件）驱动、无宿主机级共享状态——**v3 选 ai-zerolab 正因其 `CONFIG_PATH` 可覆盖 + 多账户 TOML，满足此红线且最轻量**。

### ⚠️ 关键缺口（实现必须先补）

经代码核查，`required_credentials` 目前**只做存在性校验**（`scene_projection_apply.go: findMissingCredentials`），并**不会把解密后的密钥注入到第三方 MCP server 的 env**。投射出的 `.mcp.json` env 来自 scene 的 `mcp[].config.env` / `env_presets`——那是对所有人相同的**静态值**，不能放每用户密钥。

因此"把某用户加密邮箱密码解密后注入到他那个 workspace 的 `.mcp.json` env"这条机制**当前不存在**，是本功能的实现核心，详见实现方案：`docs/superpowers/specs/2026-06-23-office-mail-scene-design.md`。
