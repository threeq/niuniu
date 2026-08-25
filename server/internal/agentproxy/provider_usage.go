package agentproxy

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Subscription-platform (env_providers) usage accounting.
//
// The existing workspace-grain tables answer "which workspace burned tokens".
// This file answers the platform-grain questions the user actually asks when
// juggling several subscriptions: how much did we spend ON 智谱 vs 百炼 this
// hour, how often does each platform throttle us, and how long were we stuck
// waiting for a quota reset.
//
// Two records are kept, both written best-effort — a failure here logs and is
// dropped, exactly like the workspace-grain writes at the result event, because
// losing an accounting row must never break a user's turn:
//
//   provider_token_hourly      — additive hourly token buckets per platform
//   provider_rate_limit_events — one row per 429 episode, with the timeline
//                                triggered_at -> reset_at -> resumed_at

// rateLimitDetailMax bounds the stored excerpt of a platform's 429 text. The
// raw line can carry a long request id and a full marketing sentence; the
// leading portion is what identifies WHICH quota tripped (5-hour / weekly /
// token-plan), which is all the UI needs.
const rateLimitDetailMax = 300

// normalizeOwnerType coerces an owner type to a value the schema's CHECK
// constraint accepts. Production sessions always carry 'user' or 'org' (copied
// from workspaces.owner_type, itself NOT NULL DEFAULT 'user'), but an empty
// string would otherwise fail the INSERT and silently drop the accounting row —
// the one failure mode this file must not have, since the write is best-effort
// and its error only reaches a log line.
func normalizeOwnerType(ownerType string) string {
	if ownerType == "org" {
		return "org"
	}
	return "user"
}

// recordProviderTokens adds one turn's token counts to the platform's hourly
// bucket. providerID is the provider the emitting process was spawned with;
// 0 means the workspace runs on the host login (no subscription platform
// bound), in which case there is no platform to attribute and we skip.
//
// The owner is the CONSUMING workspace's owner, not the provider row's: the
// seeded default providers are shared (owner_id = 0), so attributing to the
// provider's owner would collapse every user's consumption into one series.
func recordProviderTokens(
	ctx context.Context,
	q *store.Queries,
	providerID int64,
	ownerType string,
	ownerID int64,
	at time.Time,
	inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int,
) {
	if q == nil || providerID <= 0 {
		return
	}
	// A turn that reported no tokens at all carries no information; writing it
	// would still bump interaction_count and imply activity that did not
	// consume quota.
	if inputTokens == 0 && outputTokens == 0 && cacheCreationTokens == 0 && cacheReadTokens == 0 {
		return
	}
	if err := q.UpsertProviderTokenHourly(ctx, store.UpsertProviderTokenHourlyParams{
		ProviderID:          providerID,
		OwnerType:           normalizeOwnerType(ownerType),
		OwnerID:             ownerID,
		BucketHour:          at.UTC().Truncate(time.Hour),
		InputTokens:         int64(inputTokens),
		OutputTokens:        int64(outputTokens),
		CacheCreationTokens: int64(cacheCreationTokens),
		CacheReadTokens:     int64(cacheReadTokens),
	}); err != nil {
		slog.Warn("UpsertProviderTokenHourly failed",
			"provider_id", providerID, "owner_type", ownerType, "owner_id", ownerID, "error", err)
	}
}

// openProviderRateLimitEvent records that a platform just throttled us.
//
// Idempotent per episode: while the provider still has an unresumed row, a
// further 429 line does not open a second row. That matters because the CLI
// emits 429 text in bursts and because a platform can re-report the same quota
// with a later reset — both are one episode. A later reset extends the open
// row; an earlier or equal one is ignored.
func openProviderRateLimitEvent(
	ctx context.Context,
	q *store.Queries,
	providerID, workspaceID int64,
	ownerType string,
	ownerID int64,
	triggeredAt, resetAt time.Time,
	detail string,
) {
	if q == nil || providerID <= 0 {
		return
	}
	detail = trimRateLimitDetail(detail)

	if open, err := q.GetOpenProviderRateLimitEvent(ctx, providerID); err == nil {
		if resetAt.After(open.ResetAt) {
			if err := q.ExtendProviderRateLimitEvent(ctx, store.ExtendProviderRateLimitEventParams{
				ResetAt: resetAt.UTC(),
				Detail:  detail,
				ID:      open.ID,
			}); err != nil {
				slog.Warn("ExtendProviderRateLimitEvent failed",
					"provider_id", providerID, "event_id", open.ID, "error", err)
			}
		}
		return
	}

	if _, err := q.CreateProviderRateLimitEvent(ctx, store.CreateProviderRateLimitEventParams{
		ProviderID:  providerID,
		WorkspaceID: sql.NullInt64{Int64: workspaceID, Valid: workspaceID > 0},
		OwnerType:   normalizeOwnerType(ownerType),
		OwnerID:     ownerID,
		// UTC: modernc sqlite writes a non-UTC time.Time as an unparseable
		// "... +0800 +0800" string (same trap as sceneenv.MarkProviderCooldown).
		TriggeredAt: triggeredAt.UTC(),
		ResetAt:     resetAt.UTC(),
		Detail:      detail,
	}); err != nil {
		slog.Warn("CreateProviderRateLimitEvent failed",
			"provider_id", providerID, "workspaceID", workspaceID, "error", err)
	}
}

// closeProviderRateLimitEvents stamps resumed_at on every open episode for a
// provider, i.e. records the moment the platform was actually USED again.
//
// This is deliberately not "when reset_at passed": the quota window expiring
// only makes the provider eligible, and the user may not run a turn for hours
// after that. The gap between reset_at and resumed_at is exactly the idle time
// the issue asks to measure, so it must come from a real spawn, not a clock.
//
// manual=true marks the rows where the user cut the cooldown short from the
// provider list instead of waiting for the platform's reset.
func closeProviderRateLimitEvents(ctx context.Context, q *store.Queries, providerID int64, at time.Time, manual bool) {
	if q == nil || providerID <= 0 {
		return
	}
	var flag int64
	if manual {
		flag = 1
	}
	if err := q.ResumeProviderRateLimitEvents(ctx, store.ResumeProviderRateLimitEventsParams{
		ResumedAt:       sql.NullTime{Time: at.UTC(), Valid: true},
		ClearedManually: flag,
		ProviderID:      providerID,
	}); err != nil {
		slog.Warn("ResumeProviderRateLimitEvents failed", "provider_id", providerID, "error", err)
	}
}

// trimRateLimitDetail collapses a raw 429 output line into a single-line
// excerpt bounded by rateLimitDetailMax. Truncation is rune-aware: the
// platform messages are partly Chinese, and slicing bytes could split a
// multi-byte rune and store invalid UTF-8.
func trimRateLimitDetail(line string) string {
	s := strings.Join(strings.Fields(line), " ")
	if r := []rune(s); len(r) > rateLimitDetailMax {
		return string(r[:rateLimitDetailMax])
	}
	return s
}
