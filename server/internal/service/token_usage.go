package service

import (
	"context"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TokenBucket is one hourly point in a token-usage time series.
type TokenBucket struct {
	Hour                time.Time `json:"hour"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	InteractionCount    int64     `json:"interaction_count"`
}

// TokenUsageService serves hourly token-usage series for a workspace or owner.
type TokenUsageService struct{ q *store.Queries }

func NewTokenUsageService(q *store.Queries) *TokenUsageService { return &TokenUsageService{q: q} }

// WorkspaceSeries returns hourly buckets for one workspace in [from, to).
func (s *TokenUsageService) WorkspaceSeries(ctx context.Context, workspaceID int64, from, to time.Time) ([]TokenBucket, error) {
	rows, err := s.q.ListWorkspaceTokenHourly(ctx, store.ListWorkspaceTokenHourlyParams{
		WorkspaceID:  workspaceID,
		BucketHour:   from.UTC(),
		BucketHour_2: to.UTC(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]TokenBucket, len(rows))
	for i, r := range rows {
		out[i] = TokenBucket{
			Hour:                r.BucketHour,
			InputTokens:         r.InputTokens,
			OutputTokens:        r.OutputTokens,
			CacheCreationTokens: r.CacheCreationTokens,
			CacheReadTokens:     r.CacheReadTokens,
			InteractionCount:    r.InteractionCount,
		}
	}
	return out, nil
}

// OwnerSeries returns hourly buckets summed across all workspaces owned by
// (ownerType, ownerID) in [from, to).
func (s *TokenUsageService) OwnerSeries(ctx context.Context, ownerType string, ownerID int64, from, to time.Time) ([]TokenBucket, error) {
	rows, err := s.q.ListOwnerTokenHourly(ctx, store.ListOwnerTokenHourlyParams{
		OwnerType:    ownerType,
		OwnerID:      ownerID,
		BucketHour:   from.UTC(),
		BucketHour_2: to.UTC(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]TokenBucket, len(rows))
	for i, r := range rows {
		out[i] = TokenBucket{
			Hour:                r.BucketHour,
			InputTokens:         r.InputTokens,
			OutputTokens:        r.OutputTokens,
			CacheCreationTokens: r.CacheCreationTokens,
			CacheReadTokens:     r.CacheReadTokens,
			InteractionCount:    r.InteractionCount,
		}
	}
	return out, nil
}
