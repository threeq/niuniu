# 任务来源 / 自动完成：上线与运维 Runbook

> 适用版本：Epic #755 合入后的 `niuniu` 服务端（个人版 / 团队版共用同一套代码）
> 设计文档：[`docs/superpowers/specs/2026-11-03-intake-completion-autonomy-design.md`](../superpowers/specs/2026-11-03-intake-completion-autonomy-design.md)
> 状态：2026-11-04 编写，对应设计文档 §7 / §8 / §10 与 issue #781 验收项。

本文件回答四件事：

1. 新环境如何按版本上线（个人版轮询 / 团队版 webhook）。
2. `im_bot_inbox` → `intake_events` 的读切换、保留、删除与回滚怎么做。
3. 日常运维：策略版本、待校准、暂停接活、指标、常见故障。
4. 出问题时按什么顺序回滚，回滚是否安全。

文中 `$BASE` 指服务地址，例如个人版 `http://127.0.0.1:3000`，团队版 `https://self.niu6ai.com`。所有 `/api` 请求在团队版需要登录态。

---

## 0. 一页速查

### 0.1 两种部署形态

| 维度 | 个人版 / 本地优先 | 团队版 |
|---|---|---|
| GitHub 入站 | **轮询**（服务端主动出站 `api.github.com`） | **Webhook**（GitHub 主动回调公网 HTTPS） |
| 是否需要公网 | **不需要** | **需要**：公网域名 + TLS |
| 调度开关 | `intake.github_poll_cron`，默认 `*/1 * * * *` | 建议关闭轮询：`intake.github_poll_cron: ""` |
| 凭据 | 用户自己的 GitHub PAT | 用户/组织自己的 PAT；**另需 `webhook_secret`** |
| 入站端点 | 无（代码中仍注册，但个人版不配置 secret，端点不可用） | `POST /api/integrations/github/webhook/:binding` |
| 端点鉴权 | — | `X-Hub-Signature-256` HMAC-SHA256，失败 401 |
| 事件幂等 | `intake_events` UNIQUE + 轮询游标 | 同一 `intake_events` UNIQUE；与轮询共用 event id |

个人版也可以手动配置 webhook，但轮询是其默认且**无需公网**的路径；团队版应使用 webhook。

### 0.2 关键默认值

| 配置 | 默认 | 说明 |
|---|---|---|
| GitHub 轮询 cron | `*/1 * * * *` | 每分钟一次；空字符串关闭调度（手动 `RunOnce` 仍可用） |
| Intake 新来源显示默认 | `observe` | 设置页未保存时展示值；**运行时空行按 `off` 处理**（见 §3 步骤 4） |
| Intake 每日上限 `daily_cap` | `4` | 只限制 inferred/auto 自动接活；显式请求不受限 |
| Completion `max_rounds` | `20` | 达到后转人工并提结构化问题 |
| Completion `token_budget` | `0`（不限） | 达到后转人工 |
| Completion `stop_on_decision` | `true` | 遇需人类裁决输出 `[AUTOHOST_HUMAN_ACTION]` |
| Completion `require_review` | `false` | 为 true 时完成需走审查列 |
| 校准门槛 | ≥10 条、认可率 ≥80% | 达到后才建议开启 `auto`（仍需二次确认） |
| Webhook body 上限 | 25 MB | 反代需不小于该值 |

### 0.3 安全与默认策略（上线前必须知悉）

- **默认保守**：新 Source 默认 `observe`（设计 §12-3）。设置页显示 `observe`；若从未保存过该来源配置，运行时按更保守的 `off` 处理（只接受显式请求，见 §3 步骤 4）。`auto` 必须先完成 dry-run 校准（≥10 条、认可率 ≥80%）并在 UI 二次确认。
- **外部文本只是数据**：IM 消息、GitHub 评论、邮件正文都只作为任务描述/上下文，**不构成指令**；`explicit` / `inferred` / `denied` 由代码按 allow list、bot list、规则判定，prompt 内容无法提权。
- **高-risk 动作只给显式授权**：停止任务等高风险动作只允许 `explicit` 且非 bot 的 actor（`Policy.AllowsHighRiskAction`）。
- **Epic 不自动完成**：Epic 类型 issue 必须人工审核后手动移入「完成」列；autohost/`[AUTOHOST_DONE]` 不能替 Epic 收尾。
- **个人版无公网依赖**：GitHub 采用出站轮询；团队版才需要公网 webhook。

### 0.4 环境变量

Viper 把 `.` 替换为 `_`，前缀 `NIUNIU_`：

```bash
# IM 语义迁移回滚开关（见 §5.1）
NIUNIU_INTAKE_LEGACY_CHAT_MODE=true

# GitHub 轮询调度；空字符串关闭
NIUNIU_INTAKE_GITHUB_POLL_CRON="*/5 * * * *"
```

等价 YAML（`~/.niuniu/config.yaml`）：

```yaml
intake:
  legacy_chat_mode: false
  github_poll_cron: "*/1 * * * *"
```

---

## 1. 上线前置检查

在升级到包含本能力的版本前完成：

- [ ] **备份数据库**。SQLite 为 `~/.niuniu/niuniu.db`；团队版 PostgreSQL 用 `pg_dump -Fc niuniu_server > niuniu_server-before-intake.dump`。
- [ ] 确认磁盘余量：本能力新增 **12 张表**（见 §5.4），均为增量；不会重写存量数据。
- [ ] 源码构建时先跑：
  - `make schema-diff` 必须输出 `OK: no table-level drift detected`
  - `make sqlc`（无 diff）
  - `make test`
- [ ] 个人版：确认可出站访问 `https://api.github.com`（GitHub 轮询）；无需开放入站端口。
- [ ] 团队版：确认公网 HTTPS 已就绪，反代 `request_body` ≥ 25 MB，`/api/integrations/*` 未被鉴权中间件拦截（该组路由按设计在鉴权组之外，以签名/令牌自证）。
- [ ] 确认已有 GitHub 集成凭据可读目标仓库（PAT 含 Issues 读、必要时写）。
- [ ] 明确本次上线形态：个人版轮询 / 团队版 webhook（不要两种同时当作主路径）。

> 数据库 schema 变更由服务启动时的 `store/migrate.go` 自动执行（`addColumnIfNotExists` + `CREATE TABLE IF NOT EXISTS`）。本能力**没有破坏性迁移**：新表、新列；存量 `issues.goal_condition` 不回填、不改写（设计 §7.3）。迁移列：`issue_checklists.kind/brief_id`、`issues.completion_profile_id`、`im_bot_chats.intake_mode`；`issue_exec_events.kind` 放宽以接受 `intake` 轨迹。

---

## 2. 部署形态差异

### 2.1 个人版（轮询，无公网）

数据流：`scheduler → GithubSourceService.RunOnce → GitHub REST API（since/ETag）→ intake pipeline`。

- 外呼目标：`https://api.github.com`（当前版本未暴露 GitHub Enterprise BaseURL 配置）。
- 轮询游标与退避存在 `github_source_poll_state` 表，按 repo + event 流独立记录。
- 无需域名、证书、端口映射；只需要出站 443。
- 网络受限时的表现：`GET /api/projects/:id/intake/github` 返回的 `repos[].last_error` 有值，`backoff_until` 未来时间；不影响其他功能。

### 2.2 团队版（Webhook，需公网）

数据流：`GitHub → HTTPS /api/integrations/github/webhook/:binding → HMAC 校验 → normalize → 同一条 intake pipeline`。

- 必须公网可达；反代把 `/api/integrations/github/webhook/*` 原样转发到 `niuniu-server`。
- 绑定 ID 是 `project_external_sources` 行 ID。可通过 `GET /api/projects/:id/intake/github` 的 `repos[].webhook_path` 读取，或查库：
  ```sql
  SELECT id, project_id, provider, source_key
  FROM project_external_sources
  WHERE provider = 'github' AND project_id = :project_id;
  ```
- GitHub webhook 的 secret 存在该项目 GitHub 绑定所用的**外部凭据**里，字段 `webhook_secret`（兼容 `secret`）。凭据静态加密；HTTP 只返回 `webhook_configured: true/false`，不回显值。
- 当前 SPA 的 GitHub 凭据对话框只渲染 PAT 字段，**尚未暴露 `webhook_secret` 输入框**；团队版首次上线需通过 API 写入（见 §4.2）。
- 团队版建议关闭轮询：`intake.github_poll_cron: ""`。若同时开启，轮询与 webhook 的 event id 相同，`intake_events` 会去重，不会重复建任务，但会白白消耗 API 配额。

---

## 3. 个人版上线步骤（轮询）

1. **升级并启动服务**，确认 `/api/health` 返回正常、启动日志无迁移错误。
2. **配置 GitHub PAT**：`设置 → 集成` 添加 GitHub 凭据（详见 [`docs/integrations.md`](../integrations.md) §1–§2）。
3. **给项目挂 GitHub 外部源**：项目设置 → 外部源 → 添加 `owner/repo`。这一步创建 `project_external_sources` 行，轮询按它取数。
4. **保存任务来源配置（必须做一次）**：
   项目设置 → **任务来源** → GitHub 卡片 → 选择模式并保存。

   > 重要：设置页对没有存储行的来源显示 `observe`，但运行时空行按 `off` 处理（只接受显式请求）。只有 `PUT /api/projects/:id/intake/settings` 真正写入 `intake_settings` 后，才会按所选模式记录/执行。上线时至少保存一次；首次建议 `observe`。

   等价的 API 调用：

   ```bash
   curl -X PUT "$BASE/api/projects/123/intake/settings" \
     -H 'Content-Type: application/json' \
     -d '{"settings":[{"source_kind":"github","mode":"observe","daily_cap":4,
                       "allow_list":["alice"],"bot_list":["github-actions[bot]"]}]}'
   ```

5. **选择事件类型**：GitHub 卡片内只勾选需要的事件。默认（没有配置）等于全部：
   `issue_comment`、`pull_request_review`、`pull_request_review_comment`、`check_run`、`workflow_run`、`pull_request`（closed/merged）。
6. **验证轮询**：
   - 打开一个测试 PR 并发表评论（评论者要在 allow list 内，或通道是“接收全部”）。
   - 观察 `GET /api/projects/123/intake/github` 的 `last_polled_at` 前进、`last_error` 为空。
   - 观察 `GET /api/projects/123/intake/decisions?executed=0` 出现 dry-run 决策。
   - 服务端日志应出现 `github source:` 前缀的正常记录，无 `poll failed`。
7. **校准后再开 auto**：
   - 在 `/intake` 队列逐条 采纳 / 忽略（忽略必须填原因）。
   - 达到 ≥10 条且认可率 ≥80% 后，设置页会提示可开启 `auto`；确认对话框二次确认后才切换。
   - 未校准也可维持 `observe` 长期运行，不影响显式请求。
8. **IM（可选）**：已有 IM 机器人按原方式配对。迁移后：
   - `agent_mode=command` → `intake_mode=off`
   - `agent_mode=observe` → `intake_mode=auto`
   - 新 chat 默认 `observe`（dry-run）
   - 每个 chat 可在 chat 行覆盖「继承项目（`inherit`）/ 关闭（`off`）/ 观察（`observe`）/ 自动（`auto`）」。
     `inherit` 表示该 chat 不覆盖项目+来源策略，实际模式取 `intake_settings` 中 `im_bot` 来源的 `mode`；`inherit` 不是迁移回填值，只由用户主动选择。

---

## 4. 团队版上线步骤（Webhook）

### 4.1 部署服务

按 `niuniu-enterprise` 仓库的 `docs/ai-context/production-deploy.md` 既有流程部署。本能力只新增 `intake` 配置块，不改变部署拓扑：

```yaml
intake:
  # 团队版以 webhook 为主路径：关闭轮询，避免重复消耗 GitHub API。
  # 留空字符串 = 关闭调度；删除该键则回落到默认 */1。
  github_poll_cron: ""
  legacy_chat_mode: false
```

`deploy/self` 的 `server.yaml` 只会在首次部署时从 `server.yaml.example` 生成，之后持久保存；团队发行包（`deploy/team-edition`）的 `up.sh` 每次从模板重渲染，因此要固化配置请改模板而不是只改生成文件。

### 4.2 准备带 `webhook_secret` 的 GitHub 凭据

`webhook_secret` 与 PAT 一起存在外部凭据的加密 config 中。两种方式：

**新建凭据（推荐，一次写全）**

```bash
curl -X POST "$BASE/api/me/external-credentials" \
  -H "Authorization: Bearer <登录令牌>" \
  -H 'Content-Type: application/json' \
  -d '{
        "provider": "github",
        "alias": "github-intake",
        "config": {
          "token": "github_pat_xxx",
          "webhook_secret": "<随机高强度字符串>"
        }
      }'
```

**已有凭据（PATCH 会整体替换 config，必须同时带上 token）**

```bash
curl -X PATCH "$BASE/api/me/external-credentials/<credential_id>" \
  -H "Authorization: Bearer <登录令牌>" \
  -H 'Content-Type: application/json' \
  -d '{"config":{"token":"github_pat_xxx","webhook_secret":"<同一 secret>"}}'
```

> `webhook_secret` 建议用 `openssl rand -hex 32` 生成并存入团队的密码库。它是 webhook 的唯一鉴权，泄露等于任何人可伪造入站事件。

### 4.3 绑定仓库并读取回调地址

1. 项目设置 → 外部源，添加 `owner/repo`，选择上一步的凭据。
2. `GET /api/projects/:id/intake/github`，在 `repos[]` 中确认：
   - `source_key` 是目标仓库
   - `webhook_configured: true`
   - `webhook_path` 形如 `/api/integrations/github/webhook/42`
3. 回调 URL = `https://<团队版域名>` + `webhook_path`。

### 4.4 在 GitHub 配置 webhook

仓库 Settings → Webhooks → Add webhook：

| 字段 | 值 |
|---|---|
| Payload URL | `https://self.example.com/api/integrations/github/webhook/42` |
| Content type | `application/json` |
| Secret | 与 `webhook_secret` 完全一致 |
| SSL verification | Enabled |
| Events | 勾选 §3 步骤 5 的六类；或先 “Send me everything” 验证后再收窄 |

GitHub 的 ping 事件不属于已知事件类型，会被忽略并返回 200：不落库、不建任务。

### 4.5 验证

```bash
# 1) 伪造签名必须 401，且数据库不新增事件
curl -i -X POST "$BASE/api/integrations/github/webhook/42" \
  -H 'Content-Type: application/json' -d '{"action":"created"}'
# 期望 401

# 2) 正常投递后查看决策
curl "$BASE/api/projects/123/intake/decisions?executed=0"

# 3) 重复投递同一事件应返回 duplicate=true，且不重复建任务
```

- 200 + `duplicate: false`：正常进入管线。
- 200 + `ignored: true`：事件类型未启用 / 被归一化忽略（如已删除评论、非终态 check run），记录为空是预期。
- 401：secret 不匹配或凭据缺失。
- 404：binding 不存在，或 provider 与路由不匹配。

### 4.6 邮件 / 通用 webhook（团队版）

在项目设置 → 任务来源里创建 email / webhook 绑定，响应会**一次性**返回 secret：

- 入站路径：`/api/integrations/email/:binding`、`/api/integrations/webhook/:binding`
- 鉴权：`X-Webhook-Signature-256`（HMAC-SHA256）或 `Authorization: Bearer <secret>` / `X-Webhook-Token: <secret>`
- secret 之后不可读取；丢失只能删除重建绑定。
- 邮件适配器把邮件正文归一化为任务描述；发件人进入 actor 授权判定，未授权按 denied 记录。

---

## 5. 迁移说明

### 5.1 IM 语义迁移：`agent_mode` → `intake_mode`

**发生了什么**：服务启动时给 `im_bot_chats` 增加 `intake_mode` 列（默认 `observe`），并按设计 §7.1 回填：

| 旧 `agent_mode` | 新 `intake_mode` | 行为 |
|---|---|---|
| `command` | `off` | 只在显式请求时行动（与旧行为一致） |
| `observe` | `auto` | 保留旧「预算内直接行动」 |
| 其他/新 chat | `observe` | dry-run，只记录决策 |

`intake_mode` 还接受 `inherit`（跟随项目+来源设置）。它是用户显式选择值，不由本次迁移写入；`effectiveChatIntakeMode` 遇到 `inherit` 返回空模式，由 intake pipeline 采用项目策略的 `mode`。

**验证**：

```sql
SELECT agent_mode, intake_mode, COUNT(*)
FROM im_bot_chats GROUP BY agent_mode, intake_mode;
```

预期不存在 `command→auto`、`observe→off` 的错配。

**回滚开关**（不改数据，运行时覆盖）：

```bash
NIUNIU_INTAKE_LEGACY_CHAT_MODE=true
# 或 config.yaml: intake.legacy_chat_mode: true
```

开启后每 chat 的生效模式回到 `agent_mode` 推导（`command→off`、`observe→auto`），需要重启服务进程生效；无需改库。新 chat 在回滚模式下也按 `agent_mode` 推导。

### 5.2 `im_bot_inbox` → `intake_events`

**当前版本（Epic #755 合入版）的实际状态**：

- IM 入站已经写入 `intake_events` 与 `intake_decisions`。
- 同时**双写**旧的 `im_bot_inbox`（`RecordEvent` 成功后镜像；未配对/未知 chat 的早期去重守卫也会写）。
- **读路径仍以 `im_bot_inbox` 为准**：`imbot_inbound.go` 的早期守卫用 `(channel_id, event_ext_id)` 去重，覆盖“未知/未配对 chat”这类尚未进入 intake 管线的消息。
- 因此本版本**不能直接删除 `im_bot_inbox`**。设计 §7.2 的读切换安排在下一个版本。

**读切换（下一版本执行）的前置条件**：

1. 双写已稳定运行至少一个发布周期。
2. 对账通过。SQLite / PostgreSQL 通用写法：

   ```sql
   -- intake_events 中 IM 事件的条数
   SELECT COUNT(*) FROM intake_events WHERE source_kind = 'im_bot';

   -- 旧表的条数（只比较有真实平台 event id 的事件）
   SELECT COUNT(*) FROM im_bot_inbox;

   -- 找出在 intake_events 有、旧表没有的事件（按 channel_id + event_ext_id）
   SELECT e.source_ref, e.event_ext_id
   FROM intake_events e
   LEFT JOIN im_bot_inbox i
     ON i.channel_id = CAST(e.source_ref AS INTEGER)
    AND i.event_ext_id = e.event_ext_id
   WHERE e.source_kind = 'im_bot' AND i.id IS NULL
   LIMIT 50;
   ```

   第三条应只返回“合成 event id / 无平台 id”的预期差异；若出现真实消息缺失，先排查再切。

**切换动作**（随版本发布，不是配置开关）：

1. 修改 `server/internal/service/imbot_inbound.go` 的早期去重守卫：从 `im_bot_inbox` 改为查询 `intake_events`（`source_kind='im_bot' AND source_ref=channelID AND event_ext_id=eventID`）。
2. 处理“未知/未配对 chat”：这类消息可能从未进入 intake 管线。切换前要么保证早期守卫仍保留一张轻量去重表，要么让守卫先把所有入站事件写入 `intake_events` 再分诊。**这是切换的唯一致命点，必须在测试环境验证重投幂等。**
3. 保留双写一个版本；确认无回归后，移除 `recordLegacyInbox` 与守卫中的旧表读写。

**持续观察**（切换后一个版本）：

```sql
-- 双写差异应为 0（除合成 id）
SELECT COUNT(*) FROM intake_events WHERE source_kind='im_bot';
SELECT COUNT(*) FROM im_bot_inbox;
```

**删除 `im_bot_inbox`**（读切换验证一个版本之后）：

```bash
# 1) 备份
sqlite3 ~/.niuniu/niuniu.db ".backup 'niuniu-before-drop-inbox.db'"
# PostgreSQL:
pg_dump -Fc -t im_bot_inbox niuniu_server > im_bot_inbox.dump
```

```sql
-- 2) 先停写（发布移除了双写的版本），确认无进程写入后再删
DROP TABLE im_bot_inbox;
```

注意：`im_bot_inbox` 没有外键依赖它，删除不会级联；但旧版本二进制若回滚后会重新创建/使用它，所以**删除动作必须发生在确认不再回滚旧版本之后**。

**回滚**：

- 读切换后发现问题：回滚到上一版本二进制（读 `im_bot_inbox`）；因为双写仍在，旧表数据完整，无需补数据。
- 已移除双写但未删表：回滚二进制即可恢复写旧表。
- 已删表：从上述备份恢复；或用 `intake_events` 回灌（SQLite；PostgreSQL 把 `INSERT OR IGNORE` 换成 `ON CONFLICT DO NOTHING`）：
  ```sql
  INSERT OR IGNORE INTO im_bot_inbox (channel_id, event_ext_id)
  SELECT CAST(source_ref AS INTEGER), event_ext_id
  FROM intake_events
  WHERE source_kind = 'im_bot' AND event_ext_id <> '';
  ```

### 5.3 `issues.goal_condition`

- **不回填、不改写存量 issue**（设计 §7.3 / §13）。存量 issue 的 autohost 行为保持不变。
- 新建的 intake 任务由 Task Brief 渲染 `goal_condition`（缓存），验收项物化为 `issue_checklists.kind='acceptance'`。
- 手工 issue 没有 Brief 时继续使用原 `goal_condition`；不会因为升级而变化。
- 若需要修正某个存量 issue 的验收标准，人工编辑其 `goal_condition` 或创建 Task Brief；不要批量回填。

### 5.4 新增数据表与运行期状态

本能力共新增 12 张表（SQLite / PostgreSQL 双写）：

| 分组 | 表 | 用途 |
|---|---|---|
| Intake 配置 | `intake_settings` | 每 (project, source_kind) 的模式、daily_cap、allow/bot list |
| Intake 配置 | `intake_rules` | 规则：instruction + conditions + priority + enabled |
| Intake 配置 | `intake_policy_versions` | 每次设置/规则变更的策略快照（只增不改） |
| Intake 配置 | `intake_project_state` | 项目级暂停开关（`paused`）；无行 = 未暂停 |
| Intake 事件 | `intake_events` | 入站事件，UNIQUE(source_kind, source_ref, event_ext_id) 做幂等 |
| Intake 事件 | `intake_decisions` | 每条事件的决策（accept/resume/ignore/unavailable）与执行结果 |
| Intake 事件 | `intake_daily_usage` | 每日自动接活配额的原子占位 |
| GitHub | `github_source_poll_state` | 每 (project, repo, event stream) 的 since/ETag 游标与退避 |
| GitHub | `github_thread_bindings` | 每 (project, repo, PR/issue) 的 thread → issue/workspace 绑定 |
| Completion | `task_briefs` | Task Brief 1:N 追加式版本 |
| Completion | `completion_settings` | 项目默认完成策略 |
| Completion | `completion_profiles` | 命名完成策略 |

**`intake_daily_usage`（daily cap 的 TOCTOU 修复）**

- 每 (project_id, source_kind, day) 一行，`used` 为已占用配额；`day` 取触发事件时间所在日历日（`YYYY-MM-DD`）。
- 占位是一条原子语句：

  ```sql
  INSERT INTO intake_daily_usage (project_id, source_kind, day, used)
  VALUES (?, ?, ?, 1)
  ON CONFLICT (project_id, source_kind, day)
  DO UPDATE SET used = intake_daily_usage.used + 1
  WHERE intake_daily_usage.used < ?;   -- cap
  ```

  影响行数为 0 即配额用尽。这避免了“先 count 再 insert”在并发下多接任务。
- 只有 `inferred` 的自动执行占用配额；`explicit` 请求不占。
- 执行前失败会 `ReleaseIntakeDailySlot` 归还；执行成功后不归还。
- 该表只做配额计数，不保存事件内容。行数 ≈ 项目数 × 来源数 × 天数，无需常规清理；若要清理，只删历史 `day`，不要删当天行。

**`github_source_poll_state`（个人版轮询游标）**

- 每 (project_id, repo, stream) 一行；`stream` 是事件流（`issue_comment` 等）。
- 字段：`since_ts`、`etag`、`failure_count`、`backoff_until`、`last_polled_at`、`last_error`；时间用 RFC3339 文本保证 SQLite/PG 一致。
- 排查轮询问题先看 `last_error` 和 `backoff_until`。
- **不要手删这张表来“强制刷新”**：删行会让下一次轮询从默认起点重拉，可能产生大量历史事件（仍会被 `intake_events` 去重，但会浪费配额）。确需重拉时，只把目标行的 `since_ts`/`etag` 更新到期望位置。

**`github_thread_bindings`（续接依据）**

- 每 (project_id, repo, thread_ext_id) 一行，记录绑定的 `issue_id` / `workspace_id`。
- `state='ended'` 表示 PR 已关闭/合并：后续评论仍会记录为事件，但不再新建或续接任务。
- 排查“评论没续接”时先看该行是否存在、`state` 是否为 `ended`。

**备份**：以上表都在主库内，按 §1 的整体备份即可；不需要单独导出。回滚二进制时**保留**这些表（纯增量，旧版本会忽略）。

---

## 6. 日常运维

### 6.1 策略版本（policy versions）

- 每次保存来源设置或规则，都会在同一事务写一版 `intake_policy_versions`（版本号按 project + source 单调递增）。
- 快照内容：`source_kind`、`mode`、`daily_cap`、`allow_list`、`bot_list`、`rules[]`（含 name/instruction/conditions/priority/enabled）。
- 查看：`GET /api/projects/:id/intake/policy-versions?source_kind=github&limit=20`，或设置页的「策略历史」。
- **当前没有 restore API**。策略回滚 = 依据目标版本的快照，用 `PUT /intake/settings` + 规则 CRUD 重新写入；规则 ID 会变，但内容一致。回滚后会产生一个新版本，这是预期的审计轨迹。

### 6.2 待校准队列与开启 auto

- 入口：全局 `/intake` 页面；项目设置与 Mission control 只显示计数/入口。
- 每条决策包含：来源、actor、thread、强度（`explicit` / `inferred` / `denied`）、模式、原因、`would_do`（若未执行）。
- 采纳：可先编辑 Task Brief，再创建 issue/workspace。
- 忽略：**必须填原因**，否则无法保存；这是认可率指标有效的前提。
- 校准门槛由服务端返回，不要在客户端硬编码：`calibrated >= 10` 且 `acceptance_rate >= 0.8`。
- 达到门槛只是“建议开启”；切换到 `auto` 需要确认对话框二次确认。
- 暂停后恢复**不需要重新校准**（设计 §12-10）。

### 6.3 暂停接活

有两级开关，语义相同：

| 级别 | 入口 | API |
|---|---|---|
| 项目级 | 项目设置顶部 / 任务来源页 | `PUT /api/projects/:id/intake/pause` `{"paused":true}` |
| 全局 | Mission control | `PUT /api/mission-control/pause` `{"paused":true}`（对调用者可见的所有项目逐一设置） |

效果：

- 所有来源的模式被强制按 `off` 运行：**停止 inferred/observe 自动工作**。
- **显式请求仍然工作**：allow list 内 actor 的直接消息、已配对 chat 的显式 @ 仍可创建/续接任务。
- 已在执行的 workspace 不受影响；暂停只影响新的入站决策。
- 恢复后无需重新校准；不会补做暂停期间的事件。

### 6.4 指标

没有 Prometheus `/metrics` 端点；指标由 Mission control 面板（`GET /api/mission-control`）从数据库实时聚合。默认窗口 30 天（“今日”类按 UTC 当日）。

**接活面板（intake）**

| key | 含义 | 告警阈值 |
|---|---|---|
| `today_accepted` | 今日已执行的接活决策数 | — |
| `pending` | 待校准队列条数 | — |
| `approval_rate` | 已执行 accept/resume ÷（已执行 + 忽略） | <80% 且样本 ≥10 时 warning |
| `misfire_rate` | auto 执行后 24h 内 issue 被放弃的比例 | >10% warning |
| `duplicate_rate` | 同来源+thread 一小时内重复自动接活比例 | >10% warning |
| `event_to_start` | 事件到 workspace 启动的分钟数 | >60 分钟 warning |

**自动完成面板（completion）**

| key | 含义 | 告警阈值 |
|---|---|---|
| `in_progress` | 当前运行中的 workspace 数 | — |
| `stuck_rate` | 卡住 workspace 占比 | >20% warning |
| `first_pass_rate` | 完成且从未出现人工介入/ask_user 的比例 | — |
| `avg_rounds` | 完成 issue 的平均 advance 轮数 | — |
| `fake_completion_rate` | 完成后被重新打开的比例 | >10% warning |
| `interventions` | 今日人工介入/提问次数 | — |
| `today_tokens` | 今日 token 总量 | — |
| `today_cost` | 今日成本（USD） | — |

指标为 `no_data` 表示窗口内无可计算样本，不等于 0。

### 6.5 日常巡检

| 频率 | 检查 |
|---|---|
| 每日 | 待校准队列是否积压；`approval_rate` 是否低于 80%；`misfire_rate` / `duplicate_rate` 是否超阈值 |
| 每日 | 个人版：`GET /intake/github` 的 `last_error` 是否为空、`last_polled_at` 是否前进 |
| 每日 | 团队版：GitHub webhook 最近投递是否 200；401 数量是否异常 |
| 每周 | 策略版本是否有非预期变更；暂停开关状态是否需要恢复 |
| 每周 | `fake_completion_rate`、`stuck_rate`、`interventions` 趋势 |
| 每月 | GitHub PAT 过期时间；webhook secret 轮换计划；token 成本 |

### 6.6 常见故障

| 现象 | 排查 |
|---|---|
| 轮询一直 `last_error` | PAT 过期 / 无 repo 权限 / 网络出站被拦 / GitHub 限流（`backoff_until` 为限流重置时间） |
| 轮询正常但 `last_polled_at` 不动 | `intake.github_poll_cron` 被设成空；调度器未注册（看启动日志） |
| Webhook 401 | secret 不一致；凭据未带 `webhook_secret`；或 PATCH 时未带 token 覆盖掉了原 config |
| Webhook 404 | binding ID 错；provider 与路由不匹配（email/webhook 绑定不能投到 github 路由） |
| Webhook 200 但无决策 | 事件类型未启用（`ignored:true`）；看 `GET /intake/github` 的 events 配置 |
| 消息被记录为 denied | actor 不在 allow list，且来源没有“接收全部”权限；bot 永远不能获得 high-risk 权限 |
| 自动接活停止 | 项目/全局暂停；daily_cap 用尽；模式被改为 `off` |
| 任务不自动完成 | completion `autohost_enabled=false`；`max_rounds` 或 `token_budget` 用尽转人工；验收项未勾选；policy 解析失败会 fail-closed 转 attention |
| 删除 profile 返回 409 | 仍有 issue 引用；先改 issue 的 `completion_profile_id` |
| `[AUTOHOST_DONE]` 被拒绝 | 仍有未完成的 `kind='acceptance'` 项；或验收查询失败（fail-closed） |

日志检索关键词：`github source:`、`intake:`、`autohost:`、`webhook`。

---

## 7. 回滚

按影响面从小到大执行；每一级都可单独使用。

### L1 — 暂停接活（秒级，无重启）

```bash
curl -X PUT "$BASE/api/mission-control/pause" -H 'Content-Type: application/json' -d '{"paused":true}'
```

效果：停止所有 inferred/observe/auto 接活；显式请求仍可用。恢复：`{"paused":false}`。

### L2 — 关闭自动完成（秒级，下一个 turn 生效）

项目设置 → 自动完成，关闭 `autohost_enabled`；或：

```bash
curl -X PUT "$BASE/api/projects/123/completion/settings" \
  -H 'Content-Type: application/json' -d '{"autohost_enabled":false}'
```

效果：autohost 不再续跑；已运行的 agent 不受影响。按 issue 覆盖的 profile 也需逐一处理（`issue profile > 项目默认`）。

### L3 — 回滚 IM 语义迁移（需重启服务）

```bash
NIUNIU_INTAKE_LEGACY_CHAT_MODE=true
```

效果：每 chat 的生效模式改由 `agent_mode` 推导（`command→off`、`observe→auto`），恢复升级前行为；不改写数据，可随时取消。

### L4 — 关闭 GitHub 轮询（需重启服务）

```yaml
intake:
  github_poll_cron: ""
```

效果：调度器不再注册轮询；Webhook 入站不受影响。手动 `RunOnce` 仍可用于补数据。

### L5 — 停用 Webhook 入站（秒级）

- 删除绑定：`DELETE /api/projects/:id/intake/webhook/:bid`（email/generic）。GitHub 绑定走外部源删除接口/UI。
- 或在反代层临时拒绝 `/api/integrations/*`（更快的止血）。
- 删除后该端点的请求返回 404/401，不再进入管线。

### L6 — 回滚二进制 / 版本

- 数据库变更是**纯增量**：新表、新列，旧二进制可忽略它们，直接回滚二进制是安全的。
- 回滚步骤：
  1. 停服务
  2. 恢复上一版本二进制/镜像
  3. 启动
  4. 验证 `/api/health`、看板、workspace、agent 正常
- **不要**为了回滚而删除新表；保留它们，重新升级时无需重跑迁移。
- 仅当数据库本身损坏时，才使用第 1 节的备份恢复；恢复后会丢失备份点之后的数据。

### 7.1 回滚验证清单

- [ ] `GET /api/health` 200
- [ ] 已有 workspace 可打开、可对话
- [ ] 看板 issue 可正常流转
- [ ] 暂停/恢复开关行为符合预期
- [ ] 旧 IM chat 行为与升级前一致（若使用 L3）
- [ ] 无新的 `intake_decisions` 被执行（若使用 L1/L2）

---

## 8. 上线验收清单（对应设计 §10）

- [ ] Intake 与 Completion 可独立配置，修改一个不影响另一个。
- [ ] `observe` 下事件只产生决策，不创建 issue/workspace、不发消息。
- [ ] 队列可完成「查看 → 采纳/忽略 → 进入 Issue」闭环；采纳前零副作用。
- [ ] `off` 模式下 allow list 内 actor 的显式消息仍能创建/续接任务。
- [ ] 未列名单 actor 按 policy 记为 `inferred`/`denied`，不越权建任务。
- [ ] 已绑定 thread 的后续评论/PR 评论续接同一 workspace。
- [ ] intake 任务的 `goal_condition` 来自 Brief，验收项与 `kind='acceptance'` 一致；手工 `kind='task'` 行不变。
- [ ] 验收项未完成时 `[AUTOHOST_DONE]` 不被接受；达到 max rounds / token budget 转人工并提结构化问题。
- [ ] 同一事件重复投递或服务重启后只产生一个任务。
- [ ] 接活/完成指标分别可见；阈值告警可触发。
- [ ] 迁移后现有 IM command/observe 行为不变（或 L3 回滚可用）。
- [ ] 项目设置两个独立入口可用；移动端、键盘、Dark mode、设计系统检查通过。
- [ ] 个人版在无公网环境完成一次「评论 → 任务」验证。
- [ ] 团队版完成一次「GitHub webhook → 任务」验证，伪造签名被 401 拒绝。

> Epic（#755）本身**不适用自动完成**：必须人工审核后手动移入「完成」列。上面的清单用于人工审核，不要把 `[AUTOHOST_DONE]` 当作 Epic 的收尾依据。

### 8.1 已知问题（不阻塞本次上线）

| 项 | 状态 | 处理 |
|---|---|---|
| 仓库级 `pnpm lint` 仍有 29 个 `main` 既有错误 | 既有基线，本 Epic 未新增；本 Epic 改动文件 0 错误 | 作为独立 chore 跟踪，**不在本 issue 清理** |
| 团队版 GitHub `webhook_secret` 无表单入口 | 可用 API 完成（§4.2） | 后续在凭据对话框补可选字段（独立任务） |
| `im_bot_inbox` 读切换未执行 | 双写已上线；读路径仍为旧表 | 按 §5.2 在下一版本切换、保留一版后删除 |
| GitHub Enterprise BaseURL 不可配置 | 个人版轮询固定 `api.github.com` | 需要时另开任务暴露配置 |

---

## 9. 关联文档

- 设计：[`2026-11-03-intake-completion-autonomy-design.md`](../superpowers/specs/2026-11-03-intake-completion-autonomy-design.md)
- 集成与 PAT：[`docs/integrations.md`](../integrations.md)
- 个人版架构：[`docs/architecture/personal-edition.md`](../architecture/personal-edition.md)
- 团队版部署：`niuniu-enterprise` 仓库 `docs/ai-context/production-deploy.md`
