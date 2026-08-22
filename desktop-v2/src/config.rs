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

/// ~/.niuniu
pub fn data_dir() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".niuniu")
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

/// 连接主键 "host:port"。
pub fn key_for(host: &str, port: u16) -> String {
    format!("{host}:{port}")
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
