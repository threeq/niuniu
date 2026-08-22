# desktop-v2 — 牛牛桌面版（Tauri v2 壳层）

> issue #670：从 Wails v3 (Go) 迁移到 Tauri v2 (Rust)，修复 Windows 中文输入法
> 在焦点切换场景失效的框架级 bug。**独立新目录**，不替换 `desktop/`（Wails 版保留）。
> Go server 进程仍保留，作为子进程由本壳层启动（Tauri 只替换桌面壳层）。

## 架构

```
desktop-v2/
├── Cargo.toml            # tauri 2 + 官方插件（single-instance/global-shortcut/
│                         #   notification/opener/clipboard-manager）
├── tauri.conf.json       # 空 windows（setup 动态建窗）、externalBin 侧车
├── capabilities/         # 默认能力（核心窗口/通知/快捷键/打开外链/剪贴板）
├── assets/               # 内嵌前端：index.html(picker) / ai.html / runners.html
│                         #   + style.css + appicon.png；经 __TAURI__.core.invoke 调命令
├── binaries/             # （gitignore）构建期拷入的 Go server/mcp 侧车
└── src/
    ├── main.rs / lib.rs  # 入口 + Builder 装配 + 后台 boot 序列
    ├── server.rs         # 探测复用 → 侧车 spawn（--embedded）+ ready 握手 + 优雅退出
    ├── windows.rs        # 主/picker/ai-hub/连接/服务窗口 + loading splash(data:)
    ├── tray.rs           # 托盘（显示/刷新/重建/重启/退出 + 连接子菜单）
    ├── hotkeys.rs        # 全局快捷键（Ctrl/Cmd+Shift+A AI 直达 等，按 config）
    ├── sse.rs            # /api/events/stream → 通知 / open_ai_window
    ├── commands.rs       # 内嵌前端 IPC 命令（连接/服务/提示词/窗口动作）
    ├── config.rs         # ~/.niuniu/desktop/config.json（与 Wails 版同布局）
    ├── ai.rs             # AI 直达内置服务目录 + 合并
    ├── discovery.rs      # mDNS（_niuniu._tcp）局域网发现
    ├── webview2.rs       # Windows WebView2 Runtime 预检 + 原生对话框
    └── state.rs          # Tauri 托管状态
```

## 功能对照（Wails → Tauri）

| Wails 版 | desktop-v2 |
|---|---|
| main 窗口（localhost:3000 + loading splash） | `create_main_window` + data: 加载页 → `navigate` |
| picker / ai-hub / runners 隐藏窗口 | setup 动态建窗，`close→hide` 到托盘 |
| 嵌入式 server（bundle.Spawn / probe.Decide） | `server.rs`：复用 → spawn `--embedded` → ready JSON |
| 系统托盘 | `tray.rs`（动态重建） |
| 全局快捷键 | `hotkeys.rs`（Ctrl/Cmd+Shift+A 默认 AI 直达） |
| 单实例锁 | tauri-plugin-single-instance |
| 开机自启 | 侧车环境变量 `NIUNIU_PERSONAL_EXE`（server 的 GET/PUT /api/autostart 使用） |
| 原生通知 | tauri-plugin-notification + SSE agent_done/failed |
| WebView2 UA/自动化/后台渲染 | `additional_browser_args`（见下） |
| WebView2 Runtime 检测 | `webview2.rs` 注册表预检 + MessageBox |
| 连接管理（picker） | `commands.rs` 连接 CRUD + 连接窗口（connecting 页） |
| AI 直达 | `commands.rs` AI 服务/提示词 + 每服务独立 dock 窗口 |

## WebView2 配置（Windows）

Tauri 由 `wry`/WebView2 驱动，UA 伪装 / 禁用自动化检测 / 后台渲染通过
`tauri.conf.json` 的 `app.windows[].additionalBrowserArgs` 或启动前环境变量
`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` 注入：

```
--disable-blink-features=AutomationControlled
--disable-backgrounding-occluded-windows
--disable-renderer-backgrounding
--disable-background-timer-throttling
--user-agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
```

## 构建

```bash
# 本机构建（Windows 产出 .exe）：复用 _personal-prepare 建 Go 侧车 → cargo
make build-personal-v2-current

# 跨平台（需 rustup target add 对应 triple；server 侧车由 make 按 triple 拷入）
make build-personal-v2-windows   # x86_64-pc-windows-msvc
make build-personal-v2-darwin    # aarch64-apple-darwin + x86_64-apple-darwin
make build-personal-v2-linux     # x86_64-unknown-linux-gnu

# 开发运行
make dev-desktop-v2
# 或手动：make _personal-prepare-current && make _personal-prepare-v2 ... && cd desktop-v2 && cargo run
```

产物 `bin/niuniu-desktop-v2-<version>(.exe)`。完整打包（安装器）可后续用
`npx @tauri-apps/cli build --bundles ...` 基于 `externalBin` 产出。

## 已知取舍（相对 Wails 版）

- **窗口状态持久化**：主/picker/AI hub 窗口有固定初始尺寸（1440x900 / 1280x800 /
  980x720），尚未把上次的尺寸/位置/最大化持久化到 config.json（可后续用
  tauri-plugin-window-state 补上）。
- **执行器管理（runners）**：#526 本地 Runner 子系统未移植，窗口为占位页。
- **AI 服务 dock**：v2 用「每服务一个 frameless 窗口贴住 hub stage」的跨平台方案
  （对应 Wails 非 Windows 回退），Windows 上不再做 Win32 child-window 池。
- **mDNS 广播**：embedded server 本就不广播；v2 只做「发现」侧（mdns-sd）。
- **更新检查 / 远程连接 SSE 监控 / openpencil / pet**：未在 issue #670 范围内。
- 全局快捷键默认 AI 直达改为 **Ctrl/Cmd+Shift+A**（issue 明确要求）。
