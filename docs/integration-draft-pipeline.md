# 集成草稿流水线：draft → lint → 影响范围确认 → 发布 → 验证/回退

> 实现文档（issue #734，Epic #726）。安全原则与确认卡见
> [`integration-authorization-security.md`](./integration-authorization-security.md)；
> 红队演练见 [`integration-onboarding-redteam.md`](./integration-onboarding-redteam.md)。

## 1. 它解决什么

在此之前，Provider 与数据源的创建**即生效**：agent 说一句"帮我接上 X"，配置就落库并可用。
把「AI 改集成配置」从危险变成可控，靠的是一条受控流水线，两条线（工具型 Provider / 数据型
数据源）共用：

```
draft → lint → 影响范围确认 → 发布（默认停用） → 验证探针 → 启用 / 回退
```

三条不可绕过的性质：

1. **草稿不生效**。创建草稿只写一行记录，运行中的集成一个字节都不变。这同时解释了为什么
   agent 任务中断无害：候选在数据库里，不在对话里。
2. **决策面由系统渲染**。确认卡由 `BuildConfirmCard`（#746）从草稿的**结构化字段**构造，
   草稿自带的文本没有任何路径进入决策字段。
3. **授权只能由人做**。agent 可以起草、可以请求；**永远不能**发布、放宽 scope、开启写权限或
   回退。写在服务里，不靠"把按钮藏起来"。

## 2. 数据模型

| 表 | 作用 |
|---|---|
| `integration_drafts` | 一条候选配置（`kind = provider \| data_source`）+ 状态 + lint 结论 + 确认记录 |
| `integration_versions` | 已发布版本链（回退目标）；`active=1` 的那条是当前生效版 |

字段要点：

- `payload` 是**不含任何秘密**的候选配置 JSON。凭证**值**走一次性凭证链接（#727），
  `credential_fields` 只登记**字段名**。
- `status ∈ draft | lint_failed | confirmed | published | rejected`。只有 `confirmed` 能发布。
- `agent_authored` 标记草稿来自 agent 通道；`confirm_json` 存审阅者实际批准的
  `DecisionPayload` 字节（按构造丢弃建议区）；`ack_json` 是已勾选的高危项；
  `second_confirmed` 记录放宽权限的二次确认。
- `integration_versions.scope_json` 把"这一版授予了什么"冗余存一份（数据源是 `scope_config`，
  Provider 是操作集合 + 写开关），这样回退时不必再去现场推导"是否会改 scope"。

## 3. lint

`LintContract`（#729）是契约校验的唯一入口，草稿流水线直接复用它，所以"草稿说合法"与
"发布时会通过"不可能分歧。在此之上补四类：

1. **auth 字段完整性（不校验值）**：策略缺失、header 策略缺 header 名、query_param 缺参数名
   → error；声明了策略却没声明任何凭证字段 → error。值永远不看——那是凭证链接的事。
2. **验证探针必须只读**：由 `LintContract` 的 `verify_operation` 规则覆盖（指向不存在的操作或
   写操作 → error；非 GET/HEAD 的只读声明 → warning，人工确认兜底）。
3. **探针必须被声明**（本流水线附加）：`verify_operation` 为空 → error。原因见 §5。
4. **scope 合法性**：键只允许 `databases` / `tables_allow` / `tables_deny` / `methods`
   （`methods` 仅 http），值必须是字符串列表；`default_access_mode` / `require_confirm` 必须是
   合法枚举。
5. **草稿不含秘密**：`config` 里出现 `password` / `api_key` / `headers` / `private_key` /
   `options.api_key` / `ssh.password` 等任何秘密形状的键 → error（不看值，键存在即拒）。

有 error 的草稿存为 `lint_failed`，且 `ConfirmDraft` 直接拒绝确认——确认卡是给人的决策面，
不是校验器。

## 4. 影响范围确认

确认卡完全复用 #746 的 `BuildConfirmCard`：**单一构造点**，草稿卡片、发布前复核、回退卡片都
走同一个 `buildCardFromPayload`。卡片内容见
[`integration-authorization-security.md`](./integration-authorization-security.md) §4。

本流水线的补充规则：

- **`Title` 取标识符**：`in.Label` 一律留空，`confirmTitle` 回落到 `in.Name`（即
  `target_name`，被强制的标识符）。显示名不授予任何权限，投毒的显示名会把"内部可信 API"
  这类帽子扣到一组危险操作上（§4.1.1 的硬规则）。
- **数据源的写权限也是写权限**：`default_access_mode=readwrite` 映射为卡上的 `write_enabled`
  高危项。两条线的"开启写权限"必须是同一个信号。
- **放宽必须二次确认**：`ConfirmDraft` 在卡片含 `scope_widened` 或 `write_enabled` 时，要求请求
  额外带 `second_confirmation=true`（UI 上是**另一个**独立的勾选框）。缺失时草稿**保持未确认**，
  一次点击不可能同时"放宽权限"和"批准这次放宽"。
- **逐项勾选**：卡片列的每个 `ack_key` 必须出现在 `acknowledged` 里，否则
  `ErrDraftAckIncomplete`。

### 4.1 批准绑定到具体字节

`confirm_json` 存的是 `DecisionPayload(card)` 的字节。发布时**重新**从草稿当前的结构化字段
构造卡片、重算字节，两者不一致 → `ErrDraftStale`，并把确认状态清空要求重新确认。这堵住
RT-07（确认卡重放）：批准了 A、草稿被改成 B、拿 A 的批准发 B——不可能，因为 B 的字节不同。

## 5. 发布：默认停用 → 探针 → 启用

`PublishDraft` 的顺序是刻意的：

1. **授权**：`FromAgent=true` → `ErrAgentCannotAuthorize`，在任何数据库操作之前返回。
2. **批准未过期**：重建决策字节与 `confirm_json` 比对（§4.1）。
3. **快照现场**：记录目标当前是否存在、是否启用、上一版 payload。
4. **应用为停用**：写入候选配置，`enabled=0`。
5. **跑探针**：Provider 走代理的只读验证路径（`Probe=true`，允许尚未启用的 provider，跳过写
   白名单，但仍走凭证解析 / 鉴权注入 / SSRF / 审计）；数据源走连接器 `Ping`。
6. **成功** → 置 `enabled=1`、写入版本链（`active=1`）、草稿置 `published`。
   **失败** → 恢复快照（原有目标回滚 payload 与启用状态；本次新建的目标**删掉**，不留半配置的
   实体），**草稿保留**在 `confirmed`，用户修好原因后可重试。

为什么 lint 要求契约必须声明 `verify_operation`：卡片的探针由**契约里的名字**解析而来
（`resolveVerifyProbe`），而不是发布时另挑一个。如果允许留空，发布时 `SelectVerifyOperation`
会自动兜底挑一个只读操作去打——那是一个**从未出现在卡上**的出网请求，"用户看到的 = 将被执行
的"就被破坏了。声明制把这条缝焊死。

探针的非 GET 形态不需要额外确认：它已经作为**决策区的一行**出现在用户批准的卡上，卡片批准
就是这次请求的确认。

## 6. 回退：改 scope 就要重新确认

`Rollback` 恢复某个历史版本。因为"从历史版本恢复"可能把用户**已经撤销的权限**一起带回来，
它与发布同为授权动作：

- 回退前先用同一构造器算出候选（历史版本 payload）对当前生效配置的卡片。
- 只要**scope 有变化**（任何 `ConfirmScopes` 的 change ≠ unchanged、`diff` 里出现 operation /
  scope 项）**或**卡片有任何高危项 → 返回 `{requires_confirmation: true, card}`，**什么都不应用**。
- 用户看过卡片、给出确认（`confirmed=true` + 逐项勾选）后才真正应用。

这条按字面执行"若 scope 有变化"：即使回退方向是**收窄**也要确认——回退是整份配置的替换，
用户应当重新看到差异。宁可多一次确认，不要少一次。

## 7. 权限原则的落点

| 面 | 能力 |
|---|---|
| MCP（agent） | `draft_integration`（起草）、`get_integration_draft`（回读）。**没有**发布 / 回退工具 |
| `/mcp/integration-drafts/...` | 发布与回退路由**注册了**，但处理器把来源硬编码为 agent → 403 `AGENT_CANNOT_AUTHORIZE`（不是 404：agent 应该学到"这个动作归用户"，而不是"路由不存在"） |
| 服务层 | `PublishDraft` / `Rollback` 在 `FromAgent=true` 时于任何数据库操作前返回 `ErrAgentCannotAuthorize` |
| SPA（人） | `/api/me/integration-drafts/...` 全量能力 |

来源由**路由分组决定**，不从请求体读取，所以 agent 无法自称是用户的 UI。

已有测试：`TestDraftPipeline_AgentCannotPublishOrRollback`（服务层）、
`TestDraftRoutes_AgentSurfaceRefusesAuthorizationActions`（HTTP 层，断言 403 +
`AGENT_CANNOT_AUTHORIZE`）。

## 8. 前端

- `components/integrations/IntegrationDraftPanel.tsx`：草稿列表 → 确认卡 → 二次确认勾选框 →
  发布 → 版本列表与回退（回退需要确认时，在同一处渲染服务端返回的卡片）。
- 卡片本身由 `IntegrationConfirmCard`（#746）渲染，面板**不拼任何授权文案**。
- `confirmLabelKey` 让面板把按钮文案改成"确认影响范围"——本流水线里确认与发布是两步。

## 9. 已知边界（如实记录）

- **`lint_failed` 的草稿不能原地修改**：没有 payload 编辑接口，需要删除后重新起草。发布阶段
  的失败（凭证过期 / 连不通）走的是"保留草稿 + 重试"，那才是主要的重试路径。
- **内置 Provider 不可被草稿改写**：`created_by='system'` 的 provider 在 `applyProvider` 直接
  拒绝，避免草稿覆盖 github / gsc / serp-api 的锁定配置。
- **发布使用发布者本人的凭证**（SPA 路径 `WorkspaceID=0`），与既有 `PublishProvider` 一致。
- **回退失败不改变现状**：恢复的配置若探针不过，线上配置保持回退前的样子（快照已恢复），
  并提示"旧版本依赖的凭证可能已失效"。
