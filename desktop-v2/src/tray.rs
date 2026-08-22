//! 系统托盘：图标 + 菜单（显示/刷新/重建/重启 server、AI 直达、连接管理、退出），
//! 并随连接变化重建（rebuild_tray）。左键单击 → 显示主窗口。

use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager};

use crate::i18n;
use crate::state::{AppMeta, ConnState, ServerState};

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
        .show_menu_on_left_click(true)
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

/// 构建当前菜单（本地块 + 远端连接子菜单 + AI/picker + 退出）。
pub fn build_menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    use tauri::menu::IsMenuItem;

    let lang = app.state::<AppMeta>().lang.clone();
    let owned = app.state::<ServerState>().is_owned();

    let show = MenuItem::with_id(app, "show", "Show Niuniu", true, None::<&str>)?;
    let reload = MenuItem::with_id(app, "reload", "刷新页面", true, None::<&str>)?;
    let rebuild = MenuItem::with_id(app, "rebuild", "重建窗口", true, None::<&str>)?;
    let restart = MenuItem::with_id(app, "restart", "Restart Server", owned, None::<&str>)?;
    let sep1 = PredefinedMenuItem::separator(app)?;

    let ai = MenuItem::with_id(app, "ai", format!("{}…", i18n::ai_title(&lang)), true, None::<&str>)?;
    let runners = MenuItem::with_id(app, "runners", format!("{}…", i18n::runners_title(&lang)), true, None::<&str>)?;
    let picker = MenuItem::with_id(app, "picker", "连接其他节点 / 管理连接…", true, None::<&str>)?;
    let sep2 = PredefinedMenuItem::separator(app)?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;

    // 远端连接子菜单
    let conns = app.state::<ConnState>().snapshot();
    let sep3 = PredefinedMenuItem::separator(app)?;
    let mut items: Vec<&dyn IsMenuItem<tauri::Wry>> =
        vec![&show, &reload, &rebuild, &restart, &sep1, &ai, &runners, &picker];
    let mut subs: Vec<Submenu<tauri::Wry>> = Vec::new();
    if !conns.is_empty() {
        items.push(&sep3);
        let mut keys: Vec<String> = conns.keys().cloned().collect();
        keys.sort();
        for key in keys {
            if let Some(info) = conns.get(&key) {
                subs.push(connection_submenu(app, &key, info)?);
            }
        }
        items.extend(subs.iter().map(|s| s as &dyn IsMenuItem<tauri::Wry>));
    }
    items.push(&sep2);
    items.push(&quit);

    Menu::with_items(app, &items)
}

fn connection_submenu(
    app: &AppHandle,
    key: &str,
    info: &crate::state::ConnInfo,
) -> tauri::Result<Submenu<tauri::Wry>> {
    let focus = MenuItem::with_id(
        app,
        format!("conn-focus:{key}"),
        "聚焦",
        true,
        None::<&str>,
    )?;
    let reload = MenuItem::with_id(
        app,
        format!("conn-reload:{key}"),
        "刷新页面",
        true,
        None::<&str>,
    )?;
    let close = MenuItem::with_id(
        app,
        format!("conn-close:{key}"),
        "关闭连接",
        true,
        None::<&str>,
    )?;
    let items: Vec<&dyn tauri::menu::IsMenuItem<tauri::Wry>> =
        vec![&focus, &reload, &close];
    Submenu::with_items(
        app,
        format!("● {} ({}:{})", info.name, info.host, info.port),
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
