//! AI 直达窗口的内置服务目录 + 与自定义服务合并的纯函数。窗口管线见
//! commands.rs / windows.rs。内置 ID 是稳定 slug，用户默认/上次/隐藏偏好跨版本
//! 生效（与 Wails 版 aiservices.go 一致）。

use serde::Serialize;

pub const CAT_CHAT: &str = "chat"; // 对话大模型
pub const CAT_IMAGE: &str = "image"; // 图文 / 图像生成
pub const CAT_VIDEO: &str = "video"; // 视频生成
pub const CAT_CUSTOM: &str = "custom"; // 用户自定义

/// 内置服务目录（描述文案注释同 Wails 版：Claude/Perplexity 因 Cloudflare
/// Turnstile 对嵌入式 WebView 反复验证而有意不列入，用户可自定义添加）。
pub fn builtin_services() -> Vec<AIServiceDef> {
    vec![
        // 对话大模型
        AIServiceDef { id: "chatgpt", name: "ChatGPT", url: "https://chatgpt.com", category: CAT_CHAT },
        AIServiceDef { id: "gemini", name: "Gemini", url: "https://gemini.google.com", category: CAT_CHAT },
        AIServiceDef { id: "grok", name: "Grok", url: "https://grok.com", category: CAT_CHAT },
        AIServiceDef { id: "deepseek", name: "DeepSeek", url: "https://chat.deepseek.com", category: CAT_CHAT },
        AIServiceDef { id: "kimi", name: "Kimi", url: "https://www.kimi.com", category: CAT_CHAT },
        AIServiceDef { id: "tongyi", name: "通义千问", url: "https://www.tongyi.com", category: CAT_CHAT },
        AIServiceDef { id: "doubao", name: "豆包", url: "https://www.doubao.com", category: CAT_CHAT },
        AIServiceDef { id: "yiyan", name: "文心一言", url: "https://yiyan.baidu.com", category: CAT_CHAT },
        AIServiceDef { id: "yuanbao", name: "腾讯元宝", url: "https://yuanbao.tencent.com", category: CAT_CHAT },
        AIServiceDef { id: "chatglm", name: "智谱清言", url: "https://chatglm.cn", category: CAT_CHAT },
        // 图文 / 图像生成
        AIServiceDef { id: "midjourney", name: "Midjourney", url: "https://www.midjourney.com", category: CAT_IMAGE },
        AIServiceDef { id: "ideogram", name: "Ideogram", url: "https://ideogram.ai", category: CAT_IMAGE },
        AIServiceDef { id: "jimeng", name: "即梦", url: "https://jimeng.jianying.com", category: CAT_IMAGE },
        AIServiceDef { id: "wanxiang", name: "通义万相", url: "https://tongyi.aliyun.com/wanxiang", category: CAT_IMAGE },
        // 视频生成
        AIServiceDef { id: "runway", name: "Runway", url: "https://runwayml.com", category: CAT_VIDEO },
        AIServiceDef { id: "kling", name: "可灵 Kling", url: "https://klingai.com", category: CAT_VIDEO },
        AIServiceDef { id: "hailuo", name: "海螺 Hailuo", url: "https://hailuoai.com", category: CAT_VIDEO },
        AIServiceDef { id: "vidu", name: "Vidu", url: "https://www.vidu.studio", category: CAT_VIDEO },
    ]
}

#[derive(Debug, Clone)]
pub struct AIServiceDef {
    pub id: &'static str,
    pub name: &'static str,
    pub url: &'static str,
    pub category: &'static str,
}

/// 前端可见的服务形态（字段名与 Wails 版 JSON 对齐，供 ai.html 直接读取）。
#[derive(Debug, Clone, Serialize)]
pub struct AIServiceView {
    pub id: String,
    pub name: String,
    pub url: String,
    pub category: String,
    pub favicon: String,
    pub custom: bool,
    pub is_builtin: bool,
    pub is_default: bool,
    pub is_last: bool,
}

/// 从站点自身 origin 推导 favicon（绕过 GFW 下第三方 favicon 代理不可靠问题）。
pub fn favicon_url(raw: &str) -> String {
    match url::Url::parse(raw.trim()) {
        Ok(u) if !u.scheme().is_empty() && !u.host_str().unwrap_or("").is_empty() => {
            format!("{}://{}/favicon.ico", u.scheme(), u.host_str().unwrap_or(""))
        }
        _ => String::new(),
    }
}

/// 合并内置（去隐藏）+ 自定义，附 favicon 与默认/上次标记。
pub fn merge_services(cfg: &crate::config::AIConfig) -> Vec<AIServiceView> {
    let mut out = Vec::new();
    for b in builtin_services() {
        if cfg.hidden_builtins.iter().any(|h| h == b.id) {
            continue;
        }
        out.push(AIServiceView {
            id: b.id.into(),
            name: b.name.into(),
            url: b.url.into(),
            category: b.category.into(),
            favicon: favicon_url(b.url),
            custom: false,
            is_builtin: true,
            is_default: cfg.default_service_id == b.id,
            is_last: cfg.last_service_id == b.id,
        });
    }
    for c in &cfg.custom_services {
        out.push(AIServiceView {
            id: c.id.clone(),
            name: c.name.clone(),
            url: c.url.clone(),
            category: CAT_CUSTOM.into(),
            favicon: favicon_url(&c.url),
            custom: true,
            is_builtin: false,
            is_default: cfg.default_service_id == c.id,
            is_last: cfg.last_service_id == c.id,
        });
    }
    out
}

/// 按 ID 解析服务 URL + 名称（内置 + 自定义，隐藏的视为未知）。
pub fn find_service(cfg: &crate::config::AIConfig, id: &str) -> Option<(String, String)> {
    for v in merge_services(cfg) {
        if v.id == id {
            return Some((v.url, v.name));
        }
    }
    None
}

/// 规整用户输入的服务 URL：无 scheme 时补 https://。
pub fn normalize_service_url(raw: &str) -> String {
    let s = raw.trim();
    if s.is_empty() {
        return String::new();
    }
    if !s.contains("://") {
        format!("https://{s}")
    } else {
        s.to_string()
    }
}
