# 出口数据治理（body 指纹 / 数据摘要 / DLP 钩子）

> Epic #726 / #747。白名单与 scope 管「**能不能调**」；本文档描述的另一半——「**发什么数据出去**」。

## 1. 问题

白名单（method + path glob）和 scope_config 只约束**调用目标**。一个 agent（或沙箱函数）可以把本地敏感数据
POST 到**完全合法**的白名单端点：端点对了，数据却泄了。此前的审计只记 method/path，出事之后无法回答
「到底发了什么」。

治理分三层，逐层加强：

| 层 | 手段 | 记录/展示 |
|---|---|---|
| 审计 | 出口写操作记 **body 指纹** | sha256 + 字节数 + 顶层字段名（**不记值**） |
| 确认 | 写确认卡展示 **数据摘要** | 字段名 + 大小 + 敏感模式命中 |
| 告警 | 单次出口 body 超阈值 | 审计标红 + 通知 |
| 阻断 | 组织级 **DLP 策略**（团队版） | `EgressPolicy` 钩子 |

## 2. Body 指纹审计

代理出口（`call_external_api`）对每次**带 body** 的调用，在 `external_api_audit` 写入：

- `body_sha256`：实际发出的字节的 SHA-256（同一逻辑调用可据此重放识别）
- `body_bytes`：字节数
- `body_fields`：**顶层字段名**的 JSON 数组（排序、上限 100，截断以 `"..."` 结尾）

**绝不落值**：`fingerprintBody`（`server/internal/service/external_proxy.go`）只取 key，不取 value。
读取出口是 `GET /api/me/integration-activity`（设置 → 集成 → 集成活动，#728）。

## 3. 写确认数据摘要

写操作确认前展示「**将发送的数据摘要**」，而不只是端点。摘要由
`EgressDataSummary`（`server/internal/service/egress_governance.go`）生成，同样是**无值**的：

```go
type EgressDataSummary struct {
    Bytes           int64                // 字节数
    Fields          []string             // 顶层字段名（排序，上限 100）
    FieldsTruncated bool                 // 字段名是否被截断
    SensitiveHits   []EgressSensitiveHit // 敏感模式命中（按类别，无值）
}
type EgressSensitiveHit struct {
    Pattern  string   // 类别：credential / payment_card / national_id / email / phone
    Severity string   // high / medium
    Count    int      // 命中次数
    Where    []string // 命中的字段路径（如 "user.token"），上限 5；**不含值**
}
```

出现位置：

- **工具型（Provider 线）**：`call_external_api` 的 `dry_run` 预检（`CallPrecheck.DataSummary`），
  以及 `PrecheckCall` 返回的 `EgressVerdict`。
- **数据型（数据源线）**：`run_data_query` 写操作的确认卡（`PermissionService`，tool_name=`DataQuery`）
  的 `data_summary` 字段；SPA 由 `PermissionInputRenderer` 渲染。

> 敏感模式只报告**类别 + 命中次数 + 字段路径**，从不报告匹配到的子串。因此确认卡与告警都不会变成
> payload 的第二份副本。

检测是**穷尽**的：遍历所有节点、扫描每个字符串值的**全文**（不截断），非 JSON body 也按原文扫描；
只有「上报的字段路径」有上限（5 条）。因此把敏感值放在长前缀之后、大量节点之后，或用非 JSON 编码，
都不能绕过检测——一个会漏报的绊线不算绊线。模式均为 RE2（线性时间、无回溯），全量扫描是 O(body 大小)。

## 4. 阈值告警

`DefaultEgressWarnBytes = 1 MiB`（`egress_governance.go`）。单次出口 body 超过阈值：

- 出口时经 `EgressAlertNotifier` 广播 `egress_alert` 通知（站内 WS + 尽力 IM 推送，owner 维度）；
- 审计读取侧用**同一阈值**把该行标记为 `oversize`（派生标记，不落库——行里已有 `body_bytes`，
  阈值变化时不会残留过期标记），设置页的集成活动面板将其标红。

阈值是「批量外泄」的绊线，不是闸门：个人版**只告警**。

## 5. DLP 钩子（团队版接点，Epic #740）

组织级出口策略通过一个接口接入，**不改任何调用路径**——决策应用在唯一的出口点：

```go
// EgressPolicy 是组织级出口策略接点（#747 留接口，策略实现归团队版 #740）。
type EgressPolicy interface {
    Evaluate(ctx context.Context, req EgressRequest) EgressVerdict
}

type EgressRequest struct {
    UserID     int64
    Provider   string
    ProviderID int64
    Method     string
    Path       string
    Summary    EgressDataSummary // 无值摘要；策略无需看到数据即可决策
}

type EgressVerdict struct {
    Decision EgressDecision // allow | warn | block
    Reasons  []string       // 面向用户的无值说明
}
```

- **allow**：放行，不标记。
- **warn**：放行，标记审计行并通知（个人版默认档）。
- **block**：拒绝调用（`ProxyError{Code:"egress_blocked"}`），用于「允许写的源 / 大小上限 / 敏感模式阻断」。

接入方式：

```go
proxySvc.SetEgressPolicy(myOrgPolicy)          // ExternalProxyService
auditSvc.SetEgressPolicy(myOrgPolicy)          // IntegrationAuditService（读取侧标红，阈值一致）
proxySvc.SetEgressAlertNotifier(myNotifier)    // EgressAlertNotifier
```

`EgressRequest` 只携带无值摘要，因此策略（即便部署在远端）无需接触 payload 即可决策；`block` 在
**授权之后、数据出口之前**强制，任何路径都不绕过。

### 个人版默认行为

`DefaultEgressPolicy` **只告警、从不阻断**：超阈值或命中敏感模式 → `warn`。个人版不会因为一个启发式
判断打断用户的正常工作流。

## 6. 代码位置

| 关注点 | 文件 |
|---|---|
| 摘要 / 敏感模式 / 策略接口 / 默认策略 | `server/internal/service/egress_governance.go` |
| 出口评估 + 告警（Provider 线） | `server/internal/service/external_proxy.go` |
| 预检数据摘要 | `server/internal/service/external_proxy_verify.go` |
| 数据线确认卡摘要 | `server/internal/service/data_proxy.go` |
| 审计读取侧标红 | `server/internal/service/integration_audit.go` |
| 告警投递 | `server/internal/server/handler_integration_egress.go` |
| 通知主题 | `server/internal/notify/types.go`（`egress_alert`） |
| 前端展示 | `server/web/src/components/integrations/IntegrationActivityPanel.tsx`、`.../agent/PermissionInputRenderer.tsx` |
