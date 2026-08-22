//! Tauri 托管状态：各子系统独立的 Mutex 状态，避免单一大锁。

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use crate::config::DesktopConfig;
use crate::server::ServerHandle;

/// 命令行标志（从 argv 解析，对应 Wails 版 runtimeFlags）。
#[derive(Debug, Clone, Default)]
pub struct Flags {
    /// 跳过探测/spawn，直接打开该 URL（开发用）。
    pub dev_url: String,
    /// 登录触发的启动（默认显示主窗口）。
    pub autostart: bool,
    /// 最小化到托盘启动（主窗口隐藏）。
    pub start_minimized: bool,
}

/// 应用元信息。
pub struct AppMeta {
    pub data_dir: PathBuf,
    pub lang: String,
    pub flags: Flags,
}

/// 桌面配置（连接/快捷键/AI 本地状态等）。
pub struct CfgState {
    pub inner: Mutex<DesktopConfig>,
}

impl CfgState {
    pub fn new(cfg: DesktopConfig) -> Self {
        Self { inner: Mutex::new(cfg) }
    }
    pub fn snapshot(&self) -> DesktopConfig {
        self.inner.lock().unwrap().clone()
    }
    pub fn lock(&self) -> std::sync::MutexGuard<'_, DesktopConfig> {
        self.inner.lock().unwrap()
    }
}

/// 本地 server 状态。
#[derive(Default)]
pub struct ServerState {
    pub inner: Mutex<ServerInfo>,
}

pub struct ServerInfo {
    pub addr: Option<String>,
    pub handle: Option<Arc<ServerHandle>>,
    pub reused: bool,
    pub restarting: bool,
    pub shutting_down: bool,
}

impl Default for ServerInfo {
    fn default() -> Self {
        Self { addr: None, handle: None, reused: false, restarting: false, shutting_down: false }
    }
}

impl ServerState {
    pub fn new() -> Self {
        Self { inner: Mutex::new(ServerInfo::default()) }
    }
    pub fn lock(&self) -> std::sync::MutexGuard<'_, ServerInfo> {
        self.inner.lock().unwrap()
    }
    pub fn addr(&self) -> Option<String> {
        self.lock().addr.clone()
    }
    pub fn is_owned(&self) -> bool {
        let s = self.lock();
        s.handle.is_some() && !s.reused
    }
}

/// 活跃的远端连接窗口。
#[derive(Debug, Clone)]
pub struct ConnInfo {
    pub name: String,
    pub host: String,
    pub port: u16,
}

pub struct ConnState {
    pub inner: Mutex<HashMap<String, ConnInfo>>,
}

impl ConnState {
    pub fn new() -> Self {
        Self { inner: Mutex::new(HashMap::new()) }
    }
    pub fn snapshot(&self) -> HashMap<String, ConnInfo> {
        self.inner.lock().unwrap().clone()
    }
    pub fn insert(&self, key: String, info: ConnInfo) {
        self.inner.lock().unwrap().insert(key, info);
    }
    pub fn remove(&self, key: &str) {
        self.inner.lock().unwrap().remove(key);
    }
}

/// AI 直达窗口状态。
pub struct AiState {
    pub inner: Mutex<AiInner>,
}

#[derive(Default)]
pub struct AiInner {
    pub active: Option<String>,
    /// hub stage 矩形（物理像素，前端上报）。
    pub stage: (i32, i32, i32, i32),
    /// hub 上是否有 HTML 覆盖层（模态/设置/加载页）——有则隐藏服务窗口。
    pub overlay_open: bool,
    /// 已按 ID 创建的服务窗口（每服务一个独立窗口，对应 Wails 非 Windows 回退）。
    pub service_windows: HashMap<String, tauri::WebviewWindow>,
}

impl AiState {
    pub fn new() -> Self {
        Self { inner: Mutex::new(AiInner::default()) }
    }
    pub fn lock(&self) -> std::sync::MutexGuard<'_, AiInner> {
        self.inner.lock().unwrap()
    }
}

/// mDNS 发现状态。
pub struct DiscoverState {
    pub inner: Mutex<Option<crate::discovery::Discovery>>,
}

impl DiscoverState {
    pub fn new() -> Self {
        Self { inner: Mutex::new(None) }
    }
    pub fn snapshot(&self) -> Vec<crate::discovery::Instance> {
        let g = self.inner.lock().unwrap();
        match g.as_ref() {
            Some(d) => d.snapshot(),
            None => Vec::new(),
        }
    }
}

/// 重建窗口标记：为 true 时 close 不拦截（用于重建/关闭已隐藏窗口）。
pub struct RebuildingState {
    pub inner: Mutex<bool>,
}

impl RebuildingState {
    pub fn new() -> Self {
        Self { inner: Mutex::new(false) }
    }
}
