package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectBoardSection_DefaultWritesClaudeMD(t *testing.T) {
	dir := t.TempDir()
	if err := InjectBoardSection(dir, "claude", "实现 : 写代码时"); err != nil {
		t.Fatalf("InjectBoardSection: %v", err)
	}
	read := func() string {
		b, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("read CLAUDE.md: %v", err)
		}
		return string(b)
	}
	got := read()
	if !strings.Contains(got, boardSectionStart) || !strings.Contains(got, boardSectionEnd) {
		t.Fatalf("CLAUDE.md missing board markers:\n%s", got)
	}
	if !strings.Contains(got, "实现 : 写代码时") {
		t.Fatalf("CLAUDE.md missing board content:\n%s", got)
	}

	// Re-inject: replaces in place, no duplicate markers.
	if err := InjectBoardSection(dir, "claude", "审查 : review 时"); err != nil {
		t.Fatalf("InjectBoardSection re-inject: %v", err)
	}
	got = read()
	if strings.Count(got, boardSectionStart) != 1 {
		t.Fatalf("expected exactly one board section after re-inject:\n%s", got)
	}
	if strings.Contains(got, "实现 : 写代码时") || !strings.Contains(got, "审查 : review 时") {
		t.Fatalf("re-inject did not replace board content:\n%s", got)
	}

	RemoveBoardSection(dir, "claude")
	got = read()
	if strings.Contains(got, boardSectionStart) || strings.Contains(got, "审查 : review 时") {
		t.Fatalf("board section still present after remove:\n%s", got)
	}
}

func TestInjectBoardSection_CodexWritesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := InjectBoardSection(dir, "codex", "实现 : 写代码时"); err != nil {
		t.Fatalf("InjectBoardSection codex: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "实现 : 写代码时") {
		t.Fatalf("AGENTS.md missing board content:\n%s", string(agents))
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md should not be created for codex board injection, err=%v", err)
	}
}

// A HARNESS section written by an older build must be strippable, and stripping it
// must leave a co-existing BOARD section intact (independent marker pairs).
func TestRemoveLegacyHarnessSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	stale := "# ws\n\n" +
		harnessSectionStart + "\nrun harness phase\n" + harnessSectionEnd + "\n"
	if err := os.WriteFile(path, []byte(stale), 0644); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}
	if err := InjectBoardSection(dir, "claude", "实现 : 写代码时"); err != nil {
		t.Fatalf("InjectBoardSection: %v", err)
	}

	RemoveLegacyHarnessSection(dir, "claude")

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	got := string(b)
	if strings.Contains(got, harnessSectionStart) || strings.Contains(got, "run harness phase") {
		t.Fatalf("legacy harness section survived removal:\n%s", got)
	}
	if !strings.Contains(got, "实现 : 写代码时") {
		t.Fatalf("removing the harness section clobbered the board section:\n%s", got)
	}
	if !strings.Contains(got, "# ws") {
		t.Fatalf("removing the harness section clobbered user content:\n%s", got)
	}
}
