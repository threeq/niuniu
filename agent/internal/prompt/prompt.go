// Package prompt builds niuniu-agent's system prompt.
//
// The prompt is engineered as a STABLE PREFIX (identity → environment →
// tool guidance → rules → project context): within a session every byte is
// identical across rounds, which is what makes the provider-side prompt
// cache (Anthropic cache_control breakpoints, OpenAI implicit prefix
// caching) actually hit. Anything that varies per turn — tool results,
// user messages — lives in the message history, never here.
package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MaxContextBytes caps the injected project-context file (AGENTS.md /
// CLAUDE.md). Large KB projections must not crowd out the working window.
const MaxContextBytes = 40 << 10

// Build renders the system prompt for a session rooted at cwd.
func Build(cwd string) string {
	var b strings.Builder
	b.WriteString(`You are niuniu-agent, a careful coding agent working on the user's machine.

# Environment
- Working directory: ` + cwd + `
- Platform: ` + runtime.GOOS + `/` + runtime.GOARCH + `
- Today's date: ` + time.Now().Format("2006-01-02") + `

# Tools
- Explore before you change: LS/Glob to find files, Grep to search inside them, Read to inspect.
- Always Read a file before editing it with Edit; Edit replaces an exact old_string, so copy it from Read output (which carries line numbers — strip the "N→" prefixes).
- Prefer targeted edits (Edit) over rewriting whole files (Write).
- For multi-step work, maintain the task list with TodoWrite: mark items in_progress before starting and completed right after finishing; the list persists in .niuniu-agent/todos.json.
- Bash runs one-off shell commands; remember the OS shell differs per platform.

# Rules
- Reply in the language the user writes in.
- When a question depends on local files, use the tools to inspect them; never invent file listings, file contents, or command output.
- Keep answers short and factual.
`)
	if ctx, ok := LoadProjectContext(cwd); ok {
		b.WriteString("\n# Project context\n\n" + ctx + "\n")
	}
	return b.String()
}

// LoadProjectContext reads the project instruction file for cwd: AGENTS.md
// first, falling back to CLAUDE.md for repos that only carry the older
// convention. Returns ("", false) when neither exists. Content is capped at
// MaxContextBytes with a truncation note appended.
func LoadProjectContext(cwd string) (string, bool) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		if s == "" {
			continue
		}
		if len(s) > MaxContextBytes {
			s = s[:MaxContextBytes] + "\n\n[project context truncated at " +
				strconv.Itoa(MaxContextBytes/1024) + "KB; read " + name + " for the rest]"
		}
		return s, true
	}
	return "", false
}
