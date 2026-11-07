package channel

import (
	"context"
	"errors"
	"fmt"
)

// Capability names a single platform operation an adapter can perform.
type Capability string

const (
	CapPublish     Capability = "publish"
	CapMetrics     Capability = "metrics"
	CapEngagements Capability = "engagements"
	CapReply       Capability = "reply"
)

// CapabilitySet reports which capabilities an adapter can serve. A missing or
// false entry means the operation must degrade (for example to a manual
// publish package).
type CapabilitySet map[Capability]bool

// Supports reports whether c is enabled.
func (s CapabilitySet) Supports(c Capability) bool { return s[c] }

// Receipt is the platform identifier returned after a successful write.
type Receipt struct {
	ExternalID string
	URL        string
	Raw        string
}

// Engagement is one inbound interaction that may need a reply.
type Engagement struct {
	ExtID  string
	Kind   string // comment | dm
	Author string
	Text   string
}

// ChannelAdapter is the only platform surface the ops bot depends on.
type ChannelAdapter interface {
	Capabilities() CapabilitySet
	EnsureSession(ctx context.Context, accountRef string) error
	Publish(ctx context.Context, payload any) (Receipt, error)
	FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error)
	ListEngagements(ctx context.Context, cursor string) ([]Engagement, error)
	Reply(ctx context.Context, engagementID, text string) (Receipt, error)
	HealthCheck(ctx context.Context) error
}

// ErrCapabilityUnavailable marks an operation the adapter cannot serve.
var ErrCapabilityUnavailable = errors.New("opsbot: capability unavailable")

// AdapterError describes a failed adapter call and whether retrying may help.
type AdapterError struct {
	Capability Capability
	Code       string
	Retryable  bool
	Err        error
}

func (e *AdapterError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("channel %s failed (%s)", e.Capability, e.Code)
	}
	return fmt.Sprintf("channel %s failed (%s): %v", e.Capability, e.Code, e.Err)
}

func (e *AdapterError) Unwrap() error { return e.Err }

// Is reports every adapter failure as ErrCapabilityUnavailable so callers can
// gate degradation with a single check, as the publish orchestrator expects.
func (e *AdapterError) Is(target error) bool { return target == ErrCapabilityUnavailable }
