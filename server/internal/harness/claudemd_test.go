package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectIntoAgentInstructions_CodexWritesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := InjectIntoAgentInstructions(dir, "codex", "run harness phase"); err != nil {
		t.Fatalf("InjectIntoAgentInstructions: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "run harness phase") {
		t.Fatalf("AGENTS.md missing harness prompt:\n%s", string(agents))
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md should not be created for codex injection, err=%v", err)
	}

	RemoveFromAgentInstructions(dir, "codex")
	agents, err = os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md after remove: %v", err)
	}
	if strings.Contains(string(agents), "run harness phase") {
		t.Fatalf("AGENTS.md still contains harness prompt:\n%s", string(agents))
	}
}

func TestInjectBoardSection_DefaultWritesClaudeMD(t *testing.T) {
	dir := t.TempDir()
	// A pre-existing harness section must survive board injection (independent markers).
	if err := InjectIntoAgentInstructions(dir, "claude", "run harness phase"); err != nil {
		t.Fatalf("InjectIntoAgentInstructions: %v", err)
	}
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
	if !strings.Contains(got, "run harness phase") {
		t.Fatalf("board injection clobbered the harness section:\n%s", got)
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

	// Remove: board gone, harness stays.
	RemoveBoardSection(dir, "claude")
	got = read()
	if strings.Contains(got, boardSectionStart) || strings.Contains(got, "审查 : review 时") {
		t.Fatalf("board section still present after remove:\n%s", got)
	}
	if !strings.Contains(got, "run harness phase") {
		t.Fatalf("remove board clobbered harness section:\n%s", got)
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

func TestInjectIntoAgentInstructions_DefaultWritesClaudeMD(t *testing.T) {
	dir := t.TempDir()
	if err := InjectIntoAgentInstructions(dir, "claude", "run harness phase"); err != nil {
		t.Fatalf("InjectIntoAgentInstructions: %v", err)
	}
	claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claude), "run harness phase") {
		t.Fatalf("CLAUDE.md missing harness prompt:\n%s", string(claude))
	}
}
