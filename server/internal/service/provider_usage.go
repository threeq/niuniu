package service

import (
	"context"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ProviderUsageService serves subscription-platform (env_providers) grain
// reporting: hourly token consumption per platform, and the rate-limit episode
// log that records when a platform throttled us and when it came back into use.
//
// TokenUsageService answers "which workspace burned tokens"; this answers
// "which platform did we burn them ON, and what did its quota limits cost us in
// waiting time".
type ProviderUsageService struct{ q *store.Queries }

func NewProviderUsageService(q *store.Queries) *ProviderUsageService {
	return &ProviderUsageService{q: q}
}

// ProviderHourBucket is one hour of one platform's token consumption.
type ProviderHourBucket struct {
	Hour                time.Time `json:"hour"`
	ProviderID          int64     `json:"provider_id"`
	ProviderName        string    `json:"provider_name"`
	ProviderPlatform    string    `json:"provider_platform"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	InteractionCount    int64     `json:"interaction_count"`
}

// ProviderTotal is one platform's totals over the whole queried window, plus
// its rate-limit tally. TotalTokens is precomputed so every client (web, mobile,
// desktop) sorts and labels by the same definition of "consumption".
type ProviderTotal struct {
	ProviderID          int64  `json:"provider_id"`
	ProviderName        string `json:"provider_name"`
	ProviderPlatform    string `json:"provider_platform"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	InteractionCount    int64  `json:"interaction_count"`
	// RateLimitCount is how many times this platform throttled us in the
	// window; BlockedSeconds sums the CLOSED episodes only, and OpenCount is how
	// many are still unresolved (so the UI can say "currently limited" instead
	// of inventing an end time).
	RateLimitCount int64 `json:"rate_limit_count"`
	BlockedSeconds int64 `json:"blocked_seconds"`
	OpenCount      int64 `json:"open_count"`
}

// RateLimitEpisode is one 429 lifecycle: throttled at TriggeredAt, the platform
// promised a reset at ResetAt, and we actually used it again at ResumedAt (nil
// while still sidelined).
type RateLimitEpisode struct {
	ID               int64      `json:"id"`
	ProviderID       int64      `json:"provider_id"`
	ProviderName     string     `json:"provider_name"`
	ProviderPlatform string     `json:"provider_platform"`
	WorkspaceID      *int64     `json:"workspace_id"`
	TriggeredAt      time.Time  `json:"triggered_at"`
	ResetAt          time.Time  `json:"reset_at"`
	ResumedAt        *time.Time `json:"resumed_at"`
	ClearedManually  bool       `json:"cleared_manually"`
	Detail           string     `json:"detail"`
	// BlockedSeconds is ResumedAt-TriggeredAt for a closed episode, and 0 while
	// open — the UI shows "still limited" for those rather than a running clock
	// that would disagree with the server on refresh.
	BlockedSeconds int64 `json:"blocked_seconds"`
}

// maxRateLimitEvents bounds the event-log page. The log is a diagnostic list,
// not a paginated archive; three months of episodes for a handful of platforms
// stays far below this, and the cap keeps a pathological flapping provider from
// returning a multi-megabyte response.
const maxRateLimitEvents = 500

// HourlySeries returns per-platform hourly buckets for one owner in [from, to).
func (s *ProviderUsageService) HourlySeries(ctx context.Context, ownerType string, ownerID int64, from, to time.Time) ([]ProviderHourBucket, error) {
	rows, err := s.q.ListOwnerProviderTokenHourly(ctx, store.ListOwnerProviderTokenHourlyParams{
		OwnerType:    ownerType,
		OwnerID:      ownerID,
		BucketHour:   from.UTC(),
		BucketHour_2: to.UTC(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ProviderHourBucket, len(rows))
	for i, r := range rows {
		out[i] = ProviderHourBucket{
			Hour:                r.BucketHour,
			ProviderID:          r.ProviderID,
			ProviderName:        r.ProviderName,
			ProviderPlatform:    r.ProviderPlatform,
			InputTokens:         r.InputTokens,
			OutputTokens:        r.OutputTokens,
			CacheCreationTokens: r.CacheCreationTokens,
			CacheReadTokens:     r.CacheReadTokens,
			InteractionCount:    r.InteractionCount,
		}
	}
	return out, nil
}

// Totals returns per-platform token totals for [from, to), each joined with its
// rate-limit tally for the same window.
func (s *ProviderUsageService) Totals(ctx context.Context, ownerType string, ownerID int64, from, to time.Time) ([]ProviderTotal, error) {
	rows, err := s.q.SumOwnerProviderTokens(ctx, store.SumOwnerProviderTokensParams{
		OwnerType:    ownerType,
		OwnerID:      ownerID,
		BucketHour:   from.UTC(),
		BucketHour_2: to.UTC(),
	})
	if err != nil {
		return nil, err
	}
	limits, err := s.rateLimitTally(ctx, ownerType, ownerID, from, to)
	if err != nil {
		return nil, err
	}

	out := make([]ProviderTotal, len(rows))
	for i, r := range rows {
		t := ProviderTotal{
			ProviderID:          r.ProviderID,
			ProviderName:        r.ProviderName,
			ProviderPlatform:    r.ProviderPlatform,
			InputTokens:         r.InputTokens,
			OutputTokens:        r.OutputTokens,
			CacheCreationTokens: r.CacheCreationTokens,
			CacheReadTokens:     r.CacheReadTokens,
			TotalTokens:         r.InputTokens + r.OutputTokens + r.CacheCreationTokens + r.CacheReadTokens,
			InteractionCount:    r.InteractionCount,
		}
		if l, ok := limits[r.ProviderID]; ok {
			t.RateLimitCount, t.BlockedSeconds, t.OpenCount = l.count, l.blockedSeconds, l.openCount
		}
		out[i] = t
	}

	// A platform can be throttled without having recorded consumption in the
	// window (e.g. its quota was already exhausted before the window opened, so
	// every turn 429'd). Those rows carry the most useful signal of all, so
	// append them instead of letting the token-table JOIN hide them.
	seen := make(map[int64]bool, len(out))
	for _, t := range out {
		seen[t.ProviderID] = true
	}
	for pid, l := range limits {
		if seen[pid] {
			continue
		}
		name, platform := s.providerLabel(ctx, pid)
		out = append(out, ProviderTotal{
			ProviderID:       pid,
			ProviderName:     name,
			ProviderPlatform: platform,
			RateLimitCount:   l.count,
			BlockedSeconds:   l.blockedSeconds,
			OpenCount:        l.openCount,
		})
	}
	return out, nil
}

// Episodes returns the owner's rate-limit episodes in [from, to), newest first.
func (s *ProviderUsageService) Episodes(ctx context.Context, ownerType string, ownerID int64, from, to time.Time) ([]RateLimitEpisode, error) {
	rows, err := s.q.ListOwnerProviderRateLimitEvents(ctx, store.ListOwnerProviderRateLimitEventsParams{
		OwnerType:     ownerType,
		OwnerID:       ownerID,
		TriggeredAt:   from.UTC(),
		TriggeredAt_2: to.UTC(),
		Limit:         maxRateLimitEvents,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RateLimitEpisode, len(rows))
	for i, r := range rows {
		e := RateLimitEpisode{
			ID:               r.ID,
			ProviderID:       r.ProviderID,
			ProviderName:     r.ProviderName,
			ProviderPlatform: r.ProviderPlatform,
			TriggeredAt:      r.TriggeredAt,
			ResetAt:          r.ResetAt,
			ClearedManually:  r.ClearedManually != 0,
			Detail:           r.Detail,
		}
		if r.WorkspaceID.Valid {
			id := r.WorkspaceID.Int64
			e.WorkspaceID = &id
		}
		if r.ResumedAt.Valid {
			resumed := r.ResumedAt.Time
			e.ResumedAt = &resumed
			if secs := int64(resumed.Sub(r.TriggeredAt).Seconds()); secs > 0 {
				e.BlockedSeconds = secs
			}
		}
		out[i] = e
	}
	return out, nil
}

// limitTally is the per-provider rate-limit aggregate built in Go rather than
// SQL: timestamp arithmetic differs between SQLite (strftime) and PostgreSQL
// (EXTRACT), and this store targets both.
type limitTally struct {
	count          int64
	blockedSeconds int64
	openCount      int64
}

func (s *ProviderUsageService) rateLimitTally(ctx context.Context, ownerType string, ownerID int64, from, to time.Time) (map[int64]limitTally, error) {
	spans, err := s.q.ListOwnerProviderRateLimitSpans(ctx, store.ListOwnerProviderRateLimitSpansParams{
		OwnerType:     ownerType,
		OwnerID:       ownerID,
		TriggeredAt:   from.UTC(),
		TriggeredAt_2: to.UTC(),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]limitTally, len(spans))
	for _, sp := range spans {
		t := out[sp.ProviderID]
		t.count++
		if sp.ResumedAt.Valid {
			// Guard against a negative span: a clock adjustment between the two
			// writes could otherwise subtract from the total blocked time.
			if secs := int64(sp.ResumedAt.Time.Sub(sp.TriggeredAt).Seconds()); secs > 0 {
				t.blockedSeconds += secs
			}
		} else {
			t.openCount++
		}
		out[sp.ProviderID] = t
	}
	return out, nil
}

// providerLabel resolves a provider's display name, falling back to its id when
// the row is gone (a deleted provider CASCADEs its rows away, so this is only
// reachable in a race).
func (s *ProviderUsageService) providerLabel(ctx context.Context, providerID int64) (name, platform string) {
	if p, err := s.q.GetEnvProvider(ctx, providerID); err == nil {
		return p.Name, p.Platform
	}
	return "", ""
}
