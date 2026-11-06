# 任务来源补全：CI、邮件、Webhook 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把任务来源页的 CI、邮件、Webhook 三条渠道补成完整可用的 Source：可接入、可测试、可观测、可审计；三者统一进入 Intake 管线；个人版邮件走 IMAP 轮询，CI/Webhook 支持本机/局域网/隧道推送；团队版走公网入站。

**Architecture:** 复用现有 `intake.Service` 管线、`project_external_sources` 绑定、`external_provider_credentials` 凭据、`webhookResolver` / `webhookExecutor`。新增通用 `IntakeSourceService` 管理三类绑定；CI 只加归一化与入站分支；邮件新增 IMAP 轮询器与一张游标表；Webhook 加固签名/重放/限流。前端只改 Intake 面板与设置页，不改看板。

**Tech Stack:** Go 1.25、Gin、sqlc、SQLite/PostgreSQL、`github.com/emersion/go-imap/v2`、`golang.org/x/time/rate`、React 19、TanStack Query、shadcn/ui。

**设计文档：** `docs/superpowers/specs/2026-11-06-intake-ci-email-webhook-design.md`

## Global Constraints

- 任务编号只表示依赖顺序，不表示里程碑；每个任务交付最终行为，不允许占位实现或“以后补”。
- 三者只做归一化；去重、授权、日限额、模式、规则、Brief、执行、审计全部复用 Intake 管线。
- 绑定统一存 `project_external_sources`（provider=`ci|email|webhook`）；密钥统一存 `external_provider_credentials`。
- `ci/email/webhook` 绑定不得出现在“外部系统对接”面板；`ExternalSourceService.List/Add/Delete` 必须隔离。
- 密钥只在创建/轮换时显示一次；日志、对话、MCP 返回值不得出现明文。
- 外部文本只作数据；授权由代码策略决定，任何载荷内容不能提升权限。
- 双数据库：DDL 同时写 `schema.sql` 与 `schema_postgres.sql`；跑 `make schema-diff`。
- sqlc：改 `store/queries/*.sql` 后跑 `make sqlc`；查询注释纯 ASCII。
- 每任务结束前跑对应测试并提交；提交信息不添加 `Co-Authored-By`。

## 目标结构

```
server/internal/service/
  intake_source.go        # 通用绑定 CRUD / 创建 / 轮换 / 测试 / 健康
  email_source.go         # IMAP 轮询、解析、游标
  webhook_source.go       # 扩展现有入站：CI 分支、Webhook 加固
server/internal/webhooksource/
  webhook.go              # 扩展现有 NormalizeEmail / NormalizeGeneric
  ci.go                   # CI 归一化与 provider 预设
server/internal/api/
  intake_source.go        # 项目内绑定管理 API
  integration_webhook.go  # 增加 CI 公开路由
server/internal/scheduler/
  scheduler.go            # RegisterEmailPoll
server/cmd/niuniu-mcp/
  intake_source_tools.go  # intake_source_* 工具
server/web/src/
  lib/intake-api.ts       # 绑定 API 客户端
  components/projects/intake/connect-source-dialog.tsx
  components/projects/intake/source-bindings.tsx
  pages/intake-secret.tsx # 一次性密钥页
```

---

## Task 1: 数据模型与通用绑定服务

**Files:**
- Modify: `server/internal/store/schema.sql`
- Modify: `server/internal/store/schema_postgres.sql`
- Modify: `server/internal/store/migrate.go`
- Create: `server/internal/store/queries/email_source.sql`
- Create: `server/internal/service/intake_source.go`
- Modify: `server/internal/service/external_source.go`
- Test: `server/internal/service/intake_source_test.go`
- Test: `server/internal/store/migrate_email_source_test.go`

**Interfaces:**
- Produces: `IntakeSourceService`：`List`、`Create`、`Delete`、`RotateSecret`、`TestDelivery`、`PollNow`、`Health`。
- Produces: `IntakeSourceDTO`、`IntakeSourceInput`、`IntakeSourceStatus`。
- Produces: `store` 查询 `GetEmailPollState`、`UpsertEmailPollState`、`UpdateEmailPollCursor`、`SetEmailPollBackoff`、`ListEmailPollStatesByProject`。

- [ ] **Step 1: 写迁移测试（先失败）**

```go
func TestEmailSourcePollStateSchema(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='email_source_poll_state'`).Scan(&n))
	require.Equal(t, 1, n)
	ok, err := columnExists(db, "email_source_poll_state", "last_uid")
	require.NoError(t, err)
	require.True(t, ok)
}
```

- [ ] **Step 2: 双写 DDL**

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
CREATE INDEX IF NOT EXISTS idx_email_poll_project ON email_source_poll_state(project_id);
```

PostgreSQL 用 `BIGSERIAL`、`TIMESTAMPTZ`；`migrate.go` 中建索引。

- [ ] **Step 3: 写 sqlc 查询**

```sql
-- name: GetEmailPollState :one
SELECT * FROM email_source_poll_state WHERE binding_id = ? AND mailbox = ?;

-- name: UpsertEmailPollState :one
INSERT INTO email_source_poll_state (project_id, binding_id, mailbox, uid_validity, last_uid)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(binding_id, mailbox) DO UPDATE SET
    uid_validity = excluded.uid_validity,
    last_uid = excluded.last_uid,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: UpdateEmailPollCursor :execrows
UPDATE email_source_poll_state SET
    uid_validity = ?, last_uid = ?, backoff_until = NULL,
    last_error = '', updated_at = CURRENT_TIMESTAMP
WHERE binding_id = ? AND mailbox = ?;

-- name: SetEmailPollBackoff :execrows
UPDATE email_source_poll_state SET
    backoff_until = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP
WHERE binding_id = ? AND mailbox = ?;

-- name: ListEmailPollStatesByProject :many
SELECT * FROM email_source_poll_state WHERE project_id = ? ORDER BY binding_id, mailbox;
```

- [ ] **Step 4: 实现通用服务**

```go
type IntakeSourceInput struct {
	ProjectID    int64
	SourceKind   string // ci | email | webhook
	Name         string
	Mode         string // push | imap | inbound
	Provider     string
	Config       map[string]any
	CredentialID int64 // email imap
	CallerUserID int64
}

type IntakeSourceDTO struct {
	ID          int64          `json:"id"`
	ProjectID   int64          `json:"project_id"`
	SourceKind  string         `json:"source_kind"`
	Name        string         `json:"name"`
	Mode        string         `json:"mode"`
	Provider    string         `json:"provider"`
	InboundPath string         `json:"inbound_path"`
	HasSecret   bool           `json:"has_secret"`
	Status      string         `json:"status"`
	LastEventAt *time.Time     `json:"last_event_at,omitempty"`
	Config      map[string]any `json:"config"`
}
```

行为：
- `Create`：校验 `source_kind/mode/provider/name`；写 `project_external_sources`；CI/Webhook/邮件入站生成 secret 并创建 `ci|webhook|email` 凭据；邮件 IMAP 校验 `imap` 凭据归属与 `VerifyImapLogin`，写绑定 + 轮询状态。
- `List`：只返回三类 provider；状态由最近事件与退避计算。
- `Delete`：删绑定、凭据、轮询状态；不得误删其他 provider。
- `RotateSecret`：生成新 secret 覆盖凭据；旧 secret 立即失效。
- `TestDelivery`：合成事件做归一化 + 策略评估，不落库、不执行。
- `PollNow`：仅 IMAP 绑定，触发一次轮询。

- [ ] **Step 5: 隔离外部系统面板**

`ExternalSourceService.List` 过滤 `ci|email|webhook`；`Add` / `Delete` 对这三类返回明确错误，避免绕过 Intake 的密钥清理。

- [ ] **Step 6: 测试并提交**

```bash
cd server && go test ./internal/store ./internal/service -run 'EmailSourcePoll|IntakeSource' -v
cd .. && make schema-diff && make sqlc
git add server/internal/store server/internal/service/intake_source.go server/internal/service/intake_source_test.go \
  server/internal/service/external_source.go
git commit -m "feat(intake): add intake source binding service and email poll state"
```

---

## Task 2: CI 归一化、认证预设与默认规则

**Files:**
- Modify: `server/internal/intake/source.go`
- Create: `server/internal/webhooksource/ci.go`
- Test: `server/internal/webhooksource/ci_test.go`
- Modify: `server/internal/service/intake_source.go`

**Interfaces:**
- Produces: `intake.EventKindCIFailed`、`CISucceeded`、`CIRecovered`、`CICanceled`。
- Produces: `webhooksource.NormalizeCI(projectID, ref, provider string, raw []byte) (intake.Trigger, bool, error)`。
- Produces: `webhooksource.CIProviderPreset` 与认证头解析。

- [ ] **Step 1: 写失败测试**

```go
func TestNormalizeCIGitLabFailure(t *testing.T) {
	body := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"failed","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"},
	  "merge_request":{"iid":42},
	  "user":{"id":7,"name":"Alice"}
	}`)
	trig, ok, err := NormalizeCI(1, "gitlab/main", "gitlab", body)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, intake.SourceKindCI, trig.SourceKind)
	require.Equal(t, intake.EventKindCIFailed, trig.EventKind)
	require.Equal(t, "acme/api#pr:42", trig.Thread.ThreadExtID)
	require.Equal(t, "7", trig.Actor.ExtID)
	require.False(t, trig.Explicit)
}

func TestNormalizeCIIgnoresFilteredStatus(t *testing.T) {
	_, ok, err := NormalizeCI(1, "jenkins", "jenkins", []byte(`{"status":"running"}`))
	require.NoError(t, err)
	require.False(t, ok)
}
```

- [ ] **Step 2: 事件类型与归一化**

`intake/source.go` 增加四个事件常量。`ci.go` 按 provider 分支：

| provider | 关键字段 | 状态映射 |
|---|---|---|
| `generic` | `id/event/repository/branch/commit/thread_id/pipeline/job/status/summary/log_excerpt/actor/url` | `failed→ci_failed`、`succeeded→ci_succeeded`、`canceled→ci_canceled`；`previous_status=failed` 且成功 → `ci_recovered` |
| `gitlab` | `object_kind=pipeline`、`object_attributes.status/ref/sha`、`project.path_with_namespace`、`merge_request.iid` | 同上 |
| `jenkins` | 同 generic | 同上 |
| `buildkite` | `event=build.finished`、`build.state`、`pipeline.repository` | 同上 |
| `circleci` | `type=workflow-completed`、`workflow.status` | 同上 |

文本构造：`summary + 换行 + pipeline/job/branch/commit + 换行 + log_excerpt`；`log_excerpt` 截断 16KB；`SourceURL` 取 job/pipeline url；`ReceiveAll=true`、`Explicit=false`。

- [ ] **Step 3: 默认规则**

`IntakeSourceService.Create` 在 `source_kind=ci` 时插入：

```json
{"name":"CI 失败自动修复","instruction":"定位 CI 失败原因，提交修复，确认同一流水线通过",
 "conditions":{"event_kind":"ci_failed"},"priority":100,"enabled":true}
```

写 `intake_rules` 并生成 policy version；来源模式仍由用户决定。

- [ ] **Step 4: 测试并提交**

```bash
cd server && go test ./internal/webhooksource ./internal/service -run 'NormalizeCI|IntakeSourceCI' -v
git add server/internal/intake/source.go server/internal/webhooksource/ci.go \
  server/internal/webhooksource/ci_test.go server/internal/service/intake_source.go
git commit -m "feat(intake): add ci normalization and default rule"
```

---

## Task 3: CI 入站路由与闭环

**Files:**
- Modify: `server/internal/intake/service.go`
- Modify: `server/internal/intake/source.go`
- Modify: `server/internal/service/webhook_source.go`
- Modify: `server/internal/api/integration_webhook.go`
- Modify: `server/internal/server/router.go`
- Test: `server/internal/service/ci_source_test.go`

**Interfaces:**
- Produces: `POST /api/integrations/ci/:binding`。
- Produces: `Trigger.RequireBound bool`：为 true 且未解析到绑定任务时，记录 `ignore` 决策但不新建任务。

- [ ] **Step 1: 增加 RequireBound**

`intake.Trigger` 增加 `RequireBound bool`；`intake.Service.Handle` 在 resolve 后、decide 前判断：`RequireBound && !resolution.Bound → DecisionIgnore`，写入决策，不执行。用于 `ci_succeeded` / `ci_recovered` / `ci_canceled`。

- [ ] **Step 2: CI 入站分支**

`WebhookSourceService.HandleInbound` 增加 `intake.SourceKindCI`：

- 校验 provider 与认证：
  - `gitlab`：`X-Gitlab-Token` 常量时间比较；
  - `circleci`：`Circleci-Signature` HMAC；
  - `buildkite`：`X-Buildkite-Token`；
  - `jenkins/generic`：`X-Niuniu-Signature-256`（带时间戳）或 Bearer。
- 读 `config.events`；未命中返回 `Ignored:true`。
- `NormalizeCI` → `s.intake.Handle`。
- 成功类事件设置 `RequireBound=true`。

- [ ] **Step 3: 公开路由**

`integration_webhook.go` 增加 `CI(c)` 调 `handle(c, intake.SourceKindCI)`；`router.go` 注册 `POST /api/integrations/ci/:binding`。

- [ ] **Step 4: 集成测试**

覆盖：失败事件建任务/进入决策；成功事件未绑定 → ignore 且不建任务；成功事件已绑定 → resume；重复事件 duplicate；错误令牌 401；未启用事件 200 ignored。

- [ ] **Step 5: 提交**

```bash
cd server && go test ./internal/intake ./internal/service -run 'CI|RequireBound' -v
git add server/internal/intake server/internal/service/webhook_source.go \
  server/internal/api/integration_webhook.go server/internal/server/router.go \
  server/internal/service/ci_source_test.go
git commit -m "feat(intake): add ci inbound route and resume loop"
```

---

## Task 4: 邮件 IMAP 轮询

**Files:**
- Modify: `server/go.mod`、`server/go.sum`
- Create: `server/internal/service/email_source.go`
- Create: `server/internal/service/email_parse.go`
- Modify: `server/internal/scheduler/scheduler.go`
- Modify: `server/internal/config/config.go`
- Modify: `server/internal/server/server.go`
- Test: `server/internal/service/email_source_test.go`
- Test: `server/internal/service/email_parse_test.go`

**Interfaces:**
- Produces: `EmailSourceService`：`RunOnce`、`PollBinding`。
- Produces: `scheduler.RegisterEmailPoll(expr string, run func(context.Context))`。
- Produces: `config.IntakeConfig.EmailPollCron`（`intake.email_poll_cron`，env `NIUNIU_INTAKE_EMAIL_POLL_CRON`）。

- [ ] **Step 1: 加依赖**

```bash
cd server && go get github.com/emersion/go-imap/v2@latest && go mod tidy
```

- [ ] **Step 2: 写解析测试**

覆盖：`Message-ID`/`In-Reply-To`/`References` 线程根；`text/plain` 优先；HTML 转文本；引用裁剪；`Auto-Submitted`/`Precedence: bulk`/`Return-Path: <>` 忽略；正文截断 64KB；附件只留元数据。

- [ ] **Step 3: 实现轮询**

```go
type EmailSourceService struct {
	q      *store.Queries
	db     *store.DB
	creds  *ExternalCredentialService
	intake *intake.Service
	dialer IMAPDialer // 可替换，测试用 fake
}
```

行为：
- `RunOnce` 列出所有 `provider=email` 且 `config.mode=imap` 的绑定，逐个 `PollBinding`；同绑定并发跳过。
- 读取 `imap` 凭据；`VerifyImapLogin` 已在创建时通过，运行期失败置退避。
- `SELECT mailbox` → 取 `UIDVALIDITY` → `UID FETCH last_uid+1:*`（`BODY.PEEK[]`）→ 解析 → `intake.Handle` → 更新游标。
- 只读，不改 `\Seen`，不删不移。
- 错误指数退避至 30min；`UIDVALIDITY` 变化时重置游标并只取最近 24h。
- 每封邮件事件 `SourceKind=email`、`EventKind=message`、`SourceRef=<binding name/id>`。

- [ ] **Step 4: 调度与配置**

- `scheduler.RegisterEmailPoll` 仿 `RegisterGitHubPoll`，新增独立 entry。
- `server.go`：`if cfg.Intake.EmailPollCron != "" { s.scheduler.RegisterEmailPoll(..., s.emailSourceSvc.RunOnce) }`。
- 默认值在 config 中置 `"* * * * *"` 或空（空则手动轮询可用）。

- [ ] **Step 5: 测试并提交**

```bash
cd server && go test ./internal/service -run 'Email(Source|Parse)' -v
git add server/go.mod server/go.sum server/internal/service/email_source.go \
  server/internal/service/email_parse.go server/internal/service/email_source_test.go \
  server/internal/service/email_parse_test.go server/internal/scheduler/scheduler.go \
  server/internal/config/config.go server/internal/server/server.go
git commit -m "feat(intake): add email imap polling"
```

---

## Task 5: 邮件入站解析接线

**Files:**
- Modify: `server/internal/webhooksource/webhook.go`
- Modify: `server/internal/service/webhook_source.go`
- Test: `server/internal/webhooksource/email_test.go`

**Interfaces:**
- Produces: provider 预设 `mailgun|sendgrid|postmark|generic`；同一套忽略规则与线程根。

- [ ] **Step 1: 补齐 provider 字段映射**

在现有 `emailPayload` 上补常见别名：`Message-Id`、`message-id`、`body_plain`、`stripped-text`、`From`/`from`、`In-Reply-To`/`in_reply_to`、`References`/`references`。保持向后兼容。

- [ ] **Step 2: 统一忽略规则**

抽取 `shouldIgnoreEmail(headers)`，IMAP 与入站共用：

- `Auto-Submitted` 非 `no`；
- `Precedence: bulk|junk|list`；
- `Return-Path: <>`；
- 发件人等于绑定邮箱自身。

入站路径从 payload 读对应头；缺失时按 payload 字段推断。

- [ ] **Step 3: 测试并提交**

覆盖 Mailgun、SendGrid、Postmark 三种样例；自动信/退信忽略；回复线程根正确。

```bash
cd server && go test ./internal/webhooksource -run Email -v
git add server/internal/webhooksource/webhook.go server/internal/webhooksource/email_test.go \
  server/internal/service/webhook_source.go
git commit -m "feat(intake): wire email inbound provider presets and ignore rules"
```

---

## Task 6: Webhook 安全加固

**Files:**
- Modify: `server/internal/service/webhook_source.go`
- Modify: `server/internal/api/integration_webhook.go`
- Test: `server/internal/service/webhook_security_test.go`

**Interfaces:**
- Produces: `X-Webhook-Timestamp` + `X-Webhook-Signature-256 = HMAC(secret, timestamp+"."+body)`；±300s。
- Produces: 每绑定限流 60/min（`golang.org/x/time/rate`）；`config.max_body_bytes` 默认 1MB，上限 25MB。

- [ ] **Step 1: 写安全测试**

覆盖：正确时间戳签名通过；过期/未来时间戳 401；错误签名 401；无时间戳时 raw-body HMAC 兼容；Bearer 通过；超过体积 413；超过频率 429；事件过滤 ignored。

- [ ] **Step 2: 实现**

- `genericSecretValid` 增加时间戳分支。
- 每绑定 `rate.Limiter` 缓存在 `sync.Map`，`Allow()` 失败 429。
- `api/integration_webhook.go` 按 `config.max_body_bytes` 动态限制；默认 1MB。
- 事件过滤读 `config.events`；空表示全部。

- [ ] **Step 3: 提交**

```bash
cd server && go test ./internal/service -run WebhookSecurity -v
git add server/internal/service/webhook_source.go server/internal/service/webhook_security_test.go \
  server/internal/api/integration_webhook.go
git commit -m "feat(intake): harden webhook signatures replay limits and body caps"
```

---

## Task 7: 项目 API 与一次性密钥页

**Files:**
- Create: `server/internal/api/intake_source.go`
- Modify: `server/internal/server/router.go`
- Modify: `server/internal/api/intake_webhook.go`
- Create: `server/web/src/pages/intake-secret.tsx`
- Test: `server/internal/api/intake_source_test.go`

**Interfaces:**
- Produces: `GET/POST /api/projects/:id/intake/sources`、`POST .../:sid/rotate|test|poll`、`DELETE .../:sid`。
- Produces: 一次性密钥 token（内存 TTL 15min）与页面 `/intake/sources/secret/:token`。
- 保留 `/api/projects/:id/intake/webhook` 为别名。

- [ ] **Step 1: Handler 测试**

覆盖：合法 owner 创建 200；跨 owner 403；重复名称 409；IMAP 缺凭据 400；测试投递不落库；轮换后旧 secret 401；删除级联。

- [ ] **Step 2: 实现**

- 写操作先 `EnsureOwnerWritable`。
- `Create` 返回 `{source, secret_link?}`；明文 secret 只写一次性链接存储，不直接返回。
- `TestDelivery` 调服务层合成事件，返回 `{trigger, decision, would_do}`。
- `Poll` 仅 IMAP。
- 旧 webhook 路由转调新服务。

- [ ] **Step 3: 一次性密钥页**

`intake-secret.tsx`：读取 token → 展示一次 → 页面刷新/离开即失效；过期显示明确提示。

- [ ] **Step 4: 提交**

```bash
cd server && go test ./internal/api -run IntakeSource -v
git add server/internal/api/intake_source.go server/internal/api/intake_source_test.go \
  server/internal/api/intake_webhook.go server/internal/server/router.go \
  server/web/src/pages/intake-secret.tsx
git commit -m "feat(intake): add source management api and one-time secret page"
```

---

## Task 8: MCP 工具与对话配置

**Files:**
- Create: `server/cmd/niuniu-mcp/intake_source_tools.go`
- Modify: `server/cmd/niuniu-mcp/main.go`
- Test: `server/cmd/niuniu-mcp/intake_source_tools_test.go`

**Interfaces:**
- Produces: `intake_source_list`、`intake_source_create`、`intake_source_test`、`intake_source_rotate`、`intake_source_delete`、`intake_source_poll`、`intake_source_credential_link`。

- [ ] **Step 1: 实现工具**

- `intake_source_create` 参数：`project_id`、`source_kind`、`name`、`mode`、`provider`、`config`、`credential_id?`。
- 返回 `source` + `secret_link`；**不返回明文 secret**。
- `intake_source_credential_link` 为 IMAP 缺凭据时生成一次性安全链接。
- 所有工具复用项目 API，错误码透传。

- [ ] **Step 2: 对话流程测试**

“接入 Jenkins CI” → 询问名称/provider → 创建 → 返回入站 URL 与密钥链接；“接入邮件” → 询问 IMAP / 入站解析 → IMAP 缺凭据时给安全链接。

- [ ] **Step 3: 提交**

```bash
cd server && go test ./cmd/niuniu-mcp -run IntakeSource -v
git add server/cmd/niuniu-mcp/intake_source_tools.go \
  server/cmd/niuniu-mcp/intake_source_tools_test.go server/cmd/niuniu-mcp/main.go
git commit -m "feat(intake): add intake source mcp tools"
```

---

## Task 9: UI 连接状态与接入对话框

**Files:**
- Modify: `server/web/src/lib/intake-api.ts`
- Modify: `server/web/src/components/projects/intake/intake-utils.ts`
- Modify: `server/web/src/components/projects/intake/IntakePolicyPanel.tsx`
- Create: `server/web/src/components/projects/intake/connect-source-dialog.tsx`
- Modify: `server/web/src/i18n/locales/zh-CN/intake.json`、`en/intake.json`
- Test: `server/web/src/components/projects/intake/connect-source-dialog.test.tsx`

**Interfaces:**
- Produces: `intakeApi.listSources/createSource/testSource/rotateSource/deleteSource/pollSource`。
- Produces: `connected` 对 ci/email/webhook 由绑定列表计算。
- Produces: `CONNECTABLE_SOURCES` 三者映射为 `inline`。

- [ ] **Step 1: API 客户端**

补类型 `IntakeSource`、`IntakeSourceInput`、`IntakeTestResult` 与方法；错误统一解析 `{error}`。

- [ ] **Step 2: 连接状态**

`IntakePolicyPanel` 增加 `sourcesQuery`，`connected` 计算三类；`CONNECTABLE_SOURCES` 对三类返回 `inline`，`connectSource` 打开对话框而非跳转。

- [ ] **Step 3: 接入对话框**

步骤：名称 → 类型/传输 → 配置 → 创建 → 密钥与 URL → 测试投递 → 完成。
- CI：provider、事件（默认失败）、本机/内网开关。
- 邮件：IMAP/入站解析切换；IMAP 选凭据/新建；文件夹；轮询间隔。
- Webhook：事件过滤、体积、重放窗口。
- 测试结果展示归一化字段与策略决策。
- 密钥只展示一次，复制按钮；关闭后只能轮换。

- [ ] **Step 4: 测试并提交**

```bash
cd server/web && pnpm lint && pnpm test:run -t "ConnectSource"
git add server/web/src/lib/intake-api.ts server/web/src/components/projects/intake \
  server/web/src/i18n/locales/zh-CN/intake.json server/web/src/i18n/locales/en/intake.json
git commit -m "feat(intake): add source connection dialog and connected states"
```

---

## Task 10: UI 绑定管理、测试投递与可达性提示

**Files:**
- Create: `server/web/src/components/projects/intake/source-bindings.tsx`
- Modify: `server/web/src/components/projects/intake/source-config-dialog.tsx`
- Modify: `server/web/src/components/projects/intake/IntakePolicyPanel.tsx`
- Modify: i18n 文件
- Test: `server/web/src/components/projects/intake/source-bindings.test.tsx`

**Interfaces:**
- Produces: 绑定列表（名称、模式、provider、最近投递、24h 事件、状态）。
- Produces: 操作：测试、轮换、立即轮询、删除。
- Produces: 可达性提示：本机/局域网/隧道三选一。

- [ ] **Step 1: 绑定列表**

在每张来源卡的配置对话框中展示绑定；状态 `ok|degraded|error|never` 用 Badge；错误可展开最近原因。

- [ ] **Step 2: 操作接线**

测试 → `testSource`，展示结果；轮换 → 确认后展示新密钥一次；轮询 → 仅 IMAP；删除 → 二次确认并刷新。

- [ ] **Step 3: 可达性提示**

创建 CI/Webhook 后，若配置公网 URL 且未检测到反代，显示：
“该地址需要 CI 能访问到本机。可选：本机/局域网地址、自备隧道、或改用邮件 IMAP。”
不阻止创建。

- [ ] **Step 4: 测试并提交**

```bash
cd server/web && pnpm lint && pnpm test:run -t "SourceBindings"
git add server/web/src/components/projects/intake server/web/src/i18n/locales
git commit -m "feat(intake): add binding management and reachability hints"
```

---

## Task 11: 个人版可达性与安全边界

**Files:**
- Modify: `server/internal/service/intake_source.go`
- Modify: `server/internal/service/webhook_source.go`
- Create: `server/internal/service/intake_network.go`
- Test: `server/internal/service/intake_network_test.go`

**Interfaces:**
- Produces: `ClientAllowed(cfg Config, r *http.Request) bool`：`local_only` 只允许 loopback/私网；默认不使用 `X-Forwarded-For`。
- Produces: 可选 `ip_allow_list`（CIDR）。

- [ ] **Step 1: 实现网络判定**

- 默认取 `r.RemoteAddr`；仅当配置了受信反代时才解析 `X-Forwarded-For`。
- `local_only=true`：`net.IP.IsLoopback()` 或 `IsPrivate()`。
- `ip_allow_list`：逐条 CIDR 匹配；为空不限制。
- 拒绝时 403，不落事件。

- [ ] **Step 2: 测试**

覆盖：loopback、私网、公网、伪造 XFF、CIDR 命中/不命中、未配置反代时不信任 XFF。

- [ ] **Step 3: 提交**

```bash
cd server && go test ./internal/service -run IntakeNetwork -v
git add server/internal/service/intake_network.go server/internal/service/intake_network_test.go \
  server/internal/service/intake_source.go server/internal/service/webhook_source.go
git commit -m "feat(intake): enforce local-only and ip allow lists"
```

---

## Task 12: 观测与告警

**Files:**
- Modify: `server/internal/service/intake_source.go`
- Modify: `server/internal/service/mission_control.go`
- Modify: `server/web/src/components/workspaces/mission-control-panels.tsx`
- Test: `server/internal/service/intake_source_health_test.go`

**Interfaces:**
- Produces: `Health(ctx, projectID)`：每绑定最近成功、最近失败、24h 成功/去重/拒绝、退避截止。
- Produces: Mission control Intake 面板“投递健康”分组。

- [ ] **Step 1: 聚合查询**

从 `intake_events` + `intake_decisions` 聚合；不建新表。状态规则：
- `never`：无事件；
- `ok`：24h 有成功且无连续失败；
- `degraded`：有退避或连续 1–2 次失败；
- `error`：连续 3 次失败或 IMAP 认证失败。

- [ ] **Step 2: 告警与日志**

- 连续 3 次 401 → “密钥可能配置错误”；
- 连续 3 次 400 → “载荷格式不符”；
- IMAP 认证失败 → 绑定 `error` 并通知；
- 日志只记 binding id、provider、reason_code，不记密钥与正文。

- [ ] **Step 3: UI 面板**

Mission control 增加来源健康：名称、provider、状态、24h 计数、最近投递；失败可下钻到待校准队列。

- [ ] **Step 4: 提交**

```bash
cd server && go test ./internal/service -run IntakeSourceHealth -v
cd server/web && pnpm lint && pnpm test:run
git add server/internal/service/intake_source.go server/internal/service/mission_control.go \
  server/web/src/components/workspaces/mission-control-panels.tsx \
  server/internal/service/intake_source_health_test.go
git commit -m "feat(intake): add source health observability and alerts"
```

---

## Task 13: 测试、E2E 与运维文档

**Files:**
- Create: `server/internal/service/intake_sources_e2e_test.go`
- Create: `docs/operations/intake-sources-runbook.md`
- Modify: `docs/superpowers/specs/2026-11-06-intake-ci-email-webhook-design.md`（仅实现偏差时）

- [ ] **Step 1: E2E 场景**

1. CI 失败推送 → 待校准/自动建任务 → 修复后成功事件 resume。
2. 邮件 IMAP 轮询 → 新邮件建任务 → 回复邮件 resume。
3. 邮件入站解析（Mailgun 样例）→ 决策。
4. Webhook 签名投递 → 决策；重复投递 duplicate；错误签名 401。
5. 测试投递不落库、不建任务。
6. 轮换密钥后旧密钥失效、新密钥可用。
7. 删除绑定后凭据与轮询状态级联清理。
8. `local_only` 拒绝公网来源。
9. 限流 429、超大 413。
10. 外部系统面板不显示 intake 绑定。

- [ ] **Step 2: 全量验证**

```bash
cd server && go test ./internal/intake/... ./internal/webhooksource/... ./internal/service/... ./internal/api/... -race
cd .. && make schema-diff && make sqlc
cd server/web && pnpm lint && pnpm test:run && pnpm build
```

- [ ] **Step 3: 运维文档**

`docs/operations/intake-sources-runbook.md`：三类来源接入步骤、个人版可达性三选一、密钥轮换、IMAP 排错、签名样例、限流/体积、常见 401/400/429、回滚。

- [ ] **Step 4: 提交**

```bash
git add server/internal/service/intake_sources_e2e_test.go docs/operations/intake-sources-runbook.md
git commit -m "test(intake): add end-to-end coverage and runbook"
```

---

## Self-Review

**设计覆盖：**

| 设计章节 | 任务 |
|---|---|
| §3 统一模型 / 绑定与凭据 | 1 |
| §4 版本差异与可达性 | 4、11 |
| §5 CI 源 | 2、3 |
| §6 邮件源 | 4、5 |
| §7 Webhook 源 | 6 |
| §8 安全模型 | 1、6、11 |
| §9 数据模型 | 1 |
| §10 API 与 MCP | 7、8 |
| §11 UI | 9、10 |
| §12 观测与运维 | 12 |
| §13 测试与验收 | 2–13 各自测试 + 13 |

**延迟实现扫描：**

- 全文无 `TBD` / `TODO` / “后续阶段” / “先用 X 后替换”。
- 按依赖编号，不按阶段裁剪；13 个任务全部必须完成。

**类型一致性：**

- `IntakeSourceService`、`IntakeSourceDTO`、`IntakeSourceInput` 贯穿 1/2/7/8/9/10/11/12。
- `NormalizeCI`、CI 事件常量贯穿 2/3。
- `EmailSourceService`、邮箱游标贯穿 4/5/12。
- `RequireBound` 贯穿 3。
