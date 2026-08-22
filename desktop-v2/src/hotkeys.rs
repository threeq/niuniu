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

/// 位置连接快捷键的修饰符前缀：macOS 用 Cmd+Shift，其余 Ctrl+Shift
/// （与 v1 connhotkey.go connHotkeyModifierPrefix 一致）。
fn conn_prefix() -> &'static str {
    if cfg!(target_os = "macos") { "cmd+shift+" } else { "ctrl+shift+" }
}

/// AI 直达快捷键的冲突回退候选（v1 hotkey_alt_windows.go / hotkey_darwin.go）。
/// Ctrl/Cmd+Shift+A 在 Windows 常被微信/QQ/搜狗/截图占用，依次尝试备选。
fn ai_candidates() -> Vec<String> {
    let primary = normalize(&default_ai_accelerator());
    if cfg!(target_os = "macos") {
        vec![primary, "cmd+option+a".into(), "cmd+control+a".into()]
    } else {
        vec![primary, "ctrl+alt+a".into(), "ctrl+shift+space".into()]
    }
}

/// 构造 global-shortcut 插件：单一 handler 按触发组合路由到窗口动作。
pub fn plugin() -> tauri::plugin::TauriPlugin<tauri::Wry> {
    tauri_plugin_global_shortcut::Builder::new()
        .with_handler(|app, sc, ev| {
            if ev.state() != ShortcutState::Pressed {
                return;
            }
            let cfg = app.state::<CfgState>().snapshot();
            let s = normalize(&sc.to_string());
            let win = normalize(&cfg.hotkey.toggle_window);
            let ai_candidates = ai_candidates();
            if !win.is_empty() && s == win {
                crate::commands::toggle_main_window(app);
                return;
            }
            if cfg.hotkey.toggle_ai_enabled && ai_candidates.iter().any(|c| c == &s) {
                crate::commands::toggle_ai_window(app);
                return;
            }
            // 位置连接快捷键 ctrl/cmd+shift+1..9 + 0(picker)
            let prefix = conn_prefix();
            if let Some(rest) = s.strip_prefix(prefix) {
                if rest == "0" {
                    crate::commands::toggle_picker(app);
                } else if let Ok(n) = rest.parse::<u32>() {
                    if (1..=9).contains(&n) {
                        crate::commands::connect_by_position(app, n);
                    }
                }
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

    // AI 直达切换：依次尝试候选组合，第一个被 OS 接受的即生效（v1 RegisterAI）。
    if cfg.hotkey.toggle_ai_enabled {
        let mut bound = false;
        for c in ai_candidates() {
            if gs.register(c.as_str()).is_ok() {
                bound = true;
                break;
            }
        }
        if !bound {
            eprintln!("register AI hotkey: all candidates rejected");
        }
    }

    // 位置连接快捷键 Ctrl/Cmd+Shift+1..9 + 0（固定，非配置项；单个被占则跳过）。
    let prefix = conn_prefix();
    for n in 0u32..=9 {
        let spec = format!("{prefix}{n}");
        if let Err(e) = gs.register(spec.as_str()) {
            eprintln!("register connection hotkey {spec} failed: {e}");
        }
    }
}
