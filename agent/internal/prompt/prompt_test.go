package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildStableSections(t *testing.T) {
	dir := t.TempDir()
	sys := Build(dir)

	// 稳定前缀结构：身份 → 环境 → 工具指引 → 规则 →（可选）项目上下文。
	for _, want := range []string{
		"You are niuniu-agent",
		"# Environment",
		"# Tools",
		"# Rules",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing section %q:\n%.400s", want, sys)
		}
	}
	// 段落顺序固定（身份最先、规则在工具指引之后），保证跨轮稳定。
	if i, j := strings.Index(sys, "# Environment"), strings.Index(sys, "# Rules"); i > j {
		t.Errorf("Environment must precede Rules:\n%.400s", sys)
	}
	// 环境段带 cwd 与平台。
	if !strings.Contains(sys, dir) {
		t.Errorf("system prompt missing cwd %q:\n%.400s", dir, sys)
	}
	if !strings.Contains(sys, "Platform: "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("system prompt missing platform:\n%.400s", sys)
	}
}

func TestBuildIncludesProjectContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("PROJECT-MARKER-123"), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := Build(dir)
	if !strings.Contains(sys, "PROJECT-MARKER-123") {
		t.Errorf("AGENTS.md content not injected:\n%.400s", sys)
	}
	if !strings.Contains(sys, "# Project context") {
		t.Errorf("project context section missing:\n%.400s", sys)
	}
}

func TestLoadProjectContextPrefersAGENTSOverClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents-md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude-md"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadProjectContext(dir)
	if !ok || got != "agents-md" {
		t.Errorf("LoadProjectContext = %q,%v; want agents-md,true", got, ok)
	}
}

func TestLoadProjectContextFallsBackToClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude-md"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadProjectContext(dir)
	if !ok || got != "claude-md" {
		t.Errorf("LoadProjectContext = %q,%v; want claude-md,true", got, ok)
	}
}

func TestLoadProjectContextAbsent(t *testing.T) {
	got, ok := LoadProjectContext(t.TempDir())
	if ok || got != "" {
		t.Errorf("LoadProjectContext = %q,%v; want empty,false", got, ok)
	}
}

func TestLoadProjectContextTruncatedAtCap(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", MaxContextBytes+1000)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadProjectContext(dir)
	if !ok {
		t.Fatal("want ok")
	}
	if len(got) > MaxContextBytes+200 { // cap + 截断提示
		t.Errorf("context len = %d, want capped near %d", len(got), MaxContextBytes)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation not noted:\n%.200s", got)
	}
}

func TestBuildInjectsSkillsIndex(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".niuniu-agent", "skills", "deploy")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: Verify release readiness before shipping.\n---\n\nRun the gates.\n"),
		0o644)

	got := Build(dir)
	if !strings.Contains(got, "# Skills") ||
		!strings.Contains(got, "- deploy: Verify release readiness before shipping.") {
		t.Errorf("Build output missing skills index:\n%s", got)
	}
	// 正文不应内联进 system（按需经 Skill 工具加载）。
	if strings.Contains(got, "Run the gates.") {
		t.Errorf("skill body must not be inlined into system prompt:\n%s", got)
	}
	// 稳定前缀：无 skills 时不得出现空 Skills 段。
	if strings.Contains(Build(t.TempDir()), "# Skills") {
		t.Errorf("empty skills list should omit the section")
	}
}
