# 牛牛遥测与隐私说明（个人版匿名打开统计）

> 适用范围：**个人版 / 自托管单用户实例**（`Auth.Enabled=false`）。
> 厂商托管的团队版（`Auth.Enabled=true`）**完全不启用**本遥测——见 [启用范围](#启用范围)。
>
> 本说明对应 Epic #329 的 v2「最小集」方案：**只上报一条匿名「打开事件」**，
> 不上报任何用量聚合。文档字段与 relay 摄取契约（#364）**逐字一致**，
> 实现见 `relay/internal/api/telemetry_handler.go`、`server/internal/telemetry/reporter.go`。

## 为什么需要这条遥测

牛牛个人版默认本地优先、网络隔离（绑定 `127.0.0.1` + 关闭账号体系），
因此厂商无法知道「外部到底有多少人在用个人版」。为了能统计**活跃度、版本分布、
操作系统分布与留存**，个人版会在启动时向 relay 发送**一条匿名的「打开事件」**。

这是 **opt-out（默认开启、可一键关闭）** 的设计：透明地告诉你采集什么、不采集什么，
是 opt-out 成立的前提。关闭方式见 [如何关闭](#如何关闭)。

---

## 采集（最小集，仅 6 个字段）

每条「打开事件」**只包含**以下字段，别无其他：

| 字段 | 含义 | 来源 / 备注 |
|------|------|-------------|
| `install_id` | 匿名安装标识 | 等于 `cfg.Server.ID`，首次启动生成的随机 UUID，**非账号、非设备序列号** |
| `machine_fp_hash` | 机器指纹哈希（**必带**） | 原始机器指纹经固定 salt 做 **SHA-256 哈希**后的十六进制串，**不是原始硬件 ID**；仅用于去重「同一台机器重装」 |
| `version` | 应用版本号 | 构建期注入的 `api.Version` |
| `os` | 操作系统 | `runtime.GOOS`（如 `windows` / `darwin` / `linux`） |
| `arch` | CPU 架构 | `runtime.GOARCH`（如 `amd64` / `arm64`） |
| `opened_at` | 打开时间 | 客户端上报的打开时间戳，**RFC3339** 格式 |

> `received_at`（服务端收到时间）由 relay 在落库时**服务端自行打戳**，
> 不由客户端上传，也不属于上述上报白名单。

### 字段对照表（文档 ↔ #364 白名单 ↔ 代码）

下表用于评审核对「逐字一致、无遗漏 / 无多列」：

| 本文档字段 | #364 载荷契约 | 代码结构体字段（`personalOpenEvent` / `openEvent`） | 落库列（`telemetry_personal_events`） |
|------------|---------------|------------------------------------------------------|----------------------------------------|
| `install_id`       | `install_id`（必填）       | `InstallID`     `json:"install_id"`      | `install_id TEXT NOT NULL` |
| `machine_fp_hash`  | `machine_fp_hash`（必带）  | `MachineFPHash` `json:"machine_fp_hash"` | `machine_fp_hash TEXT NOT NULL` |
| `version`          | `version`                  | `Version`       `json:"version"`         | `version TEXT NOT NULL DEFAULT ''` |
| `os`               | `os`                       | `OS`            `json:"os"`              | `os TEXT NOT NULL DEFAULT ''` |
| `arch`             | `arch`                     | `Arch`          `json:"arch"`            | `arch TEXT NOT NULL DEFAULT ''` |
| `opened_at`        | `opened_at`（RFC3339）     | `OpenedAt`      `json:"opened_at"`       | `opened_at TIMESTAMPTZ NOT NULL` |
| —（服务端打戳）    | —                          | —                                        | `received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()` |

摄取端用 `DisallowUnknownFields` 严格解析：**任何未知字段、疑似 PII 字段、
任何用量数值字段都会被拒绝（400）**；缺 `install_id` / `machine_fp_hash` /
`opened_at` 也会被拒绝。

---

## 绝不采集

牛牛**永远不会**通过本遥测采集以下内容：

- **账号 / 邮箱等 PII**（个人版本就没有账号体系，这条 endpoint 完全匿名）。
- **token / 工作量 / 项目数等任何用量聚合**——不统计你用了多少 token、跑了多少
  workspace、建了多少 issue/project。
- **仓库路径与文件名**。
- **issue / 提示词 / 对话内容**。
- **token 明文**。
- **原始硬件 ID**（只发 salted 哈希 `machine_fp_hash`，不发原始指纹）。
- **精确 IP 关联画像**（relay 仅按 IP 做匿名限流，不做基于 IP 的画像关联）。

---

## 用途

采集到的最小字段**仅**用于以下统计，不作他用：

- **DAU / WAU / MAU**（按 `install_id` 在查询侧按天去重）；
- **版本分布**（`version`）；
- **操作系统分布**（`os` / `arch`）；
- **留存曲线**。

`machine_fp_hash` 仅在分析时用于把「同一台机器的多次重装」合并，避免重装被
重复计为新装机。

---

## 上报机制（技术细节）

- **触发**：应用 / server 启动后约 10 秒发一条「打开 ping」；运行中每 **24 小时**
  发一条保活心跳（字段相同），保证长开会话每天准确计一个活跃日。
- **Endpoint**：`POST https://niuniu-relay.niu6ai.com/api/telemetry/personal`。
- **匿名**：不带 JWT，跳过 Auth / IdentityResolver。
- **限流**：每个来源 IP **60 次 / 分钟**；请求体上限 **4 KiB**。
- **尽力而为**：发送失败会静默重试几次后放弃，**绝不阻塞、绝不在主流程噪声报错**。
- **落库**：原始事件落 relay 的 PostgreSQL 表 `telemetry_personal_events`。

---

## 保留期

- **原始事件：保留 90 天**，到期由清理任务删除（清理 job 见看板 #369）。
- **每日聚合：长期保留**（聚合后不含可定位到单次打开的原始明细）。

---

## 如何关闭

本遥测是 opt-out，可随时关闭，**两种入口任选其一即可**：

1. **设置 → 隐私 中的「匿名使用统计」开关**（推荐）。
   该开关读写 `/api/config` 的 `telemetry_enabled` 字段。reporter 每个心跳周期
   都会重新读取该值，因此**关闭后下一个周期即停止上报，无需重启**。
2. **直接改配置文件** `~/.niuniu/config.yaml`：

   ```yaml
   telemetry:
     enabled: false   # 对应 config.Telemetry.Enabled，默认 true（opt-out）
   ```

此外：

- **团队版根本不上报**：仅 `Auth.Enabled=false` 的实例才会启动 reporter，
  厂商托管的团队版（`Auth.Enabled=true`）**从不启动遥测**，无需关闭。

### 启用范围

| 实例形态 | `Auth.Enabled` | 是否上报 |
|----------|----------------|----------|
| 个人版 / 自托管单用户 | `false` | 是（受 `telemetry.enabled` 总开关控制，默认开） |
| 厂商托管团队版 | `true` | **否**（reporter 根本不启动） |

---

## 如何请求删除

由于本遥测**完全匿名**（无账号、无邮箱），唯一能定位你数据的标识是
你的 `install_id`。

- 你的 `install_id` 可在 **设置 → 隐私** 查看，或在 `~/.niuniu/config.yaml`
  的 `server.id` 字段找到。
- 如需删除与你 `install_id` 关联的原始事件，请携带该 `install_id`，通过官网
  <https://www.niu6ai.com> 或公开仓库
  <https://github.com/threeq/niuniu-public> 的 issue 提交删除请求。
- 即便不主动请求，原始事件也会在 **90 天**后按保留期自动清理。

---

## 实现参考（开发者）

| 关注点 | 位置 |
|--------|------|
| relay 匿名摄取 + 6 字段白名单 | `relay/internal/api/telemetry_handler.go` |
| relay 路由 + 限流（60/min per IP） | `relay/internal/api/router.go` |
| 原始事件表 + 索引 + 保留说明 | `relay/internal/store/migrations/011_telemetry_personal_events.up.sql` |
| 个人版 reporter（打开 ping + 24h 心跳 + salted 哈希） | `server/internal/telemetry/reporter.go` |
| 总开关配置 | `server/internal/config/config.go`（`TelemetryConfig.Enabled`） |
| `/api/config` 读写 `telemetry_enabled` | `server/internal/api/config.go`（#366） |

---

## 精简版（供个人版首启告知 / 官网链接）

> 以下为可挂到首启同意面（#367）与官网 / `niuniu-public` 的精简文案。

**牛牛个人版会采集什么？**

为了统计活跃用户数、版本与系统分布，牛牛个人版在每次启动时会发送**一条匿名的
「打开事件」**，只包含 6 个字段：

- 匿名安装 ID（`install_id`，随机 UUID，非账号）
- 机器指纹哈希（`machine_fp_hash`，salted 哈希，**非原始硬件 ID**，仅用于去重重装）
- 版本号（`version`）、操作系统（`os`）、CPU 架构（`arch`）、打开时间（`opened_at`）

**绝不采集**：账号 / 邮箱等个人信息、token / 工作量 / 项目数等任何用量、仓库路径与
文件名、issue / 提示词 / 对话内容、token 明文、原始硬件 ID、精确 IP 画像。

**保留期**：原始事件保留 90 天，聚合统计长期保留。

**如何关闭**：到 **设置 → 隐私** 关闭「匿名使用统计」开关，下一个周期即停止上报；
也可在 `~/.niuniu/config.yaml` 设 `telemetry.enabled: false`。团队版从不上报。

**删除数据**：携带你的 `install_id`（设置→隐私 / `config.yaml` 的 `server.id`）通过
官网 <https://www.niu6ai.com> 提交删除请求。

完整说明见本文档（`docs/telemetry-privacy.md`）。
