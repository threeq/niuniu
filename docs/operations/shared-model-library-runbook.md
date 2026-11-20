# 共享模型库 + 用量预算：运维 Runbook

> 适用版本：Epic #840 合入后的 `niuniu` 服务端（个人版 / 团队版共用同一套代码）
> 设计文档：[`docs/superpowers/specs/2026-11-19-shared-model-library-design.md`](../superpowers/specs/2026-11-19-shared-model-library-design.md)
> 实现计划：[`docs/superpowers/plans/2026-11-19-shared-model-library-implementation.md`](../superpowers/plans/2026-11-19-shared-model-library-implementation.md)
> 状态：2026-11-19 编写，对应设计 §15 与 Task 11 验收项。

本文件回答七件事：

1. 共享模型库是什么、数据落在哪。
2. 日常如何维护模型库（UI / MCP / API）。
3. 从 provider 刷新模型失败时怎么排查。
4. 用量是怎么记的、怎么查、口径是什么。
5. owner 硬上限与项目软上限怎么设。
6. 超限后如何恢复，以及误拦截如何兜底。
7. 出问题时按什么顺序回滚。

文中 `$BASE` 指服务地址：个人版默认 `http://127.0.0.1:3000`，团队版例如 `https://self.niu6ai.com`。所有 `/api/models*`、`/api/model-usage/*`、`/api/model-budget*` 请求都在现有鉴权组内，沿用项目/owner 权限；团队版需要登录态。

---

## 0. 一页速查

### 0.1 范围

```
模型库（owner 级共享）
   │  provider 表单下拉 / 手输
   ▼
env_providers（base_url + 凭据 + 模型字段，字符串）
   │  sceneenv.Resolve / ExpandProvider（运行时不变）
   ▼
Claude Code / Codex / Qwen Code
   │  turn 结果事件
   ▼
model_usage_hourly → owner 硬上限 / 项目软上限
```

- **不做代理、不加监听端口、不做请求级 fallback、不做虚拟 key、不做美元成本。**
- 模型库只影响 provider 表单的下拉候选，**不参与运行时解析**：把整张 `models` 表删掉，已有 provider 的 env 展开仍然逐字节一致（有 E2E 覆盖）。
- 模型名在 provider 里仍是自由字符串，任何值都能保存；模型库只是"建议清单"。

### 0.2 数据表

| 表 | 作用 | 关键列 |
|---|---|---|
| `models` | owner 级共享模型名 | `owner_type/owner_id`、`name`、`display_name`、`enabled`、`source`(`builtin`/`refreshed`/`manual`) |
| `model_usage_hourly` | 按小时归因的 token 用量 | `owner/project/workspace/agent_cli/model_name/provider_id/bucket_hour` |
| `model_budget_settings` | owner 硬上限 / 项目软上限 | `project_id=0` 为 owner 硬上限；`>0` 为项目软上限；`monthly_tokens=0` 为不限 |

- 系统内置行：`owner_type='user'`, `owner_id=0`, `source='builtin'`，所有 owner 可见；owner 同名行覆盖显示名/启用状态。
- 内置清单（8 个）：`glm-5.1`、`glm-5-turbo`、`glm-4.5-air`、`deepseek-v4-pro`、`deepseek-v4-flash`、`qwen3.6-plus`、`kimi-k2.6`、`MiniMax-M2.7`。

### 0.3 关键默认值

| 项 | 默认 | 说明 |
|---|---|---|
| 模型启用 | `enabled=1` | 下拉只返回启用行；管理页 `?all=1` 看全部 |
| 预算 | 无行 = 不限 | 不配置时不做任何拦截 |
| 月份边界 | UTC 自然月 | UI 展示时转换本地时区 |
| 预算统计口径 | `input+output+cache_creation+cache_read` | 请求数 = `interaction_count` |
| 统计故障 | fail-open | 用量/预算查询失败 → 放行 + 日志告警 |
| 刷新探测顺序 | `openai → codex → anthropic` | 第一个成功即停止，超时 15s |

### 0.4 接口与工具

| 方法 | 路径 |
|---|---|
| GET | `/api/models`（`?all=1` 含禁用行） |
| POST | `/api/models` |
| PATCH | `/api/models/:id` |
| DELETE | `/api/models/:id` |
| POST | `/api/models/refresh-preview` |
| POST | `/api/models/import` |
| GET | `/api/model-usage/summary?group_by=model\|provider\|project\|workspace\|cli&from=&to=` |
| GET / PUT | `/api/model-budget` |
| PUT / DELETE | `/api/model-budget/projects/:id` |

MCP 工具：`model_list`、`model_create`、`model_update`、`model_delete`、`model_refresh_preview`、`model_import`、`model_usage_summary`、`model_budget_get`、`model_budget_set`、`model_budget_set_project`。

---

## 1. 模型库维护

### 1.1 界面

**设置 → 模型 → 模型库**：

- 表格列：名称、显示名、启用、来源（弱化展示）、操作。
- 顶部：搜索、添加模型、从 provider 刷新。
- 行操作：编辑显示名、启用/禁用、删除（仅 owner 行）。
- 空状态提示：可直接手输模型名，或从 provider 刷新。

### 1.2 行为规则

| 操作 | 行为 |
|---|---|
| 新增 | 写 owner 行；同名已存在则更新显示名/启用状态（不报错） |
| 编辑 | 只能改 owner 行 |
| 禁用 | `enabled=0`，从下拉消失；**已保存的 provider 配置不受影响** |
| 删除 | 只删 owner 行；系统行不可删（400，提示改为禁用）；不检查引用关系 |

> 启用/删除只影响"下一次下拉"，不会改写任何 provider 现有字段。误删模型名不会让正在用的 provider 失效。

### 1.3 Provider 表单

- Model / Haiku / Sonnet / Opus / Subagent / CodexModel 均为**可搜索下拉 + 手输**。
- 手输库中没有的名字时，输入框旁出现"加入模型库"，点击写 owner 行（`source=manual`）。
- provider 行内提供"刷新模型"，打开预览勾选对话框。

### 1.4 命令 / MCP

```bash
# 列出启用中的模型（合并系统 + owner）
curl -s "$BASE/api/models" | jq '.[].name'

# 新增一个模型（幂等：同名更新）
curl -s -X POST "$BASE/api/models" -H 'Content-Type: application/json' \
  -d '{"name":"glm-5.1","display_name":"GLM 5.1","enabled":true}'
```

对话示例：

- "把 GLM-5.1 加到模型库" → `model_create`
- "刷新一下智谱的模型列表" → `model_refresh_preview` → 展示候选 → `model_import`
- "这个月模型用了多少 token" → `model_usage_summary`

---

## 2. 刷新排错

### 2.1 流程

```
选择 provider → 解析 base_url + 凭据（${ACCOUNT:...} 在此解析）
  → 依次尝试 openai → codex → anthropic 的 GET {base_url}/models
  → 第一个成功即停止 → 解析 data[].id / models[].id
  → 返回预览（含 already_in_library 标记）→ 用户勾选
  → 合并进共享库（source=refreshed，已存在跳过）
```

- 预览**不会**自动导入；必须勾选后调用 import，避免聚合商几百个模型污染下拉。
- 刷新只加名字，不绑定 provider。

### 2.2 状态码与处置

| 现象 | 状态码 / 错误 | 处置 |
|---|---|---|
| 端点不存在 | `422 MODEL_REFRESH_UNSUPPORTED` | 该 provider 不支持模型列表，手动输入；换协议或换 provider 再试 |
| 上游返回非 2xx | `502 MODEL_REFRESH_FAILED`（带 endpoint 与响应体前 512 字节） | 检查 base_url、凭据、上游限流 |
| 超时 / 网络错误 | `502 MODEL_REFRESH_FAILED` | 15s 超时；检查出站网络与代理 |
| 返回空列表 | `200 []` | 提示"该 provider 未返回模型"，手动输入 |
| provider 不存在 | `404` | 检查 `provider_id` |
| 无写权限 / 跨 owner | `403` | 检查登录身份与 owner 作用域 |

### 2.3 排查清单

1. **协议与 base_url**：`env_providers.base_urls` 是 `Record<protocol, base_url>`。刷新按 `openai`、`codex`、`anthropic` 顺序找第一个非空的 base_url；只配了 `anthropic` 的 provider 只会走 Anthropic 分支（带 `x-api-key` + `anthropic-version`）。
2. **凭据**：`${ACCOUNT:<name>}` 在服务端解析，账号不存在或 key 为空时请求不带凭据 → 通常会得到 401/403，表现为 502 且带上游状态码。
3. **URL 拼接**：实际请求是 `strings.TrimRight(base_url, "/") + "/models"`。base_url 若已含 `/models` 会拼成 `/models/models`。
4. **模型字段解析**：同时兼容 `{"data":[{"id":...}]}`（OpenAI）与 `{"models":[{"id":...}]}`（Anthropic）；条目优先取 `id`，其次 `name`。
5. **哪些已入库**：`already_in_library=true` 表示系统行或 owner 行已存在；导入时这些名字会被跳过并计入 `skipped`。

---

## 3. 用量口径与查询

### 3.1 记录

- 在每个 turn 的结果事件旁，与既有 `provider_token_hourly` 并行写一行 `model_usage_hourly`；两个视角并存，预算只读 `model_usage_hourly`。
- 归因维度：`owner`、`project`（workspace→issue→column→project 解析，失败记 0）、`workspace`、`agent_cli`、`model_name`、`provider_id`、小时桶。
- `model_name`：优先本次 spawn 实际模型（`NIUNIU_MODEL` 覆盖），否则 provider 的 `codex_model`（Codex）或 `model`，仍为空记 `unknown`。
- 某 turn 未回报 usage：仍记 `interaction_count`，token 记 0，`estimated=1`（UI 标注"用量不完整"）。
- 写入 best-effort：失败只记日志，不影响 turn。

### 3.2 查询

```bash
# 本月总量
curl -s "$BASE/api/model-usage/summary" | jq

# 按 CLI / 模型 / provider / 项目 / workspace 分组
curl -s "$BASE/api/model-usage/summary?group_by=cli" | jq
curl -s "$BASE/api/model-usage/summary?group_by=model" | jq

# 自定义窗口（RFC3339 或 YYYY-MM-DD，date-only 的 to 覆盖整天）
curl -s "$BASE/api/model-usage/summary?from=2026-11-01&to=2026-11-19&group_by=project" | jq
```

`from`/`to` 缺省为「本月 1 日 00:00 UTC」到「现在」。

### 3.3 直查数据库（个人版 SQLite）

```bash
sqlite3 ~/.niuniu/niuniu.db "SELECT agent_cli, model_name, SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) AS tokens, SUM(interaction_count) AS reqs FROM model_usage_hourly WHERE owner_type='user' AND owner_id=1 AND bucket_hour >= date('now','start of month') GROUP BY 1,2 ORDER BY tokens DESC;"
```

- `estimated=1` 的行代表该小时有 turn 没回报 usage，token 偏低是预期现象，不是统计 bug。
- `project_id=0` 表示 workspace 没有关联 issue/项目，或解析失败；`provider_id=0` 表示使用 host 登录、无 provider 绑定。

### 3.4 展示位置

- **设置 → 模型 → 用量**：owner 总量，按项目 / 模型 / CLI / provider 的分布。
- **Workspace 详情**：该 workspace 本月 token 与 owner 预算进度。
- 不在 Mission control 新增面板（避免同一数据两个入口）。

---

## 4. 预算设置

### 4.1 owner 月度硬上限

- **设置 → 模型 → 预算**：输入月度 token 上限，`0` 表示不限。
- API：`PUT /api/model-budget`，body `{"monthly_tokens": 50000000}`。
- **重要**：如果新上限 ≤ 本月已用量，接口返回 `409 MODEL_BUDGET_EXCEEDED` 并带 `used_tokens` / `monthly_tokens` / `period`。想把预算调低到低于已用量，请等下月或先清除（`0` 不限）。

### 4.2 项目软上限

- 只告警、不阻断；只对能解析到项目的 workspace 生效；`project_id=0` 的 workspace 只计 owner 硬上限。
- API：`PUT /api/model-budget/projects/:id`，body `{"monthly_tokens": N}`；`DELETE /api/model-budget/projects/:id` 删除。
- 软上限允许设在当前用量以下（不返回 409）。

### 4.3 检查点与行为

| 位置 | 触发 |
|---|---|
| `AgentProxy.GetOrStartSession` | 新会话启动前 |
| `WorkspaceSession.SendLoop` | 每个新 turn 前 |

- owner 超限：返回 `ErrModelBudgetExceeded`；会话写入明确错误（"本月模型 token 已用 X / Y，请调整预算或等待下月"），并通知 owner（`model_budget` / `exceeded`）。
- 项目软上限超限：允许执行，`ProjectWarning=true`，写一次告警（`model_budget` / `project_warning`），UI 标黄。
- 通知去重：同一 owner + 项目 + 月份只通知一次（进程内 map，重启后允许再提醒一次）。
- 不做请求级预留；一个 turn 内可能少量超支，可接受。

---

## 5. 超限恢复

### 5.1 立即恢复（推荐）

1. 查看现状：

```bash
curl -s "$BASE/api/model-budget" | jq '{monthly_tokens, used_tokens, period}'
```

2. 把上限调到**高于当前已用量**（否则 409）：

```bash
curl -s -X PUT "$BASE/api/model-budget" -H 'Content-Type: application/json' \
  -d '{"monthly_tokens": 100000000}'
```

3. 新会话 / 新 turn 在下一次检查点即放行，无需重启服务。

### 5.2 临时解除

`PUT /api/model-budget` 设 `{"monthly_tokens": 0}` 表示不限。适用于排障或紧急放行；季度结算前记得恢复。

### 5.3 只解除项目告警

项目软上限从不阻断，无需"恢复"；如不希望继续收到告警：

```bash
curl -s -X DELETE "$BASE/api/model-budget/projects/<project_id>"
```

### 5.4 误拦截兜底

- 预算查询故障是 **fail-open**：数据库异常时 `Check` 返回 `Allowed=true` 并记 `slog.Warn`。看到"预算检查失败 (fail-open)"日志说明拦不住用户，属于预期降级，应排查数据库而不是收紧预算。
- 只有明确"已用量 ≥ 上限"才返回 `Allowed=false`。

### 5.5 会话侧表现

- 新会话被拒：`ErrModelBudgetExceeded`，前端错误里带已用/上限。
- 已运行会话的下一 turn 被拒：以 attention/system-info 消息结束，不会把用户消息反复重排重试。

---

## 6. 团队版 / org owner 说明

- 三张表都是 owner 作用域（`owner_type` ∈ `user` / `org`），团队版可直接使用，无需改表。
- 系统内置模型（`owner_id=0`）对所有 owner 可见；org 可覆盖显示名/禁用。
- 用量查询按 owner 隔离；org 成员只能看到自己有权访问的项目/workspace。
- 预算写操作走现有 Authz；org 级预算需要管理员权限。
- **本期不做**：org 管理台、SSO/SCIM、跨实例共享预算、审计导出、按席位授权。企业版可在同一契约上实现治理层。

---

## 7. 回滚

### 7.1 特性开关式回滚（优先）

本特性没有运行时开关，但**默认关闭**：不配置预算、不修改模型库时行为与改动前一致。

按影响面从大到小：

1. **预算拦截误伤** → `PUT /api/model-budget {"monthly_tokens":0}`；项目告警 → `DELETE /api/model-budget/projects/:id`。
2. **模型库下拉异常** → 模型库只影响表单；provider 里的模型名是字符串，直接手输保存即可继续工作。
3. **刷新功能不可用** → 手动输入模型名，不依赖刷新。

### 7.2 代码回滚

- 回滚到 Epic #840 合入前的提交/镜像即可；三张新表是 `CREATE TABLE IF NOT EXISTS` 增量，**旧代码不认识它们，但不会因此报错**，因此可以直接回滚二进制而保留数据。
- 回滚前如仍在拦截，先把预算清零（见 7.1），避免回滚窗口内新会话被拒。

### 7.3 数据清理（谨慎，不可逆）

确认不再需要用量历史后，可删除三张表：

```sql
DROP TABLE IF EXISTS model_usage_hourly;
DROP TABLE IF EXISTS model_budget_settings;
DROP TABLE IF EXISTS models;
```

- 删除后重启服务，`SeedBuiltinModels` 会在下次启动时重新插入内置行（`models` 表被删时需确认 `schema.sql` 已重建表；个人版启动时 `ApplySchema`/迁移会补建）。
- 用量历史删除后不可恢复；预算行删除等于"不限"。
- 生产环境操作前先备份数据库。

### 7.4 回滚验证

回滚后确认：

1. 新建会话能正常启动（不再出现 token 预算错误）。
2. provider 表单仍能手输并保存模型名。
3. `sceneenv.Resolve` 输出与回滚前一致（模型库本就不参与解析）。
4. 无新增监听端口：`ss -lntp`（Linux）/ `netstat -ano | findstr LISTENING`（Windows）与回滚前对比，端口集合不变。

---

## 8. 验收对照（设计 §15.4）

| # | 验收项 | 如何验证 |
|---|---|---|
| 1 | 所有 provider 共用一份模型库；表单可搜索/选择/手输 | 设置 → 模型 → 模型库新增一条，切换任意 provider 编辑框均能在下拉中看到；E2E `TestE2E_ModelSelectionDrivesProviderEnvForThreeCLIs` |
| 2 | 手输新模型可一键加入模型库 | provider 模型字段手输新名 → "加入模型库" → 重开下拉可见；E2E `TestE2E_ManualModelEntryJoinsLibrary` |
| 3 | 刷新有预览、可勾选、可合并；不支持时明确提示 | provider 行"刷新模型" → 预览列表 → 勾选导入；不支持端点返回 422/手动输入提示；E2E `TestE2E_RefreshPreviewImportAndUnsupportedProvider` |
| 4 | 模型可新增/编辑/禁用/删除；系统模型不可删 | 管理页行操作；删除系统行返回 400；E2E + API 契约测试 |
| 5 | 用量可按 owner/项目/workspace/CLI/模型/provider 查看 | `GET /api/model-usage/summary?group_by=...`；E2E `TestE2E_ThreeCLIUsageAttribution` |
| 6 | owner 超限拒绝新会话/turn 并通知；项目软上限只告警 | 设小额度 → 新会话报错 + 收到通知；项目软上限 → 继续执行 + 告警；E2E `TestE2E_OwnerBudgetBlocksNewSessionThenRecovers`、`TestE2E_ProjectSoftCapWarnsAndAllows` |
| 7 | 无新代理/监听端口/虚拟 key；spawn 行为不变 | 回滚验证第 4 步的端口对比；E2E `TestE2E_NoNewProxyOrListeningPort`、`TestE2E_LibraryDoesNotAffectRuntimeEnv` |
| 8 | 团队版 org owner 可直接使用同一套表；治理不在本期 | owner 作用域表结构 + 本节说明；设计 §14 |
