//! 桌面配置（~/.niuniu/desktop/config.json）的加载/保存。字段与 Wails 版
//! internal/config 保持一致（同样的 JSON 布局，升级无缝衔接）。
//!
//! 配置承载：已保存的远端连接、通知开关、开机自启、主窗口状态、全局快捷键、
//! AI 直达窗口的本地状态（自定义服务 / 隐藏内置 / 默认/上次服务 / 提示词库）。
//!
//! 容错：所有字段都带 `#[serde(default)]` —— Go 老版本可能写出 null 字段，必须
//! 容忍，否则 load 失败会用默认值覆盖（把用户已有连接清空）。

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};

pub const DEFAULT_PORT: u16 = 3000;

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct Connection {
    pub id: String,
    pub name: String,
    pub host: String,
    pub port: u16,
    #[serde(rename = "is_default")]
    pub is_default: bool,
    #[serde(rename = "created_at")]
    pub created_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct WindowState {
    pub x: i32,
    pub y: i32,
    pub width: i32,
    pub height: i32,
    pub maximized: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct HotkeyConfig {
    /// 主窗口切换全局快捷键，如 "Ctrl+Shift+N"（Win/Linux）/ "Cmd+Shift+N"（macOS）。
    #[serde(rename = "toggle_window")]
    pub toggle_window: String,
    #[serde(rename = "toggle_window_enabled")]
    pub toggle_window_enabled: bool,
    /// AI 直达窗口切换全局快捷键（默认 Ctrl/Cmd+Shift+A，issue #670 规定）。
    #[serde(rename = "toggle_ai")]
    pub toggle_ai: String,
    #[serde(rename = "toggle_ai_enabled")]
    pub toggle_ai_enabled: bool,
}

impl Default for HotkeyConfig {
    fn default() -> Self {
        Self {
            toggle_window: default_window_accelerator(),
            toggle_window_enabled: true,
            toggle_ai: default_ai_accelerator(),
            toggle_ai_enabled: true,
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct AIService {
    pub id: String,
    pub name: String,
    pub url: String,
    #[serde(rename = "created_at")]
    pub created_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct AIPrompt {
    pub id: String,
    pub title: String,
    pub content: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct AIConfig {
    #[serde(rename = "custom_services")]
    pub custom_services: Vec<AIService>,
    #[serde(rename = "hidden_builtins")]
    pub hidden_builtins: Vec<String>,
    #[serde(rename = "default_service_id")]
    pub default_service_id: String,
    #[serde(rename = "last_service_id")]
    pub last_service_id: String,
    pub prompts: Vec<AIPrompt>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(default)]
pub struct LegacyRelayConfig {
    pub enabled: bool,
    pub url: String,
    pub email: String,
    pub password: String,
    #[serde(rename = "lan_host_enabled")]
    pub lan_host_enabled: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct DesktopConfig {
    pub connections: Vec<Connection>,
    pub notifications: bool,
    #[serde(rename = "start_on_login")]
    pub start_on_login: bool,
    #[serde(rename = "window_state")]
    pub window_state: WindowState,
    pub hotkey: HotkeyConfig,
    #[serde(rename = "skipped_version")]
    pub skipped_version: String,
    pub ai: AIConfig,
    #[serde(rename = "relay", skip_serializing_if = "is_empty_legacy")]
    pub legacy_relay: LegacyRelayConfig,
}

/// 与 Wails 版一致：全新安装默认开启通知（v1 config.go LoadFrom 预置 true）。
impl Default for DesktopConfig {
    fn default() -> Self {
        Self {
            connections: Vec::new(),
            notifications: true,
            start_on_login: false,
            window_state: WindowState::default(),
            hotkey: HotkeyConfig::default(),
            skipped_version: String::new(),
            ai: AIConfig::default(),
            legacy_relay: LegacyRelayConfig::default(),
        }
    }
}

fn is_empty_legacy(r: &LegacyRelayConfig) -> bool {
    !r.enabled && r.url.is_empty() && r.email.is_empty() && r.password.is_empty()
}

pub fn default_window_accelerator() -> String {
    if cfg!(target_os = "macos") {
        "Cmd+Shift+N".into()
    } else {
        "Ctrl+Shift+N".into()
    }
}

pub fn default_ai_accelerator() -> String {
    // issue #670 规定：Ctrl/Cmd+Shift+A 打开 AI 直达。
    if cfg!(target_os = "macos") {
        "Cmd+Shift+A".into()
    } else {
        "Ctrl+Shift+A".into()
    }
}

/// ~/.niuniu。绝对路径，多级回退（home_dir → USERPROFILE → HOME → exe 同目录），
/// 避免 GUI 启动上下文未继承 USERPROFILE 时回退到相对 CWD 导致日志/数据写到不可预期处。
pub fn data_dir() -> PathBuf {
    if let Some(h) = dirs::home_dir() {
        return h.join(".niuniu");
    }
    if let Some(h) = std::env::var_os("USERPROFILE").map(PathBuf::from) {
        return h.join(".niuniu");
    }
    if let Some(h) = std::env::var_os("HOME").map(PathBuf::from) {
        return h.join(".niuniu");
    }
    // 兜底：可执行文件旁的 .niuniu 目录（绝对路径，绝不回退到 "."）。
    if let Some(exe) = std::env::current_exe().ok().and_then(|p| p.parent().map(PathBuf::from)) {
        return exe.join(".niuniu");
    }
    PathBuf::from(".niuniu")
}

/// ~/.niuniu/desktop
pub fn desktop_dir() -> PathBuf {
    data_dir().join("desktop")
}

/// ~/.niuniu/desktop/config.json
pub fn config_path() -> PathBuf {
    desktop_dir().join("config.json")
}

pub fn load() -> DesktopConfig {
    load_from(&config_path())
}

pub fn load_from(path: &Path) -> DesktopConfig {
    match fs::read_to_string(path) {
        Ok(raw) => match serde_json::from_str::<DesktopConfig>(&raw) {
            Ok(cfg) => cfg,
            Err(e) => {
                eprintln!("desktop config parse failed, using defaults: {e}");
                DesktopConfig::default()
            }
        },
        Err(_) => DesktopConfig::default(),
    }
}

/// 原子写（临时文件 + rename），避免崩溃损坏配置。
pub fn save(cfg: &DesktopConfig) {
    save_to(cfg, &config_path());
}

pub fn save_to(cfg: &DesktopConfig, path: &Path) {
    if let Some(dir) = path.parent() {
        let _ = fs::create_dir_all(dir);
    }
    let Ok(raw) = serde_json::to_string_pretty(cfg) else {
        return;
    };
    let tmp = path.with_extension("json.tmp");
    if fs::write(&tmp, raw).is_err() {
        return;
    }
    let _ = fs::rename(&tmp, path);
}

/// 连接主键：规范化的 host[:port]（剥 scheme、剥默认端口），同 origin 共用一个 key。
/// 对应 v1 desktop/internal/connection/manager.go KeyFor。
pub fn key_for(host: &str, port: u16) -> String {
    let u = normalize_base_url(host, port);
    match u.find("://") {
        Some(i) => u[i + 3..].to_string(),
        None => u,
    }
}

/// 把用户输入的地址规范化成 base URL。host 可以是裸主机/IP、host:port 或完整
/// URL；port>0 仅在 host 未带端口时生效；scheme 取 host 中的，缺省 http
/// （443 时 https）；scheme 默认端口（443/80）省略。对应 v1 NormalizeBaseURL。
pub fn normalize_base_url(host: &str, port: u16) -> String {
    let mut h = host.trim().to_string();
    let mut scheme = String::new();
    if let Some(i) = h.find("://") {
        scheme = h[..i].to_lowercase();
        h = h[i + 3..].to_string();
    }
    // 只保留 origin：剥 path/query/fragment
    if let Some(i) = h.find(['/', '?', '#']) {
        h.truncate(i);
    }
    // 尾部 :<数字> 端口剥出（忽略 IPv6 括号形式，与 v1 一致）
    let (mut host_part, mut port_part) = (h.clone(), String::new());
    if !h.contains(']') {
        if let Some(idx) = h.rfind(':') {
            let cand = &h[idx + 1..];
            if !cand.is_empty() && cand.bytes().all(|b| b.is_ascii_digit()) {
                host_part = h[..idx].to_string();
                port_part = cand.to_string();
            }
        }
    }
    if port_part.is_empty() && port > 0 {
        port_part = port.to_string();
    }
    if scheme.is_empty() {
        scheme = if port_part == "443" { "https".into() } else { "http".into() };
    }
    // scheme 默认端口不落 URL
    if (scheme == "https" && port_part == "443") || (scheme == "http" && port_part == "80") {
        port_part.clear();
    }
    if port_part.is_empty() {
        format!("{scheme}://{host_part}")
    } else {
        format!("{scheme}://{host_part}:{port_part}")
    }
}

/// 连接窗口 label 的安全形态：tauri 窗口 label 只允许字母数字和 - / : _，
/// 域名里的 "." 非法（InvalidWindowLabel → 建窗静默失败）。把 key 里的非法
/// 字符替换成 "_"。规范 key 下仅 "." 会出现。
pub fn window_label_for_key(key: &str) -> String {
    format!("conn-{}", key.chars().map(|c| match c {
        'a'..='z' | 'A'..='Z' | '0'..='9' | '-' | '/' | ':' | '_' => c,
        _ => '_',
    }).collect::<String>())
}

/// 旧版（Wails 时代）在 ~/.niuniu/desktop/config.json 里可能残留明文 relay
/// 密码。升级后纯属风险，启动时清空并落盘；业务已迁到 niuniu-server 的
/// keychain 凭据库。
pub fn scrub_legacy_relay_password() {
    let path = config_path();
    let mut cfg = load_from(&path);
    if cfg.legacy_relay.password.is_empty() {
        return;
    }
    eprintln!("clearing legacy plaintext relay password from desktop config");
    cfg.legacy_relay = LegacyRelayConfig::default();
    save_to(&cfg, &path);
}


#[cfg(test)]
mod tests {
    use super::*;

    /// 与 v1 desktop/internal/connection/manager_test.go 对齐的用例。
    #[test]
    fn normalize_base_url_matches_v1() {
        let cases = [
            // 裸 host + 显式端口（传统 LAN 形态）
            ("192.168.1.5", 3000, "http://192.168.1.5:3000"),
            // 域名无端口 → http 默认，不补端口
            ("niuniu.example.com", 0, "http://niuniu.example.com"),
            // 完整 https URL 带 :443 → 默认端口去掉
            ("https://niuniu.dujiaoshou.pro:443", 443, "https://niuniu.dujiaoshou.pro"),
            // 完整 https URL 无端口 → 尊重 scheme
            ("https://niuniu.example.com", 0, "https://niuniu.example.com"),
            // https URL 非默认端口 → 保留
            ("https://niuniu.example.com:8443", 0, "https://niuniu.example.com:8443"),
            // 无 scheme 但端口 443 → 推断 https
            ("niuniu.example.com", 443, "https://niuniu.example.com"),
            // URL 带路径 → 只留 origin
            ("http://x.example.com:8080/foo", 0, "http://x.example.com:8080"),
            // host 内嵌端口时 port 参数忽略
            ("192.168.1.5:9000", 3000, "http://192.168.1.5:9000"),
            // 尾斜杠容忍
            ("https://niuniu.example.com/", 0, "https://niuniu.example.com"),
        ];
        for (host, port, want) in cases {
            assert_eq!(normalize_base_url(host, port), want, "normalize_base_url({host:?},{port})");
        }
    }

    #[test]
    fn key_for_strips_scheme_and_default_port() {
        assert_eq!(key_for("192.168.1.5", 3000), "192.168.1.5:3000");
        assert_eq!(key_for("https://niuniu.dujiaoshou.pro:443", 443), "niuniu.dujiaoshou.pro");
        assert_eq!(key_for("niuniu.example.com", 0), "niuniu.example.com");
        // 同 origin 不同输入形态共享 key
        assert_eq!(key_for("https://niuniu.example.com", 0), key_for("https://niuniu.example.com/", 0));
    }

    /// tauri 窗口 label 只允许字母数字与 - / : _，域名中的 "." 必须替换，
    /// 否则 InvalidWindowLabel 建窗静默失败（Ctrl+Shift+数字「不显示窗口」根因）。
    #[test]
    fn window_label_is_tauri_safe() {
        assert_eq!(window_label_for_key("192.168.1.5:3000"), "conn-192_168_1_5:3000");
        assert_eq!(window_label_for_key("self.niu6ai.com"), "conn-self_niu6ai_com");
        assert_eq!(window_label_for_key("localhost:3000"), "conn-localhost:3000");
    }
}
