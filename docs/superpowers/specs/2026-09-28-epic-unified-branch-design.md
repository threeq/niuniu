# Epic 统一分支方案：父工作空间检出 epic/<id> + 收尾集成下放工作树

日期：2026-09-28 ｜ 状态：已批准（2026-09-30 修订 §4：删除服务端合并流水线，集成收尾改由控制工作空间 Agent 在工作树内完成）

## 背景与根因

Epic 执行存在三类分支：`epic/<id>`（集成分支）、父（控制）工作空间分支 `ws-<id>/epic/<id>`、子任务工作空间分支 `ws-<子id>/epic/<id>`。痛点：

1. **AI 合并经常出错**：(a) 子任务基线在创建时切出、随 epic 推进而过期，冲突后靠子空间 AI 盲目解冲突；(b) merge-to-main 由控制工作空间 AI 执行且提示词允许「就地解决冲突」——最危险的合并交给了 AI。
2. **父工作空间看不到 epic 集成情况**：控制工作空间与 epic 分支是两条分支，子任务并入 epic 后父工作空间不实时可见。
3. 用户补充需求：(a) 支持「先建父再建子」与「直接建子时连带创建父工作空间+分支」；(b) 已创建工作空间的普通任务不能再变为子/父任务——UI 隐藏入口、API 校验。

已实测验证：git 分支是仓库全局 ref，从 worktree 内以任意分支为基线创建新 worktree 完全可行（git 2.54 实测）；niuniu 所有 worktree 均对主仓库创建、目录为兄弟关系，无嵌套。唯一硬约束「同一分支不能被两个 worktree 同时检出」天然满足（epic/<id> 只被父工作空间检出）。

## 设计

### 1. 分支统一（父工作空间直接检出 epic/<id>）

- `resolveEpicControlRepos`（epic_execution.go:579）返回的 Branch 直接为 `epic/<id>`（不再生成 `ws-<id>/` 前缀）；worktree 创建时**检出已存在的 epic 分支**（`git worktree add <path> epic/<id>`，无 -b）。`ensureEpicBranch` 保持先行。
- 子任务路径不变：仍 `ws-<子id>/epic/<id>`，从 `epic/<id>` 切出。
- `syncEpicWorkspaceLocked`（:963）：子任务并入后对父工作空间 ff 同步——统一后即「epic 分支自身前移」，工作树 ff-only 更新（脏文件拒同步留旧头，现有语义）。
- **存量兼容**：以 workspace 行的 Branch 前缀区分——`ws-` 开头走旧 sync/归档路径；`epic/<id>` 走新路径。归档清理（workspace.go）：`epic/` 分支走**安全删除**（`git branch -d`，仅当完全并入仓库 HEAD/main 时才删，未并入自动保留——中途归档不断流程，合并后归档自动清理）；`ws-` 旧分支维持强制删除。仅新建 epic 生效，存量不迁移。
- git 层新增：`WorktreeAddCheckoutExisting(repoPath, path, branch)`（检出已有分支）；`MergeAs` 冲突时解析并返回**冲突文件清单**（merge-tree 输出含 CONFLICT 行，现只返回报错文本）。

### 2. 场景补全

- **a) 先父后子**：现有路径已支持（epic 工作空间先建 → ensureEpicBranch → 子任务从 epic/<id> 切出），回归覆盖即可。
- **b) 直接建子连带建父**：
  - `createWorkspaceForIssue` 子任务路径：`ensureEpicBranch` 后若 `activeWorkspaceForIssue(epicID)` 无 → **递归调用 createWorkspaceForIssue(epicID) 连带创建父控制工作空间**，再建子。
  - **普通 HTTP 创建路径收口**：`Create`/`CreateForIssue`（workspace.go:797 / api CreateForIssue:828）对 epic-managed issue（epicIDForIssue 命中）委托到 createWorkspaceForIssue 语义——分支强制 epic 派生（忽略对话框手选分支），并执行 b 的连带逻辑。

### 3. 普通任务工作空间的父子护栏

- **判定**：issue 存在活跃工作空间（is_archived=0）。
- **服务端校验**（store 新查询 `HasActiveWorkspaceForIssue`，kanban 服务直查）：
  - `SetIssueExecFields`（kanban.go:321，含 EpicHierarchyControl 与 MCP update_issue）：给 issue 设 `parent_issue_id` 时——issue 自身有活跃工作空间 → 拒绝（不能变子）；新父有活跃工作空间 → 拒绝（普通任务不能升父收子）。
  - `CreateIssue` / `BatchCreateIssues`（带 parent_issue_id）：父 issue 有活跃工作空间 → 拒绝创建子任务。
  - 错误文案：「该任务已创建工作空间，不能变更父子关系」（变子）/「父任务已创建工作空间，不能再添加子任务」（收子）。
- **前端隐藏**：issue DTO 增加 `has_workspace`；`EpicHierarchyControl`（设置父任务）与 `issue-quick-create-dialog`（加子任务）在 `has_workspace` 时隐藏入口。

### 4. C：epic 完成后的集成提示词（2026-09-30 修订：合并下放工作树，服务端流水线删除）

> 修订背景：Wave1 落地后，epic 控制工作空间**直接检出 `epic/<id>`**——AI 就坐在集成分支上、对 epic 全程有上下文，服务端合并流水线（main→epic 预合并 + epic→main 快进）与「合并到主分支」按钮不再需要。

新流程：epic 变为 `done` 时（编排 Agent 在控制工作空间输出 [AUTOHOST_DONE] → 工作空间完成 → `terminateEpic(..., "done")`），服务端自动向**同一控制工作空间**发送一条集成提示词（`composeEpicIntegrationPrompt`），Agent 在自己的工作树内完成收尾：

1. `git merge main` 把 main 合入当前分支 epic/<id>——如遇冲突就在当前分支就地解决（AI 对 epic 全程有上下文）；
2. 完成后验证构建/测试；
3. 全部通过后，向用户报告并请求确认推送 origin——**未经用户确认不得推送**。

配套删除与保留：

- **原②（epic→main 快进）删除**：本地 main 不再被系统推进，其前进由用户手动/外部决定。
- **原③（父工作空间同步）删除**：合并在工作树内进行，天然同步。
- **「合并到主分支」按钮与 POST /issues/:id/merge-to-main 删除**：无手动触发；epic done 即自动发提示词。发送失败仅 Warn（编排已完成，收尾提示是尽力而为）。
- 服务端合并流水线（`RequestMergeToMain` / `mergeRepoToMain` / `EpicMergeConflictError` / `escalateEpicMergeSplit` 等）整体删除；**保留**子→epic 服务端合并 `mergeChildIntoEpic`、`syncEpicWorkspaceLocked`、git 层 `MergeConflictError`、父子护栏与统一分支代码。

## 明确不做

- 子任务基线刷新（B）不在本期；存量 epic 控制工作空间不迁移；不做定时轮询。

## 测试

- git 层：WorktreeAddCheckoutExisting 检出既有分支/重复检出报错；merge-tree 冲突文件清单解析。
- epic_execution：统一分支创建（控制 ws 行 Branch==epic/<id>）；场景 b 级联（父无 ws 时建子连带建父）；存量 ws- 前缀走旧路径；归档跳过 epic 分支。
- kanban 护栏：有 ws 的 issue 设父被拒、父有 ws 加子被拒、无 ws 正常通过。
- C 流程（2026-09-30 修订）：epic 编排工作空间完成 → done → 控制工作空间收到集成提示词（含 epic/<id>、git merge main、推送需用户确认）；failed 不发送；无活跃工作空间时静默跳过。
- 前端：has_workspace 隐藏入口；tsc + lint。

## 验收

1. 新建 epic：父工作空间 git 分支 == epic/<id>；子任务并入后父工作空间无需刷新即可见（ff 同步）。
2. 归档控制工作空间：未并入 main 的 epic 分支保留（`branch -d` 拒绝）；已并入 main 的 epic 分支被安全清理。
3. 无父工作空间时直接对子 issue 建工作空间 → 父控制工作空间与 epic 分支被连带创建。
4. 有工作空间的普通任务：UI 无「设置父任务/加子任务」入口；API 直调被 4xx 拒绝。
5. epic 收尾集成：epic 编排工作空间完成（exec_status=done）后，控制工作空间自动收到集成提示词（含 epic/<id>、`git merge main`、推送需用户确认）；failed 不发送；本地 main 的 ref 全程不被系统改动。
