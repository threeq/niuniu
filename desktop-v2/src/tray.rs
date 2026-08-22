//! 系统托盘：图标 + 菜单（显示/刷新/重建/重启 server、AI 直达、连接管理、退出），
//! 并随连接变化重建（rebuild_tray）。左键单击 → 显示主窗口。

use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager};

use crate::i18n;
use crate::state::{AppMeta, ConnState, CfgState, DiscoverState, ServerState};

/// 位置快捷键标签（Ctrl/Cmd+Shift+<n>），与 v1 connhotkey.go connHotkeyLabel 一致。
fn conn_hotkey_label(pos: u32) -> String {
    let prefix = if cfg!(target_os = "macos") { "Cmd+Shift+" } else { "Ctrl+Shift+" };
    format!("{prefix}{pos}")
}

/// 构建托盘（首次）或重建菜单。id "main-tray" 复用。
pub fn build_tray(app: &AppHandle) -> tauri::Result<TrayIcon> {
    let menu = build_menu(app)?;
    let icon = app.default_window_icon().cloned().ok_or_else(|| {
        tauri::Error::AssetNotFound("icon".to_string())
    })?;

    let tray = TrayIconBuilder::with_id("main-tray")
        .icon(icon)
        .tooltip(i18n::app_name(&app.state::<AppMeta>().lang))
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id().as_ref() {
            "show" => show_main_closure(app.clone())(),
            "reload" => reload_main(app),
            "rebuild" => {
                crate::commands::hard_reset_main(app);
            }
            "restart" => {
                let _ = crate::commands::restart_server(app);
            }
            "ai" => {
                crate::commands::toggle_ai_window(app);
            }
            "picker" => {
                crate::commands::open_picker(app);
            }
            "runners" => {
                crate::commands::open_runners(app);
            }
            "mobile" => {
                crate::commands::open_mobile_access(app);
            }
            "conn-focus" => {
                if let Some(key) = event.id().as_ref().strip_prefix("conn-focus:") {
                    crate::commands::focus_connection(app, key);
                }
            }
            "conn-reload" => {
                if let Some(key) = event.id().as_ref().strip_prefix("conn-reload:") {
                    crate::commands::reload_connection(app, key);
                }
            }
            "conn-rebuild" => {
                if let Some(key) = event.id().as_ref().strip_prefix("conn-rebuild:") {
                    crate::commands::hard_reset_connection(app, key);
                }
            }
            "saved-conn" => {
                if let Some(id) = event.id().as_ref().strip_prefix("saved-conn:") {
                    let _ = crate::commands::connect_by_id_internal(app, id);
                }
            }
            "discovered-conn" => {
                if let Some(rest) = event.id().as_ref().strip_prefix("discovered-conn:") {
                    // rest = "host|port"
                    if let Some((host, port)) = rest.rsplit_once('|') {
                        if let Ok(p) = port.parse::<u16>() {
                            let _ = crate::commands::connect_to_address_internal(app, host, p);
                        }
                    }
                }
            }
            "conn-close" => {
                if let Some(key) = event.id().as_ref().strip_prefix("conn-close:") {
                    crate::commands::close_connection(app, key);
                }
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                let app = tray.app_handle();
                show_main_closure(app.clone())();
            }
        })
        .build(app)?;

    Ok(tray)
}

fn show_main_closure(app: AppHandle) -> impl Fn() + Send + 'static {
    move || {
        let _ = crate::commands::show_main_window(&app);
    }
}

/// 构建当前菜单：本地块 + 活跃连接子菜单 + 未激活的已保存连接 + mDNS 发现
/// + picker（带快捷键标签）+ 移动接入 + 退出。对应 v1 app.go RebuildTray。
pub fn build_menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    use tauri::menu::IsMenuItem;

    let lang = app.state::<AppMeta>().lang.clone();
    let owned = app.state::<ServerState>().is_owned();
    let saved = app.state::<CfgState>().snapshot().connections;
    let active: std::collections::HashMap<String, crate::state::ConnInfo> =
        app.state::<ConnState>().snapshot();
    let discovered = app.state::<DiscoverState>().snapshot();

    // 前 9 个已保存连接的位置→key 映射，用于快捷键后缀标签。
    let mut pos_by_key: std::collections::HashMap<String, u32> = std::collections::HashMap::new();
    for (i, c) in saved.iter().enumerate() {
        if i >= 9 { break; }
        let k = crate::config::key_for(&c.host, c.port);
        pos_by_key.entry(k).or_insert((i + 1) as u32);
    }
    let shortcut_suffix = |key: &str| -> String {
        pos_by_key.get(key).map(|p| format!("  {}", conn_hotkey_label(*p))).unwrap_or_default()
    };

    let show = MenuItem::with_id(app, "show", "Show Niuniu", true, None::<&str>)?;
    let reload = MenuItem::with_id(app, "reload", "刷新页面", true, None::<&str>)?;
    let rebuild = MenuItem::with_id(app, "rebuild", "重建窗口", true, None::<&str>)?;
    let restart = MenuItem::with_id(app, "restart", "Restart Server", owned, None::<&str>)?;
    let sep1 = PredefinedMenuItem::separator(app)?;

    let ai = MenuItem::with_id(app, "ai", format!("{}…", i18n::ai_title(&lang)), true, None::<&str>)?;
    let runners = MenuItem::with_id(app, "runners", format!("{}…", i18n::t_runners(&lang)), true, None::<&str>)?;
    let picker = MenuItem::with_id(
        app, "picker",
        format!("连接其他节点 / 管理连接…  {}", conn_hotkey_label(0)),
        true, None::<&str>,
    )?;
    let mobile = MenuItem::with_id(app, "mobile", "移动接入…", true, None::<&str>)?;
    let sep2 = PredefinedMenuItem::separator(app)?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
    let discovered_header = MenuItem::with_id(app, "discovered-header", "Discovered", false, None::<&str>)?;

    let sep_nodes = PredefinedMenuItem::separator(app)?;
    let sep_tail = PredefinedMenuItem::separator(app)?;

    let mut items: Vec<&dyn IsMenuItem<tauri::Wry>> =
        vec![&show, &reload, &rebuild, &restart, &sep1, &ai, &runners, &sep_nodes];

    // 活跃连接子菜单
    let mut subs: Vec<Submenu<tauri::Wry>> = Vec::new();
    let mut active_keys: Vec<String> = active.keys().cloned().collect();
    active_keys.sort();
    for key in &active_keys {
        if let Some(info) = active.get(key) {
            subs.push(connection_submenu(app, key, info, shortcut_suffix(key))?);
        }
    }
    items.extend(subs.iter().map(|s| s as &dyn IsMenuItem<tauri::Wry>));

    // 未激活的已保存连接（点击直连）
    let mut saved_items: Vec<MenuItem<tauri::Wry>> = Vec::new();
    for c in &saved {
        let key = crate::config::key_for(&c.host, c.port);
        if active.contains_key(&key) { continue; }
        let label = format!("{} ({}){}", c.name, key, shortcut_suffix(&key));
        saved_items.push(MenuItem::with_id(
            app, format!("saved-conn:{}", c.id), label, true, None::<&str>,
        )?);
    }
    items.extend(saved_items.iter().map(|m| m as &dyn IsMenuItem<tauri::Wry>));

    // mDNS 发现的实例
    let mut disc_items: Vec<MenuItem<tauri::Wry>> = Vec::new();
    if !discovered.is_empty() {
        items.push(&discovered_header);
        for inst in &discovered {
            let name = if inst.hostname.is_empty() { inst.host.clone() } else { inst.hostname.clone() };
            let label = format!("  {} ({})", name, inst.host);
            disc_items.push(MenuItem::with_id(
                app, format!("discovered-conn:{}|{}", inst.host, inst.port), label, true, None::<&str>,
            )?);
        }
        items.extend(disc_items.iter().map(|m| m as &dyn IsMenuItem<tauri::Wry>));
    }

    items.push(&picker);
    items.push(&sep_tail);
    items.push(&mobile);
    items.push(&sep2);
    items.push(&quit);

    Menu::with_items(app, &items)
}

fn connection_submenu(
    app: &AppHandle,
    key: &str,
    info: &crate::state::ConnInfo,
    shortcut: String,
) -> tauri::Result<Submenu<tauri::Wry>> {
    let focus = MenuItem::with_id(app, format!("conn-focus:{key}"), "聚焦", true, None::<&str>)?;
    let reload = MenuItem::with_id(app, format!("conn-reload:{key}"), "刷新页面", true, None::<&str>)?;
    let rebuild = MenuItem::with_id(app, format!("conn-rebuild:{key}"), "重建窗口", true, None::<&str>)?;
    let sep = PredefinedMenuItem::separator(app)?;
    let close = MenuItem::with_id(app, format!("conn-close:{key}"), "关闭连接", true, None::<&str>)?;
    let items: Vec<&dyn tauri::menu::IsMenuItem<tauri::Wry>> = vec![&focus, &reload, &rebuild, &sep, &close];
    Submenu::with_items(
        app,
        format!("● {} ({}:{}){}", info.name, info.host, info.port, shortcut),
        true,
        &items,
    )
}

fn reload_main(app: &AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.eval("location.reload(true)");
    }
}

/// 重建托盘（连接增删/connect 后调用）。托盘已存在则只换菜单。
pub fn rebuild_tray(app: &AppHandle) {
    if let Some(tray) = app.tray_by_id("main-tray") {
        if let Ok(menu) = build_menu(app) {
            let _ = tray.set_menu(Some(menu));
        }
        let lang = app.state::<AppMeta>().lang.clone();
        let n = app.state::<ConnState>().snapshot().len();
        let tip = if n > 0 {
            format!("{} — {} active", i18n::app_name(&lang), n)
        } else {
            i18n::app_name(&lang).to_string()
        };
        let _ = tray.set_tooltip(Some(&tip));
    }
}
