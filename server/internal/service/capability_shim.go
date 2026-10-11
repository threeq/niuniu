package service

import (
	"context"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
)

// capabilityResolverShim adapts *CapabilityService to
// agentproxy.CapabilityResolver, rendering each doc into its section text so the
// agent-spawn path can inject capability descriptions without importing service
// (which would form an import cycle — service already imports agentproxy).
type capabilityResolverShim struct{ inner *CapabilityService }

// NewAgentProxyCapabilityResolver wraps a *CapabilityService as an
// agentproxy.CapabilityResolver. Pass the result into
// AgentProxy.SetCapabilityResolver.
func NewAgentProxyCapabilityResolver(s *CapabilityService) agentproxy.CapabilityResolver {
	return &capabilityResolverShim{inner: s}
}

func (s *capabilityResolverShim) WorkspaceCapabilities(ctx context.Context, workspaceID int64) ([]agentproxy.AgentCapability, error) {
	docs, err := s.inner.WorkspaceCapabilities(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]agentproxy.AgentCapability, 0, len(docs))
	for _, d := range docs {
		if d == nil {
			continue
		}
		out = append(out, agentproxy.AgentCapability{
			TargetKind:  d.TargetKind,
			Name:        d.Name,
			DisplayName: d.DisplayName,
			Section:     RenderCapabilitySection(d),
		})
	}
	return out, nil
}
