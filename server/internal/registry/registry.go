package registry

import (
	"context"
	"fmt"
)

type AgentRegistry struct {
	local     *CLISource
	community *CommunitySource
	custom    *CustomSource
	curated   *CuratedSource
}

func NewAgentRegistry(local *CLISource, community *CommunitySource, custom *CustomSource, curated *CuratedSource) *AgentRegistry {
	return &AgentRegistry{local: local, community: community, custom: custom, curated: curated}
}

func (r *AgentRegistry) ListAll(ctx context.Context) (map[string][]AgentInfo, error) {
	result := make(map[string][]AgentInfo)

	localAgents, err := r.local.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list local: %w", err)
	}
	if localAgents == nil {
		localAgents = []AgentInfo{}
	}
	result["local"] = localAgents

	communityAgents, err := r.community.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list community: %w", err)
	}
	if communityAgents == nil {
		communityAgents = []AgentInfo{}
	}
	result["community"] = communityAgents

	customAgents, err := r.custom.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list custom: %w", err)
	}
	if customAgents == nil {
		customAgents = []AgentInfo{}
	}
	result["custom"] = customAgents

	if r.curated != nil {
		curatedAgents, err := r.curated.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list curated: %w", err)
		}
		if curatedAgents == nil {
			curatedAgents = []AgentInfo{}
		}
		result["curated"] = curatedAgents
	} else {
		result["curated"] = []AgentInfo{}
	}

	return result, nil
}

func (r *AgentRegistry) Get(ctx context.Context, source, name string) (*AgentDetail, error) {
	switch source {
	case "local":
		return r.local.Get(ctx, name)
	case "community":
		return r.community.Get(ctx, name)
	case "custom":
		return r.custom.Get(ctx, name)
	case "curated":
		if r.curated == nil {
			return nil, fmt.Errorf("curated source unavailable")
		}
		return r.curated.Get(ctx, name)
	default:
		return nil, fmt.Errorf("unknown source %q", source)
	}
}

func (r *AgentRegistry) Clone(ctx context.Context, source, name, newName string) (*AgentInfo, error) {
	detail, err := r.Get(ctx, source, name)
	if err != nil {
		return nil, fmt.Errorf("get source agent: %w", err)
	}

	return r.custom.Create(ctx, CreateCustomAgentInput{
		Name:        newName,
		Description: detail.Description,
		Content:     detail.Content,
		ClonedFrom:  source + ":" + name,
	})
}

func (r *AgentRegistry) Refresh(ctx context.Context, source string) error {
	switch source {
	case "local":
		return r.local.Refresh(ctx)
	case "community":
		return r.community.Refresh(ctx)
	case "custom":
		return r.custom.Refresh(ctx)
	case "curated":
		if r.curated == nil {
			return nil
		}
		return r.curated.Refresh(ctx)
	default:
		return fmt.Errorf("unknown source %q", source)
	}
}

func (r *AgentRegistry) CreateCustom(ctx context.Context, input CreateCustomAgentInput) (*AgentInfo, error) {
	return r.custom.Create(ctx, input)
}

func (r *AgentRegistry) UpdateCustom(ctx context.Context, name string, input UpdateCustomAgentInput) error {
	return r.custom.Update(ctx, name, input)
}

func (r *AgentRegistry) DeleteCustom(ctx context.Context, name string) error {
	return r.custom.Delete(ctx, name)
}
