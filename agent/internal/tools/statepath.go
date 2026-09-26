package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// Per-project agent state directory (Claude-Code-style layout): the agent's
// private session/state files live under the USER's home directory, keyed by
// an escaping of the project path — never inside the project itself. Only
// shareable project artifacts (AGENTS.md, inject.md, .mcp.json, skills/,
// PROMPT.md, project-layer memory/) stay in <cwd>/.niuniu-agent.
//
//	~/.niuniu-agent/projects/<escaped-cwd>/sessions/<id>.json   session snapshots
//	~/.niuniu-agent/projects/<escaped-cwd>/session-state.json   compacted state
//	~/.niuniu-agent/projects/<escaped-cwd>/history/             compact archive
//	~/.niuniu-agent/projects/<escaped-cwd>/todos.json           task list
func StateDir(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		// Deterministic fallback (rare: no HOME) — keep the old location
		// rather than failing the tool call.
		return filepath.Join(cwd, ".niuniu-agent")
	}
	return filepath.Join(home, ".niuniu-agent", "projects", escapePath(cwd))
}

// escapePath maps a project path to a flat, filesystem-safe segment
// (Claude-Code convention): every character outside [A-Za-z0-9_-] becomes
// '-', so "C:\Users\me\repo" → "C--Users-me-repo". Collisions are
// practically impossible for real paths.
func escapePath(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, cwd)
}

// SessionsDir is where session snapshots are saved/loaded for cwd.
func SessionsDir(cwd string) string { return filepath.Join(StateDir(cwd), "sessions") }

// CompactStatePath is the structured compaction state file for cwd.
func CompactStatePath(cwd string) string {
	return filepath.Join(StateDir(cwd), "session-state.json")
}

// HistoryDir is the compacted-history archive for cwd.
func HistoryDir(cwd string) string { return filepath.Join(StateDir(cwd), "history") }

// TodosPath is the task-list file for cwd.
func TodosPath(cwd string) string { return filepath.Join(StateDir(cwd), "todos.json") }
