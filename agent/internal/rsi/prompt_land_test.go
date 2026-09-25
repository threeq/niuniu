package rsi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 接地门槛：Verified >= 2 才写入 PROMPT.md；低于门槛丢弃；无接地教训时
// 文件保持原样（不创建、不清空）。
func TestLandStrategyLessonsGroundingThreshold(t *testing.T) {
	dir := t.TempDir()
	landed, err := LandStrategyLessons(dir, []StrategyLesson{
		{Title: "grounded", Guidance: "改配置前先备份", Verified: 2},
		{Title: "ungrounded", Guidance: "凭感觉改", Verified: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed != 1 {
		t.Fatalf("landed = %d, want 1", landed)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".niuniu-agent", "PROMPT.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "改配置前先备份") {
		t.Errorf("grounded lesson missing:\n%s", s)
	}
	if strings.Contains(s, "凭感觉改") {
		t.Errorf("ungrounded lesson must not land:\n%s", s)
	}

	// 无接地教训 → 文件不被触碰。
	before, err := os.ReadFile(filepath.Join(dir, ".niuniu-agent", "PROMPT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := LandStrategyLessons(dir, []StrategyLesson{{Title: "weak", Guidance: "x", Verified: 1}}); err != nil || n != 0 {
		t.Fatalf("no-ground landing = (%d, %v)", n, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".niuniu-agent", "PROMPT.md"))
	if string(before) != string(after) {
		t.Error("no-ground landing must leave PROMPT.md untouched")
	}

	// 空指引不落地。
	if n, _ := LandStrategyLessons(dir, []StrategyLesson{{Title: "empty", Guidance: "", Verified: 5}}); n != 0 {
		t.Error("empty guidance must not land")
	}
}

// 托管段幂等替换：用户内容逐字节保留；重落地不累积、不重复。
func TestLandStrategyLessonsIdempotentReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".niuniu-agent", "PROMPT.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "# 我的项目守则\n\n1. 先读后写\n"
	if err := os.WriteFile(p, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LandStrategyLessons(dir, []StrategyLesson{
		{Title: "old", Guidance: "旧教训", Verified: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := LandStrategyLessons(dir, []StrategyLesson{
		{Title: "old", Guidance: "旧教训", Verified: 2},
		{Title: "new", Guidance: "新教训", Verified: 3},
	}); err != nil || n != 2 {
		t.Fatalf("re-landing = (%d, %v), want (2, nil)", n, err)
	}

	data, _ := os.ReadFile(p)
	s := string(data)
	if !strings.Contains(s, "先读后写") {
		t.Errorf("user content lost:\n%s", s)
	}
	if strings.Count(s, strategyStart) != 1 || strings.Count(s, strategyEnd) != 1 {
		t.Errorf("section markers duplicated (%d/%d):\n%s",
			strings.Count(s, strategyStart), strings.Count(s, strategyEnd), s)
	}
	if strings.Count(s, "旧教训") != 1 || !strings.Contains(s, "新教训") {
		t.Errorf("replace semantics broken:\n%s", s)
	}
	// 用户内容仍在托管段之前。
	if strings.Index(s, "先读后写") > strings.Index(s, strategyStart) {
		t.Errorf("user content must stay before the managed section:\n%s", s)
	}

	// 用户内容在托管段之后的情况：段替换后仍在。
	if err := os.WriteFile(p, []byte(strategyStart+"\n# Self-evolved strategy (grounded)\n\n- **a**: b\n"+strategyEnd+"\n\n尾部用户内容\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LandStrategyLessons(dir, []StrategyLesson{{Title: "c", Guidance: "d", Verified: 2}}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "尾部用户内容") || !strings.Contains(string(data), "**c**: d") {
		t.Errorf("content after section lost:\n%s", data)
	}
}

// 损坏托管段（缺 end 标记）不产生重复段。
func TestLandStrategyLessonsMalformedSection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".niuniu-agent", "PROMPT.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("用户内容\n"+strategyStart+"\n残段"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LandStrategyLessons(dir, []StrategyLesson{{Title: "x", Guidance: "y", Verified: 2}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	if strings.Count(s, strategyStart) != 1 {
		t.Errorf("malformed section caused duplicates:\n%s", s)
	}
	if !strings.Contains(s, "用户内容") {
		t.Errorf("user content lost:\n%s", s)
	}
}
