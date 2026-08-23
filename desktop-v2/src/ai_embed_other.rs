//! AI 服务窗口嵌入（非 Windows stub）：macOS/Linux 上 v1 也是独立普通窗口
//! （aiEmbedSupported=false，见 aiembed_other.go），v2 对齐——服务窗口用普通
//! show/hide 语义，不做 Win32 停靠。窗口管理在 commands.rs 里按本常量分流。

pub const AI_EMBED_SUPPORTED: bool = false;
