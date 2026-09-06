# 工程规范（Harness）功能评估：是否保留与演进方向

> 结论摘要：**保留，但必须先修 bug、再收敛形态。**
> 「感觉根本没用到」是准确的观察，而且原因不只是没人配 —— 存在一个 P0 缺陷让
> 所有列闸 / 底线闸在配置后**依然静默放行**。修复见
> `server/internal/service/gate_spec_executor.go`。
>
> **执行状态（2026-09-05 全部完成）**：P0 修复 + 死代码清理 + P1/P2/P3 已全部落地，
> 见文末「七、落地记录」。

日期：2026-09-05 ｜ 关联 issue：#693

---

## 一、这个功能现在是什么

「工程规范」= `harness_specs` 全局规则库 + checker 执行引擎 + 三条触发路径。

| 层 | 位置 | 说明 |
|----|------|------|
| 规则库 | `harness_specs`（全局单库，非项目级） | 10 条种子规范 |
| 类型化 checker | `internal/harness/checkers/` | `regex_match` / `cmd_exit` / `cmd_output_match` / `file_exists` / `ai_judge` |
| 分发 | `harness/runner.go` `CheckRunner.dispatch` | 先按 `Kind` 找 TypedChecker，回退到 legacy `category/name` |
| 列绑定 | `column_gate_specs`(`applicability`) | `if_routed` = 列出口闸；`always` = 项目底线闸 |
| 执行路径 | `service/floor_gate.go`（底线）<br>`service/exit_gate.go`（列出口）<br>`api/harness.go` PreCommitCheck（提交前）<br>`scheduler` `on_schedule` | 4 条 |
| 对 Agent 的暴露 | `harness/claudemd.go` `GenerateCLAUDEMDRules`<br>`ai_native_board_prompt.go` 菜单「工程规范[...]」<br>MCP `gate_run` / `gate_results` / `harness_pre_commit_check` | 写进 CLAUDE.md + MCP 工具 |

规模：约 **6900 行**（Go + React + 测试）。

---

## 二、「没用到」的证据（本地库实测）

```
harness_specs         10 行  →  enabled=1 的：0 条
harness_checks         0 行  →  历史上从未产生过任何一次检查记录
column_gate_specs      2 行  →  applicability 全是 if_routed，没有一条 always
```

三点结论：

1. **10 条规范全部 `enabled=0`。** 种子数据里 9 条故意 disabled（保守默认），
   唯一默认启用的 `conventional-commits` 也被手动关掉了（`updated_at` 2026-05-28）。
2. **`harness_checks` 零行** —— 这是最硬的证据：不只是「没配」，是**这套引擎
   从部署至今一次都没有真正跑出过结果**。
3. **没有任何 `always` 绑定** → `floor_gate` 的 `listFloorSpecs` 恒返回空 →
   每次完成都走「无底线闸，直接 finalize」分支。看板菜单里那句
   「【底线工程规范，无论怎么走，完成前必须全绿】」**从未渲染过**。

所以当前唯一实际生效的部分，是 `GenerateCLAUDEMDRules` 往 CLAUDE.md 写一段
markdown —— 而它只渲染 enabled 的规范，enabled 为 0 时返回空字符串。
**即这个功能目前的实际运行时行为是：完全无行为。**

---

## 三、为什么不能只当成「没人配」——一个 P0 缺陷

关键问题：**假如现在有人去 UI 里认真配一条并绑成底线，它能拦住东西吗？不能。**

`gate_spec_executor.go` 的 `ExecuteSpec` 是**全部三条列闸路径**（底线闸、列出口闸）
的唯一执行入口。它原本手写转换 store 行 → `harness.Spec`，只拷了 legacy 子集：

```go
spec := harness.Spec{
    ID: raw.ID, Scope: "global", Category: raw.Category, Name: raw.Name,
    Enabled: raw.Enabled != 0, Severity: raw.Severity, Config: raw.Config,
}   // ← Kind / Command / Pattern / Target / Timeout / threshold / judge_* 全部丢失
```

`Kind` 丢失 → `dispatch` 走不到 TypedChecker → 回退 legacy registry。而 legacy
checker 从 **`Config` JSON** 读命令，UI 写入时 `Config` 恒为 `"{}"`（见
`harness-settings.tsx` 的 payload：只写类型化列，从不写 `config`）。两者叠加：

| 场景 | 实际结果 |
|------|----------|
| UI 配 `workflow/command-exit-code`，命令 `exit 7` | legacy checker 从空 Config 读不到命令 → **`skip`** |
| UI 配 `quality/build-test-pass`（指定的唯一 severity=error 底线位） | 该 key **没有注册 legacy checker** → 无结果 → `len(results)==0` → **`return true`（放行）** |

第二行是致命的：`build-test-pass` 正是 `defaults.go` 注释里专门为「底线」补的那条
severity=error 规范。**它被设计成底线的锚点，而它恰好是最彻底失效的一条。**

已用测试实证（`gate_spec_executor_test.go`），对旧代码：

```
--- FAIL: TestExecuteSpecHonorsTypedKind
        verdict must come from running the typed command, not a fallback skip
--- FAIL: TestExecuteSpecFloorSpecCanActuallyBlock
        a configured floor spec must block on failure
```

### 修复

改为复用已有的 `storeSpecsToHarness`（`service/harness.go` 里那份**正确**的全字段
转换 —— PreCommitCheck 和 `RunForWorkspace` 走的就是它，所以那两条路径没这个 bug）。

同时修了一个**耦合缺陷**：只修 Kind 会引入误拦。原判定 `passed := r.Status == "pass"`
把 `skip` 也算失败；Kind 修好后，出厂默认那条空命令的 `build-test-pass` 会返回
`skip` → 被判失败 → **底线误拦所有完成**，与 `defaults.go` 承诺的「never
false-blocks until a user fills in their command」直接矛盾。故改为
`passed := r.Status != "fail"`（仅显式 fail 拦截）。

> 这也解释了为什么这个 bug 能存活这么久：**没有人启用过任何规范，所以缺陷从未被
> 触发。功能未被使用 ↔ 缺陷未被发现，互为因果。**

---

## 四、要不要保留

### 保留的理由

1. **它是「自动托管」闭环里唯一的客观事实来源。** 其余判定（`goal_condition`、
   review 列、`[AUTOHOST_DONE]`）**全部由 LLM 自己判断自己**。Agent 说「做完了」
   与「真的做完了」之间，目前没有任何非 LLM 的校验。`build-test-pass` 这类
   命令闸是唯一能给出「编译过/测试过」这种不可争辩信号的机制。自动化程度越高，
   这个锚点越不可替代 —— 删掉它，托管链路就变成纯自证。
2. **`floor_gate.go` 周边已经建成了相当扎实的配套**，且这些是真正难写的部分：
   多 repo 逐 worktree 执行、`code_probe_only` + 产出探测（文档类 issue 不跑
   build 闸）、失败后回退到上一个 gate-pass checkpoint 再让 agent 自修、
   `floor_retry_count` 有界重试、崩溃恢复 `RecoverFloorGates`、SSE 进度。
   删掉等于丢弃这部分设计沉淀，而它们**不依赖**规则库有多丰富。
3. **成本已沉没，边际维护成本低。** 引擎无状态、无外部依赖、测试覆盖尚可
   （每个 checker 都有 `_test.go`）。它不拖慢任何东西 —— 因为它根本没在跑。

### 该砍的部分（真·死代码）

保留 ≠ 全留。以下已确认无调用方：

| 对象 | 状态 |
|------|------|
| `harness/prompt.go` `BuildHarnessPrompt`（123 行） | **零调用**。产出的 prompt 教 agent 用 `phase_current()` / `phase_complete()` / `phase_advance()` —— 这些 MCP 工具**已随 workflow 退役被删除**，代码里只剩 `MoveSourceMCPAdvance` 一个残留常量。即：这个函数会生成一段指导 agent 调用不存在工具的说明书。 |
| `harness/claudemd.go` 中 `InjectIntoCLAUDEMD` / `InjectIntoAgentInstructions` / `HasHarnessSection` / `RemoveFromCLAUDEMD` / `RemoveFromAgentInstructions` | 全部**零生产调用**（`HARNESS:START` 注入机制已被 `generateWorkspaceAgentInstructions` 直接拼接取代）。`injectSection`/`removeSection` 仍被 BOARD 段复用，需保留。 |
| `HarnessService.GeneratePrompt` | 自身注释标 Deprecated，返回 `"Goal: "+goal`。注意 `api/prompt_gen.go` 调的是**另一个同名方法**（不同 service），不冲突。 |
| legacy `Checker` 接口 + 8 条 `cr.Register(...)` 双注册 | 全部 10 条种子规范都有合法 `Kind`。legacy registry 的唯一作用是在 `Kind` 丢失时提供一条**静默错误**的回退路径 —— 也就是上面那个 P0 的放大器。类型化迁移已完成（2026-05-20），双轨该收了。 |
| `RunGateCheck` handler / MCP `gate_run` | 名字是「run」，实际只 `ListHarnessChecksByWorkspace` 读历史，从不执行。Agent 调用它永远得到空数组，具有误导性。要么真执行，要么改名 `gate_results`。 |

粗估可删 **约 400–600 行**（实际执行后为 1398 行，含配套测试），且都是「存在即有害」的那类（教 agent 用死工具、
静默兜底掩盖 bug、名不副实的接口）。

---

## 五、演进方向

排序即建议优先级。

### P0 — 让它先真的能跑（本次已做）

修 `ExecuteSpec` 字段丢失 + `skip` 误判，补回归测试。**在此之前任何推广都是负价值**：
用户配了闸、以为有保护、实际静默放行，比没有这个功能更糟。

### P1 — 从「规则库」收敛成「一条底线」

现在的形态错了：给用户一个 10 条规范 × 5 种 kind × 4 种 trigger × 2 种
applicability 的配置矩阵，让他自己组合出「底线」。**这个抽象层级对单人本地
工作站过重**，也正是它 4 个月零使用的直接原因 —— 配置成本远高于感知收益。

建议收敛为**项目级一个字段**：

```
项目设置 → 完成前必须通过的命令： [ make test        ]
```

落地上不需要新表：写成 `quality/build-test-pass` 的 `command` + 自动绑成该项目
`完成` 列的 `always`。**引擎、执行、重试、checkpoint 回退全部复用现有代码**，
只是把入口从「规则库 CRUD」压成一个输入框。剩下的 kind / trigger / 多规范组合
降级为「高级」，或直接不在 UI 暴露。

> 依据：种子数据里 9/10 条默认 disabled，说明连设计者也不认为这些规范普遍适用。
> 普遍适用的只有一条 —— 「你的项目怎么算构建通过」。而这一条恰恰**没有**默认值，
> 因为它必须由用户填。那就只问这一句。

### P2 — 用真实信号取代 regex 类规范

`conventional-commits` / `branch-name` 这类 regex 闸，价值低且 agent 本来就基本
遵守（真要强制，git hook 比这套引擎轻得多）。反过来，`ai_judge` + `pre_commit`
才是这套架构里**别处替代不了**的能力：在提交前用一次 LLM 判断
「这个改动是否真的实现了 issue 描述的东西」。这比任何 regex 都贴近用户真正关心的
问题。已有 `PreCommitCheck`（1MB body cap、成本考虑都做了）+ MCP
`harness_pre_commit_check`，缺的是一条**默认可用**的 issue-符合度 judge 规范。

### P3 — 可见性

`harness_checks` 零行的另一面是：**跑了也没人看得见**。列闸结果目前只进
issue timeline 文本和一条 SSE。至少让 issue 卡片上的 `gate_blocked` chip 能点开
看到「哪条规范、失败输出是什么」。没有可见性，用户不会信任它，也就不会启用它 ——
这是个自我强化的死循环。

---

## 六、本次改动

| 文件 | 改动 |
|------|------|
| `server/internal/service/gate_spec_executor.go` | 改用 `storeSpecsToHarness` 全字段转换（修 Kind 丢失）；`passed` 判定改为仅 `fail` 拦截（修由此引入的 skip 误拦） |
| `server/internal/service/gate_spec_executor_test.go` | 新增：类型化 kind 生效、底线规范可真实拦截、未配置规范不误拦。三条均已验证对修复前代码会 FAIL |

---

## 七、落地记录（P0 + 死代码清理 + P1/P2/P3 全部完成）

### P0 · 列闸静默放行

| 文件 | 改动 |
|------|------|
| `service/gate_spec_executor.go` | 改用 `storeSpecsToHarness` 全字段转换（修 Kind 丢失）；`passed` 判定改为仅 `fail`/`error` 拦截（修由此引入的 skip 误拦） |
| `service/gate_spec_executor_test.go` | 3 条回归测试，均已验证对修复前代码会 FAIL |

### 死代码清理（删除 1398 行；含 P1/P2/P3 新增后全仓净减 730 行）

| 对象 | 处理 |
|------|------|
| `harness/prompt.go`（123 行）+ `prompt_template.tmpl` | 删除。生成的说明书教 agent 调用 `phase_current()`/`phase_advance()` —— 这些 MCP 工具已随 workflow 退役被删除 |
| `claudemd.go` 的 5 个 HARNESS 注入函数 | 删除，替换为单个 `RemoveLegacyHarnessSection`，并在 board 注入时调用 —— 老版本写下的残留 HARNESS 段会被主动清掉，而不是永远留在 CLAUDE.md 里指挥 agent |
| legacy `Checker` 接口 + 8 条双注册 + 8 个 checker 文件 | 删除。`kind` NOT NULL + 有默认值 + Create/Update 校验 + 迁移已回填，回退路径的唯一作用就是把真实误配变成静默通过（即 P0 的放大器） |
| `HarnessService.GeneratePrompt` | 删除（自身注释已标 Deprecated；`api/prompt_gen.go` 调的是另一个同名方法，不受影响） |
| `RunGateCheck` / MCP `gate_run` | **改为真执行**（原本只读 `harness_checks` 历史，agent 调用永远得到空数组 = 与"全部通过"无法区分）。现调用 `RunForWorkspace` 后回读，并同步修正了 MCP 工具描述 |

配套：`CheckRunner.dispatch` 对未知 Kind 由 `skip`（静默）改为 `error`（响亮），
`HasBlockingFailure` 同时拦截 `fail` 与 `error` —— 一条无法执行的 error 级规范，
不构成"规范已满足"的证据。

### P1 · 收敛为项目级单字段底线

新增 `projects.floor_command` / `floor_timeout_sec`（空 = 不设底线，升级不会突然开始拦人）。
UI 就是项目设置→看板下的一个输入框：**「完成前必须通过的命令」**。

落地上**没有新建表、没有新引擎**：底线命令被包装成一条 `specID=0` 的 `floorSpec`，
走既有的 `runFloorCheck` 分派，因此多 worktree 执行、`code_probe_only` 产出探测、
失败回退 checkpoint、有界重试、崩溃恢复、SSE 进度**全部原样复用**。
`harness_specs` 规则库保留为「高级」路径（想要多条件的用户仍可用 `always` 绑定），
两者是并集而非替代。

底线命令同时写入看板菜单的「底线」行，agent 因此知道它的存在。

> 为什么不做成 `harness_specs` 里的一行：那张表是 `UNIQUE(category, name)` 的
> **全局**库，无法为每个项目存不同的命令。

### P2 · issue 符合度 judge

新增 `issue_conformance` target（仅 `ai_judge` 可用）：把 issue 的标题+描述与
staged diff **拼在一起**送给 judge，问"这次改动是否真的实现了需求"。这是这套架构
里 regex 和 git hook 都替代不了的能力。

出厂 `enabled=false`（需 `ANTHROPIC_API_KEY` 且每次提交产生费用），
`severity=warning`（LLM 意见应该浮现，不该硬拦提交）。
无 issue 文本时**跳过而非硬判**，避免在真空里烧一次付费调用。

### P3 · 可见性

`GateDonePayload` 新增 `failures[]`（含 name / reason / **output**）：
失败输出此前只进 slog，用户只能看到 `spec#16(exit_nonzero)`。
现在 SSE 事件携带输出、前端弹出带失败详情的 toast、issue timeline 也追加输出摘要
（SSE 断流后的持久线索）。

### 顺带修掉的一个 SQLite 迁移地雷

`dropProjectsNameUniqueSQLite` 用 `strings.Contains(ddl, "UNIQUE")` 判断是否需要
重建 `projects` 表，而 `sqlite_master.sql` **原样保留注释**。我给 `floor_command`
写的注释里出现了这个词，直接触发了一次**破坏性重建** —— 该重建只恢复最初 8 列，
静默丢掉 `color` / `memory_sweep_cron` / `floor_command` 等所有后加列，
表现为很远处的 `no such column: color`。

已修根因：新增 `stripSQLComments`，匹配前先剥离注释；并补 5 条测试
（`migrate_projects_unique_comment_test.go`）覆盖"注释里的词不触发重建"
与"真约束仍被正确移除"。

### 验证

- `go build ./internal/...`、`go vet ./internal/... ./cmd/...` 全绿
- `internal/harness`、`internal/harness/checkers`、`internal/store`、`internal/event`、
  `internal/api`、`internal/service`（137s 全量）全绿
- `make schema-diff`：SQLite / PostgreSQL 无表级漂移
- 前端 `pnpm build`（含 `tsc -b`）通过；`pnpm lint` 仍为 32 项既有问题，无一在本次文件中
- 前端测试：`workspace-sidebar.test.tsx` 在全量并发下有 1~2 条超时 flake，
  **修改前的 HEAD 同样失败**、单独跑该文件 11 条全过，与本次改动无关


### P4 · 补做：底线的可发现性

用户第一反馈是「我看 UI 界面没有变化啊」。核对下来功能确实在跑，
但**入口藏错了地方**：底线编辑器在 项目 → 设置 → 看板，而顶栏「工程规范」页
只列全局规范库（默认全空、默认不启用），于是用户最自然会去看的那一页，
恰恰对"我的项目到底有没有被拦"这个唯一有意义的问题只字不提。

补齐的是这条链路：

| 文件 | 改动 |
|------|------|
| `service/project_floor.go` | 新增 `ListFloors(ids)` —— 一次性读多个项目的底线（`ProjectFloorSummary` 内嵌 `ProjectFloor`，JSON 形状与单项目接口一致，只多一个 `project_id`） |
| `api/project_floor.go` | `GET /harness/project-floors?ids=1,2,3`，逐 id 过 `CanAccessProject`：无权的 id 直接跳过而不是整个请求失败，越权看不到、有权的照常看 |
| `server/router.go` | 路由挂在 `/harness/` 而非 `/projects/floors` —— gin 路由树里静态段不能和同位置的 `:id` 做兄弟，同文件既有注释已踩过这个坑 |
| `pages/settings/project-floor-overview.tsx` | 新增。「工程规范」页顶部列出每个活跃项目的底线状态（已设/未设 + 命令原文 + `x/y 已配置`），每行直达该项目的编辑器 |
| `router.tsx` / `project-layout.tsx` / `project-detail-page.tsx` / `project/settings-tab.tsx` | 打通深链 `?tab=settings&section=board`。tab 与子分区状态原本是两层组件内的局部 `useState`，外部无法定位；现在 `validateSearch` 里两个键**都是可选**的，既有指向 `/projects/$id` 的链接不受影响 |

顺序上把这一段放在全局规范库**之前**：这一页要先回答"我的项目现在被什么拦着"，
再谈那个默认空着的规则库。
