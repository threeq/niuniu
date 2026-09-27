# 一站式 AI 视频生成能力·最终方案

> 状态：**已定稿并进入实现**（issue #707，2026-09-15 定稿，2026-09-27 起实施；评审与决策过程见看板历史，本文只保留结论）｜实现分期见 §12
> 可行性依据见同目录 `2026-09-15-video-creation-feasibility.md`
> 目标：用户给一个故事/商品/想法 → 控制工作空间按工作流编排多波次创作子任务（子 issue 流转 + 子 agent 波内并行）→ 交互式 UI 精修结构化产物 → 生成工具层产出成片。**全程看板流转、成本可控、成品质量有闸门保障。**

---

## 1. 已否决的方案（防止重议）

| 方案 | 否决理由 |
|---|---|
| 进程外集成 clipforge 作为主方案 | AGPL 只能进程外；功能绑电商、第二套 BYOK、外部生命周期管理。留作场景可声明的第三方 MCP 工具源 |
| 内置 Go 合成服务 / Toonflow 式画布时间轴产品 | 重写活跃演进生态即刻过时；WebAV 与工作空间文件模型错位；工作量以人月计 |
| vendor clipforge / Toonflow 代码 | 许可红线（AGPL-3.0 / Apache-2.0+商业补充），只参考设计不搬代码 |
| 多工作空间主拓扑（每子任务独立空间） | 上下文断层伤创作质量、编排税 30–50%、归档同步复杂——降级为逃生门（§4.1） |
| 报价确认令牌体系 | 过度设计：真实威胁只有 L3（按秒计费），现成 `ask_user` 即可覆盖 |
| Toonflow 式 vm2 运行时可编程供应商 | agent+后端适配器即适配层，运行时热插拔对牛牛是过度设计 |
| 时间轴/画布剪辑 UI | 硬边界：只编辑结构化产物；深度精剪引导导出去剪映/PR |

## 2. 总体架构（三层 + R1 拓扑）

```
┌────────────── 层一 · 编排层（R1 定案：单控制工作空间）──────────────┐
│ 控制工作空间（主 issue=Epic，导演 Agent，media-studio 场景+编排 skill）        │
│  batch_create_issues(parent, exec_wave)：子 issue 保留任务边界/评审流/目标      │
│   ├─ wave0 导演阐述（导演自产，全管线必读）    ⇒ directorial-brief.md           │
│   ├─ wave1 编剧（导演会话）                    ⇒ storyline.json / script.json   │
│   ├─ wave2 角色 ∥ 场景（会话内子agent 并行）    ⇒ characters.json / scenes.json │
│   ├─ wave3 分镜（导演会话）                    ⇒ storyboard.json                │
│   按波次分会话（3 个会话）：上下文延续 ↔ 长会话衰减的折中                        │
│   产物审核：AI 预审 → 看板审查列（approve_review / request_changes）             │
│   ★ 逃生门：某子任务需异构 MCP 环境时对该子任务单独 start_workspace（产物路径不变）│
└──────────────────────────────┬─────────────────────────────────────┘
                               ▼ 结构化产物族（唯一事实源，人审卡点）
┌────────────── 层二 · 交互层（P3，先由看板/对话替代）────────────────┐
│ 「视频产物」面板：产物树 + 卡片编辑器（编辑/备注）                              │
│   修改请求 → 路由回对应子 issue → 控制空间重新生成 → 再评审                     │
│   确认交互（层三 L3 的 ask_user 换壳成按钮）                                   │
└──────────────────────────────┬─────────────────────────────────────┘
                               ▼ approved 的 storyboard.json = 唯一生成事实源
┌────────────── 层三 · 能力模块（独立可装配 MCP，P2）─────────────────┐
│ 独立二进制 niuniu-video-mcp（与 niuniu-mcp 解耦，可装配/可裁剪，§7.1）          │
│ 工具：quote_estimate / tts_generate / image_generate / video_generate         │
│       / media_compose(FFmpeg 最终装配器)                                      │
│ ★ 场景配置制挂载：仅在声明它的场景投影（.mcp.json），绝非全局                   │
│ 账号密钥：能力配置域（独立于 env_provider，NN_CAP_* 注入，§7.5）                │
│ 硬护栏：分级确认——L2 放行留痕、L3 经 ask_user（§7.4）                          │
└───────────────────────────────────────────────────────────────────┘
```

**依赖关系**：层一独立可跑（P1）；层三补齐生成自动化（P2）；层二是体验升级（P3，前置只读仪表 gate）。P1+P2 即端到端全自动（修改用看板+对话替代 UI）。

## 3. 目标与范围

**做**：agent 在媒体舱工作空间一站式把想法变成短视频成片。画面三级档位：L1 自有素材/免费图库（零成本）→ L2 AI 文生图 → L3 AI 图生视频（报价确认制）。

**不做**（硬边界，写进 skill 与场景描述）：时间轴/画布/剪辑 UI；内置 Go 合成服务；搬任何参考项目代码；多供应商运行时可编程抽象层。

## 4. 层一 · 编排层（R1：单控制工作空间）

### 4.1 机制映射（几乎零新开发）

| 设计需要 | niuniu 现成机制 |
|---|---|
| 主任务带多个子任务 | `batch_create_issues(parent_issue_id, issue_type:'epic', exec_wave)`——子 issue 保留任务边界/评审流/goal_condition |
| 执行拓扑（R1） | 子 issue 在**控制工作空间内执行**；`start_workspace(issue_id)` 仅为逃生门——个别子任务需要异构 MCP 工具环境时单独起空间，产物路径不变 |
| 波次依赖 | `exec_wave` 标注波次（波间串行），波内并行由会话内子 agent 承担 |
| 异构能力 | **技能包替代场景切换**：写作/设计/分镜评审各物化为一个 skill 包，控制空间全量持有；生成工具组经 media-studio 场景全组挂载（§7.2） |
| 中间产物审核 | AI 预审（`ai_native_review`）→ 看板审查列；`approve_review` 通过 / `request_changes` 打回（打回=修改意见交回控制空间重做该产物，也是层二修改路由的看板原生形态） |
| 会话规划 | 按波次分会话（3 个：编剧 → 角色∥场景 → 分镜）；wave 间以产物区现状+导演阐述刷新心智 |
| 子任务自治 | `update_issue(goal_condition)`（逐子任务收敛判据） |

### 4.2 波次

| wave | 子 issue | 产物 | 审核点 |
|:---:|---|---|---|
| 0 | 导演阐述（导演自产） | `directorial-brief.md`：整体意图、基调、节奏、各场气氛、角色心象——全管线必读，弥合结构化 JSON 装不下的隐含意图 | 用户确认基调（最便宜的对齐点） |
| 1 | 编剧（导演会话） | `storyline.json` + `script.json` | 故事线+剧本评审（改这里最便宜） |
| 2 | 角色设计（子 agent 并行） | `characters.json` + 角色参考图 | 角色卡+形象一致性评审 |
| 2 | 场景设定（子 agent 并行） | `scenes.json` + 场景参考图 | 场景卡评审 |
| 3 | 分镜（导演会话） | `storyboard.json`（引用 wave1/2 资产 id） | **分镜+报价单联合评审（花钱总闸门）** |
| 4 | 生成与合成（导演调层三工具） | `shots/` + `output/final.mp4` | 成片验收（预览面板直接播） |

控制 Agent 职责：建子 issue 按波次推进、组织评审、一致性检查（id 引用完整性）、驱动层三生成；产物直落产物区，无归档同步。

### 4.3 产物区（控制工作空间内）

```
video-project/
  project.json        # 元信息：标题/画风包/目标平台/画幅/目标时长/质量档位/资产索引
  storyline.json  script.json  characters.json  scenes.json  storyboard.json
  directorial-brief.md
  assets/             # 角色/场景参考图等（wave2 产物）
  shots/  output/  quotes/  qc/   # 逐镜中间产物、成片、报价留痕、质检评分与淘汰记录
  changes/            # 修改请求（层二路由载体）
  .gitignore          # 产物目录不入库
```

## 5. 结构化产物族 schema（层二的数据基础）

- **id 引用制**：`storyboard.json` 每镜以 `character_ids`/`scene_id` 引用资产，不内联文案——跨镜一致性与单点修改的基础。
- **一致性锚**（AI 视频核心难题的对策）：角色卡含 `appearance_prompt`（稳定模板段）+ `reference_image` + 可选 `seed`；生成时工具层模板化拼接（角色段+场景段+镜头动作），不靠 LLM 每次复述；画风包模板段强制进所有视觉 prompt。
- **分镜镜对象**：

```json
{
  "id": 1, "duration_sec": 3.5, "transition": "cut|fade",
  "character_ids": ["c-hero"], "scene_id": "s-cafe",
  "action": "主角推门入座，环顾", "camera": "中景缓推",
  "visual": { "tier": "L1|L2|L3", "prompt": "...", "candidates": ["shots/01-a.mp4", "shots/01-b.mp4"], "selected": "shots/01-b.mp4" },
  "narration": "配音文本", "subtitle": "字幕文本",
  "tts": { "voice": "", "asset": "assets/tts-01.mp3" }
}
```

- **修改请求**（层二 UI 与看板路由的交换格式，落 `changes/`）：

```json
{ "id": "chg-007", "target": "characters.json#roles[c-hero].appearance_prompt",
  "kind": "annotation|edit", "content": "外套改成风衣，发色再浅一点",
  "route": "<子issue#角色>", "status": "pending|dispatched|regenerated|approved" }
```

- 每份产物顶层带 `revision` 与 `review_status`（draft/in-review/approved）；**工具层只消费 approved 的 `storyboard.json`**。

## 6. 层二 · 交互式产物编辑（P3）

**形态**：工作空间页新增「视频产物」面板 tab——左栏产物树（故事线/剧本/角色/场景/分镜/修改记录），右栏卡片编辑器（文本 inline 编辑写回 JSON、图片缩略图+替换、每卡片挂备注线程），动作区（选中节点 →「送回重生成」/「直接生效」/报价确认按钮）。

**修改路由**（都收敛为 `changes/*.json`）：
1. **备注→重生成**（默认）：修改请求 dispatched → 路由到对应子 issue（`request_changes` 或指令注入）→ 控制空间按意见重做该产物 → 新版本回流 → 再评审 → approved；
2. **直接编辑**（小改）：UI 写 JSON + bump revision + 留痕，不惊动创作会话。

**实现约束**：遵守 `docs/design-system.md` 硬门禁（token/`t()`/shadcn/lucide）；产物读写走既有工作空间文件 API，无新后端表；`changes/` 监听用既有 WS 文件事件。

**前置 gate**：先上"产物只读仪表+看板备注锚定"（~2 人日）收集真实修改模式，再决定完整编辑器形态——UI 由真实需求拉动。

## 7. 层三 · 能力模块（P2）

### 7.1 模块架构：独立于 niuniu-mcp、可装配

能力 MCP 是**独立二进制**（与主程序/niuniu-mcp 生命周期解耦），自描述 manifest：

```
module: video-gen
  binary:  niuniu-video-mcp        # 独立 cmd、独立进程
  tools:   quote_estimate / tts_generate / image_generate / video_generate / media_compose
  config:  配置 schema（能力族/实现列表/参数字段）→ 驱动设置页区块渲染 + capability_backends 存储命名空间
  scenes:  被场景 YAML 按名声明（media-studio: mcp: [video-gen]）
  degrade: 未装配/未启用 → 场景卡片提示，工具缺席，skill 走降级链
```

**四个装配点**：

| 装配点 | 机制 |
|---|---|
| 构建/发行 | 发行版按目标裁剪模块（全量桌面版含 video-gen，轻量版不含） |
| 运行时启停 | 已安装模块可停用；场景引用缺失模块 → 优雅降级提示，绝不报错堆栈 |
| 场景装配 | 场景 YAML 按名声明；投影时查模块注册表——在则投影 stdio 命令，不在则工具组缺席 |
| 配置页装配 | 设置页「能力配置」区块按已装配模块动态渲染（模块注册 schema，前端按 schema 生成表单）；模块不在 → 区块不出现 |

**与 niuniu-mcp 的边界**：niuniu-mcp = 与主程序同生命周期的内置核心工具；能力模块 = 独立二进制、独立演进节奏、可选装配。场景与 agent 视角两者无差别。存储上 `capability_backends` 带 `module` 列，按 `module+capability` 命名空间隔离。**后续更多能力 MCP 照此框架接入**（框架一次性，模块按模板复制）。

### 7.2 挂载原则：场景配置制，绝不全局

生成工具组走场景 `mcp:` 声明 → 投影 `.mcp.json`；**场景即能力边界**：

| 场景 | 声明的工具 | 理由 |
|---|---|---|
| `media-studio`（R1 下唯一创作空间） | 全组 | 全管线都在这里 |
| 逃生门独立空间 | 按职责最小授权（如仅 quote+image） | 升级空间不继承全组 |
| 其余全部场景 | **零声明** | 视频能力对它们不存在 |

双条件才可用：场景声明工具 **且** 能力配置有对应账号——"场景×账号"矩阵。

### 7.3 工具面与后端适配器

| 工具 | 输入 → 输出 | 关键行为 |
|---|---|---|
| `quote_estimate` | 付费调用清单（含质量档位的候选/修复预算）→ 报价单 | 分项+总额，落 `quotes/`；L3 确认的前置输入 |
| `tts_generate` | 文本+voice/语速 → 音频+时长元数据 | OpenAI 兼容 `/v1/audio/speech` 基线 |
| `image_generate` | prompt+参考图+画幅 → png（多候选） | wave2 参考图 + wave4 首帧 |
| `video_generate` | 首帧图+运动 prompt+时长 → **异步任务**（句柄/轮询/取片） | i2v 为主；2–3 候选；失败留痕不自动重试；第一期 recipe：豆包 Seedance / 可灵 |
| `media_compose` | approved `storyboard.json` + assets → 逐镜合成 → 拼接+ass 字幕+BGM 混音 → `output/final.mp4` | FFmpeg 最终装配器（`-filter_complex_script`、H.264/AAC/faststart、默认 720p）；坏镜单点重合成；执行 G5 技术 QC；**硬前置：只接受 review_status=approved 的 storyboard.json** |

**FFmpeg 分发（v3.1 修订：打包内嵌 + 首用解压，用户定案——不做系统依赖手动安装）**：
- **打包**：构建期由 Makefile 目标下载各平台静态构建（ffmpeg + ffprobe），放入 `internal/ffmpegbin/dist/<goos>-<goarch>/`（gitignore，不入库），以 build tag `ffmpeg_bundled` 门控 `go:embed` 进 `niuniu-video-mcp` 二进制；无 tag 时以 stub 编译（不阻断开发构建）。
- **解压**：首次使用时原子写入 `~/.niuniu/bin/ffmpeg/<指纹>/`（文件 + 权限 0755 + `.fp` 标记，指纹匹配则跳过）——新用户零手动安装。
- **解析顺序**：`NIUNIU_FFMPEG`/`NIUNIU_FFPROBE` env 覆盖 → `~/.niuniu/bin/ffmpeg/` 已解压副本 → PATH（`exec.LookPath`）兜底。
- 桌面发行经既有 sidecar 通道（`include_bytes!` + 解压到 `~/.niuniu/desktop-v2/sidecars/`）携带 `niuniu-video-mcp` 二进制；ffmpeg 随模块二进制一起走，无需额外分发面。

**后端适配器（Backend Adapter，代码级）**：TTS（`Synthesize`，同步单产物）/ Image（`Generate`，同步多候选）/ Video（`Submit+Poll+Fetch`，异步任务型）各一 Go 接口（`TTSBackend` / `ImageBackend` / `VideoBackend`）；适配器注册表 + 能力配置绑定选择实现；协议基线=OpenAI 兼容，不兼容厂商写专门适配器；**价格元数据随适配器注册**（quote 聚合）；护栏逻辑只在工具壳实现一次，新适配器天然继承。**新增后端 ≈ 实现一个适配器接口（100–200 行）+ 注册一行，≤ 半天/厂商**；模式差异（首尾帧/多参考）用 Request 可选字段表达，不支持即明确报错转降档建议。

**media_compose 扩展路径**（不动主链路）：表现力扩展走**素材维度**——动效引擎（Revideo 等）渲染特殊镜头素材进 assets/，FFmpeg 统一装配；整片云渲染（Shotstack/阿里云 ICE 类，时间线 JSON 与 storyboard 同构）留 `ComposeBackend` 口子作"精制档"，仅用户显式配置时可用。

### 7.4 硬护栏：分级确认

- **L2（图片/TTS）直接放行**——单次成本有界（分~毛级），调用留痕 `quotes/`、汇总随分镜评审可见；
- **L3（视频生成）`ask_user` 即时确认**——控制 agent 备好报价单 → ask_user 批准 → 批准后才 dispatch；确认发生在会话内，agent 无法绕过；
- 失败不自动重试；报价超上限中止并询问。

### 7.5 能力配置与密钥管理（独立于 env_provider）

**设计裁决**：生成服务的三方账号与 env_providers **完全分开**——env_providers 服务 agent 的 LLM 账号（模型分层/上下文窗口/自动切换组），能力配置服务工具调哪个生成后端，消费者/字段/绑定粒度/生命周期都不同。

| | env_providers（既有，不动） | 能力配置 capability_backends（新建） |
|---|---|---|
| 服务对象 | agent 会话的 LLM 账号 | 工具层的三方生成服务 |
| 典型字段 | 模型分层、context_window、自动切换组 | 能力族、适配器（实现）名、BaseURL、Key、生成参数、价格元数据 |
| 绑定粒度 | 项目/工作空间 → 账号组 | **全局默认（按能力族）+ 工作空间覆盖** |
| 注入命名空间 | 既有 LLM env | **`NN_CAP_<能力>_*` 独立前缀** |

**配置面**：① 工具实现（产品仓库：模块代码+场景声明，无 Key）→ ② 能力投影（`.mcp.json` 自动生成，仅 server 命令行，无 Key）→ ③ **能力配置**（设置页「能力配置」区块，schema 驱动渲染、随模块装配出现；新表 `capability_backends`：module/capability/backend(适配器实现名)/base_url/api_key 加密存储/extra_config/enabled/owner，凭据走 credstore）。

**流程**：设置 → 能力配置 → 按能力族添加账号（选实现类型 → BaseURL/Key/参数）→ 全局默认生效、工作空间可覆盖 → 建媒体舱 → 投影+`NN_CAP_*` 注入 → 工具可用；**换厂商=改绑定，零代码**。

**密钥流转**：录入 → 本地表（credstore 加密，绝不进仓库/场景 YAML/产物/git）→ 会话/工具进程启动按绑定注入 → 工具进程读自己的前缀 env → 调三方 API（用户显式配置的外部源，符合 local-first）。Key 永不出现在 `.mcp.json`/场景 YAML/产物/报价单与日志。

## 8. 质量保障体系（顶层目标：保证成品质量）

**工程语义**：AI 生成是随机的，单次质量不可承诺；本体系保证**不合格产物进不了成片**——逐层三道审（机器确定性审/AI 评审/人审），坏镜被拦截、修复或淘汰，把质量推到当前模型能力上限。

### 8.1 六道闸门（G0–G5，挂看板流转）

| 闸门 | 机器审 | AI 审（rubric + `ai_native_review`） | 人审 |
|:--:|---|---|---|
| G0 导演阐述 | 模板字段完整 | — | 基调确认 |
| G1 剧本/故事线 | 结构完整、时长预算、违禁词扫描 | **判官团**：钩子/逻辑/节奏逐项挑刺、等长重写 | 评审列 |
| G2 角色/场景图 | 尺寸/格式 | `read_image` 回看：风格一致/细节；多角色同框测试 | 人工选图 |
| G3 分镜表 | id 引用完整、时长合计=目标、字段齐备 | 逐镜可行性打分 | **分镜+报价联合评审（花钱总闸门）** |
| G4 镜头素材 | 分辨率/时长/格式 | 抽帧逐维打分，**低分自动进修复回路** | 抽检+坏镜修复确认 |
| G5 成片 | 技术 QC：音画同步/字幕时轴/电平/时长/AIGC 标识位 | 整体观感（节奏/转场/音乐情绪） | **结构化 checklist 全过才放行交付（发布门禁）** |

### 8.2 多候选择优

关键镜（角色首出/特写/钩子镜）L2 出 2–4 候选、L3 每镜 2–3 候选（随档位）；AI 按 rubric 先筛掉废片，**人只在及格线以上选条**；候选与评分落 `qc/`（维度+分数+淘汰原因，可回溯）。

### 8.3 有界修复回路

坏镜诊断 → **单变量重投**（一次只改 prompt/模型/候选之一）；每镜修复上限 2 轮，超限升级人工三选一（换方案/降档/换镜）；不允许静默无限重试；修复只重做坏镜，成本恒为单镜。

### 8.4 一致性专项

角色锚三件套（§5）+ **跨镜一致性抽查**（同一角色跨镜截图并排对比，不一致即重出首帧）；画风包模板段强制进所有视觉 prompt，不允许 agent 即兴风格词。

### 8.5 质量档位（质量-成本显式交易）

| 档位 | 候选数 | 修复轮次 | AI 审强度 | 适用 |
|---|---|---|---|---|
| 草稿 | 1 | 0–1 | G3/G5 抽查 | 内部预览、验证想法 |
| 标准（默认） | 2 | 2 | 全闸 | 日常发布 |
| 精制 | 3–4 | 3 | 全闸+判官团加轮 | 重要投放 |

档位写进 `project.json`、逐镜可覆盖；候选/修复预算计入报价单——**预算换质量显式可见**。

**落点**：零新闸门基础设施——机器审=工具层校验+skill 步骤；AI 审=rubric+`ai_native_review`；人审=审查列+issue checklist（G5 逐项打勾全过才 approve）；修复=`request_changes`+坏镜单点重生成。

## 9. 降级链与错误处理

| 场景 | 行为 |
|---|---|
| video-gen 模块未装配/被停用 | 场景卡片提示；skill 走临时配方或只出产物族 |
| 无 ffmpeg（模块未内嵌且 PATH 无） | 工具层降级：只出产物族+素材清单，不合成（正式发行版内嵌 ffmpeg，此路径仅开发构建兜底） |
| 无 TTS/图像/视频账号（能力配置为空） | 对应档位不可用，评审信息明示；L1 自有素材兜底 |
| L3 报价被拒/超上限 | 回落 L2 出图，成片改图串片 |
| 生成 API 失败 | 任务留痕 `quotes/`，不自动重试，用户决定 |
| 产物不合格 | `request_changes` 打回，控制空间按意见重做该产物 |
| Edge 免费 TTS（可选路径） | 默认关闭、显式启用，启用时告知数据流向微软端点 |

## 10. 安全 / 合规 / local-first

付费 API 全走能力配置绑定的三方服务（用户显式配置的外部源）；L3 花钱必经 `ask_user`；素材版权纪律（自有或明确可商用来源）写入编排 skill 与工具返回；AIGC 标识提醒（GB 45438-2025 参考），标识位纳入 G5 技术 QC；产物不入 git。

## 11. 验收标准（供实现期使用）

1. **编排**：Epic 主 issue 派发 5 个子 issue（wave 0–3 正确流转），控制空间按波次产出合格产物族直落产物区；
2. **审核路由**：对角色卡发 `request_changes` → 按意见重做该产物（revision 递增）→ 再审通过；
3. **硬护栏**：L3 前必经 `ask_user` 报价确认，拒绝即回落 L2；L2 放行但 `quotes/` 留痕完整；
4. **场景隔离**：未声明场景的工具清单中不存在任何生成工具；投影 `.mcp.json` 与挂载矩阵一致；
5. **模块装配**：发行版裁掉 video-gen 时，场景提示缺失、无报错堆栈、配置区块不出现；
6. **成片**：approved 分镜 → `media_compose` → final.mp4 预览可播、字幕/时长与分镜一致、`git status` 干净；
7. **UI（P3）**：产物树渲染五类卡片，inline 编辑 bump revision，备注可「送回重生成」并看到状态流转；
8. **降级**：卸载 ffmpeg / 清空能力配置后各层独立降级，无报错堆栈；
9. **一致性**：同一角色跨 5 镜形象偏差可接受（参考图+模板 prompt+抽查生效）；
10. **质量闸门**：G4 低分候选不进合成（`qc/` 有淘汰留痕）；G5 checklist 未全过时交付不可能被 approve；坏镜 2 轮内修复或升级人工，无静默重试。

## 12. 分期与估算

| 期 | 内容 | 估算 |
|---|---|---|
| **P1 层一** | 编排 skill（子 issue 推进/波次会话规划/评审纪律）+ 质量闸门 rubric（G0–G3/G5：判官团 prompt、验收 checklist、`qc/` 约定）+ 产物族 schema 与各子任务 prompt + media-studio 场景扩展 + 合成走临时 skill 配方 | ~3 人日；端到端可用（修改靠看板+对话） |
| **P2 层三** | 能力模块框架（manifest/四装配点/配置 schema 渲染，一次性 ~1 人日）+ `niuniu-video-mcp` 模块（五工具+后端适配器 ~3.5 人日）+ 场景声明挂载 + G4 自动修复回路 + 异步视频任务 + **FFmpeg 打包内嵌与首用解压（§7.3：构建期下载→go:embed→`~/.niuniu/bin/ffmpeg/` 解压→env/解压目录/PATH 三级解析）** | ~4.5 人日 |
| **P3 层二** | 前置只读仪表（~2 人日）→ 完整编辑器（产物树/卡片编辑器/备注线程/修改路由/确认按钮，过 shape/design brief 定交互稿后） | 5–8 人日 |

快速模式：不看板流转、无质量闸门矩阵，控制 agent 一口气线性跑完（产物族 schema 完全兼容），适合一次性快速出片——可随时升级为 P1 流程形态。

## 13. 风险与开放问题

| # | 风险 | 应对 |
|:-:|---|---|
| 1 | **长会话衰减**（R1 下编剧→分镜连跑，后期质量劣化） | 按波次分会话（3 个）；wave 间以产物区+导演阐述刷新心智；子 agent 只承载单产物职责 |
| 2 | **跨镜一致性链路传导**（L2 首帧→选条→i2v 任一环弱则成片崩） | 参考图+模板 prompt+seed 三件套+抽查；不达标时 P2 引入角色定制供应商 recipe（需实测） |
| 3 | **AI 评审会误杀或放过** | 人审兜底不变；rubric 随项目复盘迭代；`qc/` 留痕支持回归——承诺的是"闸门存在且不可跳过"，不是"AI 评审永远正确" |
| 4 | 视频/图像 API 形态月月变 | 后端适配器 + recipe 文件化，改文件即跟上；单家故障降档不阻塞 |
| 5 | 免费图库 keyless 限额 | L1 优先工作空间自有素材；图库仅补充并注明来源 |
| 6 | 质量档位拉高生成成本 | 档位显式化 + 报价单含候选/修复预算，预算换质量由用户决定 |
