//! Webview 创建闸门：全进程任一时刻只允许一个 `WebviewWindowBuilder::build()`
//! 在途。
//!
//! 为什么必须串行：`build()` 是跨线程同步往返——worker 线程把创建请求投给主
//! 线程事件循环后阻塞等结果；而 WebView2 的 CreateCoreWebView2Controller 在
//! 主线程初始化期间会重入泵消息。两个 `build()` 并发在途时，第二个创建请求
//! 会在第一个的初始化重入泵里被嵌套处理；WebView2 不允许嵌套创建，主线程卡
//! 死在内部等待（2026-09-15 挂死现场：主线程停在通道 recv，worker 分别停在
//! user32 交叉发送与通道往返，页面永远加载不出）。串行化后任一时刻队列里最
//! 多一个创建请求，重入泵里不再出现嵌套创建；等待中的 worker 卡在本闸门的
//! 互斥锁上而不是主线程往返上，主线程始终可泵。
//!
//! 闸门只约束 `build()` 本身；窗口建好后的 show/focus/dock 等操作经事件循环
//! 异步派发，不在约束范围。

use std::sync::Mutex;

/// 进程级创建闸门。const 初始化，无运行时依赖。
static CREATE_GATE: Mutex<()> = Mutex::new(());

/// 把一次 webview 创建包进闸门：串行执行，任一时刻全进程最多一个在途。
/// 锁中毒（某次创建 panic）后继续用残余数据——单次创建失败应作为错误返回，
/// 不能让中毒锁堵死后续所有窗口创建。
pub fn gated_create<T>(create: impl FnOnce() -> tauri::Result<T>) -> tauri::Result<T> {
    let _guard = CREATE_GATE
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    create()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::Arc;

    #[test]
    fn concurrent_creates_never_overlap() {
        // 模拟 N 个并发 build()：闸门内的「创建临界区」用计数器验证互斥——
        // 进入 +1、离开 -1，任一时刻在途数不得超过 1。
        let inside = Arc::new(AtomicUsize::new(0));
        let max_inside = Arc::new(AtomicUsize::new(0));
        let mut handles = Vec::new();
        for _ in 0..8 {
            let (inside, max_inside) = (inside.clone(), max_inside.clone());
            handles.push(std::thread::spawn(move || {
                let _ = gated_create(|| {
                    let cur = inside.fetch_add(1, Ordering::SeqCst) + 1;
                    max_inside.fetch_max(cur, Ordering::SeqCst);
                    std::thread::sleep(std::time::Duration::from_millis(5));
                    inside.fetch_sub(1, Ordering::SeqCst);
                    Ok(())
                });
            }));
        }
        for h in handles {
            h.join().unwrap();
        }
        assert_eq!(
            max_inside.load(Ordering::SeqCst),
            1,
            "两个 build() 并发在途——闸门失效"
        );
    }

    #[test]
    fn poisoned_gate_still_usable() {
        // 某次创建 panic 后闸门必须可继续使用，否则一次失败永久堵死建窗。
        let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let _ = gated_create::<()>(|| panic!("simulated creation panic"));
        }));
        assert!(gated_create(|| Ok(())).is_ok());
    }
}
