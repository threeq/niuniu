package registry

import (
	"context"
	"fmt"
)

type CommunitySource struct {
	registryName string
	registryURL  string
	enabled      bool
}

func NewCommunitySource(name, url string, enabled bool) *CommunitySource {
	return &CommunitySource{
		registryName: name,
		registryURL:  url,
		enabled:      enabled,
	}
}

func (s *CommunitySource) Type() string { return "community" }

func (s *CommunitySource) List(ctx context.Context) ([]AgentInfo, error) {
	if !s.enabled {
		return nil, nil
	}
	// TODO: implement when registry source is determined
	return nil, nil
}

func (s *CommunitySource) Get(ctx context.Context, name string) (*AgentDetail, error) {
	return nil, fmt.Errorf("community agent %q not found", name)
}

func (s *CommunitySource) Refresh(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	// TODO: implement when registry source is determined
	return nil
}
