package service

import (
	"context"
	"fmt"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ModelUsageSummary is one owner's model usage over a time window: the total,
// plus a breakdown along one requested dimension. Dimensions other than the
// requested one stay empty so a caller can ask for one view at a time.
type ModelUsageSummary struct {
	TotalTokens int64           `json:"total_tokens"`
	Requests    int64           `json:"requests"`
	ByModel     []ModelUsageRow `json:"by_model,omitempty"`
	ByProvider  []ModelUsageRow `json:"by_provider,omitempty"`
	ByProject   []ModelUsageRow `json:"by_project,omitempty"`
	ByWorkspace []ModelUsageRow `json:"by_workspace,omitempty"`
	ByCLI       []ModelUsageRow `json:"by_cli,omitempty"`
}

// ModelUsageRow is one bucket of a breakdown. Key is the dimension value
// rendered as a string (model name, CLI name, or a numeric id).
type ModelUsageRow struct {
	Key         string `json:"key"`
	TotalTokens int64  `json:"total_tokens"`
	Requests    int64  `json:"requests"`
}

type ModelUsageService struct {
	q *store.Queries
}

func NewModelUsageService(q *store.Queries) *ModelUsageService {
	return &ModelUsageService{q: q}
}

// Summary aggregates model_usage_hourly for one owner over [from, to).
// groupBy selects the single breakdown to return: "model", "provider",
// "project", "workspace", "cli", or "" for totals only.
func (s *ModelUsageService) Summary(ctx context.Context, owner OwnerRef, from, to time.Time, groupBy string) (ModelUsageSummary, error) {
	if err := owner.Validate(); err != nil {
		return ModelUsageSummary{}, err
	}
	from, to = from.UTC(), to.UTC()

	total, err := s.q.SumModelUsageByOwner(ctx, store.SumModelUsageByOwnerParams{
		OwnerType:    owner.Type,
		OwnerID:      owner.ID,
		BucketHour:   from,
		BucketHour_2: to,
	})
	if err != nil {
		return ModelUsageSummary{}, fmt.Errorf("sum model usage: %w", err)
	}
	// The workspace grouping is a complete partition of the owner's rows, so
	// summing its requests yields the window total without another query.
	byWorkspace, err := s.q.ListModelUsageByWorkspace(ctx, store.ListModelUsageByWorkspaceParams{
		OwnerType:    owner.Type,
		OwnerID:      owner.ID,
		BucketHour:   from,
		BucketHour_2: to,
	})
	if err != nil {
		return ModelUsageSummary{}, fmt.Errorf("list model usage: %w", err)
	}
	out := ModelUsageSummary{TotalTokens: total}
	for _, r := range byWorkspace {
		out.Requests += r.Requests
	}

	switch groupBy {
	case "":
		return out, nil
	case "workspace":
		out.ByWorkspace = make([]ModelUsageRow, 0, len(byWorkspace))
		for _, r := range byWorkspace {
			out.ByWorkspace = append(out.ByWorkspace, ModelUsageRow{
				Key: fmt.Sprint(r.WorkspaceID), TotalTokens: r.TotalTokens, Requests: r.Requests,
			})
		}
	case "model":
		rows, err := s.q.ListModelUsageByModel(ctx, store.ListModelUsageByModelParams{
			OwnerType: owner.Type, OwnerID: owner.ID, BucketHour: from, BucketHour_2: to,
		})
		if err != nil {
			return ModelUsageSummary{}, fmt.Errorf("list model usage by model: %w", err)
		}
		out.ByModel = make([]ModelUsageRow, 0, len(rows))
		for _, r := range rows {
			out.ByModel = append(out.ByModel, ModelUsageRow{Key: r.ModelName, TotalTokens: r.TotalTokens, Requests: r.Requests})
		}
	case "provider":
		rows, err := s.q.ListModelUsageByProvider(ctx, store.ListModelUsageByProviderParams{
			OwnerType: owner.Type, OwnerID: owner.ID, BucketHour: from, BucketHour_2: to,
		})
		if err != nil {
			return ModelUsageSummary{}, fmt.Errorf("list model usage by provider: %w", err)
		}
		out.ByProvider = make([]ModelUsageRow, 0, len(rows))
		for _, r := range rows {
			out.ByProvider = append(out.ByProvider, ModelUsageRow{Key: fmt.Sprint(r.ProviderID), TotalTokens: r.TotalTokens, Requests: r.Requests})
		}
	case "project":
		rows, err := s.q.ListModelUsageByProject(ctx, store.ListModelUsageByProjectParams{
			OwnerType: owner.Type, OwnerID: owner.ID, BucketHour: from, BucketHour_2: to,
		})
		if err != nil {
			return ModelUsageSummary{}, fmt.Errorf("list model usage by project: %w", err)
		}
		out.ByProject = make([]ModelUsageRow, 0, len(rows))
		for _, r := range rows {
			out.ByProject = append(out.ByProject, ModelUsageRow{Key: fmt.Sprint(r.ProjectID), TotalTokens: r.TotalTokens, Requests: r.Requests})
		}
	case "cli":
		rows, err := s.q.ListModelUsageByCLI(ctx, store.ListModelUsageByCLIParams{
			OwnerType: owner.Type, OwnerID: owner.ID, BucketHour: from, BucketHour_2: to,
		})
		if err != nil {
			return ModelUsageSummary{}, fmt.Errorf("list model usage by cli: %w", err)
		}
		out.ByCLI = make([]ModelUsageRow, 0, len(rows))
		for _, r := range rows {
			out.ByCLI = append(out.ByCLI, ModelUsageRow{Key: r.AgentCli, TotalTokens: r.TotalTokens, Requests: r.Requests})
		}
	default:
		return ModelUsageSummary{}, fmt.Errorf("unknown model usage groupBy %q", groupBy)
	}
	return out, nil
}
