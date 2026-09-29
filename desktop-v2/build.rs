// build.rs：编译期探测 desktop-v2/binaries/ 下的 sidecar 是否已 staging（由
// make _personal-prepare-v2 拷入）。存在则开启 `have_embedded_sidecars` cfg，
// src/server.rs 用 include_bytes! 把它们内嵌进 exe（单文件分发，对齐 v1 go:embed）。
// 同时计算一个指纹（长度 + 首尾 64KB 的 FNV）作为 EMBEDDED_SIDE_FP 环境变量，供
// 运行时跳过重复解压。无 staging（纯 cargo check / dev 无 sidecar）时 cfg 不开启，
// 回退到运行时文件查找。
//
// 四件套：server / mcp / agent（牛牛自研引擎）/ video-mcp（视频创作能力模块）。
// 后两者各自独立门控（have_embedded_agent / have_embedded_video_mcp）——任一缺席
// （老 staging、轻量发行）都不影响两件套的内嵌行为，也不互相影响；在场时参与指纹，
// 二进制一变运行时就重新解压。
fn main() {
    // 本脚本自己声明的自定义 cfg 名（Cargo.toml 的 check-cfg 列表未必列全），
    // 避免 unexpected_cfgs 警告。
    println!("cargo:rustc-check-cfg=cfg(have_embedded_agent)");
    println!("cargo:rustc-check-cfg=cfg(have_embedded_video_mcp)");

    tauri_build::build();

    let (server, mcp, agent, video_mcp) = if cfg!(target_os = "windows") {
        (
            "binaries/niuniu-server.exe",
            "binaries/niuniu-mcp.exe",
            "binaries/niuniu-agent.exe",
            "binaries/niuniu-video-mcp.exe",
        )
    } else {
        (
            "binaries/niuniu-server",
            "binaries/niuniu-mcp",
            "binaries/niuniu-agent",
            "binaries/niuniu-video-mcp",
        )
    };
    // 四件各自独立跟踪：更新任何一件都重跑本脚本。只跟踪已存在的路径 —— 对不
    // 存在的路径打 rerun-if-changed 会让 cargo 每次构建都重跑。无文件可跟踪时
    // 退回 cargo 默认的「包内任何文件变化即重跑」，新落地一件也会被看见。
    for p in [server, mcp, agent, video_mcp] {
        if std::path::Path::new(p).exists() {
            println!("cargo:rerun-if-changed={p}");
        }
    }

    // 仅在 release 打包时内嵌（include_bytes! 数百 MB 会让 cargo check/debug 很慢）；
    // dev（cargo run）CARGO_MANIFEST_DIR 已设、走文件查找，无需内嵌。
    if std::env::var("PROFILE").as_deref() != Ok("release") {
        return;
    }

    // 可选 sidecar 各自探测：agent 与 video-mcp 互不影响，也都不改变两件套的
    // 内嵌决策（server+mcp 是硬前提）。
    let agent_present = std::path::Path::new(agent).exists();
    if agent_present {
        println!("cargo:rustc-cfg=have_embedded_agent");
    }
    let video_present = std::path::Path::new(video_mcp).exists();
    if video_present {
        println!("cargo:rustc-cfg=have_embedded_video_mcp");
    }

    if std::path::Path::new(server).exists() && std::path::Path::new(mcp).exists() {
        println!("cargo:rustc-cfg=have_embedded_sidecars");
        let fp = fingerprint(
            server,
            mcp,
            agent_present.then_some(agent),
            video_present.then_some(video_mcp),
        );
        println!("cargo:rustc-env=EMBEDDED_SIDE_FP={fp}");
    }
}

/// 廉价指纹：各文件长度 + 各自首尾 64KB 的 FNV-1a。无需读完整数百 MB，构建期毫秒级。
/// 用于运行时判断解压出的 sidecar 是否已是当前内嵌版本（匹配则跳过解压）。
/// 可选件在场时按 agent → video_mcp 的顺序追加「长度-哈希」：任一内容一变指纹即变，
/// 运行时会重新解压全部内嵌件；两件套构建保持历史四段格式，老用户目录的 .fp 仍能命中。
fn fingerprint(server: &str, mcp: &str, agent: Option<&str>, video_mcp: Option<&str>) -> String {
    let (sl, sh) = sample(server);
    let (ml, mh) = sample(mcp);
    let mut fp = format!("{sl}-{ml}-{sh:016x}-{mh:016x}");
    if let Some(a) = agent {
        let (al, ah) = sample(a);
        fp.push_str(&format!("-{al}-{ah:016x}"));
    }
    if let Some(v) = video_mcp {
        let (vl, vh) = sample(v);
        fp.push_str(&format!("-{vl}-{vh:016x}"));
    }
    fp
}

/// 返回 (文件长度, 首尾 64KB 合并的 FNV-1a 哈希)。
fn sample(path: &str) -> (u64, u64) {
    let Ok(mut f) = std::fs::File::open(path) else { return (0, 0) };
    let len = std::fs::metadata(path).map(|m| m.len()).unwrap_or(0);
    use std::io::{Read, Seek, SeekFrom};
    let mut head = vec![0u8; 65536];
    let mut tail = vec![0u8; 65536];
    let hl = f.read(&mut head).unwrap_or(0);
    let _ = f.seek(SeekFrom::End(-65536));
    let tl = f.read(&mut tail).unwrap_or(0);
    let mut h: u64 = 0xcbf29ce484222325;
    for b in head[..hl].iter().chain(tail[..tl].iter()) {
        h ^= *b as u64;
        h = h.wrapping_mul(0x100000001b3);
    }
    (len, h)
}
