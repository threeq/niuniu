# 移动端功能差距分析与 UI/UX 改进方案

Date: 2026-04-17
Phase: 0 — 系统设计
Pipeline Run: #4

---

## 1. 项目概述

**目标：** 分析 Niuniu 移动端（React Native + Expo Router）现有功能，与桌面端（React 19 Web SPA）对比，识别功能差距，制定 UI/UX 改进方案。

**移动端定位（来自 2026-04-06 设计规范）：** 轻量操作为主 — 创建/编辑 issue、管理看板、与 agent 交互、查看通知。不做深度开发（代码编辑留在桌面端）。

---

## 2. 功能差距分析

### 2.1 总览矩阵

| 功能域 | Web 桌面端 | 移动端现状 | 差距级别 |
|--------|-----------|-----------|---------|
| **认证 & 服务器管理** | JWT 登录 + 刷新 | 完整（多服务器、安全存储、自动刷新） | ✅ 无差距 |
| **项目列表** | 网格布局 + 侧边栏 | 卡片列表 + 搜索 + 下拉刷新 | ✅ 基本对齐 |
| **看板 / Issue 管理** | 拖拽看板 + 侧面板详情 | 分段切换看板 + 底部 Sheet 详情 | ⚠️ 部分差距 |
| **Issue CRUD** | 完整 CRUD + 清单 + 评论 + 时间线 | 查看 + 清单 + 评论（创建/编辑为占位符） | 🔴 关键差距 |
| **工作空间列表** | 完整列表 + 状态分组 + 侧边栏 | 空壳占位页 | 🔴 关键差距 |
| **工作空间详情（Chat）** | 8 面板 IDE（Chat/终端/文件/变更/Issue/任务/预览/团队） | 空壳占位页 | 🔴 关键差距 |
| **Agent 交互** | SSE 流式 Chat + 工具调用可视化 + 费用追踪 | 仅 SSE 通知（无 Chat 界面） | 🔴 关键差距 |
| **仓库管理** | 文件浏览 + 分支 + 提交历史 + Worktree | 空壳占位页 | 🔴 关键差距 |
| **通知 / Inbox** | SSE + WebSocket 实时推送 | SSE Agent 通知 + 未读追踪 | ⚠️ 部分差距 |
| **定时任务** | 完整 CRUD + 历史 + 手动触发 | 未实现 | 🔴 关键差距 |
| **设置** | 5 个标签页（通知/环境/Harness/Agent/团队） | 基础（账户/服务器/主题/版本） | ⚠️ 部分差距 |
| **Harness 流水线** | 模板/规格/运行 CRUD + 执行 | 未实现 | ➖ 移动端不需要 |
| **代码审查** | Diff + 行内评论 + 发送给 Agent | 未实现 | ➖ 移动端不需要 |
| **终端模拟** | xterm.js PTY | 未实现 | ➖ 移动端不需要 |
| **文件编辑** | 文件树 + 内容查看 | 未实现 | ➖ 移动端不需要 |
| **团队协作** | Worker 卡片 + 黑板 + 管道进度 | 未实现 | ⚠️ 可选增强 |

### 2.2 关键差距详细分析

#### 🔴 差距 1：工作空间功能（最高优先级）

**Web 端功能：**
- 工作空间列表：按状态（Attention/Needs Review/Running/Others）分组，侧边栏筛选
- 工作空间详情：8 面板 IDE 布局
- Agent Chat：SSE 流式渲染、工具调用折叠、费用追踪、会话历史
- 控制操作：启动/停止 Agent、发送消息、斜杠命令、快捷操作
- 队列管理：任务排队、拖拽排序
- 任务追踪：按阶段分组（spec/plan/arch/impl/test）

**移动端现状：** 两个空壳页面（`workspace/index.tsx` 和 `workspace/[id].tsx`），仅显示"to be implemented"。

**影响：** 用户无法在移动端与 Agent 交互，这是产品核心功能。

#### 🔴 差距 2：Issue 创建/编辑

**Web 端功能：** 完整的 CreateIssueDialog（标题、描述、优先级、负责人、日期、标签）

**移动端现状：** FAB 按钮触发 `Alert.alert("功能即将上线")` 占位符。IssueDetailSheet 已实现查看/清单/评论，但创建和编辑未完成。

#### 🔴 差距 3：仓库管理

**Web 端功能：** 4 个标签页（文件/分支/Worktree/设置）、文件浏览、提交历史

**移动端现状：** 空壳占位页。

**移动端适配方案：** 提供只读浏览（文件树、分支列表、提交历史），不做文件编辑。

#### ⚠️ 差距 4：通知增强

**Web 端功能：** SSE 事件流 + WebSocket 通知中心 + Toast + 声音

**移动端现状：** SSE Agent 通知已实现，但缺少：
- 系统级推送通知（expo-notifications 已安装但未使用）
- 通知分类筛选（全部/未读/@我）
- 通知点击跳转到相关页面

---

## 3. UI/UX 改进方案

### 3.1 导航架构调整

**现状：** 3 Tab（Dashboard / Projects / Inbox）
**目标：** 4 Tab（项目 / 工作空间 / 仓库 / 通知），与 2026-04-06 设计规范对齐

```
底部 Tab Bar
├── 📋 项目（Projects）
│   ├── 项目列表
│   └── 项目详情（看板/列表/统计）
├── 🧊 工作空间（Workspaces）
│   ├── 工作空间列表（状态筛选）
│   └── 工作空间详情（Chat + 控制栏）
├── 📁 仓库（Repositories）
│   ├── 仓库列表
│   └── 仓库详情（文件/分支/提交/Worktree）
└── 🔔 通知（Notifications）
    └── 通知列表（全部/未读/@我）
```

**理由：** Dashboard 聚合页信息密度低，不如直接给工作空间和仓库独立 Tab 更高效。用户最常用的操作路径是「查看工作空间状态 → 与 Agent 对话」和「管理 Issue → 创建工作空间」。

### 3.2 实现优先级

#### P0 — 核心功能（本次迭代）

| # | 功能 | 涉及文件 | 说明 |
|---|------|---------|------|
| 1 | **工作空间列表** | `app/(tabs)/workspaces/index.tsx` | 卡片列表、状态筛选、搜索、Agent 状态徽章 |
| 2 | **工作空间详情 — Chat** | `app/workspace/[id].tsx` | SSE 流式 Chat、消息渲染、输入栏 |
| 3 | **工作空间控制栏** | `src/components/WorkspaceControls.tsx` | 启动/停止 Agent、任务/队列/定时/Issue/变更/设置 Chips |
| 4 | **Issue 创建** | `src/components/CreateIssueSheet.tsx` | 底部 Sheet 表单 |
| 5 | **Issue 编辑** | `src/components/IssueDetailSheet.tsx`（扩展） | 标题/描述/属性可编辑 |
| 6 | **项目创建/编辑** | `src/components/CreateProjectSheet.tsx` | 底部 Sheet 表单 |

#### P1 — 重要增强

| # | 功能 | 说明 |
|---|------|------|
| 7 | **仓库列表** | 卡片列表、搜索 |
| 8 | **仓库详情** | 文件树（只读）、分支列表、提交历史、Worktree |
| 9 | **通知增强** | 分类筛选、推送通知、点击跳转 |
| 10 | **工作空间任务面板** | Bottom Sheet overlay，任务进度 |
| 11 | **工作空间队列** | 队列列表、删除项 |

#### P2 — 可选增强

| # | 功能 | 说明 |
|---|------|------|
| 12 | **工作空间变更查看** | 简化 diff 显示（文件列表 + 增删行数） |
| 13 | **定时任务管理** | 查看/启禁用/手动触发（创建跳转桌面端） |
| 14 | **团队面板** | Worker 状态、黑板条目（只读） |
| 15 | **设置增强** | 环境变量预设、通知声音 |

### 3.3 工作空间详情页设计（核心）

这是移动端最重要的新增页面，参考 2026-04-06 设计规范：

```
┌─────────────────────────────┐
│ ← 工作空间名称     状态 ⋯  │  头部
│ 项目名 · changes 3 · ↑2    │  元信息
├─────────────────────────────┤
│ ▶启动  ✅任务2/5  📥队列(3) │  控制栏（横向滚动 Chips）
│ ⏰定时(1)  🔗Issue  📝变更  │
├─────────────────────────────┤
│                             │
│  Chat 消息列表              │  Chat 区域
│  （SSE 流式渲染）           │  - 用户消息右对齐蓝色气泡
│                             │  - Assistant 消息左对齐深色卡片
│  ┌─────────────────┐        │  - 工具调用折叠卡片
│  │ 🔧 Read file.ts │        │  - 打字动画指示器
│  │ ▸ 展开详情      │        │
│  └─────────────────┘        │
│                             │
│  ··· Agent 正在输入          │
│                             │
├─────────────────────────────┤
│ /  │ 输入消息...    │ ⚡ ↑ │  输入栏
└─────────────────────────────┘
```

**关键交互：**
- SSE 连接：`/ws/sse?workspaceId={id}&token={token}`
- 发送消息：`POST /api/workspaces/:id/agent/send`
- 启动 Agent：`POST /api/workspaces/:id/agent/start`
- 停止 Agent：`POST /api/workspaces/:id/agent/stop`
- 历史消息：`GET /api/workspaces/:id/messages`

**新增 Hooks：**
- `useWorkspaceSSE(workspaceId)` — 工作空间 SSE 流式消息
- `useWorkspaceMessages(workspaceId)` — 历史消息加载

**新增 Store：**
- `workspaceChatStore` — 按工作空间管理消息列表、输入状态、Agent 状态

### 3.4 Issue CRUD 设计

**创建 Issue Sheet：**
```
┌─────────────────────────────┐
│ ─── 拖拽指示条 ───          │
│                             │
│ 新建 Issue              取消│
├─────────────────────────────┤
│ 标题 *                      │
│ ┌─────────────────────────┐ │
│ │                         │ │
│ └─────────────────────────┘ │
│                             │
│ 描述                        │
│ ┌─────────────────────────┐ │
│ │                         │ │
│ │                         │ │
│ └─────────────────────────┘ │
│                             │
│ 列        │ 选择列...    ▾ │
│ 优先级    │ P3 (Medium)  ▾ │
│ 负责人    │ 选择...      ▾ │
│                             │
│ ┌─────────────────────────┐ │
│ │       创建 Issue        │ │
│ └─────────────────────────┘ │
└─────────────────────────────┘
```

**API：** `POST /api/projects/:projectId/issues`

### 3.5 仓库详情页设计

```
┌─────────────────────────────┐
│ ← 仓库名称                  │
│ [文件] [分支] [提交] [WT]   │  分段控件
├─────────────────────────────┤
│                             │
│ 文件标签页：                │
│  📁 src/                    │
│  📁 components/             │
│  📄 package.json            │
│  📄 tsconfig.json           │
│                             │
│ 分支标签页：                │
│  ● main（当前）             │
│  ○ feature/xxx              │
│  ○ fix/yyy                  │
│                             │
│ 提交标签页：                │
│  [hash] 提交消息 — 作者 时间│
│  [hash] 提交消息 — 作者 时间│
│                             │
└─────────────────────────────┘
```

**API：**
- 文件树：`GET /api/repositories/:id/tree`
- 分支：`GET /api/repositories/:id/branches`
- 提交：`GET /api/repositories/:id/commits`
- Worktree：`GET /api/repositories/:id/worktrees`

---

## 4. 现有 UI/UX 改进点

除功能差距外，现有已实现页面的 UI/UX 改进：

### 4.1 项目详情看板

| 问题 | 改进 |
|------|------|
| 分段控件只显示列名，不显示 Issue 数量 | 显示 `列名 (N)` |
| 列表视图无折叠功能 | 完成列默认折叠，可展开 |
| 统计视图数据有限 | 添加优先级分布、生命周期分布 |

### 4.2 Issue 详情 Sheet

| 问题 | 改进 |
|------|------|
| 标题不可编辑 | 长按进入编辑模式 |
| 描述不可编辑 | 添加编辑按钮 |
| 无"移至列"操作 | 添加底部操作栏"移至列"按钮 |
| 清单项无排序 | 支持拖拽排序（可选） |

### 4.3 Inbox / 通知

| 问题 | 改进 |
|------|------|
| 仅显示 Agent 状态变化 | 增加 Issue 变更、评论通知 |
| 无分类筛选 | 添加 全部/未读/@我 筛选 |
| 通知无跳转 | 点击跳转到相关工作空间或 Issue |
| 无系统推送 | 接入 expo-notifications，后台推送 |

### 4.4 全局改进

| 问题 | 改进 |
|------|------|
| 无离线缓存 | 关键数据（项目/Issue）缓存到 AsyncStorage |
| 无下拉刷新反馈 | 统一 RefreshControl 样式 |
| 无 Haptic 反馈 | 关键操作添加触觉反馈 |
| 无骨架屏统一 | 统一骨架屏组件风格 |

---

## 5. 技术方案

### 5.1 新增文件清单

```
mobile/
├── app/
│   ├── (tabs)/
│   │   ├── _layout.tsx                    # 修改：4 Tab 导航
│   │   ├── workspaces/
│   │   │   └── index.tsx                  # 新增：工作空间列表
│   │   ├── repositories/
│   │   │   └── index.tsx                  # 新增：仓库列表
│   │   └── notifications/
│   │       └── index.tsx                  # 修改：通知增强
│   ├── workspace/
│   │   └── [id].tsx                       # 重写：工作空间详情
│   └── repository/
│       └── [id].tsx                       # 重写：仓库详情
├── src/
│   ├── components/
│   │   ├── CreateIssueSheet.tsx           # 新增
│   │   ├── CreateProjectSheet.tsx         # 新增
│   │   ├── WorkspaceCard.tsx              # 新增
│   │   ├── WorkspaceControls.tsx          # 新增
│   │   ├── ChatMessage.tsx               # 新增
│   │   ├── ChatInput.tsx                 # 新增
│   │   ├── ToolCallCard.tsx              # 新增
│   │   ├── RepositoryCard.tsx            # 新增
│   │   ├── FileTreeItem.tsx              # 新增
│   │   ├── BranchItem.tsx                # 新增
│   │   ├── CommitItem.tsx                # 新增
│   │   ├── TaskSheet.tsx                 # 新增
│   │   ├── QueueSheet.tsx                # 新增
│   │   └── SlashCommandPopup.tsx         # 新增
│   ├── hooks/
│   │   ├── useWorkspaceSSE.ts            # 新增
│   │   └── useWorkspaceMessages.ts       # 新增
│   └── stores/
│       └── workspaceChatStore.ts         # 新增
```

### 5.2 API 端点使用

工作空间相关（已有后端支持）：
- `GET /api/workspaces` — 列表
- `GET /api/workspaces/:id` — 详情
- `POST /api/workspaces/:id/agent/start` — 启动
- `POST /api/workspaces/:id/agent/stop` — 停止
- `POST /api/workspaces/:id/agent/send` — 发送消息
- `GET /api/workspaces/:id/agent/status` — Agent 状态
- `GET /api/workspaces/:id/messages` — 历史消息
- `GET /api/workspaces/:id/costs` — 费用
- `GET /api/workspaces/:id/queue` — 队列
- `GET /api/workspaces/:id/workspace-tasks` — 任务
- `/ws/sse?workspaceId={id}` — SSE 流

仓库相关（已有后端支持）：
- `GET /api/repositories` — 列表
- `GET /api/repositories/:id` — 详情
- `GET /api/repositories/:id/tree` — 文件树
- `GET /api/repositories/:id/branches` — 分支
- `GET /api/repositories/:id/commits` — 提交
- `GET /api/repositories/:id/worktrees` — Worktree

Issue CRUD（已有后端支持）：
- `POST /api/projects/:id/issues` — 创建
- `PUT /api/issues/:id` — 更新
- `DELETE /api/issues/:id` — 删除

### 5.3 SSE 流式渲染方案

```
                    ┌──────────────┐
 EventSource ──────►│ useWorkspace │
 /ws/sse            │    SSE       │
                    └──────┬───────┘
                           │ dispatch events
                    ┌──────▼───────┐
                    │ workspaceChat│
                    │    Store     │
                    └──────┬───────┘
                           │ state update
                    ┌──────▼───────┐
                    │  Chat 消息   │
                    │   FlatList   │
                    └──────────────┘
```

事件类型映射：
| SSE Event | Store Action | UI Effect |
|-----------|-------------|-----------|
| `text` | 追加到最新 assistant 消息 | 增量文本渲染 |
| `tool_use` | 插入工具调用消息 | 显示折叠工具卡片 |
| `tool_result` | 更新工具调用状态 | 更新卡片图标/状态 |
| `thinking` | 插入思考消息 | 显示思考动画 |
| `done` | 标记消息完成 | 隐藏打字指示器 |
| `idle` | 更新 Agent 状态 | 更新控制栏 |
| `task_update` | 更新任务列表 | 刷新任务面板 |
| `error` | 显示错误 | Toast 提示 |

---

## 6. 实施路线图

### 阶段 1：P0 核心功能
1. 导航架构调整（3 Tab → 4 Tab）
2. 工作空间列表页
3. 工作空间详情 — Chat 核心（SSE + 消息渲染 + 输入）
4. 工作空间控制栏（启动/停止）
5. Issue 创建 Sheet
6. Issue 编辑模式
7. 项目创建/编辑 Sheet

### 阶段 2：P1 重要增强
8. 仓库列表页
9. 仓库详情页（文件/分支/提交/Worktree）
10. 通知增强（分类筛选 + 推送 + 跳转）
11. 工作空间任务面板
12. 工作空间队列管理

### 阶段 3：P2 可选增强
13. 变更查看（简化 diff）
14. 定时任务管理
15. 团队面板
16. 设置增强

---

## 7. 不做范围（明确排除）

以下功能保留在桌面端，移动端不实现：

- 终端模拟（xterm.js PTY）
- 代码编辑
- 完整 Diff 视图（行级对比）
- 代码审查（行内评论）
- Harness 模板/规格编辑
- Agent Registry 管理
- 复杂拖拽（看板列拖拽、队列排序）
- 文件上传/附件
- 工作空间创建（触发路径复杂，保留桌面端）
