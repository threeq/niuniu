# niuniu-agent

niuniu 的自研编码 agent（issue #708 / #709）。可行性分析与总体路线见
[docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md](../docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md)。

独立 Go 模块（挂入根 `go.work`），**零第三方依赖**，目标是单二进制分发（desktop sidecar 友好）。

## 当前状态：P7

- ✅ `-p` headless 单轮（`-p -` 读 stdin；`-y` 放行变更类工具）
- ✅ `acp` server：stdio JSON-RPC（initialize / session/new / session/prompt / session/update / session/request_permission / session/cancel），session cwd 经 chdir 生效
- ✅ 双协议模型层：Anthropic `/v1/messages` 兼容（含 Bearer 网关）、OpenAI `/chat/completions` 兼容
- ✅ 工具全集：`LS`、`Read`、`Grep`、`Glob`、`Write`、`Edit`、`Bash`、`TodoWrite` / `HistorySearch`（检索被压缩归档的历史）（零依赖、跨平台）
- ✅ 权限层：读/写分类；headless 默认拒绝变更类，`-y` 放行；ACP 路径走 request_permission 审批
- ✅ niuniu 引擎接入：`cli_type=niuniu`（agentbackend/niuniuagent 后端 + proxy 调度），真实二进制端到端验收通过
- ✅ P2 usage 链路：loop 逐轮聚合（含 cache 读/写分解）→ ACP `session/prompt` result 携带 → 服务端 `EventDone` tokens 落库；headless 结束时 stderr 打印 `[usage]` 汇总
- ✅ P2 prompt cache：Anthropic 族显式 `cache_control` 断点（system + 末位工具 + 末位消息，≤4）；GLM 网关缓存语义实测结论见 `internal/model/anthropic.go` 注释
- ✅ P2 system prompt：稳定前缀工程（身份→环境→工具指引→规则→项目上下文），session 内逐字节稳定以保缓存命中
- ✅ P2 项目上下文：session 启动读 cwd 的 `AGENTS.md`（退回 `CLAUDE.md`），上限 40KB，注入 system
- ✅ P2 auto-compact：上下文超阈值时摘要压缩早期消息（默认 120k tokens / 保留最近 12 条，`loop.Options` 可调），切点保证 tool_use/tool_result 配对完整；压缩摘要已升级为**结构化状态**（fixed-schema JSON）：key_decisions/files_touched 跨次压缩累积去重、open_items 每次刷新，杜绝「摘要的摘要」连锁失真；状态同步落盘 `.niuniu-agent/session-state.json`（compact 后消息带指针，agent 可 Read 回取精确细节），模型输出非 JSON 时回退纯文本摘要；被压缩消息**逐条归档** `.niuniu-agent/history/`，`HistorySearch` 工具按关键词检索回注精确细节（上下文只背压缩态，归档背全量）
- ✅ P3 MCP client：零依赖 stdio client（initialize / tools-list / tools-call）；session 启动读 cwd `.mcp.json` 逐 server 拉起，工具以 `mcp__<server>__<tool>` 注册；单 server 失败告警跳过不阻断；server 生命周期随会话（进程）退出
- ✅ P3 skills：扫描 `<cwd>/.niuniu-agent/skills` 与 `~/.niuniu-agent/skills` 的 `*/SKILL.md`（frontmatter name/description，项目级遮蔽用户级）；system 注入仅 name+description 的索引；`Skill` 工具按名加载正文进上下文
- ✅ P3 subagent：`Agent` 工具起进程内子 Session（独立对话、复用模型与权限策略、子注册表无 Agent 工具→递归深度限 1）；sync 回填子最终文本 + `[subagent usage]` 行；单子 agent 超时上限（默认 10 分钟）
- ✅ P4 原生记忆：`~/.niuniu-agent/memory` + `<cwd>/.niuniu-agent/memory` 双层 markdown 存储（title 去重、单条 8KB / 每层 200 条防污染）；`MemorySave`/`MemorySearch` 工具；启动评分召回 top-N 注入 system 的 Memory 段（ADVISORY 定位，字节上限）；`-reflect` 回合后可选反射提炼（同 title 去重，默认关）；纯 env+本地文件独立运行，不依赖 niuniu
- ✅ P4 能力自动注入：agent 侧 session 启动加载 `<cwd>/.niuniu-agent/inject.md`（40KB 上限）进 system 的 Host capabilities 段；server 侧为 `cli_type=niuniu` 工作空间投影 inject.md（niuniu-mcp 四族工具说明 + AUTOHOST_DONE 收尾约定 + 看板纪律 + 记忆互通指引）并重生成含 niuniu-mcp 的 `.mcp.json`（与 claude 引擎同一生成器）；投影失败不阻断
- ✅ P5 thinking 全链路：双 adapter 解析思考块（anthropic thinking+signature 原样回传 / openai reasoning_content）→ ACP `agent_thought_chunk` → 服务端 EventThinking 落库；`NIUNIU_AGENT_THINKING`（off|low|medium|high|<tokens>）预算/effort 透传；headless stderr `[thinking]` 行
- ✅ P5 subagent 共享/隔离：cwd/system 继承钉住、ContextPreamble+context 叠加、background=true + AgentResult 轮询、同回合多 Agent 并行；窗口隔离（仅报告回填）、compact 继承、报告 16KB 截断、TodoWrite 等排除清单、召回减半
- ✅ P5 长任务：后台 Bash（run_in_background + BashOutput 轮询）、session 持久化 `.niuniu-agent/sessions/` + `-resume <id|latest>`、compact 摘要三节结构化（Background/Key decisions/Open items）
- ✅ P5 缓存精细化：usage 行 cache-hit 命中率、增量消息断点（cache_control 落倒数第二条消息，跨轮字节稳定才命中）、prompt 防抖规则成文（见 internal/prompt 包注释）
- ✅ P6 token 级流式：双族 SSE 解析（Request.Stream 增量回调，tool_use 分片聚合），loop/ACP chunk 增量化；`NIUNIU_AGENT_STREAM` 默认开，headless 打 `[stream] first-token` 时延；`NIUNIU_AGENT_CONTEXT_EDITING=1` 启用 Anthropic 服务端 context editing（clear_tool_uses，服务端自动清旧工具结果；openai 族忽略该开关，走本地逐出）
- ✅ P6 多模态：IR image 块（user/tool_result 均可携带），Read 图片（ImageResult 接口），anthropic source / openai image_url 双族线格式，ACP image block 接入
- ✅ P6 WebFetch/WebSearch：零依赖抓取 + HTML→文本（20KB 截断）；SSRF 硬防护（重定向逐跳公网校验，私网/环回全拒）；WebSearch 可配 provider（duckduckgo 无 key），未配置报指引
- ✅ P6 子 agent 类型化：内置 explore/plan/worker/reviewer（工具白名单+角色前缀+模型档位），`.niuniu-agent/agents/*.md` 声明式自定义，Agent 工具 `subagent_type` 入参
- ✅ P6 记忆 consolidate：同主题合并/老化清理/容量 LRU 三趟清理；MemoryConsolidate 工具 + `memory-consolidate` CLI 子命令
- ✅ P6 eval 评估体系：`eval/tasks/*.md` 任务集（20 个脱敏任务）、`niuniu-agent eval` runner（一次性沙箱 + 规则判定 contains/file-exists/command-exit-0 等）、JSON+markdown 报告与 baseline.json 对比
- ✅ P7 RSI 探索式预热：explore 三角色（Curriculum 自生成练习任务 → Actor 沙箱执行（记忆冻结）→ Verifier 规则判定零模型调用），广-深两阶段可配，仅 verified pass 沉淀接地经验；方法借鉴 RSIAgent（Apache-2.0），见 ATTRIBUTION.md
- ✅ P8a 适应度门控自进化：eval 公开/私有拆分（迭代用公开集、采纳看私有集零容差）、think-first 提案协议（缺段拒绝）、对抗复验（均值仍胜才加冕）、PROMPT.md 动态任务指引（32KB 上限、缺失跳过、安全边界成文）；方法融合 OpenRSI 思路，见 ATTRIBUTION.md
- ⏳ P8+：hooks、sandbox、checkpoint/rewind、交互式 TUI、desktop sidecar 打包

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

其他 flag：`-max-turns`（模型往返上限，默认 16）、`-timeout`（整体超时）、`-profile`（选择 config.json 里的档案）、`-config`（profile 配置路径覆盖）、`-print-system`（stderr 打印合成后的 system）、`-reflect`（回合后反射沉淀经验）、`-resume`（续跑会话）。

### 多 Profile 配置（config.json）

查找顺序：`--config <path>` > `<cwd>/.niuniu-agent/config.json` > `~/.niuniu-agent/config.json`。

```json
{
  "defaultProfile": "glm",
  "profiles": {
    "glm": {
      "provider": "anthropic",
      "baseURL": "https://open.bigmodel.cn/api/anthropic",
      "authTokenEnv": "GLM_TOKEN",
      "model": "GLM-5.3-Flash",
      "thinking": "off"
    },
    "local-openai": {
      "provider": "openai",
      "baseURL": "http://localhost:11434/v1",
      "apiKeyEnv": "LOCAL_OPENAI_KEY",
      "model": "qwen2.5-coder:7b"
    }
  }
}
```

⚠️ **密钥安全**：配置文件会被提交进仓库——**永不写入明文密钥**。凭据只存
环境变量，配置里用 `apiKeyEnv`/`authTokenEnv` 引用 env 名；加载器遇到
`apiKey`/`authToken` 等明文字段直接拒绝。

优先级（高→低）：CLI flag（`-profile`/显式 `-provider`/`-model`/`-base-url`）>
env（workspace 注入的 `ANTHROPIC_*/OPENAI_*` 与 `NIUNIU_AGENT_PROFILE`）>
项目级 config > 用户 env > 全局 config > 内置默认。

子命令：
- `niuniu-agent profiles` —— 列出 profiles 与当前生效者（凭据只显示 env 名与 set/unset）
- `niuniu-agent eval -auto-explore` —— 重复失败模式自动触发 RSI explore
- `niuniu-agent memory-consolidate` —— 记忆整理
- `niuniu-agent explore -gated` —— 全飞轮（Measure→Evolve→三道安全闸门→Land→Control）

新供应商家族：实现 `model.Model` 接口并 `model.RegisterProvider(name, factory)`
注册，profile 的 `provider` 字段直接引用注册名。

## 布局

```
agent/
├── cmd/niuniu-agent/     CLI 入口（-p headless；acp server；eval；memory-consolidate）
├── eval/tasks/           评测任务集（脱敏、自带 fixture 与规则判定）
└── internal/
    ├── model/            中性消息 IR + anthropic/openai 双 adapter + env 配置
    ├── loop/             核心 agent loop + auto-compact + Agent 工具（subagent）
    ├── prompt/           system prompt 稳定前缀工程 + AGENTS.md/CLAUDE.md 加载
    ├── acp/              ACP server（stdio JSON-RPC）
    ├── mcp/              MCP stdio client（.mcp.json → mcp__<server>__<tool>）
    ├── eval/             评测 runner（沙箱执行 + 规则判定 + 报告/基线）
    ├── rsi/              RSI 探索式预热（Curriculum/Actor/Verifier 三角色）
    ├── skills/           SKILL.md 扫描/加载 + Skill 工具
    ├── memory/           原生记忆（双层存储/召回/反射 + MemorySave/Search）
    ├── perm/             权限层
    ├── tools/            全套工具（含 WebFetch/WebSearch，SSRF 防护）
    └── perm/             权限层
```

## 归属

- RSI 探索式预热：方法思路借鉴 RSIAgent（AetherLabsAI，Apache-2.0）。
- 适应度门控自进化 / think-first 提案：方法思路借鉴 OpenRSI
  （AlexWortega/OpenRsi，仓库未附 LICENSE，仅注明方法来源，不复制代码）。

均为 idea 级借鉴 + 独立 clean-room 实现，详见 [ATTRIBUTION.md](ATTRIBUTION.md)。

## Clean-room 纪律（硬约束）

Claude Code 是闭源软件。本模块的任何实现**不得**反编译、脱混淆或逐字复制
Claude Code 的代码、bundle 或 system prompt；允许的参考来源仅限公开文档、
公开发表的工程文章、正常使用中的行为观察，以及其他开源实现（opencode /
goose / pi / Gemini CLI 等）。system prompt 针对自家模型自行撰写与调优。

## 测试

```bash
cd agent && go test ./...
```
