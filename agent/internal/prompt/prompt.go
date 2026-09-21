// Package prompt builds niuniu-agent's system prompt.
//
// The prompt is engineered as a STABLE PREFIX (identity → environment →
// tool guidance → rules → project context): within a session every byte is
// identical across rounds, which is what makes the provider-side prompt
// cache (Anthropic cache_control breakpoints, OpenAI implicit prefix
// caching) actually hit. Anything that varies per turn — tool results,
// user messages — lives in the message history, never here.
//
// Prompt-cache anti-debounce rules (cache killers are FORBIDDEN in the
// stable prefix):
//   - no wall-clock timestamps finer than a date, and no monotonic counters
//     (the date is session-constant, which keeps the prefix stable within a
//     session; cross-session churn only costs one write);
//   - no random ids, no request-scoped state, no per-turn file listings;
//   - section ORDER is fixed; new sections append after stable ones;
//   - anything dynamic belongs to the message history, where every turn is
//     expected to change anyway.
//
// These hold for every Build/BuildSession* variant and any future section —
// a review that adds per-turn content to the prefix should be rejected.
package prompt

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/skills"
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
`)
	// Skills index (name + description only) — the Skill tool loads the full
	// body on demand. Stable within the session (scanned once at Build), so
	// the cache-friendly prefix property holds.
	if idx := skills.Index(skills.Scan(cwd)); idx != "" {
		b.WriteString(`
# Skills

Full instructions for these load via the Skill tool (pass the name) when a task matches:
` + idx)
	}
	b.WriteString(`
# Rules
- Reply in the language the user writes in.
- When a question depends on local files, use the tools to inspect them; never invent file listings, file contents, or command output.
- Keep answers short and factual.
`)
	if ctx, ok := LoadProjectContext(cwd); ok {
		b.WriteString("\n# Project context\n\n" + ctx + "\n")
	}
	// Host capabilities injection: the generic contract any host (niuniu or
	// otherwise) uses to project abilities into the session. Same cap and
	// session-stable placement as the project context.
	if inject, ok := LoadInject(cwd); ok {
		b.WriteString("\n# Host capabilities\n\n" + inject + "\n")
	}
	// Task guidance (PROMPT.md): advisory, session-stable, placed after the
	// project context so earlier sections keep their byte offsets.
	if dyn, ok := LoadDynamicPrompt(cwd); ok {
		b.WriteString("\n# Task guidance\n\n" + dyn + "\n")
	}
	return b.String()
}

// BuildSession is the full session system prompt: Build plus the
// recalled-memory section. Both headless main and the ACP server use this
// as their systemFor, so every entry path gets identical context assembly.
// Session-constant across rounds (stable prefix for the prompt cache).
func BuildSession(cwd string) string {
	return BuildSessionCapped(cwd, 5, 2048)
}

// BuildSessionFor is the task-aware tiered variant: with a task hint, the
// recall ranks task-relevant experience first (hint keywords score the
// entries). Use for known-task runs — eval tasks, typed subagents — where
// relevant lessons matter more than blanket recency.
func BuildSessionFor(cwd, taskHint string) string {
	store := memory.NewStore(cwd)
	recall, err := store.RecallFor(taskHint, 3, 1024)
	if err != nil {
		slog.Warn("memory recall failed", "err", err)
	}
	return Build(cwd) + memory.Section(recall)
}

// BuildSessionCapped is BuildSession with explicit recall caps. Subagent
// wiring calls it with halved numbers: a narrow child task needs less
// recalled memory, and halving keeps the child's window lean.
func BuildSessionCapped(cwd string, recallTopN, recallBytes int) string {
	store := memory.NewStore(cwd)
	recall, err := store.Recall(recallTopN, recallBytes)
	if err != nil {
		slog.Warn("memory recall failed", "err", err)
	}
	return Build(cwd) + memory.Section(recall)
}

// LoadInject reads the host capability injection file for cwd:
// <cwd>/.niuniu-agent/inject.md. Returns ("", false) when absent. Content is
// capped at MaxContextBytes with a truncation note appended.
func LoadInject(cwd string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(cwd, ".niuniu-agent", "inject.md"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", false
	}
	if len(s) > MaxContextBytes {
		s = s[:MaxContextBytes] + "\n\n[host capabilities truncated at " +
			strconv.Itoa(MaxContextBytes/1024) + "KB; read .niuniu-agent/inject.md for the rest]"
	}
	return s, true
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

// LoadDynamicPrompt reads the task guidance file for cwd:
// <cwd>/.niuniu-agent/PROMPT.md. Returns ("", false) when absent. Content
// is capped at MaxContextBytes with a truncation note appended.
// SAFETY BOUNDARY: PROMPT.md is advisory task guidance only — the
// permission/approval layers are code-enforced hard constraints and are
// never overridable by prompt content.
func LoadDynamicPrompt(cwd string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(cwd, ".niuniu-agent", "PROMPT.md"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", false
	}
	if len(s) > MaxContextBytes {
		s = s[:MaxContextBytes] + "\n\n[task guidance truncated at " +
			strconv.Itoa(MaxContextBytes/1024) + "KB; read .niuniu-agent/PROMPT.md for the rest]"
	}
	return s, true
}
