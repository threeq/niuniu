//! 窗口创建/管理：主窗口、picker、AI hub、远端连接窗口、AI 服务窗口，以及
//! 关闭→托盘、加载过渡页（data: URL，与 Wails 版同款）、快捷键 hash 注入。

use base64::Engine;
use tauri::{Manager, WebviewUrl, WebviewWindow, WebviewWindowBuilder, WindowEvent};
use url::Url;

use crate::config::DesktopConfig;
use crate::i18n;
use crate::state::{ConnInfo, RebuildingState};

/// WebView2 用户数据目录：固定到 ~/.niuniu/webview2（绝对路径，跨构建稳定，
/// 对齐 v1 main.go 的 WebviewUserDataPath）。避免启动上下文未继承 LOCALAPPDATA
/// 时 Tauri 解析失败、wry 回退到 CWD 下 <exename>.WebView2。
fn webview_data_dir() -> std::path::PathBuf {
    let p = crate::config::data_dir().join("webview2");
    let _ = std::fs::create_dir_all(&p);
    p
}

/// RGBA 窗口底色：WebView2 默认白底，webview 首帧渲染前会白屏一闪。设成各页面
/// 自身背景色（splash #111111 / picker #0a0a0a / ai·runners #0b1220），窗口出现
/// 到首帧之间无感。
const SPLASH_BG: tauri::utils::config::Color = tauri::utils::config::Color(0x11, 0x11, 0x11, 0xFF);
const PICKER_BG: tauri::utils::config::Color = tauri::utils::config::Color(0x0A, 0x0A, 0x0A, 0xFF);
const HUB_BG: tauri::utils::config::Color = tauri::utils::config::Color(0x0B, 0x12, 0x20, 0xFF);

/// 应用图标（512x512 appicon.png，编译期内嵌）。供主窗口标题栏与系统托盘使用，
/// 高分辨率源对齐 v1（Wails 用 appIconPNG），避免 default_window_icon 在高 DPI
/// 下渲染模糊。
pub fn app_icon() -> tauri::image::Image<'static> {
    tauri::image::Image::from_bytes(include_bytes!("../assets/appicon.png"))
        .expect("embedded assets/appicon.png must be a valid PNG")
}

/// 主窗口初始加载页：data URL 旋转加载页（服务就绪后 navigate 到本地 SPA）。
pub fn create_main_window(app: &tauri::AppHandle, lang: &str, hidden: bool) -> tauri::Result<WebviewWindow> {
    let title = i18n::local_title(lang);
    let win = WebviewWindowBuilder::new(app, "main", WebviewUrl::External(splash_data_url(lang)))
        .title(title)
        .inner_size(1440.0, 900.0)
        .min_inner_size(800.0, 600.0)
        .center()
        .visible(!hidden)
        .icon(app_icon())?
        .background_color(SPLASH_BG)
        .data_directory(webview_data_dir())
        .build()?;
    Ok(win)
}

/// picker（连接管理）窗口：内嵌 /index.html。
pub fn create_picker_window(app: &tauri::AppHandle, lang: &str) -> tauri::Result<WebviewWindow> {
    WebviewWindowBuilder::new(app, "picker", WebviewUrl::App("index.html".into()))
        .title(i18n::manage_title(lang))
        .inner_size(1280.0, 800.0)
        .visible(false)
        .background_color(PICKER_BG)
        .data_directory(webview_data_dir())
        .build()
}

/// AI 直达窗口：内嵌 /ai.html。注册 hub 移动/缩放跟随——停靠的服务窗口是
/// owned 顶层窗口，不会自动跟着 owner 走，hub 每次移动/缩放都要按 stage 矩形
/// 重贴（对应 v1 main.go 的 WindowDidMove/WindowDidResize →
/// repositionActiveAIService；Tauri 的 Moved/Resized 事件逐次触发无 debounce，
/// 拖动过程中即跟随）。
pub fn create_ai_hub_window(app: &tauri::AppHandle, lang: &str) -> tauri::Result<WebviewWindow> {
    let win = WebviewWindowBuilder::new(app, "ai-hub", WebviewUrl::App("ai.html".into()))
        .title(i18n::ai_title(lang))
        .inner_size(980.0, 720.0)
        .visible(false)
        .background_color(HUB_BG)
        .data_directory(webview_data_dir())
        .build()?;
    let app2 = app.clone();
    win.on_window_event(move |event| {
        if matches!(event, WindowEvent::Moved(_) | WindowEvent::Resized(_)) {
            crate::commands::reposition_active_ai_service(&app2);
        }
    });
    Ok(win)
}

/// 执行器管理窗口（占位页；执行器子系统 v2 尚未移植）。
pub fn create_runners_window(app: &tauri::AppHandle, lang: &str) -> tauri::Result<WebviewWindow> {
    WebviewWindowBuilder::new(app, "runners", WebviewUrl::App("runners.html".into()))
        .title(i18n::runners_title(lang))
        .inner_size(900.0, 640.0)
        .visible(false)
        .background_color(HUB_BG)
        .data_directory(webview_data_dir())
        .build()
}

/// 远端连接窗口：加载 connecting 过渡页（页面自身轮询健康后跳转到目标）。
///
/// 新建 webview 窗口必须在独立线程：快捷键 handler 在主线程的 WM_HOTKEY
/// wndproc 里执行、托盘菜单在主线程事件回调里执行，主线程同步 build() 时
/// WebView2 的异步初始化需要消息泵，嵌在回调里会把整个事件循环挂死（表现为
/// Ctrl+Shift+数字「无效」）。线程内 build() 经事件循环 proxy 派发，主线程
/// 空闲时完成创建，show/focus 消息排队在其后按序执行。
pub fn open_connection_window(
    app: &tauri::AppHandle,
    lang: &str,
    key: &str,
    info: &ConnInfo,
) -> tauri::Result<()> {
    let label = crate::config::window_label_for_key(key);
    if let Some(win) = app.get_webview_window(&label) {
        // 已存在：恢复并聚焦（纯窗口操作，无 webview 创建，可同步）
        let _ = win.show();
        let _ = win.set_focus();
        return Ok(());
    }
    let app = app.clone();
    let lang = lang.to_string();
    let label = label.clone();
    let key = key.to_string();
    let info = info.clone();
    std::thread::spawn(move || {
        // 目标 URL 走 normalize_base_url：host 可能是完整 URL（scheme 进 URL），
        // 端口 0 表示 scheme 默认（443/80 省略），对应 v1 BuildURL。
        let base = crate::config::normalize_base_url(&info.host, info.port);
        let target = format!("{base}/");
        let url = connecting_splash_url(&lang, &info.name, &target);
        let built = WebviewWindowBuilder::new(&app, &label, WebviewUrl::External(url))
            .title(i18n::remote_title(&lang, &info.name, &base))
            .inner_size(1280.0, 840.0)
            .visible(false)
            .background_color(SPLASH_BG)
            .data_directory(webview_data_dir())
            .build();
        match built {
            Ok(win) => {
                // 远程窗口 X 关闭 = 真关闭（与本地主窗口的 close->hide 不同）：
                // 清理 ConnState 并重建托盘，避免幽灵项（对应 v1 connwin.go
                // createAndRegisterConnWindow 的 WindowClosing 钩子）。
                register_conn_close_cleanup(&win, &app, &key);
                let _ = win.show();
                let _ = win.set_focus();
            }
            // 连按两次快捷键的竞态：第二个线程撞已存在 label 报错，忽略即可。
            Err(e) => eprintln!("create connection window {label} failed: {e}"),
        }
    });
    Ok(())
}

/// 远程连接窗口关闭 → 从 ConnState 移除 + 重建托盘。不 prevent_close（远程窗口真关闭）。
fn register_conn_close_cleanup(window: &WebviewWindow, app: &tauri::AppHandle, key: &str) {
    let app = app.clone();
    let key = key.to_string();
    window.on_window_event(move |event| {
        if let WindowEvent::CloseRequested { .. } = event {
            if *app.state::<RebuildingState>().inner.lock().unwrap() {
                return; // 重建流程：放行真关闭，不在此清理
            }
            app.state::<crate::state::ConnState>().remove(&key);
            crate::tray::rebuild_tray(&app);
            // 不调用 api.prevent_close()：远程窗口允许真关闭
        }
    });
}

/// 为窗口注册「关闭 → 隐藏到托盘」行为。rebuilding 状态为 true 时放行真正关闭
/// （重建窗口流程用 destroy()，不受此 hook 影响）。macOS 跳过 set_focus 避免
/// WebKit ServicesController 死锁。
pub fn register_close_to_tray(window: &WebviewWindow, app: &tauri::AppHandle) {
    let w = window.clone();
    let app = app.clone();
    window.on_window_event(move |event| {
        if let WindowEvent::CloseRequested { api, .. } = event {
            if *app.state::<RebuildingState>().inner.lock().unwrap() {
                return; // 重建流程：放行
            }
            api.prevent_close();
            let _ = w.hide();
        }
    });
}

/// 启动失败：主窗口内联显示错误页（对应 Wails showStartupError）。
pub fn show_startup_error(app: &tauri::AppHandle, err: &str) {
    let html = format!(
        "<html><body style='font-family:system-ui;padding:32px'><h1>无法启动 牛牛桌面版</h1><p>{}</p></body></html>",
        err.replace('&', "&amp;").replace('<', "&lt;").replace('>', "&gt;")
    );
    if let Some(win) = app.get_webview_window("main") {
        let url = data_url(&html);
        let _ = win.navigate(url);
        let _ = win.show();
    }
}

/// 主窗口切换全局快捷键组合的展示名（写入 config 用）。
/// 仅构造展示值，不在此注册（注册在 hotkeys.rs）。
pub fn hotkey_config_json(cfg: &DesktopConfig) -> serde_json::Value {
    serde_json::json!({
        "window": {
            "enabled": cfg.hotkey.toggle_window_enabled,
            "accelerator": if cfg.hotkey.toggle_window.trim().is_empty() {
                crate::config::default_window_accelerator()
            } else {
                cfg.hotkey.toggle_window.clone()
            },
        },
        "ai": {
            "enabled": cfg.hotkey.toggle_ai_enabled,
            "accelerator": if cfg.hotkey.toggle_ai.trim().is_empty() {
                crate::config::default_ai_accelerator()
            } else {
                cfg.hotkey.toggle_ai.clone()
            },
        },
    })
}

/// 返回 "#__nnhk=<base64url json>"，供本地 SPA（server/web）同步读取快捷键配置。
pub fn hotkey_url_hash(cfg: &DesktopConfig) -> String {
    let raw = serde_json::to_string(&hotkey_config_json(cfg)).unwrap_or_else(|_| "{}".into());
    let b64 = base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(raw.as_bytes());
    format!("#__nnhk={b64}")
}

/// 在 URL 上追加快捷键 hash（先剥离已有 fragment）。
pub fn with_hotkey_hash(url: &str, cfg: &DesktopConfig) -> String {
    let base = match url.find('#') {
        Some(i) => &url[..i],
        None => url,
    };
    format!("{base}{}", hotkey_url_hash(cfg))
}

/// 旋转加载页 data URL（主窗口）。body 做百分号编码，避免 url 解析致命。
fn splash_data_url(lang: &str) -> Url {
    let heading = i18n::local_boot_heading(lang);
    let sub = if lang == "zh" { "正在初始化本地服务" } else { "Initializing local service" };
    let body = format!(
        "<!doctype html><html><head><meta charset='utf-8'><title>Niuniu</title>\
         <style>body{{font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111;color:#ccc}}\
         .s{{text-align:center}}h1{{font-weight:normal;margin:0 0 12px 0;font-size:24px}}p{{font-size:14px;color:#888}}\
         @keyframes spin{{to{{transform:rotate(360deg)}}}}\
         .spin{{width:32px;height:32px;border:3px solid #333;border-top-color:#0af;border-radius:50%;animation:spin 1s linear infinite;margin:0 auto 16px}}\
         </style></head><body><div class='s'><div class='spin'></div><h1>{heading}</h1><p>{sub}</p></div></body></html>"
    );
    data_url(&body)
}

/// 远端连接过渡页 data URL：轮询 /api/health 成功后跳转目标；到达上限强跳。
fn connecting_splash_url(lang: &str, name: &str, target: &str) -> Url {
    let connecting = if lang == "zh" { "正在连接" } else { "Connecting" };
    let brand = if lang == "zh" { "牛牛" } else { "Niuniu" };
    let t = target.replace('\\', "\\\\").replace('\'', "\\'");
    let n = name.replace('\\', "\\\\").replace('\'', "\\'");
    let body = format!(
        "<!doctype html><html><head><meta charset='utf-8'><title>{brand}</title>\
         <style>body{{font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111;color:#ccc}}\
         .s{{text-align:center}}h1{{font-weight:normal;margin:0 0 12px 0;font-size:22px}}p{{font-size:14px;color:#888}}\
         @keyframes spin{{to{{transform:rotate(360deg)}}}}\
         .spin{{width:32px;height:32px;border:3px solid #333;border-top-color:#0af;border-radius:50%;animation:spin 1s linear infinite;margin:0 auto 16px}}\
         </style></head><body><div class='s'><div class='spin'></div><h1>{connecting} {n}…</h1><p>{t}</p></div>\
         <script>var T='{t}';var t0=Date.now(),gone=false,MIN=1200,CAP=15000;\
         function go(){{if(gone)return;gone=true;try{{location.replace(T)}}catch(e){{location.href=T}}}}\
         function probe(){{if(gone)return;fetch(T+'api/health',{{mode:'no-cors',cache:'no-store'}}).then(function(){{setTimeout(go,Math.max(0,MIN-(Date.now()-t0)))}}).catch(function(){{setTimeout(probe,700)}})}}\
         setTimeout(go,CAP);probe();</script></body></html>"
    );
    data_url(&body)
}

fn data_url(body: &str) -> Url {
    // 百分号编码，保留空格等（URL_SAFE_NO_PAD 不适合；用 form 编码空格为 +，再替换）
    let mut s = String::from("data:text/html;charset=utf-8,");
    for b in body.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' | b'/' | b':' | b'&' | b'=' | b',' | b'(' | b')' | b'+' | b' ' => {
                if b == b' ' {
                    s.push_str("%20");
                } else {
                    s.push(b as char);
                }
            }
            _ => {
                s.push('%');
                s.push_str(&format!("{b:02X}"));
            }
        }
    }
    Url::parse(&s).unwrap_or_else(|_| Url::parse("data:text/html,ok").unwrap())
}
