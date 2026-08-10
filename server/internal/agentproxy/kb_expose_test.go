package agentproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKBDatasetRoots(t *testing.T) {
	got := kbDatasetRoots([]KBDatasetDir{
		{Name: "a", Root: "/data/a"},
		{Name: "b", Root: "  "}, // blank root skipped
		{Name: "c", Root: "/data/c"},
	})
	if len(got) != 2 || got[0] != "/data/a" || got[1] != "/data/c" {
		t.Fatalf("unexpected roots: %+v", got)
	}
	if kbDatasetRoots(nil) != nil {
		t.Fatalf("nil dirs should yield nil roots")
	}
}

func TestUpsertKBBlock(t *testing.T) {
	block := renderKBBlock([]KBDatasetDir{{Name: "docs", Root: "/data/docs", Description: "公司文档"}})
	if !strings.Contains(block, "/data/docs") || !strings.Contains(block, "只读") {
		t.Fatalf("block missing root/readonly note: %q", block)
	}

	// Append to existing content.
	base := "# Workspace\n\nsome intro\n"
	out := upsertKBBlock(base, block)
	if !strings.Contains(out, "some intro") || strings.Count(out, kbBlockBegin) != 1 {
		t.Fatalf("append result wrong: %q", out)
	}

	// Replacing is idempotent and never duplicates the block.
	out2 := upsertKBBlock(out, block)
	if strings.Count(out2, kbBlockBegin) != 1 || strings.Count(out2, kbBlockEnd) != 1 {
		t.Fatalf("upsert duplicated the block: %q", out2)
	}

	// A new (different) block replaces the old one.
	block2 := renderKBBlock([]KBDatasetDir{{Name: "kb2", Root: "/data/kb2"}})
	out3 := upsertKBBlock(out2, block2)
	if strings.Contains(out3, "/data/docs") || !strings.Contains(out3, "/data/kb2") {
		t.Fatalf("replace did not swap content: %q", out3)
	}
	if strings.Count(out3, kbBlockBegin) != 1 {
		t.Fatalf("replace left more than one block: %q", out3)
	}

	// Empty block removes the section but keeps surrounding content.
	out4 := upsertKBBlock(out3, "")
	if strings.Contains(out4, kbBlockBegin) || !strings.Contains(out4, "some intro") {
		t.Fatalf("clear did not remove block or lost content: %q", out4)
	}
}

func TestWriteKBInstructionBlock(t *testing.T) {
	dir := t.TempDir()
	// Seed an existing CLAUDE.md (claude default).
	claude := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claude, []byte("# Workspace\n\nintro\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dirs := []KBDatasetDir{{Name: "docs", Root: filepath.Join(dir, "kb")}}
	if err := writeKBInstructionBlock(dir, "claude", dirs); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(claude)
	if !strings.Contains(string(data), kbBlockBegin) || !strings.Contains(string(data), "intro") {
		t.Fatalf("block not written or intro lost: %q", data)
	}

	// codex writes AGENTS.md; create from scratch when absent.
	if err := writeKBInstructionBlock(dir, "codex", dirs); err != nil {
		t.Fatalf("codex write: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), kbBlockBegin) {
		t.Fatalf("AGENTS.md not created with block: err=%v data=%q", err, agents)
	}

	// Clearing removes the managed block from CLAUDE.md.
	if err := writeKBInstructionBlock(dir, "claude", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	data, _ = os.ReadFile(claude)
	if strings.Contains(string(data), kbBlockBegin) {
		t.Fatalf("clear left block behind: %q", data)
	}
}
