//! 主线程消息泵看门狗（诊断）：每秒经 run_on_main_thread 派发一个心跳任务，
//! 主线程泵消息时递增计数器；计数器连续无进展 = 主线程未泵消息（挂死），
//! 写一行到 desktop-v2-boot.log（`watchdog:` 前缀，供 verify_fix.ps1 与人工
//! 排查 grep）。只做诊断记录，不杀进程不弹窗——挂死现场比自动恢复更有价值。
//!
//! 假阳性边界：主线程长同步 command（当前最重的是 save_cfg 文件 IO，毫秒级）
//! 不会超过 FIRST_MISS_SECS；WebView2 初始化的合法内部泵会照常处理心跳任务，
//! 不触发。

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use tauri::Manager;

/// 心跳计数：主线程每执行一次心跳任务 +1。
static HEARTBEAT: AtomicU64 = AtomicU64::new(0);

/// 连续无进展秒数达到该值记第一行，之后每 REPEAT_MISS_SECS 秒补一行。
const FIRST_MISS_SECS: u64 = 10;
const REPEAT_MISS_SECS: u64 = 30;

/// 启动看门狗线程（setup 里调用，早于事件循环启动也安全：任务会排队）。
pub fn start(app: tauri::AppHandle) {
    std::thread::spawn(move || {
        crate::boot_log("watchdog: started (main-thread pump monitor)");
        let mut miss: u64 = 0;
        loop {
            let before = HEARTBEAT.load(Ordering::SeqCst);
            let _ = app.run_on_main_thread(|| {
                HEARTBEAT.fetch_add(1, Ordering::SeqCst);
            });
            std::thread::sleep(Duration::from_secs(1));
            if HEARTBEAT.load(Ordering::SeqCst) != before {
                miss = 0;
            } else {
                miss += 1;
                if should_log(miss) {
                    crate::boot_log(format!(
                        "watchdog: main thread not pumping for {miss}s — UI 挂死，抓线程栈分析"
                    ));
                }
            }
        }
    });
}

/// 第 miss 秒是否记日志：首次达到 FIRST_MISS_SECS，其后每 REPEAT_MISS_SECS 一行。
fn should_log(miss: u64) -> bool {
    miss == FIRST_MISS_SECS
        || (miss > FIRST_MISS_SECS && (miss - FIRST_MISS_SECS) % REPEAT_MISS_SECS == 0)
}

#[cfg(test)]
mod tests {
    use super::should_log;

    #[test]
    fn logs_first_hit_then_every_repeat() {
        assert!(!should_log(1));
        assert!(!should_log(9));
        assert!(should_log(10));
        assert!(!should_log(11));
        assert!(!should_log(39));
        assert!(should_log(40));
        assert!(!should_log(41));
        assert!(should_log(70));
    }
}
