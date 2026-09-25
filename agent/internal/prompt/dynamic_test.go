package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 动态提示词：cwd/.niuniu-agent/PROMPT.md 注入 system 的独立段落，
// LoadDynamicPrompt 内部截断（MaxContextBytes + 尾注），缺失跳过。
func TestLoadDynamicPrompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".niuniu-agent", "PROMPT.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# 任务守则\n\n1. 先写测试\n2. 再写实现\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := LoadDynamicPrompt(dir)
	if !ok {
		t.Fatal("want ok")
	}
	if !strings.Contains(got, "先写测试") {
		t.Errorf("content = %q", got)
	}

	// 超限截断：专用上限 32KB——恰好 32KB 不截断，再多 1 字节即截断。
	exact := strings.Repeat("x", MaxDynamicPromptBytes)
	if err := os.WriteFile(p, []byte(exact), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadDynamicPrompt(dir); strings.Contains(got, "truncated") {
		t.Errorf("exactly %d bytes must not truncate (len %d)", MaxDynamicPromptBytes, len(got))
	}
	big := strings.Repeat("x", MaxDynamicPromptBytes+1)
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got2, _ := LoadDynamicPrompt(dir)
	if !strings.Contains(got2, "truncated at 32KB") {
		t.Errorf("cap note missing / wrong cap (len %d)", len(got2))
	}
	if !strings.Contains(got2, "truncated") || len(got2) > MaxDynamicPromptBytes+200 {
		t.Errorf("truncation did not bound content (len %d)", len(got2))
	}

	// 缺失跳过。
	if _, ok := LoadDynamicPrompt(filepath.Join(dir, "other")); ok {
		t.Error("missing dir must not be ok")
	}
}

func TestBuildEmbedsDynamicPrompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".niuniu-agent", "PROMPT.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("总是先用 Grep 定位再修改。"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Build(dir)
	if !strings.Contains(got, "总是先用 Grep 定位再修改。") {
		t.Errorf("Build missing dynamic prompt:\n%s", got)
	}
	// 位置在 Project context 之后（稳定顺序）。
	if strings.Index(got, "# Project context") > strings.Index(got, "总是先用") {
		t.Error("dynamic prompt must come after project context")
	}
	// 无 PROMPT.md 时不出现空段。
	if strings.Contains(Build(t.TempDir()), "# Task guidance") {
		t.Error("missing PROMPT.md must omit the section")
	}
}
