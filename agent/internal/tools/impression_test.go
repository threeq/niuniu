package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// round-trip：写入的内容经 Load 原样读回。
func TestImpressionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects", "x", "impression.md")
	body := "技术栈: Go 多模块\n关键决策: 状态落盘用户目录\n用户脾气: 讨厌冗长汇报\n当前阶段: P4 记忆升级"
	if err := WriteImpression(path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := LoadImpression(path)
	if !ok {
		t.Fatal("load: not ok")
	}
	if got != body {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, body)
	}
}

// 硬约束：超过 200 字（rune）的内容在写入侧被截断到恰好 200 rune。
func TestImpressionHardCapOnWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "impression.md")
	big := strings.Repeat("印", MaxImpressionRunes+80) + strings.Repeat("x", 50)
	if err := WriteImpression(path, big); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := LoadImpression(path)
	if !ok {
		t.Fatal("load: not ok")
	}
	if n := len([]rune(got)); n != MaxImpressionRunes {
		t.Errorf("impression = %d runes, want exactly %d", n, MaxImpressionRunes)
	}
}

// 硬约束在读取侧同样强制：手改的超大文件不会撑爆 system prompt。
func TestImpressionHardCapOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "impression.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", MaxImpressionRunes*3)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadImpression(path)
	if !ok {
		t.Fatal("load: not ok")
	}
	if n := len([]rune(got)); n != MaxImpressionRunes {
		t.Errorf("impression = %d runes, want capped at %d", n, MaxImpressionRunes)
	}
}

// 空内容是 no-op：不建文件，也不清掉已有印象。
func TestImpressionEmptyWriteIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "impression.md")
	if err := WriteImpression(path, "   \n  "); err != nil {
		t.Fatalf("empty write: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("empty write must not create the file")
	}
	if err := WriteImpression(path, "旧印象"); err != nil {
		t.Fatal(err)
	}
	if err := WriteImpression(path, ""); err != nil {
		t.Fatalf("second empty write: %v", err)
	}
	got, ok := LoadImpression(path)
	if !ok || got != "旧印象" {
		t.Errorf("empty write wiped an existing impression: %q,%v", got, ok)
	}
}

// 容错：文件缺失 → 无值、无错误。
func TestLoadImpressionMissing(t *testing.T) {
	got, ok := LoadImpression(filepath.Join(t.TempDir(), "nope", "impression.md"))
	if ok || got != "" {
		t.Errorf("missing file = %q,%v; want empty,false", got, ok)
	}
}

// 容错：损坏文件（非法 UTF-8 / 纯空白）→ 跳过注入，不报错不 panic。
func TestLoadImpressionCorrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "impression.md")
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 0x00, 0x9a}, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadImpression(path); ok || got != "" {
		t.Errorf("corrupted file = %q,%v; want empty,false", got, ok)
	}
	if err := os.WriteFile(path, []byte("\n \t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadImpression(path); ok || got != "" {
		t.Errorf("blank file = %q,%v; want empty,false", got, ok)
	}
}
