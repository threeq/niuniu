# Personal edition

`niuniu-desktop` is the single Wails v3 desktop app. It wraps `niuniu-server`
as a child process (the **local** connection, #0) and — after the 方案 A merge —
also absorbs the former `cmd/connect` remote picker, so the same binary connects
to LAN/cloud/remote nodes via the tray and a picker window. `cmd/connect` has
been retired; only `cmd/personal` remains. Coexists with a standalone
`niuniu-server`.

**Merged window model** (`cmd/personal/connwin.go`): the local server is
connection #0 with dedicated lifecycle (boot/RestartServer/HardResetMain, close
= hide to tray). Remote nodes live in a `connWindows` map keyed by `host:port`,
only connect (never spawn), and their windows truly close. Native window titles
and the process app name are brand-prefixed and localized via
`desktop/internal/i18n` (`{BRAND} · {LOCAL}` / `{BRAND} · {REMOTE} · {name}
({host:port})` / `{BRAND} · {MANAGE}`; lang resolved once from the OS locale).

**Server `--embedded` mode** (`niuniu-server --embedded --addr=127.0.0.1:0`)
flag handling lives in tiny files under `server/cmd/niuniu/`: `flags.go`,
`embedded.go` (forces `Auth.Enabled=false`, `Server.Host=127.0.0.1`,
`Log.Output=file` — the log override is critical to keep stdout clean for the
ready handshake), `handshake.go`, `lockfile.go`, `heartbeat.go`. Startup
order: admin check → parseFlags → `config.Load` → `applyEmbeddedOverrides`
(BEFORE logging.Setup) → `listenEmbedded` → `emitReady(stdout)` →
write server.lock → `srv.Serve` + `watchParentPipe(stdin)`. mDNS gated by
`!flags.Embedded`.

**Stable port across restarts**: when the requested port is 0 (the desktop
always passes `--addr=127.0.0.1:0`), `listenEmbedded` first tries the port
persisted in `~/.niuniu/embedded.port` (written after every successful bind),
falling back to an OS-assigned ephemeral port if it is occupied. This keeps
the personal-edition URL — and any user config keyed on it (browser storage
etc.) — stable across launches.

**Desktop bundle** (`desktop/internal/bundle/`): `Spawn(ctx, Spec) (*Handle,
error)` manages the subprocess. `bundle_unix.go` uses `setpgid` + heartbeat
pipe + SIGTERM-to-pgroup. `bundle_windows.go` uses
CREATE_SUSPENDED→Job(KILL_ON_JOB_CLOSE)→Resume + `HideWindow:true` (or the
GUI parent triggers a console window). Server + MCP binaries are embedded via
per-platform build tags from `desktop/internal/bundle/server-bin/<goos>-<goarch>/`
and hash-cached at `os.UserCacheDir()/niuniu-desktop/`, GC'd after 7 days.

**Probe** (`desktop/internal/probe/`): `AcquireBootLock` (single-instance,
held for app lifetime); `Decide(dataDir, version)` uses SQLite exclusive-lock
probe + `findRunningServerAddr` + `checkHealth` + `versionCompatible`.

**Personal shell** (`desktop/cmd/personal/main.go`): boot-lock → probe.Decide
→ bundle.Spawn → mainWindow.SetURL → startListeners → maybeShowFirstRunDialog
→ tray. `RestartServer` is concurrent-call-guarded.

## Personal-edition quirks

(These were moved out of CLAUDE.md "Important quirks" because they only
apply when touching `desktop/cmd/personal/` or `desktop/internal/bundle/`.)

- **Personal embed path**: `//go:embed` resolves relative to the Go source
  file. Server binaries live at
  `desktop/internal/bundle/server-bin/<goos>-<goarch>/`. The Makefile's
  `_personal-prepare` removes the prior output before `go build -o` (Go
  refuses to overwrite a non-object file).
- **Personal stdout contract**: in embedded mode, the first stdout line is
  reserved for the ready-handshake JSON. `applyEmbeddedOverrides` forces
  `Log.Output="file"` regardless of user config — do not remove.
- **Windows GUI subprocess console**: Wails (`-H windowsgui`) spawning console
  children needs `SysProcAttr.HideWindow=true`. Already in
  `bundle_windows.go`.
- **Single-instance via boot-lock**: `AcquireBootLock(~/.niuniu/personal.boot.lock)`.
  A second personal launch fails to acquire and exits silently.
- **Personal dev mode**: `niuniu-desktop --dev-url=http://localhost:5173`
  skips probe+spawn for SPA iteration.

## 本地沙箱与权限边界

personal/desktop 是**单机、单 OS 用户、本地回环**部署（embedded 模式强制
`Auth.Enabled=false` + 绑定 `127.0.0.1`，见 `cmd/niuniu/embedded.go`）。其安全模型
按"纵深"分三层，**OS 用户账户是唯一硬边界**，应用内其余皆为"约定 + 默认值"软边界。
不要给非技术用户"应用层已沙箱化"的错误安全感——niuniu **不提供也不声称提供**
chroot / 容器 / 命名空间级文件系统 jail。

1. **硬边界（OS 用户账户）**：embedded server 子进程、agent 子进程、所有产物都以
   启动 Wails 应用的同一 OS 用户身份运行；文件系统权限 = 该用户的权限。agent 进程
   理论上可 `cd` 到该用户可达的任意路径——工作空间目录只是**起始 cwd**，不是 jail。
2. **软边界（工作空间约定）**：每工作空间一个 owner 维度目录
   （`OwnerRef.WorkspacePath` → `~/.niuniu/users/<id>/workspaces/<wsID>/`），agent 起始
   cwd 指向它。办公（无仓库）场景的 `office-doc` 提示显式要求"一律落到当前工作空间
   目录…不要写到工作空间之外"。这是**提示约定**，非强制。
3. **可选的 CLI 沙箱（仅 Codex）**：`workspaces.codex_sandbox_mode ∈
   {read-only, workspace-write, danger-full-access}` + `codex_approval_policy`。
   `workspace-write` 让 Codex CLI 把写入限制在工作目录树内；`danger-full-access`
   则下发 `--dangerously-bypass-approvals-and-sandbox` 关掉这层。Claude CLI 侧无对应
   文件系统沙箱，其 `--allowedTools` / `--permission-prompt-tool` 是**工具调用门**
   （放行 Bash/Write 后即可读写工作空间内外），不是文件路径门。

设计与收敛建议（默认值取舍、纵深校验）见
`docs/superpowers/specs/2026-06-14-personal-local-sandbox-hardening-design.md`。

## 本地 Runner（桌面端执行引擎，Epic #526）

远端节点上的 AI 无法直接触达用户本机的工具链（Xcode / Android SDK / 私有构建
环境）。**本地 Runner** 让桌面端把用户机器的一个绑定目录变成远端 workspace 的
可执行后端：远端 agent 通过一组 MCP 工具下发命令，桌面端在本机执行并把
stdout/stderr 流式回传。**方向是反的**——不是远端主动连本机，而是桌面端主动向
远端 server 建立一条长连（reverse channel），复用连接窗口已登录的 JWT 鉴权。

MVP 仅覆盖 **LAN / VPN 直连**（桌面端能直接 HTTP/WS 到远端 server）；relay 跨公网
反向通道是 Epic 已声明的非 MVP 项。

### 组件与数据流

- **反向通道（desktop `internal/localrunner/client.go`）**：对
  `/ws/workspaces/:id/local-runner/runner` 维护一条长连，断线指数退避重连
  （1s→30s），15s 心跳。帧类型：`log`（stdout/stderr/system 行）、`exit`（退出码）、
  `response`（按 id 路由的请求结果）、`pong`。
- **服务端在场登记 + 日志枢纽（server `service/local_runner.go`）**：`online` map
  记录当前在线的 runner；状态机 `unbound → connecting → active → error`
  （曾在线后掉线为 error）。日志走 500 行环形缓冲 + 一对多 fan-out 给 SPA 的
  执行日志面板（`/ws/workspaces/:id/local-runner/logs`）。在场/在线是**纯内存**，
  从不落库。
- **安全网关（desktop `internal/localrunner/gateway.go`）**：**默认拒绝**。命令授权
  = 精确白名单命中，或（仅当命令是无 shell 操作符的简单程序名时）程序名白名单
  命中，否则弹**原生 OS 确认框**。用户勾"始终允许"时把条目回写服务端白名单
  （`PUT .../local-runner` 的 `allowed_commands`），下次免提示。路径经
  `ResolvePath`（`securejoin`）解析，拒绝绝对路径与 `..` 越界——绑定目录是硬边界。
  每次放行/拒绝写入本地 `runner-<id>.jsonl` 审计（**从不**回传远端）。
- **执行 + 流式（`executor.go`）**：在绑定目录内经平台 shell 执行，逐行把
  stdout/stderr 作为 `log` 帧回传（单行 16 KiB、单次 256 KiB 上限，超出丢弃但直播
  流不截断），完成发 `exit` 帧。
- **代码同步（`sync.go`，#472）**：exec 前把绑定目录对齐远端 worktree——
  `git checkout <当前分支>` + `git apply --3way` 远端未提交 diff，**保留**未跟踪/
  被忽略的构建产物（node_modules、target…）。best-effort：非 git 仓库或 diff 不
  干净只记日志、不阻断执行（安全边界是网关，不是 sync）。远端 diff 由
  `GET /api/workspaces/:id/diff` 提供（`remotestate.go` 复用同一鉴权）。
- **种子仓库 clone（`seeder.go`，#478）**：runner 启动时若绑定目录**为空**，从
  workspace 已登记仓库的 git remote（diff 载荷新增的 `clone_url` 字段 = 仓库
  `git_remote`）用**本机 git 凭证**（credential helper / SSH key，不耗 niuniu token）
  clone 进来。单仓库 clone 进绑定目录本身（保持 sync 的单仓库模型），多仓库各
  clone 进 `<dir>/<name>` 子目录（"由 AI 自建/管理多个 repo"布局）。best-effort：
  目录非空、无 clone URL、clone 失败都只记日志、绝不覆盖已有 checkout、绝不阻断
  上线——未种子化的目录仍是可操作目录，AI 可用 `local_exec` 自行 clone。

### 条件注入（子B）与 MCP 工具集

runner 提供三个 MCP 工具，桥接到 `/mcp/local-runner/*`，再经反向通道下发到本机：

| 工具 | 端点 | 作用 |
|------|------|------|
| `local_exec` | `POST /mcp/local-runner/exec {command}` | 在绑定目录用本机工具链跑构建/打包/测试，流式回传 + 退出码 |
| `local_read` | `POST /mcp/local-runner/read {path}` | 读绑定目录内文件（如未回流的构建产物），512 KiB 截断 |
| `local_sync` | `POST /mcp/local-runner/sync` | 手动触发一次远端→本地同步 |

**条件注入**：`niuniu-mcp` 里这三个工具属于 `local-runner` 工具组，只有
`!disabledGroups[toolGroupLocalRun]` 时才注册。scene projection 在生成每
workspace 的 MCP 配置时，调用 `LocalRunnerService.DisableToolGroupsFor(wsID)`——
runner 离线即把该组塞进 `--disable-tool-groups`。于是"优先本地、否则服务端"
纯粹由**工具是否出现**决定：`local_exec` 内部没有服务端兜底，runner 离线时
`/mcp/local-runner/*` 直接返回 **409 + 明确文案**引导 agent 回退服务端执行
（`ErrRunnerOffline`），而不是静默空结果。同时 runner 在线时把配置的
`prompt_snippet` splice 进 worktree 提示（`PromptFragmentFor`）。

### 三引擎验证（#479）：MCP 引擎无关

结论：**claude / codex / qwen 三引擎均能发现并调用本地 Runner 工具集，无需按引擎
特化 runner 侧任何代码**。原因是 MCP 是传输/配置标准，三引擎跑的是**同一个**
`niuniu-mcp` 进程、**同一套**工具、**同一个** `--disable-tool-groups` 值——唯一的
按引擎差异是 scene projection 写哪种**配置文件格式**，而它三种都覆盖
（`scene_projection_apply.go`）：

| 引擎 | MCP 配置落地 | 生成函数 |
|------|--------------|----------|
| claude | `.mcp.json`（`mcpServers`） | `MCPConfigGenerator.Generate` |
| qwen | `.mcp.json`（与 claude 同格式，走 else 分支） | `MCPConfigGenerator.Generate` |
| codex | `.codex/config.toml`（`[mcp_servers.*]`） | `GenerateCodexConfigTomlWithExtras` |

三者都把 `niuniu-mcp` 声明为 MCP server 并透传相同的 `--disable-tool-groups`，
故 `local_exec/local_read/local_sync` 对三引擎一致地"runner 在线才出现"。

**离线实测清单**（需真实桌面端 + 三 CLI + 在线 runner，无法在无头 CI 跑）：
1. 绑定并启动一个 workspace 的本地 Runner，确认 SPA 状态转 `active`。
2. 分别以 `cli_type ∈ {claude, codex, qwen}` 建 workspace（同一在线 runner 的
   连接），让 agent 列出可用工具，确认三者都能看到 `local_exec` 等三工具。
3. 各引擎调 `local_exec "echo hi"`，确认执行日志面板出现 `hi` 且退出码 0。
4. 停掉 runner，确认三引擎侧工具消失（或调用得到 409 回退提示）。

### 鉴权模型

桌面端↔远端是一个用户已登录的 webview，JWT 在该页 localStorage。注入的采集器把
它 post 给 Go（`RawMessageHandler → SetLocalRunnerToken`），按连接归档。runner 只有
在**base URL（连接窗口打开时已知）与 token 都就绪**后才能上线。种子 clone 用的是
**本机 git 凭证**，与这条 JWT 无关——JWT 仅用于拉取 clone URL / diff / 白名单等
niuniu server REST。

### 明确非 MVP

- relay 跨公网反向通道（MVP 仅 LAN/VPN 直连）。
- 绑定 workspace 的 review/diff/pipeline 回流。
- 构建产物主动回流远端（已由 `local_read` 按需读取覆盖）。

## Related specs / plans

- `docs/superpowers/specs/2026-04-18-personal-bundle-design.md` and the
  corresponding plan
- Multi-tenant design: `docs/superpowers/specs/2026-04-24-server-multi-tenant-design.md` (v2)
- M1/M2/M3 plans: `docs/superpowers/plans/2026-04-{24,25}-*`
