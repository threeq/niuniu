# niuniu-agent

niuniu 的自研编码 agent（issue #708 / #709）。可行性分析与总体路线见
[docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md](../docs/specs/2026-09-15-niuniu-agent-feasibility-and-plan.md)。

独立 Go 模块（挂入根 `go.work`），**零第三方依赖**，目标是单二进制分发（desktop sidecar 友好）。

## 当前状态：P7

- ✅ `-p` headless 单轮（`-p -` 读 stdin；`-y` 放行变更类工具）
- ✅ `acp` server：stdio JSON-RPC（initialize / session/new / session/prompt / session/update / session/request_permission / session/cancel），session cwd 经 chdir 生效
- ✅ 双协议模型层：Anthropic `/v1/messages` 兼容（含 Bearer 网关）、OpenAI `/chat/completions` 兼容
- ✅ 工具全集：`LS`、`Read`、`Grep`、`Glob`、`Write`、`Edit`、`Bash`、`TodoWrite` / `HistorySearch`（检索被压缩归档的历史） / `Monitor`（后台任务事件同步）/ `LSP`（definition/references/hover/symbols 导航）（零依赖、跨平台）
- ✅ 权限层：读/写分类；headless 默认拒绝变更类，`-y` 放行；ACP 路径走 request_permission 审批
- ✅ niuniu 引擎接入：`cli_type=niuniu`（agentbackend/niuniuagent 后端 + proxy 调度），真实二进制端到端验收通过
- ✅ P2 usage 链路：loop 逐轮聚合（含 cache 读/写分解）→ ACP `session/prompt` result 携带 → 服务端 `EventDone` tokens 落库；headless 结束时 stderr 打印 `[usage]` 汇总
- ✅ P2 prompt cache：Anthropic 族显式 `cache_control` 断点（system + 末位工具 + 末位消息，≤4）；GLM 网关缓存语义实测结论见 `internal/model/anthropic.go` 注释
- ✅ P2 system prompt：稳定前缀工程（身份→环境→工具指引→规则→项目上下文），session 内逐字节稳定以保缓存命中
- ✅ P2 项目上下文：session 启动读 cwd 的 `AGENTS.md`（退回 `CLAUDE.md`），上限 40KB，注入 system
- ✅ P2 auto-compact：上下文超阈值时摘要压缩早期消息（默认 120k tokens / 保留最近 12 条，`loop.Options` 可调），切点保证 tool_use/tool_result 配对完整；压缩摘要已升级为**结构化状态**（fixed-schema JSON）：key_decisions/files_touched 跨次压缩累积去重、open_items 每次刷新，杜绝「摘要的摘要」连锁失真；状态同步落盘 `~/.niuniu-agent/projects/<escaped-cwd>/session-state.json`（compact 后消息带指针，agent 可 Read 回取精确细节），模型输出非 JSON 时回退纯文本摘要；被压缩消息**逐条归档** `~/.niuniu-agent/projects/<escaped-cwd>/history/`，`HistorySearch` 工具按关键词检索回注精确细节（上下文只背压缩态，归档背全量）
- ✅ P3 MCP client：零依赖 stdio client（initialize / tools-list / tools-call）；session 启动读 cwd `.mcp.json` 逐 server 拉起，工具以 `mcp__<server>__<tool>` 注册；单 server 失败告警跳过不阻断；server 生命周期随会话（进程）退出
- ✅ P3 skills：扫描 `<cwd>/.niuniu-agent/skills` 与 `~/.niuniu-agent/skills` 的 `*/SKILL.md`（frontmatter name/description，项目级遮蔽用户级）；system 注入仅 name+description 的索引；`Skill` 工具按名加载正文进上下文
- ✅ P3 subagent：`Agent` 工具起进程内子 Session（独立对话、复用模型与权限策略、子注册表无 Agent 工具→递归深度限 1）；sync 回填子最终文本 + `[subagent usage]` 行；单子 agent 超时上限（默认 10 分钟）
- ✅ P4 原生记忆：`~/.niuniu-agent/memory` + `<cwd>/.niuniu-agent/memory` 双层 markdown 存储（title 去重、单条 8KB / 每层 200 条防污染）；`MemorySave`/`MemorySearch` 工具（支持 domain / lifecycle / expires_at 入参与过滤）；启动评分召回 top-N 注入 system 的 Memory 段（ADVISORY 定位，字节上限，closed 生命周期与过期条目停注入）；当轮纠错协议（旧条目 deprecated + 新事实另存）；`-reflect` 回合后可选反射提炼（同 title 去重，默认关，纠错已当轮落地的不再二次提炼）；纯 env+本地文件独立运行，不依赖 niuniu——字段表、生命周期流转、纠错协议、印象层详见下文「[P4 原生记忆：schema 与语义](#p4-原生记忆schema-与语义)」
- ✅ P4 能力自动注入：agent 侧 session 启动加载 `<cwd>/.niuniu-agent/inject.md`（40KB 上限）进 system 的 Host capabilities 段；server 侧为 `cli_type=niuniu` 工作空间投影 inject.md（niuniu-mcp 四族工具说明 + AUTOHOST_DONE 收尾约定 + 看板纪律 + 记忆互通指引）并重生成含 niuniu-mcp 的 `.mcp.json`（与 claude 引擎同一生成器）；投影失败不阻断
- ✅ P4 项目印象层：跨 session 印象文件 `~/.niuniu-agent/projects/<escaped-cwd>/impression.md`（技术栈/关键决策/用户脾气/当前阶段，≤200 字硬约束——写入侧与读取侧双重截断）；compact 时由同一次压缩摘要调用顺带刷新（summarizer 输出 JSON 增加 `impression` 字段，零额外 token），链式压缩沿用上一版防丢；新 session 启动注入 system 前缀末尾可变区（在 AGENTS.md 项目上下文与 Memory 召回段之间，条目级细节仍归 memory）；文件缺失/损坏（非法 UTF-8）/生成失败一律跳过不报错、不影响会话
- ✅ P5 thinking 全链路：双 adapter 解析思考块（anthropic thinking+signature 原样回传 / openai reasoning_content）→ ACP `agent_thought_chunk` → 服务端 EventThinking 落库；`NIUNIU_AGENT_THINKING`（off|low|medium|high|<tokens>）预算/effort 透传；headless stderr `[thinking]` 行
- ✅ P5 subagent 共享/隔离：cwd/system 继承钉住、ContextPreamble+context 叠加、background=true + AgentResult 轮询、同回合多 Agent 并行；窗口隔离（仅报告回填）、compact 继承、报告 16KB 截断、TodoWrite 等排除清单、召回减半
- ✅ P5 长任务：后台 Bash（run_in_background + BashOutput 轮询）、session 持久化 `~/.niuniu-agent/projects/<escaped-cwd>/sessions/`（Claude-Code 式用户目录布局，不污染项目）+ `-resume <id|latest>`、compact 摘要三节结构化（Background/Key decisions/Open items）
- ✅ P5 缓存精细化：usage 行 cache-hit 命中率、增量消息断点（cache_control 落倒数第二条消息，跨轮字节稳定才命中）、prompt 防抖规则成文（见 internal/prompt 包注释）
- ✅ P6 token 级流式：双族 SSE 解析（Request.Stream 增量回调，tool_use 分片聚合），loop/ACP chunk 增量化；`NIUNIU_AGENT_STREAM` 默认开，headless 打 `[stream] first-token` 时延；`NIUNIU_AGENT_CONTEXT_EDITING=1` 启用 Anthropic 服务端 context editing（clear_tool_uses，服务端自动清旧工具结果；openai 族忽略该开关，走本地逐出）
- ✅ P6 多模态：IR image 块（user/tool_result 均可携带），Read 图片（ImageResult 接口），anthropic source / openai image_url 双族线格式，ACP image block 接入
- ✅ P6 WebFetch/WebSearch：零依赖抓取 + HTML→文本（20KB 截断）；SSRF 硬防护（重定向逐跳公网校验，私网/环回全拒）；WebSearch 可配 provider（duckduckgo 无 key），未配置报指引
- ✅ P6 子 agent 类型化：内置 explore/plan/worker/reviewer（工具白名单+角色前缀+模型档位），`.niuniu-agent/agents/*.md` 声明式自定义，Agent 工具 `subagent_type` 入参
- ✅ P6 记忆 consolidate：同主题合并/老化清理/容量 LRU 三趟清理；MemoryConsolidate 工具 + `memory-consolidate` CLI 子命令
- ✅ P6 eval 评估体系：`eval/tasks/*.md` 任务集（27 个脱敏任务，其中 21-27 为记忆行为语义用例：状态 vs 偏好（不改旧偏好 / 不落临时状态两侧）/ 当轮纠错（文件协议+行为同步）/ 过期停引 / 分域保存 / open_item 回访）、`niuniu-agent eval` runner（一次性沙箱 + 规则判定 contains/not-contains/file-exists/file-count/command-exit-0 等，沙箱 `.niuniu-agent/memory` fixtures 走真实召回注入路径）、JSON+markdown 报告与 baseline.json 对比
- ✅ P7 RSI 探索式预热：explore 三角色（Curriculum 自生成练习任务 → Actor 沙箱执行（记忆冻结）→ Verifier 规则判定零模型调用），广-深两阶段可配，仅 verified pass 沉淀接地经验；方法借鉴 RSIAgent（Apache-2.0），见 ATTRIBUTION.md
- ✅ P8a 适应度门控自进化：eval 公开/私有拆分（迭代用公开集、采纳看私有集零容差）、think-first 提案协议（缺段拒绝）、对抗复验（均值仍胜才加冕）、PROMPT.md 动态任务指引（32KB 上限、缺失跳过、安全边界成文）；方法融合 OpenRSI 思路，见 ATTRIBUTION.md
- ✅ 差异化壁垒（路线图表述，长期保留）：P7/P8a 的 eval 门控自进化（公开/私有评估集、对抗复验、零容差采纳）是全部竞品都没有的独有壁垒——竞品迭代 agent 能力均靠人工迭代 prompt，无行为回归门控；niuniu-agent 的每一次能力/语义升级都沉淀为可回归的 eval 用例（如 P4 记忆升级 → 21-27 号用例），防后续迭代回退
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
| `NIUNIU_AGENT_MAX_TURNS` | 每次 prompt 的模型往返上限（正整数）；未设或 ≤0 = 不限制 |
| `ANTHROPIC_BASE_URL` | 默认 `https://api.anthropic.com`；GLM 等兼容网关填其地址 |
| `ANTHROPIC_AUTH_TOKEN` | Bearer 方式鉴权（Anthropic 兼容网关常用） |
| `ANTHROPIC_API_KEY` | `x-api-key` 方式鉴权 |
| `ANTHROPIC_MODEL` | 模型名（或 `-model` 传入） |
| `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL` | OpenAI 兼容协议同理 |

其他 flag：`-max-turns`（模型往返上限，默认不限制；可用 `NIUNIU_AGENT_MAX_TURNS` 环境变量设定上限，对 ACP 会话同样生效）、`-timeout`（整体超时）、`-profile`（选择 config.json 里的档案）、`-config`（profile 配置路径覆盖）、`-print-system`（stderr 打印合成后的 system）、`-reflect`（回合后反射沉淀经验）、`-resume`（续跑会话）。

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
    },
    "claude": {
      "provider": "anthropic",
      "baseURL": "https://api.anthropic.com",
      "apiKeyEnv": "ANTHROPIC_API_KEY_CLAUDE",
      "model": "claude-sonnet-4-5",
      "thinking": "medium"
    }
  }
}
```

**对接 Claude 官方 API**：上面的 `claude` 档案即为完整示例——`provider:
"anthropic"` 走 `/v1/messages` 协议（默认 base 即 `https://api.anthropic.com`，
可省略 baseURL），请求带 `x-api-key` + `anthropic-version` 头；用
`authTokenEnv` 代替 `apiKeyEnv` 则走 `Authorization: Bearer`（代理网关用）。
使用：

```bash
export ANTHROPIC_API_KEY_CLAUDE=sk-ant-...
niuniu-agent -profile claude -p "..."          # 显式选择（profile 字段整体生效）
niuniu-agent -p "..."                          # 或设 defaultProfile: "claude" 隐式启用
```

注意：**显式 `-profile` 时该档案的 baseURL/model/凭据整体生效**，不会被
环境里同名的 `ANTHROPIC_*`（如 workspace 注入指向其他网关的变量）抢走；
隐式 `defaultProfile` 时标准 `ANTHROPIC_*` env 仍可临时覆盖档案字段。
Claude 模型的 extended thinking 由 `thinking` 档位透传（budget_tokens），
原生 context editing（`NIUNIU_AGENT_CONTEXT_EDITING=1`）在官方 API 上
直接可用。

⚠️ **密钥安全**：配置文件会被提交进仓库——**永不写入明文密钥**。凭据只存
环境变量，配置里用 `apiKeyEnv`/`authTokenEnv` 引用 env 名；加载器遇到
`apiKey`/`authToken` 等明文字段直接拒绝。

优先级（高→低）：CLI flag（`-profile`/显式 `-provider`/`-model`/`-base-url`）>
env（workspace 注入的 `ANTHROPIC_*/OPENAI_*` 与 `NIUNIU_AGENT_PROFILE`）>
项目级 config > 用户 env > 全局 config > 内置默认。细化语义：**显式选择
profile**（`-profile` / `NIUNIU_AGENT_PROFILE`）时该档案的 baseURL/model/
凭据 env 整体生效（标准 `ANTHROPIC_*` 仅作未声明字段的兜底）；**隐式
defaultProfile** 时标准 env 反压档案字段（env 作为临时覆盖手段）。

子命令：
- `niuniu-agent profiles` —— 列出 profiles 与当前生效者（凭据只显示 env 名与 set/unset）
- `niuniu-agent eval -auto-explore` —— 重复失败模式自动触发 RSI explore
- `niuniu-agent memory-consolidate` —— 记忆整理
- `niuniu-agent explore -gated` —— 全飞轮（Measure→Evolve→三道安全闸门→Land→Control）

### LSP 语言服务器（可选）

`.niuniu-agent/lsp.json` 声明语言服务器后，`LSP` 工具提供 definition/
references/hover/symbols 精准导航（按文件扩展名路由，server 进程懒启动、
会话内复用）：

```json
[
  { "name": "go",  "command": "gopls", "args": ["serve"], "extensions": [".go"] },
  { "name": "ts",  "command": "typescript-language-server", "args": ["--stdio"],
    "extensions": [".ts", ".tsx", ".js"] },
  { "name": "py",  "command": "pyright-langserver", "args": ["--stdio"],
    "extensions": [".py"] }
]
```

新供应商家族：实现 `model.Model` 接口并 `model.RegisterProvider(name, factory)`
注册，profile 的 `provider` 字段直接引用注册名。

### 状态存储布局

agent 私有状态（会话快照 / 压缩状态 / 压缩归档 / 任务清单 / 项目印象）按
Claude-Code 式布局存**用户主目录**，按项目路径转义分区，不写入项目目录：

```
~/.niuniu-agent/projects/<escaped-cwd>/
├── sessions/<id>.json      # 会话快照（headless 与 ACP 每 turn 保存，-resume 恢复）
├── session-state.json      # 结构化压缩状态
├── impression.md           # 跨 session 项目印象（≤200 字，compact 顺带刷新，启动注入）
├── history/                # 被 compact 移除的消息归档（HistorySearch 检索）
└── todos.json              # 任务清单
```

工作空间内的 `.niuniu-agent/` 只保留**可共享的项目工件**：AGENTS.md、
inject.md、.mcp.json、skills/、PROMPT.md、memory/（项目层）。

## P4 原生记忆：schema 与语义

记忆条目是带 frontmatter 的 markdown 文件（title 即 slug 即文件名，同 title
更新去重）。防污染上限：单条 8KB、每层 200 条，均为硬约束。

### 字段表

| 字段 | 说明 |
|------|------|
| `title` | 唯一标题，slug 化后即文件名；同 title 保存 = 原地更新（保留 created） |
| `type` | 条目类别（开放分类，未知值原样保留）：`pattern` / `gotcha` / `decision` / `user` / `ref` |
| `domain` | 生活域（与 type 正交，旧数据无此字段 = 未分类）：`decision`（已定项目决策）/ `preference`（长期偏好）/ `environment`（环境习惯）/ `open_item`（进行中事项）/ `collaboration`（协作方式）/ `other` |
| `tags` | 逗号分隔的召回关键词 |
| `lifecycle` | 生命周期（见下）；缺省读为 `open` |
| `expires_at` | 可选截止时间：RFC3339（`2026-10-20T09:00:00Z`）或 date-only（`2026-10-20`，覆盖该本地日全天）；已过即在读取路径判定为 expired；Save 传 `"none"` 显式清除 |
| `created` / `updated` | 时间戳；更新保留 created、刷新 updated |

### 生命周期流转

- 新条目默认 `open`；closed 状态（`done` 完成 / `cancelled` 撤回 / `expired` 过期
  / `deprecated` 被纠正取代）**保留在盘但停止召回**——Recall / RecallFor 与
  MemorySearch 默认结果均过滤；MemorySearch 显式传 lifecycle 过滤仍可查回
  （供纠错定位旧条目），显式过滤匹配**磁盘原始值**，因此已过期的 open 条目用
  `lifecycle: "open"` 可找回续期（默认结果里它按生效状态被隐藏）。
- **惰性过期**：`lifecycle: open` 且 `expires_at` 已过的条目，在读取/召回路径
  判定为 expired 并停止注入（「明天面试」过期后自动停引，无后台任务）。判定
  只在消费点折叠、绝不改写字段——consolidate 重写幸存条目时盘上仍是原文，
  不会把 expired 盖章泄漏到文件；重排截止时间或显式 `open` 重开即恢复召回
  （重开会顺带清除已过期的死截止时间）。
- **更新保留语义**：Save 未显式指定 type / domain / tags / lifecycle / expires_at
  时一律保留旧值——内容编辑不会悄悄重开 done/deprecated 条目，也不会把条目
  重分类或清掉 domain（纠错协议只带 title/content/lifecycle 的部分重存因此
  安全）；显式传入即覆盖，`expires_at: "none"` 单独清除截止时间。
- **consolidate 安全**：同主题合并只发生在**生效 open** 的条目之间，分组键含
  domain——closed 条目是历史记录，既不参与合并也不被吸收（被 deprecated 的
  纠错内容不会被"复活"进活跃条目）；删除失败（文件被占用）不计入报告数。

### 纠错协议（当轮完成，不拖到 reflect）

用户纠正已存储的记忆时，在同一轮内完成三步，不等 end-of-session reflect：

1. `MemorySearch` 定位旧条目；
2. 同 title 重存并带 `lifecycle: deprecated`（保留原内容）；
3. 新 title 存纠正后的事实。

deprecated 条目即刻停止召回。会话中途已完成的纠错，reflect 不再二次提炼。

**状态 vs 偏好判据**：用户只说了「这次/今天」的临时状态（「这次别写测试」
「今天不跑 lint」）不得落长期记忆——除非他明确表述为长期（from now on /
always /「以后都这样」）；持久偏好必须能预测未来会话，而非记录当下情绪。

### 印象层（跨 session 项目印象）

与条目级记忆互补的「兜底氛围」：即使不触发任何具体条目，session 也带着一层
对项目的整体理解。

- **文件**：`~/.niuniu-agent/projects/<escaped-cwd>/impression.md`（与
  session-state.json 等同级，见「状态存储布局」）；内容为四行结构化摘要——
  技术栈 / 关键决策 / 用户脾气（协作风格与偏好）/ 当前阶段。
- **硬约束**：≤200 字（rune），写入侧与读取侧双重截断；截断按**整行边界**进行
  ——四行结构里放不下的行整行舍弃，绝不把半截标签行注入未来所有会话（仅单行
  自身超限才硬切）。上限在 mergeState（唯一合并点）施加，链式 Previous state、
  session-state.json 与印象文件三处字节一致。
- **刷新**：随 compact 的**同一次**摘要调用顺带产出（summarizer 输出 JSON 增加
  `impression` 字段，零额外 token 成本）；链式压缩把上一版传给模型沿用，新输出
  缺该字段/为空时保留旧文件。跨进程同理：新 session 首次 compact 从磁盘读回
  既有印象喂给 summarizer（进程内 state 不跨 session），旧印象不会被本会话的
  一次性猜测覆盖；内容未变时跳过重写。
- **注入**：session 启动时注入 system 前缀末尾可变区（稳定段之后、Memory 召回
  段之前）；session 内字节稳定，不破坏 prompt 缓存。
- **容错**：文件缺失 / 损坏（非法 UTF-8）/ 写入失败一律跳过——不报错、不影响
  会话；写入为原子替换（唯一临时文件 + rename），并发 session 不会读到半截
  文件，也不留临时残渣；条目级细节仍归 memory 存储，印象只是氛围（可能过时，
  ADVISORY 同契约）。

### 召回注入（Memory 段）

- 启动按评分（关键词命中 title×3 / tags×2 / content×1、updated recency、项目层
  优先）取 top-N 注入 system 的 Memory 段，字节封顶；已知任务时走 `RecallFor`
  按任务相关性分层（相关优先，无关条目保持 recency 沉底）。
- **分域路由**：`decision` / `preference` / `open_item` / `collaboration` 为信号域；
  未分类（空 domain——全部存量旧数据）与未识别新域值同等对待、不降权（既不
  埋没看不懂的语义，也不因一条带域新条目就饿死旧库）。仅 `environment` /
  `other` 两个显式噪声域限流 1 条，且限流**原位**生效——超出的条目就地跳过，
  不会把与任务最相关的 environment 命中挤到队尾而被 top-N 截断；无信号域条目
  时限流不生效。Type 不是路由轴。
- **状态标注**：未过期 `open_item`（或带 expires_at 的条目）注入时带
  `[open]` / `[open, due YYYY-MM-DD]` 回访提示；closed 与已过期条目不进注入。
- **ADVISORY 契约**：记忆可能过期或错误——引用偏好/决策须用确认句式（「之前
  记录你偏好X，这次沿用吗？」），用户否认则当轮停用；该指导文字在 Section
  固定段内逐字节稳定，不破坏 prompt 缓存。

条目语义由 `internal/memory` 单测与 `eval/tasks/21`-`27` 号行为用例双重回归
守护（用例经 eval runner 走真实召回注入路径）；印象层由 `internal/tools` /
`internal/prompt` / `internal/loop` 的读写下 round-trip、容错与注入单测守护。

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
