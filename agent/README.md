# niuniu-agent

niuniu 的自研编码 agent（issue #708 / #709）。可行性分析与总体路线见
[docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md](../docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md)。

独立 Go 模块（挂入根 `go.work`），**零第三方依赖**，目标是单二进制分发（desktop sidecar 友好）。

## 当前状态：P1

- ✅ `-p` headless 单轮（`-p -` 读 stdin；`-y` 放行变更类工具）
- ✅ `acp` server：stdio JSON-RPC（initialize / session/new / session/prompt / session/update / session/request_permission / session/cancel），session cwd 经 chdir 生效
- ✅ 双协议模型层：Anthropic `/v1/messages` 兼容（含 Bearer 网关）、OpenAI `/chat/completions` 兼容
- ✅ 工具全集：`LS`、`Read`、`Grep`、`Glob`、`Write`、`Edit`、`Bash`、`TodoWrite`（零依赖、跨平台）
- ✅ 权限层：读/写分类；headless 默认拒绝变更类，`-y` 放行；ACP 路径走 request_permission 审批
- ✅ niuniu 引擎接入：`cli_type=niuniu`（agentbackend/niuniuagent 后端 + proxy 调度），真实二进制端到端验收通过
- ⏳ P2：token 级流式、真实 usage/成本上报、MCP client、skills、session resume、compact

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
├── cmd/niuniu-agent/     CLI 入口（-p headless；acp 占位）
└── internal/
    ├── model/            中性消息 IR + anthropic/openai 双 adapter + env 配置
    ├── loop/             核心 agent loop
    └── tools/            工具注册表 + LS/Read
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
