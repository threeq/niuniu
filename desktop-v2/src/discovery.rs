//! mDNS 局域网发现：浏览 _niuniu._tcp 服务，供 picker 的「网络中发现」展示。
//! 与 Wails 版 internal/discovery 对应（server 的 embedded 模式不做广播，所以
//! 这里只发现其它机器/非 embedded 实例）。

use mdns_sd::{ServiceDaemon, ServiceEvent};
use serde::Serialize;
use std::collections::HashMap;
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

pub const SERVICE_TYPE: &str = "_niuniu._tcp.local.";

/// 一个被发现实例（完整域名做 key，host:port 供连接）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Instance {
    pub fullname: String,
    pub hostname: String,
    pub host: String,
    pub port: u16,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
}

/// 后台 mDNS 浏览线程的句柄：持续把解析到的实例写入共享 map。
pub struct Discovery {
    pub instances: Arc<Mutex<HashMap<String, Instance>>>,
    stop: mpsc::Sender<()>,
}

impl Discovery {
    /// 启动浏览线程。失败（例如本机无网络栈）返回 Err，调用方降级为空列表。
    pub fn start() -> Result<Discovery, String> {
        let daemon = ServiceDaemon::new().map_err(|e| format!("mdns: {e}"))?;
        let receiver = daemon
            .browse(SERVICE_TYPE)
            .map_err(|e| format!("mdns browse: {e}"))?;

        let instances: Arc<Mutex<HashMap<String, Instance>>> =
            Arc::new(Mutex::new(HashMap::new()));
        let (tx, rx) = mpsc::channel::<()>();
        let map = instances.clone();

        thread::spawn(move || loop {
            match receiver.recv_timeout(Duration::from_millis(500)) {
                Ok(ServiceEvent::ServiceResolved(info)) => {
                    let host = info.get_addresses_v4().into_iter().next()
                        .map(|a| a.to_string())
                        .filter(|s| !s.is_empty())
                        .unwrap_or_else(|| {
                            info.get_hostname().trim_end_matches('.').to_string()
                        });
                    // TXT 记录里的 version=...（server/internal/discovery/mdns.go 广播）
                    let version = info.get_property_val_str("version").map(|s| s.to_string());
                    let inst = Instance {
                        fullname: info.get_fullname().to_string(),
                        hostname: host.clone(),
                        host,
                        port: info.get_port(),
                        version,
                    };
                    if let Ok(mut m) = map.lock() {
                        m.insert(inst.fullname.clone(), inst);
                    }
                }
                Ok(ServiceEvent::ServiceRemoved(_, fullname)) => {
                    if let Ok(mut m) = map.lock() {
                        m.remove(&fullname);
                    }
                }
                Ok(_) => {}
                Err(_) => {
                    // recv 超时/断开：正常继续；若收到停止信号则退出
                    if rx.try_recv().is_ok() {
                        return;
                    }
                }
            }
        });

        Ok(Discovery { instances, stop: tx })
    }

    /// 当前实例快照（按 fullname 排序保证稳定）。
    pub fn snapshot(&self) -> Vec<Instance> {
        let m = self.instances.lock().unwrap();
        let mut v: Vec<Instance> = m.values().cloned().collect();
        v.sort_by(|a, b| a.fullname.cmp(&b.fullname));
        v
    }

    pub fn stop(&self) {
        let _ = self.stop.send(());
    }
}
