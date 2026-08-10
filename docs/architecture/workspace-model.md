# Workspace 数据模型与执行约束

定义 workspace 与 issue / project / repository / schedule 之间的关系，以及
所有"在 workspace 中执行"的子系统（agent、harness、scheduler、code review）
必须遵守的解析路径。

> **何时读这篇**：在你要做以下任何事之前
> - 引入新的"按 project 范围筛选"特性（per-project gate spec、per-project
>   notification 等）
> - 引入新的"按时间触发"特性（定时跑 harness、定时跑 agent 任务）
> - 在 service / api 层手写 `workspace.issue_id` / `issue.column_id` /
>   `column.project_id` 的 JOIN
> - 设计任何"workspace 关联多个 X"的特性 — 几乎一定与本文档冲突

---

## 核心规则

### 规则 1：一个 workspace 恰好关联一个 issue 和一个 project

```
workspace  1 ─── 1  issue  N ─── 1  column  N ─── 1  project
```

| 关系 | 基数 | 实现 |
|------|------|------|
| workspace → issue | 1 : 1 | `workspaces.issue_id` 非空时唯一指向 |
| issue → column | N : 1 | `issues.column_id` |
| column → project | N : 1 | `columns.project_id` |

**派生结论**：每个 workspace（在 `issue_id` 非空时）有且只有一个 project。
拿到 workspace 就能确定地反查 project，无歧义。

**禁止扩展**：

- ❌ 不允许 `workspaces` 表加 `project_id` 直接列。所有 project lookup 必须
  通过 issue → column → project 的链路。给了直接列就会和 issue 的归属出现
  drift，几乎一定不一致。
- ❌ 不允许一个 workspace 关联多个 issue（如 "处理多个相关 issue 的批量
  workspace"）。如有需求，开新 workspace。
- ❌ 不允许 workspace 直接挂在 project 上（如 "项目级临时 workspace"）。
  workspace 永远从 issue 长出来。

### 规则 2：定时任务挂在 workspace 上，执行在 workspace 里

```
workspace_schedule  N ─── 1  workspace
```

| 字段 | 含义 |
|------|------|
| `workspace_schedules.workspace_id` | 调度归属 |
| `workspace_schedules.cron_expr` / `run_at` | 何时触发 |
| 触发动作 | 总在 `workspace_id` 所指 workspace 的工作目录里运行 |

**派生结论**：

- 不存在"全局调度任务"。所有调度都通过某个 workspace。
- 调度产生的 `schedule_runs` 行带 `workspace_id`，结果也算在该 workspace 名下。
- 任何"按时间触发的 harness 检查 / agent 调用"都不需要独立的 cron 设施
  ——挂到 `workspace_schedules` 上，按现有调度机制走。

**禁止扩展**：

- ❌ 不允许在 `harness_specs` 上加 `cron_expr` 列实现"per-spec 定时跑"。
  spec 是配置，触发是调度的事。
- ❌ 不允许 scheduler 模块跑"全局 background sweep"去查所有
  `trigger_on='on_schedule'` 的 spec。所有触发都从某个 `workspace_schedule`
  出发，针对该 workspace 跑。

### 规则 3：无仓库（纯文件目录）workspace 模式

办公 / 非代码任务不需要 git worktree。`no_repo` 模式让 workspace 退化为
**一个按 owner 隔离的普通目录**：不挂任何 repo、不创建 worktree。

| 维度 | no_repo workspace |
|------|-------------------|
| 目录 | `OwnerRef.WorkspacePath` 解析出的普通目录（与普通 workspace 同构） |
| worktree 行 | **零行** —— `no_repo` workspace 的定义就是"`worktrees` 表里没有它的行" |
| issue 绑定 | 不变：仍遵守规则 1 的 1:1（或临时 workspace 的 `issue_id` 为空） |
| schedule | 不变：仍可挂 `workspace_schedules`（规则 2） |
| 触发方式 | API `POST /api/workspaces` 带 `no_repo:true` 且 `repos` 为空 |

**实现要点**：

- `no_repo` **不落库为独立列**。它在语义上完全等价于"该 workspace 的
  worktree 计数为 0"，再加一列只会和 worktree 行漂移。`CreateWorkspaceInput.NoRepo`
  / `CreateWorkspaceRequest.no_repo` 只是**创建时的意图标志**：用于跳过
  worktree provisioning、校验 `repos` 必须为空、并写入纯目录版的根 CLAUDE.md。
- API 契约是 either/or：`no_repo=true` ⟺ `repos` 为空；`no_repo=false` ⟺
  至少一个 repo。空 `repos` 不再被当作意外。
- 下游（archive / delete / sidebar / harness diff / code review）天然兼容
  空 worktree 集，无需特判。

**禁止扩展**：

- ❌ 不允许给 `workspaces` 加 `no_repo` 列。判定一律走"是否有 worktree 行"。
- ❌ 不允许 no_repo workspace 事后再挂 repo —— 需要 repo 就开普通 workspace。

---

## workspace → project 查询链（规范实现）

写在这里供未来 Phase C 直接照抄。约束：先看 `workspaces.issue_id` 是否非空；
为空（临时 workspace）时不存在 project，跳过 project-scoped 处理。

### Go service 层

```go
// ProjectForWorkspace returns the project ID associated with this workspace
// via issue -> column -> project. Returns (0, nil) when the workspace has
// no issue bound (temporary workspaces).
func (s *WorkspaceService) ProjectForWorkspace(ctx context.Context, workspaceID int64) (int64, error) {
    var projectID int64
    err := s.db.QueryRowContext(ctx, `
        SELECT c.project_id
        FROM workspaces w
        JOIN issues  i ON w.issue_id  = i.id
        JOIN columns c ON i.column_id = c.id
        WHERE w.id = ?
    `, workspaceID).Scan(&projectID)
    if errors.Is(err, sql.ErrNoRows) {
        return 0, nil // workspace has no issue, or issue has no column
    }
    return projectID, err
}
```

### 调用者

- harness pre_commit 处理器：用此函数解析 project，再调
  `ResolveForProject(ctx, &projectID)` 拿到 global + project 合并的 spec 集。
- 通知 / activity feed 的 project 维度聚合：同上。
- 任何写 `workspace.path` 的代码：**不要**借此函数推路径，path 由 OwnerRef
  解析。

---

## on_schedule 实现规范（Phase C 落地路径）

trigger_on='on_schedule' 的 harness spec 不需要 cron 字段。落地步骤：

1. **schedule 配置侧**：让现有 `workspace_schedules` 行可选地携带 "fire
   harness pre-flight" 标志（schema 加 bool 列或 `actions` JSON 字段）。
2. **触发侧**：`scheduler.trigger()` 在执行调度动作前，先调 harness 服务对
   该 workspace 跑所有 `trigger_on='on_schedule' AND enabled=1` 的 spec
   （global + 该 workspace 关联 project 的 project-scoped spec 合并集）。
3. **阻塞策略**：error-severity 的 fail 跳过本次调度执行并标记
   `schedule_runs.status='blocked'`；warning / info 不阻塞，只记录。
4. **结果归集**：结果写入 `harness_checks` 带 `phase_name='on_schedule'`，
   绑 `workspace_id`。

不需要的：

- ❌ 独立的 cron 解析器
- ❌ `harness_specs.cron_expr` 列
- ❌ scheduler 模块对 harness 包的反向感知 — 让 harness 服务 export 一个
  `RunForWorkspace(ctx, workspaceID, triggerOn)` 方法，scheduler 调用它。

---

## project-scoped pre_commit 实现规范（Phase C 落地路径）

agent 调 `harness_pre_commit_check` 工具 → niuniu-server 收到 workspace_id
→ 用规则 1 的 JOIN 解析 project_id → `ResolveForProject(ctx, &projectID)` →
合并 global + project specs 跑 pre_commit gate。

为空的 issue_id（临时 workspace）不报错，等价于"只有 global specs 生效"。

`service/harness.go` 的 `validateTriggerScope` 在 Phase C 删除
`scope='project' AND trigger_on='pre_commit'` 的拒绝分支；同时保留
`trigger_on='on_schedule'` 的拒绝直到 schedule 侧落地。

---

## 历史决策

- 2026-05-21 — 规则 1/2 写入架构规范，由 ai_judge + pre_commit 引发。原因：
  Phase B 实现 pre_commit 时一度以为 workspace 可能挂多个 issue / project，
  因此在 service 层强制 `scope='project' + trigger='pre_commit'` 报错。澄清后
  关系已确定为 1:1，限制可在 Phase C 解除。
