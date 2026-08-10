package agentproxy

// Backwards-compat shim. The real parser, types, and Adapter abstraction live
// in agentproxy/adapter/. We expose type aliases + function variables here so
// the rest of the package (proxy.go, codex_exec.go, tests, etc.) keeps
// compiling without a 61-place find/replace.
//
// New code should import agentproxy/adapter directly and use the qualified
// names (adapter.ParsedEvent, adapter.ParseStreamLine, ...).

import (
	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// Type aliases keep `agentproxy.ParsedEvent` etc. usable everywhere.
type (
	ParsedEvent     = adapter.ParsedEvent
	TextBlock       = adapter.TextBlock
	ToolUseBlock    = adapter.ToolUseBlock
	ThinkingBlock   = adapter.ThinkingBlock
	ToolResultBlock = adapter.ToolResultBlock
	ApprovalRequest = adapter.ApprovalRequest
)

// ParseStreamLine forwards to the adapter implementation so existing callers
// keep working. New code should use adapter.ClaudeAdapter{}.ParseLine.
var ParseStreamLine = adapter.ParseStreamLine

// truncate keeps living here because several agentproxy-internal files
// (codex_exec.go, proxy.go) call it; the adapter package doesn't need it.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
