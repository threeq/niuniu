# Windows 中文 IME 修复 —— WebView2 宿主模式验证 (ws-683)

> 结论先行：desktop 在 Windows 上是 **窗口化宿主（windowed hosting）**，不是
> composition / 视觉宿主。聊天 `<textarea>` 间歇性中文输不进去的根因是
> **WebView2「失焦→重新获焦」后 IME 未重新挂载（focus-regain IME re-arm）**，
> 已由 SPA 侧兜底自动复位。日期 2026-06-25。

## 精确复现

- 托盘 显示/隐藏：正常（此前「托盘 Show 补 SetForegroundWindow」一说作废）。
- **Alt-Tab 切走再切回**：有概率中文输不进去（Alt-Tab 是触发，非修复）。
- 恢复：点一下输入框 / Tab 回到输入框即可输入 —— 这是 focus-regain IME 类 bug 的标志。
- 范围：聊天 `<textarea>`（HTML 输入），非终端 xterm.js；未用无边框窗口；同一 SPA
  在 Edge/Chrome 正常（浏览器进程焦点链完整，故不复现）。

## 四个行动的验证结论

| # | 行动 | 验证结论 |
|---|------|----------|
| 1 | **升级 WebView2 Runtime** | 运维/用户动作，非代码。最便宜，建议先做：candidate 候选框定位与 IME 行为随 Runtime 修复。 |
| 2 | **切回窗口化宿主** | **无需做 —— 本来就是窗口化**。`go-webview2 v1.0.23` 只调用 `CreateCoreWebView2Controller(hwnd)`，依赖里**根本没有** composition controller（`edge/` 包内唯一出现 "visual hosting" 的是 `version_map.go` 的发行说明字符串，非代码）。niuniu 也未设任何 `Windows{}` / frameless / 透明 / acrylic/mica/backdrop 选项。→ 此前「composition 重回嫌疑」**判断有误**；微软「visual hosting 失焦再获焦丢/重复 IME」的发行说明**不适用本应用**。 |
| 3 | **SPA 侧兜底** | **已上线**，是我们可控、可即时发布的修复。`server/web/src/lib/ime-caret-fix.ts`：监听 window `blur` 把焦点移出可编辑元素到 offscreen sink；window `focus` 时**同步**（同一事件循环、无 setTimeout）做一次 blur→refocus 重挂 IME —— 选定要重挂的可编辑元素（直接点击激活则取点中的那个，否则取失焦前的 `lastEditable`），把焦点移到 park 元素（牛牛品牌元素，offscreen sink 单独不够；nav 未挂载时回退 sink）再立刻移回目标。**真机调参（ws-683）发现旧版把焦点停在可见品牌元素上等 ~150ms，浏览器会把这一帧「停在品牌元素」绘制出来 —— 这就是用户看到的「闪烁」（正常能用时也闪）。改同步后中间帧不绘制，无闪烁**；同时验证「重挂是否必须等 WebView2 settle」。仅 Windows 生效，保留 selectionStart/End。若真机证明确需 settle，再重新引入单次 deferred refocus。 |
| 4 | **Wails 宿主 MoveFocus** | **确认存在竞态（真正的上游缺口）**。Wails v3 alpha.74 的 `webview_window_windows.go`：`WM_ACTIVATE/WA_ACTIVE`（Alt-Tab 激活）只 `emit(WindowActive)`，**不**调用 `chromium.Focus()`/`controller.MoveFocus`；只有 `WM_SETFOCUS`（frame 收到焦点）才会。Alt-Tab 切回时 Windows 可能把焦点直接还给 WebView2 子 HWND，绕过 frame 的 `WM_SETFOCUS` → MoveFocus 不触发 → IME 不重挂 → 间歇失效。SPA 侧兜底（#3）在 DOM 层补上了这层复位，**无需改上游/无需提补丁**。 |

## 最终生效项

- **窗口化宿主已是现状**（#2 无操作）。
- **SPA 侧兜底已生效并保留为常驻安全网**（#3，`ime-caret-fix.ts`，仅 Windows）。
- **建议运维升级 WebView2 Runtime 到最新**（#1，命中候选框定位与 IME 行为修复）。
- Wails 上游缺口（#4）已被 #3 在 DOM 层兜住，**暂不提上游补丁**；若未来 SPA 兜底
  仍偶发不复位，再考虑在 niuniu 自有代码里 `RegisterHook(events.Windows.WindowActive)`
  调 `window.Focus()` 强制重发 MoveFocus（非上游补丁，但需在 Wails 构建上实测确认
  不引起焦点抖动 / 主线程编组问题）。

## 机制澄清:延迟非关键,焦点转移才是关键

复位 IME 的是**焦点从输入框移出去、再移回来**(blur→refocus,等价手动「点一下输入框」),
不是延迟本身。`ime-caret-fix.ts` 里的 `RESTORE_DELAY_MS=150` 只是等 WebView2 在窗口激活后
稳定下来、让这个转移能被接住而重挂 IME context;**单纯「延迟 200ms 后 focus」而不先 blur**
(元素本来就 focused)等于没动焦点,WebView2 不会重挂 IME,不能替代本兜底。

由此:
- **上游 Wails 补丁 / Go 侧 `RegisterHook(WindowActive)→Focus()`(#4):当前不需要**。只要
  SPA 侧的 blur→refocus 在 `window focus` 时可靠触发,已在 DOM 层补上 #4 的 MoveFocus 竞态;
  Go 侧钩子仅作「DOM focus 事件偶发不触发」的保险,暂不实现。
- **Runtime 升级(#1)与延迟无关、独立**:针对候选框定位 + 从源头修 focus-regain IME。若升级后
  实测 Alt-Tab 不再复发,SPA 这套 park/restore 将来才可考虑移除;物理复测确认前两者并存最稳。

## 验收

- Alt-Tab 切走再切回后，聊天 `<textarea>` 无需手动点击/Tab 即可输入中文；候选框跟随
  光标（随 Runtime 升级与 Edge/Chrome 一致）；反复 Alt-Tab 稳定不复现。
- 代码层验证已落在 `server/web/src/lib/ime-caret-fix.ts` 头部注释（含宿主模式结论）。

## 关键文件 / 证据

- SPA 兜底：`server/web/src/lib/ime-caret-fix.ts`（+ `.test.ts`）。
- 依赖宿主模式：`go-webview2@v1.0.23/pkg/edge/chromium.go:323`
  （`CreateCoreWebView2Controller(e.hwnd, ...)`），无 composition controller。
- Wails 激活路径：`wails/v3@v3.0.0-alpha.74/pkg/application/webview_window_windows.go`
  —— `WM_ACTIVATE` (1432) 不调 Focus；`WM_SETFOCUS` (1499) 调 `w.focus()`→`chromium.Focus()`→`MoveFocus`。
- niuniu 窗口创建：`desktop/cmd/personal/main.go`
  （`WebviewWindowOptions` 仅设跨平台字段，无 `Windows{}` 宿主选项）。
</content>
</invoke>
