// build.rs：编译期探测 desktop-v2/binaries/ 下的 server/mcp sidecar 是否已 staging
//（由 make _personal-prepare-v2 拷入）。存在则开启 `have_embedded_sidecars` cfg，
// src/server.rs 用 include_bytes! 把它们内嵌进 exe（单文件分发，对齐 v1 go:embed）。
// 同时计算一个指纹（长度 + 首尾 64KB 的 FNV）作为 EMBEDDED_SIDE_FP 环境变量，供
// 运行时跳过重复解压。无 staging（纯 cargo check / dev 无 sidecar）时 cfg 不开启，
// 回退到运行时文件查找。
fn main() {
    tauri_build::build();

    let (server, mcp) = if cfg!(target_os = "windows") {
        ("binaries/niuniu-server.exe", "binaries/niuniu-mcp.exe")
    } else {
        ("binaries/niuniu-server", "binaries/niuniu-mcp")
    };
    if std::path::Path::new(server).exists() && std::path::Path::new(mcp).exists() {
        for p in [server, mcp] {
            println!("cargo:rerun-if-changed={p}");
        }
        // 仅在 release 打包时内嵌（include_bytes! 244MB 会让 cargo check/debug 很慢）；
        // dev（cargo run）CARGO_MANIFEST_DIR 已设、走文件查找，无需内嵌。
        let is_release = std::env::var("PROFILE").as_deref() == Ok("release");
        if is_release {
            println!("cargo:rustc-cfg=have_embedded_sidecars");
            let fp = fingerprint(server, mcp);
            println!("cargo:rustc-env=EMBEDDED_SIDE_FP={fp}");
        }
    }
}

/// 廉价指纹：两文件长度 + 各自首尾 64KB 的 FNV-1a。无需读完整 244MB，构建期毫秒级。
/// 用于运行时判断解压出的 sidecar 是否已是当前内嵌版本（匹配则跳过解压）。
fn fingerprint(server: &str, mcp: &str) -> String {
    let (sl, sh) = sample(server);
    let (ml, mh) = sample(mcp);
    format!("{sl}-{ml}-{sh:016x}-{mh:016x}")
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
