# 用户自配知识库 MCP + 行业预设（子任务B）

护城河第 2 项（本地数据主权 / 经营数据复利）的知识供给，**不由牛牛自建语义 KB
内核**（那是 #626 路线，已降级为可选/延后），而是让用户把自己的/行业的知识库以
**MCP 服务器**形式配进 scene，牛牛只负责**编排调用**——数据始终在用户 MCP 后面，
牛牛不碰数据本身。

## 组成

1. **行业知识库预设**（`docs/scenes/builtin/kb-*.yaml`，随二进制 embed）
   - `kb-ecommerce`（电商）、`kb-legal`（法律）、`kb-medical`（医疗）、`kb-custom`（通用/自有）。
   - 每个预设声明一台 **http 传输的 KB MCP server**（名为 `kb`），鉴权头写成
     `Authorization: Bearer ${cred:kb-api.token}`；并声明 `required_credentials`
     （alias `kb-api`，provider `knowledge-base`）。行业差异体现在检索纪律 prompt、
     quick_actions 与推荐 match 规则。
   - 都带 `knowledge-base` 标签，供 UI 作为「行业预设」一键罗列。

2. **凭据注入（credstore）**
   - `SceneProjector.resolveProjectionCredentials` 在投射时把 MCP 配置里
     `env` **与 `headers`** 两个子映射中的 `${cred:<alias>.<field>}` 占位符，用
     `(owner, workspace-creator, provider, alias)` 维度从 credstore 解密后就地替换
     （见 `internal/service/scene_projection_creds.go`）。
   - token **永不**落在场景定义或投射缓存里；任一占位符解析失败则**整台 server 被丢弃**
     （绝不写半填鉴权），并在缺失凭据卡片里报出该 alias。
   - `env` 供 stdio server；`headers` 供 http/sse server（KB MCP 走这条）。本子任务把
     注入从「仅 env」扩展到「env + headers」，是 http KB MCP 头鉴权能用 credstore 的关键。

3. **编排调用**
   - 解析后的 KB MCP 配置（含 `type/url/headers`）由 `MCPConfigGenerator` **原样写入**
     工作空间 `.mcp.json` 的 `mcpServers`（`internal/service/mcp.go`）。
   - scene 挂到 workspace 后，Claude agent 即可用该 KB MCP 的检索工具查询用户私有知识。

## 低门槛入口（UI）

场景列表页（`/scenes`）右上「配置知识库 MCP」按钮打开引导对话框
（`server/web/src/pages/scenes/components/kb-mcp-dialog.tsx`）：

1. 选一个行业预设卡片；
2. 填「知识库名称 / KB MCP 端点 URL / API 凭据」；
3. 一键完成：`fork` 预设 → 把 `kb` server 的 `url` 指向用户端点、鉴权头改用**每场景
   唯一 alias**（`kb-<slug>`，避免多个 KB 的 token 撞 alias）→ 把 token 存入 credstore。

纯转换逻辑抽在 `kb-mcp-config.ts`（含 Vitest 单测），对话框只做 API 编排。

## 测试

- 后端：`internal/service/scene_projection_creds_test.go`（http headers 注入 + 缺失丢弃）、
  `internal/service/scene_kb_preset_test.go`（4 个预设的 embed + 契约形状）。
- 前端：`server/web/src/pages/scenes/components/kb-mcp-config.test.ts`（定义改写/凭据体/校验）。
