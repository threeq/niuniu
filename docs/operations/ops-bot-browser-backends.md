# Ops Bot 浏览器运行后端：本地与团队版

对应设计：`docs/superpowers/specs/2026-11-06-niuniu-ops-bot-design.md` §8.1、§8.2
对应实现：`server/internal/opsbot/browser/backend.go`、`backend_local.go`、`backend_cdp.go`

浏览器 recipe 不直接面向某一种部署形态。Ops Bot 只依赖 `BrowserBackend`
契约：个人版用本机浏览器执行，团队版换成云端容器，调用方代码不变。

## 1. 契约

```go
type BrowserBackend interface {
    Start(ctx context.Context, spec SessionSpec) (SessionHandle, error)
    Stop(ctx context.Context, handle SessionHandle) error
    Exec(ctx context.Context, handle SessionHandle, recipe packs.Recipe, in ExecInput) (ExecOutput, error)
    Health(ctx context.Context, handle SessionHandle) (Health, error)
}
```

- `SessionSpec{BotID, Backend, ProfileDir, AllowedHosts, TenantID}`：账号级会话描述。
  `Backend` 为空按 `local` 处理；`ProfileDir` 为空时由后端按账号派生。
- `SessionHandle`：会话标识。本地 handle 只在创建它的进程有效；云 handle 可跨进程。
- `ExecInput{Values, Files}`：`Values` 是 recipe 模板变量，`Files` 是允许上传的具名文件。
- `ExecOutput{Values, Receipt, Screenshot, ScreenshotPath}`：提取值、平台回执、证据截图。
- `Health{State, LoginStatus, URL, Detail, CheckedAt}`：`healthy | degraded | down`。

recipe 使用 `packs.Recipe`（`server/internal/opsbot/packs/types.go`），即 Pack 目录里的
声明式 JSON；`recipe.go` 里的 `Recipe`/`RecipeStep` 就是这两个类型的别名。后端只解释
固定动作集，不执行任意代码。

recipe 级失败 sentinel（`ErrHostNotAllowed`、`ErrSelectorNotFound`、`ErrInvalidRecipe`、
`ErrAssertFailed`、`ErrUnknownAction`、`ErrMissingInput`）定义在 `recipe.go`，
`LocalBackend` 与 `RecipeRunner` 共用同一套，调用方只按 sentinel 分类，不用区分是谁执行。

## 2. LocalBackend（个人版）

### 2.1 启动与隔离

- 每个账号一个浏览器进程 + 一个 `--user-data-dir`：
  `<DataDir>/ops/browser/<botID>/local/`（目录 0700，登录态留在磁盘，重启/重登不需重新扫码）。
- 同一个 profile 目录被两个 bot 复用会直接返回 `ErrProfileShared`。
- `Stop` 只关闭会话，不删除 profile（保留 cookie / localStorage / 指纹）。
- `Start` 时通过 `SessionManager.Ensure` 写入 `ops_browser_sessions` 行，和 Worker 共用同一张表。

### 2.2 浏览器引擎

`backend_cdp.go` 直接说 Chrome DevTools Protocol（即 Playwright 底层协议），不额外引入
Node/Playwright 运行时：

1. 查找浏览器：`LocalConfig.ChromePath` → `NIUNIU_CHROME_PATH` → 常见安装路径 → `PATH`；
2. 以 `--remote-debugging-port=0` 启动，读取 profile 下的 `DevToolsActivePort`；
3. `Target.createTarget` + `Target.attachToTarget`（flatten）拿页面 session；
4. 页面操作走 `Runtime.evaluate`（模板固定、插值全部经 `json.Marshal` 转义）、
   `Input.dispatchMouseEvent`、`DOM.setFileInputFiles`、`Page.captureScreenshot`；
5. `Close` 先发 `Browser.close` 让 Chrome 落盘 cookie/localStorage，超时才 kill。

`DriverFactory` 可替换驱动层（测试注入 fake；后续换引擎不动 `LocalBackend`）。

### 2.3 出网 allowlist

- `SessionSpec.AllowedHosts` 来自 Pack manifest 的 `allowed_hosts`。
- 规则：精确主机、`*.example.com` 通配（含 apex）、`*` 显式放开；比较时忽略大小写与端口。
- **空 allowlist = 拒绝所有导航**；recipe 不能自行扩大。
- `goto` 只允许 `http`/`https`，拒绝 `file:`/`javascript:` 和带 userinfo 的 URL。
- `Health` 发现当前页已跳到 allowlist 之外时返回 `degraded`。

### 2.4 租约、风控与失败分类

`LocalBackend.Exec` 默认取 `ops_browser_sessions` 的行级租约（与 Worker 同一条 SQL），
保证同一账号不会并发操作。`LocalConfig.DisableLease = true` 用于「调用方（Worker）
已经持有租约」的嵌入场景。

进入 recipe 前会检查会话状态：`login_status=expired` → `ErrLoginExpired`，
`risk_state=blocked/throttled` → `ErrBlocked`/`ErrRateLimited`。

失败分类（供 Worker 决定重试还是降级人工包）：

| 现象 | 返回 |
|---|---|
| 跳转到登录页 | `ErrLoginExpired` |
| 页面出现验证码 / 操作频繁 | `ErrRateLimited` |
| 页面出现封禁提示 | `ErrBlocked` |
| selector 找不到 / assert 失败 | `ErrRecipeBroken` |
| 页面加载超时 | `ErrTimeout` |
| 网络/导航异常 | `ErrTransient` |
| 非法 recipe、越权域名、文件越权 | 原样返回（配置错误，不重试） |

成功后会话置为 `login_status=valid`、`health=healthy`、`risk_state=normal`；
失败按上表写回状态。`Exec` 出错时仍返回已产生的 `ExecOutput`（含截图），便于留证据。

### 2.5 截图与证据

`screenshot` 动作把 PNG 放入 `ExecOutput.Screenshot`；配置了 `EvidenceDir` 时还会写入
`<EvidenceDir>/<botID>/<recipe>-<unixnano>.png` 并回填 `ScreenshotPath`。

### 2.6 Recipe 动作语义

动作白名单：`goto/wait/click/fill/upload/extract/screenshot/assert/loop/branch`。
其余动作返回 `unknown action`。

| 动作 | 字段 | 说明 |
|---|---|---|
| `goto` | `value`、`fallback` | URL 必须先过 allowlist；fallback 依次尝试 |
| `wait` | `selector`、`args.ms` | 等元素出现；`args.ms` 表示固定等待毫秒 |
| `click` | `selector`、`fallback` | 先滚动到可视区，再发真实鼠标事件 |
| `fill` | `selector`、`value` | 触发 `input`/`change`，兼容受控组件 |
| `upload` | `selector`、`value`/`args.files` | 逗号分隔的绝对路径；见 2.6.1 |
| `extract` | `selector`、`args.as`、`args.attr` | 输出键默认 `value`；`as=receipt` 同时填回执 |
| `screenshot` | — | 保存 PNG 证据 |
| `assert` | `selector`、`args.when/contains/equals` | `when=absent` 断言不存在，默认 present |
| `loop` | `selector`、`args.max` | 对匹配元素重复**紧随其后的一步**，`{{index}}` 为 0 基下标，上限 100 |
| `branch` | `selector`、`args.when/skip` | 条件不满足时跳过后续 `skip` 步（默认 1） |

模板变量：`{{name}}` 从 `ExecInput.Values` 取，`{{files.name}}` 从 `ExecInput.Files` 取；
未知变量直接报错，避免把占位符发到平台。

步骤超时默认 15s（`LocalConfig.ActionTimeout`），导航超时默认 30s
（`NavigateTimeout`）；步骤可用 `timeout_ms` 覆盖。

#### 2.6.1 上传白名单

只允许上传 `ExecInput.Files` 里声明的路径；`LocalConfig.FileRoot` 非空时也允许该目录下的
文件。路径必须是绝对路径且是普通文件，否则返回 `ErrFileNotAllowed`。

### 2.7 配置

```go
backend, err := browser.NewBackend(browser.BackendConfig{
    Kind: "local",
    Local: browser.LocalConfig{
        DataDir:         dataDir,      // ~/.niuniu
        EvidenceDir:     evidenceDir,  // 截图目录
        ChromePath:      "",           // 留空自动发现
        Headless:        false,        // 扫码登录需要 headed
        Sessions:        sessions,     // 与 Worker 共享租约/风控
        // DisableLease: true,         // 由 Worker 持租约时打开
    },
})
```

`NewLocalBackend` 在 `DataDir` 为空时返回 `*ConfigError`（含修复 hint），不会半启动。

## 2.8 登录契约（HTTP / MCP）

登录是交互式长流程，**不能**在一个同步调用里等待扫码。

| 动作 | HTTP | MCP | 行为 |
|---|---|---|---|
| 读状态 | `GET /api/ops/bots/:id/browser` | `opsbot_browser_status` | 返回 `login_status`、`has_login_recipe`、`state`、`next_action`（`none`/`login`/`relogin`）、`evidence_url` |
| 开始登录 | `POST /api/ops/bots/:id/login` | `opsbot_browser_login` | 立即 202 + `state=running`，后台执行 login recipe；轮询状态直到 `login_status=valid` |
| 注册 profile | `POST /api/ops/bots/:id/browser-login` | `opsbot_browser_profile` | 只创建/返回 profile 目录，**不登录**；非浏览器渠道返回 409 `OPS_LOGIN_NOT_REQUIRED` |
| 二维码/证据 | `GET /api/ops/bots/:id/browser/evidence` | — | 返回最近一张登录截图（扫码中或失败时即二维码） |

约束：

- `login_status=expired` 的 session 只允许 login recipe 运行（`Task.IsLogin` /
  `ExecInput.AllowExpired`）；否则账号永远无法恢复。
- resume / onboarding 的验证走 `VerifySession`（优先 `health`
  recipe，短超时），不会触发扫码，也不会阻塞 login recipe 的 180 秒等待。
- 非浏览器 transport（openapi / mcp / manual）返回 `state=not_required`，
  不写 browser session，也不注册 profile。

## 3. CloudBackend（团队版）

实现位于 enterprise 仓库，通过同一接口注入；个人版仓库只提供契约和「未配置」的明确失败。

```go
type CloudBackend interface {
    BrowserBackend
    Container(ctx context.Context, handle SessionHandle) (ContainerInfo, error)
}
```

`ContainerInfo{ContainerID, TenantID, EgressHosts, ReceiptPrefix}` 暴露隔离事实，
供健康检查与审计核对。

企业版实现必须满足：

1. **每账号独立容器**：一个 bot 一个容器，不复用、不共享登录态。
2. **租户标签**：容器、日志、截图/回执对象都带 `TenantID`，跨租户不可见。
3. **固定出口 + allowlist**：出口 IP 固定；导航仍受 `SessionSpec.AllowedHosts` 约束，
   越权域名与非 http(s) URL 必须拒绝。
4. **对象存储回执**：截图/回执写入租户前缀（`ReceiptPrefix`），保留审计线索。
5. **登录态加密**：凭据与会话加密保存，租户级隔离；不得进入对话或 prompt。
6. **审计**：启动、执行、失败、风控事件按租户记录。
7. **并发配额**：按账号配额限制，同一账号同一时刻只允许一个操作租约。

注入方式（企业版启动时）：

```go
err := browser.RegisterBackendFactory("cloud", func(cfg browser.BackendConfig) (browser.BrowserBackend, error) {
    return enterprise.NewCloudBackend(cfg.Cloud, cfg.Cloud.TenantID)
})
```

之后业务侧统一调用 `browser.NewBackend(browser.BackendConfig{Kind: "cloud", Cloud: ...})`。

## 4. 未配置时明确报错，不静默降级

```go
b, err := browser.NewBackend(browser.BackendConfig{Kind: "cloud"})
// err: opsbot/browser: browser backend is not configured (cloud): no cloud backend factory is registered;
//      hint: the team edition must call RegisterBackendFactory("cloud", ...) ...
// b:  *browser.UnsupportedBackend —— 每个方法都返回 ErrBackendNotConfigured / ErrBackendUnsupported
```

即：即使调用方忽略 `err`，也不会悄悄跑成本地浏览器。云配置（`Endpoint`、`TenantID`、
`EgressProxy`、`ReceiptBucket`）由企业版实现校验并给出自己的 `*ConfigError`。

## 5. 排障

| 现象 | 原因 / 处理 |
|---|---|
| `no Chrome/Chromium executable found` | 安装 Chrome，或设置 `ChromePath` / `NIUNIU_CHROME_PATH` |
| `waiting for chrome DevTools port` | 浏览器启动失败；错误里带 Chrome stderr 尾部，检查 profile 权限/被杀毒软件拦截 |
| `navigation host is not in the allowlist` | Pack 的 `allowed_hosts` 没包含目标域名；不要靠改代码绕过 |
| `profile directory is shared by multiple bots` | 两个账号配了同一个 `ProfileDir`；每账号必须独立 |
| `session lease is busy` | 同一账号已有 Worker/其他进程在操作；等待租约过期或检查僵尸任务 |
| `recipe broken` | selector/断言失效（平台改版）→ 更新 Pack 版本，降级人工发布包 |
| `login expired` | 生成「需要重新登录」任务，让运营人扫码；不要重试爆破 |

## 6. 测试

```bash
cd server
go test ./internal/opsbot/browser -run TestBackend -v
go test ./internal/opsbot/browser -race
```

默认测试注入 fake driver，不依赖浏览器。要验证真实 CDP 链路（启动 Chrome、导航、
填写、点击、断言、截图）：

```bash
NIUNIU_OPSBOT_CHROME_TEST=1 go test ./internal/opsbot/browser -run TestBackendLocalCDPChrome -v
```
