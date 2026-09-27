# 质量闸门检查项明细（G0–G5）

三道审的落点：**机器审** = 工具层校验 + skill 步骤（确定性，可脚本化）；**AI 审** = rubric + 看板 AI 预审（`ai_native_review`）与 `read_image` 回看；**人审** = 审查列（`approve_review` / `request_changes`）+ issue checklist 逐项打勾。闸门**不可跳过**；承诺的是「闸门存在且不可跳过」，rubric 随项目复盘迭代。

统一通过条件：该闸门所有机器审项通过 + AI 审无「不合格」判定（阈值见各节）+ 人审确认（需要时）。不通过 = 产物标 `in-review` 并 `request_changes` 打回对应子 issue 重做（revision 递增）。

## G0 · 导演阐述（directorial-brief.md）

- 机器审：模板字段完整——整体意图、基调（视听风格）、节奏（段落时长感觉）、各场气氛、角色心象（每个主要角色一段）。
- AI 审：无（篇幅短、成本低，直接给人看）。
- 人审：**用户确认基调**——这是全片最便宜的对齐点，未确认不得进入 wave1。
- 通过动作：`directorial-brief.md` 定稿；该文件是全管线必读（每个会话开场都读）。

## G1 · 剧本 / 故事线（storyline.json + script.json）

- 机器审：
  - `storyline.json` 结构完整（主旨 / 起承转合 / 每段情绪与时长预算）；
  - 时长预算合计 = `project.json` 目标时长（±10%）；
  - 违禁词扫描（平台违规词、夸大宣传、未授权 IP 名）；
  - 顶层 `revision` 与 `review_status` 字段存在。
- AI 审（**判官团**，可跑 1–2 轮；精制档加轮）：
  - 钩子官：前 3 秒 / 首句能否留住目标观众；给 1 条替代开场。
  - 逻辑官：设定 / 动机 / 因果是否断裂；逐条列出。
  - 节奏官：起承转合与时长分配是否合理；哪里该删 / 该扩。
  - 每条意见必须附「**等长重写**」示例（与原文等字数）；任一官给出不可修复级问题（人设崩坏 / 事实错误 / 违规内容）→ 整体判不通过。
- 人审：审查列评审（此刻改动最便宜——故事层返工成本远低于素材层）。
- 通过动作：`review_status=approved`，放行 wave2。

## G2 · 角色 / 场景图（characters.json / scenes.json + assets/）

- 机器审：参考图存在、尺寸与画幅匹配、格式为 png/jpg；角色卡含 `appearance_prompt` + `reference_image`（+ 可选 `seed`），场景卡含环境 / 光线 / 氛围；两张卡都不内联进分镜（只被 id 引用）。
- AI 审（`read_image` 回看）：
  - 风格一致性：与画风包描述一致，组内（多角色 / 多场景）互相不串味；
  - 细节：五官 / 服装 / 关键道具无畸变（多指、融脸、文字乱码）；
  - **多角色同框测试**：把主要角色合成一图回看，验证形象可区分、比例合理。
- 人审：**人工选图**（AI 先筛掉废片，人只在及格线以上挑）。
- 通过动作：卡片 `approved`，参考图进 `assets/`；不一致即重出（重出属 L2 调用，留痕 `quotes/`）。

## G3 · 分镜表（storyboard.json）——花钱总闸门

- 机器审（对照 `schemas/storyboard.schema.json`）：
  - id 引用完整：所有 `character_ids` 可解析到 `characters.json`、`scene_id` 可解析到 `scenes.json`；
  - 时长合计 = 目标时长（窄容差，如 ±0.5s）；
  - 必填字段齐备（`title / aspect_ratio / fps / resolution / review_status / shots[...]`）；
  - `visual.tier` 分布与账号能力匹配（无 L3 账号却标 L3 → 先降档改标）。
- AI 审：**逐镜可行性打分**（1–5 分，每镜四项）：可生成性（prompt 具体可执行，非抽象形容词堆砌）、一致性（角色 / 场景引用明确且与卡一致）、时长合理性（与旁白字数匹配）、与剧本贴合度。任一维 <3 或总分 <14/20 的镜头退回修改后再评审。
- 人审：**分镜 + 报价联合评审**——报价单（`quotes/` 汇总，含 L3 逐镜分项与候选 / 修复预算）与分镜一起展示；用户在此一次性决定「花多少钱、拍成什么样」。L3 报价的 `ask_user` 确认在此前后完成。
- 通过动作：`review_status=approved`——**这是唯一允许进入生成的版本**（`media_compose` 硬前置）。

## G4 · 镜头素材（shots/）

- 机器审：分辨率 / 画幅 = 分镜声明、时长 = `duration_sec`（±10%）、格式可被 ffprobe 解析、音轨存在（无配音为静音轨）。
- AI 审：**抽帧逐维打分**（每候选按 4 维 1–5 分）：
  - 构图与主体清晰度；与 prompt 的符合度；跨镜一致性（与参考图 / 同角色其它镜）；技术质量（清晰度 / 抖动 / 畸变 / 闪烁）。
  - 淘汰线：任一维 <3 或总分 <14/20 → 不合格，自动进修复回路。
- 人审：抽检（每个角色首出镜、钩子镜必看）+ 坏镜修复确认。
- **修复回路**：诊断坏镜 → **单变量重投**（一次只改 prompt / 模型 / 候选之一，记录 `rounds[].changed`）→ 每镜上限 **2 轮** → 超限升级人工三选一（换方案 / 降档 / 换镜），不允许静默重试。
- 通过动作：选定素材写入 `visual.asset`；所有候选与淘汰原因留在 `qc/<shot-id>.json`，低分候选绝不进入合成。

## G5 · 成片（output/final.mp4）——发布门禁

- 机器审（技术 QC，数字落 `qc/g5-final.json`）：音画同步、字幕时轴、电平、时长、画幅、AIGC 标识位（检查项与阈值见下）。
- AI 审：整体观感——节奏是否拖沓 / 跳脱、转场是否自然、音乐情绪与基调是否匹配（对照 `directorial-brief.md`）。
- 人审：**结构化 checklist 逐项全过**才可 `approve_review` 放行交付；未全过时清单未过项即修复清单（哪项不过改哪项），重验后再走本闸门。

**G5 checklist（与 SKILL §3 一致，逐项打勾）**：

- [ ] 成片可播放：`output/final.mp4` 可解析，时长 = 各镜 `duration_sec` 之和（误差 ≤ 0.5s）
- [ ] 音画同步：抽查首 / 中 / 尾三镜，画面与旁白起止对齐（偏差 ≤ 0.3s）
- [ ] 字幕时轴：与 `storyboard.json` 逐镜 `subtitle` 文本一致，入 / 出时间与旁白对齐，断句可读、不遮挡主体
- [ ] 电平：整体响度 -16 LUFS ±2（或峰值 ≤ -1 dBTP），无爆音、无异常静音段
- [ ] 画幅与分辨率：与 `resolution` / `aspect_ratio` 一致（默认 720p），全片统一，无异常黑边
- [ ] 一致性：同一角色跨镜形象偏差可接受，无闪烁 / 跳变；转场与分镜 `transition` 一致
- [ ] AIGC 标识：按 GB 45438-2025 在片头 / 片尾或元数据中带显式 AI 生成标识
  - 工具事实：分镜设 `aigc_label: true` 时 `media_compose` 会自动烧制角标（独立 `output/aigc-label.ass`，不污染台词字幕），并在 `qc/` 记录 `aigc_label_burned`；**要求了却没烧上（`aigc_label_burned=false` 或 `checks_passed=false`）= G5 不合格，不得放行交付**
- [ ] 版权：所有素材为自有或明确可商用来源，BGM 无版权风险，图库素材已注明来源
- [ ] 留痕：`quotes/` 与 `qc/` 完整，候选与淘汰原因可回溯，无未记录的付费调用

## qc/ 留痕结构

`qc/<shot-id>.json`（G4）：

```json
{
  "shot_id": "01",
  "gate": "G4",
  "prompt_version": 2,
  "candidates": [
    {"path": "shots/01-a.mp4", "scores": {"composition": 4, "adherence": 5, "consistency": 3, "technical": 4},
     "total": 16, "verdict": "pass", "reject_reason": ""},
    {"path": "shots/01-b.mp4", "scores": {"composition": 2, "adherence": 3, "consistency": 3, "technical": 4},
     "total": 12, "verdict": "reject", "reject_reason": "构图失衡，主体被裁"}
  ],
  "rounds": [{"round": 1, "changed": "prompt", "note": "补主体位置描述", "result": "达标"}],
  "verdict": "pass"
}
```

`qc/g5-final.json`（G5）：`{"gate":"G5", "checks":[{"item":"av_sync","result":"pass","value":"..."}, ...], "tech":{"duration_sec":31.2,"loudness_lufs":-15.4,"resolution":"1280x720"}, "verdict":"pass|reject"}`。

约定：**淘汰原因必填**（可回溯）；`rounds[].changed` 只能是 `prompt|model|candidate` 之一（单变量纪律的机器可读体现）；超限升级的镜头 `verdict="escalated"` 并写明三选一的人工决议。

## 质量档位对 AI 审强度的影响

| 档位 | G1 判官团 | G2/G3 AI 审 | G4 候选数 / 修复轮次 | G5 |
|---|---|---|---|---|
| 草稿 | 免 | G3 抽查 | 1 / 0–1 | 抽查（可只过人审） |
| 标准（默认） | 1 轮 | 全跑 | 2 / 2 | 全跑 |
| 精制 | 加轮 | 全跑 + 加严阈值 | 3–4 / 3 | 全跑 + 附加观感复核 |
