package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLanguageFromFilename(t *testing.T) {
	cases := map[string]string{
		"main.go":           "go",
		"app.ts":            "typescript",
		"comp.tsx":          "typescript",
		"script.js":         "javascript",
		"util.py":           "python",
		"lib.rs":            "rust",
		"Main.java":         "java",
		"Service.kt":        "kotlin",
		"app.rb":            "ruby",
		"index.php":         "php",
		"prog.cs":           "csharp",
		"View.swift":        "swift",
		"util.c":            "c",
		"engine.cpp":        "cpp",
		"README.md":         "",
		"package.json":      "",
		"image.png":         "",
		"":                  "",
	}
	for name, want := range cases {
		assert.Equal(t, want, languageFromFilename(name), "name=%q", name)
	}
}

func TestTopNLanguages_OrdersByCountDesc(t *testing.T) {
	counts := map[string]int{"go": 10, "typescript": 5, "python": 20, "java": 1}
	got := topNLanguages(counts, 3)
	assert.Equal(t, []string{"python", "go", "typescript"}, got)
}

func TestTopNLanguages_TiebreakByName(t *testing.T) {
	counts := map[string]int{"go": 5, "typescript": 5}
	got := topNLanguages(counts, 2)
	// Tie at count=5; alphabetical "go" < "typescript"
	assert.Equal(t, []string{"go", "typescript"}, got)
}

func TestTopNLanguages_NLargerThanInput(t *testing.T) {
	counts := map[string]int{"go": 1}
	got := topNLanguages(counts, 5)
	assert.Equal(t, []string{"go"}, got)
}

func TestTopNLanguages_Empty(t *testing.T) {
	got := topNLanguages(map[string]int{}, 3)
	assert.Empty(t, got)
}

func TestTokenizeInto(t *testing.T) {
	out := map[string]int{}
	tokenizeInto("Customer support ticket — 客服 工单", out)
	assert.Equal(t, 1, out["customer"])
	assert.Equal(t, 1, out["support"])
	assert.Equal(t, 1, out["ticket"])
	assert.Equal(t, 1, out["客服"])
	assert.Equal(t, 1, out["工单"])
}

func TestTokenizeInto_FiltersTooShort(t *testing.T) {
	out := map[string]int{}
	tokenizeInto("a b ab abc", out)
	// length<2 filtered (single ascii chars)
	assert.NotContains(t, out, "a")
	assert.NotContains(t, out, "b")
	assert.Equal(t, 1, out["ab"])
	assert.Equal(t, 1, out["abc"])
}

func TestScanLanguageExtensions_RealDir(t *testing.T) {
	dir := t.TempDir()
	// Create a couple of files in language-distinguishing extensions.
	mustWrite(t, filepath.Join(dir, "main.go"), "package main")
	mustWrite(t, filepath.Join(dir, "util.go"), "package main")
	mustWrite(t, filepath.Join(dir, "page.tsx"), "export const X = 1")
	// Make a nested skip dir to confirm it's filtered.
	mustMkdir(t, filepath.Join(dir, "node_modules"))
	mustWrite(t, filepath.Join(dir, "node_modules", "boot.js"), "// vendored")

	counts := map[string]int{}
	scanLanguageExtensions(dir, 0, counts)
	assert.Equal(t, 2, counts["go"])
	assert.Equal(t, 1, counts["typescript"])
	assert.Zero(t, counts["javascript"], "node_modules must be skipped")
}

func TestUtf8RuneCount(t *testing.T) {
	assert.Equal(t, 0, utf8RuneCount(""))
	assert.Equal(t, 3, utf8RuneCount("abc"))
	assert.Equal(t, 2, utf8RuneCount("客服"))
}

// mustMkdir is a tiny test helper. (mustWrite already exists in
// mcp_detector_test.go in this package; we reuse it.)
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
