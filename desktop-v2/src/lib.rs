//! 牛牛桌面版 v2（Tauri）—— 模块聚合 + 启动装配 + 后台 boot 序列。
//! 壳层职责与 Wails 版 cmd/personal 对应；Go server 作为子进程保留。

mod ai;
mod commands;
mod config;
mod discovery;
mod hotkeys;
mod i18n;
mod sse;
mod server;
mod state;
mod tray;
mod webview2;
mod windows;

use tauri::Manager;

use state::{AppMeta, CfgState, DiscoverState, ServerState};

pub fn run() {
    let flags = parse_flags();

    // 必须先于任何 WebView2 环境创建注入浏览器参数（UA 伪装 / 禁自动化检测 /
    // 后台渲染），对应 Wails 版在 app 构建前 setenv 的做法。
    webview2::apply_webview2_env_args();

    // WebView2 Runtime 预检（Windows）：LTSC/Server 无该组件时 Tauri 会静默
    // 崩溃，先弹原生对话框提示安装再退出。
    #[cfg(windows)]
    if webview2::find_webview2_version().is_none() {
        eprintln!("WebView2 Runtime not detected; prompting to install");
        webview2::show_missing_dialog();
        std::process::exit(1);
    }

    let data_dir = config::data_dir();
    let lang = i18n::detect_lang().to_string();
    let cfg = config::load();
    // 升级清理：抹掉旧版遗留的明文 relay 密码
    config::scrub_legacy_relay_password();

    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            // 第二次启动：抬升已存在实例的主窗口
            if let Some(win) = app.get_webview_window("main") {
                let _ = win.show();
                let _ = win.unminimize();
            }
        }))
        .plugin(hotkeys::plugin())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_clipboard_manager::init())
        .manage(AppMeta { data_dir, lang, flags })
        .manage(CfgState::new(cfg))
        .manage(ServerState::new())
        .manage(state::ConnState::new())
        .manage(state::AiState::new())
        .manage(state::DiscoverState::new())
        .manage(state::RebuildingState::new())
        .setup(|app| {
            let handle = app.handle().clone();
            let lang = app.state::<AppMeta>().lang.clone();
            let flags = app.state::<AppMeta>().flags.clone();

            // 主窗口（loading splash，随后导航到本地 server）
            let main_win = windows::create_main_window(&handle, &lang, flags.start_minimized)?;
            windows::register_close_to_tray(&main_win, &handle);
            // picker / ai-hub / runners 全部隐藏创建，按需打开（方案 A: 绝不抢首启）
            let picker = windows::create_picker_window(&handle, &lang)?;
            windows::register_close_to_tray(&picker, &handle);
            let hub = windows::create_ai_hub_window(&handle, &lang)?;
            windows::register_close_to_tray(&hub, &handle);
            let runners = windows::create_runners_window(&handle, &lang)?;
            windows::register_close_to_tray(&runners, &handle);

            // 托盘
            let _tray = tray::build_tray(&handle)?;

            // 全局快捷键（按 config 注册）
            let cfg = app.state::<CfgState>().snapshot();
            hotkeys::apply_hotkeys(&handle, &cfg);

            // mDNS 发现（失败降级为空列表）
            match discovery::Discovery::start() {
                Ok(d) => *app.state::<DiscoverState>().inner.lock().unwrap() = Some(d),
                Err(e) => eprintln!("mDNS discovery disabled: {e}"),
            }

            // 后台 boot：探测/复用/自产 server → 主窗口导航 → SSE
            let app2 = handle.clone();
            std::thread::spawn(move || boot(&app2));
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::get_connections,
            commands::get_active_connections,
            commands::get_discovered_instances,
            commands::add_connection,
            commands::remove_connection,
            commands::move_connection,
            commands::connect_by_id,
            commands::connect_from_picker,
            commands::connect_to_address,
            commands::connect_to_address_from_picker,
            commands::refresh_tray_menu,
            commands::get_ai_hotkey,
            commands::get_ai_services,
            commands::activate_ai_service,
            commands::set_ai_stage_rect,
            commands::set_ai_overlay_open,
            commands::reload_active_ai_service,
            commands::add_ai_service,
            commands::remove_ai_service,
            commands::set_default_ai_service,
            commands::get_ai_prompts,
            commands::add_ai_prompt,
            commands::remove_ai_prompt,
            commands::show_local_window,
            commands::copy_to_clipboard,
            commands::open_service_in_browser,
        ])
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(|app_handle, event| {
            if let tauri::RunEvent::ExitRequested { .. } = event {
                shutdown(app_handle);
            }
        });
}

/// 后台启动序列：探测已有 server → 复用或自产 → 导航主窗口 → 启动 SSE。
fn boot(app: &tauri::AppHandle) {
    let flags = app.state::<AppMeta>().flags.clone();
    let data_dir = app.state::<AppMeta>().data_dir.clone();

    // dev-url 模式：跳过探测/spawn，直接打开指定 URL（对应 Wails --dev-url）
    if !flags.dev_url.is_empty() {
        eprintln!("dev-url mode: {}", flags.dev_url);
        let st = app.state::<ServerState>();
        st.lock().addr = Some(dev_url_host_port(&flags.dev_url));
        commands::navigate_main_to_server(app);
        return;
    }

    // 复用已运行的 server（config 端口 → 默认端口）
    if let Some(rs) = server::probe_running_server() {
        eprintln!("reusing existing server at {} (source {})", rs.addr, rs.source);
        {
            let st = app.state::<ServerState>();
            let mut s = st.lock();
            s.addr = Some(rs.addr);
            s.reused = true;
        }
        commands::navigate_main_to_server(app);
        tray::rebuild_tray(app);
        sse::start(app.clone());
        return;
    }

    // 自产嵌入式 server
    match server::spawn(&data_dir) {
        Ok(h) => {
            let addr = h.addr.clone();
            eprintln!("embedded server ready at {addr}");
            {
                let st = app.state::<ServerState>();
                let mut s = st.lock();
                s.handle = Some(std::sync::Arc::new(h));
                s.addr = Some(addr);
            }
            commands::navigate_main_to_server(app);
            tray::rebuild_tray(app);
            sse::start(app.clone());
        }
        Err(e) => {
            eprintln!("server spawn failed: {e}");
            windows::show_startup_error(app, &e);
        }
    }
}

/// 退出：优雅关闭自产 server（stdin EOF 触发 heartbeat 退出）。
fn shutdown(app: &tauri::AppHandle) {
    let handle = {
        let st = app.state::<ServerState>();
        let mut s = st.lock();
        s.shutting_down = true;
        s.handle.take()
    };
    if let Some(h) = handle {
        server::shutdown(&h);
    }
    if let Some(discover) = app.try_state::<DiscoverState>() {
        if let Some(d) = discover.inner.lock().unwrap().take() {
            d.stop();
        }
    }
}

fn parse_flags() -> state::Flags {
    let args: Vec<String> = std::env::args().collect();
    let mut f = state::Flags::default();
    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--dev-url" => {
                i += 1;
                if i < args.len() {
                    f.dev_url = args[i].clone();
                }
            }
            "--autostart" => f.autostart = true,
            "--minimized" => f.start_minimized = true,
            _ => {}
        }
        i += 1;
    }
    f
}

/// 从 URL 提取 "host:port"（dev-url 用）。
fn dev_url_host_port(u: &str) -> String {
    let s = u.strip_prefix("http://").or_else(|| u.strip_prefix("https://")).unwrap_or(u);
    let end = s.find(['/', '?', '#']).unwrap_or(s.len());
    s[..end].to_string()
}
