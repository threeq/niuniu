package service

import (
	"net/url"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// extractMatcherField returns the input field used for allowlist matching for
// the given tool. Returns "" when the tool is unrecognized or the field is
// missing/wrong-type — callers should treat empty as "fall through to 'any'
// matchers only".
func extractMatcherField(tool string, input map[string]any) string {
	switch tool {
	case "Bash":
		s, _ := input["command"].(string)
		return s
	case "Edit", "Write":
		s, _ := input["file_path"].(string)
		return s
	case "WebFetch":
		s, _ := input["url"].(string)
		return s
	default:
		return ""
	}
}

// matcherMatches returns true when (kind, value) matches the extracted field.
// Unknown kinds return false.
func matcherMatches(kind, value, field string) bool {
	switch kind {
	case "any":
		return true
	case "exact":
		return field == value
	case "prefix":
		return strings.HasPrefix(field, value)
	case "glob":
		ok, err := doublestar.PathMatch(value, field)
		return err == nil && ok
	case "domain":
		u, err := url.Parse(field)
		if err != nil || u.Host == "" {
			return false
		}
		return u.Host == value || strings.HasSuffix(u.Host, "."+value)
	default:
		return false
	}
}

// highRiskTools are tools where "always allow" requires a non-'any' matcher.
var highRiskTools = map[string]struct{}{
	"Bash":     {},
	"Edit":     {},
	"Write":    {},
	"WebFetch": {},
}

// IsHighRiskTool reports whether always-allow for this tool requires a matcher
// kind other than 'any'. Used by REST validation and by the SPA to choose
// between one-click and submenu UX.
func IsHighRiskTool(tool string) bool {
	_, ok := highRiskTools[tool]
	return ok
}

// DefaultSafeTools are read-only tools auto-allowed without prompting.
// Concatenated into --allowedTools at agent start.
//
// Mirrored as defaultSafeToolsLocal in agentproxy/proxy.go (the agentproxy
// package can't import this package due to an existing service→agentproxy
// dependency). Keep both in sync.
var DefaultSafeTools = []string{"Read", "Glob", "Grep", "LS", "NotebookRead", "TodoRead"}
