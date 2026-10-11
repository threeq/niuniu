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
    "strategy": "query_param",                 // bearer|basic|custom_header|query_param|imap|(未来 oauth2_refresh|sigv4|mtls)
    "header": "",                              // header 类策略的注入头名
    "prefix": "",                              // 值前缀，如 "Bearer"
    "param": "api_key",                        // strategy=query_param 时的查询参数名
    "note": "injected by the proxy — do NOT include it yourself."
  },
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
