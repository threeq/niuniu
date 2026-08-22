//! 极简原生层本地化：窗口标题 / 托盘文案在 OS 层由本字典给出（SPA 自己的 i18n
//! 在这里不可达，与 Wails 版 internal/i18n 同构）。语言在启动时解析一次。

/// 解析系统 locale（如 "zh-CN" / "en_US"），归一化为 "zh" 或 "en"。
pub fn detect_lang() -> &'static str {
    match sys_locale::get_locale() {
        Some(l) if l.to_lowercase().starts_with("zh") => "zh",
        _ => "en",
    }
}

fn t(lang: &str, key: &str) -> &'static str {
    let zh: bool = lang == "zh";
    match key {
        "brand" => {
            if zh {
                "牛牛"
            } else {
                "Niuniu"
            }
        }
        "app_name" => {
            if zh {
                "牛牛桌面版"
            } else {
                "Niuniu Desktop"
            }
        }
        "local" => {
            if zh {
                "本地"
            } else {
                "Local"
            }
        }
        "remote" => {
            if zh {
                "远端"
            } else {
                "Remote"
            }
        }
        "manage" => {
            if zh {
                "管理连接"
            } else {
                "Manage Connections"
            }
        }
        "ai" => {
            if zh {
                "AI 直达"
            } else {
                "AI Hub"
            }
        }
        "runners" => {
            if zh {
                "执行器管理"
            } else {
                "Runner Manage"
            }
        }
        "booting" => {
            if zh {
                "启动"
            } else {
                "Starting"
            }
        }
        "init_local_service" => {
            if zh {
                "正在初始化本地服务"
            } else {
                "Initializing local service"
            }
        }
        _ => "?",
    }
}

/// 全称产品名（进程 app name / 托盘 tooltip）。
pub fn app_name(lang: &str) -> &'static str {
    t(lang, "app_name")
}

/// 短品牌名（窗口标题前缀）。
pub fn brand(lang: &str) -> &'static str {
    t(lang, "brand")
}

/// 主窗口标题 "{品牌} · {本地}"。
pub fn local_title(lang: &str) -> String {
    format!("{} · {}", brand(lang), t(lang, "local"))
}

/// 主窗口加载过渡页标题 "{启动} {全称}…"。
pub fn local_boot_heading(lang: &str) -> String {
    format!("{} {}…", t(lang, "booting"), app_name(lang))
}

/// picker（连接管理）窗口标题。
pub fn manage_title(lang: &str) -> String {
    format!("{} · {}", brand(lang), t(lang, "manage"))
}

/// AI 直达窗口标题。
pub fn ai_title(lang: &str) -> String {
    format!("{} · {}", brand(lang), t(lang, "ai"))
}

/// 执行器管理窗口标题。
pub fn runners_title(lang: &str) -> String {
    format!("{} - {}", brand(lang), t(lang, "runners"))
}

/// 托盘菜单用的「执行器管理」裸标签（不带品牌前缀，对应 v1 app.go 的
/// i18n.T(lang, KeyRunners)）。
pub fn t_runners(lang: &str) -> &'static str {
    if lang == "zh" { "执行器管理" } else { "Runner Manage" }
}

/// 远端连接窗口标题。
pub fn remote_title(lang: &str, name: &str, host_port: &str) -> String {
    format!("{} · {} · {} ({})", brand(lang), t(lang, "remote"), name, host_port)
}
