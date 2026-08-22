//! 全局快捷键：主窗口切换 + AI 直达切换 + 连接位置快捷键（Ctrl/Cmd+Shift+1..9
//! 开第 N 个已保存连接、0 切换 picker），按 config 注册。
//!
//! 用 `on_shortcut`（注册时直接绑 per-shortcut handler），不靠 `sc.to_string()` 字符串
//! 匹配——global-hotkey 的 Display 产出 "shift+control+a"（小写、control 非 ctrl、顺序
//! shift 在前），与 config 的 "Ctrl+Shift+A" 比字符串永不相等。on_shortcut 经
//! HotKeyId 派发（tauri-plugin-global-shortcut lib.rs 的 dispatch 按 id 查表调
//! shortcut.handler），无格式依赖。

use tauri_plugin_global_shortcut::GlobalShortcutExt;

use crate::config::{default_ai_accelerator, default_window_accelerator};

/// 位置连接快捷键的修饰符前缀：macOS 用 Cmd+Shift，其余 Ctrl+Shift
/// （与 v1 connhotkey.go connHotkeyModifierPrefix 一致）。global-hotkey from_str
/// 大小写不敏感，这里用小写。
fn conn_prefix() -> &'static str {
    if cfg!(target_os = "macos") { "cmd+shift+" } else { "ctrl+shift+" }
}

/// AI 直达快捷键的冲突回退候选（v1 hotkey_alt_windows.go / hotkey_darwin.go）。
/// Ctrl/Cmd+Shift+A 在 Windows 常被微信/QQ/搜狗/截图占用，依次尝试备选。
fn ai_candidates() -> Vec<String> {
    let primary = default_ai_accelerator();
    if cfg!(target_os = "macos") {
        vec![primary, "Cmd+Option+A".into(), "Cmd+Control+A".into()]
    } else {
        vec![primary, "Ctrl+Alt+A".into(), "Ctrl+Shift+Space".into()]
    }
}

/// 构造 global-shortcut 插件。handler 走 per-shortcut on_shortcut，插件级无 handler。
pub fn plugin() -> tauri::plugin::TauriPlugin<tauri::Wry> {
    tauri_plugin_global_shortcut::Builder::new().build()
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
        if let Err(e) = gs.on_shortcut(accel.as_str(), |app, _sc, _ev| {
            crate::commands::toggle_main_window(app);
        }) {
            eprintln!("register window hotkey {accel} failed: {e}");
        }
    }

    // AI 直达切换：依次尝试候选组合，第一个被 OS 接受的即生效（v1 RegisterAI）。
    if cfg.hotkey.toggle_ai_enabled {
        let mut bound = false;
        for c in ai_candidates() {
            if gs.on_shortcut(c.as_str(), |app, _sc, _ev| {
                crate::commands::toggle_ai_window(app);
            }).is_ok() {
                bound = true;
                break;
            }
        }
        if !bound {
            eprintln!("register AI hotkey: all candidates rejected");
        }
    }

    // 位置连接快捷键 Ctrl/Cmd+Shift+1..9（开第 N 个已保存连接）+ 0（toggle picker）。
    // 固定键，非配置项；单个被 OS 占则跳过。位置每次按键实时从 config 快照解析。
    let prefix = conn_prefix();
    for n in 1u32..=9 {
        let spec = format!("{prefix}{n}");
        if let Err(e) = gs.on_shortcut(spec.as_str(), move |app, _sc, _ev| {
            crate::commands::connect_by_position(app, n);
        }) {
            eprintln!("register connection hotkey {spec} failed: {e}");
        }
    }
    let zero = format!("{prefix}0");
    if let Err(e) = gs.on_shortcut(zero.as_str(), |app, _sc, _ev| {
        crate::commands::toggle_picker(app);
    }) {
        eprintln!("register picker hotkey {zero} failed: {e}");
    }
}

/// 当前已注册快捷键的展示标签（供 ai.html 显示 AI 快捷键）。读 config，空则用默认。
#[allow(dead_code)]
pub fn ai_hotkey_label(cfg: &crate::config::DesktopConfig) -> String {
    if cfg.hotkey.toggle_ai.trim().is_empty() {
        default_ai_accelerator()
    } else {
        cfg.hotkey.toggle_ai.clone()
    }
}
