package main

// Real-ffmpeg tests. They build their fixtures with lavfi (a red still, a 2s
// motion clip, sine "voice-over"/"BGM") and skip on any machine where ffmpeg or
// the subtitles filter (libass) is unavailable, so the suite passes everywhere.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// composeFixtureWS builds a two-shot workspace:
//
//	assets/shot1.png  红色静帧（1s，配 vo1.wav 人声、字幕"第一镜"）
//	assets/shot2.mp4  2s 动态片段（自带音轨）
//	assets/bgm.wav    4s 正弦 BGM
//	storyboard.json   review_status=approved，合计 3.0s
func composeFixtureWS(t *testing.T, bin string) string {
	t.Helper()
	ws := testWS(t)
	if err := ensureDirs(ws); err != nil {
		t.Fatal(err)
	}
	assets := projectPath(ws, "assets")
	ffmpegRun(t, bin, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=red:s=320x180:d=1", "-frames:v", "1",
		filepath.Join(assets, "shot1.png"))
	ffmpegRun(t, bin, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=30:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest",
		filepath.Join(assets, "shot2.mp4"))
	ffmpegRun(t, bin, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=300:duration=1",
		filepath.Join(assets, "vo1.wav"))
	ffmpegRun(t, bin, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=220:duration=4",
		filepath.Join(assets, "bgm.wav"))

	writeStoryboard(t, ws, storyboardJSON("approved", []testShot{
		{ID: "1", Dur: 1.0, Type: "image", Asset: assetPath("shot1.png"),
			TTSAsset: assetPath("vo1.wav"), Subtitle: "第一镜：开场"},
		{ID: "2", Dur: 2.0, Type: "video", Asset: assetPath("shot2.mp4")},
	}))
	return ws
}

func composeDeps(bin string) Deps {
	return Deps{ResolveFFmpeg: func() (string, error) { return bin, nil }}
}

func TestComposeFilmFullPipeline(t *testing.T) {
	bin := resolveTestFFmpeg(t)
	if !ffmpegHasFilter(t, bin, "subtitles") {
		t.Skip("跳过（本机 ffmpeg 未编入 libass/subtitles 滤镜）")
	}
	ws := composeFixtureWS(t, bin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	report, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{
		BGM: assetPath("bgm.wav"), BGMVolume: 0.2,
	})
	if err != nil {
		t.Fatalf("composeFilm: %v（report=%+v）", err, report)
	}
	if report.Final != "video-project/output/final.mp4" {
		t.Errorf("final = %q", report.Final)
	}
	if report.Composed != 2 || report.Reused != 0 {
		t.Errorf("composed/reused = %d/%d, want 2/0", report.Composed, report.Reused)
	}
	for _, sh := range report.Shots {
		if sh.Error != "" {
			t.Errorf("镜 %s 报错: %s", sh.ID, sh.Error)
		}
	}

	// 产物齐备：逐镜 mp4、成片、字幕、滤镜脚本、QC 记录。
	for _, rel := range []string{
		"video-project/shots/1.mp4",
		"video-project/shots/2.mp4",
		"video-project/output/final.mp4",
		"video-project/output/subtitles.ass",
		"video-project/output/compose.filter.txt",
		"video-project/output/concat-list.txt",
		"video-project/qc/final-qc.json",
	} {
		if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("缺少产物 %s: %v", rel, err)
		}
	}

	// 字幕：本片两镜的台词都进了 ass（镜 2 无字幕文本，不应出现空行）。
	ass, err := os.ReadFile(projectPath(ws, "output", "subtitles.ass"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ass), "第一镜：开场") {
		t.Errorf("ass 缺少字幕文本:\n%s", ass)
	}
	if n := strings.Count(string(ass), "Dialogue: 0,"); n != 1 {
		t.Errorf("Dialogue 行数 = %d, want 1（仅镜 1 有字幕）", n)
	}
	if !strings.Contains(string(ass), "PlayResX: 640") {
		t.Errorf("ass 未使用 storyboard 分辨率:\n%s", ass)
	}

	// 滤镜脚本：必须经 -filter_complex_script 落盘，且含字幕与 BGM 混音。
	script, err := os.ReadFile(projectPath(ws, "output", "compose.filter.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "subtitles=") || !strings.Contains(string(script), "amix=inputs=2") {
		t.Errorf("滤镜脚本内容不符:\n%s", script)
	}

	// G5 技术 QC。
	if report.QC == nil {
		t.Fatalf("缺少 QC 记录（warnings=%v）", report.Warnings)
	}
	qc := report.QC
	if !qc.ChecksPassed {
		t.Errorf("QC 未全过: %+v", qc)
	}
	if !qc.DurationOk || qc.DurationSec < 2.0 || qc.DurationSec > 4.0 {
		t.Errorf("时长 QC 不符: %+v（期望≈3.0s）", qc)
	}
	if !qc.Faststart {
		t.Error("faststart 未生效")
	}
	if qc.Width != 640 || qc.Height != 360 {
		t.Errorf("分辨率 = %dx%d, want 640x360", qc.Width, qc.Height)
	}
	if !strings.Contains(qc.VideoCodec, "264") || !strings.Contains(qc.AudioCodec, "aac") {
		t.Errorf("编码 = %q/%q, want h264/aac", qc.VideoCodec, qc.AudioCodec)
	}
	if qc.Shots != 2 {
		t.Errorf("QC.Shots = %d", qc.Shots)
	}
	// 本分镜未要求 AIGC 标识（缺省 false）：不生成角标、QC 仍显式记录 false。
	if qc.AIGCLabel || qc.AIGCLabelBurned || qc.AIGCLabelFile != "" {
		t.Errorf("未要求 AIGC 标识时 QC 标识位应为 false: %+v", qc)
	}
	if _, err := os.Stat(projectPath(ws, "output", "aigc-label.ass")); err == nil {
		t.Error("未要求 AIGC 标识时不得生成角标 ass")
	}
	var qcOnDisk composeQC
	raw, err := os.ReadFile(projectPath(ws, "qc", "final-qc.json"))
	if err != nil || json.Unmarshal(raw, &qcOnDisk) != nil {
		t.Fatalf("读取 qc/final-qc.json: %v", err)
	}
	if !qcOnDisk.ChecksPassed {
		t.Errorf("落盘 QC 与返回不一致: %+v", qcOnDisk)
	}

	// 第二遍：合格 shots 复用，只做装配（不重做镜头）。
	again, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{
		BGM: assetPath("bgm.wav"), BGMVolume: 0.2,
	})
	if err != nil {
		t.Fatalf("第二遍 composeFilm: %v", err)
	}
	if again.Reused != 2 || again.Composed != 0 {
		t.Errorf("第二遍 reused/composed = %d/%d, want 2/0（缓存应复用）", again.Reused, again.Composed)
	}
	for _, sh := range again.Shots {
		if !sh.Reused {
			t.Errorf("镜 %s 未被复用", sh.ID)
		}
	}

	// 输出/BGM 路径越界必须被拒（shots 已缓存，这两次调用几乎不花时间）。
	if _, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{OutputRel: "../../escape.mp4"}); err == nil || !strings.Contains(err.Error(), "路径越界") {
		t.Errorf("越界的 output 应被拒绝，实得: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ws), "escape.mp4")); err == nil {
		t.Error("越界的 output 不得写出工作空间外的文件")
	}
	if _, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{BGM: `..\..\evil.wav`}); err == nil || !strings.Contains(err.Error(), "路径越界") {
		t.Errorf("越界的 BGM 应被拒绝，实得: %v", err)
	}
}

// TestComposeFilmBurnsAIGCLabel: storyboard.aigc_label=true → 拼接后烧录
// 独立角标 ass（缺省文字），QC 记录 aigc_label/aigc_label_burned，且台词字幕
// 文件不被污染（两者是各自独立的 ass）。
func TestComposeFilmBurnsAIGCLabel(t *testing.T) {
	bin := resolveTestFFmpeg(t)
	if !ffmpegHasFilter(t, bin, "subtitles") {
		t.Skip("跳过（本机 ffmpeg 未编入 libass/subtitles 滤镜）")
	}
	ws := composeFixtureWS(t, bin)
	writeStoryboard(t, ws, withAIGCLabel(storyboardJSON("approved", []testShot{
		{ID: "1", Dur: 1.0, Type: "image", Asset: assetPath("shot1.png"),
			TTSAsset: assetPath("vo1.wav"), Subtitle: "第一镜：开场"},
		{ID: "2", Dur: 2.0, Type: "video", Asset: assetPath("shot2.mp4")},
	}), `"aigc_label":true`))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{})
	if err != nil {
		t.Fatalf("composeFilm: %v（report=%+v）", err, report)
	}
	if report.AIGCLabelFile != "video-project/output/aigc-label.ass" {
		t.Errorf("aigc_label_file = %q", report.AIGCLabelFile)
	}
	// 角标 ass 独立生成，含缺省标识文字与定位/透明度控制。
	labelASS, err := os.ReadFile(projectPath(ws, "output", "aigc-label.ass"))
	if err != nil {
		t.Fatalf("角标 ass 未生成: %v", err)
	}
	for _, want := range []string{"AI 生成", `\pos(`, `\alpha`} {
		if !strings.Contains(string(labelASS), want) {
			t.Errorf("角标 ass 缺少 %q:\n%s", want, labelASS)
		}
	}
	// 台词字幕文件保持干净：AIGC 标识不得混入。
	subs, err := os.ReadFile(projectPath(ws, "output", "subtitles.ass"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(subs), "AI 生成") {
		t.Errorf("台词字幕被 AIGC 标识污染:\n%s", subs)
	}
	// 总装滤镜：台词 + 角标两段 subtitles 串联，且只出现在整片装配这一步。
	script, err := os.ReadFile(projectPath(ws, "output", "compose.filter.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(script), "subtitles="); n != 2 {
		t.Errorf("滤镜脚本应有 2 段 subtitles=（台词+角标），实得 %d:\n%s", n, script)
	}
	if !strings.Contains(string(script), "aigc-label.ass") {
		t.Errorf("滤镜脚本未引用角标 ass:\n%s", script)
	}
	// 真烧上了：抽首帧查像素。镜 1 是纯红静帧，标识（半透明白字）落在左上角
	// \pos 处——红底 G=B=0，出现任何亮像素即标识已渲染（防 ass 语法错被 libass 静默忽略）。
	frame := filepath.Join(ws, "video-project", "output", "frame0.rgb")
	ffmpegRun(t, bin, "-y", "-hide_banner", "-loglevel", "error",
		"-i", projectPath(ws, "output", "final.mp4"), "-frames:v", "1",
		"-pix_fmt", "rgb24", "-f", "rawvideo", frame)
	pix, err := os.ReadFile(frame)
	if err != nil {
		t.Fatal(err)
	}
	lit := 0
	for y := 0; y < 60; y++ {
		for x := 0; x < 240; x++ {
			i := (y*640 + x) * 3
			if i+2 >= len(pix) {
				break
			}
			if pix[i] > 90 || pix[i+1] > 90 || pix[i+2] > 90 {
				lit++
			}
		}
	}
	if lit == 0 {
		t.Error("成片左上角无标识像素：AIGC 角标未真正烧录")
	}
	// G5 技术 QC：标识位。
	if report.QC == nil {
		t.Fatalf("缺少 QC 记录（warnings=%v）", report.Warnings)
	}
	if !report.QC.AIGCLabel || !report.QC.AIGCLabelBurned {
		t.Errorf("QC 标识位不符: %+v", report.QC)
	}
	if report.QC.AIGCLabelText != "AI 生成" {
		t.Errorf("QC.AIGCLabelText = %q, want 缺省文字", report.QC.AIGCLabelText)
	}
	if !report.QC.ChecksPassed {
		t.Errorf("QC 未全过: %+v", report.QC)
	}
	var onDisk composeQC
	raw, err := os.ReadFile(projectPath(ws, "qc", "final-qc.json"))
	if err != nil || json.Unmarshal(raw, &onDisk) != nil {
		t.Fatalf("读取 qc/final-qc.json: %v", err)
	}
	if !onDisk.AIGCLabel || !onDisk.AIGCLabelBurned || onDisk.AIGCLabelFile == "" {
		t.Errorf("落盘 QC 标识位不符: %+v", onDisk)
	}
}

// TestComposeFilmAIGCLabelBurnFailureWarns: 分镜要求标识但烧录失败（这里让
// output/aigc-label.ass 是个目录，写文件必失败）→ 成片照常产出但必须给出
// 明确 warning、QC 记 burned=false 且 checks_passed=false，绝不静默。
func TestComposeFilmAIGCLabelBurnFailureWarns(t *testing.T) {
	bin := resolveTestFFmpeg(t)
	if !ffmpegHasFilter(t, bin, "subtitles") {
		t.Skip("跳过（本机 ffmpeg 未编入 libass/subtitles 滤镜）")
	}
	ws := composeFixtureWS(t, bin)
	writeStoryboard(t, ws, withAIGCLabel(storyboardJSON("approved", []testShot{
		{ID: "1", Dur: 1.0, Type: "image", Asset: assetPath("shot1.png"),
			TTSAsset: assetPath("vo1.wav"), Subtitle: "第一镜：开场"},
		{ID: "2", Dur: 2.0, Type: "video", Asset: assetPath("shot2.mp4")},
	}), `"aigc_label":true`))
	// 角标路径被目录占位 → 写角标 ass 失败。
	if err := os.MkdirAll(projectPath(ws, "output", "aigc-label.ass"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{})
	if err != nil {
		t.Fatalf("标识烧录失败不应阻塞成片: %v", err)
	}
	if _, err := os.Stat(projectPath(ws, "output", "final.mp4")); err != nil {
		t.Errorf("成片应照常产出: %v", err)
	}
	warned := false
	for _, w := range report.Warnings {
		if strings.Contains(w, "AIGC 标识烧录被跳过") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("缺少标识烧录失败的 warning: %v", report.Warnings)
	}
	if report.QC == nil {
		t.Fatal("缺少 QC 记录")
	}
	if !report.QC.AIGCLabel || report.QC.AIGCLabelBurned {
		t.Errorf("QC 应记录「要求了但未烧录」: %+v", report.QC)
	}
	if report.QC.ChecksPassed {
		t.Error("标识未烧录时 QC 不得判合格")
	}
	if !strings.Contains(strings.Join(report.QC.Notes, "\n"), "G5 标识位不合格") {
		t.Errorf("QC notes 应说明标识位不合格: %v", report.QC.Notes)
	}
}

// TestWriteAIGCLabelASS: 角标 ass 的纯生成逻辑（无需 ffmpeg）——自定义文字、
// 分辨率相关字号/边距、\pos 左上角定位、透明度；空文字/非法时长回落缺省。
func TestWriteAIGCLabelASS(t *testing.T) {
	ws := testWS(t)
	if err := ensureDirs(ws); err != nil {
		t.Fatal(err)
	}
	path, err := writeAIGCLabelASS(ws, 1280, 720, 6.5, "本片含 AI 生成内容", "Test Font")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "output/aigc-label.ass") {
		t.Errorf("path = %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		"PlayResX: 1280", "PlayResY: 720",
		"Style: AIGCLabel,Test Font,28,", // 字号 = 720×0.04 = 28
		`{\pos(25,21)\alpha&H80&}`,       // 边距 = 1280×0.02 / 720×0.03，左上角
		"本片含 AI 生成内容",
		"0:00:00.00,0:00:06.50", // 整片时轴常驻
	} {
		if !strings.Contains(s, want) {
			t.Errorf("角标 ass 缺少 %q\n--- 实际 ---\n%s", want, s)
		}
	}

	// 空文字回落缺省文案；非法时长（≤0）回落 1s；空字体回落平台默认 CJK 字体；
	// 小画面字号不低于 16。
	path2, err := writeAIGCLabelASS(ws, 320, 180, 0, "  ", "")
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	s2 := string(raw2)
	if !strings.Contains(s2, defaultAIGCLabelText) {
		t.Errorf("空文字应回落缺省 %q:\n%s", defaultAIGCLabelText, s2)
	}
	if !strings.Contains(s2, "0:00:01.00") {
		t.Errorf("非法时长应回落 1s:\n%s", s2)
	}
	if !strings.Contains(s2, "Style: AIGCLabel,"+defaultSubtitleFont()+",16,") {
		t.Errorf("空字体/小画面回落不符（默认字体 + 最小字号 16）:\n%s", s2)
	}
}

func TestComposeFilmRedoesOnlyTheBadShot(t *testing.T) {
	bin := resolveTestFFmpeg(t)
	if !ffmpegHasFilter(t, bin, "subtitles") {
		t.Skip("跳过（本机 ffmpeg 未编入 libass/subtitles 滤镜）")
	}
	ws := composeFixtureWS(t, bin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 先正常产出一遍，再把镜 2 的素材换掉（mtime 更新 → 缓存失效）。
	if _, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{}); err != nil {
		t.Fatalf("首遍: %v", err)
	}
	src2 := filepath.Join(ws, "video-project", "assets", "shot2.mp4")
	if err := os.Chtimes(src2, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	again, err := composeFilm(ctx, ws, composeDeps(bin), composeOptions{})
	if err != nil {
		t.Fatalf("第二遍: %v", err)
	}
	if again.Reused != 1 || again.Composed != 1 {
		t.Errorf("reused/composed = %d/%d, want 1/1（只重做素材更新的镜）", again.Reused, again.Composed)
	}
}

func TestComposeFilmReportsMissingAssetPerShot(t *testing.T) {
	bin := resolveTestFFmpeg(t)
	ws := testWS(t)
	writeStoryboard(t, ws, storyboardJSON("approved", []testShot{
		{ID: "1", Dur: 1.0, Asset: assetPath("nope.png")},
	}))
	report, err := composeFilm(context.Background(), ws, composeDeps(bin), composeOptions{})
	if err == nil {
		t.Fatal("素材缺失应报错且不产出成片")
	}
	if !strings.Contains(err.Error(), "未产出 final.mp4") {
		t.Errorf("err = %v", err)
	}
	if report == nil || len(report.Shots) != 1 || !strings.Contains(report.Shots[0].Error, "素材文件不存在") {
		t.Fatalf("缺少逐镜诊断: %+v", report)
	}
	if _, statErr := os.Stat(projectPath(ws, "output", "final.mp4")); statErr == nil {
		t.Error("有失败镜时不得产出 final.mp4")
	}
}

// TestTTSGenerateProbesDurationWithFFmpeg covers the "duration metadata via
// ffmpeg probe" half of tts_generate with a real binary (the degrade half is
// covered by tools_test.go).
func TestTTSGenerateProbesDurationWithFFmpeg(t *testing.T) {
	bin := resolveTestFFmpeg(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "voice.wav")
	ffmpegRun(t, bin, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=300:duration=1.5", wav)
	audio, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(audio)
	}))
	defer srv.Close()

	ws := testWS(t)
	reg := &Registry{Problems: map[string]string{}, TTS: &TTSBinding{
		Name: BackendOpenAICompat, Model: "tts-1", DefaultVoice: "nova",
		Backend: &OpenAICompatTTS{baseURL: srv.URL, model: "tts-1", client: srv.Client(), timeout: 5 * time.Second},
		Price:   PriceMeta{Unit: "元/千字符", UnitPrice: 0.02, Per: 1000, Configured: true},
	}}
	app := testApp(ws, reg, composeDeps(bin))
	res, err := app.handleTTSGenerate(context.Background(), callArgs(map[string]any{"text": "旁白一"}))

	// 后端返回的是 wav 字节但工具按约定写 .mp3：ffmpeg 仍能按容器探测出时长
	// （这正是「时长只在能探测时给出」的语义）。
	if err != nil || res.IsError {
		t.Fatalf("tts_generate: %v / %s", err, resultText(t, res))
	}
	out := decodeResult(t, resultText(t, res))
	dur, ok := out["duration_sec"].(float64)
	if !ok {
		t.Fatalf("有 ffmpeg 时应给出 duration_sec: %v", out)
	}
	if dur < 1.2 || dur > 1.8 {
		t.Errorf("duration_sec = %v, want ≈1.5", dur)
	}
}
