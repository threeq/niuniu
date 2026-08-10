# 工作空间「内容查看区」重构设计

日期：2026-07-03　范围：`server/web/src/pages/workspaces/*`（前端，无后端改动）

## 背景与目标

工作空间 IDE 当前存在几处交互不一致：

1. 点击文件树里的文件 → 弹出全屏 **modal**（`FilePreviewModal`）遮住整个界面。
2. 「画布」(canvas / Excalidraw) 与「图表」(drawio) 是两个独立的顶栏按钮 + 独立右侧
   panel，只能「新建空白图 → 发给 Agent」，与「查看已有文件」的心智模型割裂。
3. 「变更」panel 点击某个 diff → 进入 focus 模式（chat 收成竖排 rail + panel 内
   master-detail），与文件查看是两套完全不同的展示方式。

目标：引入统一的**中间内容查看区（Content Viewer）**，位于 chat 与右侧 panel 之间，
默认隐藏。文件 / git-diff / 画布 / 图表都在这一个区域内展示；打开时自动隐藏左侧
sidebar、chat 收窄（近手机宽度），获得沉浸式阅读空间。移除「画布 / 图表」独立按钮，
改由「点击对应类型文件」进入内容区。

## 交互规格

- **默认隐藏**：内容区仅在 `contentViewer[workspaceId]` 有目标时出现。
- **打开来源**：
  - 文件树点击文件 → `file`（按扩展名分派：`.excalidraw`→`canvas`，`.drawio`→`drawio`，其余→通用 `FilePreview`）。
  - 变更列表点击 diff → `diff`（repo + path，行级 diff + 评论 queue/send 保留）。
- **打开副作用**：自动隐藏 sidebar（记住原状态）、chat 收窄。关闭内容区时恢复 sidebar。
- **关闭**：内容区自带关闭按钮；同一目标再次点击亦切换关闭。
- **文件头部动作**：保留原 modal 的「提交为产物」「下载」「关闭」。
- **画布 / 图表**：只读预览（Excalidraw `viewModeEnabled`，drawio 载入 XML 只读），不含「发给 Agent」。

## 布局

`workspace-page.tsx` 中间横向 `Group` 组合：

```
[Chat]  ├ ([ContentViewer] 当有目标) ┤  [RightPanelStack 当有开启的 side panel]
```

- 无内容区、无 side panel：chat 独占（现状）。
- 内容区开启：chat 收窄（defaultSize 小），内容区占主体；若同时有 side panel，再切一块给它。

移除原 focus 模式（`ChatRail` / `isChangesFocused`）——diff 改由内容区承载。

## 数据 / Store（`workspace-panel-store.ts`）

- `PanelId` 移除 `'canvas' | 'drawio'`（不再是可开关 panel）。
- 新增：
  ```ts
  type ContentViewerTarget =
    | { kind: 'file'; path: string; title?: string }
    | { kind: 'diff'; repo: string; path: string }
    | { kind: 'canvas'; path: string }
    | { kind: 'drawio'; path: string };
  contentViewer: Record<string, ContentViewerTarget | null>;
  openContentViewer(workspaceId, target); // 自动隐藏 sidebar
  closeContentViewer(workspaceId);         // 恢复 sidebar
  ```
- 移除 focus 相关字段（`isChangesFocused` 等）与 `changesSelectedFile`（选中高亮改由
  `contentViewer` 目标派生）；保留 `changesFileSearch`。
- persist `version` 升到 3，migrate 清洗掉持久化 `openPanels` 里的 `canvas/drawio`。

## 组件

- 新增 `panels/content-viewer-panel.tsx`：头部（路径 + 动作 + 关闭）+ 按 kind 分派 body。
  - `file` → `FilePreview` + 提交产物/下载。
  - `diff` → 复用 `changes-panel` 导出的 `DiffPane` + `useWorkspaceComments` 评论管线，自带 unified/split 切换。
  - `canvas` / `drawio` → 拉取文件原文，喂给扩展后的只读编辑器。
- `file-tree-panel.tsx`：删除 `FilePreviewModal`，点击 → `openContentViewer`。
- `changes-panel.tsx`：点击 diff → `openContentViewer({kind:'diff'})`；面板恒为列表，去掉 focus/master-detail；选中高亮取自 `contentViewer`；导出 `DiffPane`。
- `workspace-toolbar.tsx`：移除 canvas / drawio 两个按钮。
- `components/canvas/excalidraw-canvas.tsx`、`drawio-canvas.tsx`：新增可选 `initialContent`/`viewOnly`（`registerExporter` 变可选），支持只读加载已有内容。

## i18n

复用既有键（`filePreview.download/submitArtifact/close`、`panels.changes.viewUnified/viewSplit`、
`canvas.title/loading`、`drawio.title/loading`），无需新增语言键。原 focus 与 canvas/drawio
toolbar 键成为未使用键，暂保留（无害）。

## 验证

- `pnpm tsc -b` 通过；`pnpm test:run` 相关测试通过（`file-preview.*`、`artifact-preview-panel`、`workspace-sidebar` 等）。
- 手工核对：文件/画布/图表/diff 四类内容均在中间区展示；打开自动收起 sidebar 并在关闭后恢复；无残留 modal / focus rail。
