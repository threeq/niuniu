//! SSE 监听器：订阅本地 server 的 /api/events/stream，把「AI 直达」按钮点击
//! 桥到原生窗口（open_ai_window），把 agent 完成/失败桥到原生通知。断线自动
//! 重连（5s 退避）。

use std::io::BufRead;
use std::time::Duration;

use tauri::Manager;
use tauri_plugin_notification::NotificationExt;

/// 后台 SSE 监听循环：每次连接前从 ServerState 读取当前地址（server 重启后
/// 自动指向新地址）。
pub fn start(app: tauri::AppHandle) {
    std::thread::spawn(move || loop {
        let addr = app.state::<crate::state::ServerState>().addr();
        if let Some(addr) = addr {
            match stream_once(&app, &addr) {
                Ok(()) => eprintln!("SSE stream ended, reconnecting"),
                Err(e) => eprintln!("SSE listener error: {e}, reconnecting"),
            }
        }
        std::thread::sleep(Duration::from_secs(5));
    });
}

fn stream_once(app: &tauri::AppHandle, addr: &str) -> Result<(), String> {
    let url = format!("http://{addr}/api/events/stream");
    let resp = ureq::get(&url)
        .timeout(Duration::from_secs(60))
        .call()
        .map_err(|e| format!("connect {url}: {e}"))?;
    let mut reader = std::io::BufReader::new(resp.into_reader());
    let mut line = String::new();
    let mut cur_event = String::new();
    loop {
        line.clear();
        let n = reader.read_line(&mut line).map_err(|e| e.to_string())?;
        if n == 0 {
            return Ok(());
        }
        let t = line.trim_end();
        if let Some(rest) = t.strip_prefix("event:") {
            cur_event = rest.trim().to_string();
        } else if let Some(rest) = t.strip_prefix("data:") {
            let data = rest.trim();
            handle_event(app, &cur_event, data);
        } else if t.is_empty() {
            cur_event.clear(); // SSE 帧边界
        }
    }
}

fn handle_event(app: &tauri::AppHandle, event: &str, data: &str) {
    // 服务端 event 类型用 camelCase workspaceId（见 server/internal/event/types.go），
    // 与 Wails 版 internal/connection/sse.go 一致；勿用 snake_case。
    let v: serde_json::Value = serde_json::from_str(data).unwrap_or(serde_json::Value::Null);
    let wsid: i64 = v.get("workspaceId").and_then(|x| x.as_i64()).unwrap_or(0);
    let content: String = v
        .get("error")
        .or_else(|| v.get("message"))
        .and_then(|x| x.as_str())
        .unwrap_or("")
        .to_string();

    match event {
        // SPA 顶部「AI 直达」按钮 → 抬升（而非 toggle）原生 AI hub 窗口。
        // v1 OpenAIWindow 永远 show，不会把已开的 hub 关掉。
        "open_ai_window" => {
            crate::commands::open_ai_window(app);
        }
        "agent_done" => {
            let focused = app
                .get_webview_window("main")
                .map(|w| w.is_focused().unwrap_or(false))
                .unwrap_or(false);
            if !focused {
                let _ = app
                    .notification()
                    .builder()
                    .title("牛牛")
                    .body(format!("Workspace {wsid} 完成"))
                    .show();
            }
        }
        "agent_failed" => {
            let body = if content.is_empty() {
                format!("Workspace {wsid} 失败")
            } else {
                format!("Workspace {wsid} 失败：{content}")
            };
            let _ = app.notification().builder().title("牛牛").body(body).show();
        }
        _ => {}
    }
}
