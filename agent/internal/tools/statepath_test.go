package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 状态目录在用户主目录下、按项目路径转义分区——绝不指向项目目录本身。
func TestStateDirUnderHome(t *testing.T) {
	cwd := t.TempDir()
	dir := StateDir(cwd)
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(dir, home) {
		t.Errorf("state dir %q must live under home %q", dir, home)
	}
	if strings.HasPrefix(dir, cwd) {
		t.Errorf("state dir %q must not pollute the project %q", dir, cwd)
	}
	base := filepath.Base(dir)
	if base == "" || base == "." || strings.ContainsAny(base, `\/:.`) {
		t.Errorf("escaped segment = %q, want flat [A-Za-z0-9_-]+", base)
	}
	// 同一 cwd 稳定；不同 cwd 不同分区。
	if StateDir(cwd) != dir {
		t.Error("not stable")
	}
	if StateDir(filepath.Join(cwd, "other")) == dir {
		t.Error("different projects must not share a state dir")
	}
}

// 各子路径派生自同一状态目录。
func TestStateDirSubpaths(t *testing.T) {
	cwd := t.TempDir()
	dir := StateDir(cwd)
	if SessionsDir(cwd) != filepath.Join(dir, "sessions") {
		t.Errorf("sessions = %q", SessionsDir(cwd))
	}
	if CompactStatePath(cwd) != filepath.Join(dir, "session-state.json") {
		t.Errorf("state = %q", CompactStatePath(cwd))
	}
	if HistoryDir(cwd) != filepath.Join(dir, "history") {
		t.Errorf("history = %q", HistoryDir(cwd))
	}
	if TodosPath(cwd) != filepath.Join(dir, "todos.json") {
		t.Errorf("todos = %q", TodosPath(cwd))
	}
}
