# 资讯雷达使用说明

一个"资讯雷达" = 一个挂了「资讯雷达」场景的**周期托管任务**，用 imbot/收件箱推送。不新建任何后端子系统，全部由现有原语组合：托管任务（触发 + 隔离工作空间）+ 场景（取数/处理资源）+ imbot（推送渠道）+ blackboard（去重）。

## 建一个雷达

两种方式：

1. **对话式（推荐）**：在挂了「资讯雷达」场景的工作空间里用快捷操作 **「新建资讯雷达」**，按引导回答关注主题 / 频率 / 是否结合牛牛工作信息 / 推送渠道 / 每轮上限，agent 会代你调用 `create_managed_task` 建好。
2. **手动**：直接调用 `create_managed_task(cron_expr, description)`，`description` 里写清关注主题、是否结合工作信息、推送渠道、每轮上限，并让它遵循 info-radar skill 的管线。

## 运行与管理

- 到点由托管任务在其**专属工作空间**自跑一轮：组装语境（关注主题 + 可选牛牛工作信息）→ `WebSearch`/`WebFetch` 取外部资讯 → 逐条判相关（达不到门槛丢弃）→ blackboard 键 `radar:pushed` 去重 → 命中项经 imbot 频道 / `inbox_send` 推送、**无命中则静默** → 回写已推 hash。
- 在 **`/schedules`** 页可见、可暂停、可删。
- 底线：**精准 > 数量**，宁可当轮不推，也不发"无更新"噪音。
- 不同关注方向 = 再建一个托管任务（独立工作空间天然隔离，互不干扰）。

## 数据源

- **牛牛工作信息**（Phase 1）：agent 自带 niuniu MCP，指令开关即可结合近期 issue/工作空间。
- **浏览器记录**（Phase 2，路子 A，已实现）：info-radar 场景启用 `browser-history` 工具组后，agent 可调用 `read_browser_history(since_days, domains, limit)` **本地直读**浏览器历史（Chrome/Edge/Brave/Firefox），skill 会把它压成"最近在看 X/Y 方向"的摘要纳入判相关。
  - 隐私：该工具**默认关闭**，仅本场景显式启用（`enable_tool_groups: [browser-history]`）；只读历史（URL/标题/时间），不碰 cookie/密码；原始记录**本地不出设备、不落库、不进推送正文**；默认仅取近 7 天、可带域名过滤。
- **后续扩展点**：新增 RSS 等数据源可挂到场景 `mcp:` 列表，skill 管线不动。

## 相关文档

- 设计稿：`docs/superpowers/specs/2026-07-16-proactive-info-radar-design.md`
- 实现计划：`docs/superpowers/plans/2026-07-16-proactive-info-radar-phase1.md`
