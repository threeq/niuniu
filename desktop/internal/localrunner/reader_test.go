package localrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile_WithinBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("contents here"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})

	got, err := ReadFile(g, "note.txt", fixedNow)
	if err != nil {
		t.Fatalf("read within boundary: %v", err)
	}
	if got != "contents here" {
		t.Fatalf("got %q", got)
	}
}

func TestReadFile_RejectsEscape(t *testing.T) {
	dir := t.TempDir()
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})
	if _, err := ReadFile(g, "../outside.txt", fixedNow); err == nil {
		t.Fatal("reading outside the boundary must fail")
	}
}

func TestReadFile_Truncates(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxReadBytes+100)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})

	got, err := ReadFile(g, "big.txt", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "(truncated)…") {
		t.Fatal("oversized file should be truncated with a marker")
	}
}

func TestReadFile_DirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})
	if _, err := ReadFile(g, "sub", fixedNow); err == nil {
		t.Fatal("reading a directory should error")
	}
}
