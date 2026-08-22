//! 全局快捷键：主窗口切换 + AI 直达切换两个全局组合键，按 config 注册。
//!
//! tauri-plugin-global-shortcut 2.x 的 `register` 只接受组合键（handler 是插件级
//! 唯一的），因此这里在 `plugin()` 里装一个全局分发 handler，根据触发键与当前
//! config 的组合串匹配来路由动作。注册失败（组合被其它应用占用）时静默跳过。

use tauri::Manager;
use tauri_plugin_global_shortcut::{GlobalShortcutExt, ShortcutState};

use crate::config::{default_ai_accelerator, default_window_accelerator};
use crate::state::CfgState;

/// 规范化加速器串（global-hotkey 解析器对大小写宽容，这里统一小写）。
fn normalize(accel: &str) -> String {
    accel.trim().to_lowercase()
}

/// 构造 global-shortcut 插件：单一 handler 按触发组合路由到窗口动作。
pub fn plugin() -> tauri::plugin::TauriPlugin<tauri::Wry> {
    tauri_plugin_global_shortcut::Builder::new()
        .with_handler(|app, sc, ev| {
            if ev.state() != ShortcutState::Pressed {
                return;
            }
            let cfg = app.state::<CfgState>().snapshot();
            let win = normalize(&cfg.hotkey.toggle_window);
            let ai = normalize(&cfg.hotkey.toggle_ai);
            let s = normalize(&sc.to_string());
            if !win.is_empty() && s == win {
                crate::commands::toggle_main_window(app);
            } else if !ai.is_empty() && s == ai {
                crate::commands::toggle_ai_window(app);
            }
        })
        .build()
}

/// 按当前 config 应用快捷键：先注销全部再注册，幂等。
pub fn apply_hotkeys(app: &tauri::AppHandle, cfg: &crate::config::DesktopConfig) {
    let gs = app.global_shortcut();
    let _ = gs.unregister_all();

    // 主窗口切换（默认 Ctrl/Cmd+Shift+N）
    if cfg.hotkey.toggle_window_enabled {
        let accel = if cfg.hotkey.toggle_window.trim().is_empty() {
            default_window_accelerator()
        } else {
            cfg.hotkey.toggle_window.clone()
        };
        let accel = normalize(&accel);
        if let Err(e) = gs.register(accel.as_str()) {
            eprintln!("register window hotkey {accel} failed: {e}");
        }
    }

    // AI 直达切换（本 issue 规定 Ctrl/Cmd+Shift+A）
    if cfg.hotkey.toggle_ai_enabled {
        let accel = if cfg.hotkey.toggle_ai.trim().is_empty() {
            default_ai_accelerator()
        } else {
            cfg.hotkey.toggle_ai.clone()
        };
        let accel = normalize(&accel);
        if let Err(e) = gs.register(accel.as_str()) {
            eprintln!("register AI hotkey {accel} failed: {e}");
        }
    }
}
