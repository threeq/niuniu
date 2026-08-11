package service

import (
	"fmt"
	"path/filepath"
	"strings"
)

// workspaceAgentTarget resolves where (and in what format) a scene-referenced
// agent is materialized for a given workspace CLI. Each CLI keeps its subagents
// in a different directory with a different on-disk format:
//
//	claude → <ws>/.claude/agents/<name>.md    (markdown + YAML frontmatter)
//	qwen   → <ws>/.qwen/agents/<name>.md       (same markdown format)
//	codex  → <ws>/.codex/agents/<name>.toml     (TOML, body → developer_instructions)
//
// render produces the file content from the source agent markdown plus the
// authoritative name/description from the DB row.
type workspaceAgentTarget struct {
	dir    string // relative subdir under the workspace dir, e.g. ".claude/agents"
	ext    string // file extension including the dot, e.g. ".md"
	render func(content, name, description string) string
}

// agentCliTypes is the set of CLIs that have a workspace subagent directory.
// Used to clean every CLI's managed agents on each recompute so switching a
// workspace's CliType doesn't leave stale niuniu-managed files behind.
var agentCliTypes = []string{"claude", "qwen", "codex", "omp", "goose"}

// workspaceAgentTargetFor returns the materialization target for a CLI type.
// Unknown types fall back to the Claude layout (the historic default).
func workspaceAgentTargetFor(cliType string) workspaceAgentTarget {
	switch cliType {
	case "codex":
		return workspaceAgentTarget{
			dir:    filepath.Join(".codex", "agents"),
			ext:    ".toml",
			render: renderCodexAgentTOML,
		}
	case "qwen":
		return workspaceAgentTarget{
			dir:    filepath.Join(".qwen", "agents"),
			ext:    ".md",
			render: RewriteNiuniuAgentContent,
		}
	case "omp":
		// omp agents are markdown (.omp/agents/*.md), like Claude's.
		return workspaceAgentTarget{
			dir:    filepath.Join(".omp", "agents"),
			ext:    ".md",
			render: RewriteNiuniuAgentContent,
		}
	case "goose":
		// goose agents are markdown (.goose/agents/*.md), like Claude's.
		return workspaceAgentTarget{
			dir:    filepath.Join(".goose", "agents"),
			ext:    ".md",
			render: RewriteNiuniuAgentContent,
		}
	default: // claude (and any unknown CLI) keep the historic layout.
		return workspaceAgentTarget{
			dir:    filepath.Join(".claude", "agents"),
			ext:    ".md",
			render: RewriteNiuniuAgentContent,
		}
	}
}

// renderCodexAgentTOML converts a niuniu agent markdown file into a Codex
// subagent TOML file. The YAML frontmatter is dropped; its `model` field (if
// any) is mapped to a top-level `model` key, and the markdown body becomes the
// `developer_instructions` multi-line string. The DB-authoritative name and
// description take precedence over any frontmatter values. The file is stamped
// `managed_by = "niuniu"` so cleanManagedAgentsDir can reclaim it later.
func renderCodexAgentTOML(content, name, description string) string {
	_, body, _ := splitFrontmatter(content)
	model := readFrontmatterScalar(content, "model")

	var b strings.Builder
	b.WriteString("managed_by = " + tomlQuote("niuniu") + "\n")
	b.WriteString("name = " + tomlQuote(name) + "\n")
	if description != "" {
		b.WriteString("description = " + tomlQuote(description) + "\n")
	}
	if model != "" {
		b.WriteString("model = " + tomlQuote(model) + "\n")
	}
	b.WriteString("developer_instructions = " + tomlMultilineBasic(body) + "\n")
	return b.String()
}

// tomlMultilineBasic renders s as a TOML multi-line basic string (""" … """).
// Backslashes and double quotes are escaped so the body can never accidentally
// terminate the string or be reinterpreted as an escape sequence; the leading
// newline after the opening delimiter is trimmed by TOML parsers, so the body
// starts exactly at s.
//
// A multi-line basic string may contain literal newlines and tabs, but every
// other control character (including a bare CR that is not part of a CRLF) is
// illegal unescaped and makes the whole file unparseable. We therefore escape
// CR as \r and any remaining C0 control / DEL as \uXXXX, so an agent body with
// odd line endings or stray control bytes still round-trips instead of silently
// writing a .toml that Codex cannot load.
func tomlMultilineBasic(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	b.WriteString("\"\"\"\n")
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n' || r == '\t':
			b.WriteRune(r) // allowed literally in a multi-line basic string
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(fmt.Sprintf(`\u%04X`, r))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString("\"\"\"")
	return b.String()
}

// isManagedByNiuniuTOML reports whether a Codex agent TOML file declares the
// top-level key managed_by = "niuniu".
func isManagedByNiuniuTOML(content string) bool {
	return readTOMLTopLevelString(content, "managed_by") == "niuniu"
}

// readTOMLTopLevelString returns the value of key only when it is the *first*
// top-level key in the file, or "" otherwise. renderCodexAgentTOML always emits
// the managed_by marker as the very first key, so anchoring on first-key
// position lets this deliberately small reader (mirroring the frontmatter
// helpers) stay oblivious to multiline strings: a managed_by = "niuniu" line
// appearing inside a later developer_instructions = """…""" block is never the
// first key, so a hand-written user .toml is not misclassified as niuniu-managed
// and deleted. On any ambiguity it returns "" and the file is preserved.
func readTOMLTopLevelString(content, key string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break // entered a table before any key; marker absent
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			break // not a key/value line; stop before scanning into a value body
		}
		if strings.TrimSpace(line[:eq]) != key {
			break // a different key comes first; this file is not niuniu-managed
		}
		return unquoteTOMLBasic(strings.TrimSpace(line[eq+1:]))
	}
	return ""
}

// unquoteTOMLBasic decodes a single-line TOML basic string (`"..."`), stopping
// at the first unescaped closing quote (so a trailing inline comment is
// ignored). Returns "" if v is not a quoted basic string.
func unquoteTOMLBasic(v string) string {
	if len(v) < 2 || v[0] != '"' {
		return ""
	}
	var b strings.Builder
	esc := false
	for i := 1; i < len(v); i++ {
		c := v[i]
		if esc {
			switch c {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(c) // \\ \" and any other escape → literal char
			}
			esc = false
			continue
		}
		switch c {
		case '\\':
			esc = true
		case '"':
			return b.String() // closing quote
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
