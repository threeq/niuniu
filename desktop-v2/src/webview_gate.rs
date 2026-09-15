//! Webview 创建闸门 + 在途排队：解决 webview 创建期间的两类主线程挂死。
//!
//! 第一类（并发嵌套创建）：`WebviewWindowBuilder::build()` 是跨线程同步往返
//! ——worker 线程把创建请求投给主线程事件循环后阻塞等结果；而 WebView2 的
//! CreateCoreWebView2Controller 在主线程初始化期间会重入泵消息。两个
//! `build()` 并发在途时，第二个创建请求会在第一个的初始化重入泵里被嵌套处
//! 理；WebView2 不允许嵌套创建，主线程卡死在内部等待。闸门串行化后任一时刻
//! 队列里最多一个创建请求，重入泵里不再出现嵌套创建；等待中的 worker 卡在本
//! 闸门的互斥锁上而不是主线程往返上，主线程始终可泵。
//!
//! 第二类（重入泵内联执行窗口任务）：创建在途期间，主线程正处于 WebView2 初
//! 始化的重入泵里，tao 会照常分发排队的用户消息——此时执行的任何窗口操纵任
//! 务（SetWindowPos/SetFocus/样式改写等同步 Win32 调用）会经 wry 的 wndproc
//! 打进尚未就绪的 WebView2 COM 对象并同步等待，把主线程卡死在自己的泵里
//! （2026-09-15 第二次挂死：watchdog 记录主线程停泵 40s+，发生在 hub webview
//! 加载完成、自动激活服务建窗期间）。因此创建在途时，主线程窗口任务一律排
//! 队，创建完成后（闸门仍持有、不可能有新创建在途）统一补发。
//!
//! 闸门只约束 `build()` 本身；窗口建好后的任务经 dispatch_main 派发。

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;

/// 进程级创建闸门。const 初始化，无运行时依赖。
static CREATE_GATE: Mutex<()> = Mutex::new(());

/// 是否有创建在途（主线程可能正处于其初始化重入泵）。仅在建窗临界区内为 true。
static IN_FLIGHT: AtomicBool = AtomicBool::new(false);

/// 排队的主线程窗口任务。建窗在途时由 dispatch_main 投入，建窗完成后补发。
type Task = Box<dyn FnOnce() + Send + 'static>;
static PENDING: Mutex<Vec<Task>> = Mutex::new(Vec::new());

/// 把一次 webview 创建包进闸门：串行执行，任一时刻全进程最多一个在途；创建
/// 返回后（闸门仍持有）补发排队的主线程窗口任务。
/// 锁中毒（某次创建 panic）后继续用残余数据——单次创建失败应作为错误返回，
/// 不能让中毒锁堵死后续所有窗口创建。
pub fn gated_create<T>(
    app: &tauri::AppHandle,
    create: impl FnOnce() -> tauri::Result<T>,
) -> tauri::Result<T> {
    let result = gated_inner(create);
    // 补发必须仍在闸门内：此刻不可能有新创建在途（闸门在我们手里），主线程
    // 也不在任何创建泵里（我们的创建已返回）——补发的任务走普通排队路径。
    let pending: Vec<Task> = PENDING
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
        .drain(..)
        .collect();
    for task in pending {
        let _ = app.run_on_main_thread(task);
    }
    result
}

/// 闸门内层：串行 + 在途标记。独立出来让单元测试无需 AppHandle。
fn gated_inner<T>(create: impl FnOnce() -> tauri::Result<T>) -> tauri::Result<T> {
    let _guard = CREATE_GATE
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    IN_FLIGHT.store(true, Ordering::SeqCst);
    let result = create();
    IN_FLIGHT.store(false, Ordering::SeqCst);
    result
}

/// 主线程窗口任务统一派发口：创建在途（主线程可能正处于其初始化重入泵）时
/// 排队补发，否则走 run_on_main_thread（主线程调用时内联执行，保持既有语义）。
pub fn dispatch_main(app: &tauri::AppHandle, task: impl FnOnce() + Send + 'static) {
    if IN_FLIGHT.load(Ordering::SeqCst) {
        PENDING
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
            .push(Box::new(task));
    } else {
        let _ = app.run_on_main_thread(task);
    }
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
                let _ = gated_inner(|| {
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
            let _ = gated_inner::<()>(|| panic!("simulated creation panic"));
        }));
        assert!(gated_inner(|| Ok(())).is_ok());
    }

    #[test]
    fn dispatch_queues_while_in_flight_and_flushes() {
        // 建窗在途时 dispatch_main 必须排队；建窗结束后补发（用计数器验证任务
        // 真正执行，且执行时机在 gated_inner 退出之后）。
        use std::sync::Mutex as StdMutex;
        let executed = Arc::new(StdMutex::new(Vec::new()));
        let in_flight_during_task = Arc::new(AtomicBool::new(false));

        let (executed2, flag2) = (executed.clone(), in_flight_during_task.clone());
        let task: Task = Box::new(move || {
            executed2.lock().unwrap().push("ran");
            flag2.store(IN_FLIGHT.load(Ordering::SeqCst), Ordering::SeqCst);
        });

        // 模拟建窗临界区：in_flight 置位期间派发 → 排队；退出后 PENDING 里有任务。
        gated_inner::<()>(|| {
            dispatch_main_for_test(task);
            assert_eq!(executed.lock().unwrap().len(), 0, "在途期间任务不得内联执行");
            Ok(())
        })
        .unwrap();
        // gated_inner 不补发（补发在 gated_create，需要 AppHandle）——测试手动
        // 排空，验证任务会执行且执行时 in_flight 已清零。
        let pending: Vec<Task> = PENDING
            .lock()
            .unwrap_or_else(|p| p.into_inner())
            .drain(..)
            .collect();
        assert_eq!(pending.len(), 1, "在途期间派发的任务应已排队");
        for t in pending {
            t();
        }
        assert_eq!(executed.lock().unwrap().len(), 1);
        assert!(
            !in_flight_during_task.load(Ordering::SeqCst),
            "补发任务执行时创建不应再在途"
        );
    }

    /// 测试专用：dispatch_main 的免 AppHandle 版（只走排队分支）。
    fn dispatch_main_for_test(task: Task) {
        PENDING
            .lock()
            .unwrap_or_else(|p| p.into_inner())
            .push(task);
    }
}
