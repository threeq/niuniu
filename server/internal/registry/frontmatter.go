package registry

import (
	"fmt"
	"sort"
	"strings"
)

func parseFrontmatter(content string) (map[string]string, string, error) {
	// Normalize CRLF to LF
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	fm := make(map[string]string)
	if !strings.HasPrefix(content, "---") {
		return fm, content, nil
	}

	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, content, nil
	}

	header := rest[:idx]
	after := rest[idx+4:]
	body := strings.TrimPrefix(after, "\n")

	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)
		fm[key] = val
	}

	return fm, body, nil
}

func buildFrontmatter(fields map[string]string, body string) string {
	// Sort keys for deterministic output
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString("---\n")
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("%s: %s\n", k, fields[k]))
	}
	sb.WriteString("---\n")
	sb.WriteString(body)
	return sb.String()
}
