//! 嵌入式 Go server 子进程管理：探测复用 → 侧车 spawn → ready 握手 → 优雅退出。
//! 与 Wails 版 internal/bundle + internal/probe 对应，但用 Tauri externalBin
//! 侧车承载 server 二进制（niuniu-mcp 与 server 放同一目录，server 通过
//! os.Executable() + dirname 找到它）。

use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use crate::config;

/// 服务端就绪握手超时（冷启动含 schema 初始化 / 杀软扫描，放宽到 60s）。
const READY_TIMEOUT: Duration = Duration::from_secs(60);

/// 正在运行的 server 信息（复用或自产）。
#[derive(Debug, Clone)]
pub struct RunningServer {
    pub addr: String,
    pub source: String,
}

/// 我们自产的 server 子进程句柄。Shutdown 幂等。
pub struct ServerHandle {
    child: Arc<Mutex<Child>>,
    /// 就绪后仍持有 stdin 写端；Shutdown 时 take 掉 → EOF → server 优雅退出
    /// （embedded 模式 heartbeat 监听 stdin，父进程退出/关管道即自行清理）。
    stdin: Arc<Mutex<Option<ChildStdin>>>,
    /// 后台排空 stdout（embedded 模式日志已强制进文件，这里兜底防管道写满阻塞）。
    _drain: Option<thread::JoinHandle<()>>,
    pub addr: String,
}

// ─── 二进制定位 ────────────────────────────────────────────────────────────

/// 解析 niuniu-server 侧车路径。候选顺序：
/// 1. 打包产物：可执行文件旁 / Contents/MacOS 下的去 triple 名；
/// 2. 开发：<crate>/binaries/<去 triple 名>（make _personal-prepare-v2 拷入）；
/// 3. 开发：<crate>/binaries/<target-triple 名>（手工按 triple 放置）。
/// 不依赖 tauri externalBin（构建期不做文件存在性校验，toolchain 差异也不影响）。
pub fn server_binary_path() -> Result<PathBuf, String> {
    let ext = if cfg!(windows) { ".exe" } else { "" };
    let plain_name = format!("niuniu-server{ext}");

    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            let p = dir.join(&plain_name);
            if p.exists() {
                return Ok(p);
            }
            // macOS .app bundle：sidecar 放 Contents/MacOS
            let mac = dir.join("Contents").join("MacOS").join(&plain_name);
            if mac.exists() {
                return Ok(mac);
            }
        }
    }
    let crate_dir = std::env::var("CARGO_MANIFEST_DIR").unwrap_or_default();
    let p = PathBuf::from(&crate_dir).join("binaries").join(&plain_name);
    if p.exists() {
        return Ok(p);
    }
    let triple = tauri::utils::platform::target_triple().unwrap_or_default();
    let p = PathBuf::from(&crate_dir)
        .join("binaries")
        .join(format!("niuniu-server-{triple}{ext}"));
    if p.exists() {
        return Ok(p);
    }
    Err(format!("niuniu-server sidecar not found (looked for {plain_name} near exe / in binaries/)"))
}

// ─── 探测：已有 server 是否在跑（复用而非自产） ────────────────────────────

/// 探测已运行的 server。优先级：config.yaml 端口 → 默认端口。返回健康可用的
/// 地址即复用；否则返回 None 由调用方自产。
pub fn probe_running_server() -> Option<RunningServer> {
    let data = config::data_dir();
    // 1) ~/.niuniu/config.yaml 里的 server.port
    if let Some(port) = read_configured_port(&data) {
        let addr = format!("127.0.0.1:{port}");
        if health_check(&addr).is_ok() {
            return Some(RunningServer { addr, source: "configport".into() });
        }
    }
    // 2) 默认端口
    let addr = format!("127.0.0.1:{}", config::DEFAULT_PORT);
    if health_check(&addr).is_ok() {
        return Some(RunningServer { addr, source: "defaultport".into() });
    }
    None
}

/// 极小 YAML 扫描：读 server: 块下的 port 字段，不引入 YAML 依赖。
fn read_configured_port(data_dir: &Path) -> Option<u16> {
    let raw = std::fs::read_to_string(data_dir.join("config.yaml")).ok()?;
    let mut in_server = false;
    for line in raw.lines() {
        let line = line.trim();
        if line.starts_with("server:") {
            in_server = true;
            continue;
        }
        if in_server {
            if line.starts_with(' ') || line.starts_with('\t') {
                if let Some(rest) = line.strip_prefix("port:") {
                    let v = rest.trim();
                    if let Ok(p) = v.parse::<u16>() {
                        return Some(p);
                    }
                }
            } else {
                break; // 离开 server 块
            }
        }
    }
    None
}

/// GET /api/health。OK 返回版本号。
pub fn health_check(addr: &str) -> Result<String, String> {
    let url = format!("http://{addr}/api/health");
    let resp = ureq::get(&url)
        .timeout(Duration::from_secs(3))
        .call()
        .map_err(|e| format!("health {addr}: {e}"))?;
    let text = resp.into_string().map_err(|e| e.to_string())?;
    let v: serde_json::Value =
        serde_json::from_str(&text).unwrap_or(serde_json::Value::Null);
    Ok(v.get("version").and_then(|x| x.as_str()).unwrap_or("").to_string())
}

// ─── 自产 server ───────────────────────────────────────────────────────────

/// 启动嵌入式 server：--embedded --addr=127.0.0.1:0，读取 stdout 就绪握手。
pub fn spawn(data_dir: &Path) -> Result<ServerHandle, String> {
    let bin = server_binary_path()?;
    let log_path = log_file_path(data_dir);
    let stderr = open_rotated_log(&log_path)?;

    let mut cmd = Command::new(&bin);
    cmd.args(["--embedded", "--addr=127.0.0.1:0"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(stderr);

    // 环境：重定向 server 的 home 使 os.UserHomeDir() + "/.niuniu" == data_dir；
    // 并把自己的可执行路径交给 server 注册开机自启（GET/PUT /api/autostart）。
    let exe_path = std::env::current_exe()
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_default();
    cmd.env("NIUNIU_PERSONAL_EXE", exe_path);
    let home_parent = data_dir
        .parent()
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_default();
    if cfg!(windows) {
        cmd.env("USERPROFILE", &home_parent);
    } else {
        cmd.env("HOME", &home_parent);
    }

    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0); // setpgid：子进程独立进程组，退出时整组清理
    }

    let mut child = cmd.spawn().map_err(|e| format!("spawn {bin:?}: {e}"))?;

    let stdout = child.stdout.take().ok_or("child stdout unavailable")?;
    let stdin = child.stdin.take();

    // 就绪握手：第一行 JSON {"event":"ready","addr":"..."}
    let (tx, rx) = mpsc::channel();
    let handshake = thread::spawn(move || {
        let mut reader = BufReader::new(stdout);
        let mut line = String::new();
        let n = reader.read_line(&mut line).unwrap_or(0);
        if n == 0 {
            let _ = tx.send(Err("server exited before ready handshake".into()));
            return;
        }
        match serde_json::from_str::<serde_json::Value>(line.trim()) {
            Ok(v) if v.get("event").and_then(|x| x.as_str()) == Some("ready") => {
                let addr = v.get("addr").and_then(|x| x.as_str()).unwrap_or("").to_string();
                if addr.is_empty() {
                    let _ = tx.send(Err("ready handshake missing addr".into()));
                } else {
                    let _ = tx.send(Ok(addr));
                }
            }
            Ok(v) => {
                let _ = tx.send(Err(format!("unexpected ready event: {v}")));
            }
            Err(e) => {
                let _ = tx.send(Err(format!("parse ready JSON: {e}")));
            }
        }
        // 就绪后继续排空 stdout（embedded 模式日志已强制进文件，兜底防阻塞）
        let mut sink = std::io::sink();
        let _ = std::io::copy(&mut reader, &mut sink);
    });

    let addr = match rx.recv_timeout(READY_TIMEOUT) {
        Ok(Ok(a)) => a,
        Ok(Err(e)) => {
            kill_hard(&mut child);
            return Err(e);
        }
        Err(_) => {
            kill_hard(&mut child);
            return Err("server did not emit ready within timeout".into());
        }
    };

    Ok(ServerHandle {
        child: Arc::new(Mutex::new(child)),
        stdin: Arc::new(Mutex::new(stdin)),
        _drain: Some(handshake),
        addr,
    })
}

/// 优雅退出：丢弃 stdin 写端 → server heartbeat 读到 EOF → 自行清理退出；
/// 3 秒内未退再强杀。幂等。
pub fn shutdown(h: &ServerHandle) {
    // 关 stdin（EOF 触发优雅退出）
    if let Ok(mut s) = h.stdin.lock() {
        *s = None;
    }
    let mut child = match h.child.lock() {
        Ok(c) => c,
        Err(_) => return,
    };
    let deadline = Instant::now() + Duration::from_secs(3);
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return,
            Ok(None) => {
                if Instant::now() >= deadline {
                    kill_hard(&mut child);
                    return;
                }
                thread::sleep(Duration::from_millis(100));
            }
            Err(_) => return,
        }
    }
}

/// 强杀（Unix 杀进程组；Windows TerminateProcess）。
fn kill_hard(child: &mut Child) {
    #[cfg(unix)]
    {
        // SAFETY: kill(-pgid, SIGKILL) 杀整组；pgid == child pid（setpgid(0)）
        unsafe {
            libc::kill(-(child.id() as i32), libc::SIGKILL);
        }
        let _ = child.kill();
    }
    #[cfg(not(unix))]
    {
        let _ = child.kill();
    }
}

/// 旋转日志：<data>/logs/embedded-server-YYYY-MM-DD.log（stderr），并在启动时
/// 清理 30 天前的旧文件。
fn open_rotated_log(log_path: &Path) -> Result<Stdio, String> {
    if let Some(dir) = log_path.parent() {
        let _ = std::fs::create_dir_all(dir);
    }
    prune_old_logs(log_path);
    let f = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(log_path)
        .map_err(|e| format!("open server log: {e}"))?;
    Ok(Stdio::from(f))
}

fn log_file_path(data_dir: &Path) -> PathBuf {
    let stamp = chrono::Local::now().format("%Y-%m-%d").to_string();
    data_dir.join("logs").join(format!("embedded-server-{stamp}.log"))
}

fn prune_old_logs(current: &Path) {
    let Some(dir) = current.parent() else { return };
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    let now = chrono::Local::now().date_naive();
    for e in entries.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if !name.starts_with("embedded-server-") || !name.ends_with(".log") {
            continue;
        }
        if let Some(stamp) =
            name.strip_prefix("embedded-server-").and_then(|s| s.strip_suffix(".log"))
        {
            if let Ok(d) = chrono::NaiveDate::parse_from_str(stamp, "%Y-%m-%d") {
                if now.signed_duration_since(d).num_days() > 30 {
                    let _ = std::fs::remove_file(e.path());
                }
            }
        }
    }
}
