//! 启动决策：复用已运行的 niuniu server、或在 DB 被占用且不可达时拒绝，
//! 否则返回空决策由调用方自产。对应 Wails 版 `internal/probe`（probe.Decide）。
//!
//! 复用判定靠 `~/.niuniu/server.lock` 里的 PID 存活 + `/api/health` 可达。
//! **关键**：server.lock 在 server emitReady 前就写出，任何运行中的 niuniu
//! 必有 live-PID lockfile；若 lockfile 指向的 PID 存活但 API 不可达，说明
//! 该进程卡死并占着 SQLite 写锁——此时必须**拒绝**，绝不能再 spawn 第二个
//! server（否则新 server 抢不到写锁、永不 emit ready，主窗口卡在 splash）。
//!
//! 与 v1 的差异：v1 还用 `dbIsBusy`（非阻塞 SQLite BEGIN EXCLUSIVE 探测）作
//! 兜底守卫；v2 暂未移植该 SQLite 探测（需引入 SQLite 依赖），靠 lockfile 覆
//! 盖常见场景——DB 被无名进程独占且无 lockfile 的罕见情况会落到 spawn 后由
//! server 自行报 DB-busy 错误，不再卡死握手。

use std::path::Path;

use crate::config;
use crate::server::health_check;

/// 已复用的 server 地址（source: lockfile/configport/defaultport）。
#[derive(Debug, Clone)]
pub struct RunningServer {
    pub addr: String,
    pub source: String,
}

/// 启动决策。`reuse` 有值则复用；`refuse` 非空则拒绝启动并提示用户；
/// 两者皆空则自产 embedded server。
#[derive(Debug, Default)]
pub struct Decision {
    pub reuse: Option<RunningServer>,
    pub refuse: String,
}

impl Decision {
    fn reuse(addr: impl Into<String>, source: &str) -> Self {
        Self {
            reuse: Some(RunningServer { addr: addr.into(), source: source.into() }),
            refuse: String::new(),
        }
    }
    fn refuse(msg: impl Into<String>) -> Self {
        Self { reuse: None, refuse: msg.into() }
    }
}

/// 决策入口：lockfile（PID 存活）→ config 端口 → 默认端口 → 空（自产）。
pub fn decide() -> Decision {
    let data = config::data_dir();

    // 1) server.lock：PID 存活 + health OK → 复用；PID 存活但 health 失败 → 拒绝。
    if let Some(lock) = read_lockfile(&data) {
        if pid_alive(lock.pid) {
            match health_check(&lock.addr) {
                Ok(_) => return Decision::reuse(lock.addr, "lockfile"),
                Err(e) => {
                    return Decision::refuse(format!(
                        "另一个 niuniu 进程（pid {pid}）正在使用数据库，但其 API {addr} 不可达：{err}。请先退出该进程再启动。",
                        pid = lock.pid, addr = lock.addr, err = e
                    ));
                }
            }
        }
        // PID 已死：lockfile 过期，忽略并继续探测端口。
    }

    // 2) ~/.niuniu/config.yaml 里的 server.port。
    if let Some(port) = crate::server::read_configured_port(&data) {
        let addr = format!("127.0.0.1:{port}");
        if health_check(&addr).is_ok() {
            return Decision::reuse(addr, "configport");
        }
    }

    // 3) 默认端口。
    let addr = format!("127.0.0.1:{}", config::DEFAULT_PORT);
    if health_check(&addr).is_ok() {
        return Decision::reuse(addr, "defaultport");
    }

    Decision::default() // 既无 lockfile 也无端口探测命中 → 自产
}

// ─── server.lock 解析 ───────────────────────────────────────────────────────

#[derive(Debug)]
struct Lockfile {
    pid: u32,
    addr: String,
}

/// 读 ~/.niuniu/server.lock（JSON {pid, addr, version, started_at}）。缺失/损坏
/// 返回 None。
fn read_lockfile(data_dir: &Path) -> Option<Lockfile> {
    let raw = std::fs::read_to_string(data_dir.join("server.lock")).ok()?;
    let v: serde_json::Value = serde_json::from_str(&raw).ok()?;
    let pid = v.get("pid").and_then(|x| x.as_u64()).map(|x| x as u32)?;
    let addr = v.get("addr").and_then(|x| x.as_str())?.to_string();
    if addr.is_empty() {
        return None;
    }
    Some(Lockfile { pid, addr })
}

// ─── PID 存活检测（对应 v1 internal/probe/pid_*.go） ───────────────────────

#[cfg(unix)]
fn pid_alive(pid: u32) -> bool {
    // kill(pid, 0)：0 表示进程存在（或权限不足，二者都视为存活）；
    // ESRCH 表示进程已死。
    if pid == 0 {
        return false;
    }
    unsafe { libc::kill(pid as i32, 0) == 0 || *libc::__errno_location() != libc::ESRCH }
}

#[cfg(windows)]
fn pid_alive(pid: u32) -> bool {
    use windows_sys::Win32::Foundation::{CloseHandle, STILL_ACTIVE};
    use windows_sys::Win32::System::Threading::{
        GetExitCodeProcess, OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION,
    };

    if pid == 0 {
        return false;
    }
    unsafe {
        let h = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid);
        if h == 0 {
            // ERROR_INVALID_PARAMETER (87) → 进程已死；其余（权限）视为存活。
            return false;
        }
        let mut code: u32 = 0;
        let ok = GetExitCodeProcess(h, &mut code) != 0;
        CloseHandle(h);
        ok && code == STILL_ACTIVE as u32
    }
}
