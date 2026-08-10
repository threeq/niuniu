# macOS AI 直达窗口嵌入 + 提速 设计

> 面向 issue「mac版本链接团队版本后…」延伸需求①：**Mac 上「AI 直达」打开很慢、
> 且不是嵌入主窗口而是弹出独立新窗口。** 需求②（可配置快捷键）已在
> commit `94f478ad` 完成，本文档只覆盖①。
>
> 本文档在 Windows 开发机上撰写：`cmd/personal` 的 darwin 构建依赖 Wails 的
> Cgo/WKWebView，**无法在本机编译验证**，故①的实现与验证必须在 macOS 上进行。
> 本文把方案定到可直接落地的粒度，减少真机来回。

## 现状与根因

- Windows：每个 AI 服务是一个 WebView2 窗口，放进有界 LRU 池（`aiPool`，≤9），
  通过 Win32 `SetWindowLongPtr(GWLP_HWNDPARENT)` 把服务窗口设为主窗口(hub)的
  **owner 顶层窗口**（`aiembed_windows.go`），并用屏外 stash 保活。切换即
  reveal，0 延迟、保留登录/滚动。核心机制在 `aiwin.go`（池、`aiActiveService`、
  `applyAIServiceVisibility`、stage 定位）+ 四个 `aiEmbed*` 函数。
- macOS/Linux：`aiembed_other.go` 中 `aiEmbedSupported = false`，四个 `aiEmbed*`
  为空实现 → `ActivateAIService` 退化到 `OpenAIService`，每个服务是**独立
  NSWindow**（`aiServiceWindows` map；已复用、切换会 raise，但**不嵌入**、首开
  仍是整窗+整页加载）。
- 结论：「不嵌入=独立新窗口」是根因；「慢」主要来自独立窗口 + 首次整页加载。
  Wails v3 **未暴露**跨平台父子窗口 API（只有 `Show/Hide/SetSize/
  SetRelativePosition/SetAlwaysOnTop/Focus`），要真嵌入必须走原生 Cocoa。

## 目标

在 macOS 复用现有 Windows 那套「池 + 可见性 + stage 定位」机制，实现：
1. 服务 webview **嵌入**到 hub 的 stage 区域（随 hub 移动/缩放跟随，无独立
   任务栏/Mission Control 条目，随 hub 关闭而隐藏）。
2. 切换**即时**（窗口常驻、Show/reposition 而非重建），保留登录/滚动。

## 方案（推荐）：purego + objc 实现 darwin 版 `aiEmbed*`

复刻 `aiembed_windows.go` 的思路，但用 macOS 原生等价物。新增
`desktop/cmd/personal/aiembed_darwin.go`（`//go:build darwin`），实现同签名的四个
函数并置 `const aiEmbedSupported = true`，**无需改动** `aiwin.go` 的池/可见性逻辑。

取原生句柄：`w.NativeWindow()` 在 darwin 返回 `NSWindow*`（与 Windows 取 HWND 同
入口）。用 `github.com/ebitengine/purego` + `purego/objc` 调 objc 运行时（纯 Go，
不引 Cgo；ARM64/AMD64 由 purego 处理 msgSend ABI 差异）。若引入 purego 依赖不合适，
备选是在 darwin 用 Cgo `.m`（与 Wails 一致），把四个函数实现为 C 桥接。

四个函数的 macOS 等价实现：

| 函数 | Windows | macOS 等价 |
|------|---------|-----------|
| `aiEmbedOwn(hub, child)` | 去边框+设 owner | `child.styleMask = NSWindowStyleMaskBorderless`；`[hub addChildWindow:child ordered:NSWindowAbove]`（子窗随父移动、随父关闭、置于父上）；`child.collectionBehavior |= NSWindowCollectionBehaviorTransient`（不进 Mission Control/Exposé），必要时 `setLevel` 对齐 |
| `aiEmbedPosition(hub,child,x,y,w,h)` | 客户区相对→屏幕坐标 SetWindowPos | 用 hub 的 `contentView` 在屏幕中的 frame 作原点；**坐标翻转**：macOS 屏幕原点在左下，stage 的 (x,y,h) 来自前端左上原点，需 `screenY = hubContentTop - y - h`；`[child setFrame:NSMakeRect(...) display:YES]` |
| `aiEmbedReveal(...)` | 移回 stage + 显示 + 聚焦 | `aiEmbedPosition` 后 `[child orderFront:nil]`（作为 childWindow 会跟随 hub 层级）；Turnstile 需要时 `makeFirstResponder` webview |
| `aiEmbedStash(child,w,h)` | 移屏外保活（不 SW_HIDE） | 优先 `[hub removeChildWindow:child]; [child orderOut:nil]`；若 orderOut 导致 WKWebView 挂起白屏，则改用屏外移动（`setFrameOrigin` 到 `-32000`）保活，与 Windows 一致 |

要点/坑：
- **坐标系翻转 + Retina 缩放**：stage rect 是物理像素、左上原点；NSWindow frame 是
  点(point)、左下原点。需按 `hub.screen.backingScaleFactor` 从物理像素换算为点，并
  翻转 Y。这是最易错处，先用固定 stage 打通再接前端 `SetAIStageRect`。
- **跟随 hub 移动/缩放**：`main.go` 目前只给 Windows 注册了 `WindowDidMove/
  WindowDidResize → repositionActiveAIService`。需同样注册 macOS 事件
  （`events.Mac.*` 里的移动/缩放完成事件），复用 `repositionActiveAIService`。
- **UI 线程**：所有 AppKit 调用必须在主线程 —— 复用现有 `application.InvokeSync`
  包裹（`ActivateAIService` 已如此 own）。
- **frameless 主窗**：服务窗创建时已 `Frameless:true`（`createServiceEntry`），
  darwin 沿用即可。
- **LRU 上限**：Windows 设 9。macOS 每个 WKWebView 是独立进程，内存更高，建议
  darwin 用更小上限（如 5），通过给 `aiPoolMax` 加平台常量实现。

## 影响面 / 不改动项

- 新增：`aiembed_darwin.go`；把 `aiembed_other.go` 的 build tag 改为
  `//go:build !windows && !darwin`（Linux 继续走空实现/独立窗口）。
- 复用：`aiwin.go` 全部池/可见性逻辑、`createServiceEntry`（HTML+guard+SetURL）、
  `applyAIServiceVisibility`、`SetAIStageRect`、前端 `ai.html`（stage ResizeObserver）
  —— 一旦 `aiEmbedSupported=true` 且四函数生效，走的就是与 Windows 相同的路径。
- `main.go`：为 darwin 注册 hub 移动/缩放→`repositionActiveAIService`。

## 验证计划（在 macOS 上）

1. `make build-personal-darwin` 编译通过（首关：purego/objc 或 Cgo 桥接编译）。
2. 打开 AI 直达：服务出现在 hub stage 内（非独立窗口），Dock/Mission Control 无
   多余条目。
3. 切换服务即时、保留登录与滚动；切回不重载。
4. 拖动/缩放 hub，服务窗跟随、不撕裂、不错位（重点验证坐标翻转与缩放）。
5. 关闭/最小化 hub，服务窗随之隐藏；再唤起恢复。
6. Cloudflare Turnstile 类站点可正常交互（焦点链路）。
7. 内存：连开多个服务触发 LRU 淘汰，稳定在上限内。

## 增量落地顺序（降低盲写风险）

1. 先只实现 `aiEmbedOwn`+`aiEmbedPosition`（固定 stage 矩形、单服务），验证子窗口
   能贴到 hub 上、坐标正确。
2. 再接 `aiEmbedReveal`/`aiEmbedStash` 与 `applyAIServiceVisibility`，打通多服务
   切换与保活。
3. 最后接 hub 移动/缩放跟随与前端动态 stage。
4. 每步单独在真机构建冒烟，避免一次性大改难定位。
