# niuniu-agent

niuniu 的自研编码 agent（issue #708 / #709）。可行性分析与总体路线见
[docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md](../docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md)。

独立 Go 模块（挂入根 `go.work`），**零第三方依赖**，目标是单二进制分发（desktop sidecar 友好）。

## 当前状态：P3

- ✅ `-p` headless 单轮（`-p -` 读 stdin；`-y` 放行变更类工具）
- ✅ `acp` server：stdio JSON-RPC（initialize / session/new / session/prompt / session/update / session/request_permission / session/cancel），session cwd 经 chdir 生效
- ✅ 双协议模型层：Anthropic `/v1/messages` 兼容（含 Bearer 网关）、OpenAI `/chat/completions` 兼容
- ✅ 工具全集：`LS`、`Read`、`Grep`、`Glob`、`Write`、`Edit`、`Bash`、`TodoWrite`（零依赖、跨平台）
- ✅ 权限层：读/写分类；headless 默认拒绝变更类，`-y` 放行；ACP 路径走 request_permission 审批
- ✅ niuniu 引擎接入：`cli_type=niuniu`（agentbackend/niuniuagent 后端 + proxy 调度），真实二进制端到端验收通过
- ✅ P2 usage 链路：loop 逐轮聚合（含 cache 读/写分解）→ ACP `session/prompt` result 携带 → 服务端 `EventDone` tokens 落库；headless 结束时 stderr 打印 `[usage]` 汇总
- ✅ P2 prompt cache：Anthropic 族显式 `cache_control` 断点（system + 末位工具 + 末位消息，≤4）；GLM 网关缓存语义实测结论见 `internal/model/anthropic.go` 注释
- ✅ P2 system prompt：稳定前缀工程（身份→环境→工具指引→规则→项目上下文），session 内逐字节稳定以保缓存命中
- ✅ P2 项目上下文：session 启动读 cwd 的 `AGENTS.md`（退回 `CLAUDE.md`），上限 40KB，注入 system
- ✅ P2 auto-compact：上下文超阈值时摘要压缩早期消息（默认 120k tokens / 保留最近 12 条，`loop.Options` 可调），切点保证 tool_use/tool_result 配对完整
- ✅ P3 MCP client：零依赖 stdio client（initialize / tools-list / tools-call）；session 启动读 cwd `.mcp.json` 逐 server 拉起，工具以 `mcp__<server>__<tool>` 注册；单 server 失败告警跳过不阻断；server 生命周期随会话（进程）退出
- ✅ P3 skills：扫描 `<cwd>/.niuniu-agent/skills` 与 `~/.niuniu-agent/skills` 的 `*/SKILL.md`（frontmatter name/description，项目级遮蔽用户级）；system 注入仅 name+description 的索引；`Skill` 工具按名加载正文进上下文
- ✅ P3 subagent：`Agent` 工具起进程内子 Session（独立对话、复用模型与权限策略、子注册表无 Agent 工具→递归深度限 1）；sync 回填子最终文本 + `[subagent usage]` 行；单子 agent 超时上限（默认 10 分钟）
- ⏳ P4：token 级流式、hooks、session resume/checkpoint、sandbox、eval 集

## 用法

```bash
cd agent
go run ./cmd/niuniu-agent -p "列出当前目录下有哪些文件"
```

配置（环境变量，或用 `-provider` / `-model` 覆盖）：

| 变量 | 说明 |
|------|------|
| `NIUNIU_AGENT_PROVIDER` | `anthropic`（默认）/ `openai` |
| `ANTHROPIC_BASE_URL` | 默认 `https://api.anthropic.com`；GLM 等兼容网关填其地址 |
| `ANTHROPIC_AUTH_TOKEN` | Bearer 方式鉴权（Anthropic 兼容网关常用） |
| `ANTHROPIC_API_KEY` | `x-api-key` 方式鉴权 |
| `ANTHROPIC_MODEL` | 模型名（或 `-model` 传入） |
| `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL` | OpenAI 兼容协议同理 |

其他 flag：`-max-turns`（模型往返上限，默认 16）、`-timeout`（整体超时，默认 3m）。

## 布局

```
agent/
├── cmd/niuniu-agent/     CLI 入口（-p headless；acp server）
└── internal/
    ├── model/            中性消息 IR + anthropic/openai 双 adapter + env 配置
    ├── loop/             核心 agent loop + auto-compact + Agent 工具（subagent）
    ├── prompt/           system prompt 稳定前缀工程 + AGENTS.md/CLAUDE.md 加载
    ├── acp/              ACP server（stdio JSON-RPC）
    ├── mcp/              MCP stdio client（.mcp.json → mcp__<server>__<tool>）
    ├── skills/           SKILL.md 扫描/加载 + Skill 工具
    ├── perm/             权限层
    └── tools/            LS/Read/Grep/Glob/Write/Edit/Bash/TodoWrite
```

## Clean-room 纪律（硬约束）

Claude Code 是闭源软件。本模块的任何实现**不得**反编译、脱混淆或逐字复制
Claude Code 的代码、bundle 或 system prompt；允许的参考来源仅限公开文档、
公开发表的工程文章、正常使用中的行为观察，以及其他开源实现（opencode /
goose / pi / Gemini CLI 等）。system prompt 针对自家模型自行撰写与调优。

## 测试

```bash
cd agent && go test ./...
```
