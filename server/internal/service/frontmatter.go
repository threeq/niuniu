package service

import (
	"fmt"
	"strings"
)

// Frontmatter helpers that operate on YAML-style `---` blocks at the head of
// agent markdown files. These do NOT re-serialize the block on write — they
// perform targeted line edits so unknown fields (including multi-line values
// like `tools:\n  - Read`) survive round-trips untouched.

const frontmatterDelim = "---"

// splitFrontmatter finds the frontmatter block. If present, returns:
//
//	header   — the lines between the opening and closing `---` (no delimiters)
//	body     — everything after the closing `---\n`
//	ok       — true if a complete frontmatter block was found
//
// If no frontmatter block is present, ok is false and header is "".
func splitFrontmatter(content string) (header, body string, ok bool) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, frontmatterDelim+"\n") {
		return "", content, false
	}
	rest := content[len(frontmatterDelim)+1:]
	idx := strings.Index(rest, "\n"+frontmatterDelim)
	if idx < 0 {
		return "", content, false
	}
	header = rest[:idx]
	after := rest[idx+1+len(frontmatterDelim):]
	body = strings.TrimPrefix(after, "\n")
	return header, body, true
}

// joinFrontmatter re-assembles a frontmatter block with header and body.
func joinFrontmatter(header, body string) string {
	var sb strings.Builder
	sb.WriteString(frontmatterDelim)
	sb.WriteByte('\n')
	if header != "" {
		sb.WriteString(header)
		if !strings.HasSuffix(header, "\n") {
			sb.WriteByte('\n')
		}
	}
	sb.WriteString(frontmatterDelim)
	sb.WriteByte('\n')
	sb.WriteString(body)
	return sb.String()
}

// topLevelKey returns the top-level key declared on a frontmatter line, or ""
// if the line doesn't introduce a key (indented continuation, empty, comment).
func topLevelKey(line string) string {
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	// Indented line = continuation of previous key (e.g. YAML list item).
	if line[0] == ' ' || line[0] == '\t' {
		return ""
	}
	colon := strings.Index(line, ":")
	if colon < 0 {
		return ""
	}
	return strings.TrimSpace(line[:colon])
}

// readFrontmatterScalar returns the scalar value of a top-level key, or "" if
// the key is absent or has a non-scalar value.
func readFrontmatterScalar(content, key string) string {
	header, _, ok := splitFrontmatter(content)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(header, "\n") {
		if topLevelKey(line) != key {
			continue
		}
		colon := strings.Index(line, ":")
		return strings.TrimSpace(line[colon+1:])
	}
	return ""
}

// setFrontmatterField sets a scalar field in the frontmatter block.
//   - If the block is absent, one is created containing just this field.
//   - If the key exists as a top-level scalar, its line is replaced.
//   - If the key exists as a non-scalar (e.g. list or mapping), those lines
//     are dropped and a new scalar line is appended. This is a compromise:
//     we never turn a scalar we own (name/description) into a list.
//   - If the key is missing, a new line is appended before the closing `---`.
//
// All other lines (comments, unknown keys, list items) are preserved verbatim.
func setFrontmatterField(content, key, value string) string {
	header, body, ok := splitFrontmatter(content)
	if !ok {
		return joinFrontmatter(fmt.Sprintf("%s: %s", key, value), content)
	}

	lines := strings.Split(header, "\n")
	// Drop empty trailing line caused by trailing newline in header.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	newLine := fmt.Sprintf("%s: %s", key, value)
	replaced := false
	out := make([]string, 0, len(lines)+1)
	i := 0
	for i < len(lines) {
		line := lines[i]
		if topLevelKey(line) == key {
			// Replace this key and skip any indented continuation lines.
			if !replaced {
				out = append(out, newLine)
				replaced = true
			}
			i++
			for i < len(lines) && isContinuation(lines[i]) {
				i++
			}
			continue
		}
		out = append(out, line)
		i++
	}
	if !replaced {
		out = append(out, newLine)
	}
	return joinFrontmatter(strings.Join(out, "\n"), body)
}

// isContinuation reports whether a line is an indented continuation or blank
// within a YAML block — i.e. belongs to the previous key.
func isContinuation(line string) bool {
	if line == "" {
		return false
	}
	return line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(line, "- ")
}

// ensureFrontmatter guarantees name and description fields exist. Existing
// values are preserved; only missing fields are added.
func ensureFrontmatter(content, name, description string) string {
	if readFrontmatterScalar(content, "name") == "" {
		content = setFrontmatterField(content, "name", name)
	}
	if description != "" && readFrontmatterScalar(content, "description") == "" {
		content = setFrontmatterField(content, "description", description)
	}
	return content
}

// syncFrontmatterMetadata forces the `name` and `description` frontmatter
// fields to match the given values, so they stay in sync with the DB row on
// every write. Other fields (tools, model, color, …) are preserved verbatim.
// An empty description leaves the existing description line untouched.
func syncFrontmatterMetadata(content, name, description string) string {
	content = setFrontmatterField(content, "name", name)
	if description != "" {
		content = setFrontmatterField(content, "description", description)
	}
	return content
}

// rewriteFrontmatterName forces the name field to the given value (used when
// namespacing team agents with a niuniu- prefix). description is filled in
// only if absent.
func rewriteFrontmatterName(content, newName, description string) string {
	content = setFrontmatterField(content, "name", newName)
	if description != "" && readFrontmatterScalar(content, "description") == "" {
		content = setFrontmatterField(content, "description", description)
	}
	return content
}

// stampManagedBy marks the file as niuniu-managed so CleanWorkspaceAgents can
// safely distinguish it from user-installed agents that happen to share a
// name prefix.
func stampManagedBy(content string) string {
	return setFrontmatterField(content, "managed_by", "niuniu")
}

// RewriteNiuniuAgentContent prepares agent markdown for placement under
// workspace/.claude/agents/ as a niuniu-managed file: it rewrites the
// frontmatter `name` field to prefixedName, fills in description if absent,
// and stamps `managed_by: niuniu`. Unknown frontmatter fields (including
// multi-line values such as `tools:` lists) are preserved verbatim.
func RewriteNiuniuAgentContent(content, prefixedName, description string) string {
	content = rewriteFrontmatterName(content, prefixedName, description)
	content = stampManagedBy(content)
	return content
}

// isManagedByNiuniu returns true when the file declares `managed_by: niuniu`
// in its frontmatter.
func isManagedByNiuniu(content string) bool {
	return readFrontmatterScalar(content, "managed_by") == "niuniu"
}
