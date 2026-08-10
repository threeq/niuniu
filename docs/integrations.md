# 集成外部工单系统

niuniu 支持把 **GitHub Issues** 作为外部工单源接入。配置后即可：

- 在 project 浏览面板里搜索 / 拖拽 GitHub Issue 落到 niuniu kanban
- 一键起 workspace，由 Claude 处理
- 状态变更、agent 完成、niuniu 评论自动写回 GitHub

> 后续会扩展 TAPD 等其它 provider，这里只覆盖 v1（MVP）的 GitHub。

---

## 1. 创建 GitHub fine-grained PAT

niuniu 用每个用户**自己的** Personal Access Token 调用 GitHub API，写回的评论 / close 操作会显示为本人。请**不要**和同事共享 PAT。

1. 打开 [GitHub → Settings → Developer settings → Personal access tokens (fine-grained)](https://github.com/settings/personal-access-tokens/new)
2. **Token name**：填 `niuniu`（任意，方便日后辨认）
3. **Expiration**：建议 90 天。到期前需回 niuniu 重新粘贴
4. **Repository access**：选 *Only select repositories*，勾选所有要让 niuniu 操作的 repo（**不要**给 *All repositories*）
5. **Permissions → Repository permissions**：
   - **Issues**：**Read and write**（必填，写回评论 / close 必需）
   - **Metadata**：**Read-only**（GitHub 自动勾选，必填）
   - 其它一律保持 **No access**（最小权限原则）
6. **Generate token**，复制生成的 `github_pat_…` 字符串。这个字符串只显示一次

> 不要使用 *classic* PAT 或 OAuth App token —— v1 校验路径只针对 fine-grained PAT 设计，权限粒度也更细。

---

## 2. 在 niuniu 配凭据

1. 登录 niuniu，打开右上角 → **设置** → **集成 / Integrations**
2. 点 **添加凭据** → 选 **GitHub** → 粘贴 PAT
3. 点 **测试连接**（调 `/api/me/external-credentials/github/verify`）
   - 成功：显示 GitHub 账号 login 和 PAT 过期时间，**保存**即可
   - 失败：先看下面 §7 故障排查

> 个人版（personal）：单用户，凭据只属于自己。
> 团队版（team）：每个用户独立配自己的凭据，互不可见；切到组织 owner 时，看到的是"该 org 内自己的凭据"。

---

## 3. 给项目挂外部源

外部源 = 让 project 知道 "我要从哪个 GitHub repo 拉 issue"。一个 project 可以挂多个源。

1. 打开任一 project 详情页 → 点 **项目设置**（齿轮图标）
2. 进入 **外部源 / External sources** sub-tab（**注**：是设置页内的 sub-tab，不是独立路由）
3. 点 **添加外部源** → 选 **GitHub** → 填 `owner/repo`（如 `acme/foo`）
4. 可选：填默认浏览过滤条件（state / labels / assignee），保存

如果当前用户没配凭据，会提示先去 §2 配。

---

## 4. 浏览 + Import

1. 打开 project 详情页右上角 **外部工单** 按钮 → 弹出抽屉
2. 选择源、用搜索框过滤、切 open / closed
3. 把任一条目**拖到**任一 kanban 列即完成 import
   - 或点单条右侧 **Import** 按钮，选择目标列
4. niuniu issue 创建后保留 `external_source=github` + `external_id=owner/repo#N`，详情页顶部出现 **GitHub** 徽章 + 跳转链接

**去重**：同一 `external_id` 在同一 project 内只能 import 一次。第二次拖拽会高亮已存在的卡片，不会重复建。

**已在外部关闭**：浏览面板会显示外部状态徽章；import 已 closed 的 issue 会出现确认对话框（避免误拉旧 issue）。

---

## 5. 写回触发点

niuniu 在以下事件**自动**写回 GitHub：

| 事件                                                                | 写回动作                                              |
| ------------------------------------------------------------------- | ----------------------------------------------------- |
| 把 issue 拖到 done 列（即 `lifecycle_mapping = 'completed'` 的列）  | close GitHub Issue + 留终结评论                       |
| `harness_run` 完成（Claude agent 跑完一轮）                         | 在 GitHub Issue 加过程评论（含 diff 摘要 + PR 链接）  |
| 在 niuniu issue 页面新增评论                                        | 异步写回到 GitHub 评论区                              |

**写回身份**：
- 每个 niuniu 用户配自己的 PAT，写回时调用调用方本人的 PAT
- GitHub 上看到的 author = 该 niuniu 用户绑定的 GitHub 账号
- 如果当前 actor 没配凭据 / 凭据失效，该 job 进入 failed 状态（详见 §7）

**幂等性**：每条写回评论尾部带不可见的 `niuniu-writeback-key: <hash>` marker。worker 重试不会重复评论。

**暂停写回**（per-issue）：
- 在 niuniu issue 详情页顶部有 **暂停写回 / Pause writeback** toggle
- 打开后该 issue 的所有写回 job 立即停止；新事件不再产生写回
- 关闭可恢复（不会回放暂停期间错过的事件）

**手动重试**：
- 评论写回失败时，niuniu 评论旁出现红色 ⚠ 图标
- 点击图标 → **重试** → 调 `POST /api/issue-comments/:commentId/retry-writeback`
- 重试 N 次后 worker 自动进入 backoff（指数退避，封顶 24h）

---

## 6. MVP 限制（v1 不支持，知悉范围）

以下功能**不**在 v1 范围内，规划 v1.1 / v2 跟进。

- ❌ **niuniu → 外部** 的 assignee / labels / milestone 同步（v1 这些字段编辑只在 niuniu 内可见）
- ❌ niuniu 评论的**编辑 / 删除**写回外部（GitHub 上仍是首次发出的版本）
- ❌ **外部新增评论反向同步**到 niuniu（v1.1 计划：手动"刷新"时拉一份只读快照展示，不入 `issue_comments` 表）
- ❌ 自动**为 issue 创建 PR 并关联**（独立 spec，不在本 MVP）
- ❌ 外部 issue 被**第三方 close** 时主动 poll 反向同步 niuniu 状态（v1 仅在用户手动点"刷新"时显示外部状态徽章；不自动推到 done 列）
- ❌ **TAPD** 集成（v2）

---

## 7. 故障排查

| 现象                                                                       | 检查                                                                                                                                       |
| -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| 浏览面板报 "GitHub 限流恢复倒计时 …"                                       | 等到时间窗口；fine-grained PAT 通常 5000/h，被 hit 多半是误用了 classic token                                                              |
| 凭据验证失败 (401)                                                         | PAT 已过期 / 被撤销 / 没勾选目标 repo。重走 §1 重新生成并粘贴                                                                              |
| 凭据验证失败 (403 + `permission`)                                          | PAT 缺 Issues:Read and write，回 §1 step 5 修改                                                                                            |
| 浏览面板返回 412 + `error_kind=no_credential`                              | 当前用户对 github 没配凭据，去 **设置 → 集成** 配                                                                                          |
| 浏览面板找不到目标 repo                                                    | §1 step 4 没把该 repo 加入 *Only select repositories* 白名单；重新生成 PAT 时勾上                                                          |
| import 时报 "已在外部关闭"                                                 | issue 在 GitHub 已 closed；可选确认仍 import 或取消                                                                                        |
| 评论写回 failed                                                            | hover 红色 ⚠ 看具体错误 message；常见原因：PAT 过期 / repo 被改名 / 网络抖动。点重试                                                       |
| GitHub 评论出现两条一样的                                                  | 不会发生 —— `niuniu-writeback-key:` marker 保证幂等。如果真出现，多半是手动从 GitHub 端复制粘贴了，对照 marker 判断                        |
| niuniu 拖到 done 但 GitHub 没 close                                        | 看 issue 详情页 **写回状态** 区域；可能 actor 凭据失效，或写回被暂停。修复后点重试                                                         |
| 暂停 toggle 开关后，旧的失败 job 还在重试                                  | 已经入队的 job 会跑完一次再停；如要立即停，先关 toggle，再去评论旁手动 cancel                                                              |
| PAT 快到期，提前看哪些到期                                                 | **设置 → 集成** 列表显示每条凭据的 expires_at，7 天内黄色 / 1 天内红色提示（v1.1 加邮件提醒）                                              |

---

## 8. 安全说明

- PAT 在 niuniu 后端以 **AES-GCM** 加密落库，密钥保存在 `~/.niuniu/integration_secret`，文件权限 `0600`
  - 服务器启动时若该文件不存在自动生成；存在则直接复用
  - **不会**自动 fallback 到明文存储；密钥读不到则启动失败（安全第一）
- handler 永远**不**在响应体里返回明文 token —— 即便是凭据列表也只回 `last_verified_at` / `expires_at` 等元数据
- API 调用日志强制 **redact** `Authorization` header 与请求 body 中可能含 token 的字段
- 每次调用 GitHub API，niuniu 后端日志只记录 `provider` / `action` / `external_id` / `actor_user_id` / `duration_ms` / `status`，不记 body
- 若怀疑 `integration_secret` 泄露，运维侧用 `niuniu admin rotate-integration-secret` 轮转（会要求所有用户重新粘贴 PAT，旧密文不可解）

---

如需更多技术细节，参阅 spec：`docs/superpowers/specs/2026-05-12-external-issue-tracker-integration-design.md`。
