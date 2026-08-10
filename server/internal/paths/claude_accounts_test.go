package paths

import (
	"path/filepath"
	"testing"
)

func TestClaudeAccountDir(t *testing.T) {
	got := ClaudeAccountDir("/data/.niuniu", "abc-123")
	want := filepath.Join("/data/.niuniu", "claude-accounts", "abc-123")
	if got != want {
		t.Errorf("ClaudeAccountDir = %q, want %q", got, want)
	}
}

func TestClaudeAccountDir_DataDirIsRelative(t *testing.T) {
	got := ClaudeAccountDir(".niuniu", "u1")
	want := filepath.Join(".niuniu", "claude-accounts", "u1")
	if got != want {
		t.Errorf("ClaudeAccountDir = %q, want %q", got, want)
	}
}
