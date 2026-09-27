# FFmpeg 合成配方（手工 / 降级路径）

**何时用**：`media_compose` 工具可用时**优先用它**（它内建了本配方并执行 G5 技术 QC）。本文件用于：模块未装配 / 停用、无 ffmpeg 内嵌的开发构建、或需要手工排障、坏镜单点重合成时。

## 0. 约定与 ffmpeg 解析

- 目标规格：**默认 720p**——横屏 `1280x720`、竖屏 `720x1280`；H.264 + AAC + `faststart`；帧率取自 `storyboard.json` 的 `fps`（默认 30）。
- 输入：逐镜素材 `shots/<id>-<variant>.mp4`（或静图 `.png`）、配音 `assets/tts-<id>.mp3`、BGM 由用户提供（版权自担，见 SKILL §6）。
- ffmpeg 解析顺序：`$NIUNIU_FFMPEG` → `~/.niuniu/bin/ffmpeg/<指纹>/ffmpeg[.exe]` → PATH（`ffmpeg -version` 自检）。找不到就按降级链只交付产物族 + 素材清单。
- 全部命令把 `ffmpeg` / `ffprobe` 换成解析到的实际路径。

## 1. 逐镜标准化（每镜一条命令，坏镜单点重跑就是重跑它）

先把每个镜头统一到相同编码参数、相同画布、**且一定有音轨**（无配音的镜头用 `anullsrc` 补静音），这是后续无损拼接的前提。

```bash
# 静图镜头 + 配音（duration 用分镜的 duration_sec）
ffmpeg -y -loop 1 -t 3.5 -i shots/01.png -i assets/tts-01.mp3 \
  -filter_complex "[0:v]scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p,setpts=PTS-STARTPTS[v];[1:a]aresample=48000,asetpts=PTS-STARTPTS[a]" \
  -map "[v]" -map "[a]" -shortest \
  -c:v libx264 -crf 20 -preset medium -c:a aac -b:a 192k shots/render/01.mp4

# 视频镜头 + 配音
ffmpeg -y -i shots/01-a.mp4 -i assets/tts-01.mp3 \
  -filter_complex "[0:v]scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p,setpts=PTS-STARTPTS[v];[1:a]aresample=48000,asetpts=PTS-STARTPTS[a]" \
  -map "[v]" -map "[a]" -shortest \
  -c:v libx264 -crf 20 -preset medium -c:a aac -b:a 192k shots/render/01.mp4
```

无配音的镜头把 `-i assets/tts-01.mp3` 换成 `-f lavfi -t 3.5 -i anullsrc=r=48000:cl=stereo`（`-t` = 该镜 `duration_sec`）。

注意：
- `duration_sec` 是权威时长。TTS 返回的音频时长与它偏差 >10% 时，**先改文案 / 语速或调整分镜时长再重生成**，不要用硬裁切糊过去。
- 竖屏项目把 `scale/pad` 的 `1280:720` 全部换成 `720:1280`。

## 2. 拼接（参数已统一，可流拷贝）

```bash
printf "file 'shots/render/01.mp4'\nfile 'shots/render/02.mp4'\nfile 'shots/render/03.mp4'\n" > shots/render/concat.txt
ffmpeg -y -f concat -safe 0 -i shots/render/concat.txt -c copy shots/render/all.mp4
```

## 3. 字幕（.ass 从 storyboard 生成，再烧录）

字幕时间轴**从 `storyboard.json` 累加 `duration_sec` 生成**——产物是唯一事实源，不手工拍脑袋。生成脚本（标准库 Python，无联网依赖）：

```python
import json
sb = json.load(open("storyboard.json", encoding="utf-8"))
W, H = sb["resolution"].split("x")
def ts(t):
    h = int(t // 3600); m = int(t % 3600 // 60); s = t % 60
    return f"{h:d}:{m:02d}:{s:05.2f}"
L = ["[Script Info]", "ScriptType: v4.00+", "WrapStyle: 0",
     f"PlayResX: {W}", f"PlayResY: {H}", "[V4+ Styles]",
     "Format: Name, Fontname, Fontsize, PrimaryColour, OutlineColour, BackColour, "
     "Bold, Italic, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding",
     "Style: Sub,Noto Sans CJK SC,42,&H00FFFFFF,&H00000000,&H80000000,0,0,1,2,1,2,60,60,48,1",
     "[Events]", "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text"]
t = 0.0
for sh in sb["shots"]:
    d = float(sh["duration_sec"]); txt = sh.get("subtitle", "")
    if txt:
        L.append(f"Dialogue: 0,{ts(t)},{ts(t+d)},Sub,,0,0,0,,{txt.replace(chr(10), chr(92)+'N')}")
    t += d
open("subs.ass", "w", encoding="utf-8").write("\n".join(L) + "\n")
```

- 字体名用系统通用 CJK 回退（如 `Noto Sans CJK SC` / `Microsoft YaHei`），**不要写死单平台字体**；每行 ≤ 16 个汉字，换行用 `\N`。
- 烧录即下方 `compose.filter` 里的 `ass=subs.ass`（需要 ffmpeg 带 libass，静态构建默认带）。

## 4. 拼接后总装：字幕 + BGM 混音 + 编码（一次过）

`compose.filter`（`-filter_complex_script` 文件，避免命令行转义地狱）：

```
[0:v]ass=subs.ass[vout];
[1:a]volume=0.18[bgm];
[0:a][bgm]amix=inputs=2:duration=first:dropout_transition=0:normalize=0,loudnorm=I=-16:TP=-1.5:LRA=11[aout]
```

```bash
ffmpeg -y -i shots/render/all.mp4 -stream_loop -1 -i assets/bgm.mp3 \
  -filter_complex_script compose.filter \
  -map "[vout]" -map "[aout]" \
  -c:v libx264 -crf 20 -preset medium -pix_fmt yuv420p \
  -c:a aac -b:a 192k -movflags +faststart \
  output/final.mp4
```

- BGM 音量系数 0.18 是起点，最终以 G5 电平项（-16 LUFS ±2 / 峰值 ≤ -1 dBTP）为准；无 BGM 时删掉 `[1:a]` 与 amix 段，直接 `[0:a]loudnorm=...`。
- `normalize=0` 需要 FFmpeg ≥ 5.1（BtbN 静态构建满足）；更老版本删掉它，并接受 amix 的默认衰减后由 loudnorm 兜底。

## 5. 坏镜单点重合成

1. 只重跑**该镜**的第 1 步（新候选 → `shots/render/<id>.mp4`）；
2. 更新 `concat.txt` 后重复第 2–4 步；
3. 若该镜时长变了，第 3 步的字幕脚本必须重新生成（时间轴整体平移）；
4. 其它镜头一律不重跑——修复成本恒为单镜。

## 6. 技术 QC（G5 用）

```bash
ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 output/final.mp4
ffprobe -v error -select_streams v:0 -show_entries stream=width,height,r_frame_rate -of default=nw=1 output/final.mp4
ffmpeg -hide_banner -i output/final.mp4 -af volumedetect -f null -   # 看 mean_volume / max_volume
```

把数字填进 `qc/g5-final.json`，再走 SKILL §3 的 G5 checklist。

## 7. 常见坑

| 现象 | 原因 / 处置 |
|---|---|
| 拼接后花屏、时长错乱 | 直接 concat 参数不一致的素材——必须先做第 1 步标准化 |
| 播放器里画面被拉伸 | SAR/DAR 不匹配——`setsar=1` + `force_original_aspect_ratio=decrease` + `pad` |
| concat 报音视频轨不齐 | 有镜头缺音轨——用 `anullsrc` 补齐 |
| 字幕不显示 / 方框 | 字体名写死或系统无该字体——用通用 CJK 字体名 |
| 首帧加载慢、拖动卡顿 | 忘了 `-movflags +faststart`（必须在最后输出这一步加） |
| 音量忽大忽小 | 逐镜响度不一致——总装统一过 `loudnorm`，不要逐镜各调 |
