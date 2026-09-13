//! AI 服务窗口嵌入（非 Windows stub）：macOS/Linux 上 v1 也是独立普通窗口
//! （aiEmbedSupported=false，见 aiembed_other.go），v2 对齐——服务窗口用普通
//! show/hide 语义，不做 Win32 停靠。窗口管理在 commands.rs 里按本常量分流。

use tauri::WebviewWindow;

pub const AI_EMBED_SUPPORTED: bool = false;

// 这些函数在 Windows 版里做 Win32 停靠（ai_embed_windows.rs）；非 Windows 是
// 普通窗口语义，commands.rs 在 AI_EMBED_SUPPORTED 分支下不调用它们，但引用
// 处是运行时判断而非 #[cfg]，所以这里必须提供同名 no-op stub 保持可编译。
pub fn dock_over_stage(_hub: &WebviewWindow, _child: &WebviewWindow) {}
pub fn reveal_over_stage(_hub: &WebviewWindow, _child: &WebviewWindow, _stage: (i32, i32, i32, i32)) {}
pub fn position_window(_hub: &WebviewWindow, _child: &WebviewWindow, _stage: (i32, i32, i32, i32)) {}
pub fn stash_offscreen(_win: &WebviewWindow, _stage: (i32, i32, i32, i32)) {}
