# 三级档位与工具调用配方（L1 / L2 / L3）

本文件是能力模块五个工具的调用配方。**参数名以工具自身 schema 为准**（调用时按工具描述传参；下面给的是典型形态）；工具缺席时按 SKILL §5 降级链处理，不报错误堆栈。

| 工具 | 作用 | 档位 / 成本 | 关键前置 |
|---|---|---|---|
| `quote_estimate` | 付费调用清单 → 报价单（分项 + 总额），落 `quotes/<quote_id>.json` | 任何付费调用前 | L3 确认的前置输入 |
| `tts_generate` | 文本 + voice / 语速 → 音频 + 时长元数据 | L2（放行 + 留痕） | 有 TTS 账号 |
| `image_generate` | prompt + 参考图 + 画幅 → png（多候选） | L2（放行 + 留痕） | 有图像账号 |
| `video_generate` | 首帧图 + 运动 prompt + 时长 → 异步任务（句柄 / 轮询 / 取片） | L3（**ask_user 报价确认**） | 批准后的报价单 |
| `media_compose` | approved storyboard + assets → 逐镜合成 → 拼接 + ass 字幕 + BGM → `output/final.mp4` | 本地零成本 | `review_status=approved`（硬前置） |

**通用纪律**

- prompt 一律**模板化拼接**：画风包段 + 角色 `appearance_prompt` 段 + 场景段 + 镜头动作；不从零即兴复述角色外观，不添加与画风包冲突的风格词。
- 角色一致性三件套当前必用前两件：`appearance_prompt` 模板段 + `reference_image`；后端支持时带 `seed` 并记录进角色卡。
- 密钥永不写进 prompt / 产物 / 报价单 / 日志；调用痕迹只落 `quotes/` 与 `qc/`。

## L1 — 自有素材 / 免费图库（零成本，兜底档）

1. 素材来源：工作空间内既有文件、用户上传、建舱时启用的 LFS 素材目录。
2. 登记：把实际路径写进 `project.json` 资产索引，并让 `storyboard.json` 的 `visual.tier="L1"`、`visual.asset` 指向它。
3. 免费图库仅作补充：**必须注明来源**（写进资产索引），且确认许可可商用；来源不明不用。
4. L1 不需要 `quote_estimate`，也不需要 `ask_user`。
5. 规格不统一时按 `compose-recipe.md` 第 1 步标准化（裁剪 / 缩放 / 补边 / 统一帧率），L1 也能直接出片。

## L2 — AI 文生图 / TTS（放行但留痕）

**流程（每个付费调用前重复）**：`quote_estimate` → 报价单落 `quotes/` → 直接调用（无需 ask_user）→ 结果与评分落 `shots/`、`qc/`。

### 参考图与首帧（wave2 / wave3 / wave4）

```
quote_estimate({capability:"image", items:[{shot_id:"01", candidates:2}]})   # 返回 quote_id，落 quotes/<quote_id>.json
image_generate({
  prompt: "<画风段> + <角色 appearance_prompt 段> + <场景段> + <镜头动作/构图/光线>",
  reference_images: ["assets/char-<id>.png", "assets/scene-<id>.png"],
  aspect_ratio: "16:9",
  candidates: 2,           # 质量档位：草稿 1 / 标准 2 / 精制 3–4
  seed: <可选，固定以复现>
})
```

- 关键镜（角色首出 / 特写 / 钩子镜）出满候选数；所有候选与逐维评分、淘汰原因落 `qc/<shot-id>.json`，**低分候选不进合成**。
- 多角色同框必须在参考图回归中验证（G2）；不一致时以「重出首帧」为修复手段（单变量重投）。

### 配音（TTS）

```
quote_estimate({capability:"tts", items:[{shot_id:"01", chars:<narration 字数>}]})
tts_generate({ text: "<narration>", voice: "<voice id>", speed: 1.0 })
# → assets/tts-01.mp3 + 时长元数据
```

- 音色写进 `characters.json`（角色声线）与 `storyboard.json` 的 `tts.voice`，全片同一角色同一音色。
- 返回时长与该镜 `duration_sec` 偏差 >10% 时先改文案 / 语速再重生成，不硬裁音频。

## L3 — AI 图生视频（按秒计费，必须报价确认）

**三步护栏，缺一不可**：

1. `quote_estimate` 汇总本片全部 L3 调用（逐镜时长 × 候选数 + 修复预算），报价单落 `quotes/` 并**随 G3 分镜评审展示**（花钱总闸门）；
2. 控制 agent 用 `ask_user`（`niuniu_ask_user_question`）请用户确认报价——**批准后才 dispatch**，被拒 / 超上限即回落 L2（成片改图串片）；
3. 逐镜发起：

```
video_generate({ first_frame:"shots/render/01.png", prompt:"<运动/镜头语言>", duration_sec:3.5, candidates:2 })
# → 异步：submit 返回任务句柄/任务号 → 轮询 Poll → Fetch 取片落 shots/
```

- **失败不自动重试**：把明确错误 + 任务号留痕 `quotes/`，交用户决定（重投属一次新的付费调用，需重新报价确认）。
- 候选 2–3 个（随质量档位）；评分与选择落 `qc/`；未选中的候选保留可回溯。
- i2v 运动 prompt 只描述**运镜与主体动作**（如「中景缓推，主角推门入座」），不重复首帧已有的外观描述。

## 合成（media_compose）

```
media_compose({ storyboard:"video-project/storyboard.json" })   # 硬前置：review_status == "approved"
# → 逐镜合成 → 拼接 + ass 字幕 + BGM 混音 → output/final.mp4，并执行 G5 技术 QC
```

- 被拒绝时（分镜未 approved）**不要绕过**：把分镜补审到 approved 再合成。
- 坏镜单点重合成：只重做坏镜素材后重跑合成；无 `media_compose` 时用 `references/compose-recipe.md` 手工完成。
- 合成后按 SKILL §3 的 G5 checklist 逐项验收，数字与结论落 `qc/g5-final.json`。

## 档位不可用时的兜底（对照 SKILL §5）

| 缺什么 | 退到哪 |
|---|---|
| 无图像账号 | L2 不可用——关键镜回落 L1 素材或纯字幕卡（图串片的前提是 L2） |
| 无 TTS 账号 | 只出旁白稿 + 字幕文件（.ass/.srt），或在用户明示后启用可选免费 TTS（默认关闭，启用需告知数据流向第三方） |
| 无视频账号 / 报价被拒 | L3 → L2 图串片（静图运镜） |
| 无 video-gen 模块 | 不调任何生成工具；只交付产物族 + 素材清单 + 手工合成配方 |
| 无 ffmpeg | 平台内嵌时不存在；开发构建下只交付产物族，并注明缺 ffmpeg |
