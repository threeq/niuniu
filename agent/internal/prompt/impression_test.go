package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// 启动注入：impression.md 存在时 BuildSession* 在尾部可变区注入印象段，
// 位置在稳定前缀之后、Memory 召回段之前。
func TestBuildSessionInjectsImpression(t *testing.T) {
	dir := t.TempDir()
	body := "技术栈: Go\n关键决策: 决策A\n用户脾气: 直接\n当前阶段: P4"
	if err := tools.WriteImpression(tools.ImpressionPath(dir), body); err != nil {
		t.Fatalf("write impression: %v", err)
	}
	sys := BuildSessionCapped(dir, 2, 1024)
	if !strings.Contains(sys, "# Project impression") {
		t.Errorf("system missing impression section:\n%.600s", sys)
	}
	if !strings.Contains(sys, body) {
		t.Errorf("system missing impression body:\n%.600s", sys)
	}
	// 前缀末尾可变区：印象段在稳定段之后。
	if i, j := strings.Index(sys, "# Rules"), strings.Index(sys, "# Project impression"); i > j {
		t.Errorf("impression must come after the stable prefix:\n%.600s", sys)
	}
	// 与 Memory 召回互补：印象段在 Memory 段之前。
	if i, j := strings.Index(sys, "# Project impression"), strings.Index(sys, "# Memory"); j >= 0 && i > j {
		t.Errorf("impression must precede the memory recall section:\n%.600s", sys)
	}
}

// 缺失/损坏容错：没有印象文件或文件损坏（非法 UTF-8）→ 无印象段、不报错。
func TestBuildSessionImpressionFaultTolerance(t *testing.T) {
	dir := t.TempDir()
	if sys := BuildSessionCapped(dir, 2, 1024); strings.Contains(sys, "# Project impression") {
		t.Errorf("missing impression file must omit the section:\n%.600s", sys)
	}
	path := tools.ImpressionPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	sys := BuildSessionCapped(dir, 2, 1024)
	if strings.Contains(sys, "# Project impression") {
		t.Errorf("corrupted impression file must omit the section:\n%.600s", sys)
	}
}
