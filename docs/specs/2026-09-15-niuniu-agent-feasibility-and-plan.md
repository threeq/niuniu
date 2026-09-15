# niuniu-agent 可行性分析与方案（issue #708）

- 日期：2026-09-15
- 状态：**分析文档，供立项决策**。本文回答 issue #708：「Claude 不开源，参考 Claude 写一个 agent，要求能力与 Claude agent 一致，分析可行性和方案」
- 结论速览见 §0；需要人工拍板的决策点集中在 §8

---

## 0. 结论先行

1. **可行性：高。** Claude Code 的能力 = agent loop + 工具集 + 上下文工程 + 权限/扩展体系，全部是可复现的工程产物，不存在不可逾越的技术机密。业界已有十余个开源等价物（opencode、Codex CLI、Gemini CLI、goose、OpenHands、Aider、pi……），其中多个已达到「日常可用」，可行性被反复验证。
2. **但要校准「一致」的期望。** 「能力一致」分两层：
   - **harness 层**（工具、循环、上下文管理、权限、扩展）——可以做到一致，这正是本文方案的范围；
   - **端到端效果层**——还取决于模型。同一个 harness 换模型后表现有差距。自研 harness 的真正价值恰恰在这里：**为 niuniu 主力模型（GLM / Qwen 等）做一等公民调优**，而不是指望闭源 harness 兼容第三方模型。
3. **推荐路线：C（自研 niuniu-agent，Go 实现），同时最大化吸收路线 A 的成果**——工具语义、上下文策略大量参照 Apache/MIT 开源实现，协议边界优先 ACP。**不推荐 greenfield 式从零发明**，也**不推荐**把 Anthropic Agent SDK 当底座（§3）。
4. **与 niuniu 的集成成本低且插座已备好。** niuniu 已为 goose、cursor 实现了两个 ACP-over-stdio 的 `agentbackend.Backend` 适配器（`agentbackend/agentbackend.go` 注释自述「reusable, runtime-agnostic contract」）；niuniu-agent 说 ACP 即可以第七个 `cli_type` 接入，host 侧的 SSE、成本、审批卡全部复用。
5. **工作量：MVP（单轮对话 + 核心工具 + 权限 + 接入 niuniu）约 1.5~2 人月；达到「日常可用、可作为可选引擎」约 3~5 人月（1 人，AI 辅助开发）。** 「全面对齐 Claude Code」是移动靶，正确目标是**能力域一致**而非版本对齐（§6）。

---

## 1. 需求澄清：Claude Code 到底是什么

把「与 Claude agent 能力一致」拆成六个能力域，逐域评估复现难度：

| # | 能力域 | 内容 | 复现难度 | 说明 |
|---|--------|------|----------|------|
| 1 | **Agent loop** | 流式、tool-use 循环、abort、重试/退避、turn 管理、单飞约束 | 低 | 数百行核心；openai/anthropic 兼容协议均有 tool-use |
| 2 | **工具集** | Read / Write / Edit / Bash / Grep / Glob / WebFetch / TodoWrite / Agent(subagent) / Notebook… | 低→中 | 单个工具都不难；难点在**人体工学**：Edit 的容错匹配、Bash 持久会话与超时、Grep 的 ripgrep 依赖、Windows 路径差异 |
| 3 | **上下文工程** | system prompt 设计、CLAUDE.md/AGENTS.md 分层 memory、auto-compact、prompt-cache 友好的提示词布局、todo 持久化 | **中（质量大头）** | 这是各家 agent 拉开差距的地方；需要针对自家模型迭代，不是抄一份 prompt 就行 |
| 4 | **权限与安全** | allow/ask/deny 规则、settings、hooks、sandbox（macOS seatbelt / Linux bubblewrap / Windows 受限 token） | 中→高 | 审批制 MVP 一周可成；**跨平台 sandbox 是全项目最重的工程**，可后置（§6 P4） |
| 5 | **扩展体系** | MCP client、skills、slash commands、subagents、plugins | 中 | MCP 有官方 Go/TS SDK，标准化程度高；skills 本质是「按需加载的 markdown + 脚本」，约定简单 |
| 6 | **交互与协议** | 交互式 TUI、headless `-p`、stream-json、session resume、IDE/客户端协议（ACP 等） | 中 | TUI 做到「能用」容易，做到 Claude Code 的丝滑度需要打磨；headless + 协议层对 niuniu 是刚需 |

**关键认知：Anthropic 自己已经把方法论公开了。** Claude Agent SDK 的官方定位就是「把 Claude Code 的同一套 tools、agent loop、context management 以可编程方式暴露出来」（[Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview)）；Anthropic 的工程博客系列（building agents / Claude Code best practices）把 harness 设计讲得很透。**harness 是公开知识，闭源的只是那一份具体实现与品牌。**

---

## 2. 可行性论证

### 2.1 业界已经证明

| 项目 | 出处 | 定位 | 备注 |
|------|------|------|------|
| **opencode** | sst/opencode（MIT） | 目前公认最完整的开源 Claude Code 替代品，TUI，多 provider | 2026 年多份对比文将其列为开源第一梯队（[对比](https://ofox.ai/blog/opencode-vs-codex-cli-terminal-coding-agent-2026/)） |
| **Codex CLI** | OpenAI（Apache-2.0，Rust） | 官方开源 agent CLI | 证明「一线厂商认为 harness 无需保密」 |
| **Gemini CLI / Qwen Code** | Google / 阿里（Apache-2.0，TS） | Qwen Code 是 Gemini CLI 的 fork | niuniu 已接入 qwen |
| **goose** | Block（Apache-2.0） | 可扩展 agent CLI，ACP 一等公民 | niuniu 已接入（`goose acp`） |
| **pi / oh-my-pi** | badlogic 系（MIT） | 极简内核 coding agent，「一个 prompt 循环 + 工具」流派的代表 | niuniu 的 omp 引擎即此系（`omp --mode rpc`） |
| **OpenHands / SWE-agent / Aider / Cline / Roo** | 开源（MIT/Apache-2.0） | IDE 内嵌与科研向等价物 | 能力域重叠度高的旁证 |

**十余个团队各自独立复现出了同一形态的产品**——这本身就是可行性最强的证据。差别只在打磨程度与模型绑定。

### 2.2 模型侧无壁垒

- 主流模型 API 全部收敛到 **Anthropic 兼容**与 **OpenAI 兼容**两族协议，tool-use / 流式 / 多模态齐备；GLM、Qwen、DeepSeek、Kimi 均通过这两族协议暴露。
- claude-code-router 等生态早已证明「第三方模型驱动类 Claude Code harness」可行；反过来说，**自研 harness 针对 GLM 调优是更可控的方向**——本工作区当前就是「GLM 驱动 Claude 形态 harness」的活样本。
- 需要抽象的点：prompt caching（Anthropic 显式 `cache_control` vs OpenAI 系隐式缓存）、推理内容（reasoning/thinking 块）表达差异、上下文窗口与截断策略。这是模型适配层的设计输入，不是风险。

### 2.3 合规边界：clean-room 纪律（硬约束）

Claude Code 是闭源软件，分发的为压缩混淆产物，其消费者条款禁止反向工程。因此本项目必须遵守：

1. **不反编译、不脱混淆、不逐字复制** Claude Code 的 bundle 代码或 system prompt；
2. 允许的参考来源：**公开文档**（docs、Agent SDK 文档）、**公开发表的工程博客与演讲**、**行为观察**（正常使用中可见的提示词与交互）、**开源实现**（opencode / goose / pi / Gemini CLI 等）；
3. system prompt **自己写、针对自家模型调**——逐字复刻别人的 prompt 既无法律必要性，对非 Anthropic 模型也没有效果必要性。

这条纪律写进 P0 的验收，避免任何一次「抄近路」把整个项目置于法律风险中。

### 2.4 真正的难点：不是「能不能」，是「打磨」

可行性问题早已解决，真正的成本在质量细节：

- **工具人体工学**：Edit 的 old_string 匹配容错、Bash 输出截断与后台任务、错误信息怎么写才能让模型自我纠正——这些决定 agent 的「手感」；
- **上下文管理**：何时 compact、compact 后保留什么、cache 命中率——决定长任务的成本与稳定性；
- **错误恢复**：网络抖动、429、工具失败后的重试与改道。

对策：以 **eval 驱动迭代**（§6 P4），且**第一性目标是服务 niuniu 场景**（看板、MCP 工具、审批卡），不是泛用市场——范围小得多，打磨量也可控。

---

## 3. 路线选择

| 路线 | 内容 | 优点 | 缺点 | 判定 |
|------|------|------|------|------|
| **A. fork / 嵌入现有 OSS** | 以 opencode（或 pi/goose 内核）为底座二开 | 最快拿到 80 分；上游持续进化 | opencode 是巨型 TS 代码库且迭代极快，深度定制 niuniu 场景（看板/审批卡/场景 MCP 一等公民化）必然偏离上游主线 → 长期背着 fork 维护税；许可证与品牌归属需逐项核对 | 可作为**工具语义与上下文策略的参考来源**，不作底座 |
| **B. Anthropic Agent SDK 包壳** | Node 进程跑官方 SDK，外面包 niuniu 协议 | harness 官方出品，能力最接近；Python/TS 双语言 | 闭源依赖只是从 CLI 换成了 SDK；默认绑定 Anthropic API 语义，第三方模型走兼容层非一等公民；Node 运行时分发摩擦（desktop sidecar 场景劣势）；深度定制（审批流、niuniu MCP）受 SDK 抽象约束 | **不推荐作底座**；作为效果对照组有价值的 fallback |
| **C. 自研 niuniu-agent（推荐）** | Go 实现，能力域对齐 §1 六域，参照开源实现 clean-room 重建 | 技术栈与 niuniu server 同构（Go 1.25），**单二进制**对 desktop sidecar 天然契合；为 GLM 等主力模型一等公民调优；看板/审批/场景 MCP 可做成一等公民；无上游依赖 | 需要自担打磨成本；起步慢于 A | **推荐**，并最大化吸收 A 的开源成果 |

**为什么不害怕 C 的「自担打磨成本」**：niuniu 的定位是工作站（多引擎编排），agent 引擎是它最后一块不可控的外部组件。自研后，引擎与 host 之间的协议（审批、权限、上下文注入、MCP 投影）从「适配别人」变成「自己定义」，这正是 `docs/unified-agent-design.md`（能力注入而非模式切换）所需要的执行体。

---

## 4. 自研方案概要

### 4.1 技术栈

- **语言：Go**（与 server 同栈；`go.work` 增加模块或独立 repo 均可，P0 决策）；模型层用现成 Go SDK / 轻量自写 HTTP 客户端（两族协议各一个 adapter）；
- **分发：单二进制** `niuniu-agent`，跨平台交叉编译，desktop-v2 直接作为第二个 sidecar 与 Go server 并列；
- **对外形态**：交互式 TUI（日常）、`niuniu-agent -p`（headless/脚本）、`niuniu-agent acp`（ACP server，niuniu/IDE 接入）。

### 4.2 分层架构

```
┌────────────────────────────────────────────────────┐
│ 接口层    TUI / headless -p / ACP server (stdio)   │
├────────────────────────────────────────────────────┤
│ 会话层    session、resume、todo 持久化、成本记账     │
├────────────────────────────────────────────────────┤
│ 核心层    agent loop（流式/tool-use/abort/重试）     │
│           上下文管理（分层 memory、auto-compact、   │
│           cache 布局）                              │
│           权限引擎（allow/ask/deny 规则 + hooks）   │
├────────────────────────────────────────────────────┤
│ 工具层    Read/Write/Edit/Bash/Grep/Glob/WebFetch/  │
│           Todo/Agent(subagent)  + MCP client       │
├────────────────────────────────────────────────────┤
│ 模型层    anthropic-compatible / openai-compatible  │
│           adapter（含缓存、reasoning 块差异吸收）   │
└────────────────────────────────────────────────────┘
```

设计原则沿用 niuniu 既有共识：**能力注入而非模式切换**（`docs/unified-agent-design.md`）——协作/编排能力以 MCP 工具形式按需注入，agent 始终只有一个。

### 4.3 能力域 → 交付物对照

| 能力域（§1） | MVP | 完全体 |
|---|---|---|
| 1 loop | ✅ P1 | + 稳态重试/退避打磨 |
| 2 工具集 | 8 个核心工具（Read/Write/Edit/Bash/Grep/Glob/WebFetch/Todo） | + Agent subagent、Notebook、后台 Bash |
| 3 上下文 | AGENTS.md/CLAUDE.md 两级 + todo | + auto-compact、cache 布局调优、分层 memory |
| 4 权限 | allow/ask/deny 规则 + 审批卡（经 ACP 桥） | + hooks、sandbox（P4） |
| 5 扩展 | MCP client（NIUNIU 场景投影即用） | + skills、slash commands、plugins |
| 6 协议 | ACP server + headless | + TUI 打磨、checkpoint/rewind |

---

## 5. 与 niuniu 的集成（插座已备好）

### 5.1 接入方式

1. **新增 `cli_type = 'niuniu'`**：`server/web/src/lib/cli-types.ts` 的 `CLI_TYPES` 数组尾部追加（该文件自述「append new engines at the end」），Go 侧枚举、store 迁移、各 picker 由该单一来源派生；
2. **实现 `agentbackend.Backend`**：完全复制 goose/cursor 适配器的既有模式——
   - 进程：`niuniu-agent acp`（ACP over stdio）；
   - `Prompt` → ACP `session/prompt`，事件映射到归一化 `Event`（text/thinking/tool_use/tool_result/done，含 `CacheReadTokens` 上下文占用信号）；
   - 权限请求 → ACP `session/request_permission` → 既有 `ResolvePermission` 桥 → **审批卡 UI 零改动复用**（参照 `goose_exec.go:165` 的桥接函数）；
3. **scene 投影扩展**：`sceneenv` 现在写 `.mcp.json` + `~/.claude` overlay；为 niuniu-agent 定义等价约定（`AGENTS.md` 兼容 + 同一份 `.mcp.json`），niuniu-mcp-workspace（blackboard/inbox/phase/gate）天然可用；
4. **PTY 终端路径**顺带获得（niuniu-agent 本身就是交互式 CLI，`terminal/` 直接可用）。

### 5.2 集成改动清单（预估）

| 位置 | 改动 |
|------|------|
| `server/internal/agentbackend/niuniuagent/`（新） | ACP 客户端 Backend（可大量借鉴 `agentbackend/goose/`） |
| `server/internal/agentproxy/niuniuagent_exec.go`（新） | turn 驱动 + 事件映射（复制 `goose_exec.go` 骨架） |
| `cli-types.ts` + Go 枚举 + store | `niuniu` 引擎注册 |
| `sceneenv/` | AGENTS.md 投影 |
| web 设置页 | 引擎文案 |

host 侧改动量级为**周级**，真正的工程量在 agent 本体（§4）。

---

## 6. 分阶段实施计划

| 阶段 | 内容 | 工作量 | 验收标准 |
|------|------|--------|----------|
| **P0 立项与骨架** | §8 决策点拍板；repo 骨架；模型层双协议 adapter；clean-room 纪律写入 CONTRIBUTING | 1 周 | `niuniu-agent -p "列出当前目录"` 经 GLM 单轮跑通 |
| **P1 MVP loop** | 8 个核心工具、流式、权限规则 + ACP 审批桥、session resume、`acp` server；niuniu 接入（§5） | 3~6 周 | **在 niuniu 里以 cli_type=niuniu 完成一轮真实修 bug 任务**（读代码→改→验证→提交），全程审批卡/SSE/成本正常 |
| **P2 上下文工程** | 分层 memory、auto-compact、cache 布局、subagent | 3~5 周 | 长任务（>100 轮工具调用）不失控；token 成本对比 claude 引擎有基线数据 |
| **P3 扩展体系** | MCP client 完整化（niuniu-mcp-workspace 实测）、skills、slash commands | 2~4 周 | 看板 issue 全流程（advance/blackboard/harness gate）在 niuniu-agent 上走通 |
| **P4 硬化（持续）** | sandbox（先 Linux 后 macOS/Windows）、hooks、**eval 集**（20~50 个真实 niuniu issue 重放） | 持续 | eval 报告：完成率/成本/时长 vs claude 引擎同任务基线 |

**替代门槛（量化）**：niuniu-agent + GLM 在内部 eval 上 ≥ claude + GLM 的 90%，即可作为非默认可选引擎进入引擎矩阵；是否设为默认另议。**不做**「全面替代 claude」的承诺——引擎矩阵多样化本身就是收益。

---

## 7. 风险与对策

| # | 风险 | 等级 | 对策 |
|---|------|------|------|
| 1 | **模型差距**：harness 一致 ≠ 效果一致，GLM 类模型在长 agent 任务上与 Claude 有差距 | 高（最大风险） | eval 驱动；harness 侧补偿（更强的工具描述、结果校验、失败改道）；定位为引擎矩阵新成员而非替代者 |
| 2 | **范围蔓延**：Claude Code 每周在进化，追版本永无止境 | 中 | 目标锁定「六能力域一致」（§1），版本对齐明确出 scope |
| 3 | 跨平台工程（Windows sandbox/PTY 细节） | 中 | sandbox 整体后置 P4；MVP 用审批制不用 sandbox |
| 4 | 合规（clean-room 被无意破坏） | 中 | §2.3 纪律入 CONTRIBUTING；PR review 清单加一条 |
| 5 | 维护成本（模型协议、MCP 演进） | 低 | 模型层 adapter 隔离；MCP 用官方 SDK |
| 6 | 人力单点 | 中 | P0-P1 文档化充分；协议边界清晰使后续接手成本低 |

---

## 8. 决策点（需人工拍板）

1. **路线确认**：推荐 C（自研）+ 吸收 A。若更看重速度可改为「A 起步、C 迁移」，需接受 fork 维护税；
2. **语言栈**：推荐 Go（与 server 同栈、sidecar 契合）。若团队更倾向 TS 生态则路线 A 的权重上升；
3. **协议**：推荐 ACP over stdio（生态标准化、niuniu 已有两套 ACP 适配先例）；私有 RPC（照 omp `--mode rpc`）为备选；
4. **repo 归属**：独立 repo 还是 `go.work` 内新模块；
5. **排期与人力**：MVP 1.5~2 人月的投入是否立项；
6. **开源策略**：niuniu-agent 本体是否开源、何种 license（影响能否直接吸收 GPL 系参考实现）。

---

## 9. 参考来源

- niuniu 仓库内：`docs/unified-agent-design.md`（能力注入设计）、`server/internal/agentbackend/agentbackend.go`（Backend 契约）、`agentbackend/goose/`、`agentbackend/cursor/`（ACP 适配先例）、`agentproxy/goose_exec.go`、`agentproxy/omp_exec.go`
- [Agent SDK overview — Claude Code Docs](https://code.claude.com/docs/en/agent-sdk/overview)（「同一套 tools、agent loop、context management」的官方表述）
- [OpenCode vs Codex CLI 2026 对比](https://ofox.ai/blog/opencode-vs-codex-cli-terminal-coding-agent-2026/)、[devToolLab CLI agent 排名](https://devtoollab.com/blog/top-cli-ai-coding-agents)
- [ACP 官网与 agent 注册表](https://agentclientprotocol.com/get-started/agents)、[vscode-acp（一个客户端接多家 agent 的实证）](https://github.com/formulahendry/vscode-acp)、[OpenHands 经 ACP 驱动各家 agent](https://docs.openhands.dev/openhands/usage/agent-canvas/acp-agents)
- [Building a Coding Agent From Scratch（开源课程）](https://github.com/decodingai-magazine/building-a-coding-agent-from-scratch-course)、[Around the Loop（Python 手搓 harness 实录）](https://eddmann.com/posts/around-the-loop-building-a-coding-agent-harness-in-python/)
- 各项目 license 以其仓库为准，本文引用仅作路线论证
