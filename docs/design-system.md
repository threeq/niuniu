# Niuniu 设计系统规范

> 本文档是 niuniu 前端的**强制规范**。所有 SPA 代码（`server/web/`、`relay/web/`、`mobile/`）的视觉与交互必须遵循。
> PR review 不通过此规范的，必须修复后才能合并。

**最后更新**: 2026-05-05
**适用范围**: 所有前端工程师、所有 Claude 会话、所有 UI 改动
**Owner**: 设计 + 前端架构

---

## 0. 怎么用这份文档

| 你是谁 | 怎么用 |
|---|---|
| 写新功能 | 先读 §1 原则 + §2 token 表，挑现有 token 用；缺 token 走 §11 流程申请 |
| Code review | 用 §10 反模式清单检查 PR |
| 改既有页面 | 不破坏 §1 原则的前提下迭代；不要在组件内硬编码十六进制 |
| Claude 会话 | 任何 UI 改动前必须 grep 此文件中的 token，禁止造新色 |

**唯一裁决线**：组件内**禁止硬编码十六进制色值**（除外部用户输入如 label.color）；间距、字号、圆角、阴影必须走 token。

---

## 1. 设计原则

1. **Token 优先**：所有视觉值（色、字号、间距、圆角、阴影、动效）通过 CSS variable 暴露，组件层只引用 token，不引用具体数值。
2. **Light/Dark 平价**：每个 token 必须同时定义 `:root` 与 `.dark`。新增 token 时缺一不可。
3. **暖中性 + 冷品牌**：UI 主色调走暖灰（`hsl(36 18% 98%)`-`hsl(30 5% 11%)`），品牌色走冷蓝点缀。避免冷白冷灰造成"医院/CRM 感"。
4. **离散语义色**：success / warning / info / destructive 仅承载语义，不当装饰用。优先级、状态色与语义色独立 token 命名空间，避免混用（如 P0 紧急 ≠ destructive）。
5. **节制装饰**：不用渐变文字、不用 dark glow、不用 glassmorphism（除非品牌明确需要）。一次设计只允许一个视觉重点。
6. **键盘可达**：所有交互组件必须 `:focus-visible` 可见、可 Tab 到达、可 Enter / Space 触发。
7. **i18n 默认**：用户可见文案必须走 `react-i18next` 的 `t()`；禁止硬编码中英文。
8. **不增加请求**：UI 升级不触发额外接口调用；缺字段时走后端 schema 流程。

---

## 2. Token 总表（权威清单）

所有 token 定义在 `server/web/src/index.css` 的 `:root` 与 `.dark` 块。Tailwind 在 `server/web/tailwind.config.js` 的 `theme.extend.colors` 暴露为类名。

### 2.1 中性色（暖灰基底）

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--warm-canvas` | `36 18% 98%` `#FAFAF9` | `30 5% 7%` | 页面底色（替代纯白） |
| `--warm-surface` | `0 0% 100%` `#FFFFFF` | `30 4% 11%` | 卡片 / 抬升表面 |
| `--warm-muted` | `40 14% 95%` `#F5F4F1` | `30 4% 13%` | 次级表面（列底、tab 底） |
| `--warm-border` | `35 12% 89%` `#E8E6E1` | `30 4% 19%` | 边框（暖调，替代冷灰边框） |
| `--warm-text` | `30 5% 11%` `#1C1B1A` | `36 18% 96%` | 主文字（暖黑） |
| `--warm-text-muted` | `30 4% 41%` `#6B6862` | `30 4% 65%` | 次文字 |

Tailwind: `bg-warm-canvas` / `bg-warm-surface` / `bg-warm-muted` / `border-warm-border` / `text-warm-text` / `text-warm-text-muted`

> **保留兼容**：原有 `--background` / `--foreground` / `--card` / `--muted` / `--border` 等 shadcn 默认 token 不动，避免影响现有组件。新代码优先用 `warm-*`，逐步迁移。

### 2.2 品牌色（冷蓝，只作点缀）

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--brand` | `218 75% 46%` `#1E5FCC` | `218 75% 60%` | 主 CTA、当前选中、链接 |
| `--brand-foreground` | `0 0% 100%` | `222 47% 11%` | brand 之上的文字色 |
| `--brand-soft` | `218 75% 95%` | `218 50% 18%` | hover / 选中底色（轻量） |

Tailwind: `bg-brand` / `text-brand` / `bg-brand-soft`

**使用边界**：
- ✅ 主操作按钮（一个页面 ≤1 个）、当前 tab 下划线、当前选中项左侧色条、链接文字
- ❌ 装饰渐变、纯背景填充、随机点缀

### 2.3 语义色（离散，仅承载状态）

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--success` | `142 71% 45%` | `142 71% 55%` | 完成态、成功提示 |
| `--warning` | `38 92% 50%` | `38 92% 60%` | 警告、需注意 |
| `--info` | `221 83% 53%` | `221 83% 65%` | 信息（中性提示） |
| `--destructive` | `0 84.2% 60.2%` | `0 62.8% 30.6%` | 删除、错误 |

每个语义色配 `*-foreground` token（在该色之上的文字色）。

**使用边界**：
- ✅ Toast、表单错误、状态徽章、Done 列、删除按钮
- ❌ 当作装饰色（如"我希望按钮是绿的所以用 success"），优先级色，分类色

### 2.4 优先级色（4 级，独立命名空间）

| Token | Light | Dark | 含义 |
|---|---|---|---|
| `--prio-low` | `220 9% 65%` `#9CA3AF` | `220 9% 55%` | P3 低 — 中性灰，**禁止用绿色**（避免与"完成态"歧义） |
| `--prio-medium` | `40 88% 40%` `#CA8A04` | `40 88% 60%` | P2 中 — 黄褐 |
| `--prio-high` | `20 90% 49%` `#EA580C` | `20 90% 60%` | P1 高 — 橙 |
| `--prio-critical` | `0 72% 51%` `#DC2626` | `0 72% 60%` | P0 紧急 — 红 |

Tailwind: `bg-prio-low` / `text-prio-low` / 同样的 high / medium / critical

### 2.5 工作流状态色（kanban 列、workspace 状态）

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--col-backlog` | `35 8% 56%` | `35 8% 50%` | Backlog / 未开始 |
| `--col-spec` | `218 75% 46%` | `218 75% 60%` | Spec·Plan / 规划中（同 brand）|
| `--col-impl` | `30 82% 41%` | `30 82% 55%` | Implement / 实现中 |
| `--col-review` | `262 70% 56%` | `262 70% 65%` | Review / 评审中 |
| `--col-done` | `142 71% 38%` | `142 71% 50%` | Done / 已完成（同 success）|

### 2.5.1 Diff / 语法高亮色（行级 diff 查看器）

行级 diff 查看器（`changes-panel` 内嵌）的增删色与语法高亮色**必须**走以下 token，
**禁止**在组件里硬编码绿/红（`bg-green-50`、`text-red-500`、`#1a7f3d` 等）。

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--diff-add-bg` | `142 71% 95%` | `142 71% 15%` | 新增行背景（`bg-diff-add`）|
| `--diff-del-bg` | `0 84% 95%` | `0 62% 18%` | 删除行背景（`bg-diff-del`）|
| `--diff-add-fg` | `142 66% 30%` | `142 55% 62%` | 新增行色带/`+`号/行号（`text-diff-add-fg`、`border-diff-add-fg`）|
| `--diff-del-fg` | `0 66% 42%` | `0 75% 70%` | 删除行色带/`−`号/行号（`text-diff-del-fg`、`border-diff-del-fg`）|
| `--syntax-keyword` | `209 75% 42%` | `209 80% 68%` | 关键字（`text-syntax-keyword`）|
| `--syntax-string` | `142 55% 32%` | `142 50% 62%` | 字符串字面量（`text-syntax-string`）|
| `--syntax-comment` | `215 12% 52%` | `215 14% 55%` | 注释（`text-syntax-comment`）|
| `--syntax-number` | `28 78% 43%` | `28 80% 65%` | 数字字面量（`text-syntax-number`）|

文件状态徽标（M/A/D）复用语义色：M→`warning`、A→`success`、D→`destructive`。

### 2.5.2 图片预览透明棋盘（产物面板）

产物面板的图片预览（`file-preview` 的 `ImageFilePreview`）在图片后铺一层透明棋盘，
让**透明背景的 SVG/PNG**（如未兑底导出的 fireworks 技术图）在明暗两模下都可读。
经 `.preview-checkerboard` 类消费，**禁止**在组件里硬编码棋盘色。两 token 在 dark
下刻意保持偏亮（棋盘是中性「画布」，类比图片编辑器），以免深色内容沉进暗底。

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--preview-checker-base` | `0 0% 100%` | `220 13% 82%` | 棋盘底色（`.preview-checkerboard` 背景色）|
| `--preview-checker-cell` | `220 13% 90%` | `220 9% 72%` | 棋盘格色（`.preview-checkerboard` 渐变格）|

### 2.6 Surface 抬升

| Token | Light | Dark | 用途 |
|---|---|---|---|
| `--surface` | `0 0% 100%` | `222.2 84% 4.9%` | 默认表面 |
| `--surface-muted` | `210 40% 98%` | `217.2 32.6% 12%` | 下沉表面（嵌入区） |
| `--surface-raised` | `0 0% 100%` | `217.2 32.6% 15%` | 抬升表面（modal/popover） |

> 保留以兼容现有组件；新代码优先用 `warm-*` 体系。

### 2.7 字号 / 字重

| Token | px | 用途 |
|---|---|---|
| `text-[10px]` | 10 | 角标、计数（仅作标记用） |
| `text-[11px]` | 11 | 元数据（截止日期、checklist 进度） |
| `text-xs` | 12 | 标签 chip、辅助文字 |
| `text-sm` | 14 | 卡片标题、表单 label、表格内容 |
| `text-base` | 16 | 正文、对话框正文 |
| `text-lg` | 18 | section 标题 |
| `text-xl` | 20 | page subtitle |
| `text-2xl` | 24 | page title |

字重：仅使用 `font-normal (400)` / `font-medium (500)` / `font-semibold (600)`。**禁止 bold 700+**（除非超大字号）。

数字必须 `tabular-nums`（如计数、进度）。

### 2.8 间距（4px 网格）

仅使用 Tailwind 默认间距类，禁止 `[7px]` 这类任意值：

| 类 | px |
|---|---|
| `gap-0.5` `p-0.5` | 2 |
| `gap-1` `p-1` | 4 |
| `gap-1.5` `p-1.5` | 6 |
| `gap-2` `p-2` | 8 |
| `gap-3` `p-3` | 12 |
| `gap-4` `p-4` | 16 |
| `gap-6` `p-6` | 24 |
| `gap-8` `p-8` | 32 |

**单一组件内最多用 3 种间距值**。

### 2.9 圆角

| 类 | 值 | 用途 |
|---|---|---|
| `rounded` | 4px | tag / chip / 小按钮 |
| `rounded-md` | 6px | 输入框 / 普通按钮 |
| `rounded-lg` | 8px | 卡片 / dialog |
| `rounded-xl` | 12px | 大型 hero / 视觉强调容器（节制使用）|
| `rounded-full` | 9999 | avatar / pill / 圆形按钮 |

定义在 `--radius: 0.5rem`，shadcn 组件经此推导。

### 2.10 阴影 / 抬升（仅 3 级）

| 类 | 用途 |
|---|---|
| `shadow-sm` | 卡片默认 |
| `shadow-md` | 卡片 hover / 浮出 |
| `shadow-lg` | 拖拽 ghost / modal / popover |

**禁止 `shadow-xl` / `shadow-2xl` / `shadow-[...]` 自定义阴影**。如果你觉得需要更深的阴影，先思考是不是 contrast / 信息层级出了问题。

### 2.11 动效

| Token | 值 | 用途 |
|---|---|---|
| `duration-100` | 100ms | 即时反馈（hover）|
| `duration-150` | 150ms | 默认（大多数过渡）|
| `duration-200` | 200ms | 列展开 / 折叠、tab 切换 |
| `duration-300` | 300ms | modal / drawer 进出 |

**所有动画必须 `transition-` + 明确属性**（`transition-colors` / `transition-transform` / `transition-opacity`），**禁止 `transition-all`** 除非局部必要。

缓动：默认 `ease-in-out`。进入用 `ease-out`，退出用 `ease-in`，匀速用 `linear`（仅 loading）。

**Reduced motion**：所有非必要动画必须包裹在 `@media (prefers-reduced-motion: no-preference)`，关键过渡（如 modal 进出）可以保留但缩短至 ≤100ms。

---

## 3. 字体

```css
/* tailwind.config.js theme.extend.fontFamily 默认即可，不要换 */
sans:  Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif
mono:  ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace
```

中文 fallback 走系统字体栈（`PingFang SC` / `Microsoft YaHei` / `Noto Sans SC`），不引入自定义中文字体（包大小考虑）。

代码、commit hash、id、数字（计数 / 估时）使用 `font-mono` 或 `tabular-nums`。

---

## 4. 图标

**唯一图标库**：`lucide-react`。禁止：
- 引入第二套图标库（`react-icons` / `heroicons` / 等）
- 用 emoji 当 icon（除非是用户内容如 issue 标题中的 📅）
- 用 SVG 内联拼接图标

尺寸：
- 内联图标：`w-3 h-3` (12px) / `w-3.5 h-3.5` (14px) — 配 12-14px 文字
- 普通按钮图标：`w-4 h-4` (16px) — 配 14-16px 文字
- 大按钮 / 工具栏：`w-5 h-5` (20px)
- icon-only 触摸目标：≥40×40px（含 padding）

颜色：通过 `text-*` 继承（如 `text-warm-text-muted`），不在 SVG 内写 stroke 色。

---

## 5. 组件规范

### 5.1 按钮

四种变体，**一个页面同时只允许一个 primary**：

| 变体 | 用途 | className |
|---|---|---|
| **Primary** | 页面主操作（"提交"、"创建"）| shadcn `<Button>` 默认 |
| **Secondary** | 次要操作（"取消"、"返回"）| `<Button variant="secondary">` |
| **Ghost** | 工具栏、列表内联操作 | `<Button variant="ghost">` |
| **Destructive** | 删除、不可逆操作 | `<Button variant="destructive">` |

尺寸用 `size="sm" | "default" | "lg" | "icon"`。

**Don't**：
- 不要用 `<button className="bg-blue-500 ...">` 自造按钮，必须走 shadcn `<Button>`
- 不要给 secondary 按钮加品牌色

### 5.2 卡片

默认结构：

```tsx
<div className="bg-warm-surface border border-warm-border rounded-lg shadow-sm p-3
                hover:shadow-md hover:-translate-y-px transition-shadow duration-150">
  ...
</div>
```

**禁用**：纯白卡片放在纯白背景上（必须有 `border` 或 `shadow-sm` 至少一个边界）。

### 5.3 表单

- 所有表单项走 shadcn `<Input>` / `<Select>` / `<Textarea>` / `<Checkbox>`。
- Label 与 input 间距 `gap-1.5`，组与组间距 `gap-4`。
- 错误信息：在 input 下方红色 `text-xs text-destructive`。
- 禁用态：`opacity-50 cursor-not-allowed`，不要再额外改背景色。

### 5.4 表格

- 表头 `bg-warm-muted text-warm-text-muted text-xs font-medium`。
- 行高 ≥40px（手感空间）。
- 斑马纹**只在数据密集时使用**（>10 行）。
- 排序 / 过滤 icon 与表头同色，hover 加深。

### 5.5 Modal / Dialog

- 走 shadcn `<Dialog>`。
- 标题 `text-lg font-semibold`，关闭按钮在右上角 ghost。
- 主操作按钮放右下角，副操作（取消）紧挨其左。
- 移动端走 `<Drawer>`（底部抽屉）。

### 5.6 Empty State

- 必须使用 `components/shared/empty-state` 的 `<EmptyState>`，统一样式。
- 字段：`title` (必)、`description` (可选)、`icon` (可选)、`action` (可选)。
- **禁止重复 empty state**（同一屏 ≥3 个都显示完整 empty state）。

### 5.7 Loading State

- **首屏 / 大列表**：用 skeleton（如 `IssueCardSkeleton`）。
- **按钮内 loading**：spinner + 禁用。
- **页内局部刷新**：顶部 progress bar（不打断阅读）。
- **绝不**用整页 spinner 遮罩（破坏可视连续性）。

### 5.8 Toast / 通知

- 走 `sonner`。
- 成功：`toast.success(msg)` 默认 3s 自动关闭。
- 错误：`toast.error(msg)` 默认 5s，可手动关闭。
- 不要用 `toast()` 简单字符串，永远配语义类型。

### 5.9 Tab

- 顶部 tab：当前态下方 2px brand 色条 + `text-warm-text` 实色，非当前 `text-warm-text-muted`。
- 子 tab（嵌套）：用胶囊型（`rounded-md bg-warm-muted` 当前态）以区别于父级。
- **同一页面禁止 3 层 tab 嵌套**。

### 5.10 状态徽章 (Badge)

形式：圆角 chip + 色 + 文字。**色与文字双重编码**（无障碍）。

```tsx
// good
<span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5
                 text-xs bg-success/15 text-success border border-success/30">
  <Circle className="w-1.5 h-1.5 fill-current" />
  完成
</span>

// bad: 仅靠颜色传达状态
<div className="w-2 h-2 rounded-full bg-success" />
```

---

## 6. 布局与响应式

### 6.1 容器宽度

- 全宽页面：`w-full`
- 受控宽度内容（设置页等）：`max-w-3xl mx-auto`
- 列表 / 看板：`w-full overflow-x-auto`，子元素自适应

### 6.2 断点

走 Tailwind 默认（`sm 640` / `md 768` / `lg 1024` / `xl 1280` / `2xl 1536`）。**禁止自定义断点**。

桌面优先（`server/web/`）；移动优先逻辑放 `mobile/`（RN）。

### 6.3 滚动

- 自定义滚动条已在 `index.css` 定义（暖灰），不要二次自定义。
- 横向滚动（如 kanban）：用 `overflow-x-auto` + `scrollbar-thin`（如需）。

---

## 7. Dark Mode

- 由 `next-themes` 切换；CSS 走 `.dark` 选择器。
- **新增 token 必须同时给 `:root` 和 `.dark`**，缺一即审核拒绝。
- 暗色下检查项：
  - 文字 / 背景对比度 ≥ WCAG AA（4.5:1）
  - 不出现 `bg-white` 这种硬编码（必然在暗色下错）
  - 边框可见（不要 `border-white/5`）
  - 阴影减弱或换为边框区分层级（`shadow-*` 在深底上效果弱）

测试方法：开发完一个组件**必须切换 light/dark 各看一次**，不可省略。

---

## 8. 无障碍

### 8.1 键盘

- 所有可交互元素 Tab 可达
- `:focus-visible` 必须有清晰外环（`ring-2 ring-brand` 或 `ring-ring`）
- Modal / Dropdown 进入时焦点自动落到内部首个可交互元素，关闭时回到触发器

### 8.2 ARIA

- icon-only 按钮：必须有 `aria-label` 或包裹 `<TooltipProvider>`
- 状态变化（如 loading 完成、toast 出现）：用 `aria-live`
- 装饰图标加 `aria-hidden="true"`

### 8.3 对比度

- 文字 ≥ 4.5:1（小字 ≥ 7:1）
- 图标 ≥ 3:1
- 不能仅靠色彩传达信息（色盲友好）

### 8.4 触控目标

- 触摸友好：≥44×44px（移动端）
- 桌面：≥32×32px

---

## 9. i18n

- 所有用户可见文案走 `useTranslation()` 的 `t()`
- key 命名：`<page>.<section>.<key>`（如 `kanban.column.addIssue`）
- 同时维护 `zh` 与 `en` 两份 `locales/**/*.json`
- 数字 / 日期走 `Intl.NumberFormat` / `Intl.DateTimeFormat`，不要手拼字符串
- 复数走 `i18next` 的 `count` 参数

**禁止**：
- 在 JSX 中硬编码中文或英文 `{"添加"}`
- 拼接字符串造句（"Hello " + name + "!"），用 `t('greet', { name })`

---

## 10. 反模式（PR review checklist）

> 命中任何一条都需要修复。

### 10.1 色彩反模式
- [ ] 组件内出现 `#xxxxxx` 十六进制色（用户输入除外）
- [ ] Tailwind 任意值色：`bg-[#1234ab]` `text-[rgb(...)]`
- [ ] 用 `success` 绿表达"低优先级"
- [ ] 用 `destructive` 红表达"高优先级"
- [ ] 渐变文字 `bg-gradient-to-r ... bg-clip-text text-transparent`
- [ ] 大面积渐变背景（除非是品牌识别区）
- [ ] dark glow / neon 效果
- [ ] 一个页面 >1 个 primary CTA

### 10.2 排版反模式
- [ ] `font-bold`（700+）日常使用
- [ ] 多种字重混用（>3 种）
- [ ] 字号 `text-[13.5px]` 这种任意值
- [ ] 数字未用 `tabular-nums`（导致计数跳动）
- [ ] 中文使用 `italic`（中文不该斜体）

### 10.3 间距反模式
- [ ] 间距任意值：`p-[7px]` `gap-[13px]`
- [ ] 同组件内 >3 种间距尺度
- [ ] 间距与上下文不匹配（如 modal 内用 `p-1`）

### 10.4 组件反模式
- [ ] 自造按钮（不走 shadcn `<Button>`）
- [ ] 重复 empty state（同屏 ≥3 个）
- [ ] 整页 spinner 遮罩
- [ ] hero 大数字 + 大字网格（"1,234 项目 · 567 用户"那种 AI 生成感模板）
- [ ] glassmorphism（半透明 + 模糊）除非品牌需求
- [ ] tab 嵌套 ≥3 层

### 10.5 交互反模式
- [ ] `transition-all`（应明确属性）
- [ ] 动画 ≥500ms（无理由）
- [ ] 没有 `:focus-visible` 样式
- [ ] icon-only 按钮无 `aria-label` 或 tooltip
- [ ] modal 关闭后焦点丢失
- [ ] 不可逆操作（删除）无确认

### 10.6 国际化反模式
- [ ] 硬编码中英文
- [ ] 字符串拼接造句

### 10.7 暗色反模式
- [ ] 新增 token 仅有 `:root`，没有 `.dark`
- [ ] 用 `bg-white` / `bg-black` 硬编码
- [ ] dark mode 下未实测就 commit

---

## 11. 扩展流程：什么时候加新 token

**默认答案：不加。** 优先用现有 token 组合。

确实需要加的场景：
1. 引入新视觉语义（如新的工作流状态、新的图表色系）
2. 现有 token 无法表达需求且组合无效

加 token 流程：
1. 在 `docs/design-system.md` 的 §2 表格中补充行
2. 在 `server/web/src/index.css` 的 `:root` 与 `.dark` 各加一行
3. 在 `server/web/tailwind.config.js` 的 `theme.extend.colors` 暴露
4. PR 描述中说明"为什么现有 token 不够用"
5. 至少一位前端 reviewer + 一位设计 reviewer 通过

**禁止**：在组件内一次性硬编码，承诺"以后再抽 token"。这会导致永远抽不出来。

---

## 12. 工具与配套

- **Tailwind 4** — 主要 CSS 工具
- **shadcn/ui** — 组件原语（Button / Dialog / Input 等）
- **lucide-react** — 唯一图标库
- **sonner** — toast
- **react-i18next** — i18n
- **@dnd-kit** — 拖拽
- **react-resizable-panels** — 可拖拽面板（注意 import `Group` `Panel` `Separator`）
- **next-themes** — 主题切换

新增第三方 UI 库需要架构 review，原则上：先看 shadcn 是否能解决；不能解决再看是否能自己写；都不行再引入。

---

## 13. 已知特例与历史包袱

- `--background` / `--card` / `--muted` / `--border` 等 shadcn 默认 token 沿用。新代码优先 `warm-*`；现有代码可渐进迁移，不要一次性改完。
- `IssueCard.tsx` 的 `wsCardStyles` 直接写了 `bg-green-50` 这类 Tailwind palette 色，下一轮迁到 `--col-*` token。
- `priorityColors` 的优先级映射在 2026-05-05 修复（原映射 0=低 用绿色，与 Done 列误读，改为中性灰→黄→橙→红）。
- 表格组件目前散落在多个页面各自实现，待抽统一 `<DataTable>`（暂不在本规范强制范围）。

---

## 14. 修订日志

| 日期 | 变更 | 作者 |
|---|---|---|
| 2026-05-05 | 初版：暖中性 token、品牌色、优先级 4 级、状态色独立命名空间、反模式清单 | UI 优化任务 #152 |

后续修订请在此追加，并在 PR 标题用 `docs(design):` 前缀。
