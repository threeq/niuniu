//! WebView2 Runtime 预检（仅 Windows）。LTSC / Enterprise / Server 系统不预装
//! WebView2，此时 Tauri（wry/webview2-com）同样会静默崩溃 —— 与 Wails 版在
//! main() 里先行检测的做法一致：检测不到就弹原生模态框提示安装后退出。

/// Microsoft 公布的 Evergreen WebView2 Runtime 客户端 ID（检测契约见官方文档）。
const WEBVIEW2_CLIENT_GUID: &str = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}";

const WEBVIEW2_DOWNLOAD_URL: &str = "https://developer.microsoft.com/microsoft-edge/webview2/";

/// 按文档顺序探测：HKLM-64 → HKLM-WOW64 → HKCU，返回第一个有 pv 的版本号。
pub fn find_webview2_version() -> Option<String> {
    let base = format!(r"Microsoft\EdgeUpdate\Clients\{WEBVIEW2_CLIENT_GUID}");
    let probes: [(winreg::HKEY, String); 3] = [
        (winreg::enums::HKEY_LOCAL_MACHINE, format!(r"SOFTWARE\{base}")),
        (
            winreg::enums::HKEY_LOCAL_MACHINE,
            format!(r"SOFTWARE\WOW6432Node\{base}"),
        ),
        (winreg::enums::HKEY_CURRENT_USER, format!(r"Software\{base}")),
    ];
    for (root, path) in probes {
        let Ok(hk) = winreg::RegKey::predef(root).open_subkey(&path) else {
            continue;
        };
        let Ok(pv) = hk.get_value::<String, _>("pv") else {
            continue;
        };
        if !pv.is_empty() {
            return Some(pv);
        }
    }
    None
}

/// 启动前注入 WebView2 附加浏览器参数（须在任何 WebView2 环境创建之前）：
/// 禁用自动化检测（减少 Cloudflare Turnstile 验证）、后台渲染节流、UA 伪装
/// 为常规 Chrome（WebView2 默认 UA 带 "Edg/…" 指纹被 Cloudflare 不信任）。
/// 保留用户已有值（追加而非覆盖）。Windows-only；其它平台为 no-op。
pub fn apply_webview2_env_args() {
    #[cfg(windows)]
    {
        let ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 \
                  (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36";
        let args = format!(
            "--disable-blink-features=AutomationControlled \
             --disable-backgrounding-occluded-windows \
             --disable-renderer-backgrounding \
             --disable-background-timer-throttling \
             --user-agent=\"{ua}\""
        );
        match std::env::var("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") {
            Ok(prev) if !prev.contains("AutomationControlled") => {
                let _ = std::env::set_var(
                    "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
                    format!("{prev} {args}"),
                );
            }
            Ok(_) => {}
            Err(_) => {
                let _ = std::env::set_var("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", args);
            }
        }
    }
    #[cfg(not(windows))]
    {
        let _ = ();
    }
}

/// 弹原生 MessageBox（用户点「确定」则打开下载页），返回是否点了确定。
/// 在 Tauri app 启动前调用，因此不依赖任何 Tauri API。
#[cfg(windows)]
pub fn show_missing_dialog() -> bool {
    use windows_sys::Win32::UI::Shell::ShellExecuteW;
    use windows_sys::Win32::UI::WindowsAndMessaging::{MessageBoxW, MB_ICONERROR, MB_OKCANCEL, MB_SETFOREGROUND};

    let title = encode_wide("需要安装 WebView2 Runtime");
    let body = encode_wide(
        "牛牛桌面版 需要 Microsoft Edge WebView2 Runtime 才能显示界面。\n\n\
         您当前的 Windows 版本（如 LTSC、企业精简版、Windows Server 等）默认不预装该组件。\n\n\
         点击「确定」打开 Microsoft 官方下载页（请下载 \"Evergreen Standalone Installer\" x64），\n\
         安装后重新启动 牛牛桌面版。",
    );
    let flags = MB_OKCANCEL | MB_ICONERROR | MB_SETFOREGROUND;
    // SAFETY: 仅传入有效的 UTF-16 指针；hwnd=0（无父窗口）；返回值 ≥1 表示 OK。
    let ret = unsafe { MessageBoxW(0, body.as_ptr(), title.as_ptr(), flags) };
    if ret == 1 {
        let verb = encode_wide("open");
        let url = encode_wide(WEBVIEW2_DOWNLOAD_URL);
        // SAFETY: ShellExecuteW 打开默认浏览器；失败仅记录。
        unsafe {
            let r = ShellExecuteW(
                0,
                verb.as_ptr(),
                url.as_ptr(),
                std::ptr::null(),
                std::ptr::null(),
                1,
            );
            if r <= 32 {
                eprintln!("ShellExecuteW open download page failed: {r}");
            }
        }
        return true;
    }
    false
}

#[cfg(not(windows))]
pub fn show_missing_dialog() -> bool {
    false
}

#[cfg(windows)]
fn encode_wide(s: &str) -> Vec<u16> {
    use std::os::windows::ffi::OsStrExt;
    std::ffi::OsStr::new(s).encode_wide().chain(std::iter::once(0)).collect()
}
