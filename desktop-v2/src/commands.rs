//! Tauri 命令（内嵌前端经 window.__TAURI__.core.invoke 调用）与供托盘/快捷键/
//! SSE 复用的窗口动作。功能面与 Wails 版 connwin.go / aiwin.go / hotkeywin.go
//! 对齐。

use std::time::{SystemTime, UNIX_EPOCH};

use serde::Serialize;
use tauri::Manager;

use crate::ai;
use crate::ai_embed;
use crate::config;
use crate::i18n;
use crate::server;
use crate::state::{AiState, AppMeta, CfgState, ConnInfo, ConnState, DiscoverState, ServerState};
use crate::windows;

// ─── 连接管理 ──────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize)]
pub struct ConnView {
    pub id: String,
    pub name: String,
    pub host: String,
    pub port: u16,
    pub is_default: bool,
}

fn conn_view(c: &config::Connection) -> ConnView {
    ConnView {
        id: c.id.clone(),
        name: c.name.clone(),
        host: c.host.clone(),
        port: c.port,
        is_default: c.is_default,
    }
}

fn new_id(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    format!("{prefix}-{nanos}")
}

fn save_cfg(app: &tauri::AppHandle) {
    let cfg = app.state::<CfgState>().snapshot();
    config::save(&cfg);
}

/// 已保存连接列表。
#[tauri::command]
pub fn get_connections(app: tauri::AppHandle) -> Vec<ConnView> {
    app.state::<CfgState>()
        .snapshot()
        .connections
        .iter()
        .map(conn_view)
        .collect()
}

/// 活跃连接 key 列表（"host:port"）。
#[tauri::command]
pub fn get_active_connections(app: tauri::AppHandle) -> Vec<String> {
    app.state::<ConnState>().snapshot().into_keys().collect()
}

/// mDNS 发现实例。
#[tauri::command]
pub fn get_discovered_instances(app: tauri::AppHandle) -> Vec<crate::discovery::Instance> {
    app.state::<DiscoverState>().snapshot()
}

/// 新增/保存连接。port=0 表示按 URL/scheme 默认。
#[tauri::command]
pub fn add_connection(
    app: tauri::AppHandle,
    name: String,
    host: String,
    port: u16,
) -> Result<ConnView, String> {
    let host = host.trim().to_string();
    if host.is_empty() {
        return Err("host is required".into());
    }
    let id = new_id("conn");
    let display = if name.trim().is_empty() {
        host.clone()
    } else {
        name.trim().to_string()
    };
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        if cfg
            .connections
            .iter()
            .any(|c| c.host == host && c.port == port)
        {
            return Err(format!("connection {host}:{port} already exists"));
        }
        let is_default = cfg.connections.is_empty();
        cfg.connections.push(config::Connection {
            id: id.clone(),
            name: display,
            host,
            port,
            is_default,
            created_at: None,
        });
    }
    save_cfg(&app);
    crate::tray::rebuild_tray(&app);
    Ok(conn_view(
        &app.state::<CfgState>()
            .snapshot()
            .connections
            .iter()
            .find(|c| c.id == id)
            .unwrap(),
    ))
}

/// 删除连接（同时关闭其已打开的窗口，对应 v1 connwin.go RemoveConnection）。
#[tauri::command]
pub fn remove_connection(app: tauri::AppHandle, id: String) -> Result<(), String> {
    let mut removed_key: Option<String> = None;
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        if let Some(pos) = cfg.connections.iter().position(|c| c.id == id) {
            let c = cfg.connections.remove(pos);
            removed_key = Some(config::key_for(&c.host, c.port));
        }
    }
    if let Some(key) = removed_key {
        // 关闭已打开的窗口（关闭钩子会清理 ConnState 并 rebuild tray）。
        let label = config::window_label_for_key(&key);
        if let Some(win) = app.get_webview_window(&label) {
            let _ = win.close();
        } else {
            // 没开窗口：直接落盘 + 重建。
            save_cfg(&app);
            app.state::<ConnState>().remove(&key);
            crate::tray::rebuild_tray(&app);
        }
    }
    Ok(())
}

/// 调整连接在列表中的顺序（决定 Ctrl/Cmd+Shift+1..9 快捷位）。
#[tauri::command]
pub fn move_connection(app: tauri::AppHandle, id: String, delta: i32) -> Result<(), String> {
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        let len = cfg.connections.len();
        if let Some(pos) = cfg.connections.iter().position(|c| c.id == id) {
            let np = (pos as i32 + delta).clamp(0, len as i32 - 1) as usize;
            if np != pos {
                let c = cfg.connections.remove(pos);
                cfg.connections.insert(np, c);
            }
        }
    }
    save_cfg(&app);
    crate::tray::rebuild_tray(&app);
    Ok(())
}

/// 设置默认连接（对应 v1 SetDefaultConnection）。
#[tauri::command]
pub fn set_default_connection(app: tauri::AppHandle, id: String) -> Result<(), String> {
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        for c in cfg.connections.iter_mut() {
            c.is_default = c.id == id;
        }
    }
    save_cfg(&app);
    Ok(())
}

/// 按 ID 连接。
pub fn connect_by_id_internal(app: &tauri::AppHandle, id: &str) -> Result<bool, String> {
    let cfg = app.state::<CfgState>().snapshot();
    let Some(conn) = cfg.connections.iter().find(|c| c.id == id) else {
        return Err("connection not found".into());
    };
    connect_internal(
        app,
        &config::key_for(&conn.host, conn.port),
        &ConnInfo {
            name: conn.name.clone(),
            host: conn.host.clone(),
            port: conn.port,
        },
    )?;
    Ok(true)
}

#[tauri::command]
pub fn connect_by_id(app: tauri::AppHandle, id: String) -> Result<bool, String> {
    connect_by_id_internal(&app, &id)
}

#[tauri::command]
pub fn connect_from_picker(app: tauri::AppHandle, id: String) -> Result<bool, String> {
    let ok = connect_by_id_internal(&app, &id)?;
    if ok {
        hide_picker(&app);
    }
    Ok(ok)
}

/// 直连 host:port；若与某已保存连接匹配则复用其名称（对应 v1 ConnectToAddress）。
pub fn connect_to_address_internal(
    app: &tauri::AppHandle,
    host: &str,
    port: u16,
) -> Result<bool, String> {
    let name = app
        .state::<CfgState>()
        .snapshot()
        .connections
        .iter()
        .find(|c| c.host == host && c.port == port)
        .map(|c| c.name.clone())
        .unwrap_or_else(|| host.to_string());
    connect_internal(
        app,
        &config::key_for(host, port),
        &ConnInfo {
            name,
            host: host.to_string(),
            port,
        },
    )?;
    Ok(true)
}

#[tauri::command]
pub fn connect_to_address(app: tauri::AppHandle, host: String, port: u16) -> Result<bool, String> {
    connect_to_address_internal(&app, &host, port)
}

#[tauri::command]
pub fn connect_to_address_from_picker(
    app: tauri::AppHandle,
    host: String,
    port: u16,
) -> Result<bool, String> {
    let ok = connect_to_address_internal(&app, &host, port)?;
    if ok {
        hide_picker(&app);
    }
    Ok(ok)
}

fn connect_internal(app: &tauri::AppHandle, key: &str, info: &ConnInfo) -> Result<(), String> {
    let lang = app.state::<AppMeta>().lang.clone();
    app.state::<ConnState>()
        .insert(key.to_string(), info.clone());
    windows::open_connection_window(app, &lang, key, info).map_err(|e| e.to_string())?;
    crate::tray::rebuild_tray(app);
    Ok(())
}

fn hide_picker(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("picker") {
        let _ = win.hide();
    }
}

/// 托盘菜单重建（picker 前端手动刷新用）。
#[tauri::command]
pub fn refresh_tray_menu(app: tauri::AppHandle) {
    crate::tray::rebuild_tray(&app);
}

// ─── 窗口动作（托盘 / 快捷键 / SSE 复用） ──────────────────────────────────

pub fn show_main_window(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.show();
        let _ = win.unminimize();
        if !cfg!(target_os = "macos") {
            let _ = win.set_focus();
        }
    }
}

pub fn toggle_main_window(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        if win.is_visible().unwrap_or(false) {
            let _ = win.hide();
        } else {
            show_main_window(app);
        }
    }
}

/// 重新显示 hub 并强制 WebView2 重组帧。hide 过的窗口 WebView2 会挂起合成，
/// 仅 show 回来内容区是空白的，要等一次移动/缩放才重绘（ai_embed_windows
/// stash 注释记载的同款坑，这次出现在 hub 的 X 关闭→热键重开路径：顶栏正常、
/// 停靠的服务页空白，挪一下窗口才显示）。±1px 尺寸抖动制造非零 WM_SIZE：
/// hub 自身重绘，同时经 Resized 事件触发 reposition_active_ai_service，停靠
/// 的服务窗口跟着重贴。经 dispatch_main 排在 reveal 任务之后执行，保证补绘
/// 是最后一笔。
fn reshow_hub(app: &tauri::AppHandle, hub: &tauri::WebviewWindow) {
    crate::boot_log("[ai-diag] reshow_hub: show+focus, nudge queued");
    let _ = hub.show();
    let _ = hub.set_focus();
    let hub2 = hub.clone();
    crate::webview_gate::dispatch_main(app, move || {
        if let Ok(size) = hub2.inner_size() {
            let (w, h) = (size.width.max(2), size.height.max(2));
            let _ = hub2.set_size(tauri::PhysicalSize::new(w, h - 1));
            let _ = hub2.set_size(tauri::PhysicalSize::new(w, h));
        }
    });
}

/// AI 直达：显示/隐藏 hub（并联动服务窗口可见性）。首次打开经独立线程建窗
/// （主线程同步建 webview 会死锁，见 spawn_aux_window）。
pub fn toggle_ai_window(app: &tauri::AppHandle) {
    match app.get_webview_window("ai-hub") {
        Some(win) => {
            let visible = win.is_visible().unwrap_or(false);
            crate::boot_log(format!("[ai-diag] toggle_ai_window: visible={visible}"));
            if visible {
                let _ = win.hide();
                update_ai_service_visibility_at(app, false);
            } else {
                reshow_hub(app, &win);
                update_ai_service_visibility_at(app, true);
            }
        }
        None => {
            let lang = app.state::<AppMeta>().lang.clone();
            spawn_aux_window(app, move |a| windows::create_ai_hub_window(a, &lang));
        }
    }
}

/// AI 直达：抬升 hub（只显示，不 toggle——对应 v1 OpenAIWindow）。
/// 用于 SSE `open_ai_window` 信号和托盘菜单：已可见时聚焦而非隐藏。
pub fn open_ai_window(app: &tauri::AppHandle) {
    match app.get_webview_window("ai-hub") {
        Some(win) => {
            // 抬升语义：目标态恒为可见——显式传 true，不读 show 前的旧状态。
            crate::boot_log(format!(
                "[ai-diag] open_ai_window: visible={}",
                win.is_visible().unwrap_or(false)
            ));
            if win.is_visible().unwrap_or(false) {
                let _ = win.set_focus();
            } else {
                reshow_hub(app, &win);
            }
            update_ai_service_visibility_at(app, true);
        }
        None => {
            let lang = app.state::<AppMeta>().lang.clone();
            spawn_aux_window(app, move |a| windows::create_ai_hub_window(a, &lang));
        }
    }
}

/// 确保 aux 窗口（picker/ai-hub/runners）存在并在独立线程创建+显示。
/// webview 创建绝不能在主线程同步执行：快捷键 handler 在主线程 WM_HOTKEY
/// wndproc 里跑、托盘菜单在主线程事件回调里跑、同步 command 也在主线程跑，
/// build() 里 WebView2 的异步初始化需要消息泵，嵌在回调里会挂死整个事件
/// 循环（表现为「AI 直达一开就卡死」）。线程内 build() 经事件循环 proxy
/// 派发，主线程空闲时完成创建，show/focus 消息排队按序执行。
fn spawn_aux_window(
    app: &tauri::AppHandle,
    create: impl FnOnce(&tauri::AppHandle) -> tauri::Result<tauri::WebviewWindow> + Send + 'static,
) {
    let app = app.clone();
    std::thread::spawn(move || {
        if let Ok(w) = create(&app) {
            // ai-hub 在 create_ai_hub_window 里自带关闭钩子（X = 隐藏 + stash
            // 停靠的服务窗口），再叠通用 close-to-tray 会双 prevent_close。
            if w.label() != "ai-hub" {
                windows::register_close_to_tray(&w, &app);
            }
            // show/focus 经 dispatch_main：排队的主线程窗口任务可能落在另一
            // 创建的 WebView2 重入泵里被内联执行。
            crate::webview_gate::dispatch_main(&app, move || {
                let _ = w.show();
                let _ = w.set_focus();
            });
        }
    });
}

pub fn open_picker(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("picker") {
        let _ = win.show();
        let _ = win.set_focus();
        return;
    }
    let lang = app.state::<AppMeta>().lang.clone();
    spawn_aux_window(app, move |a| windows::create_picker_window(a, &lang));
}

/// picker toggle（Ctrl/Cmd+Shift+0）：可见则隐藏，否则打开。对应 v1 TogglePickerWindow。
pub fn toggle_picker(app: &tauri::AppHandle) {
    match app.get_webview_window("picker") {
        Some(win) => {
            if win.is_visible().unwrap_or(false) {
                let _ = win.hide();
            } else {
                let _ = win.show();
                let _ = win.set_focus();
            }
        }
        None => {
            let lang = app.state::<AppMeta>().lang.clone();
            spawn_aux_window(app, move |a| windows::create_picker_window(a, &lang));
        }
    }
}

/// 位置快捷键：打开已保存连接列表中第 pos 个（1-based）。越界静默 no-op。
/// 每次按键实时快照列表（对应 v1 connhotkey.go connectByPosition）。
pub fn connect_by_position(app: &tauri::AppHandle, pos: u32) {
    let conns = app.state::<CfgState>().snapshot().connections;
    let Some(conn) = conns.get(pos as usize - 1) else {
        return;
    };
    let id = conn.id.clone();
    let _ = connect_by_id_internal(app, &id);
}

/// 移动接入：主窗口导航到 Settings → 移动接入（对应 Wails tray「移动接入…」）。
pub fn open_mobile_access(app: &tauri::AppHandle) {
    let Some(addr) = app.state::<ServerState>().addr() else {
        return;
    };
    let cfg = app.state::<CfgState>().snapshot();
    let url = windows::with_hotkey_hash(&format!("http://{addr}/settings?tab=mobile-access"), &cfg);
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.show();
        let _ = win.unminimize();
        if let Ok(u) = url::Url::parse(&url) {
            let _ = win.navigate(u);
        }
    }
}

pub fn open_runners(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("runners") {
        let _ = win.show();
        let _ = win.set_focus();
        return;
    }
    let lang = app.state::<AppMeta>().lang.clone();
    spawn_aux_window(app, move |a| windows::create_runners_window(a, &lang));
}

pub fn focus_connection(app: &tauri::AppHandle, key: &str) {
    let label = config::window_label_for_key(key);
    if let Some(win) = app.get_webview_window(&label) {
        let _ = win.show();
        if !cfg!(target_os = "macos") {
            let _ = win.set_focus();
        }
    }
}

pub fn close_connection(app: &tauri::AppHandle, key: &str) {
    let label = config::window_label_for_key(key);
    if let Some(win) = app.get_webview_window(&label) {
        let _ = win.close();
    }
    app.state::<ConnState>().remove(key);
    crate::tray::rebuild_tray(app);
}

/// 重启自产 server：停止监听 → 关旧 → spawn 新 → 主窗口导航 → 重建托盘。
pub fn restart_server(app: &tauri::AppHandle) -> Result<(), String> {
    {
        let st = app.state::<ServerState>();
        let mut s = st.lock();
        if s.shutting_down || s.restarting || s.handle.is_none() || s.reused {
            return Ok(());
        }
        s.restarting = true;
    }
    let data_dir = app.state::<AppMeta>().data_dir.clone();
    let handle = {
        let st = app.state::<ServerState>();
        let s = st.lock();
        s.handle.clone()
    };
    if let Some(h) = handle {
        server::shutdown(&h);
    }
    let new_h = match server::spawn(&data_dir) {
        Ok(h) => h,
        Err(e) => {
            {
                let st = app.state::<ServerState>();
                st.lock().restarting = false;
            }
            return Err(e);
        }
    };
    {
        let st = app.state::<ServerState>();
        let mut s = st.lock();
        s.handle = Some(std::sync::Arc::new(new_h));
        s.addr = Some(s.handle.as_ref().unwrap().addr.clone());
        s.restarting = false;
    }
    navigate_main_to_server(app);
    crate::tray::rebuild_tray(app);
    Ok(())
}

/// 主窗口导航到本地 server（带快捷键 hash）。
pub fn navigate_main_to_server(app: &tauri::AppHandle) {
    let Some(addr) = app.state::<ServerState>().addr() else {
        crate::boot_log("navigate: no addr set, skipping");
        return;
    };
    let cfg = app.state::<CfgState>().snapshot();
    let url = windows::with_hotkey_hash(&format!("http://{addr}/"), &cfg);
    crate::boot_log(format!("navigate: target url = {url}"));
    match app.get_webview_window("main") {
        Some(win) => match url::Url::parse(&url) {
            Ok(u) => match win.navigate(u) {
                Ok(_) => crate::boot_log("navigate: win.navigate Ok"),
                Err(e) => crate::boot_log(format!("navigate: win.navigate ERR {e}")),
            },
            Err(e) => crate::boot_log(format!("navigate: url parse ERR {e}")),
        },
        None => crate::boot_log("navigate: get_webview_window('main') = None"),
    }
}

// ─── AI 直达 ──────────────────────────────────────────────────────────────

/// stage 停靠：只把「当前应在台上」的窗口按 stage 矩形重贴（hub 拖动/缩放后
/// 由 set_ai_stage_rect / 前端 ResizeObserver 触发）。stash 中的窗口（加载中/
/// 覆盖层下）绝不能贴回 stage——它们停留在屏幕外，拖动中贴回会在 hub 旧位置
/// 闪现（用户实测回归）。对应 v1 SetAIStageRect 只动 revealTargetLocked。
/// 非 Windows 无嵌入路径，no-op。Win32 HWND 操作经 run_on_main_thread 派发。
fn position_service_window(app: &tauri::AppHandle) {
    if !ai_embed::AI_EMBED_SUPPORTED {
        return;
    }
    let stage = app.state::<AiState>().lock().stage;
    let hub = app.get_webview_window("ai-hub");
    // ⚠️ getter 往返必须在拿 AiState 之前完成：is_visible 是到主线程的同步
    // 往返，持 AiState 调它会 ABBA 死锁——主线程的 on_page_load 处理也要拿
    // AiState（2026-09-15 22:24 挂死实证：服务页加载事件与建窗线程在此互等，
    // watchdog 记录主线程停泵 10s+）。铁律：任何 managed-state 锁的临界区内
    // 不得调用窗口 getter/setter。
    let hub_visible = hub_visible(app);
    let win = {
        let st = app.state::<AiState>();
        let ai = st.lock();
        match (&hub, &ai.active) {
            (Some(_), Some(active)) if hub_visible && !ai.overlay_open => ai
                .service_windows
                .get(&format!("ai-service-{active}"))
                .cloned(),
            _ => None,
        }
    };
    let (Some(hub), Some(win)) = (hub, win) else {
        return;
    };
    crate::webview_gate::dispatch_main(&app, move || {
        ai_embed::position_window(&hub, &win, stage);
    });
}

/// 服务窗口可见性 = hub 可见性意图 && 无覆盖层 && 是当前激活服务。
/// hub 可见性必须由调用方显式传入意图：show()/hide() 都是异步派发，紧跟其后的
/// is_visible() 读到的是旧状态——重开 hub 时读到 false 导致所有服务窗被再次
/// stash（表现为「关闭再打开不显示网页，挪一下窗口才出现」，2026-09-15 实测）。
///
/// Windows 嵌入路径：激活服务 reveal（上 stage + 置顶 + 焦点），其余 stash
/// （挪屏幕外但保持显示——SW_HIDE 会让 WebView2 挂起合成，再显示回来是空白，
/// v1 aiembed_windows.go 实测踩坑点）。非 Windows 回退普通 show/hide。
pub fn update_ai_service_visibility(app: &tauri::AppHandle) {
    // hub 未发生显隐变化的场景（覆盖层开关等）：实时读当前状态是新鲜的。
    let hub_visible = app
        .get_webview_window("ai-hub")
        .map(|w| w.is_visible().unwrap_or(false))
        .unwrap_or(false);
    update_ai_service_visibility_at(app, hub_visible);
}

/// 显式意图版本：紧邻 show()/hide() 的调用必须用这个，传目标态而非读旧态。
pub fn update_ai_service_visibility_at(app: &tauri::AppHandle, hub_visible: bool) {
    let hub = app.get_webview_window("ai-hub");
    let st = app.state::<AiState>();
    let ai = st.lock();
    let show_any = hub_visible && !ai.overlay_open;
    let active = ai.active.clone();
    let stage = ai.stage;
    crate::boot_log(format!(
        "[ai-diag] svc_visibility: intent_hub_visible={hub_visible} overlay={} active={:?} pooled={} show_any={show_any}",
        ai.overlay_open,
        ai.active,
        ai.service_windows.len()
    ));
    // ⚠️ service_windows 的键是窗口 label（"ai-service-{id}"），而 ai.active 存
    // 裸服务 id（"deepseek"）——直接 Some(id)==active 比较永远 false，reveal
    // 分支成为死代码、所有服务窗被错误 stash（hub 重开空白 + 挪窗口才显示的
    // 根因）。比较必须统一到 label 维度。
    let active_label = active
        .as_deref()
        .map(|a| format!("ai-service-{a}"));
    if ai_embed::AI_EMBED_SUPPORTED {
        let Some(hub) = hub else { return };
        let windows: Vec<(String, tauri::WebviewWindow)> = ai
            .service_windows
            .iter()
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect();
        drop(ai);
        crate::webview_gate::dispatch_main(&app, move || {
            for (id, win) in windows {
                let reveal_it = show_any && active_label.as_deref() == Some(id.as_str());
                crate::boot_log(format!(
                    "[ai-diag] svc_visibility apply: {id} -> {} (active_label={:?}) stage={stage:?}",
                    if reveal_it { "reveal" } else { "stash" },
                    active_label
                ));
                if reveal_it {
                    ai_embed::reveal_over_stage(&hub, &win, stage);
                } else {
                    ai_embed::stash_offscreen(&win, stage);
                }
            }
        });
    } else {
        for (id, win) in ai.service_windows.iter() {
            if show_any && Some(id.clone()) == active {
                let _ = win.show();
            } else {
                let _ = win.hide();
            }
        }
    }
}

/// AI 直达当前生效的快捷键（组合键标签，供 ai.html 展示）。
#[tauri::command]
pub fn get_ai_hotkey(app: tauri::AppHandle) -> String {
    let cfg = app.state::<CfgState>().snapshot();
    if cfg.hotkey.toggle_ai.trim().is_empty() {
        config::default_ai_accelerator()
    } else {
        cfg.hotkey.toggle_ai.clone()
    }
}

#[tauri::command]
pub fn get_ai_services(app: tauri::AppHandle) -> Vec<ai::AIServiceView> {
    let cfg = app.state::<CfgState>().snapshot();
    ai::merge_services(&cfg.ai)
}

/// 激活结果：loaded=true 表示服务窗口已存在（LRU 池命中，立即揭示）。
#[derive(Serialize)]
pub struct AIActivateView {
    pub loaded: bool,
}

/// 激活某 AI 服务：创建/复用并 dock 服务窗口。
///
/// LRU 池命中：纯窗口操作（定位/显隐/聚焦），可同步。池未命中：新建 webview
/// 必须在独立线程——本命令是同步 command，在主线程执行，主线程 build() 会因
/// WebView2 异步初始化需要消息泵而挂死整个事件循环。active 先行置位，线程内
/// 完成创建+登记+定位+显隐；返回 loaded=false，前端保持 splash 到页面加载完。
#[tauri::command]
pub fn activate_ai_service(app: tauri::AppHandle, id: String) -> Result<AIActivateView, String> {
    let lang = app.state::<AppMeta>().lang.clone();
    let cfg = app.state::<CfgState>().snapshot();
    let Some((url, name)) = ai::find_service(&cfg.ai, &id) else {
        return Err("service not found".into());
    };
    {
        let st = app.state::<CfgState>();
        let mut cfg2 = st.lock();
        cfg2.ai.last_service_id = id.clone();
    }
    save_cfg(&app);

    let label = format!("ai-service-{id}");
    let pooled = {
        let st = app.state::<AiState>();
        let ai = st.lock();
        ai.service_windows.get(&label).cloned()
    };

    // 池命中：立即揭示（页面/登录态/滚动位置全保留）。
    if let Some(win) = pooled {
        // 关闭服务窗口 = 隐藏（不真正关闭），并从登记表移除以便下次重建
        {
            let app2 = app.clone();
            let id2 = id.clone();
            win.on_window_event(move |event| {
                if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    let _ = app2
                        .state::<AiState>()
                        .lock()
                        .service_windows
                        .remove(&format!("ai-service-{id2}"));
                }
            });
        }
        // 幂等再 own 一次：窗口可能经历过注销/复建（CloseRequested 移除后再
        // activate 走不到建窗分支的 own），确保 owner 关系在。
        if ai_embed::AI_EMBED_SUPPORTED {
            // 旧版本建的池窗口可能带默认 DWM 阴影 inset（黑边），补关一次。
            let _ = win.set_shadow(false);
            if let Some(hub) = app.get_webview_window("ai-hub") {
                let (hub2, win2) = (hub, win.clone());
                crate::webview_gate::dispatch_main(&app, move || {
                    ai_embed::dock_over_stage(&hub2, &win2);
                });
            }
        }
        position_service_window(&app);
        {
            let st = app.state::<AiState>();
            st.lock().active = Some(id);
        }
        // 服务窗口显隐交给 update_ai_service_visibility（hub 可见才显示）。
        update_ai_service_visibility(&app);
        {
            let st = app.state::<AiState>();
            if !ai_embed::AI_EMBED_SUPPORTED && hub_visible(&app) && !st.lock().overlay_open {
                let _ = win.set_focus();
            }
        }
        return Ok(AIActivateView { loaded: true });
    }

    // 新服务：独立线程建窗（主线程同步 build() 死锁）。
    {
        let st = app.state::<AiState>();
        st.lock().active = Some(id.clone());
    }
    let app2 = app.clone();
    std::thread::spawn(move || {
        let label = format!("ai-service-{id}");
        // 页面加载完成 → 通知 hub 揭幕（对应 v1 onAIServiceNavigated →
        // ExecJS window.onServiceLoaded）。v2 初版漏了这条链路，前端只能靠
        // 2.5s 兜底定时器。on_page_load 只能在 builder 上注册。
        let app4 = app2.clone();
        let id3 = id.clone();
        let Ok(parsed_url) = url::Url::parse(&url) else {
            return;
        };
        // build() 经 webview_gate 串行：并发创建会在 WebView2 重入泵里嵌套，
        // 挂死主线程（表现为同时开多窗口时整个应用卡死、页面加载不出）。
        let built = crate::webview_gate::gated_create(&app2, || {
            tauri::WebviewWindowBuilder::new(&app2, &label, tauri::WebviewUrl::External(parsed_url))
                .title(format!("{} · {}", i18n::ai_title(&lang), name))
                .decorations(false)
                // 无框窗口默认带 DWM「无装饰阴影」：tao 的 WM_NCCALCSIZE 会按
                // SM_CXSIZEFRAME+SM_CXPADDEDBORDER 把客户区四周内缩（150% DPI 下约
                // 20px），webview 填的是缩过的客户区而窗口外框是 stage 尺寸 → 网页
                // 四周等宽黑边、内容等比缩小。停靠窗口必须零 inset。
                .shadow(false)
                .visible(false)
                .data_directory(crate::config::data_dir().join("webview2"))
                .on_page_load(move |_w, payload| {
                    if let tauri::webview::PageLoadEvent::Finished = payload.event() {
                        let active = app4.state::<AiState>().lock().active.clone();
                        if active.as_deref() == Some(id3.as_str()) {
                            if let Some(hub) = app4.get_webview_window("ai-hub") {
                                let _ =
                                    hub.eval("window.onServiceLoaded && window.onServiceLoaded()");
                            }
                        }
                    }
                })
                .build()
        });
        match built {
            Ok(win) => {
                {
                    let app3 = app2.clone();
                    let id2 = id.clone();
                    win.on_window_event(move |event| {
                        if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                            api.prevent_close();
                            let _ = app3
                                .state::<AiState>()
                                .lock()
                                .service_windows
                                .remove(&format!("ai-service-{id2}"));
                        }
                    });
                }
                {
                    let st = app2.state::<AiState>();
                    st.lock().service_windows.insert(label, win.clone());
                }
                // Windows：先 own（owner=hub + 无框 + 无任务栏按钮），再按
                // stage reveal/stash。Win32 调度统一派发主线程。
                if ai_embed::AI_EMBED_SUPPORTED {
                    if let Some(hub) = app2.get_webview_window("ai-hub") {
                        let hub2 = hub;
                        let win2 = win.clone();
                        crate::webview_gate::dispatch_main(&app2, move || {
                            ai_embed::dock_over_stage(&hub2, &win2);
                        });
                    }
                }
                position_service_window(&app2);
                update_ai_service_visibility(&app2);
                {
                    let st = app2.state::<AiState>();
                    if !ai_embed::AI_EMBED_SUPPORTED
                        && hub_visible(&app2)
                        && !st.lock().overlay_open
                    {
                        let _ = win.set_focus();
                    }
                }
            }
            Err(e) => eprintln!("create ai service window {label} failed: {e}"),
        }
    });
    Ok(AIActivateView { loaded: false })
}

fn hub_visible(app: &tauri::AppHandle) -> bool {
    app.get_webview_window("ai-hub")
        .map(|w| w.is_visible().unwrap_or(false))
        .unwrap_or(false)
}

/// hub 移动/缩放跟随：只重贴当前在台上的服务窗口（reveal 状态），stash 中的
/// （加载中/覆盖层下）留在屏幕外，下次揭示时自然落到新位置——拖动中把 stash
/// 窗口拽进视野会闪出加载页。对应 v1 repositionActiveAIService。
pub fn reposition_active_ai_service(app: &tauri::AppHandle) {
    if !ai_embed::AI_EMBED_SUPPORTED {
        return;
    }
    let hub = app.get_webview_window("ai-hub");
    let Some(hub) = hub else { return };
    let hub_visible_now = hub.is_visible().unwrap_or(false);
    let (active, stage, overlay) = {
        let st = app.state::<AiState>();
        let ai = st.lock();
        (ai.active.clone(), ai.stage, ai.overlay_open)
    };
    let Some(active) = active else { return };
    // hub 隐藏或覆盖层打开时台上没有窗口，无需跟随。
    if !hub_visible_now || overlay {
        crate::boot_log(format!(
            "[ai-diag] reposition: skip (hub_visible={hub_visible_now} overlay={overlay})"
        ));
        return;
    }
    let win = {
        let st = app.state::<AiState>();
        let ai = st.lock();
        ai.service_windows
            .get(&format!("ai-service-{active}"))
            .cloned()
    };
    let Some(win) = win else {
        crate::boot_log("[ai-diag] reposition: skip (active window not pooled)");
        return;
    };
    crate::boot_log(format!(
        "[ai-diag] reposition: repositioning {active} stage={stage:?}"
    ));
    crate::webview_gate::dispatch_main(&app, move || {
        ai_embed::position_window(&hub, &win, stage);
    });
}

#[tauri::command]
pub fn set_ai_stage_rect(app: tauri::AppHandle, x: i32, y: i32, w: i32, h: i32) {
    {
        let st = app.state::<AiState>();
        let mut st = st.lock();
        st.stage = (x, y, w, h);
    }
    if ai_embed::AI_EMBED_SUPPORTED {
        // 停靠路径：激活服务按新 stage 重贴（client-origin → 屏幕坐标在嵌入层做）。
        position_service_window(&app);
    } else {
        let st = app.state::<AiState>();
        let ai = st.lock();
        if let Some(active) = &ai.active {
            if let Some(win) = ai.service_windows.get(&format!("ai-service-{active}")) {
                let _ = win.set_position(tauri::PhysicalPosition::new(x, y));
                let _ = win.set_size(tauri::PhysicalSize::new(w as u32, h as u32));
            }
        }
    }
}

#[tauri::command]
pub fn set_ai_overlay_open(app: tauri::AppHandle, open: bool) {
    {
        let st = app.state::<AiState>();
        st.lock().overlay_open = open;
    }
    update_ai_service_visibility(&app);
}

#[tauri::command]
pub fn reload_active_ai_service(app: tauri::AppHandle) {
    let st = app.state::<AiState>();
    let ai = st.lock();
    if let Some(active) = &ai.active {
        if let Some(win) = ai.service_windows.get(&format!("ai-service-{active}")) {
            let _ = win.eval("location.reload(true)");
        }
    }
}

#[tauri::command]
pub fn add_ai_service(app: tauri::AppHandle, name: String, url: String) -> Result<String, String> {
    let url = ai::normalize_service_url(&url);
    if url.is_empty() {
        return Err("url is required".into());
    }
    let name = if name.trim().is_empty() {
        url.clone()
    } else {
        name
    };
    let id = new_id("ai");
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        cfg.ai.custom_services.push(config::AIService {
            id: id.clone(),
            name,
            url,
            created_at: None,
        });
    }
    save_cfg(&app);
    Ok(id)
}

#[tauri::command]
pub fn remove_ai_service(app: tauri::AppHandle, id: String) {
    let mut changed = false;
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        if let Some(pos) = cfg.ai.custom_services.iter().position(|s| s.id == id) {
            cfg.ai.custom_services.remove(pos);
            changed = true;
        } else if !cfg.ai.hidden_builtins.iter().any(|h| h == &id) {
            // 内置服务：隐藏（不可删除）
            cfg.ai.hidden_builtins.push(id.clone());
            changed = true;
        }
        // 清除指向已移除服务的悬空引用（对应 v1 aiwin.go RemoveAIService）。
        if cfg.ai.default_service_id == id {
            cfg.ai.default_service_id.clear();
            changed = true;
        }
        if cfg.ai.last_service_id == id {
            cfg.ai.last_service_id.clear();
            changed = true;
        }
    }
    // 关闭该服务已打开的 dock 窗口，并清掉 active。destroy 而非 close：close 会被
    // CloseRequested 的 prevent_close 拦截，而 stash 语义下被遗弃的窗口会在屏幕外
    // 永久保持渲染（合成不挂起），白白耗资源。
    {
        let label = format!("ai-service-{id}");
        if let Some(win) = app.get_webview_window(&label) {
            let _ = win.destroy();
        }
        let st = app.state::<AiState>();
        let mut ai = st.lock();
        ai.service_windows.remove(&format!("ai-service-{id}"));
        if ai.active.as_deref() == Some(id.as_str()) {
            ai.active = None;
        }
    }
    if changed {
        save_cfg(&app);
    }
}

#[tauri::command]
pub fn set_default_ai_service(app: tauri::AppHandle, id: String) {
    {
        let st = app.state::<CfgState>();
        st.lock().ai.default_service_id = id;
    }
    save_cfg(&app);
}

#[tauri::command]
pub fn get_ai_prompts(app: tauri::AppHandle) -> Vec<config::AIPrompt> {
    app.state::<CfgState>().snapshot().ai.prompts
}

#[tauri::command]
pub fn add_ai_prompt(
    app: tauri::AppHandle,
    title: String,
    content: String,
    tags: Vec<String>,
) -> Result<String, String> {
    if title.trim().is_empty() && content.trim().is_empty() {
        return Err("title or content is required".into());
    }
    let tags = normalize_tags(tags);
    let id = new_id("p");
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        cfg.ai.prompts.push(config::AIPrompt {
            id: id.clone(),
            title,
            content,
            tags,
        });
    }
    save_cfg(&app);
    Ok(id)
}

#[tauri::command]
pub fn remove_ai_prompt(app: tauri::AppHandle, id: String) {
    let mut changed = false;
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        if let Some(pos) = cfg.ai.prompts.iter().position(|p| p.id == id) {
            cfg.ai.prompts.remove(pos);
            changed = true;
        }
    }
    if changed {
        save_cfg(&app);
    }
}

fn normalize_tags(tags: Vec<String>) -> Vec<String> {
    let mut seen = std::collections::HashSet::new();
    let mut out = Vec::new();
    for t in tags {
        let s = t.trim().to_string();
        if s.is_empty() {
            continue;
        }
        if seen.insert(s.to_lowercase()) {
            out.push(s);
        }
    }
    out
}

/// 打开本地主窗口（ai.html 顶栏入口）。
#[tauri::command]
pub fn show_local_window(app: tauri::AppHandle) {
    show_main_window(&app);
}

#[tauri::command]
pub fn copy_to_clipboard(app: tauri::AppHandle, text: String) -> Result<(), String> {
    use tauri_plugin_clipboard_manager::ClipboardExt;
    app.clipboard().write_text(text).map_err(|e| e.to_string())
}

/// 在系统浏览器打开某 AI 服务。
#[tauri::command]
pub fn open_service_in_browser(app: tauri::AppHandle, id: String) -> Result<(), String> {
    let cfg = app.state::<CfgState>().snapshot();
    let Some((url, _)) = ai::find_service(&cfg.ai, &id) else {
        return Err("service not found".into());
    };
    tauri_plugin_opener::open_url(&url, None::<&str>).map_err(|e| e.to_string())
}
