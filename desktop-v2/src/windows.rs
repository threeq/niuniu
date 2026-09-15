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

/// 应用图标（512x512 appicon.png，编译期内嵌）。供全部窗口标题栏/任务栏与系统
/// 托盘使用，高分辨率源对齐 v1（Wails 的 app 级 Icon 对所有窗口生效），避免
/// default_window_icon（icons/icon.ico 仅 32x32）在高 DPI 下渲染模糊。
pub fn app_icon() -> tauri::image::Image<'static> {
    tauri::image::Image::from_bytes(include_bytes!("../assets/appicon.png"))
        .expect("embedded assets/appicon.png must be a valid PNG")
}

/// 把窗口尺寸/位置夹到所在显示器的可见范围内。builder 里的固定 inner_size
/// 是逻辑像素：DPI 缩放（125%/150%）下 900/840/800 逻辑高会超过屏幕的可用
/// 逻辑高度（1080p@150% 仅 720），首次打开即表现为窗口下沿出屏。缩/夹之后
/// 在显示器几何内重新居中（纵向预留 48 逻辑 px 给任务栏，横向 8）。
fn clamp_to_monitor(win: &WebviewWindow) {
    let Ok(cur) = win.inner_size() else {
        return;
    };
    // 隐藏窗口可能还没有关联显示器，回退主显示器。
    let mon = win
        .current_monitor()
        .ok()
        .flatten()
        .or_else(|| win.primary_monitor().ok().flatten());
    let Some(mon) = mon else {
        return;
    };
    let scale = mon.scale_factor();
    let size = mon.size();
    let avail_lw = ((size.width as f64) / scale - 16.0).max(320.0);
    let avail_lh = ((size.height as f64) / scale - 48.0).max(240.0);
    let lw = (cur.width as f64) / scale;
    let lh = (cur.height as f64) / scale;
    if lw <= avail_lw && lh <= avail_lh {
        return;
    }
    let new_lw = lw.min(avail_lw);
    let new_lh = lh.min(avail_lh);
    let _ = win.set_size(tauri::LogicalSize::new(new_lw, new_lh));
    let pos = mon.position();
    let x = pos.x + ((size.width as f64 - new_lw * scale) / 2.0).round() as i32;
    let y = pos.y + ((size.height as f64 - 48.0 * scale - new_lh * scale) / 2.0).round() as i32;
    let _ = win.set_position(tauri::PhysicalPosition::new(x, y));
}

/// 主窗口初始加载页：data URL 旋转加载页（服务就绪后 navigate 到本地 SPA）。
pub fn create_main_window(
    app: &tauri::AppHandle,
    lang: &str,
    hidden: bool,
) -> tauri::Result<WebviewWindow> {
    let title = i18n::local_title(lang);
    // 所有 build() 经 webview_gate 串行（并发创建会在 WebView2 重入泵里嵌套，
    // 挂死主线程——见 webview_gate.rs 模块注释）。
    let win = crate::webview_gate::gated_create(app, || {
        WebviewWindowBuilder::new(app, "main", WebviewUrl::External(splash_data_url(lang)))
            .title(title)
            .inner_size(1440.0, 900.0)
            .min_inner_size(800.0, 600.0)
            .center()
            .visible(!hidden)
            .icon(app_icon())?
            .background_color(SPLASH_BG)
            .data_directory(webview_data_dir())
            .build()
    })?;
    clamp_to_monitor(&win);
    Ok(win)
}

/// picker（连接管理）窗口：内嵌 /index.html。
pub fn create_picker_window(app: &tauri::AppHandle, lang: &str) -> tauri::Result<WebviewWindow> {
    let win = crate::webview_gate::gated_create(app, || {
        WebviewWindowBuilder::new(app, "picker", WebviewUrl::App("index.html".into()))
            .title(i18n::manage_title(lang))
            .inner_size(1280.0, 800.0)
            .visible(false)
            .icon(app_icon())?
            .background_color(PICKER_BG)
            .data_directory(webview_data_dir())
            .build()
    })?;
    clamp_to_monitor(&win);
    Ok(win)
}

/// AI 直达窗口：内嵌 /ai.html。hub 自带关闭钩子（spawn_aux_window 对 ai-hub
/// 跳过通用的 close-to-tray）：X = 隐藏到托盘，且必须联动 stash 全部停靠的
/// 服务窗口——它们是 owned 顶层窗口，Windows 上 owner 被 SW_HIDE 隐藏时
/// owned 窗口不会跟着隐藏，不联动的话关掉 hub 后 AI 网页会孤零零留在屏幕上
/// （对应 v1 main.go 的 WindowClosing → Cancel + Hide + setHubVisible(false)）。
/// 另注册 hub 移动/缩放跟随——服务窗口不会自动跟着 owner 走，hub 每次移动/
/// 缩放都要按 stage 矩形重贴（对应 v1 WindowDidMove/WindowDidResize →
/// repositionActiveAIService；Tauri 的 Moved/Resized 事件逐次触发无 debounce，
/// 拖动过程中即跟随）。
pub fn create_ai_hub_window(app: &tauri::AppHandle, lang: &str) -> tauri::Result<WebviewWindow> {
    let win = crate::webview_gate::gated_create(app, || {
        WebviewWindowBuilder::new(app, "ai-hub", WebviewUrl::App("ai.html".into()))
            .title(i18n::ai_title(lang))
            .inner_size(980.0, 720.0)
            .visible(false)
            .icon(app_icon())?
            .background_color(HUB_BG)
            .data_directory(webview_data_dir())
            .build()
    })?;
    clamp_to_monitor(&win);
    let app2 = app.clone();
    let w2 = win.clone();
    win.on_window_event(move |event| match event {
        WindowEvent::Moved(_) | WindowEvent::Resized(_) => {
            crate::commands::reposition_active_ai_service(&app2);
        }
        WindowEvent::CloseRequested { api, .. } => {
            api.prevent_close();
            let _ = w2.hide();
            crate::commands::update_ai_service_visibility(&app2);
        }
        _ => {}
    });
    Ok(win)
}

/// 执行器管理窗口（占位页；执行器子系统 v2 尚未移植）。
pub fn create_runners_window(app: &tauri::AppHandle, lang: &str) -> tauri::Result<WebviewWindow> {
    let win = crate::webview_gate::gated_create(app, || {
        WebviewWindowBuilder::new(app, "runners", WebviewUrl::App("runners.html".into()))
            .title(i18n::runners_title(lang))
            .inner_size(900.0, 640.0)
            .visible(false)
            .icon(app_icon())?
            .background_color(HUB_BG)
            .data_directory(webview_data_dir())
            .build()
    })?;
    clamp_to_monitor(&win);
    Ok(win)
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
        let built = crate::webview_gate::gated_create(&app, || {
            WebviewWindowBuilder::new(&app, &label, WebviewUrl::External(url))
                .title(i18n::remote_title(&lang, &info.name, &base))
                .inner_size(1280.0, 840.0)
                .visible(false)
                .background_color(SPLASH_BG)
                .data_directory(webview_data_dir())
                .build()
        });
        match built {
            Ok(win) => {
                // 远程窗口 X 关闭 = 真关闭（与本地主窗口的 close->hide 不同）：
                // 清理 ConnState 并重建托盘，避免幽灵项（对应 v1 connwin.go
                // createAndRegisterConnWindow 的 WindowClosing 钩子）。
                register_conn_close_cleanup(&win, &app, &key);
                // 建后补挂高清图标（闭包返回 ()，builder 链上的 ? 传播不了）。
                // 图标/显隐/焦点统一经 dispatch_main：排队的主线程窗口任务可能
                // 落在另一创建的 WebView2 重入泵里被内联执行，同步 Win32 调用
                // 不得在泵内跑。
                let win2 = win.clone();
                crate::webview_gate::dispatch_main(&app, move || {
                    clamp_to_monitor(&win2);
                    let _ = win2.set_icon(app_icon());
                    let _ = win2.show();
                    let _ = win2.set_focus();
                });
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
    let sub = if lang == "zh" {
        "正在初始化本地服务"
    } else {
        "Initializing local service"
    };
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
    let connecting = if lang == "zh" {
        "正在连接"
    } else {
        "Connecting"
    };
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
            b'A'..=b'Z'
            | b'a'..=b'z'
            | b'0'..=b'9'
            | b'-'
            | b'_'
            | b'.'
            | b'~'
            | b'/'
            | b':'
            | b'&'
            | b'='
            | b','
            | b'('
            | b')'
            | b'+'
            | b' ' => {
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
