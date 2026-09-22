package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runEdit(t *testing.T, dir, oldStr, newStr string, replaceAll bool) (string, error) {
	t.Helper()
	in, _ := json.Marshal(editInput{Path: filepath.Join(dir, "f.txt"), OldString: oldStr, NewString: newStr, ReplaceAll: replaceAll})
	return Edit{}.Execute(context.Background(), in)
}

// CRLF 文件的精确匹配自动容错（模型常以 \n 描述 CRLF 文件内容）。
func TestEditToleratesCRLF(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f.txt"), "line1\r\nkeep me\r\nline3\r\n")
	out, err := runEdit(t, dir, "keep me", "changed", false)
	if err != nil {
		t.Fatalf("CRLF tolerance: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(data), "changed") || strings.Contains(string(data), "keep me") {
		t.Fatalf("out=%q data=%q", out, data)
	}
	// 行尾风格必须保留（CRLF 不被改写成 LF）。
	if !strings.Contains(string(data), "changed\r\n") {
		t.Errorf("CRLF endings not preserved: %q", data)
	}
}

// 行尾空白容错（old_string 无尾随空格而文件行尾有）。
func TestEditToleratesTrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f.txt"), "alpha  \nbeta\t\ngamma\n")
	if _, err := runEdit(t, dir, "beta", "BETA", false); err != nil {
		t.Fatalf("trailing-ws tolerance: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(data), "BETA") {
		t.Errorf("data=%q", data)
	}
}

// 完全不匹配时：错误信息包含最相似行（帮助模型自纠）。
func TestEditMissSuggestsClosestLine(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f.txt"), "func handler(w http.ResponseWriter) {\n\treturn nil\n}\n")
	_, err := runEdit(t, dir, "func handler(w http.ResponceWriter) {", "x", false)
	if err == nil {
		t.Fatal("want error on miss")
	}
	if !strings.Contains(err.Error(), "handler(w http.ResponseWriter)") {
		t.Errorf("error missing closest-line hint: %v", err)
	}
}

// 归一化匹配只在精确匹配失败时启用；精确匹配优先。
func TestEditExactMatchStillWins(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f.txt"), "a \nb\n")
	if _, err := runEdit(t, dir, "a", "A", false); err != nil {
		t.Fatalf("exact: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.HasPrefix(string(data), "A \n") {
		t.Errorf("exact match must replace only the exact span, preserving tail: %q", data)
	}
}
