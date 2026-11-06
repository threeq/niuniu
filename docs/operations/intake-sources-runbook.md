# Intake Sources（CI / 邮件 / Webhook）：接入与运维 Runbook

> 适用版本：Epic #823 合入后的 `niuniu` 服务端（个人版 / 团队版共用同一套代码）
> 设计文档：[`docs/superpowers/specs/2026-11-06-intake-ci-email-webhook-design.md`](../superpowers/specs/2026-11-06-intake-ci-email-webhook-design.md)
> 状态：2026-11-07 编写，对应设计 §13 与 issue #1026 验收项。

本文件回答六件事：

1. 三类来源（CI / 邮件 / Webhook）如何接入。
2. 个人版没有公网时，投递地址怎么选（三选一）。
3. 密钥如何一次性交付与轮换。
4. 签名怎么算、常见 401/400/403/404/413/429 怎么排查。
5. IMAP 轮询怎么排错。
6. 出问题时按什么顺序回滚，回滚是否安全。

文中 `$BASE` 指服务地址：个人版默认 `http://127.0.0.1:3000`，团队版例如 `https://self.niu6ai.com`。管理类 `/api/projects/...` 请求在团队版需要登录态；`/api/integrations/...` 入站端点**不在**鉴权组内，签名/令牌就是鉴权。

---

## 0. 一页速查

### 0.1 统一模型

```
传输层   IMAP 轮询 / 入站 HTTP 推送
   │
归一化层 ci / email / webhook 适配器 → intake.Trigger
   │
准入层   intake.Service（去重 → 授权 → 解析任务 → 决策 → 记录 → 执行）
   │
执行层   TaskRouter + workspace + Task Brief + autohost
```

三类来源共用同一条 Intake 管线：allow list、模式（off/observe/auto）、每日上限、审计表 `intake_events` / `intake_decisions` 全部一致。外部载荷只当作**数据**，不产生任何权限。

### 0.2 来源与传输

| 来源 | `source_kind` | `mode` | 入站端点 / 传输 |
|---|---|---|---|
| CI | `ci` | `push` | `POST /api/integrations/ci/:binding` |
| 邮件（IMAP） | `email` | `imap` | 服务端出站轮询，无入站端点 |
| 邮件（入站解析） | `email` | `inbound` | `POST /api/integrations/email/:binding` |
| Webhook | `webhook` | `push` | `POST /api/integrations/webhook/:binding` |

同一个邮箱只开一种传输（IMAP 或入站），避免双份事件；去重键能挡住重复，但会浪费每日配额。

### 0.3 关键默认值

| 配置 | 默认 | 说明 |
|---|---|---|
| 新来源设置页显示模式 | `observe` | 未保存过配置时运行时空行按 **`off`** 处理（只执行显式请求） |
| `daily_cap` | `4` | 只限制 inferred/auto 自动接活；显式请求不受限 |
| CI 默认订阅事件 | `["ci_failed"]` | 成功事件需显式加入 `config.events` |
| 入站体积上限 | 1 MB | `config.max_body_bytes` 可调，硬上限 25 MB |
| 每绑定限流 | 60 次/分钟 | 超限 429，响应带 `Retry-After: 1` |
| Webhook 重放窗口 | ±300 秒 | 带时间戳的签名超出窗口即 401 |
| IMAP 轮询 cron | `*/1 * * * *` | `NIUNIU_INTAKE_EMAIL_POLL_CRON`；空字符串关闭调度 |
| IMAP 退避 | 1m → 2m → … → 30m | 单绑定独立退避，不影响其他绑定 |
| IMAP 单次批量 | 200 封 | 支持邮箱范围默认 `INBOX` |
| 连续 401 告警 | 3 次 | `alert=secret_misconfigured` |
| 连续 400 告警 | 3 次 | `alert=payload_mismatch` |

### 0.4 绑定与凭据的存储

| 需求 | 存储 |
|---|---|
| 绑定 | `project_external_sources`，`provider = ci / email / webhook` |
| 密钥 | `external_provider_credentials`（AES-GCM 加密） |
| 事件 / 决策 | `intake_events` / `intake_decisions` |
| IMAP 游标 | `email_source_poll_state` |
| 投递健康 | 从上述表 + 进程内跟踪器聚合，**不建表** |

CI/Webhook/邮件入站的密钥由服务端生成（provider 分别为 `ci` / `webhook` / `email`）。邮件 IMAP 复用已有的 `imap` 凭据（host/port/username/password/security），这是唯一“绑定 provider ≠ 凭据 provider”的例外。

> `ci` / `email` / `webhook` 三类绑定**不会**出现在「外部系统对接」面板：`ExternalSourceService.List` 过滤它们，`Add` / `Delete` 返回 `ErrIntakeSourceReserved`。这样任何通用解绑都无法绕过密钥清理。管理只能走 Intake 面板 / API / MCP。

---

## 1. 个人版可达性：三选一

个人版不内置云端 relay，也不提供“无签名开放模式”。CI 与 Webhook 的推送地址按部署环境选择：

| 方案 | 适用 | 填写的地址 |
|---|---|---|
| 1. 本机 | CI / 监控与牛牛同机（本地 Jenkins、GitLab Runner、同机脚本） | `http://127.0.0.1:3000/api/integrations/...` |
| 2. 局域网 / VPN | CI 服务器与牛牛同网段 | `http://<牛牛局域网 IP>:3000/api/integrations/...` |
| 3. 自备隧道 / 反向代理 | 已有 cloudflared、frp、nginx 等 | `https://<用户自己的域名>/api/integrations/...` |

要点：

- 牛牛只展示完整 URL 并提供连通性检查，**不申请公网地址、不代管隧道**。
- 未启用鉴权（个人版默认）时服务端强制 loopback 绑定；需要 LAN 访问请先启用鉴权或自行加反代并保持签名校验。
- 公网可达时必须使用签名 / 令牌，不能裸奔。
- 绑定的 `config.local_only = true` 只允许回环与私有网段来源；`config.ip_allow_list` 可进一步收窄；`config.trusted_proxies` 只有在直连对端是显式配置的反向代理时才采信 `X-Forwarded-For`。

邮件在个人版默认走 **IMAP 出站轮询**，完全不需要公网；团队版默认走**入站解析 Webhook**。

---

## 2. 接入步骤

### 2.1 通用：打开 Intake 设置

1. 进入项目 → 设置 → Intake（或 Mission control 的 Intake 面板）。
2. 三张来源卡（CI / 邮件 / Webhook）不再显示“暂未开放接入”。
3. 新建绑定后：
   - 页面会返回一个**一次性密钥链接** `/intake/sources/secret/<token>`，15 分钟内有效、只能打开一次；
   - 链接页展示 `secret`、`source_id`、`inbound_path`；
   - 对话、日志、数据库里都不会出现明文密钥。

API 等价操作（团队版需登录态）：

```bash
# 列出绑定
curl -s "$BASE/api/projects/$PROJECT/intake/sources"

# 创建（响应 {source, secret_link}）
curl -s -X POST "$BASE/api/projects/$PROJECT/intake/sources" \
  -H 'Content-Type: application/json' \
  -d '{"source_kind":"webhook","name":"ops-alerts","mode":"push"}'
```

### 2.2 CI

创建参数：

```json
{
  "source_kind": "ci",
  "name": "gitlab-main",
  "mode": "push",
  "provider": "gitlab",
  "config": { "events": ["ci_failed", "ci_succeeded"], "local_only": false }
}
```

- `provider` 必填，支持 `generic | gitlab | jenkins | buildkite | circleci`；未知 provider → 400。
- 创建时自动种一条默认规则「CI 失败自动修复」（`event_kind = ci_failed`），避免“出厂即空”。规则存在**不等于**自动执行：来源模式仍是 `observe`，需要用户显式切 `auto`。
- `config.events` 缺省 = 只接失败。要闭环（成功后 resume 通知）请加入 `ci_succeeded`，并为成功事件单独建一条规则（或在 allow list 中放行 CI 机器人）。
- 在 CI 侧把 Webhook 指向 `inbound_path`，认证按 provider：

| provider | 认证头 |
|---|---|
| `gitlab` | `X-Gitlab-Token: <secret>` |
| `jenkins` | `X-Niuniu-Token: <secret>` 或 `Authorization: Bearer <secret>` |
| `buildkite` | `X-Buildkite-Token: <secret>` |
| `circleci` | `Circleci-Signature: sha256=<hex HMAC-SHA256(body)>` |
| `generic` | `X-Niuniu-Timestamp` + `X-Niuniu-Signature-256: sha256=<hex>`（HMAC(secret, ts + "." + body)），或 Bearer |

CI 事件契约（generic 示例）：

```json
{
  "id": "ci-1",
  "event": "ci_failed",
  "repository": "acme/api",
  "branch": "main",
  "commit": "abc123",
  "pull_request": 42,
  "thread_id": "acme/api#42",
  "pipeline": { "id": "123", "name": "build", "url": "https://ci/…/123" },
  "job": { "id": "456", "name": "test", "url": "https://ci/…/456" },
  "status": "failed",
  "summary": "2 tests failed",
  "log_excerpt": "…最多 16KB…",
  "actor": { "id": "u1", "name": "Alice" }
}
```

- `id` 必须跨重试稳定；缺失时由 `pipeline.id + job.id + status + commit` 拼。
- `thread_id` 缺省按 PR → `repo#pr:<n>`，否则 `repo#branch#commit`；同一线程连续失败会续接同一任务。
- `log_excerpt` 硬截断 16 KB；完整日志请通过 `url` 回源，不要内联。

### 2.3 邮件：IMAP 轮询（个人版默认）

1. 先在「凭据」里创建 `imap` 凭据：`imap_host`、`imap_port`、`security`（ssl/starttls/none）、`username`、`password`。
2. 创建绑定：

```json
{
  "source_kind": "email",
  "name": "support-inbox",
  "mode": "imap",
  "credential_id": 123,
  "config": { "mailbox": "INBOX", "poll_interval_sec": 60, "ignore_auto": true }
}
```

- 创建时会做一次真实 LOGIN 探测；失败直接 400，不会留下一个每分钟都失败的绑定。
- 轮询只读：`BODY.PEEK[]`，不设 `\Seen`、不删除、不移动、不读 Spam/Trash。
- 游标 `UIDVALIDITY + UID`；`UIDVALIDITY` 变化时重置并只回看最近 24 小时。
- 线程根：`References` 第一个 ID → `In-Reply-To` → 自身 `Message-ID`。**回复因此能续接原任务**。
- 事件 ID：`Message-ID`；缺失时 `imap:<uidvalidity>:<uid>`。
- 默认忽略：`Auto-Submitted` 非 `no`、`Precedence: bulk|junk|list`、`Return-Path: <>`（退信）、自己发给自己的邮件。需要关闭时设 `config.ignore_auto=false`。
- 附件只记录 `kind/name/mime/size` 元数据，不内联正文。

轮询调度：

```yaml
intake:
  email_poll_cron: "*/1 * * * *"   # 环境变量 NIUNIU_INTAKE_EMAIL_POLL_CRON
```

置空字符串可关闭定时轮询（面板上的“立即轮询”仍可用）。

### 2.4 邮件：入站解析（团队版默认）

```json
{
  "source_kind": "email",
  "name": "inbound-support",
  "mode": "inbound",
  "provider": "mailgun",
  "config": { "events": ["message"], "self_address": "support@inbound.example.com" }
}
```

- `provider` 支持 `mailgun | sendgrid | postmark | generic`；只影响正文/头字段的别名映射，三家的线程与忽略规则一致。
- 认证：`Authorization: Bearer <secret>` 或 `X-Webhook-Signature-256`（raw-body HMAC）。
- Mailgun 样例：

```json
{
  "from": "Alice Example <alice@example.test>",
  "to": "support@inbound.example.test",
  "subject": "Deploy staging",
  "body-plain": "please deploy v3",
  "Message-Id": "<mailgun-2@example.test>",
  "message-headers": "[[\"From\", \"Alice Example <alice@example.test>\"], [\"In-Reply-To\", \"<mailgun-1@example.test>\"], [\"References\", \"<mailgun-root@example.test> <mailgun-1@example.test>\"], [\"Auto-Submitted\", \"no\"]]",
  "timestamp": "1762300000"
}
```

- `References` 的第一个 ID 作为线程根，和 IMAP 路径完全一致。
- 自动回复、群发、退信、自发邮件同样被忽略，不落库。

### 2.5 Webhook

```json
{
  "source_kind": "webhook",
  "name": "ops-alerts",
  "mode": "push",
  "config": { "events": [], "max_body_bytes": 1048576, "local_only": true }
}
```

载荷（generic）：

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

- `id` 缺失时用 body hash 去重；生产调用方必须提供稳定 `id`。
- `text` 或附件至少一个，否则 400。
- `thread_id` 相同 → 续接同一任务；不同 → 新任务。
- `config.events` 为空表示接收全部事件。

---

## 3. 密钥：一次性展示与轮换

### 3.1 一次性展示

- 创建 / 轮换的响应**只返回 `secret_link`**，不返回明文。
- 链接 15 分钟有效、单次使用；打开后即失效。刷新、第二标签页、重放都会得到 410。
- 服务端重启会使未打开的链接作废（明文只存在内存里）。
- 密钥只存在加密凭据表中；日志、对话、prompt、MCP 返回里都不出现明文。

### 3.2 轮换

```bash
curl -s -X POST "$BASE/api/projects/$PROJECT/intake/sources/$SID/rotate"
# → {"source": {...}, "secret_link": "/intake/sources/secret/<token>"}
```

- 轮换后**旧密钥立即失效**（凭据配置整体覆盖），无需重启。
- CI / Webhook / 邮件入站都可轮换；邮件 IMAP 绑定没有服务端生成的密钥，轮换返回 400（`this source has no rotatable secret`）。
- 轮换后记得同步更新上游 CI / 邮件服务商 / 监控系统的配置。

### 3.3 删除

- 删除绑定会级联清理 `email_source_poll_state` 与该绑定**自有的**生成密钥。
- 邮件 IMAP 绑定的 `imap` 凭据属于用户，删除绑定时**保留**，因为可能还有其他场景引用它。

---

## 4. 签名样例与手工投递

### 4.1 带时间戳的 HMAC（generic webhook / generic CI）

```bash
SECRET='<从一次性链接取得的密钥>'
BINDING='123'
BODY='{"id":"evt-1","thread_id":"monitor/api","actor":{"id":"alice","name":"Alice"},"text":"CPU 95%"}'
TS=$(date +%s)
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $2}')

curl -sS -X POST "$BASE/api/integrations/webhook/$BINDING" \
  -H "X-Webhook-Timestamp: $TS" \
  -H "X-Webhook-Signature-256: sha256=$SIG" \
  -H 'Content-Type: application/json' \
  --data-raw "$BODY"
```

- 时间戳超出当前时间 ±300 秒 → 401。
- 带了时间戳就必须带时间戳签名；不会回退到别的方式，避免重放。
- 无时间戳时兼容 raw-body HMAC：`printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex`，头为 `X-Webhook-Signature-256: sha256=<hex>`。

### 4.2 Bearer（webhook / email / jenkins）

```bash
curl -sS -X POST "$BASE/api/integrations/webhook/$BINDING" \
  -H "Authorization: Bearer $SECRET" \
  -H 'Content-Type: application/json' \
  --data-raw "$BODY"
```

### 4.3 GitLab CI

```bash
curl -sS -X POST "$BASE/api/integrations/ci/$BINDING" \
  -H "X-Gitlab-Token: $SECRET" \
  -H 'Content-Type: application/json' \
  --data-raw '{"object_kind":"pipeline","object_attributes":{"id":123,"status":"failed","ref":"main","sha":"abc"},"project":{"path_with_namespace":"acme/api"},"merge_request":{"iid":42},"user":{"id":7,"name":"Alice"}}'
```

### 4.4 CircleCI

```bash
BODY='{"status":"failed"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $2}')
curl -sS -X POST "$BASE/api/integrations/ci/$BINDING" \
  -H "Circleci-Signature: sha256=$SIG" \
  -H 'Content-Type: application/json' \
  --data-raw "$BODY"
```

### 4.5 响应

成功（含被忽略 / 重复）：

```json
{ "ok": true, "ignored": false, "duplicate": false, "decision": "accept", "reason_code": "allow_list" }
```

| 状态 | 含义 | 是否落库 |
|---|---|---|
| 200 | 已处理（`decision` 可能是 `accept` / `resume` / `ignore`） | 是（ignore 也记录决策） |
| 401 | 签名/令牌错误 | 否 |
| 403 | 来源地址被 `local_only` / `ip_allow_list` 拒绝 | 否 |
| 404 | 绑定不存在，或 provider 与路由不匹配 | 否 |
| 400 | 载荷无法归一化 | 否 |
| 413 | 超过体积上限 | 否 |
| 429 | 超过每绑定限流 | 否 |

### 4.6 测试投递（dry run）

```bash
curl -sS -X POST "$BASE/api/projects/$PROJECT/intake/sources/$SID/test" \
  -H 'Content-Type: application/json' \
  -d '{"sample":{"object_kind":"pipeline","object_attributes":{"id":9,"status":"failed","ref":"main","sha":"abc"},"project":{"path_with_namespace":"acme/api"},"user":{"id":7,"name":"Alice"}}}'
```

响应包含归一化后的 `event_kind`、`actor_ext_id`、`thread_ext_id`，以及策略判定 `mode / strength / allowed / reason_code / matched_rule_ids`。**不写事件、不写决策、不建任务、不投递**。传纯文本时会按文本评估并返回 `warnings`。

---

## 5. 限流与体积

| 项 | 规则 |
|---|---|
| 每绑定限流 | 60 次/分钟（burst 60），超限 429 + `Retry-After: 1` |
| 默认体积 | 1 MB |
| 最大体积 | 25 MB（`config.max_body_bytes` 超过会被截到 25 MB） |
| GitHub 入站 | 固定 25 MB |
| 批量 | 单次投递只处理一个事件；批量请逐条发送 |

- 限流在认证之后执行，所以伪造请求（401）不会消耗配额。
- 体积检查在认证之前：超大载荷先得 413，不会泄露绑定是否存在。
- 每日上限（`daily_cap`）是另一层：只限制 inferred/auto 自动接活，显式请求不受限。超限决策的 `reason_code = daily_cap`。

---

## 6. 常见错误码与排查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| 401 `unauthorized` | 密钥错误 / 轮换后仍用旧密钥 / 时间戳超窗 / 签名算法或头部不对 | 对照 §4 重算签名；确认上游已更新为最新密钥；确认时钟同步 |
| 400 `invalid payload` | JSON 结构不符；generic webhook 缺 `text` 与附件；CI provider 与载荷不匹配 | 用“测试投递”验证归一化；CI 的 `provider` 要与载荷格式一致（GitLab 用 `object_attributes`） |
| 403 `forbidden` | `local_only` 或 `ip_allow_list` 拒绝 | 查看来源 IP；局域网场景改用 LAN 地址或关闭 `local_only`；反代场景配置 `trusted_proxies` |
| 404 `not found` | 绑定 id 不存在 / 已删除 / provider 与路由不匹配 | 确认 `inbound_path`；CI 绑定只能投 `/api/integrations/ci/:binding` |
| 413 | 载荷超限 | 调大 `config.max_body_bytes`（≤25 MB）或截断 `log_excerpt` |
| 429 | 单绑定超过 60 次/分钟 | 降低发送频率；上游批量事件逐条排队；确认不是重试风暴 |
| 200 但 `decision=ignore` | 事件类型未订阅 / 策略判定不通过 / 线程未绑定（CI 成功）/ 重复 | 看 `reason_code`：`unbound`、`no_matching_rule`、`off_inferred`、`duplicate`、`daily_cap`、`denied` 等 |
| 连续 401 | 密钥可能配置错误 | 健康面板 `alert=secret_misconfigured`；重新轮换并更新上游 |
| 连续 400 | 载荷格式不符 | 健康面板 `alert=payload_mismatch`；用测试投递对比归一化结果 |

决策理由码速查：`allow_list` / `bot_list` / `rule` / `direct_request` / `unlisted_actor` / `denied` / `duplicate` / `observed` / `accepted` / `resumed` / `off_inferred` / `no_matching_rule` / `daily_cap` / `disabled` / `thread_ended` / `stale` / `unbound`。

> `stale`：事件时间超过容忍窗口（重连后平台回放积压）会被记录但不执行。检查上游时钟与重试策略。

---

## 7. IMAP 排错

### 7.1 先看健康面板

每个绑定展示：

- 最近成功时间、最近失败时间；
- 24h 成功 / 去重 / 拒绝数；
- 连续失败次数；
- 当前退避截止时间；
- 最近错误 `last_error`；
- 状态：`never`（从未投递）、`ok`、`degraded`（退避中或 1–2 次失败）、`error`（3+ 次失败或 IMAP 认证失败）。

`error` 且 `alert=imap_auth_failed` 表示 LOGIN 被拒，绑定不会继续正常轮询，需要更新凭据。

### 7.2 常见原因

| 现象 | 原因 | 处理 |
|---|---|---|
| 创建绑定直接 400 | LOGIN 探测失败 | 检查 host/port/security/username/password；确认邮箱已开启 IMAP；部分服务商需要“应用专用密码” |
| 状态 `error`，`last_error` 含 `imap login rejected` | 密码过期 / 应用密码被撤销 / 账号锁定 | 更新 `imap` 凭据；保存后下一轮轮询自动恢复 |
| 一直 `never`，没有事件 | 轮询 cron 关闭；邮箱没有新邮件；邮件只落在 Spam/Trash | 检查 `NIUNIU_INTAKE_EMAIL_POLL_CRON`；面板点“立即轮询”；确认只读 `INBOX` |
| 有邮件但无事件 | 被忽略规则过滤 | 检查 `Auto-Submitted`、`Precedence`、`Return-Path`、是否自发；必要时临时设 `ignore_auto=false` 验证 |
| 反复重扫旧邮件 | `UIDVALIDITY` 变化 | 正常保护行为：重置后只回看最近 24 小时；确认邮箱没有被服务端重建 |
| 退避时间越来越长 | 连续传输失败 | 修复网络/认证后，一次成功的轮询会清除错误并恢复 1 分钟基线 |

### 7.3 只读与安全边界

- 轮询使用 `BODY.PEEK[]`，绝不修改已读状态；不执行 STORE/EXPUNGE/MOVE。
- 解析失败的单封邮件会被跳过，游标继续前进，不会卡住整个邮箱。
- 批内中途失败只保存已成功处理到的 UID，下一轮从断点继续。
- 绑定创建时的 LOGIN 探测受 SSRF 防护约束：个人版允许本地邮件服务器，托管版默认拒绝回环/私网地址。

---

## 8. 观测

- **每绑定**：最近成功/失败、24h 成功/去重/拒绝、当前退避、`last_error`、连续失败数。
- **每来源**：Mission control 的 Intake 面板「投递健康」分组。
- **告警**：连续 3 次 401 → `secret_misconfigured`；连续 3 次 400 → `payload_mismatch`；IMAP 认证失败 → `imap_auth_failed`，绑定置 `error`。
- **审计**：所有决策都在 `intake_decisions`，可按 `source_kind`、`decision`、`reason_code` 筛选。

常用查询：

```sql
-- 最近 24h 各来源投递结果
SELECT e.source_kind, d.decision, d.reason_code, COUNT(*)
FROM intake_events e
LEFT JOIN intake_decisions d ON d.event_id = e.id
WHERE e.received_at >= datetime('now', '-1 day')
GROUP BY e.source_kind, d.decision, d.reason_code
ORDER BY 4 DESC;

-- 某绑定的 IMAP 游标与错误
SELECT mailbox, uid_validity, last_uid, backoff_until, last_error, updated_at
FROM email_source_poll_state WHERE binding_id = ?;
```

---

## 9. 暂停与回滚

### 9.1 先暂停，再删

1. **暂停项目接活**：项目级 stop switch 会让所有来源按 `off` 处理（显式请求仍可执行）。
2. **停用单个来源**：把该来源模式设为 `off`（或 `enabled=false`），入站事件会继续记录但不再执行自动工作。
3. **关闭 IMAP 调度**：`NIUNIU_INTAKE_EMAIL_POLL_CRON=""`（空字符串）后重启，定时轮询停止；面板的“立即轮询”仍可用。
4. **删除绑定**：在 Intake 面板删除，或 `DELETE /api/projects/$PROJECT/intake/sources/$SID`。删除会清理游标与该绑定自有的生成密钥。

### 9.2 数据层回滚

本 Epic **只新增一张表** `email_source_poll_state`，没有修改既有表结构；存量项目无绑定，行为不变。

- 回滚绑定：删除 `project_external_sources` 中 `provider in ('ci','email','webhook')` 的目标行，再删除对应 `external_provider_credentials` 生成凭据（IMAP 用户凭据保留）。
- 回滚事件：`intake_events` / `intake_decisions` 按 `source_kind` + `source_ref` 删除；`source_ref` 形如 `<name>/<binding_id>`。
- 回滚游标：`DELETE FROM email_source_poll_state WHERE binding_id = ?`。
- 回滚调度：清空 `NIUNIU_INTAKE_EMAIL_POLL_CRON`。
- 不需要回滚数据库 DDL：`email_source_poll_state` 留着也不会影响旧版本（旧版本不读它）。

### 9.3 安全回滚顺序

1. 停调度 / 设 `off`（止血）。
2. 轮换或删除泄漏的密钥（`rotate` 会让旧密钥立即失效）。
3. 删除绑定与事件（如需清理数据）。
4. 检查上游 CI / 邮件服务商 / 监控系统已停止向旧地址投递。

回滚是安全的：三类来源共用统一管线，删除绑定不会影响其他来源；IMAP 用户凭据不会被误删。

---

## 10. 明确不做（避免误用）

- 不做邮件发信 / 自动回复。
- 不做 CI 厂商轮询器（GitHub Actions 用 GitHub Source）。
- 不做 IMAP IDLE。
- 不做个人版云端 relay。
- 不做跨项目共享绑定。
- 不做无签名的公网开放模式。
- 不执行外部载荷中的指令（正文、日志、邮件都只是数据）。

---

## 11. 设计 §13.3 验收对照

| # | 验收项 | 如何验证 |
|---|---|---|
| 1 | 三张卡可完成接入，不再显示“暂未开放接入” | Intake 面板新建 CI / 邮件 / Webhook 绑定成功；`POST /intake/sources` 返回 201 + `secret_link` |
| 2 | 三者进入同一管线，遵守 allow list / 模式 / 日限额 / 审计 | `TestIntakeSourcesE2E_*`；`intake_events.source_kind` 覆盖三类；`intake_decisions.reason_code` 可见 |
| 3 | 个人版无公网可用 IMAP；回复续接任务 | `TestIntakeSourcesE2E_IMAPNewMailAndReplyResume`；IMAP 出站轮询无需入站 port |
| 4 | CI 失败建任务/续接，成功 resume | `TestIntakeSourcesE2E_CIFailureFixLoop`；`external_source=ci`、`decision=resume` |
| 5 | Webhook 支持签名、重放保护、限流、体积上限 | `TestIntakeSourcesE2E_WebhookSignatureDuplicateAndForgery`、`TestIntakeSourcesE2E_RateLimitAndBodyCap`；HTTP 状态见 `internal/api/integration_webhook_test.go` |
| 6 | 密钥只显示一次、可轮换、日志/对话/DB 无明文 | `TestIntakeSourcesE2E_RotateSecretRetiresOldSecret`；API 只返回 `secret_link`；凭据表 AES-GCM |
| 7 | 每个绑定有最近投递与错误状态，失败可定位 | 健康面板 + `TestIntakeSourceHealth_*`；`last_error`、`backoff_until`、`alert` |
| 8 | 个人版可达性三种明确路径，无云端 relay | 本文 §1；代码中没有 relay / 隧道实现 |
| 9 | “明确不做”没有被偷偷实现或降级 | 本文 §10；无发信、无厂商轮询器、无 IDLE、无跨项目绑定、无免签名开放模式 |

---

## 12. 相关测试

```bash
cd server
go test ./internal/intake/... ./internal/webhooksource/... ./internal/service/... ./internal/api/... -race
cd ..
make schema-diff
make sqlc
cd server/web && pnpm lint && pnpm test:run && pnpm build
```

专项：

```bash
cd server
go test ./internal/service/ -run 'TestIntakeSourcesE2E' -race -v
go test ./internal/api/ -run 'TestIntegrationWebhook' -v
```
