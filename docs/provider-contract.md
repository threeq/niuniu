# Provider 集成契约（Provider Contract）

外部 API Provider 的**机器可读集成契约**：agent 通过 `get_provider_schema` 读到它，
即可在不翻上游文档的前提下驱动该 Provider。契约是 LocalRouter「Pack」机制的本地实现。

实现：`server/internal/service/provider_contract.go`。

## 存储与兼容

- 契约是一份 JSON 文档，存放在既有 `external_providers.profile` 列（**不加列、不改 DDL**）。
- 该列历史上存过自由文本或松散 JSON（`{"provider","base_url","auth","endpoints":[…]}`）。
  `ParseContract` 按三种形态解析，**旧行照常可读**：
  1. 结构化契约（含 `operations` 或 `schema_version` 键）→ 直接解析；
  2. 旧 JSON profile（含 `endpoints`）→ 端点尽力转换为 operations（名称由 method+path 派生，
     `read_only` 按 method 粗判，`auth` 文本保留到 `auth.note`）；
  3. 自由文本 / 空 / 无法识别的 JSON → operations 为空，原文保留在 `legacy_profile`。
- `get_provider_schema` 同时返回旧字段（含原样 `profile`）与新的结构化 `contract`，
  旧 provider 不崩。

## Schema（schema_version = "1"）

```jsonc
{
  "schema_version": "1",
  "provider": "SerpAPI",                       // 展示名
  "base_url": "https://serpapi.com",
  "auth": {                                    // 代理在出口注入的鉴权要求（非密钥本体）
    "strategy": "query_param",                 // 见下「鉴权策略族」；也可用顶层 auth_strategy 平铺声明
    "header": "",                              // header 类策略的注入头名
    "prefix": "",                              // 值前缀，如 "Bearer"
    "param": "api_key",                        // strategy=query_param 时的查询参数名
    "note": "injected by the proxy — do NOT include it yourself."
  },
  "auth_strategy": "oauth2_refresh",           // 可选：auth.strategy 的平铺别名（二者等价，解析时折叠）
  "capabilities": ["serp", "keyword-ranking"], // 能力标签：发现与候选对照
  "verify_operation": "search",                // 廉价、幂等、只读的验证探针（按 operation.name 引用）
  "operations": [
    {
      "name": "search",                        // 稳定语义名
      "method": "GET",
      "path": "/search",                       // 相对 base_url；占位符用 {name}
      "read_only": true,                       // 只读/写（粗筛；POST 也可只读，但会给出 lint 警告）
      "summary": "Run a search-engine query.",
      "parameters": [
        { "name": "q", "in": "query", "type": "string", "required": true, "description": "the query" }
      ],
      "request_example": { "engine": "google", "q": "best crm" },
      "response_shape": "organic_results[] each {position,title,link,snippet}",
      "notes": "..."
    }
  ],
  "notes": "",
  "digest": "sha256:…",                        // 见下：读取时重算，落库值不作数
  "legacy_profile": ""                         // 仅当 profile 为自由文本时保留原文
}
```

`parameters[].in` ∈ `query | path | body | header`。

## digest

`digest = "sha256:" + hex(sha256(json(contract, digest 字段置空)))`。

- 内容相同 ⇒ digest 相同；内容变化 ⇒ digest 变化（用于漂移感知，见 #744）。
- **服务端每次读取都重算**（`ProviderDef.Contract()`），因此落库里的旧 digest 永不会被原样返回。

## 校验（lint 入口）

`LintContract` 返回结构化 findings（`severity` = `error` | `warning`），供草稿流水线（#734）
在发布前复用；`ValidateContract` 在存在 `error` 时返回错误、`warning` 不阻塞。

规则：

- `operations` 非空；
- 每个 operation：名称唯一且非空、HTTP method 合法、path 以 `/` 开头；
- 每个参数：名称非空、`in` 合法；
- `verify_operation`（若声明）必须指向**存在**且**只读**的 operation（探针不得有副作用，见 #732）；
- 非 GET/HEAD 的 operation 声明 `read_only` → **warning**（如 GSC `searchAnalytics/query` 以 POST 只读；
  粗筛无法区分，人工确认兜底）。

## 契约化样例

内置 Provider **SerpAPI** 与 **Google Search Console** 的 profile 已改写为结构化契约
（`server/internal/service/external_provider.go` 的 `serpAPIProfile` / `gscProfile`），
作为 schema 的实际样例，并由 `TestLintContract_BuiltinSeedsValid` 保证其可发布。

## 鉴权策略族（issue #737）

契约只声明**策略名**（`auth.strategy` 或平铺的 `auth_strategy`），实现与参数全部留在服务端。
参数（`token_url` / `client_secret` / `secret_key` / `region` / 证书 PEM …）随凭证一起
**加密存储**（integration keyring），agent 既看不到也传不了。策略解析优先级：

1. 凭证 `auth_strategy`（最具体、且承载策略参数——避免「凭证已配 OAuth2 却被静态契约静默覆盖」）；
2. 契约 `auth.strategy`（凭证未声明时以契约为准）；3. 凭证 `auth_mode`（旧）；4. provider `auth_type`。

| strategy | 说明 | 所需凭证参数（加密） |
|----------|------|----------------------|
| `bearer` / `basic` / `custom_header` / `query_param` / `imap` | 静态注入（**与 #737 之前逐字节一致**，回归兼容） | 见既有凭证形状 |
| `oauth2_refresh`（别名 `oauth2` / `oauth2_client_credentials` / `oauth2_authorization_code`） | OAuth2：客户端凭证 / 刷新令牌授权，refresh token 由服务端持有并**自动换取**新 token 注入；access token 按凭证缓存、到期前自动续期，遇 401 强制刷新重试一次 | `token_url`、`client_id`、`client_secret`（或 `token_auth: body`）、`refresh_token`（刷新授权）或 `grant_type: client_credentials`，可选 `scope`。仅 `access_token` 无 `token_url` 时按静态令牌直传 |
| `hmac`（别名 `hmac_sha256` / `hmac_sha1`） | 请求签名：默认对 `METHOD\n/path\nrawquery\ntimestamp\nsha256(body)` 做 HMAC，写入 `hmac_header`（默认 `X-Signature`）与时间戳头；`hmac_sign: body` 切换为仅签 body（GitHub/Slack webhook 形态） | `hmac_secret`、可选 `hmac_key_id` / `hmac_algorithm` / 头名 |
| `sigv4`（别名 `aws_sigv4`） | AWS Signature Version 4（标准算法，已被 AWS 官方向量验证） | `access_key`、`secret_key`、`region`、`service`，可选 `session_token` |
| `mtls` | 双向 TLS：客户端证书仅在握手期出示，不经 header；代理为本调用换用带证书的 client（克隆 SSRF 传输层，不削弱地址防护） | `client_cert_pem`、`client_key_pem`，可选 `ca_cert_pem` |

未知策略名**降级为静态**（不 fail-closed），因此未声明策略的旧 provider 行为不变。

## 操作级授权（issue #737）

授权对象从 URL glob 升级为**契约里的 operations**（语义名，如 `checkOrder` / `cancelOrder`），
代理在**出口**强制。调用方式两种：

- **按 operation 名**（推荐）：`call_external_api(provider, operation, query/body)`。代理从契约
  解析 method+path（`{name}` 占位符从 query/body 取值并折叠进 path），并按 operation 授权。
- **按 method+path**（旧）：代理把请求反查到契约 operation，命中则按 operation 授权。

授权判定（写开关 ON 时全部放行；OFF 时）：

| 情况 | 结果 |
|------|------|
| `operation` 名不在契约中 | 拒绝，`unknown_operation`（reason 含该 operation 名） |
| 命中的 operation `read_only: true` | 放行 |
| 命中的 operation `read_only: false`（写/敏感） | 拒绝，`operation_requires_write`（reason 含 operation 名 + 路径） |
| 未命中任何 operation（旧 provider / 契约外调用） | 回退到既有 method+path 白名单（**不收紧**，旧 provider 零回归） |

`get_provider_schema` 额外返回 `read_operations` / `write_operations` 两个名字列表
（**写/敏感操作单列**），供确认卡与写开关按操作粒度控制。数据源侧的 `scope_config`
（`tables_allow` / `tables_deny`）是同一「授权对象即声明式清单」语义在数据线上的对应物：
Provider 线用 operations、数据线用 tables，二者共用出口强制 + 审计写入 + 凭证注入纪律。

## 端到端验证

`server/internal/service/auth_strategy_test.go` 对每个策略给出真实系统级验证：

- **OAuth2**：token 端点强制 `refresh_token` 授权 + Basic 客户端认证，API 只接受当前有效 token；
  1 秒 TTL 下第二次调用自动续期并被接受。
- **签名**：HMAC 与 SigV4 各自对**独立重算签名**的校验服务发请求并被接受；SigV4 另钉住 AWS 官方
  SigV4 test-suite 的 `get-vanilla` 已知答案向量（签名逐字节一致）。
- **mTLS**：要求客户端证书的 TLS 服务——无证书被拒、带凭证证书握手通过。
- **静态**：策略输出与旧 `buildAuthHeader` / `buildAuthQuery` 逐例对比（零回归守卫）。
- **操作级授权**：`external_proxy_operation_test.go` 覆盖未授权 operation 被拒且 reason 明确。
