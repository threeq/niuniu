# Provider Group：spawn 时重新解析覆盖 workspace_env + 每空间状态跟进 + chat 状态栏显示

日期：2026-09-21 ｜ 状态：已批准（方案 A）

## 需求

1. 工作空间配置 provider group 后，**每次启动新的 agent 进程都重新解析最新可用 provider**，其结果覆盖工作空间自身配置的环境变量。
2. 可用 provider 的选取算法必须**稳定**且符合**组内指定排序**（group_position）。
3. **每个工作空间独立跟进**自己当前使用的 provider 状态。
4. UI：chat flow 状态栏显示当前使用的 provider 名称（否则用户不知道当前空间用的是哪个 provider）。

## 已确认的三个决策

| 决策点 | 结论 |
|---|---|
| 覆盖语义 | 仅覆盖 provider 展开产出的键（ANTHROPIC_*、OPENAI_* 等）；workspace_env 其他键不受影响；未绑定 provider 的空间行为不变 |
| cooldown 范围 | 保持 provider 全局 cooldown（配额是账号级的）；每空间持久记录「当前实际使用的 provider」 |
| UI 刷新 | 实时——spawn 与 429 切换重启后都推送 |

## 现状（探索结论）

- 两条 spawn 路径每次启动都已现场调 `sceneenv.Resolve` → `ActiveProvider` → `GroupProvider`，直接读库、无缓存——「每次重新解析」已天然满足。
- `GroupProvider`（`server/internal/sceneenv/provider_fallback.go:131`）已是稳定算法：过滤同组 + enabled + 非 cooldown + 有对应协议 base_url，取 `group_position` 最小（平局 id 最小）——「稳定 + 组内排序」已满足。
- **差距 1**：`Resolve` 合并顺序为 provider 展开 < scene providers < scene presets < workspace_env（`sceneenv.go:190-193`），工作空间自身 env 会遮蔽最新解析的 provider。
- **差距 2**：chat 路径仅在内存记 `s.activeProviderID`（429 归因用），不持久化；PTY 路径完全无记录。
- **差距 3**：UI 无 provider 展示。

## 设计

### 1. 覆盖语义 — `sceneenv.Resolve`

新合并顺序（低→高）：scene providers → scene presets → workspace_env → **绑定 provider 展开（最后铺）**。

- `ActiveProvider` 返回 ok 时缓存 `ExpandProvider` 结果（preserveRef=true），在 workspace_env 合并完成后再铺一遍：provider 产出的键以最新解析为准；workspace_env 其余键（GIT_AUTHOR_* 等）原样保留。
- 未绑定 provider/group 时 `boundEnv` 为空，输出与现在**逐字节一致**（零回归安全阀）。
- `${ACCOUNT:<name>}` 引用机制不变：展开保持引用，返回前统一 `SubstituteAccounts` 替换。
- 绑定 provider 展开因此也高于 scene 层——工作空间级显式绑定是最具体的用户意图，语义为「绑定 provider 的解析结果对该空间最权威」。

### 2. 每空间状态跟进 — `workspaces.active_env_provider_id`

- `workspaces` 新列 `active_env_provider_id INTEGER`（NULL=无）。按仓库红线：双 schema（`schema.sql` + `schema_postgres.sql`）同步、走 `addColumnIfNotExists` 迁移、**不在 schema 文件里建索引**、`make sqlc` 重新生成。
- 写入点：
  - PTY：`service/agent.go` `Start`（agent.go:203 Resolve 之后）——`ActiveProvider` ok 则写 provider id，否则清 NULL。
  - chat：`agentproxy.ensureProcess`（proxy.go:2212 Resolve 之后）同样写入；429 触发的 `restartForProviderFallback` 重启走 ensureProcess，**天然刷新**。内存 `s.activeProviderID` 保留用于 429 归因（stale-line guard 依赖）。
- **解绑/改绑不清除该列**：语义是「当前进程实际在用的 provider」，进程还活着就该显示；下次 spawn 自然刷新。
- 每个工作空间一行独立更新，互不影响；cooldown 保持全局不变。

### 3. UI — chat 状态栏 provider pill

- 后端：workspace DTO 增加 `active_env_provider_name`（service 层按 active id 补查 `env_providers.name`，id 为 NULL 或查不到时为空）。
- WS 推送：notify hub（TopicWorkspace）新 action `provider_changed`，payload 含 workspace id 与 provider 名称；发送点 = ensureProcess 与 agent.Start 持久化之后。前端收到后 invalidate workspace query；页面加载/刷新时 GET 兜底。
- 前端：`chat-input.tsx:354` 状态栏右侧组、usage pill 之前插入 provider pill（muted 风格，如 `⌁ 智谱-主`）；未绑定时不渲染；tooltip 走 `t('panels.chatInput.activeProvider')`；三语言 locale 同步；遵守 `docs/design-system.md`（bg-muted 圆角、lucide 图标、无 hex、无任意色值/间距）。

### 4. 错误处理

- `ActiveProvider` 查询失败 → 现状降级（无 provider env），active 列清空，UI pill 消失。
- notify 推送失败仅记日志（参照 `mcp_error` 广播先例），不影响 spawn。
- 正在跑的进程不受改绑影响（env 已随进程注入），下次 spawn 生效。

### 5. 测试

| 层 | 用例 |
|---|---|
| sceneenv | 组绑定 + workspace_env 冲突键 → provider 键胜、其余 workspace_env 键保留；未绑定 → 输出与旧逻辑逐字节一致；`${ACCOUNT:}` 引用在最终覆盖后仍正确替换 |
| service | agent.Start 写 active 列（有/无 provider 两态） |
| agentproxy | ensureProcess 持久化 active；429 fallback 重启后 active 变为 fallback 成员（扩展现有 `provider_rate_limit_test.go`） |
| 工程 | `make schema-diff` 通过；`go vet` / `go test` 全绿；前端 tsc + lint |

### 6. 明确不做

- cooldown 改按工作空间（已确认保持全局）。
- 主动健康探测（可用性 = enabled + cooldown 判定，沿用现状）。
- PTY 终端视图的 provider 显示（只做 chat flow 状态栏）。

## 验收标准

1. 工作空间绑定组 + workspace_env 写入同名 `ANTHROPIC_BASE_URL` → spawn 后进程实际使用组内排序最前的可用成员（而非 workspace_env 里的值）。
2. 未绑定 provider 的工作空间，Resolve 输出与改动前逐字节一致。
3. 组内首成员进入 cooldown → 新 spawn 自动取下一成员（现有行为不回归）；429 自动切换重启后状态栏名称实时更新。
4. 每个工作空间状态栏显示各自当前 provider 名称，刷新页面后仍可见（持久化）。
