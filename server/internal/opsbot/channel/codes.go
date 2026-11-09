package channel

import "fmt"

// Adapter error codes shared with the publish orchestrator and metrics sync.
const (
	CodeCredentialInvalid = "credential_invalid"
	CodeRateLimited       = "rate_limited"
	CodeUnsupported       = "unsupported"
	CodeInvalidPayload    = "invalid_payload"
	CodeSystemBusy        = "system_busy"
	CodeTransport         = "transport"
)

// Unified metric keys: every adapter reports the same names.
const (
	MetricViews         = "views"
	MetricShares        = "shares"
	MetricLikes         = "likes"
	MetricFollowerDelta = "follower_delta"
)

var unifiedMetrics = []string{MetricViews, MetricShares, MetricLikes, MetricFollowerDelta}

func invalidPayload(cap Capability, msg string) *AdapterError {
	return &AdapterError{Capability: cap, Code: CodeInvalidPayload, Err: fmt.Errorf("%s", msg)}
}
