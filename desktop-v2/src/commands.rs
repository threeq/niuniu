//! Tauri 命令（内嵌前端经 window.__TAURI__.core.invoke 调用）与供托盘/快捷键/
//! SSE 复用的窗口动作。功能面与 Wails 版 connwin.go / aiwin.go / hotkeywin.go
//! 对齐。

use std::time::{SystemTime, UNIX_EPOCH};

use serde::Serialize;
use tauri::Manager;

use crate::ai;
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
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
    format!("{prefix}-{nanos}")
}

fn save_cfg(app: &tauri::AppHandle) {
    let cfg = app.state::<CfgState>().snapshot();
    config::save(&cfg);
}

/// 已保存连接列表。
#[tauri::command]
pub fn get_connections(app: tauri::AppHandle) -> Vec<ConnView> {
    app.state::<CfgState>().snapshot().connections.iter().map(conn_view).collect()
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
pub fn add_connection(app: tauri::AppHandle, name: String, host: String, port: u16) -> Result<ConnView, String> {
    let host = host.trim().to_string();
    if host.is_empty() {
        return Err("host is required".into());
    }
    let id = new_id("conn");
    let display = if name.trim().is_empty() { host.clone() } else { name.trim().to_string() };
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        if cfg.connections.iter().any(|c| c.host == host && c.port == port) {
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
        &app.state::<CfgState>().snapshot().connections.iter().find(|c| c.id == id).unwrap(),
    ))
}

/// 删除连接。
#[tauri::command]
pub fn remove_connection(app: tauri::AppHandle, id: String) -> Result<(), String> {
    let mut removed = false;
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        if let Some(pos) = cfg.connections.iter().position(|c| c.id == id) {
            cfg.connections.remove(pos);
            removed = true;
        }
    }
    if removed {
        save_cfg(&app);
        crate::tray::rebuild_tray(&app);
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

/// 按 ID 连接。
pub fn connect_by_id_internal(app: &tauri::AppHandle, id: &str) -> Result<bool, String> {
    let cfg = app.state::<CfgState>().snapshot();
    let Some(conn) = cfg.connections.iter().find(|c| c.id == id) else {
        return Err("connection not found".into());
    };
    connect_internal(app, &config::key_for(&conn.host, conn.port), &ConnInfo {
        name: conn.name.clone(),
        host: conn.host.clone(),
        port: conn.port,
    })?;
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

/// 直连 host:port（不做保存）。
pub fn connect_to_address_internal(app: &tauri::AppHandle, host: &str, port: u16) -> Result<bool, String> {
    let name = host.to_string();
    connect_internal(app, &config::key_for(host, port), &ConnInfo {
        name,
        host: host.to_string(),
        port,
    })?;
    Ok(true)
}

#[tauri::command]
pub fn connect_to_address(app: tauri::AppHandle, host: String, port: u16) -> Result<bool, String> {
    connect_to_address_internal(&app, &host, port)
}

#[tauri::command]
pub fn connect_to_address_from_picker(app: tauri::AppHandle, host: String, port: u16) -> Result<bool, String> {
    let ok = connect_to_address_internal(&app, &host, port)?;
    if ok {
        hide_picker(&app);
    }
    Ok(ok)
}

fn connect_internal(app: &tauri::AppHandle, key: &str, info: &ConnInfo) -> Result<(), String> {
    let lang = app.state::<AppMeta>().lang.clone();
    app.state::<ConnState>().insert(key.to_string(), info.clone());
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

/// AI 直达：显示/隐藏 hub（并联动服务窗口可见性）。
pub fn toggle_ai_window(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("ai-hub") {
        if win.is_visible().unwrap_or(false) {
            let _ = win.hide();
        } else {
            let _ = win.show();
            let _ = win.set_focus();
        }
        update_ai_service_visibility(app);
    }
}

pub fn open_picker(app: &tauri::AppHandle) {
    if let Some(win) = app.get_webview_window("picker") {
        let _ = win.show();
        let _ = win.set_focus();
    }
}

/// 移动接入：主窗口导航到 Settings → 移动接入（对应 Wails tray「移动接入…」）。
pub fn open_mobile_access(app: &tauri::AppHandle) {
    let Some(addr) = app.state::<ServerState>().addr() else { return };
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
    }
}

pub fn focus_connection(app: &tauri::AppHandle, key: &str) {
    let label = format!("conn-{key}");
    if let Some(win) = app.get_webview_window(&label) {
        let _ = win.show();
        if !cfg!(target_os = "macos") {
            let _ = win.set_focus();
        }
    }
}

pub fn reload_connection(app: &tauri::AppHandle, key: &str) {
    let label = format!("conn-{key}");
    if let Some(win) = app.get_webview_window(&label) {
        let _ = win.eval("location.reload(true)");
    }
}

pub fn close_connection(app: &tauri::AppHandle, key: &str) {
    let label = format!("conn-{key}");
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
            { let st = app.state::<ServerState>(); st.lock().restarting = false; }
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

/// 重建主窗口：销毁旧窗口 → 新建同标签窗口 → 导航到本地 server。
pub fn hard_reset_main(app: &tauri::AppHandle) {
    let lang = app.state::<AppMeta>().lang.clone();
    let addr = app.state::<ServerState>().addr();
    {
        let rb = app.state::<crate::state::RebuildingState>();
        *rb.inner.lock().unwrap() = true;
    }
    if let Some(old) = app.get_webview_window("main") {
        let _ = old.destroy();
    }
    if let Ok(win) = windows::create_main_window(app, &lang, false) {
        crate::windows::register_close_to_tray(&win, app);
        let _ = win.show();
        if let Some(addr) = addr {
            let cfg = app.state::<CfgState>().snapshot();
            let url = windows::with_hotkey_hash(&format!("http://{addr}/"), &cfg);
            if let Ok(u) = url::Url::parse(&url) {
                let _ = win.navigate(u);
            }
        }
    }
    {
        let rb = app.state::<crate::state::RebuildingState>();
        *rb.inner.lock().unwrap() = false;
    }
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

/// 服务窗口按 stage 定位（frameless 贴合 hub stage）。
fn position_service_window(app: &tauri::AppHandle, win: &tauri::WebviewWindow) {
    let st = app.state::<AiState>();
    let stage = st.lock().stage;
    let (x, y, w, h) = stage;
    if w > 0 && h > 0 {
        let _ = win.set_position(tauri::PhysicalPosition::new(x, y));
        let _ = win.set_size(tauri::PhysicalSize::new(w as u32, h as u32));
    }
}

/// 服务窗口可见性 = hub 窗口实际可见 && 无覆盖层 && 是当前激活服务。
/// hub 可见性实时查窗口，避免「hub 未打开但服务窗口乱跳」的启动态竞态。
fn update_ai_service_visibility(app: &tauri::AppHandle) {
    let hub_visible = app
        .get_webview_window("ai-hub")
        .map(|w| w.is_visible().unwrap_or(false))
        .unwrap_or(false);
    let st = app.state::<AiState>();
    let ai = st.lock();
    let show_any = hub_visible && !ai.overlay_open;
    let active = ai.active.clone();
    for (id, win) in ai.service_windows.iter() {
        if show_any && Some(id.clone()) == active {
            let _ = win.show();
        } else {
            let _ = win.hide();
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
    let (win, loaded) = {
        let st = app.state::<AiState>();
        let mut ai = st.lock();
        match ai.service_windows.get(&label) {
            Some(w) => (w.clone(), true),
            None => {
                let created = tauri::WebviewWindowBuilder::new(&app, &label, tauri::WebviewUrl::External(
                    url::Url::parse(&url).map_err(|e| e.to_string())?,
                ))
                .title(format!("{} · {}", i18n::ai_title(&lang), name))
                .decorations(false)
                .visible(false)
                .build()
                .map_err(|e| e.to_string())?;
                ai.service_windows.insert(label.clone(), created.clone());
                (created, false)
            }
        }
    };
    // 关闭服务窗口 = 隐藏（不真正关闭），并从登记表移除以便下次重建
    {
        let app2 = app.clone();
        let id2 = id.clone();
        win.on_window_event(move |event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = app2.state::<AiState>().lock().service_windows.remove(&format!("ai-service-{id2}"));
            }
        });
    }

    position_service_window(&app, &win);
    {
        let st = app.state::<AiState>();
        let mut st = st.lock();
        st.active = Some(id);
    }
    // 服务窗口显隐交给 update_ai_service_visibility（hub 可见才显示）。
    update_ai_service_visibility(&app);
    {
        let st = app.state::<AiState>();
        if hub_visible(&app) && !st.lock().overlay_open {
            let _ = win.set_focus();
        }
    }
    Ok(AIActivateView { loaded })
}

fn hub_visible(app: &tauri::AppHandle) -> bool {
    app.get_webview_window("ai-hub")
        .map(|w| w.is_visible().unwrap_or(false))
        .unwrap_or(false)
}

#[tauri::command]
pub fn set_ai_stage_rect(app: tauri::AppHandle, x: i32, y: i32, w: i32, h: i32) {
    {
        let st = app.state::<AiState>();
        let mut st = st.lock();
        st.stage = (x, y, w, h);
    }
    let st = app.state::<AiState>();
    let ai = st.lock();
    if let Some(active) = &ai.active {
        if let Some(win) = ai.service_windows.get(&format!("ai-service-{active}")) {
            let _ = win.set_position(tauri::PhysicalPosition::new(x, y));
            let _ = win.set_size(tauri::PhysicalSize::new(w as u32, h as u32));
        }
    }
}

#[tauri::command]
pub fn set_ai_overlay_open(app: tauri::AppHandle, open: bool) {
    { let st = app.state::<AiState>(); st.lock().overlay_open = open; }
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
            cfg.ai.hidden_builtins.push(id);
            changed = true;
        }
    }
    if changed {
        save_cfg(&app);
    }
}

#[tauri::command]
pub fn set_default_ai_service(app: tauri::AppHandle, id: String) {
    { let st = app.state::<CfgState>(); st.lock().ai.default_service_id = id; }
    save_cfg(&app);
}

#[tauri::command]
pub fn get_ai_prompts(app: tauri::AppHandle) -> Vec<config::AIPrompt> {
    app.state::<CfgState>().snapshot().ai.prompts
}

#[tauri::command]
pub fn add_ai_prompt(app: tauri::AppHandle, title: String, content: String, tags: Vec<String>) -> Result<String, String> {
    let tags = normalize_tags(tags);
    let id = new_id("p");
    {
        let st = app.state::<CfgState>();
        let mut cfg = st.lock();
        cfg.ai.prompts.push(config::AIPrompt { id: id.clone(), title, content, tags });
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
