# Ops Bot 运维手册（Runbook）

对应设计：`docs/superpowers/specs/2026-11-06-niuniu-ops-bot-design.md` §16、§17
对应实现：`server/internal/opsbot/`（配置与生命周期）、`/browser`（运行时）、`/channel`（平台适配）、`/content`（内容管线）、`/packs`（Platform Pack）
验收用例：`server/internal/opsbot/e2e_test.go`

本手册面向运维/运营负责人，覆盖接入、授权、Pack、预算、审批、风控、回滚和常见故障。

---

## 1. 能力边界

Ops Bot 负责「账号级运营自动化」，但把不可逆、高风险和外发动作交给人工：

- **会做**：对话式接入、账号级配置与隔离、选题与文案/封面/视频生产、合规门禁、发布编排、指标采集、互动建议、总监跨账号排期与去重、Pack 生成/校验/导出导入、周报复盘与爆款拆解回流。
- **不会做**：代持账号密码；把凭据写进对话、日志或 Pack；自动通过审批；批量关注/点赞/私信；绕过平台频次或人工复核；默认接入第三方代发 SaaS。
- **默认人工确认**：发布（publish）、回复（reply）、视频成片（video）三类动作默认都生成审批卡，可按账号关闭对应开关。互动永远只生成建议，发送必须经过审批或显式批准。
- **平台差异只进 Pack**：新增平台通过对话生成 Pack（OpenAPI / MCP / 浏览器 recipe），不改 Ops Bot 核心。

## 2. 对话接入

入口复用 IM Bot，`Onboarding.Handle` 负责状态机。支持的自然语言入口：

```
接入公众号 / 接入小红书 / 接入 Facebook / 接入知乎
接入新渠道 <名称>        # 没有已安装 Pack 时进入 Pack 生成流程
```

流程（以公众号为例）：

```
用户：接入公众号
Bot ：已选定「微信公众号」Pack wechat_mp v1。账号名称是？
用户：品牌公众号
Bot ：主要目标是涨粉、转化还是品牌？
用户：涨粉与品牌
Bot ：每周发几篇？要热点加更吗？
用户：每周3篇，要热点加更
Bot ：每月预算多少？（回复「不限」表示不限额）
用户：每月 500 元
Bot ：发布前需要你确认吗？
用户：需要
Bot ：账号已登记。请点这个一次性链接完成授权：https://…
用户：完成
```

常用命令：

| 命令 | 作用 |
|---|---|
| `/bots` | 列出本 owner 的 Bot 与状态 |
| `/bot <id>` | 查看单个 Bot |
| `/approvals` | 查看待处理审批 |
| `/pause <id>` / `/resume <id>` | 暂停 / 恢复账号自动动作 |
| `/budget <id>` | 查看月度预算与账号状态（调额走预算接口 `Budget.SetLimit`） |
| `/calendar` | 内容日历（首篇内容生成后可用） |
| `/cancel` | 取消当前对话流程 |

未安装 Pack 的渠道：Bot 会先询问 OpenAPI 文档 URL/JSON、`mcp:<provider>` 或 `skip`（生成浏览器 recipe 草稿），缺失事实逐项追问，草稿必须通过沙箱测试才能发布。

## 3. 账号授权

- 授权只通过**一次性链接/二维码**完成，会话只出现链接，不接收也不回显明文密钥（`CredentialLinker` 契约）。
- 凭据按 `credential_refs_json` 中的 `{app, account}` 槽位解析；不同账号的凭据、profile、预算互不串号。
- 浏览器渠道（小红书/知乎等）每个账号使用独立 profile：`<DataDir>/ops/browser/<botID>/local/`，登录态留在磁盘；同一 profile 被两个 Bot 复用会直接报 `ErrProfileShared`。
- 扫码/登录态是账号级资产：登录失效时风险控制器会暂停该账号并通知运营人，恢复前必须重新验证登录。

## 4. Platform Pack 管理

### 4.1 目录与版本

```
<DataDir>/<users|orgs>/<ownerID>/ops/packs/<packID>/<version>/
```

- 版本一旦写入不可变；`pack_version=0` 表示跟随最新已发布版本。
- 内置种子 Pack：`wechat_mp`、`xiaohongshu`、`facebook`、`zhihu`（仅作种子/样例）。
- 一个 Pack 的 `allowed_hosts` 是浏览器 recipe 的出网白名单；**空列表 = 拒绝所有导航**。
- recipe 只解释固定动作集：`goto/wait/click/fill/upload/extract/screenshot/assert/loop/branch`。

### 4.2 生成与发布

1. 对话中给出 OpenAPI/MCP 信息，或 `skip` 走浏览器草稿；
2. 逐一回答 `PackQuestions`，不猜测缺失事实；
3. `Generator.Test` 在沙箱（离线 fixtures）跑通每个 recipe，未通过不能发布；
4. `Generator.Publish` 标记 published，账号即可绑定；
5. 生成结果保存在客户环境，永久保留。

### 4.3 导出/导入（跨 owner 迁移）

```bash
# 导出（服务内部接口，等价调用 Registry.Export）
# <DataDir>/<users|orgs>/<ownerA>/ops/packs/<packID>/<version> → zip
# 导入到另一个 owner（Registry.Import）
```

- 导入会校验每个 zip entry，拒绝路径逃逸/非普通文件/多 manifest 的归档。
- 导入后目标 owner 以同一 `packID/version` 独立存在；源 owner 不受影响。
- 迁移后仍需要为账号单独授权（凭据不属于 Pack）。

### 4.4 视频通道边界

- 内置 `xiaohongshu` Pack 只有图文发布 recipe，没有 `publish_video` recipe；因此**小红书视频不走该通道**。
- 视频成片当前走**微信公众号官方 API**：`WeChatAdapter` 支持 `Kind=video`，先上传永久视频素材（`VideoPath`）与封面，再创建并发布草稿。端到端用例见 `TestE2E04VideoArtifactApprovalAndPublish`。
- 若视频目标渠道没有可用的视频发布能力（无 recipe / MCP 能力缺失 / 登录失效 / UI 改版），发布器会**降级为人工发布包**（`manual_pending`），而不是伪装成功；运营人按包手工发布。
- 因此部署前必须确认账号绑定的 Pack 与适配器确实声明了视频发布能力，否则视频任务会在审批通过后落到人工包。

## 5. 预算与审批默认值

### 5.1 预算

`budget_json` 关键字段：

```json
{"currency":"CNY","monthly_limit":500,"unlimited":false}
```

- `monthly_limit <= 0` 表示不限额。
- 成本按 token / 图片张数 / 视频秒数 / API 调用分别记录到 `issue_exec_events.detail_json`：`{"category":"text|image|video|api","unit":"token|image|second|call","quantity":N}`。
- 超限时：`ErrBudgetExceeded` → 账号 `paused`、写入 `paused_reason=budget`、通知运营人，发布/热点加更等预检查都会拦下。
- 调额：`Budget.SetLimit` 写入新上限，并且**只**恢复由预算导致的暂停；人工暂停不会被它清除。

### 5.2 审批

默认策略：`{"publish":true,"reply":true,"video":true}`，审批卡默认 TTL 24 小时。

- 可配置键：`publish`、`reply`、`video`（布尔）、`operator_id`、`reply_hourly_limit`（数字）。
- 每类卡的合法动作：
  - `publish`：approved / rejected / modified
  - `reply`：approved / modified / rejected
  - `video`：approved / rejected / regenerate
- 审批复用 `agent_ask_user_requests`（`kind=ops_approval`）；决策写入 `answers_json` 与 metadata 审计字段：`action`、`approved_by`、`proxy_for`，`decision_source` 为 `user` / `proxy` / `kill_switch`。
- 过期未被处理的卡由 sweeper 置为 `timeout`；已过期卡不可再批。
- 总监可代理审批（`ProxyApprove`），必须带 `OperatorID` 且账号在总监 owner 范围内，代理记录单独审计。
- 回复卡额外受 `reply_hourly_limit` 限制；超限的已批准回复保持 approved，等待下一个窗口，不记为失败。

## 6. 风控与降级

### 6.1 风险信号

| 信号 | 触发条件 | 结果 |
|---|---|---|
| `login_expired` | 凭据失效 / 登录态过期 | 暂停账号、记录会话状态、通知重登；发布降级为人工包 |
| `rate_limited` | 平台限流 | 暂停账号并通知；待发送回复保留待恢复 |
| `captcha` | 验证码挑战 | 暂停账号并通知，等待人工验证 |
| `banned` | 平台封禁 | 暂停并标记会话 down，通知运营人 |
| `adapter_failed` | 连续平台操作失败 | 暂停账号并通知 |
| `kill_switch` | 人工急停 | 禁用账号 |

同一事故只通知一次：最近一次已通知信号写入会话 `last_error` 的 `risk:<signal>` 标记；恢复后清除，下一次事故会重新通知。禁用态（kill switch）不会被降级成「可恢复的 paused」，也不会重复通知。

### 6.2 发布失败路由

| 错误 | 行为 |
|---|---|
| `login_expired` / `unsupported` / `capability_unavailable` / `recipe_broken` / `no_adapter` / `manual` | 降级为人工发布包（`manual_pending`），写 `<DataDir>/ops/manual/manual/<bot>/<idempotency>/payload.json` |
| `rate_limited` / `system_busy` / `transport` 等可重试错误 | 指数退避重试（默认 3 次，500ms 起） |
| `credential_invalid` / `invalid_payload` 等不可重试错误 | 记录 `failed`，打开具体问题 issue，通知运营人 |
| 调用方上下文取消 | 记录保持 `pending`，不判为平台失败 |

UI 改版导致的 recipe 失效属于「数据问题」：**不会暂停账号**，而是降级人工包并告警；修复方式是更新 Pack recipe 版本并切换 `pack_version`，不改核心代码。

### 6.3 Kill switch

触发后按顺序：

1. 账号置 `disabled`（新动作立即停止）；
2. 取消该账号所有 pending 审批（`decision_source=kill_switch`）；
3. 已批准但未发送的回复置 `rejected`；
4. 通知运营人。

即便有待处理记录也已批准，发布器/调度器都会在发送前复查账号状态（`ErrBotNotActive`），记录保持 pending；恢复后不会用旧审批卡自动放行。

## 7. 回滚

| 场景 | 操作 | 说明 |
|---|---|---|
| 单账号异常 | `/pause <id>`（`SetStatus paused`） | 只停自动动作，保留配置与记录 |
| 高风险急停 | Kill switch（`Risk.Kill`） | 禁用账号 + 取消审批 + 清空待发回复 |
| 某个 Pack 版本有问题 | 把 Bot 的 `pack_version` 指回上一个已发布版本 | 版本不可变，切换是回滚；目标版本必须存在 |
| Pack 升级失败/改版 | 用升级前的导出 zip 重新 `Import`，再切换版本 | 升级前先 `Export` 备份 |
| recipe 选择器失效 | 生成/修正新版本 recipe，沙箱测试通过后发布并切换 | 数据修复，不动 Ops Bot 核心 |
| 误暂停 | `/resume <id>` 或 `Risk.Resume` | 需先通过登录校验；预算暂停可用调额恢复 |
| 定时发布需要取消 | 重启进程可取消内存中的定时器；记录保持 pending，由人工决定重发或忽略 | `ops_publish_records` 是事实来源，重发前检查幂等键 |

发布幂等键由 `(bot_id, issue_id, content_version, platform, scheduled_at)` 派生；同一键重复调用返回已有记录，不会重复触达平台。

## 8. 常见错误与排查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| 发布卡在 `pending` | 账号 paused/disabled（预算、风控、急停） | `GET /api/opsbot/...` 查 bot 状态与 `notifications`，先恢复账号 |
| `ErrBudgetExceeded` | 本月费用 + 本次超过 `monthly_limit` | 通过预算接口调高上限（`Budget.SetLimit`），或设为 `0`（不限额） |
| 登录失效 / `login_expired` | cookie/扫码过期 | 重新走一次性授权链接，再 `/resume` |
| `rate_limited` | 平台限流或账号频率过高 | 降低频率，等待窗口后恢复；检查 `reply_hourly_limit` |
| `captcha` | 平台验证码 | 人工完成验证后恢复账号 |
| `credential_invalid` | 凭据错误/缺权限（如 FB 缺 `pages_manage_posts`、`pages_read_engagement`） | 重新授权并确认权限 |
| `recipe_broken` | 平台 UI 改版、选择器失效 | 降级人工包先发出内容；更新 Pack recipe 版本 |
| `capability_unavailable` / `unsupported` | Pack 未声明该能力或 recipe 缺失 | 检查 manifest `capabilities` / `bindings` |
| 未安装 Pack | 该渠道没有已发布 Pack | 走对话生成/导入 Pack |
| 审批无法操作 | 已超时/已取消/已被他人决策 | `ErrAlreadyDecided`；重新发起内容版本与审批 |
| 指标缺日 | 平台当天无数据 | `Sync` 会写入空对象保持曲线连续；属正常 |
| 指标拉取失败 | 登录/限流/权限 | 按对应风险信号处理；失败会触发账号风险路由 |
| Pack 导入失败 | 归档路径逃逸、多 manifest、非普通文件 | 用 `Registry.Export` 重新导出，不要手工打包 |
| 同一账号重复 Bot | `(owner, pack, account_ref)` 唯一 | 复用已有 Bot，或换 `account_ref` |
| 重复发布 | 幂等键不同（内容版本/平台/时间不同） | 先查 `ops_publish_records`，确认是否真的需要再发 |

## 9. 巡检清单

Mission control 运营面板汇总账号、审批、成本与风险；以下为需要人工复核的原始数据源。

- **账号状态**：是否有非预期 `paused`/`disabled`；`budget_json.paused_reason` 是否为 `budget`。
- **待办审批**：`ListPending` 是否有积压或即将过期的卡。
- **预算**：`Budget.Usage` 的 `spent`/`remaining` 与成本分项是否异常。
- **发布**：`ops_publish_records` 中 `pending`/`manual_pending`/`failed` 的占比与原因码。
- **会话**：`ops_browser_sessions` 的 `login_status`、`health`、`risk_state`、`last_error` 是否为 `risk:*`。
- **互动**：`ops_engagements` 中 `escalated`（高风险，需人工）与 `failed`（发送失败）数量。
- **调度**：已批准但长时间未发送的回复（dispatcher 是否运行、账号是否 paused）。
- **Pack**：账号固定版本是否落后于最新发布版本；是否有 sandbox 未通过的草稿。
- **日志/通知**：`opsbot` topic 的 `publish_succeeded`、`publish_manual_pending`、`publish_failed`、`budget_exceeded`、风险告警。

## 10. 设计 §17.2 验收对照

| # | 验收项 | 验证用例 |
|---|---|---|
| 1 | IM 对话完成创建、授权、目标/节奏/预算/审批配置 | `TestE2E01ConversationalWeChatPublishLoop` |
| 2 | 同渠道多账号、Pack 复用、账号级隔离 | `TestE2E02SecondAccountSharesPackWithIsolatedState` |
| 3 | 有 OpenAPI 优先 API，否则 MCP/浏览器 recipe | `TestE2E03XiaohongshuBrowserLoop`、`TestE2E09FacebookPackPublishMetricsAndComments`、`TestE2E10ZhihuPackSandboxAndPublish` |
| 4 | 新渠道由对话生成 Pack，不改核心 | `TestE2E10ZhihuPackSandboxAndPublish`（沙箱）、onboarding/pack generator 单测 |
| 5 | 公众号图文闭环 | `TestE2E01ConversationalWeChatPublishLoop` |
| 6 | 小红书图文闭环（本地浏览器，测试账号） | `TestE2E03XiaohongshuBrowserLoop` |
| 7 | 视频成片进入审批 | `TestE2E04VideoArtifactApprovalAndPublish` |
| 8 | 发布/回复/成片默认需人工确认，可按账号调整 | `TestE2E01`、`TestE2E03`、`TestE2E12KillSwitchStopsAllAutomaticActions` |
| 9 | 互动只生成建议，不自动发送 | `TestE2E03`、`TestE2E09`（Pull→Classify→Suggest→Approval→Send） |
| 10 | 每账号预算独立，超限暂停并通知 | `TestE2E02`、`TestE2E06BudgetBreachPauseNotifyAndRecover` |
| 11 | 总监跨渠道/跨账号排期、去重、分配预算 | `TestE2E08DirectorPlanHotspotAndCrossAccountDedupe` |
| 12 | 所有外发有审批与审计；凭据不出现在对话/日志 | `TestE2E01`～`TestE2E12`（审批元数据/发布记录断言）；凭据仅经一次性链接 |

运行：

```bash
cd server
go test ./internal/opsbot/... -run 'TestE2E' -count=1 -v
```

## 11. C1–C14 可执行验证映射

| 能力 | 端到端 | 单元/集成 |
|---|---|---|
| C1 Bot 与账号管理 | `TestE2E01`、`TestE2E02` | `TestCreateTwoAccountsSamePackAndRejectDuplicate`、`TestUpdateConfigValidatesJSON`、`TestRiskKillDisablesAndCancelsQueuedActions` |
| C2 对话式接入 | `TestE2E01` | `TestOnboardingCreateAccount`、`TestOnboardingSecondAccountReusesPack`、`TestOnboardingConfigCommands` |
| C3 Platform Pack | `TestE2E11PackExportImportAcrossOwners` | `TestRegistry*`、`TestValidate*`、`TestBuiltin*` |
| C4 Pack 生成 | `TestE2E10ZhihuPackSandboxAndPublish` | `TestGenerator*`、`TestOnboardingNewChannelDraftsTestsAndPublishesPack` |
| C5 图文生产 | `TestE2E01`、`TestE2E03` | `TestPipeline*`、`TestBrief*` |
| C6 视频生产 | `TestE2E04VideoArtifactApprovalAndPublish`、`TestE2E05RejectRegenerateAndReapprove` | `TestVideoPipeline*` |
| C7 合规门禁 | `TestE2E01`、`TestE2E04` | `TestReview*`、`TestGate*` |
| C8 发布 | 全部 E2E 用例 | `TestPublishIsIdempotent`、`TestPublishFallsBackToManualPackage`、`TestPublishBlockedWhenBotNotActive`、`TestPublishLoginExpiredDegradesToManual` |
| C9 指标 | `TestE2E01`、`TestE2E03`、`TestE2E09` | `TestMetricsSync*`、`TestMetricsSummary*` |
| C10 互动 | `TestE2E03`、`TestE2E09`、`TestE2E12` | `TestEngagementSendRequiresApproval`、`TestEngagementRateLimitQueues`、`TestDispatcherRespectsKillSwitch`、`TestDispatcherSkipsPausedAccount` |
| C11 浏览器运行时 | `TestE2E03XiaohongshuBrowserLoop`、`TestE2E07LoginExpiredAndRedesignDegrade`、`TestE2E10` | `browser` 包 `TestLocalBackend*`、`TestWorker*`、`TestRecipe*`、`TestZhihu*` |
| C12 总监协调 | `TestE2E08DirectorPlanHotspotAndCrossAccountDedupe` | `TestDirectorPlanAllAggregatesCalendarAndReschedulesConflict`、`TestDirectorDedupe*`、`TestTrend*` |
| C13 复盘回流 | — | `TestWeeklyReportAndBreakdown`、`TestWriteBackFeedsNextBrief`、`TestDirectorWeeklyReportRollsUpAccounts` |
| C14 可观测与治理 | `TestE2E12KillSwitchStopsAllAutomaticActions` | `TestRisk*`、`TestOpsRuntimeWiring`（装配 + dispatcher 启停）、`TestMissionControl*` |

装配检查：

```bash
cd server
go test ./internal/server -run 'TestOpsRuntimeWiring|TestOpsRoutes' -count=1 -v
```
