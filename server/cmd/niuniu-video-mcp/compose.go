package main

// media_compose — the FFmpeg final assembler (design §7.3 tool table).
//
// Pipeline (two stages, so a bad shot only redoes that shot):
//
//	stage 1  逐镜合成：<ws>/video-project/shots/<shot_id>.mp4
//	         每镜统一到 storyboard.resolution/fps，H.264(yuv420p)+AAC，
//	         人声取 tts.asset，缺省补静音轨；素材短于目标时长用 tpad 补帧，
//	         长于目标时长裁掉；transition=fade 的镜头加黑场淡入淡出。
//	         已存在且时长/流合格、且比素材新的 shots 复用（不重做）。
//	stage 2  整片装配：output/final.mp4
//	         concat 逐镜 → ass 字幕烧录 →（可选）AIGC 标识角标 → BGM 混音（amix）
//	         → H.264/AAC/faststart。AIGC 角标（storyboard.aigc_label=true）在
//	         拼接后只烧一次，绝不进逐镜合成。
//
// 滤镜一律经 -filter_complex_script 以脚本文件传入（Windows 命令行长度上限
// 32k，整片滤镜图随镜头数增长，内联会被截断）。
//
// 硬前置：storyboard.json 的 review_status 必须是 "approved" —— 未过审的分镜
// 绝不进入合成（设计 §5「工具层只消费 approved 的 storyboard.json」）。
//
// 失败处理：单镜失败不阻塞其余镜头的合成尝试，但只要存在失败镜就【不产出】
// final.mp4 —— 返回失败清单；重跑时合格 shots 复用，只重做坏镜。
// 生成 API 失败不自动重试（设计 §7.4），ffmpeg 缺失时本工具明确降级报错。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// composeOptions are the media_compose tool parameters.
type composeOptions struct {
	BGM       string  // BGM 文件（workspace 相对或绝对）；空 = 仅人声
	BGMVolume float64 // 0..1，默认 0.2
	Font      string  // 字幕字体，默认按平台选择
	OutputRel string  // 默认 output/final.mp4
	Force     bool    // true = 忽略缓存，全部重做
}

// shotResult reports one shot's stage-1 outcome.
type shotResult struct {
	ID          string  `json:"id"`
	Output      string  `json:"output"`
	Source      string  `json:"source"`
	Reused      bool    `json:"reused"`
	DurationSec float64 `json:"duration_sec"`
	Error       string  `json:"error,omitempty"`
}

// composeQC is the G5 technical QC record (basic tier) written to qc/.
// The AIGC label trio is the 标识位 item of the G5 checklist (GB 45438-2025 参考):
// AIGCLabel = 分镜要求标识（storyboard.aigc_label），AIGCLabelBurned = 标识确已
// 烧录进成片——两者都为 false 表示本片不需要标识；要求了却没烧上时
// ChecksPassed 为 false 且 Notes 说明，绝不静默。
type composeQC struct {
	CheckedAt           string   `json:"checked_at"`
	Output              string   `json:"output"`
	DurationSec         float64  `json:"duration_sec"`
	ExpectedDurationSec float64  `json:"expected_duration_sec"`
	DurationOk          bool     `json:"duration_ok"`
	HasVideo            bool     `json:"has_video"`
	HasAudio            bool     `json:"has_audio"`
	VideoCodec          string   `json:"video_codec,omitempty"`
	AudioCodec          string   `json:"audio_codec,omitempty"`
	Width               int      `json:"width,omitempty"`
	Height              int      `json:"height,omitempty"`
	Faststart           bool     `json:"faststart"`
	AIGCLabel           bool     `json:"aigc_label"`
	AIGCLabelBurned     bool     `json:"aigc_label_burned"`
	AIGCLabelText       string   `json:"aigc_label_text,omitempty"`
	AIGCLabelFile       string   `json:"aigc_label_file,omitempty"`
	Shots               int      `json:"shots"`
	Reused              int      `json:"reused"`
	Composed            int      `json:"composed"`
	ChecksPassed        bool     `json:"checks_passed"`
	Notes               []string `json:"notes,omitempty"`
}

// aigcLabelState carries the AIGC label outcome from the burn step into the QC
// record (Required 来自分镜，Burned 来自本次合成是否真的烧上了)。
type aigcLabelState struct {
	Required bool
	Text     string
	Burned   bool
	File     string // workspace 相对路径（已烧录时）
}

// composeReport is what the tool returns.
type composeReport struct {
	Final         string       `json:"final"`
	Shots         []shotResult `json:"shots"`
	Reused        int          `json:"reused"`
	Composed      int          `json:"composed"`
	DurationSec   float64      `json:"duration_sec"`
	SubtitleFile  string       `json:"subtitle_file,omitempty"`
	AIGCLabelFile string       `json:"aigc_label_file,omitempty"`
	QCFile        string       `json:"qc_file,omitempty"`
	QC            *composeQC   `json:"qc,omitempty"`
	Warnings      []string     `json:"warnings,omitempty"`
}

// isImagePath decides whether a source asset is a still image (→ -loop 1).
func isImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp", ".tif", ".tiff", ".gif":
		return true
	}
	return false
}

// composeFilm runs the full pipeline. It returns a report even on failure (the
// per-shot diagnostics are the useful part), with err non-nil when final.mp4
// was not produced.
func composeFilm(ctx context.Context, wsDir string, deps Deps, opts composeOptions) (*composeReport, error) {
	// 1. Hard precondition: the storyboard must be valid AND approved. Checked
	//    before ffmpeg so an unapproved storyboard is rejected identically with
	//    or without ffmpeg installed.
	sb, _, err := LoadStoryboard(wsDir)
	if err != nil {
		return nil, err
	}
	if sb.ReviewStatus != "approved" {
		return nil, fmt.Errorf("拒绝合成：storyboard.json 的 review_status 是 %q，媒体合成硬前置要求 \"approved\"。\n"+
			"请先在审查列完成分镜评审（approve_review）并把 storyboard.json 的 review_status 改为 approved 后再试。", sb.ReviewStatus)
	}
	if err := ensureDirs(wsDir); err != nil {
		return nil, err
	}

	// 2. ffmpeg (explicit degrade when unavailable).
	ffmpegBin, err := deps.ffmpegPath()
	if err != nil {
		return nil, fmt.Errorf("媒体合成降级：%w", err)
	}

	warnings := []string{}
	W, H, err := sb.ResolutionWH()
	if err != nil {
		return nil, err
	}
	// H.264 yuv420p requires even dimensions.
	if W%2 != 0 || H%2 != 0 {
		warnings = append(warnings, fmt.Sprintf("resolution %dx%d 含奇数边，已调整为 %dx%d（H.264 yuv420p 要求偶数）", W, H, W-W%2, H-H%2))
		W, H = W-W%2, H-H%2
	}
	fps := sb.FPS
	if fps <= 0 {
		fps = 30
		warnings = append(warnings, "storyboard.fps 非正数，按 30 处理")
	}

	report := &composeReport{Warnings: warnings}

	// 3. Stage 1 — per-shot normalization (cache-friendly).
	failed := []string{}
	for i, shot := range sb.Shots {
		id := sanitizeShotID(string(shot.ID))
		srcRef := AssetForShot(shot)
		srcPath := resolveWSAsset(wsDir, srcRef)
		res := shotResult{ID: string(shot.ID), Source: relToWS(wsDir, srcPath), DurationSec: shot.DurationSec}
		if srcRef == "" {
			res.Error = fmt.Sprintf("shots[%d]（id=%s）没有可用素材：visual.asset 为空", i, shot.ID)
			report.Shots = append(report.Shots, res)
			failed = append(failed, fmt.Sprintf("镜 %s：%s", shot.ID, res.Error))
			continue
		}
		if srcPath == "" {
			// 越界路径绝不落到文件系统上（storyboard 是不可信输入）。
			res.Source = strings.TrimSpace(srcRef)
			res.Error = "素材" + outsideWSErr("visual.asset", srcRef).Error()
			report.Shots = append(report.Shots, res)
			failed = append(failed, fmt.Sprintf("镜 %s：%s", shot.ID, res.Error))
			continue
		}
		if st, err := os.Stat(srcPath); err != nil || st.IsDir() {
			res.Error = fmt.Sprintf("素材文件不存在或不可读：%s", res.Source)
			report.Shots = append(report.Shots, res)
			failed = append(failed, fmt.Sprintf("镜 %s：%s", shot.ID, res.Error))
			continue
		}
		outPath := projectPath(wsDir, "shots", id+".mp4")
		res.Output = relToWS(wsDir, outPath)

		if !opts.Force {
			if reused, err := shotCacheUsable(ctx, ffmpegBin, outPath, srcPath, shot.DurationSec); err != nil {
				warnings = append(warnings, fmt.Sprintf("镜 %s 缓存检查失败（将重做）：%v", shot.ID, err))
			} else if reused {
				res.Reused = true
				report.Shots = append(report.Shots, res)
				report.Reused++
				continue
			}
		}

		if err := composeOneShot(ctx, ffmpegBin, wsDir, srcPath, shot, outPath, W, H, fps); err != nil {
			res.Error = err.Error()
			report.Shots = append(report.Shots, res)
			failed = append(failed, fmt.Sprintf("镜 %s：%v", shot.ID, err))
			continue
		}
		report.Shots = append(report.Shots, res)
		report.Composed++
	}

	if len(failed) > 0 {
		return report, fmt.Errorf("有 %d 个镜头合成失败，未产出 final.mp4（已成功的镜头已缓存，重跑只会重做失败镜）：\n  - %s",
			len(failed), strings.Join(failed, "\n  - "))
	}

	// 4. Stage 2 — concat + subtitles + BGM.
	finalPath := projectPath(wsDir, "output", "final.mp4")
	if strings.TrimSpace(opts.OutputRel) != "" {
		p := resolveWSAsset(wsDir, opts.OutputRel)
		if p == "" {
			return report, outsideWSErr("output", opts.OutputRel)
		}
		finalPath = p
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return report, fmt.Errorf("创建输出目录失败: %w", err)
	}
	report.Final = relToWS(wsDir, finalPath)

	subPath, hasSubs, err := writeSubtitlesASS(wsDir, sb, W, H, opts.Font)
	if err != nil {
		return report, err
	}
	if hasSubs {
		report.SubtitleFile = relToWS(wsDir, subPath)
	}

	concatList, err := writeConcatList(wsDir, report.Shots)
	if err != nil {
		return report, err
	}

	bgmPath := ""
	bgmRef := strings.TrimSpace(opts.BGM)
	if bgmRef == "" {
		bgmRef = strings.TrimSpace(sb.BGM)
	}
	if bgmRef != "" {
		bgmPath = resolveWSAsset(wsDir, bgmRef)
		if bgmPath == "" {
			return report, outsideWSErr("BGM", bgmRef)
		}
		if st, err := os.Stat(bgmPath); err != nil || st.IsDir() {
			return report, fmt.Errorf("BGM 文件不存在或不可读：%s", bgmRef)
		}
	}

	total := sb.ExpectedDuration()

	// AIGC 标识角标（可选，GB 45438-2025 参考）：整片常驻、独立 ass 文件，
	// 在拼接之后随最终编码一次烧录。生成失败不阻塞成片（便于排障），但绝不
	// 静默——report.Warnings + QC 明确记 aigc_label_burned=false 且不放行。
	label := aigcLabelState{Required: sb.AIGCLabel}
	labelPath := ""
	if sb.AIGCLabel {
		label.Text = sb.AIGCLabelText
		if strings.TrimSpace(label.Text) == "" {
			label.Text = defaultAIGCLabelText
		}
		p, err := writeAIGCLabelASS(wsDir, W, H, total, label.Text, opts.Font)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"AIGC 标识烧录被跳过：%v（storyboard.aigc_label=true，成片未带显式标识，G5 技术 QC 将判不合格）", err))
		} else {
			labelPath = p
			label.Burned = true
			label.File = relToWS(wsDir, p)
			report.AIGCLabelFile = label.File
		}
	}

	if err := assembleFilm(ctx, ffmpegBin, wsDir, concatList, subPath, hasSubs, labelPath, bgmPath, opts.BGMVolume, W, H, total, finalPath); err != nil {
		return report, err
	}

	// 5. G5 technical QC (basic tier) — write the record the delivery gate reads.
	qc, err := runTechnicalQC(ctx, ffmpegBin, wsDir, finalPath, total, len(sb.Shots), report.Reused, report.Composed, label)
	if err != nil {
		// 追加到 report.Warnings（而非构造期的 warnings 切片）——后者只被
		// 报告按长度拷贝一次，append 到这里之前的元素不会出现在返回值里。
		report.Warnings = append(report.Warnings, fmt.Sprintf("技术 QC 未能完成：%v", err))
	} else {
		report.QC = qc
		report.QCFile = relToWS(wsDir, projectPath(wsDir, "qc", "final-qc.json"))
		report.DurationSec = qc.DurationSec
		if !qc.ChecksPassed {
			report.Warnings = append(report.Warnings, "技术 QC 未全过（详见 qc/final-qc.json）：交付前请人工复核。")
		}
	}
	return report, nil
}

// shotCacheUsable reports whether an existing shots/<id>.mp4 can be reused:
// it must have video+audio, a duration matching the storyboard (0.35s
// tolerance) and be at least as new as its source asset.
func shotCacheUsable(ctx context.Context, ffmpegBin, outPath, srcPath string, wantDur float64) (bool, error) {
	outStat, err := os.Stat(outPath)
	if err != nil {
		return false, nil // 不存在 → 重做
	}
	srcStat, err := os.Stat(srcPath)
	if err != nil {
		return false, err
	}
	if srcStat.ModTime().After(outStat.ModTime()) {
		return false, nil // 素材更新过，重做
	}
	info, err := probeMedia(ctx, ffmpegBin, outPath)
	if err != nil {
		return false, err
	}
	if !info.Valid || !info.HasVideo || !info.HasAudio {
		return false, nil
	}
	if delta := info.DurationSec - wantDur; delta > 0.35 || delta < -0.35 {
		return false, nil
	}
	return true, nil
}

// composeOneShot normalizes a single shot into its own mp4.
func composeOneShot(ctx context.Context, ffmpegBin, wsDir, srcPath string, shot Shot, outPath string, W, H int, fps float64) error {
	dur := shot.DurationSec
	d := trimFloat(dur)
	videoChain := fmt.Sprintf(
		"[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,fps=%s,setsar=1,format=yuv420p,trim=duration=%s,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS",
		W, H, W, H, trimFloat(fps), d, d, d)
	if shot.Transition == "fade" {
		fd := 0.4
		if dur < 1.0 {
			fd = dur / 4
		}
		videoChain += fmt.Sprintf(",fade=t=in:st=0:d=%s,fade=t=out:st=%s:d=%s",
			trimFloat(fd), trimFloat(dur-fd), trimFloat(fd))
	}
	videoChain += "[v]"
	audioChain := fmt.Sprintf(
		"[1:a]aformat=sample_fmts=fltp:sample_rates=44100:channel_layouts=stereo,apad,atrim=duration=%s,asetpts=PTS-STARTPTS[a]", d)
	graph := videoChain + ";" + audioChain

	preArgs := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if isImagePath(srcPath) {
		preArgs = append(preArgs, "-loop", "1")
	}
	preArgs = append(preArgs, "-i", srcPath)
	audioRef := strings.TrimSpace(shot.TTS.Asset)
	if audioRef != "" {
		audioPath := resolveWSAsset(wsDir, audioRef)
		if st, err := os.Stat(audioPath); err != nil || st.IsDir() {
			return fmt.Errorf("配音文件不存在或不可读：%s（tts.asset）", audioRef)
		}
		preArgs = append(preArgs, "-i", audioPath)
	} else {
		preArgs = append(preArgs, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo")
	}
	postArgs := []string{
		"-map", "[v]", "-map", "[a]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2",
		"-movflags", "+faststart",
		"-t", d,
		outPath,
	}
	if _, err := runFFmpegFilter(ctx, ffmpegBin, preArgs, graph, postArgs, ""); err != nil {
		return fmt.Errorf("逐镜合成失败: %w", err)
	}
	return nil
}

// writeConcatList writes output/concat-list.txt in the concat demuxer format.
// Paths are written with forward slashes (the demuxer treats a backslash as an
// escape character on every platform).
func writeConcatList(wsDir string, shots []shotResult) (string, error) {
	var b strings.Builder
	for _, sh := range shots {
		p := resolveWSAsset(wsDir, sh.Output)
		b.WriteString("file '")
		b.WriteString(escapeConcatPath(filepath.ToSlash(p)))
		b.WriteString("'\n")
	}
	path := projectPath(wsDir, "output", "concat-list.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("写入 concat 清单失败: %w", err)
	}
	return path, nil
}

// escapeConcatPath escapes a path for the concat demuxer's quoted form
// (backslash is the escape character; a literal quote is written as '\”).
func escapeConcatPath(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, `'`, `'\''`)
	return p
}

// assembleFilm runs stage 2: concat demuxer input -> subtitles -> (optional
// AIGC label overlay) -> BGM mix -> final.mp4. labelPath is the label's own ass
// file (empty = 不烧录); it is chained after the dialogue subtitles so the two
// never share a file and the label stays on top.
func assembleFilm(ctx context.Context, ffmpegBin, wsDir, concatList, subPath string, hasSubs bool, labelPath string, bgmPath string, bgmVolume float64, W, H int, total float64, finalPath string) error {
	preArgs := []string{"-y", "-hide_banner", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", concatList}
	if bgmPath != "" {
		preArgs = append(preArgs, "-stream_loop", "-1", "-i", bgmPath)
	}

	chain := []string{}
	if hasSubs {
		chain = append(chain, fmt.Sprintf("subtitles='%s'", escapeFilterPath(subPath)))
	}
	if labelPath != "" {
		chain = append(chain, fmt.Sprintf("subtitles='%s'", escapeFilterPath(labelPath)))
	}
	videoChain := "[0:v]null[v]"
	if len(chain) > 0 {
		videoChain = "[0:v]" + strings.Join(chain, ",") + "[v]"
	}
	audioChain := "[0:a]anull[a]"
	if bgmPath != "" {
		vol := bgmVolume
		if vol <= 0 || vol > 1 {
			vol = 0.2
		}
		audioChain = fmt.Sprintf(
			"[1:a]aformat=sample_fmts=fltp:sample_rates=44100:channel_layouts=stereo,volume=%s[bgm];[0:a][bgm]amix=inputs=2:duration=first:dropout_transition=0:normalize=0[a]",
			trimFloat(vol))
	}
	graph := videoChain + ";" + audioChain

	postArgs := []string{
		"-map", "[v]", "-map", "[a]",
		"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2",
		"-movflags", "+faststart",
		"-t", trimFloat(total),
		finalPath,
	}
	// Keep the final filter graph on disk: it is the artifact you want when
	// debugging a bad assembly, and it documents exactly what was rendered.
	keep := projectPath(wsDir, "output", "compose.filter.txt")
	if _, err := runFFmpegFilter(ctx, ffmpegBin, preArgs, graph, postArgs, keep); err != nil {
		return fmt.Errorf("整片装配失败: %w", err)
	}
	return nil
}

// escapeFilterPath escapes a filesystem path for use inside a filtergraph
// value (the filtergraph parser consumes ':' and '\', so a Windows path must
// become C\:/dir/file.ass).
func escapeFilterPath(p string) string {
	p = filepath.ToSlash(p)
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, `:`, `\:`)
	p = strings.ReplaceAll(p, `'`, `\'`)
	return p
}

// writeSubtitlesASS derives the subtitle file from the storyboard itself (每镜
// subtitle 文本 + 时长 → 全局时轴), because the storyboard is the only source
// of subtitle text. Returns the path and whether any subtitle text exists.
func writeSubtitlesASS(wsDir string, sb *Storyboard, W, H int, font string) (string, bool, error) {
	type line struct {
		start, end float64
		text       string
	}
	var lines []line
	cursor := 0.0
	for _, sh := range sb.Shots {
		text := strings.TrimSpace(sh.Subtitle)
		if text != "" {
			lines = append(lines, line{start: cursor, end: cursor + sh.DurationSec, text: text})
		}
		cursor += sh.DurationSec
	}
	path := projectPath(wsDir, "output", "subtitles.ass")
	if len(lines) == 0 {
		return path, false, nil
	}
	if strings.TrimSpace(font) == "" {
		font = defaultSubtitleFont()
	}
	fontSize := int(float64(H) * 0.045)
	if fontSize < 16 {
		fontSize = 16
	}
	marginV := int(float64(H) * 0.05)
	if marginV < 12 {
		marginV = 12
	}

	var b strings.Builder
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	b.WriteString("WrapStyle: 2\n")
	fmt.Fprintf(&b, "PlayResX: %d\nPlayResY: %d\n\n", W, H)
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,2,1,2,40,40,%d,1\n\n",
		font, fontSize, marginV)
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
			assTime(l.start), assTime(l.end), escapeASSText(l.text))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return path, false, fmt.Errorf("写入字幕文件失败: %w", err)
	}
	return path, true, nil
}

// writeAIGCLabelASS renders the AIGC explicit label (GB 45438-2025 参考) as
// its own overlay .ass file — deliberately separate from the dialogue
// subtitles so neither can contaminate the other. One Dialogue line spans the
// whole film: 左上角、半透明、字号约 4% 画面高（与分辨率成正比），位置由
// \pos(...) 显式给出。text 为空时用缺省文案。
func writeAIGCLabelASS(wsDir string, W, H int, total float64, text, font string) (string, error) {
	path := projectPath(wsDir, "output", "aigc-label.ass")
	text = strings.TrimSpace(text)
	if text == "" {
		text = defaultAIGCLabelText
	}
	if strings.TrimSpace(font) == "" {
		font = defaultSubtitleFont()
	}
	if total <= 0 {
		total = 1.0 // 防御：时长为 0 时给最小可见时长（校验已保证各镜时长为正）
	}
	fontSize := int(float64(H) * 0.04)
	if fontSize < 16 {
		fontSize = 16
	}
	marginH := int(float64(W) * 0.02)
	if marginH < 12 {
		marginH = 12
	}
	marginV := int(float64(H) * 0.03)
	if marginV < 10 {
		marginV = 10
	}

	var b strings.Builder
	b.WriteString("[Script Info]\n")
	b.WriteString("; AIGC 显式标识角标（GB 45438-2025 参考）——整片常驻，独立于台词字幕\n")
	b.WriteString("ScriptType: v4.00+\n")
	b.WriteString("WrapStyle: 2\n")
	fmt.Fprintf(&b, "PlayResX: %d\nPlayResY: %d\n\n", W, H)
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	// Alignment=7（左上）作为默认锚点；实际位置由 Dialogue 里的 \pos 给出。
	// 主色/描边都取半透明（&H80 前缀 = 约 50% 透明），不遮挡画面主体。
	fmt.Fprintf(&b, "Style: AIGCLabel,%s,%d,&H80FFFFFF,&H000000FF,&H80000000,&H80000000,0,0,0,0,100,100,0,0,1,1,0,7,0,0,0,1\n\n",
		font, fontSize)
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	fmt.Fprintf(&b, "Dialogue: 0,%s,%s,AIGCLabel,,0,0,0,,{\\pos(%d,%d)\\alpha&H80&}%s\n",
		assTime(0), assTime(total), marginH, marginV, escapeASSText(text))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return path, fmt.Errorf("写入 AIGC 标识角标文件失败: %w", err)
	}
	return path, nil
}

// defaultSubtitleFont picks a CJK-capable system font per platform.
func defaultSubtitleFont() string {
	switch runtime.GOOS {
	case "windows":
		return "Microsoft YaHei"
	case "darwin":
		return "PingFang SC"
	default:
		return "Noto Sans CJK SC"
	}
}

// assTime formats seconds as H:MM:SS.cc (ASS centisecond timestamps).
func assTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	cs := int(sec*100 + 0.5)
	h := cs / 360000
	cs -= h * 360000
	m := cs / 6000
	cs -= m * 6000
	s := cs / 100
	cs -= s * 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

// escapeASSText escapes a subtitle line for the ASS dialogue text field.
func escapeASSText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "{", `\{`)
	s = strings.ReplaceAll(s, "}", `\}`)
	s = strings.ReplaceAll(s, "\r\n", `\N`)
	s = strings.ReplaceAll(s, "\n", `\N`)
	s = strings.ReplaceAll(s, "\r", `\N`)
	return s
}

// runTechnicalQC probes the finished film and writes qc/final-qc.json.
func runTechnicalQC(ctx context.Context, ffmpegBin, wsDir, finalPath string, expected float64, shots, reused, composed int, label aigcLabelState) (*composeQC, error) {
	info, err := probeMedia(ctx, ffmpegBin, finalPath)
	if err != nil {
		return nil, err
	}
	if !info.Valid {
		return nil, fmt.Errorf("无法从 final.mp4 解析出时长")
	}
	qc := &composeQC{
		CheckedAt:           time.Now().Format(time.RFC3339),
		Output:              relToWS(wsDir, finalPath),
		DurationSec:         info.DurationSec,
		ExpectedDurationSec: expected,
		HasVideo:            info.HasVideo,
		HasAudio:            info.HasAudio,
		VideoCodec:          info.VideoCodec,
		AudioCodec:          info.AudioCodec,
		Width:               info.Width,
		Height:              info.Height,
		AIGCLabel:           label.Required,
		AIGCLabelBurned:     label.Burned,
		AIGCLabelFile:       label.File,
		Shots:               shots,
		Reused:              reused,
		Composed:            composed,
	}
	if label.Required {
		qc.AIGCLabelText = label.Text
	}
	tolerance := expected * 0.02
	if tolerance < 1.0 {
		tolerance = 1.0
	}
	qc.DurationOk = info.DurationSec >= expected-tolerance && info.DurationSec <= expected+tolerance
	faststart, fsNote := detectFaststart(finalPath)
	qc.Faststart = faststart
	if fsNote != "" {
		qc.Notes = append(qc.Notes, fsNote)
	}
	qc.ChecksPassed = qc.DurationOk && qc.HasVideo && qc.HasAudio && qc.Faststart &&
		(!label.Required || label.Burned)
	if !qc.DurationOk {
		qc.Notes = append(qc.Notes, fmt.Sprintf("成片时长 %.2fs 与分镜合计 %.2fs 偏差超过容差 %.2fs", info.DurationSec, expected, tolerance))
	}
	if !qc.HasVideo || !qc.HasAudio {
		qc.Notes = append(qc.Notes, "成片缺少视频流或音频流")
	}
	if label.Required && !label.Burned {
		qc.Notes = append(qc.Notes, "分镜要求 AIGC 标识（storyboard.aigc_label=true）但标识角标未烧录，G5 标识位不合格，不得放行交付")
	}
	data, err := json.MarshalIndent(qc, "", "  ")
	if err != nil {
		return qc, fmt.Errorf("序列化 QC 记录失败: %w", err)
	}
	if err := os.WriteFile(projectPath(wsDir, "qc", "final-qc.json"), append(data, '\n'), 0o644); err != nil {
		return qc, fmt.Errorf("写入 QC 记录失败: %w", err)
	}
	return qc, nil
}

// detectFaststart checks the moov/mdat atom order in the file head (moov first
// = faststart applied). A missing mdat in the head window is fine (big file).
func detectFaststart(path string) (bool, string) {
	f, err := os.Open(path)
	if err != nil {
		return false, "无法读取成片检查 faststart"
	}
	defer f.Close()
	head := make([]byte, 1<<20)
	n, _ := f.Read(head)
	head = head[:n]
	moov := strings.Index(string(head), "moov")
	mdat := strings.Index(string(head), "mdat")
	if moov < 0 {
		return false, "未在文件头部找到 moov（faststart 未生效）"
	}
	if mdat >= 0 && mdat < moov {
		return false, "moov 位于 mdat 之后（faststart 未生效）"
	}
	return true, ""
}

// runFFmpegFilter runs ffmpeg with a filtergraph passed via
// -filter_complex_script (never inline: the final graph grows with the shot
// count and Windows caps a command line at 32k chars). keepPath non-empty
// persists the script next to the artifacts for debugging.
//
// Newer FFmpeg builds (the app-downloaded nightly, FFmpeg ≥ 8) removed
// -filter_complex_script; the official replacement is `-/filter_complex <file>`
// (file-valued option syntax). The legacy form is tried first for old binaries
// and the new syntax is used only when the binary rejects the legacy option —
// so the module composes on both.
func runFFmpegFilter(ctx context.Context, ffmpegBin string, preArgs []string, graph string, postArgs []string, keepPath string) (string, error) {
	scriptPath := keepPath
	cleanup := func() {}
	if scriptPath == "" {
		f, err := os.CreateTemp("", "niuniu-filter-*.txt")
		if err != nil {
			return "", fmt.Errorf("创建滤镜脚本失败: %w", err)
		}
		scriptPath = f.Name()
		if _, err := f.WriteString(graph); err != nil {
			f.Close()
			os.Remove(scriptPath)
			return "", fmt.Errorf("写入滤镜脚本失败: %w", err)
		}
		if err := f.Close(); err != nil {
			os.Remove(scriptPath)
			return "", fmt.Errorf("关闭滤镜脚本失败: %w", err)
		}
		cleanup = func() { os.Remove(scriptPath) }
	} else {
		if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
			return "", fmt.Errorf("创建滤镜脚本目录失败: %w", err)
		}
		if err := os.WriteFile(scriptPath, []byte(graph), 0o644); err != nil {
			return "", fmt.Errorf("写入滤镜脚本 %s 失败: %w", scriptPath, err)
		}
	}
	defer cleanup()

	args := append([]string{}, preArgs...)
	args = append(args, "-filter_complex_script", scriptPath)
	args = append(args, postArgs...)
	out, err := runFFmpeg(ctx, ffmpegBin, args)
	if err != nil && filterComplexScriptUnsupported(out) {
		args = append([]string{}, preArgs...)
		args = append(args, "-/filter_complex", scriptPath)
		args = append(args, postArgs...)
		return runFFmpeg(ctx, ffmpegBin, args)
	}
	return out, err
}

// filterComplexScriptUnsupported reports whether ffmpeg rejected the
// -filter_complex_script option itself (removed in newer builds) rather than
// failing on the graph contents — only then is the `-/filter_complex` retry
// attempted. A graph error never carries this exact wording.
func filterComplexScriptUnsupported(stderr string) bool {
	return strings.Contains(stderr, "Unrecognized option 'filter_complex_script'")
}

// parseFloatDefault is a small helper for tool parameters.
func parseFloatDefault(v any, def float64) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return def
}
