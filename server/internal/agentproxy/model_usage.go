package agentproxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// modelUsageParams is one turn's contribution to model_usage_hourly. The row is
// keyed by every dimension the aggregation can group by, so a single turn
// produces one row per (owner, project, workspace, cli, model, provider, hour).
type modelUsageParams struct {
	OwnerType                            string
	OwnerID, ProjectID, WorkspaceID      int64
	AgentCLI, ModelName                  string
	ProviderID                           int64
	InputTokens, OutputTokens            int
	CacheCreationTokens, CacheReadTokens int
	InteractionCount                     int
	Estimated                            bool
	At                                   time.Time
}

// recordModelUsage writes one turn to model_usage_hourly. It is the model-aware
// sibling of recordProviderTokens and runs alongside it: provider_token_hourly
// answers "how much did this subscription platform consume", model_usage_hourly
// answers "what did this owner spend, on which model, in which project".
//
// A turn that reported no usage at all still counts as a request: the row lands
// with zero tokens and estimated=1 so the UI can say that window's usage is a
// floor rather than a measurement. Write failures are logged, never returned —
// accounting must not fail a turn.
func recordModelUsage(ctx context.Context, q *store.Queries, p modelUsageParams) {
	if q == nil || p.OwnerID <= 0 || p.InteractionCount <= 0 {
		return
	}
	estimated := p.Estimated
	if p.InputTokens == 0 && p.OutputTokens == 0 &&
		p.CacheCreationTokens == 0 && p.CacheReadTokens == 0 {
		estimated = true
	}
	if p.ModelName == "" {
		p.ModelName = "unknown"
	}
	if err := q.UpsertModelUsageHourly(ctx, store.UpsertModelUsageHourlyParams{
		OwnerType:           normalizeOwnerType(p.OwnerType),
		OwnerID:             p.OwnerID,
		ProjectID:           p.ProjectID,
		WorkspaceID:         p.WorkspaceID,
		AgentCli:            p.AgentCLI,
		ModelName:           p.ModelName,
		ProviderID:          p.ProviderID,
		BucketHour:          p.At.UTC().Truncate(time.Hour),
		InputTokens:         int64(p.InputTokens),
		OutputTokens:        int64(p.OutputTokens),
		CacheCreationTokens: int64(p.CacheCreationTokens),
		CacheReadTokens:     int64(p.CacheReadTokens),
		InteractionCount:    int64(p.InteractionCount),
		Estimated:           boolToInt64(estimated),
	}); err != nil {
		slog.Warn("recordModelUsage failed",
			"workspace_id", p.WorkspaceID, "model", p.ModelName, "error", err)
	}
}

// recordTurnModelUsage records one completed turn against both dimensions.
// turns is the turn's request count; a caller that cannot tell gets 1.
func (s *WorkspaceSession) recordTurnModelUsage(
	ctx context.Context,
	providerID int64,
	inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int,
	turns int,
) {
	if s.isTemporary {
		return
	}
	if turns <= 0 {
		turns = 1
	}
	recordModelUsage(ctx, s.q, modelUsageParams{
		OwnerType:           s.ownerType,
		OwnerID:             s.ownerID,
		ProjectID:           s.projectID,
		WorkspaceID:         s.workspaceID,
		AgentCLI:            s.cliType,
		ModelName:           s.resolveUsageModelName(ctx, providerID),
		ProviderID:          providerID,
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		CacheCreationTokens: cacheCreationTokens,
		CacheReadTokens:     cacheReadTokens,
		InteractionCount:    turns,
		At:                  time.Now(),
	})
}

// resolveUsageModelName picks the model this turn actually ran on: the
// NIUNIU_MODEL override when set, else the bound provider's configured model
// (codex reads codex_model, every other CLI reads model), else "unknown".
func (s *WorkspaceSession) resolveUsageModelName(ctx context.Context, providerID int64) string {
	s.mu.Lock()
	name := s.modelName
	s.mu.Unlock()
	if name != "" {
		return name
	}
	if providerID <= 0 {
		return "unknown"
	}
	p, err := s.q.GetEnvProvider(ctx, providerID)
	if err != nil {
		return "unknown"
	}
	if s.cliType == "codex" && p.CodexModel != "" {
		return p.CodexModel
	}
	if p.Model != "" {
		return p.Model
	}
	return "unknown"
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
