# MCP 场景凭证明文旁路治理：本地凭证代理（威胁模型与取舍）

> Issue **#733**（Epic #726 Wave 3）· 2026-10-11
> 实现：`server/internal/service/cred_proxy.go`、
> `server/internal/service/scene_projection_creds.go`、
> `server/internal/service/scene_projection_email.go`、
> `server/internal/server/handler_cred_proxy.go`

## 1. 问题

场景物化（`SceneProjector.Apply`）把场景定义的 `${cred:<alias>.<field>}`
占位符在投射时解密，并把**明文**写入工作区文件：

| 落盘位置 | 内容 | 原文位置 |
|---|---|---|
| `<wsDir>/.mcp.json` → `mcpServers.<name>.headers` | 例如 `Authorization: Bearer <真实令牌>` | `scene_projection_creds.go` |
| `<wsDir>/.mcp.json` → `mcpServers.<name>.env` | stdio 服务器的密钥型 env | 同上 |
| `<wsDir>/.email-config/config.toml` | 邮箱 IMAP/SMTP 明文口令 | `scene_projection_email.go` |

工作区 agent 有读取自身工作区文件的能力。明文一旦落盘，就至少带来两类损失：

1. **进入模型上下文**：agent 用 Read/Grep 读到口令，等于把长期凭证写进了对话与
   provider 的日志/缓存。
2. **被外带**：agent 可以把文件内容发到任意地方。

## 2. 威胁模型

**资产**：用户绑定的长期外部凭证（KB/公众号 API Token、邮箱 IMAP 口令等）。

**对手**

- A1 **工作区内运行的 agent**（本 issue 的主要对手）。能力：Read/Grep/Glob 文件、
  执行 Bash、发起网络请求。目的：读取并/或外带工作区里的凭证。
- A2 **读取到磁盘快照的第三方**（备份、同步盘、误提交）。能力：读取静态文件。
- A3 **同机其它进程**（同一 OS 用户）。本方案**不**防御 —— 见 §4.4。

**信任边界**

- niuniu 服务进程 = 可信（持有解密密钥，天然可访问全部凭证）。
- 工作区 = **不可信**（agent 可读写）。
- 场景定义 / 投射缓存 = 已可信（本来就不含明文，只含占位符）。

**要防住的攻击路径**

- P1 agent 读 `.mcp.json` 拿到 `Authorization` 明文 → 直接调用上游 API 或外带。
- P2 agent 读 `.mcp.json` / `config.toml` 拿到邮箱口令 → 登录邮箱。
- P3 静态快照（备份/同步）泄露明文。

**明确不防**

- agent 通过任意子进程（python/node/curl）读取同 OS 用户可读的任何文件。
- agent 通过被授予的 MCP 工具**正常使用**该凭证所代表的权限。

## 3. 方案：本地凭证代理

### 3.1 机制

投射时不再把解析值写进文件，而是：

1. `CredProxyService.Issue(wsID, serverName)` 用**集成主密钥**
   （`integration/crypto.Keyring`，AES-GCM）密封一份**只含
   `{workspaceID, serverName, 过期时间}` 的凭据**，得到 token（前缀 `ncp_`）；
2. `.mcp.json` 里写入的是 **loopback 代理 URL**，并把原 secret header **整条删除**：

   ```jsonc
   // 投射前（明文）
   { "type": "http", "url": "https://kb.example.com/mcp",
     "headers": { "Authorization": "Bearer sk-kb-secret-123" } }

   // 投射后（无明文）
   { "type": "http", "url": "http://127.0.0.1:3000/cred-proxy/ncp_<sealed>" }
   ```

3. 请求时 MCP 客户端把 JSON-RPC 打到该 loopback URL：
   `ANY /cred-proxy/:token/*` 先**解封 token** 拿到 `(wsID, serverName)`，
   再**按当前投射与当前绑定现解一次凭证**（`SceneProjector.ResolveProxyTarget`），
   然后反代到真实上游，**在出境时报文头注入真实凭证**，响应（含 SSE 流）原样回传。

路由注册在 `router.go` 的**鉴权组之外** —— **token 即鉴权**，与 #727 一次性
凭证链接同一模型。token 非法在建立上游连接**之前**返回 401，让过期 `.mcp.json`
立即失败而不是静默打到错的地方。另加一道 **loopback-only** 限制：非本机来源的
请求一律 403 —— token 不可伪造，但可能被偷，所以不允许它从网络重放。

### 3.2 为什么是「指针」而不是「抓取到的密文」

第一版设计把 `token → {上游 URL, 真实 header}` 记在**进程内存**里。那有三个问题：

1. **重启即失效**：`.mcp.json` 是会话启动快照，niuniu 重启后 token→密文表清空、
   而 agent 启动路径（`Generate`，`extras == nil`）只会**保留**磁盘上的旧 URL，
   于是所有带密 MCP 直接 401 —— 这是对「既有 MCP server 零回归」的实打实回归。
2. 进程里长期驻留明文。
3. 需要一套吊销/续期簿记。

现方案把 token 降级为**指针**：它只声明「哪个工作空间的哪个 server」，不含任何
密文；真实凭证在**每次请求**时从「当前投射 + 当前凭证绑定」现取。由此得到：

- **重启无感**：密封密钥持久（OS keychain / 0600 文件），token 解封后现场重解凭证；
- **无吊销需求**：场景移除 → 投射里没有该 server → 403；凭证轮换/解绑 → 下次请求
  即生效；`ResolveProxyTarget` 读的就是当前状态，不需要任何失效列表；
- **进程不驻留明文**：解密值只在处理该请求的栈上存在。

### 3.3 token 的作用域与生命周期

- **作用域**：token 只命名 `(workspaceID, serverName)`，因此它的权限被三重夹逼：
  只能命中**当前投射里仍存在**的该 server，只能用**场景作者写定的那个上游 URL**
  （`ResolveProxyTarget` 从投射读 url，绝不接受 agent 输入），只能注入**该
  server 的 header 占位符**所对应的字段。
- **生命周期**：密封时带绝对过期时间，默认 **7 天**。它只是二次防御 —— token 本身
  不携带任何权限，真正的边界是实时投射，所以不需要「短到会打断长跑会话」的 TTL；
  7 天只是让一份被遗忘在磁盘上的旧 `.mcp.json` 不会永远有效。
- **不可伪造**：AES-GCM（认证加密）+ 集成主密钥；换密钥后旧 token 一律解不开。

### 3.4 关键不变量

- **没有任何代码路径会把解析后的 secret header 写进工作区文件**。若无法代理
  （无 proxy、stdio、无 url、token 生成失败），服务端**丢弃该服务器**并把它
  记入 `MissingCredentials`（与「占位符未解析」同样的 fail-closed 纪律，见
  `proxifyCredentialHeaders` 返回 `false` 的分支）。
- URL 里出现 `${cred:}`（无法用 header 注入替换）→ 同样丢弃，不落明文。
- 投射阶段仍会**解析**一次占位符 —— 那是为了沿用「凭证缺失 → 丢弃服务器 +
  上报绑定卡片」的既有语义，解析结果随后被丢弃，不写入任何文件。

## 4. 覆盖范围与残余风险

### 4.1 已覆盖：http/sse 服务器的 header 凭证

内置场景中携带凭证的全部走 header：`kb-*.yaml`、`wechat-mp.yaml`
（`Authorization: Bearer ${cred:...}`），以及由 KB 列表展开的 mcp-kind KB
（`scene_projection_apply.go` 的 `expandKnowledgeBases`）。这些已完全覆盖。

### 4.2 残余风险：stdio 服务器的 `env` 凭证

stdio 服务器（有 `command`）从**子进程环境变量**取密，没有「出境报文头」这一
拦截点。把真实口令换成 token 也没有意义 —— 第三方服务器期待的是口令本身。
因此：

- `env` 中的 `${cred:}` 仍按原逻辑解析写入 `.mcp.json`，**并在投射时打 WARN**；
- 该风险**在本文件显式记录**（issue 改动要求 #2 允许的另一条路径）。

> 现状：**没有任何内置场景走 env 占位符**。office-mail 早已改为 TOML 物化
> （见 §4.3），`wechat-mp.yaml` 注释里的 env 写法是文档化的备选形态。
> 因此实际投射产物中不会出现 env 明文。

**若未来要彻底关闭**：需给 stdio 服务器套一个 niuniu 提供的
`cred-exec` 包装器（`command` = `niuniu-mcp cred-exec --token <t> -- <real-cmd>`，
启动时向 loopback 换取真实 env 再 exec），把「文件里的明文」变成「spawn 时注入的
进程环境」。本次未做，原因是它要改动 `cmd/niuniu-mcp` 的启动/信号/Windows exec
语义，风险高于收益，而当前无场景需要它。

### 4.3 office-mail TOML：迁出工作区 + 拒绝读

`mcp-email-server` 只接受**文件路径**（`MCP_EMAIL_SERVER_CONFIG_PATH`），
文件里必须是可以直接登录 IMAP 的口令，没有任何注入点。因此改为：

1. **移出工作区**：`<wsDir>/.email-config/config.toml`
   → `<ownerRoot>/cred-material/workspaces/<wsID>/email-config.toml`
   （`OwnerRef.CredMaterialPath`，目录 0700 / 文件 0600）。投射时顺带删除旧位置的
   遗留文件（`removeLegacyEmailConfig`），避免升级后旧明文继续存在。
2. **拒绝读**：`MCPConfigGenerator.SetWorkspaceCredPathDeny` 把该目录的
   `Read`/`Edit`/`Write` 写入 `.claude/settings.json` 的 `permissions.deny`
   （带自有清单 `.niuniu-cred-deny.json`，重算时不动用户自撰规则）。

**残余风险**：`permissions.deny` 是 Claude **工具层**的闸门，不是 OS 隔离。
agent 若用任意子进程（python/node/curl）读该绝对路径仍可成功 —— 这与仓库里
KB 只读保护已承认的限制同一类（见 `mcp.go` 中 `kbReadonlyTools` 注释）。

### 4.4 残余风险：同 OS 用户的其它进程

解密后的凭证在 niuniu 进程内、按请求现解。任何同 OS 用户、能读该进程内存
或能读 `cred-material/` 的进程都能拿到明文。本方案**不**防御这一层（需要 OS
sandbox / keychain 级隔离，属另一议题）。

### 4.5 agent 拿到 token 意味着什么

agent 读 `.mcp.json` 会看到 token。它能做的：用该 token 向**那一个**上游发请求
并带上凭证 —— 也就是该 MCP 服务器本来就有的能力。它**不能**：

- 得到凭证本身（token 是密封的指针，不含密文；无法拿去别的服务、无法外带复用）；
- 指向别的上游（上游 URL 来自投射，不接受请求方输入）；
- 在场景移除 / 凭证解绑后继续使用（403）；也无法在 7 天后继续使用；
- 从**网络**上重放（loopback-only）。

注意 loopback-only 只能区分「本机 vs 网络」，不能区分「同一个 OS 用户下的哪个本地进程」：
另一个本地进程若拿到了 token，同样可以用它调用该上游。这与 §4.4 同一类限制。

这即是「能力指针」而非「凭证副本」的核心差别，也是本方案相对现状的净收益。

## 5. 取舍记录

| 决策 | 取舍 |
|---|---|
| 反代 loopback 而非把凭证交给 agent | 上游看到的源 IP 变成 niuniu 主机；换来明文不落盘 |
| token 放 URL path 而非自定义 header | 兼容 Claude Code 对 http/sse MCP 的 `url` 语义（它不发送自定义鉴权头）；代理只把**上游** URL 发出去，自身 token 不会出现在上游 access log |
| token = 密封指针，逐请求重解 | 每次 MCP 请求多一次 `Recompute` + 一次凭证解密（MCP 调用频率下可忽略）；换来重启无感、无吊销簿记、进程不驻留明文 |
| TTL 7 天（绝对值，不滑动） | 真正的边界是实时投射，TTL 只是二次防御；避免「短 TTL 打断长跑 agent」 |
| 拒绝**读**即可，未做 OS 隔离 | 见 §4.3 残余风险；与既有 KB 只读保护同构 |
| 不引入 `cred-exec` 包装器 | 见 §4.2；当前无场景需要，且风险高 |
| 复用 `call_external_api` 的 SSRF 防护？**否** | 该防护会阻断 loopback/私网，而场景上游（企业内部 KB）常是私网地址。上游 URL 是**场景作者写定的配置**（非 agent 输入），故使用普通 `http.Transport` |
| 复用集成主密钥密封 token | 与凭据落库共用一套密钥生命周期（OS keychain，#745）；代价是轮换主密钥会让既有 token 失效（下次投射即恢复） |
| loopback-only 限制 | 关掉了「远程 MCP 客户端」这一未来形态；本版本工作区与 server 同机，故成立。若将来支持远程 runner，需要显式放开并重新评估该边界 |

## 6. 验收与验证

**1. 投射文件不再出现明文**

对任一 office-mail / KB 场景工作区：

```bash
ws=<workspace 目录>
grep -rInE 'Bearer [A-Za-z0-9._-]{20,}|password *=' "$ws/.mcp.json" "$ws/.codex/config.toml" 2>/dev/null
# 期望：无输出。.mcp.json 里只应有非密字段：
#   "url": "http://127.0.0.1:<port>/cred-proxy/ncp_..."
#   "env": { "MCP_EMAIL_SERVER_CONFIG_PATH": "<owner>/cred-material/...", ... }
ls "$ws/.email-config" 2>/dev/null   # 期望：不存在（已迁出工作区）
```

由测试固化的证据链：

- `TestResolveProjectionCredentials_ProxiesHTTPHeaders`：投射产物无明文、url 被改写、
  密封 token 里连 server 名都读不出来；
- `TestResolveProjectionCredentials_HeaderWithoutProxyDropsServer`：无代理时**丢弃**
  而不是落明文；
- `mcp_test.go` 既有用例：`ExtraMCPServers` 原样写入 `.mcp.json`；
- `TestReprojectImapWorkspaces_RefreshesConfig`：TOML 不在工作区内、旧位置被清理。

**2. 既有 MCP server 仍可正常调用**

- `.mcp.json` 的 `url` 指向 loopback，`niuniu-mcp` 会话 token 机制不变；
- 已知一次性影响：升级后**首次投射**会让投射摘要变化 → 工作区会出现一次
  「需重启」提示；重启后 `url` 变为 loopback 形态，之后不再变化。
- 测试：
  `go test ./internal/service/ -run 'TestCredProxy|TestResolveProjectionCredentials|TestReprojectImapWorkspaces'`
  `go test ./internal/server/ -run TestCredProxyRoute`
- 代理转发覆盖：header 注入、inbound Authorization 被丢弃、query 合并、
  sub-path 追加、上游不可达返回 502 且不回显密钥、非 loopback 来源 403。

**3. 威胁模型成文**：本文件。

## 7. 相关代码索引

- 代理：`server/internal/service/cred_proxy.go`（密封指针 / 反代 / 出境注入）
- 目标重解：`server/internal/service/scene_projection_creds.go`
  （`ResolveProxyTarget`、`proxifyCredentialHeaders`）
- HTTP 入口：`server/internal/server/handler_cred_proxy.go`、`router.go`（公共路由）
- 邮箱材料：`server/internal/service/scene_projection_email.go`、
  `server/internal/service/owner_ref.go`（`CredMaterialPath`）
- 拒绝读：`server/internal/service/mcp.go`（`SetWorkspaceCredPathDeny`）
- 装配：`server/internal/server/server.go`（`NewCredProxyService`）
