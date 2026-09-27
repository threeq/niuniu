---
name: shortvideo-forge
version: 1.0.0
description: 一站式 AI 短视频创作编排 — 从故事/商品/想法（或一批现有素材）到可交付成片：按 R1 单控制工作空间拓扑用 batch_create_issues 派发波次子 issue（导演阐述 → 编剧 → 角色∥场景 → 分镜 → 生成合成），产物族直落 video-project/，全程 G0–G5 质量闸门与 L1/L2/L3 分级成本护栏，最终由视频能力模块（quote_estimate / tts_generate / image_generate / video_generate / media_compose）合成 output/final.mp4。触发词：短视频、出片、分镜、视频创作、AI 视频、口播、带货视频、解说二创、storyboard、短剧、video。
license: MIT
platforms: [macos, linux, windows]
metadata: {"hermes":{"tags":["video","shortvideo","storyboard","orchestration","media"],"category":"media"},"author":"niuniu"}
---

# 短视频一站式创作编排（Shortvideo Forge）

把一个故事 / 商品 / 想法（或一批现有素材）编排成可交付的短视频成片。本 skill 是**编排纪律**：子任务拓扑、产物族、质量闸门与成本护栏都在这里定义；逐工具的调用配方与 FFmpeg 细节见 `references/`。

**做**：多波次创作编排（子 issue 流转 + 波内并行子 agent）、结构化产物族落 `video-project/`、G0–G5 质量闸门、L1/L2/L3 分级成本护栏、最终合成 `output/final.mp4`。

**不做（硬边界）**：时间轴 / 画布 / 剪辑 UI；内置合成服务；搬任何第三方项目代码；深度精剪引导用户去剪映 / PR 等专业工具。

两条路径：

- **完整流程（默认）**：主 issue = Epic + 子 issue 波次流转 + 审查列闸门（下文 §1–§6）。
- **快速模式**：不看板流转、无闸门矩阵，控制 agent 一口气线性跑完，产物族 schema 完全兼容。仅建议一次性快速出片，可随时升级为完整流程。

## 1. 子任务拓扑（R1：单控制工作空间）

主 issue 作为 **Epic 父任务**；子任务用 `batch_create_issues` 一次建齐（每个 task 带 `parent_issue_id=<主 issue>` 与 `exec_wave`）。**5 个子 issue 覆盖 wave 0–3**；wave4（生成与合成）由控制会话直接驱动能力工具完成，不建子 issue（如需独立审核留痕，也可补一个同 Epic 的 task）。

```
batch_create_issues(tasks=[
  {title:"wave0 导演阐述", description:"…基调/节奏/角色心象，用户确认基调", issue_type:"task", parent_issue_id:<主issue>, exec_wave:0},
  {title:"wave1 编剧",     description:"storyline.json + script.json，过 G1", issue_type:"task", parent_issue_id:<主issue>, exec_wave:1},
  {title:"wave2 角色设计", description:"characters.json + 参考图，过 G2",     issue_type:"task", parent_issue_id:<主issue>, exec_wave:2},
  {title:"wave2 场景设定", description:"scenes.json + 参考图，过 G2",         issue_type:"task", parent_issue_id:<主issue>, exec_wave:2},
  {title:"wave3 分镜",     description:"storyboard.json（引用 wave1/2 资产 id），过 G3", issue_type:"task", parent_issue_id:<主issue>, exec_wave:3},
])
```

| wave | 子 issue（产物） | 执行者 | 审核点 |
|:---:|---|---|---|
| 0 | 导演阐述（导演自产，全管线必读） | 导演 / 控制会话 | 用户确认基调（最便宜的对齐点） |
| 1 | 编剧 → `storyline.json` + `script.json` | S1 会话 | G1 故事线 + 剧本评审（改这里最便宜） |
| 2 | 角色设计 ∥ 场景设定 → `characters.json` / `scenes.json` + `assets/` 参考图 | S2 会话，两个子 agent 并行 | G2 角色卡 / 场景卡评审 |
| 3 | 分镜 → `storyboard.json`（引用 wave1/2 资产 id） | S3 会话 | G3 分镜 + 报价联合评审（花钱总闸门） |
| 4 | 生成与合成 → `shots/` + `output/final.mp4` | 控制会话调能力工具 | G4 逐镜质检 + G5 成片验收 |

纪律：

1. **波间串行、波内并行**：`exec_wave` 标波次（波间依赖）；波内并行（角色 ∥ 场景这类）用**会话内子 agent** 承担，每个子 agent 只承载单产物职责，不额外起工作空间。wave N+1 必须等 wave N 的产物 `review_status=approved` 才开始。
2. **按波次分会话（3 个会话）**：S1 编剧（wave0 导演阐述由导演自产 + wave1 编剧）→ S2 角色 ∥ 场景（wave2）→ S3 分镜（wave3，并延续驱动 wave4 生成合成）。每个会话开始前，用「导演阐述 + 产物区现状」刷新心智，避免长会话衰减。子 agent 只做单产物，不串联多产物职责。
3. **逃生门（默认不用）**：个别子任务确需异构 MCP 工具环境时，才对该子任务单独 `start_workspace(issue_id)`；逃生门空间按最小授权声明工具（不继承全组），其产物仍按 `video-project/` 布局产出，完成后并入控制空间产物区。
4. **子任务自治**：逐子任务 `update_issue(goal_condition)` 写明收敛判据；产物审核走 `approve_review` / `request_changes`——打回 = 把修改意见交回控制空间重做该产物（revision 递增）→ 再评审。
5. **产物直落产物区**，无归档同步；主 issue 为 Epic，按看板规则**需人工审核后移入完成列**（Epic 不自动收尾）。收尾前确认 `git status` 干净（产物目录不入库）。

## 2. 产物族（唯一事实源）

```
video-project/
  project.json          # 元信息：标题 / 画风包 / 目标平台 / 画幅 / 目标时长 / 质量档位 / 资产索引
  directorial-brief.md  # wave0 导演阐述（全管线必读）
  storyline.json        # wave1 故事线
  script.json           # wave1 剧本（分场 / 台词 / 旁白）
  characters.json       # wave2 角色卡（appearance_prompt / reference_image / seed）
  scenes.json           # wave2 场景卡
  storyboard.json       # wave3 分镜（唯一生成事实源；id 引用 wave1/2 资产）
  changes/              # 修改请求 chg-*.json（层二路由载体）
  assets/               # 角色 / 场景参考图、TTS 音频等
  shots/                # 逐镜中间产物（候选与选定素材）
  quotes/               # 报价留痕 <quote_id>.json
  qc/                   # 逐镜评分 / 淘汰记录、G5 验收记录
  output/               # final.mp4 等交付物
  .gitignore            # 产物目录不入库
```

五份 JSON 的用途：

| 产物 | 用途 | 关键点 |
|---|---|---|
| `storyline.json` | 故事线：主旨、起承转合、每段情绪与时长预算 | 改这里最便宜 |
| `script.json` | 剧本：分场、台词、旁白文案 | G1 评审对象 |
| `characters.json` | 角色卡：每个角色的外观 / 性格 / 声线 | 含一致性锚三件套 |
| `scenes.json` | 场景卡：每个场景的环境 / 光线 / 氛围 | 含一致性锚 |
| `storyboard.json` | 分镜：逐镜 duration / 画面 / 旁白 / 字幕 / 转场 | **唯一生成事实源**，工具层只消费 `approved` 版本 |

- **id 引用制**：`storyboard.json` 每镜以 `character_ids` / `scene_id` 引用 `characters.json` / `scenes.json` 的资产 id，**不内联文案**——这是跨镜一致性与单点修改的基础。控制 agent 必须校验引用完整性（所有 id 可解析），断链分镜不进 G3。
- **一致性锚（AI 视频核心难题的对策）**：角色卡含 `appearance_prompt`（稳定模板段，描述外观特征，措辞不随镜头变化）+ `reference_image`（`assets/` 参考图）+ 可选 `seed`；生成时**模板化拼接** prompt（画风包段 + 角色 appearance_prompt 段 + 场景段 + 镜头动作），不靠 LLM 每次即兴复述；画风包模板段强制进所有视觉 prompt，**不允许 agent 即兴风格词**。
- 每份产物顶层带 `revision`（整数，每次重做递增）与 `review_status`（`draft|in-review|approved`）；审核打回后 revision 递增再评审。
- 分镜的机器可校验 schema：`schemas/storyboard.schema.json`（必填字段为冻结契约：`title / aspect_ratio / fps / resolution / review_status / shots[{id, duration_sec, narration, subtitle, visual{type,tier,prompt,asset}, tts{voice,asset}, transition}]`；可选合规字段：顶层 `aigc_label: true` + `aigc_label_text`，含 AI 生成内容时置 true，`media_compose` 合成时自动烧录显式标识角标并记入 G5 技术 QC）。`media_compose` 的硬前置是 `review_status == "approved"`。
- 修改请求（层二 / 看板路由的交换格式，落 `changes/`）：`{id, target:"<产物>#<路径>", kind:"annotation|edit", content, route:"<子issue>", status:"pending|dispatched|regenerated|approved"}`。

## 3. 质量闸门 G0–G5

工程语义：AI 生成是随机的，单次质量不可承诺；本体系保证**不合格产物进不了成片**——逐层三道审（机器确定性审 / AI 评审 / 人审），坏镜被拦截、修复或淘汰。闸门挂在看板流转上，**不可跳过**。逐闸门检查项明细见 `references/quality-rubrics.md`。

| 闸门 | 机器审 | AI 审 | 人审 |
|:--:|---|---|---|
| G0 导演阐述 | 模板字段完整（意图 / 基调 / 节奏 / 各场气氛 / 角色心象） | — | 基调确认 |
| G1 剧本 / 故事线 | 结构完整、时长预算、违禁词扫描 | 判官团（钩子 / 逻辑 / 节奏逐项挑刺、等长重写） | 评审列 |
| G2 角色 / 场景图 | 尺寸 / 格式 | `read_image` 回看：风格一致 / 细节；多角色同框测试 | 人工选图 |
| G3 分镜表 | id 引用完整、时长合计 = 目标、字段齐备 | 逐镜可行性打分 | **分镜 + 报价联合评审（花钱总闸门）** |
| G4 镜头素材 | 分辨率 / 时长 / 格式 | 抽帧逐维打分，低分自动进修复回路 | 抽检 + 坏镜修复确认 |
| G5 成片 | 技术 QC：音画同步 / 字幕时轴 / 电平 / 时长 / AIGC 标识位 | 整体观感（节奏 / 转场 / 音乐情绪） | **结构化 checklist 全过才放行交付（发布门禁）** |

**判官团要点（G1 / 精制档加轮）**：三个独立评审角色各自逐条挑刺，不互相迁就——钩子官（前 3 秒 / 首句能否留住目标观众，给 1 条替代开场）、逻辑官（设定 / 动机 / 因果断裂，逐条列出）、节奏官（起承转合与时长分配，哪里该删 / 该扩）。每条意见必须配「**等长重写**」示例（与原文等字数）便于直接替换；任一官给出不可修复级问题（人设崩坏 / 事实错误 / 违规内容）即整体不通过。

**逐维打分维度**（每维 1–5 分，满分 20）：G3 逐镜四项——可生成性（prompt 具体可执行）/ 一致性（角色、场景引用明确且与卡一致）/ 时长合理性（与旁白字数匹配）/ 与剧本贴合度；G4 每候选四项——构图与主体清晰度 / 与 prompt 符合度 / 跨镜一致性（对照参考图与同角色其它镜）/ 技术质量（清晰度、抖动、畸变、闪烁）。**淘汰线：任一维 <3 或总分 <14** → 不合格，自动进修复回路。

**修复回路纪律（G4）**：

1. 诊断坏镜，明确坏在哪个维度；
2. **单变量重投**——一次只改 prompt / 模型 / 候选之一，改动记录在 `qc/`；
3. 每镜修复上限 **2 轮**；
4. 超限**升级人工三选一**：换方案（改镜头设计）/ 降档（L3 → L2 图串片）/ 换镜（删除或替换该镜）；
5. 不允许静默无限重试；修复只重做坏镜，成本恒为单镜。

**qc/ 留痕约定**：逐镜 `qc/<shot-id>.json` 记 `{shot_id, gate, prompt_version, candidates:[{path, scores:{逐维分数}, total, verdict, reject_reason}], rounds:[{round, changed, note, result}], verdict:"pass|escalated"}`；成片 `qc/g5-final.json` 记 checklist 逐项结果 + 技术 QC 数字（时长 / 响度 / 分辨率）+ 结论。**淘汰原因必填**，候选与评分可回溯；G4 低分候选**不得进入合成**。

**G5 结构化验收 checklist（逐项全过才可 approve 交付）**：

- [ ] 成片可播放：`output/final.mp4` 可解析，时长 = 各镜 `duration_sec` 之和（误差 ≤ 0.5s）
- [ ] 音画同步：抽查首 / 中 / 尾三镜，画面与旁白起止对齐（偏差 ≤ 0.3s）
- [ ] 字幕时轴：与 `storyboard.json` 逐镜 `subtitle` 文本一致，入 / 出时间与旁白对齐，断句可读、不遮挡主体
- [ ] 电平：整体响度 -16 LUFS ±2（或峰值 ≤ -1 dBTP），无爆音、无异常静音段
- [ ] 画幅与分辨率：与 `resolution` / `aspect_ratio` 一致（默认 720p），全片统一，无异常黑边
- [ ] 一致性：同一角色跨镜形象偏差可接受，无闪烁 / 跳变；转场与分镜 `transition` 一致
- [ ] AIGC 标识：按 GB 45438-2025 在片头 / 片尾或元数据中带显式 AI 生成标识；`storyboard.json` 顶层 `aigc_label: true` 时 `media_compose` 已把标识角标烧录进成片（QC 记录 `aigc_label` / `aigc_label_burned`）——交付前确认 `qc/final-qc.json` 的 `aigc_label_burned` 为 true 且无相关 warning（要求了却没烧上 = 不合格）；手工合成路径按 `references/compose-recipe.md` §3.1 自行烧录
- [ ] 版权：所有素材为自有或明确可商用来源，BGM 无版权风险，图库素材已注明来源
- [ ] 留痕：`quotes/` 与 `qc/` 完整，候选与淘汰原因可回溯，无未记录的付费调用

## 4. 成本纪律（分级确认 + 质量档位）

| 档位 | 覆盖 | 确认要求 | 留痕 |
|---|---|---|---|
| **L1** 自有素材 / 图库 | 零成本 | 无需确认 | 素材来源登记（图库需注明来源） |
| **L2** AI 文生图 / TTS | 单次分~毛级 | **直接放行** | 调用前 `quote_estimate` 报价单落 `quotes/`，汇总随 G3 分镜评审可见 |
| **L3** AI 图生视频 | 按秒计费 | **`quote_estimate` → `ask_user` 报价确认，批准后才 dispatch** | `quotes/` 完整留痕（含任务号） |

- **L3 确认不可绕过**：报价单备好（分项 + 总额）→ 用 `ask_user`（niuniu 工具名 `niuniu_ask_user_question`）请用户批准 → 批准后才发起生成。报价被拒 / 超用户上限 → **回落 L2 出图，成片改图串片**，并明示降档。
- **失败不自动重试**：生成 API 失败时留痕 `quotes/`（含明确错误与任务号），交用户决定，不做静默重试。
- **默认 720p**：`resolution` 默认 `1280x720`（横屏）/ `720x1280`（竖屏），更高分辨率需用户显式要求。
- **质量档位**（写进 `project.json`，逐镜可覆盖；候选 / 修复预算计入报价单——预算换质量显式可见）：

| 档位 | 候选数 | 修复轮次 | AI 审强度 | 适用 |
|---|---|---|---|---|
| 草稿 | 1 | 0–1 | G3 / G5 抽查 | 内部预览、验证想法 |
| 标准（默认） | 2 | 2 | 全闸 | 日常发布 |
| 精制 | 3–4 | 3 | 全闸 + 判官团加轮 | 重要投放 |

## 5. 降级链与错误处理

| 场景 | 行为 |
|---|---|
| `video-gen` 能力模块未装配 / 被停用 | 场景卡片提示；不调用生成工具——走临时配方（`references/compose-recipe.md`）或只交付产物族 + 素材清单 |
| 无 ffmpeg（模块未内嵌且 PATH 无） | 只出产物族 + 素材清单，不合成，并说明缺什么（正式发行版内嵌 ffmpeg，此路径仅开发构建兜底） |
| 无 TTS / 图像 / 视频账号（能力配置为空） | 对应档位不可用并在评审信息中明示；L1 自有素材兜底 |
| L3 报价被拒 / 超上限 | 回落 L2 出图，成片改图串片 |
| 生成 API 失败 | 任务留痕 `quotes/`，不自动重试，用户决定 |
| 产物不合格 | `request_changes` 打回，控制空间按意见重做该产物（revision 递增）后再评审 |
| 可选免费 TTS 路径（Edge 等） | 默认关闭、显式启用；启用时告知数据流向第三方端点 |

降级一律**优雅提示、不抛错误堆栈**；任何降级都要在给用户的结论里写明「哪一层降了、影响什么、下一步可选什么」。

## 6. 素材版权与 AIGC 标识

- **版权纪律**：只用用户自有或明确可商用的素材；免费图库仅作补充并注明来源；BGM 必须无版权风险；**不使用未授权的影视 / 他人作品片段**。来源不明时停下询问用户，不擅自使用。
- **AIGC 标识提醒**：按 GB 45438-2025 参考要求，AI 生成内容需带显式标识——在片头 / 片尾或元数据中标注（如「本片含 AI 生成内容」），标识位纳入 **G5 技术 QC**，缺标识不放行。工具层已内建：分镜顶层置 `aigc_label: true`（可用 `aigc_label_text` 改文字，缺省「AI 生成」）→ `media_compose` 在拼接后于画面左上角烧录整片常驻的半透明标识角标，并写入 `qc/final-qc.json`（`aigc_label` / `aigc_label_burned` / `aigc_label_text`）；标识烧录失败时合成仍出片但 QC 判不合格且返回 warning，**不得静默放行**。
- 密钥与隐私：能力配置的密钥只存在于本机能力配置，**绝不写进产物 / 报价单 / 日志 / git**；产物目录不入库。

## 7. 内置资源（按需读，不必预先全部加载）

| 文件 | 什么时候读 |
|---|---|
| `schemas/storyboard.schema.json` | 写 / 校 `storyboard.json` 时（必填字段与枚举的机器契约） |
| `references/quality-rubrics.md` | 执行任一闸门（G0–G5）的机器审 / AI 审 / 人审检查项与评分维度时 |
| `references/tts-image-video-recipes.md` | 调 `quote_estimate` / `tts_generate` / `image_generate` / `video_generate` / `media_compose`，或走 L1/L2/L3 档位时 |
| `references/compose-recipe.md` | 无模块 / 无 `media_compose` 时手工合成，或需要坏镜单点重合成、排障 FFmpeg 时 |
