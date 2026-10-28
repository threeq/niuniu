# 集成治理 · 团队版

> 本文是团队版集成治理的**唯一需求记录**。原 Epic #740（工作空间 `!954`）及其子任务
> 已按用户要求并入外部系统集成 Epic #726，随后从看板删除；个人版与团队版的全部集成
> 工作此后在 #726 收口。相关文档：`integrations.md`、`integration-authorization-security.md`、
> `integration-draft-pipeline.md`、`egress-data-governance.md`。

## 范围（三项治理）

### 1. 角色模型与审批人

- 组织/项目角色：`owner / admin / member`。
- 把集成侧的**授权动作**（草稿确认、放宽 scope、开启写权限、发布）绑定到角色，
  而不是「任何能打开本机界面的人」。
- 确认卡路由给**有权限的成员**；发起人不能自批（自批请求被拒）。

### 2. 组织凭证策略

- 个人凭证 vs 组织凭证（`owner_type IN ('user','org')` 贯穿核心表）。
- 使用策略：哪些项目/工作空间可以使用某份组织凭证（`credential_usage_grants`，
  scope 为 `project` 或 `workspace`）。
- 凭证链接的**申请者 + owner 绑定**，防越权提交 / 冒领。

### 3. 跨用户审计视图

- 组织管理员视角，按人 / 项目 / 源 / 时间聚合：「谁在何时用哪份组织凭证调了什么」。
- 与个人版审计出口共用同一读取层（不维护两套实现）。

## 代码落点

两个仓库的 `epic/726` 分支均已包含 `epic/740` 的全部提交：

| 仓库 | `epic/740` tip | 相对 `epic/726` 领先提交 |
|---|---|---|
| niuniu（core） | `d3bcde2` | 0 |
| niuniu-enterprise | `10d01a1a` | 0 |

主要提交：

- `6fbcafb` 组织凭证使用策略 + 凭证链接 owner 绑定
- `2fe324f` 集成授权审批：动作绑定角色，确认卡路由给有权限成员
- `3654152` 审批路由测试：确认卡只路由给具备角色的成员且排除发起人
- `b1ba988` 跨用户审计视图（人/项目/源/时间聚合）
- `55dba55` 审计视图三项审查修复：跨租户泄漏、导出静默截断、分页不变量
- `c4d30bf` append-only 守卫扩展到 Go 源码（覆盖 `db.Exec` 里的裸 SQL）
- `7e4cead` 修复 #742 与 #728 的合并残留（审计测试补 usage-policy 依赖）
- `d3bcde2` 团队版治理端到端演示
- 企业仓库冒烟：`deploy/self/smoke/integration-audit.sh`、`integration-health.sh`

## 与个人版的耦合：「允许写入」按 owner scope

团队版能否使用取决于写权限的解析范围，因此「允许写入」统一为 **owner scope**：
授权对象是工作空间的 owner（`user` 个人空间 / `org` 组织），不是调用的用户；
SPA 直调（无 workspace）回落到调用者个人 owner。组织级 grant 因此管辖该组织下
每个工作空间的 agent，与「设置」页开关呈现的粒度一致。

同一 scope 贯穿以下链路（任一处退回 per-user 都会让团队版成员拿到
`write_enabled=false`）：

| 环节 | 位置 |
|---|---|
| 出口鉴权 | `ExternalProxyService.Call` → `writePrefScope` |
| 预检 / 校验 | `external_proxy_verify.go` → 同一 `writePrefScope` |
| 设置开关写入 | `handler_external_proxy.go` → `SetWritePref(owner.Type, owner.ID, …)` |
| 开关列表读取 | `handler_external_proxy.go` → `ListWritePrefs(ownerType, ownerID)` |
| 草稿发布 / 回滚 | `integration_draft_publish.go` → `row.OwnerType / row.OwnerID` |
| office-mail 写开关 | `imapWriteEnabled(ctx, owner.Type, owner.ID)` |
| SQL | `WHERE owner_type = ? AND owner_id = ?`；`ON CONFLICT(owner_type, owner_id, provider_id)` |

同时保留操作级契约授权与出口安全集（非 GET/HEAD 的方法即使声明 `read_only`
也不进入默认安全集，需显式写开关），避免 owner scope 扩大后放行面变大。

## 验收

- 服务层端到端：`TestTeamGovernanceEndToEnd`
  （组织 OrgA + 管理员 + 成员 team-a / team-b + 组织凭证 + 组织数据源：
  team-a 起草 scope_relax → 管理员批准（卡路由给有权限者、自批被拒）→
  默认最小权限拦截 → grant 后绑定 → team-b 经项目绑定使用组织凭证 →
  审计视图按人/项目可查、越权被拦）。
- 冒烟脚本：企业仓库 `deploy/self/smoke/integration-*.sh`。
