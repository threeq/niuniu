# 任务来源补全：CI、邮件、Webhook 设计

日期：2026-11-06 ｜ 状态：待审核（Draft for review）
关联：`2026-11-03-intake-completion-autonomy-design.md`、`2026-11-06-niuniu-ops-bot-design.md`

## 0. 一句话

把任务来源页里仍未开放的 **CI、邮件、Webhook** 三条渠道补成可接入、可测试、可审计的完整 Source：三者统一归一化为 `intake.Trigger`，走同一条 Intake 管线；邮件在个人版用 IMAP 轮询、团队版用入站解析，CI 与 Webhook 用带签名/令牌的入站推送；个人版通过本机/局域网地址或用户自备隧道解决可达性，不引入云端 relay。

---

## 1. 现状与差距

### 1.1 已有能力

| 层 | 现状 |
|---|---|
| Intake 管线 | `server/internal/intake` 已支持 `dedupe → authorize → resolve → decide → record → execute`，`Trigger` 与 `SourceKind` 已包含 `ci`、`email`、`webhook` |
| 配置 | `intake_settings` 已为五个来源种下默认行；`intakeSourceKinds` 已含三者 |
| 绑定 | `project_external_sources` 可存 `(project, provider, source_key, credential_id, config)` |
| 邮件/Webhook 入站 | `server/internal/service/webhook_source.go` 已有签名校验、`NormalizeEmail` / `NormalizeGeneric`、幂等、策略、执行；公开路由 `/api/integrations/email/:binding`、`/api/integrations/webhook/:binding` 已注册 |
| 邮件凭据 | 已有 `imap` provider 凭据与 `VerifyImapLogin` 登录探测；office-mail 场景已在使用 |
| GitHub | 轮询 + Webhook 双传输，已完整接入 |

### 1.2 为什么页面上仍显示“暂未开放接入”

1. **UI 没有接线**：`server/web/src/components/projects/intake/IntakePolicyPanel.tsx` 的 `connected` 只计算 `im_bot` 与 `github`；`intake-utils.ts` 的 `CONNECTABLE_SOURCES` 也只有这两个。`ci` / `email` / `webhook` 永远渲染为未连接。
2. **前端没有绑定 API 客户端**：`server/web/src/lib/intake-api.ts` 没有任何 email/webhook binding 方法。
3. **CI 没有适配器**：只有 source-kind 常量，没有归一化、绑定创建、公开路由。
4. **邮件缺个人版传输**：现有入站解析要求公网可达；个人版本地优先，没有 IMAP 轮询，所以即使 UI 接上也无法在无公网环境使用。
5. **个人版可达性没有产品答案**：CI/Webhook 本质是入站推送，本机没有公网地址时如何接入未定义。

### 1.3 结论

邮件/Webhook 不是“从零开发”，而是**补 UI、补个人版传输、补运维面**；CI 需要新增适配器。三者共用同一套绑定、凭据、策略与审计。

---

## 2. 目标与非目标

### 2.1 目标

- **G1 三源可用**：CI、邮件、Webhook 都能在项目设置里完成接入、测试、查看状态、删除。
- **G2 同一管线**：三者只负责归一化，鉴权、去重、日限额、模式、规则、Brief、执行、审计全部复用 Intake。
- **G3 个人版可用**：邮件走 IMAP 轮询；CI/Webhook 支持本机/局域网地址与用户自备隧道，并明确提示可达性要求。
- **G4 团队版可规模化**：邮件入站解析、CI/Webhook 公网推送，密钥与绑定按项目隔离。
- **G5 默认安全**：签名/令牌校验、常量时间比较、重放窗口、体积上限、限流、密钥只显示一次、可轮换。
- **G6 可观测**：每个绑定有最近投递、最近错误、24h 计数、去重数，失败可定位。
- **G7 对话可配置**：MCP 工具可创建/测试/轮换/删除来源；密钥通过一次性链接展示，不进入对话。
- **G8 零假连接**：没有绑定时不显示“已连接”；没有可用传输时不显示“接入”按钮。

### 2.2 非目标

- 不做邮件自动回复/发信；本设计只处理入站。
- 不为每个 CI 厂商写轮询器；GitHub Actions 已由 GitHub Source 的 `check_run` / `workflow_run` 覆盖。
- 不做 IMAP IDLE 长连接；首版用定时轮询。
- 不做个人版云端 relay。
- 不执行 Webhook 载荷中的任何指令；外部文本只作数据。
- 不做跨项目共享绑定；绑定属于项目。

---

## 3. 统一模型

### 3.1 分层

```
传输层        IMAP 轮询 / 入站 HTTP 推送
   │
归一化层      email / webhook / ci 适配器 → intake.Trigger
   │
准入层        intake.Service（去重 → 授权 → 解析任务 → 决策 → 记录 → 执行）
   │
执行层        TaskRouter + workspace + Task Brief + autohost
```

代码复用：

- 入站 HTTP：扩展现有 `WebhookSourceService.HandleInbound`，增加 `ci` 分支；email/webhook 已有分支不动。
- IMAP：新增 `EmailSourceService`，产出同样的 `intake.Trigger`。
- 三者共用现有 `webhookResolver`（按 `external_source + external_id` 续接任务）与 `webhookExecutor`（路由、Brief、投递）；差异只在传输与归一化。

### 3.2 绑定与凭据

- **绑定**继续存 `project_external_sources`，`provider` 取 `ci | email | webhook`。
- **凭据**继续存 `external_provider_credentials`，AES-GCM 加密：
  - CI/Webhook 入站：服务端生成随机 secret，provider 分别为 `ci` / `webhook`。
  - 邮件入站解析：服务端生成 secret，provider `email`。
  - 邮件 IMAP：复用已有 `imap` 凭据（host/port/username/password/security），绑定通过 `credential_id` 引用。
- **绑定 provider 与凭据 provider 可以不同**：`email` 绑定在 IMAP 模式下引用 `imap` 凭据。这是唯一例外，由服务层显式校验，不放开通用校验。
- **与“外部系统”面板隔离**：`ci` / `email` / `webhook` 绑定只能由 Intake 服务创建和删除。`ExternalSourceService.List` 过滤掉这三类 provider，`Add` / `Delete` 拒绝它们，避免它们出现在“外部系统对接”面板并被误删（否则会绕过密钥清理）。

### 3.3 Source 与传输解耦

同一个 `source_kind=email` 有 `imap` 与 `inbound` 两种传输；同一个 `ci` 只有 `push`；`webhook` 只有 `push`。传输方式存 `config.mode`，不影响下游管线。

---

## 4. 版本差异与可达性

| 来源 | 个人版默认 | 团队版默认 | 备选 |
|---|---|---|---|
| 邮件 | IMAP 轮询（出站） | 入站解析 Webhook | 个人版也可用入站（需可达） |
| CI | 入站推送到本机/局域网/隧道 | 入站推送到公网/反代 | — |
| Webhook | 入站推送到本机/局域网/隧道 | 入站推送到公网/反代 | — |
| GitHub Actions | GitHub Source 轮询 | GitHub Source Webhook | 已实现，不重复建设 |

**可达性三选一（个人版）**：

1. **本机**：CI 与本机同机（如本地 Jenkins、GitLab Runner）可直接 `http://127.0.0.1:3000/...`。
2. **局域网/VPN**：CI 服务器与牛牛同网，使用 `http://<lan-ip>:3000/...`。
3. **自备隧道/反向代理**：用户已有 cloudflared、frp、nginx 等，填写公网基地址；牛牛只展示完整 URL 并提供连通性检查，不内置隧道。

**不提供**：个人版云端 relay、自动申请公网地址、绕过签名的“开放模式”。

---

## 5. CI 源

### 5.1 事件契约

统一入口 `POST /api/integrations/ci/:binding`，JSON：

```json
{
  "id": "gitlab:123:job:456:failed",
  "event": "ci_failed",
  "provider": "gitlab",
  "repository": "acme/api",
  "branch": "main",
  "commit": "abc123",
  "pull_request": 42,
  "thread_id": "acme/api#42",
  "pipeline": { "id": "123", "name": "build", "url": "https://ci/…/123" },
  "job": { "id": "456", "name": "test", "url": "https://ci/…/456" },
  "status": "failed",
  "conclusion": "failure",
  "summary": "2 tests failed",
  "log_excerpt": "…最多 16KB…",
  "actor": { "id": "u1", "name": "Alice" },
  "url": "https://ci/…/456"
}
```

字段规则：

- `id` 必须跨重试稳定；缺失时用 `pipeline.id + job.id + status + commit` 拼。
- `event` 取 `ci_failed | ci_succeeded | ci_recovered | ci_canceled`。
- `log_excerpt` 硬截断 16KB；完整日志通过 `url` 回源，不内联。
- `thread_id` 可选；缺省按 PR → `repo#pr:<n>`，否则 `repo#branch#commit`。

### 5.2 Provider 预设与认证

| provider | 认证 | 事件映射 |
|---|---|---|
| `generic` | `X-Niuniu-Signature-256`（HMAC-SHA256，带时间戳）或 Bearer | 原样读取 |
| `gitlab` | `X-Gitlab-Token` 常量时间比较 | Pipeline / Job 事件 → `ci_*` |
| `jenkins` | `X-Niuniu-Token` 或 Bearer | Job 完成 → `ci_*` |
| `buildkite` | `X-Buildkite-Token` | build finished → `ci_*` |
| `circleci` | `Circleci-Signature` HMAC | workflow-completed → `ci_*` |
| `github_actions` | 建议直接用 GitHub Source | 若走 CI 源，用 generic + HMAC |

认证失败一律 401，不记录事件；未知 provider 404。

### 5.3 归一化

```go
Trigger{
  SourceKind: "ci",
  SourceRef:  "<binding name>",
  EventID:    <provider event id>,
  EventKind:  "ci_failed" | "ci_succeeded" | "ci_recovered" | "ci_canceled",
  Thread:     ThreadRef{ThreadExtID: <thread_id>},
  Actor:      <payload actor 或 ci:<provider> bot>,
  Text:       "<summary>\n\n<pipeline/job/branch/commit>\n\n<log excerpt>",
  SourceURL:  <job url>,
  ReceiveAll: true,
  Explicit:   false,
}
```

- **不是 explicit**：CI 是机器事件，不能因为“绑定已配置”就获得用户级授权。
- **默认只接失败**：`config.events` 缺省 `["ci_failed"]`，避免成功事件刷屏。
- **成功事件只在已绑定任务时有用**：`ci_succeeded` / `ci_recovered` 若解析到已绑定 issue，走 `resume` 投递“CI 已通过”；未绑定则 `ignore`，不建任务。

### 5.4 默认规则与决策

创建 CI 绑定时同时种一条规则（避免“出厂即空”）：

```json
{
  "name": "CI 失败自动修复",
  "instruction": "定位 CI 失败原因，提交修复，确认同一流水线通过",
  "conditions": { "event_kind": "ci_failed" },
  "priority": 100,
  "enabled": true
}
```

- 来源模式仍是 `observe`，由用户在设置页显式切 `auto`；规则存在不等于自动执行。
- 决策理由码新增 `ci_failed` / `ci_recovered`，写入 `intake_decisions.reason_code`。

### 5.5 闭环

```
CI 失败 → 新任务/续接 → agent 修复 → 推送 → CI 通过
   → ci_succeeded 事件 → resume 绑定任务 → 投递“CI 已通过”
```

- 去重键由 `intake_events` 的 `(source_kind, source_ref, event_ext_id)` 保证。
- 同一流水线连续失败：同一 `thread_id` 已绑定则续接，不重复建任务。

### 5.6 体积与限流

- 请求体上限 1MB（`log_excerpt` 已截断）。
- 每绑定 60 次/分钟，超限 429。
- 单次投递只处理一个事件；批量请逐条发送。

---

## 6. 邮件源

### 6.1 两种传输

| 模式 | 适用 | 传输 |
|---|---|---|
| `imap` | 个人版默认、自建邮箱、无公网 | 出站 IMAP 轮询 |
| `inbound` | 团队版默认、Mailgun/SendGrid/Postmark | 入站解析 Webhook |

绑定 `config.mode` 决定；同一个邮箱不要同时开两种，避免双份事件（去重能挡住，但浪费配额）。

### 6.2 IMAP 轮询

**新增 `server/internal/service/email_source.go`**：

- 复用 `imap` 凭据（`imap_host` / `imap_port` / `username` / `password` / `security`）。
- 绑定创建时调用现有 `VerifyImapLogin`，失败直接拒绝。
- IMAP 读写使用 `github.com/emersion/go-imap/v2`（新增依赖）；现有 `imap_verify.go` 只做 LOGIN 探测，不足以 FETCH。
- 轮询任务由 `scheduler` 驱动，沿用 GitHub 的注册模式：新增 `RegisterEmailPoll(expr, run)`，配置 `intake.email_poll_cron`，默认每分钟；错误按绑定指数退避至 30min。
- 只读：`BODY.PEEK[]`，不设置 `\Seen`，不删除、不移动邮件。
- 游标：`UIDVALIDITY` + `UID`；`UIDVALIDITY` 变化时重置并只取最近 24h。
- 邮箱范围：默认 `INBOX`，可配置 folder；不读 Spam/Trash。

**新增表 `email_source_poll_state`**：

```sql
CREATE TABLE IF NOT EXISTS email_source_poll_state (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    binding_id     INTEGER NOT NULL REFERENCES project_external_sources(id) ON DELETE CASCADE,
    mailbox        TEXT NOT NULL DEFAULT 'INBOX',
    uid_validity   INTEGER NOT NULL DEFAULT 0,
    last_uid       INTEGER NOT NULL DEFAULT 0,
    backoff_until  TIMESTAMP,
    last_error     TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(binding_id, mailbox)
);
```

**解析**：

- 头：`Message-ID`、`In-Reply-To`、`References`、`From`、`Subject`、`Date`、`Auto-Submitted`、`Precedence`、`Return-Path`。
- 正文：优先 `text/plain`；无则 HTML 转文本；去掉引用回复（`>` 行与 `On … wrote:` 块）；截断 64KB。
- 附件：只记录 `kind/name/mime/size`，任务真正需要时再取，不内联。
- 线程根：`References` 第一个 ID → `In-Reply-To` → 自身 `Message-ID`；回复因此能 `resume` 原任务。
- 事件 ID：`Message-ID`；缺失时 `imap:<uidvalidity>:<uid>`。

**忽略规则（默认开启）**：

- `Auto-Submitted` 非 `no` → 自动回复/系统信；
- `Precedence: bulk|junk|list` → 群发/列表；
- `Return-Path: <>` → 退信；
- 自身地址发件 → 防环。

### 6.3 入站解析

复用现有 `NormalizeEmail` 与 `/api/integrations/email/:binding`，补：

- provider 预设：`mailgun`、`sendgrid`、`postmark`、`generic`；
- 字段映射已在 `webhooksource` 覆盖（`message_id`、`body-plain`、`body`、`html`、`from`、`in_reply_to`、`references`）；
- 认证：生成的 secret 支持 Bearer 或 `X-Webhook-Signature-256`；
- 同一套忽略规则与线程规则。

### 6.4 凭据与安全

- IMAP 密码只存 credstore；绑定页只显示“已配置”。
- 绑定创建时验证登录；运行中认证失败 → 绑定状态 `error`，退避并通知。
- 不自动回复、不代发；发信能力属于 office-mail 场景，与本 Source 解耦。

---

## 7. Webhook 源

### 7.1 契约

沿用现有 generic payload：

```json
{
  "id": "evt_123",
  "event": "alert",
  "thread_id": "monitor/api",
  "actor": { "id": "u1", "name": "Alice", "is_bot": false },
  "text": "CPU 持续 95% 超过 10 分钟",
  "url": "https://monitor/incidents/123",
  "received_at": "2026-11-06T12:00:00Z",
  "attachments": [
    { "kind": "image", "name": "graph.png", "url": "https://…", "mime_type": "image/png", "size_bytes": 12345 }
  ]
}
```

- `id` 缺失时用 body hash 去重；生产调用方必须提供稳定 id。
- `text` 或附件至少一个；`thread_id` 缺省时以事件 id 作为独立线程。

### 7.2 认证与重放

现有实现支持 raw-body HMAC 与 Bearer。本设计增加：

- `X-Webhook-Timestamp`（Unix 秒）+ `X-Webhook-Signature-256 = HMAC(secret, timestamp + "." + body)`；
- 时间戳窗口 ±300s，超出 401；
- 无时间戳时退回 raw-body HMAC，保持兼容；
- 常量时间比较；
- 密钥只在创建/轮换时显示一次，支持立即轮换（旧密钥立即失效）。

### 7.3 事件过滤与线程

- `config.events` 为允许的 `event` 列表；空表示全部。
- `thread_id` 相同 → 续接同一任务；不同 → 新任务。
- `actor.is_bot=true` 不获得 explicit 授权，仍走 allow list / 规则。

### 7.4 体积与限流

- 默认 1MB；`config.max_body_bytes` 可调到 25MB 上限。
- 每绑定 60 次/分钟，超限 429。
- 响应：200 `{ok, ignored, duplicate, decision, reason_code}`；401/404/400/413/429 语义固定。

---

## 8. 安全模型

| 风险 | 对策 |
|---|---|
| 伪造投递 | HMAC/令牌 + 常量时间比较；失败 401 且不落库 |
| 重放 | 时间戳窗口 + `intake_events` 唯一键 |
| 密钥泄露 | credstore 加密；只显示一次；轮换；日志/对话/prompt 不出现 |
| 大包耗尽内存 | 按来源体积上限；`io.LimitReader` |
| 刷量 | 每绑定限流 + 每日 cap |
| 越权执行 | 外部文本不产生权限；`Explicit=false`，由 allow list/规则决定 |
| 恶意附件 | 只存元数据；按需取用，域名 allowlist |
| 个人版暴露 | 默认 loopback；公网必须签名；`local_only` 可限制来源网段 |
| 伪造来源 IP | 只信任显式配置的反向代理；默认不用 `X-Forwarded-For` 做 `local_only` 判断 |
| SSRF | 不回源任意 URL；附件/日志取用走显式 allowlist |

**Prompt injection**：邮件正文、CI 日志、Webhook text 一律视为数据；Intake 的授权由代码策略决定，模型不能因文本内容提升权限。

---

## 9. 数据模型

### 9.1 复用与新增

| 需求 | 存储 |
|---|---|
| 绑定 | `project_external_sources`（provider=ci/email/webhook） |
| 密钥 | `external_provider_credentials`（provider=ci/email/webhook/imap） |
| 事件/决策 | `intake_events` / `intake_decisions` |
| IMAP 游标 | **新增** `email_source_poll_state` |
| 投递健康 | 从 `intake_events` 聚合，不建表 |

### 9.2 绑定 config

```json
// ci
{ "mode": "push", "provider": "gitlab", "events": ["ci_failed"], "local_only": false }

// email (imap)
{ "mode": "imap", "mailbox": "INBOX", "poll_interval_sec": 60, "ignore_auto": true }

// email (inbound)
{ "mode": "inbound", "provider": "mailgun", "events": ["message"] }

// webhook
{ "mode": "push", "events": [], "max_body_bytes": 1048576, "replay_window_sec": 300 }
```

### 9.3 迁移

- 新表写 `schema.sql` 与 `schema_postgres.sql`，`migrate.go` 建索引；跑 `make schema-diff`。
- 不新增列、不改 `intake_settings` / `intake_events`。
- 存量项目无绑定，行为不变。

---

## 10. API 与 MCP

### 10.1 项目内管理 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/projects/:id/intake/sources` | 列出 ci/email/webhook 绑定与状态 |
| POST | `/api/projects/:id/intake/sources` | 创建绑定；返回一次性 secret 或 secret 链接 |
| POST | `/api/projects/:id/intake/sources/:sid/rotate` | 轮换 secret |
| POST | `/api/projects/:id/intake/sources/:sid/test` | 合成事件测试，不落库不执行 |
| DELETE | `/api/projects/:id/intake/sources/:sid` | 删除绑定与凭据 |
| POST | `/api/projects/:id/intake/sources/:sid/poll` | IMAP 立即轮询一次 |

旧的 `/api/projects/:id/intake/webhook` 保留为别名，响应转调新服务，避免移动端/旧客户端断裂。

一次性密钥页：`GET /intake/sources/secret/:token`，15 分钟有效、只展示一次；对话与 MCP 只返回该链接，不返回明文。

### 10.2 公开入站

- `POST /api/integrations/ci/:binding`
- `POST /api/integrations/email/:binding`
- `POST /api/integrations/webhook/:binding`

均在鉴权 API 组之外，靠签名/令牌授权；正文上限与限流按来源执行。

### 10.3 MCP 工具

- `intake_source_list(project_id, source_kind?)`
- `intake_source_create(project_id, source_kind, name, mode, provider, config, credential_id?)`
- `intake_source_test(source_id, sample?)`
- `intake_source_rotate(source_id)`
- `intake_source_delete(source_id)`
- `intake_source_poll(source_id)`
- `intake_source_credential_link(source_id)` — 邮件 IMAP 缺少凭据时返回一次性安全链接

**密钥不下发到对话**：`intake_source_create` 返回 `secret_link`（15 分钟一次性页面），不返回明文；`intake_source_rotate` 同理。

---

## 11. UI 设计

### 11.1 来源卡状态

`IntakePolicyPanel` 的 `connected` 增加：

```ts
out.ci = bindings.some(b => b.source_kind === 'ci')
out.email = bindings.some(b => b.source_kind === 'email')
out.webhook = bindings.some(b => b.source_kind === 'webhook')
```

- 已连接：显示启用开关、配置、待校准数，并增加一行“N 个绑定 · 最近投递 …”。
- 未连接：显示“接入”按钮，点击打开**面板内对话框**，不再跳转到其他 section。
- `CONNECTABLE_SOURCES` 对三者返回新的 `inline` 目标；`im_bot` / `github` 保持原跳转。

### 11.2 接入对话框

通用步骤：

1. **名称**：1–64 字符，项目内同 provider 唯一。
2. **来源类型与传输**：
   - CI：provider 预设 → `push`。
   - 邮件：`IMAP 轮询` / `入站解析` 二选一。
   - Webhook：`push`。
3. **配置**：
   - CI：事件类型（默认只勾失败）、是否仅本机/内网。
   - 邮件 IMAP：选择已有 `imap` 凭据，或通过现有凭据表单 / 一次性安全链接新建；邮箱文件夹；轮询间隔。密码不进入对话。
   - 邮件入站：服务商预设。
   - Webhook：事件过滤、体积上限、重放窗口。
4. **创建**：显示一次性 secret、完整入站 URL、复制按钮、示例 `curl`。
5. **测试投递**：调用 `test`，展示归一化结果与策略决策（不建任务）。
6. **完成**：卡片变为已连接。

### 11.3 绑定管理

每个来源卡的“配置”对话框内列出绑定：

- 名称、模式、provider、最近投递、24h 事件数、状态（正常/退避/错误）；
- 操作：测试、轮换密钥、立即轮询（IMAP）、删除；
- 错误详情：最近一次 401/400/超时原因。

### 11.4 空状态与可达性提示

- 个人版创建 CI/Webhook 绑定后，若配置的是公网地址但本机未检测到反代，显示提示：
  “该地址需要 CI 能访问到本机。可选：本机/局域网地址、自备隧道、或改用邮件 IMAP。”
- 不阻止创建，只提示；测试投递会给出实际失败原因。

---

## 12. 观测与运维

- **每绑定**：最近成功投递、最近失败、24h 成功/去重/拒绝数、当前退避截止。
- **每来源**：在 Mission control 的 Intake 面板增加“投递健康”分组。
- **告警**：连续 3 次 401 → 提示密钥可能配置错误；连续 3 次 400 → 提示载荷格式不符；IMAP 认证失败 → 绑定置 `error` 并通知。
- **审计**：所有决策已有 `intake_decisions`；新增 `reason_code` 值（`ci_failed`、`email_auto_ignored` 等）便于筛选。

---

## 13. 测试与验收

### 13.1 单元

- CI：各 provider 载荷 → `Trigger` 字段、事件类型、线程、事件 ID；缺字段报错。
- CI：`ci_succeeded` 未绑定 → ignore；已绑定 → resume。
- 邮件：IMAP 头解析、引用裁剪、HTML 转文本、自动信/退信忽略、UIDVALIDITY 重置。
- 邮件：`In-Reply-To`/`References` 线程根。
- Webhook：HMAC（带/不带时间戳）、重放窗口、Bearer、体积上限。
- 绑定：provider/credential 校验（email-imap 例外）、名称唯一、删除级联。
- 隔离：`ExternalSourceService.List` 不返回 ci/email/webhook；`Add` / `Delete` 拒绝这三类。

### 13.2 集成

- 创建绑定 → 测试投递 → 归一化预览与决策。
- 真实投递 → `intake_events` 落库 → observe 决策 → 待校准队列可见 → 采纳建 issue。
- 重复投递 → 200 duplicate，无新事件。
- 错误密钥 → 401，无事件。
- 超限 → 429；超大 → 413。
- IMAP 轮询 → 新邮件事件；回复邮件 → 续接同一 issue。
- CI 失败 → 任务；CI 通过 → resume 投递。
- 删除绑定 → 凭据与轮询状态级联清理。

### 13.3 验收

1. CI、邮件、Webhook 三张卡可完成接入，不再显示“暂未开放接入”。
2. 三者事件都进入同一 Intake 管线，遵守 allow list、模式、日限额、审计。
3. 邮件在无公网的个人版环境可通过 IMAP 轮询工作；回复能续接任务。
4. CI 失败能建任务/续接，成功后能 resume 通知。
5. Webhook 支持签名、重放保护、限流与体积上限。
6. 密钥只显示一次、可轮换，日志/对话/数据库中无明文。
7. 每个绑定有最近投递与错误状态；失败可定位。
8. 个人版可达性有三种明确路径，不引入云端 relay。
9. 设计中的“明确不做”没有偷偷实现或偷偷降级。

---

## 14. 明确不做

- 不做邮件发信/自动回复。
- 不做 CI 厂商轮询器（GitHub Actions 由 GitHub Source 覆盖）。
- 不做 IMAP IDLE。
- 不做个人版云端 relay。
- 不做跨项目共享绑定。
- 不做无签名的公网开放模式。
- 不执行外部载荷中的指令。

---

## 15. 已确定取舍

1. **CI 只做推送**：GitHub Actions 已有轮询；其他 CI 推送更简单、实时。个人版用本机/局域网/隧道解决可达性。
2. **邮件双传输**：个人版 IMAP 轮询，团队版入站解析；同一绑定二选一，避免双份事件。
3. **绑定复用 `project_external_sources`**：不新建绑定表；只有 IMAP 游标新增一张表。
4. **CI 默认只接失败并种默认规则**：避免成功事件刷屏，同时不出现“规则为空导致 auto 无效”。
5. **CI 事件不算 explicit**：机器事件不获得用户级授权，auto 需要规则匹配。
6. **密钥一次性展示 + 可轮换**：对话里只给一次性链接，不传明文。
7. **个人版不内置隧道**：只提供 URL 与连通性提示，避免承担公网暴露与运维责任。
