package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManualPublishPayload is materialized as an on-disk package for a human
// operator to publish by hand.
type ManualPublishPayload struct {
	Bot            string            `json:"bot"`
	IdempotencyKey string            `json:"idempotency_key"`
	Platform       string            `json:"platform,omitempty"`
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	MediaPaths     []string          `json:"media_paths,omitempty"`
	Params         map[string]string `json:"params,omitempty"`
}

// ManualAdapter only supports publishing, by writing a package under
// <root>/manual/<bot>/<idempotency>/.
type ManualAdapter struct {
	root string
}

var _ ChannelAdapter = (*ManualAdapter)(nil)

func NewManualAdapter(root string) *ManualAdapter {
	return &ManualAdapter{root: root}
}

func (a *ManualAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{CapPublish: true}
}

func (a *ManualAdapter) EnsureSession(context.Context, string) error { return nil }

func (a *ManualAdapter) HealthCheck(context.Context) error { return nil }

func (a *ManualAdapter) Publish(_ context.Context, payload any) (Receipt, error) {
	p, err := manualPayload(payload)
	if err != nil {
		return Receipt{}, &AdapterError{Capability: CapPublish, Code: "invalid_payload", Err: err}
	}

	dir := filepath.Join(a.root, "manual", safeSegment(p.Bot), safeSegment(p.IdempotencyKey))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Receipt{}, &AdapterError{Capability: CapPublish, Code: "package_write_failed", Retryable: true, Err: err}
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return Receipt{}, &AdapterError{Capability: CapPublish, Code: "package_encode_failed", Err: err}
	}
	if err := os.WriteFile(filepath.Join(dir, "payload.json"), data, 0o644); err != nil {
		return Receipt{}, &AdapterError{Capability: CapPublish, Code: "package_write_failed", Retryable: true, Err: err}
	}

	return Receipt{ExternalID: "manual:" + dir, Raw: string(data)}, nil
}

func (a *ManualAdapter) FetchMetrics(context.Context, string, string) (map[string]float64, error) {
	return nil, manualUnavailable(CapMetrics)
}

func (a *ManualAdapter) ListEngagements(context.Context, string) ([]Engagement, error) {
	return nil, manualUnavailable(CapEngagements)
}

func (a *ManualAdapter) Reply(context.Context, string, string) (Receipt, error) {
	return Receipt{}, manualUnavailable(CapReply)
}

func manualUnavailable(c Capability) *AdapterError {
	return &AdapterError{Capability: c, Code: "manual", Err: ErrCapabilityUnavailable}
}

func manualPayload(payload any) (ManualPublishPayload, error) {
	var p ManualPublishPayload
	switch v := payload.(type) {
	case ManualPublishPayload:
		p = v
	case *ManualPublishPayload:
		if v == nil {
			return p, fmt.Errorf("nil manual publish payload")
		}
		p = *v
	default:
		return p, fmt.Errorf("unsupported payload type %T", payload)
	}
	if strings.TrimSpace(p.Bot) == "" {
		return p, fmt.Errorf("bot is required")
	}
	if strings.TrimSpace(p.IdempotencyKey) == "" {
		return p, fmt.Errorf("idempotency key is required")
	}
	return p, nil
}

// safeSegment keeps a path component inside the package root.
func safeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "_"
	}
	return out
}
