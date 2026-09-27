package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateStoryboardAcceptsValidDocument(t *testing.T) {
	raw := storyboardJSON("approved", []testShot{
		{ID: "1", Dur: 1.5, Type: "image", Asset: assetPath("img-01-1.png"), TTSAsset: assetPath("tts-1.mp3"), Subtitle: "第一镜"},
		{ID: "c-02", Dur: 2, Type: "video", Asset: assetPath("shot2.mp4")},
	})
	sb, err := ValidateStoryboard([]byte(raw))
	if err != nil {
		t.Fatalf("合法分镜被拒: %v", err)
	}
	if len(sb.Shots) != 2 {
		t.Fatalf("shots = %d, want 2", len(sb.Shots))
	}
	if string(sb.Shots[0].ID) != "1" || string(sb.Shots[1].ID) != "c-02" {
		t.Errorf("id 归一化失败: %q / %q", sb.Shots[0].ID, sb.Shots[1].ID)
	}
	if got := sb.ExpectedDuration(); got != 3.5 {
		t.Errorf("ExpectedDuration = %v, want 3.5", got)
	}
	w, h, err := sb.ResolutionWH()
	if err != nil || w != 640 || h != 360 {
		t.Errorf("ResolutionWH = %d,%d,%v", w, h, err)
	}
	if sb.ReviewStatus != "approved" || sb.FPS != 30 || sb.AspectRatio != "16:9" {
		t.Errorf("头部字段不符: %+v", sb)
	}
}

func TestValidateStoryboardReportsEveryProblemInChinese(t *testing.T) {
	raw := `{
	  "title": "",
	  "aspect_ratio": "wide",
	  "fps": "30",
	  "resolution": "640x360",
	  "review_status": "done",
	  "shots": [
	    {"id": null, "duration_sec": -1, "narration": 5, "subtitle": "",
	     "visual": {"type": "gif", "tier": "L4", "prompt": 1, "asset": ""},
	     "tts": {"voice": "alloy", "asset": ""},
	     "transition": "dissolve"}
	  ]
	}`
	_, err := ValidateStoryboard([]byte(raw))
	if err == nil {
		t.Fatal("非法分镜应被拒")
	}
	msg := err.Error()
	for _, want := range []string{
		"storyboard.title 不能为空",
		`storyboard.aspect_ratio 的值 "wide" 不是 W:H 格式`,
		"storyboard.fps 必须是数字（当前为字符串）",
		`storyboard.review_status 的值 "done" 不合法`,
		"shots[0].id 缺失",
		"shots[0].duration_sec 必须为正数",
		"shots[0].narration 必须是字符串",
		"shots[0].visual.type 的值 \"gif\" 不合法",
		"shots[0].visual.tier 的值 \"L4\" 不合法",
		"shots[0].visual.prompt 必须是字符串",
		"shots[0].visual.asset 不能为空",
		"shots[0].transition 的值 \"dissolve\" 不合法",
		"校验失败",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q\n--- 实际 ---\n%s", want, msg)
		}
	}
}

func TestValidateStoryboardStructuralErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"非 JSON", "{not json", "不是合法 JSON"},
		{"缺 shots", `{"title":"t","aspect_ratio":"16:9","fps":30,"resolution":"640x360","review_status":"draft"}`, "storyboard.shots 缺失"},
		{"shots 为空", `{"title":"t","aspect_ratio":"16:9","fps":30,"resolution":"640x360","review_status":"draft","shots":[]}`, "shots 不能为空"},
		{"shot 不是对象", `{"title":"t","aspect_ratio":"16:9","fps":30,"resolution":"640x360","review_status":"draft","shots":["x"]}`, "shots[0] 必须是对象"},
		{"缺 visual", `{"title":"t","aspect_ratio":"16:9","fps":30,"resolution":"640x360","review_status":"draft","shots":[{"id":1,"duration_sec":1,"narration":"","subtitle":"","tts":{"voice":"","asset":""},"transition":"cut"}]}`, "shots[0].visual 缺失"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateStoryboard([]byte(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want 含 %q", err, tc.want)
			}
		})
	}
}

func TestLoadStoryboardMissingFile(t *testing.T) {
	ws := testWS(t)
	_, path, err := LoadStoryboard(ws)
	if err == nil {
		t.Fatal("缺 storyboard.json 应报错")
	}
	if !strings.Contains(err.Error(), "未找到分镜文件") {
		t.Errorf("错误信息应说明未找到分镜文件，实得：%v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "video-project/storyboard.json") {
		t.Errorf("path = %q", path)
	}
	// The error must name the workspace-relative path so the agent can act.
	if !strings.Contains(err.Error(), "video-project/storyboard.json") {
		t.Errorf("错误信息应给出相对路径，实得：%v", err)
	}
}

func TestLoadStoryboardValidatesAndReadsAsset(t *testing.T) {
	ws := testWS(t)
	writeStoryboard(t, ws, storyboardJSON("draft", []testShot{{ID: "1", Dur: 1, Asset: "assets/a.png"}}))
	sb, raw, err := LoadStoryboard(ws)
	if err != nil {
		t.Fatalf("LoadStoryboard: %v", err)
	}
	if !strings.Contains(raw, "测试短片") {
		t.Error("raw 应返回分镜原文（评审留痕用）")
	}
	if sb.Shots[0].Visual.Asset != "assets/a.png" {
		t.Errorf("asset = %q", sb.Shots[0].Visual.Asset)
	}
}

func TestAssetForShotPrefersSelected(t *testing.T) {
	sh := Shot{Visual: Visual{Asset: "assets/first.png"}, Selected: " assets/picked.png "}
	if got := AssetForShot(sh); got != "assets/picked.png" {
		t.Errorf("AssetForShot = %q, want 选条优先", got)
	}
	sh.Selected = ""
	if got := AssetForShot(sh); got != "assets/first.png" {
		t.Errorf("AssetForShot = %q, want visual.asset", got)
	}
	if got := AssetForShot(Shot{Visual: Visual{}}); got != "" {
		t.Errorf("无素材时应为空串，实得 %q", got)
	}
}

func TestResolveWSAssetForms(t *testing.T) {
	ws := testWS(t)
	outside := t.TempDir() // 另一个目录 = 工作空间之外的绝对路径
	parentOfWS := filepath.Join(filepath.Dir(ws), "sibling-escape.png")
	cases := []struct {
		ref  string
		want string
	}{
		// 正样本：项目内相对/带前缀相对/工作空间内绝对路径。
		{"assets/a.png", filepath.Join(ws, "video-project", "assets", "a.png")},
		{"video-project/assets/a.png", filepath.Join(ws, "video-project", "assets", "a.png")},
		{filepath.Join(ws, "x.png"), filepath.Join(ws, "x.png")},
		// L1 素材可放在工作空间任意位置（不限于 video-project）。
		{filepath.Join(ws, "raw", "hero.mp4"), filepath.Join(ws, "raw", "hero.mp4")},
		{"raw/hero.mp4", filepath.Join(ws, "video-project", "raw", "hero.mp4")},
		// Clean 之后仍在工作空间内 → 合法（filepath.Join 会把 assets/.. 折叠掉）。
		{"video-project/assets/../b.png", filepath.Join(ws, "video-project", "b.png")},
		// Clean 后落到 video-project 自身（仍在工作空间内，未逃逸）：
		// 调用方 Stat 到目录会自报错。
		{"video-project/..", filepath.Join(ws, "video-project")},
		// 负样本：相对逃逸（正/反斜杠）、越界的绝对路径。
		{"../../x.png", ""},
		{`..\..\x.png`, ""},
		{"video-project/../../../x.png", ""},
		{parentOfWS, ""},
		{filepath.Join(outside, "x.png"), ""},
	}
	for _, tc := range cases {
		if got := resolveWSAsset(ws, tc.ref); got != tc.want {
			t.Errorf("resolveWSAsset(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
	if got := resolveWSAsset(ws, "  "); got != "" {
		t.Errorf("空引用应为空串，实得 %q", got)
	}
	if !withinDir(ws, ws) {
		t.Error("withinDir 应接受目录自身")
	}
	if withinDir(ws, filepath.Join(ws, "..", "x")) {
		t.Error("withinDir 应拒绝工作空间之外")
	}
	if _, err := os.Stat(projectPath(ws, "assets")); err == nil {
		// ensureDirs is only called by writers; resolveWSAsset must stay pure.
		t.Error("resolveWSAsset 不应创建目录")
	}
}

func TestResolveCheckedAssetReportsBoundary(t *testing.T) {
	ws := testWS(t)
	if p, err := resolveCheckedAsset(ws, "  ", "reference_image"); p != "" || err != nil {
		t.Errorf("空引用应返回空串且不报错: %q, %v", p, err)
	}
	if _, err := resolveCheckedAsset(ws, "../../secret.png", "reference_image"); err == nil {
		t.Error("越界引用应报错")
	} else if !strings.Contains(err.Error(), "路径越界") || !strings.Contains(err.Error(), "reference_image") {
		t.Errorf("错误信息应说明越界且点名参数，实得：%v", err)
	}
	if _, err := resolveCheckedAsset(ws, `..\..\secret.png`, "image"); err == nil {
		t.Error("反斜杠变体同样应报错")
	}
}

func TestTaskRecordPathStaysInsideShots(t *testing.T) {
	ws := testWS(t)
	shots := projectPath(ws, "shots")
	for _, group := range []string{"../../evil", `..\..\evil`, "vg-20260927-153045-1a2b", "", "a/b/c"} {
		if p := taskRecordPath(ws, group); !withinDir(shots, p) {
			t.Errorf("taskRecordPath(%q) 越出 shots/：%s", group, p)
		}
	}
}

func TestSanitizeShotIDAndNextAssetIndex(t *testing.T) {
	if got := sanitizeShotID("12"); got != "12" {
		t.Errorf("sanitizeShotID(12) = %q", got)
	}
	if got := sanitizeShotID("c 01/hero"); got != "c-01-hero" {
		t.Errorf("sanitizeShotID = %q, want c-01-hero", got)
	}
	if got := sanitizeShotID("///"); got != "shot" {
		t.Errorf("sanitizeShotID = %q, want shot", got)
	}

	dir := t.TempDir()
	if got := nextAssetIndex(dir, "tts-"); got != 1 {
		t.Errorf("空目录 = %d, want 1", got)
	}
	for _, name := range []string{"tts-1.mp3", "tts-3.mp3", "img-9-1.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := nextAssetIndex(dir, "tts-"); got != 4 {
		t.Errorf("tts 序号 = %d, want 4", got)
	}
	if got := nextAssetIndex(dir, "img-"); got != 10 {
		t.Errorf("img 序号 = %d, want 10", got)
	}
}
