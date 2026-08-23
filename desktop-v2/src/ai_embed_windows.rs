//! AI 服务窗口嵌入（Windows）：把每个服务的 webview 停靠在 hub 的 stage 区域
//! 上，让整个 AI 直达看起来是「单窗口 + 窗内切换器」。移植自 v1 Wails 版
//! desktop/cmd/personal/aiembed_windows.go（issue #674：v2 初版迁移丢了这里的
//! 全套机制，服务窗口表现为空白）。
//!
//! 设计要点（为什么是 OWNED 顶层窗口而不是 WS_CHILD）：
//! 子窗口渲染没问题，但浏览器语义异常——Chromium 会把它当被遮挡/后台文档，
//! Cloudflare Turnstile 不信任并反复验证。服务窗口保持普通顶层 WebView2 窗口
//! （正常可见性/焦点），用 GWLP_HWNDPARENT 把 OWNER 设成 hub：永远浮在 hub
//! 之上、无任务栏按钮、随 hub 关闭/最小化；再用屏幕坐标摆在 stage 上，hub
//! 移动/缩放时同步。
//!
//! 本文件函数都直接操作 HWND。Win32 窗口操作要求在创建该窗口的线程（主线程）
//! 执行——调用方统一经 run_on_main_thread 派发。

use tauri::WebviewWindow;
use windows::Win32::Foundation::{HWND, POINT};
use windows::Win32::Graphics::Gdi::ClientToScreen;
use windows::Win32::UI::Input::KeyboardAndMouse::SetFocus;
use windows::Win32::UI::WindowsAndMessaging::{
    GetWindowLongPtrW, SetWindowLongPtrW, SetWindowPos, ShowWindow, GWL_EXSTYLE, GWL_STYLE,
    GWLP_HWNDPARENT, HWND_TOP, SW_SHOWNOACTIVATE, SWP_FRAMECHANGED, SWP_NOACTIVATE, SWP_NOMOVE,
    SWP_NOSIZE, SWP_NOZORDER, WS_BORDER, WS_CAPTION, WS_DLGFRAME, WS_EX_APPWINDOW,
    WS_EX_TOOLWINDOW, WS_MAXIMIZEBOX, WS_MINIMIZEBOX, WS_POPUP, WS_SYSMENU, WS_THICKFRAME,
};

/// stash 位置：把「仍显示着」的窗口挪到所有显示器之外。不用 SW_HIDE——隐藏的
/// WebView2 会挂起合成，再次显示回来是空白页（v1 实测踩坑点）；保持显示但挪到
/// 屏幕外，配合 --disable-backgrounding-occluded-windows 不节流，页面持续渲染，
/// 揭示时只是挪回来且已画好。
const OFFSCREEN_POS: i32 = -32000;

/// 帧样式位全剥（无边框贴 stage）。保留/添加 WS_POPUP（顶层无框），显式去掉
/// WS_CHILD（我们是 owned 顶层，不是子窗口）。
const WS_FRAME_BITS: u32 = WS_CAPTION.0
    | WS_THICKFRAME.0
    | WS_MINIMIZEBOX.0
    | WS_MAXIMIZEBOX.0
    | WS_SYSMENU.0
    | WS_BORDER.0
    | WS_DLGFRAME.0;

fn hwnd_of(win: &WebviewWindow) -> Option<HWND> {
    win.hwnd().ok().map(|h| h.0).map(HWND)
}

/// hub 客户区左上角的屏幕坐标（stage 矩形是 client-relative，需加这个原点）。
fn client_origin(hwnd: HWND) -> (i32, i32) {
    let mut pt = POINT { x: 0, y: 0 };
    // SAFETY: 传入有效 HWND 与指向有效 POINT 的指针；失败时保持 (0,0)。
    let _ = unsafe { ClientToScreen(hwnd, &mut pt) };
    (pt.x, pt.y)
}

/// 把 child 变成 hub 的 frameless OWNED 顶层窗口（浮在 hub 上、无任务栏按钮、
/// 无边框）。不移动不显示——揭示走 reveal，隐藏走 stash。
pub fn own(hub_hwnd: HWND, child_hwnd: HWND) {
    // SAFETY: 常规 Win32 样式操作；句柄由调用方保证有效（None 时上游已跳过）。
    unsafe {
        let style = GetWindowLongPtrW(child_hwnd, GWL_STYLE) as usize;
        SetWindowLongPtrW(
            child_hwnd,
            GWL_STYLE,
            ((style & !(WS_FRAME_BITS as usize)) | WS_POPUP.0 as usize) as isize,
        );

        let ex = GetWindowLongPtrW(child_hwnd, GWL_EXSTYLE) as usize;
        SetWindowLongPtrW(
            child_hwnd,
            GWL_EXSTYLE,
            ((ex & !(WS_EX_APPWINDOW.0 as usize)) | WS_EX_TOOLWINDOW.0 as usize) as isize,
        );

        // GWLP_HWNDPARENT 在顶层窗口上设的是 OWNER（名字误导，不是父子关系）。
        SetWindowLongPtrW(child_hwnd, GWLP_HWNDPARENT, hub_hwnd.0 as isize);

        // SWP_FRAMECHANGED 让样式剥离生效；不移动/缩放/置顶/激活。
        let _ = SetWindowPos(
            child_hwnd,
            None,
            0,
            0,
            0,
            0,
            SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED | SWP_NOSIZE | SWP_NOMOVE,
        );
    }
}

/// 把停靠的服务窗口移/缩到 hub 的 stage 上。x,y,w,h 是物理像素、client-relative
/// 到 hub；经 hub 客户区原点转屏幕坐标。不改可见性与 z-order。
pub fn position(hub_hwnd: HWND, child_hwnd: HWND, x: i32, y: i32, w: i32, h: i32) {
    let (ox, oy) = client_origin(hub_hwnd);
    // SAFETY: 常规窗口定位；flags 不激活不置顶。
    unsafe {
        let _ = SetWindowPos(
            child_hwnd,
            None,
            ox + x,
            oy + y,
            w,
            h,
            SWP_NOZORDER | SWP_NOACTIVATE,
        );
    }
}

/// 揭示：摆到 stage 上、显示、抬到 hub 之上并给焦点（文档需要 report focused/
/// visible，否则 Cloudflare Turnstile 反复挑战）。窗口此前只是被 stash 到屏幕外
/// （从未 SW_HIDE），WebView2 合成未中断，出现时已画好——这就是消灭切换空白页
/// 的机制。
///
/// stash 期间窗口客户尺寸可能过期（hub 被缩放/最大化时 WebView2 会推迟/跳过那次
/// resize，按旧尺寸渲染留下黑边），所以分两步：先 h+1 再落回 h，强制产生一次
/// 非零 delta 的 WM_SIZE 让 WebView2 按当前 stage 尺寸重排。
pub fn reveal(hub_hwnd: HWND, child_hwnd: HWND, x: i32, y: i32, w: i32, h: i32) {
    let (ox, oy) = client_origin(hub_hwnd);
    let (px, py) = (ox + x, oy + y);
    // SAFETY: ShowWindow/SetWindowPos/SetFocus 常规用法；句柄有效。
    unsafe {
        let _ = ShowWindow(child_hwnd, SW_SHOWNOACTIVATE);
        // nudge h+1 再落回——强制 re-show 后 WebView2 重算尺寸。
        let _ = SetWindowPos(child_hwnd, Some(HWND_TOP), px, py, w, h + 1, SWP_NOACTIVATE);
        let _ = SetWindowPos(child_hwnd, Some(HWND_TOP), px, py, w, h, SWP_NOACTIVATE);
        let _ = SetFocus(Some(child_hwnd));
    }
}

/// stash：不 SW_HIDE，把窗口整个挪到所有显示器之外但保持显示，WebView2 在后台
/// 继续渲染（--disable-backgrounding-occluded-windows 关掉节流），而不是挂起
/// 合成变成空白。加载 splash / HTML 覆盖层打开时使用。窗口保持 stage 尺寸，
/// 揭示无需重排；绝不聚焦它。
pub fn stash(child_hwnd: HWND, w: i32, h: i32) {
    let w = if w <= 0 { 1 } else { w };
    let h = if h <= 0 { 1 } else { h };
    // SAFETY: 先挪屏幕外（新建窗口还隐藏时无害），再确保显示——窗口绝不会在
    // 默认屏内位置闪一下才被挪走。
    unsafe {
        let _ = SetWindowPos(
            child_hwnd,
            None,
            OFFSCREEN_POS,
            OFFSCREEN_POS,
            w,
            h,
            SWP_NOZORDER | SWP_NOACTIVATE,
        );
        let _ = ShowWindow(child_hwnd, SW_SHOWNOACTIVATE);
    }
}

// ─── Tauri WebviewWindow 层面的便捷封装（hub 从 label 现查） ──────────────

/// hub HWND（ai-hub 窗口不存在或未就绪时 None）。
fn hub_hwnd(win: &WebviewWindow) -> Option<HWND> {
    hwnd_of(win)
}

/// own + position 的组合：建窗后立即调用（在主线程）。stage 矩形经 AiState
/// 由调用方环境读取（见 commands.rs：建窗线程里先 own，reveal/stash 随后统一
/// 按 stage 处理，这里只建立 owner 关系与无框样式）。
pub fn dock_over_stage(hub: &WebviewWindow, child: &WebviewWindow) {
    let (Some(hh), Some(ch)) = (hub_hwnd(hub), hwnd_of(child)) else { return };
    own(hh, ch);
}

/// reveal 的窗口封装（hub HWND 从 GWLP_HWNDPARENT owner 读回，调用方无需再传 hub）。
pub fn reveal_over_stage(child: &WebviewWindow, stage: (i32, i32, i32, i32)) {
    let (x, y, w, h) = stage;
    if w <= 0 || h <= 0 {
        return;
    }
    let Some(ch) = hwnd_of(child) else { return };
    // SAFETY: 只读 owner 句柄。
    let hub_raw = unsafe { GetWindowLongPtrW(ch, GWLP_HWNDPARENT) };
    if hub_raw == 0 {
        return;
    }
    reveal(HWND(hub_raw as _), ch, x, y, w, h);
}

/// position 的窗口封装。
pub fn position_window(hub: &WebviewWindow, child: &WebviewWindow, stage: (i32, i32, i32, i32)) {
    let (Some(hh), Some(ch)) = (hub_hwnd(hub), hwnd_of(child)) else { return };
    let (x, y, w, h) = stage;
    if w > 0 && h > 0 {
        position(hh, ch, x, y, w, h);
    }
}

/// stash 的窗口封装。
pub fn stash_offscreen(win: &WebviewWindow, stage: (i32, i32, i32, i32)) {
    let Some(wh) = hwnd_of(win) else { return };
    let (_, _, w, h) = stage;
    stash(wh, w, h);
}

/// 嵌入能力标记：commands.rs 据此分流（Windows 停靠 / 非 Windows 普通显隐）。
pub const AI_EMBED_SUPPORTED: bool = true;
