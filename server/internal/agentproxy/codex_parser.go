package agentproxy

// Backwards-compat shim — see parser.go for the rationale. The codex parser
// implementation lives in agentproxy/adapter/codex.go.

import (
	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// ParseCodexJSONLine forwards to the adapter implementation.
var ParseCodexJSONLine = adapter.ParseCodexJSONLine
