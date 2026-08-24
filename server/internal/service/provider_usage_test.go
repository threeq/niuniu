package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// seedProviderUsageDB builds an in-memory DB with the production schema plus one
// env provider, and returns the queries handle and that provider's id.
func seedProviderUsageDB(t *testing.T) (*store.Queries, int64) {
	t.Helper()
	q := store.New(setupTestDB(t))
	p, err := q.CreateEnvProvider(context.Background(), store.CreateEnvProviderParams{
		Name: "智谱", Platform: "zhipu",
		BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey:   "${ACCOUNT:智谱}", Model: "glm-5.1", Enabled: 1,
		OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider: %v", err)
	}
	return q, p.ID
}

// TestProviderUsage_HourlySeriesAndTotals verifies the read path the UI uses:
// hourly buckets come back per (hour, platform), and Totals sums them with
// total_tokens precomputed.
func TestProviderUsage_HourlySeriesAndTotals(t *testing.T) {
	ctx := context.Background()
	q, providerID := seedProviderUsageDB(t)
	svc := NewProviderUsageService(q)

	h1 := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	h2 := h1.Add(time.Hour)
	for _, b := range []struct {
		hour                    time.Time
		in, out, cacheC, cacheR int64
	}{
		{h1, 100, 10, 5, 20},
		{h2, 200, 30, 0, 40},
	} {
		if err := q.UpsertProviderTokenHourly(ctx, store.UpsertProviderTokenHourlyParams{
			ProviderID: providerID, OwnerType: "user", OwnerID: 1, BucketHour: b.hour,
			InputTokens: b.in, OutputTokens: b.out,
			CacheCreationTokens: b.cacheC, CacheReadTokens: b.cacheR,
		}); err != nil {
			t.Fatalf("UpsertProviderTokenHourly: %v", err)
		}
	}

	from, to := h1.Add(-time.Hour), h2.Add(time.Hour)
	buckets, err := svc.HourlySeries(ctx, "user", 1, from, to)
	if err != nil {
		t.Fatalf("HourlySeries: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("want 2 hourly buckets, got %d", len(buckets))
	}
	// Ordered by hour, and labelled with the platform name via the JOIN.
	if !buckets[0].Hour.Equal(h1) || buckets[0].ProviderName != "智谱" {
		t.Errorf("first bucket = %+v, want hour %v named 智谱", buckets[0], h1)
	}
	if buckets[1].InputTokens != 200 {
		t.Errorf("second bucket input = %d, want 200", buckets[1].InputTokens)
	}

	totals, err := svc.Totals(ctx, "user", 1, from, to)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("want 1 platform total, got %d", len(totals))
	}
	got := totals[0]
	if got.InputTokens != 300 || got.OutputTokens != 40 {
		t.Errorf("totals in=%d out=%d, want 300/40", got.InputTokens, got.OutputTokens)
	}
	// 300 + 40 + 5 + 60
	if got.TotalTokens != 405 {
		t.Errorf("total_tokens = %d, want 405", got.TotalTokens)
	}
	if got.InteractionCount != 2 {
		t.Errorf("interaction_count = %d, want 2", got.InteractionCount)
	}

	// A different owner must not see this consumption (owner is in the PK
	// precisely so shared seeded providers stay separated).
	other, err := svc.Totals(ctx, "user", 99, from, to)
	if err != nil {
		t.Fatalf("Totals other owner: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("owner 99 should see nothing, got %+v", other)
	}

	// Range filtering: a window ending before the second bucket excludes it.
	narrow, err := svc.Totals(ctx, "user", 1, from, h2)
	if err != nil {
		t.Fatalf("Totals narrow: %v", err)
	}
	if len(narrow) != 1 || narrow[0].InputTokens != 100 {
		t.Errorf("narrow window = %+v, want only the first bucket (input 100)", narrow)
	}
}

// TestProviderUsage_EpisodesAndBlockedTime covers the rate-limit reporting: a
// closed episode reports its blocked duration, an open one reports none and is
// counted as still-limited.
func TestProviderUsage_EpisodesAndBlockedTime(t *testing.T) {
	ctx := context.Background()
	q, providerID := seedProviderUsageDB(t)
	svc := NewProviderUsageService(q)

	base := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	// Closed episode: throttled at base, used again 2h later.
	closed, err := q.CreateProviderRateLimitEvent(ctx, store.CreateProviderRateLimitEventParams{
		ProviderID: providerID, OwnerType: "user", OwnerID: 1,
		WorkspaceID: sql.NullInt64{},
		TriggeredAt: base, ResetAt: base.Add(90 * time.Minute), Detail: "5-hour quota",
	})
	if err != nil {
		t.Fatalf("CreateProviderRateLimitEvent closed: %v", err)
	}
	if err := q.ResumeProviderRateLimitEvents(ctx, store.ResumeProviderRateLimitEventsParams{
		ResumedAt:  sql.NullTime{Time: base.Add(2 * time.Hour), Valid: true},
		ProviderID: providerID,
	}); err != nil {
		t.Fatalf("ResumeProviderRateLimitEvents: %v", err)
	}
	// Open episode, later the same day.
	if _, err := q.CreateProviderRateLimitEvent(ctx, store.CreateProviderRateLimitEventParams{
		ProviderID: providerID, OwnerType: "user", OwnerID: 1,
		TriggeredAt: base.Add(5 * time.Hour), ResetAt: base.Add(10 * time.Hour),
		Detail: "weekly quota",
	}); err != nil {
		t.Fatalf("CreateProviderRateLimitEvent open: %v", err)
	}

	from, to := base.Add(-time.Hour), base.Add(24*time.Hour)
	events, err := svc.Episodes(ctx, "user", 1, from, to)
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(events))
	}
	// Newest first.
	if events[0].ResumedAt != nil {
		t.Error("newest episode should still be open (resumed_at nil)")
	}
	if events[0].BlockedSeconds != 0 {
		t.Errorf("open episode blocked_seconds = %d, want 0", events[0].BlockedSeconds)
	}
	older := events[1]
	if older.ID != closed.ID {
		t.Errorf("second episode id = %d, want the closed one %d", older.ID, closed.ID)
	}
	if older.ResumedAt == nil {
		t.Fatal("closed episode should carry resumed_at")
	}
	if older.BlockedSeconds != int64((2 * time.Hour).Seconds()) {
		t.Errorf("closed episode blocked_seconds = %d, want 7200", older.BlockedSeconds)
	}
	if older.ProviderName != "智谱" {
		t.Errorf("provider_name = %q, want 智谱", older.ProviderName)
	}

	// Totals must surface the tally even though this provider recorded NO tokens
	// in the window — a platform throttled before the window opened is exactly
	// the case a token-table-only JOIN would hide.
	totals, err := svc.Totals(ctx, "user", 1, from, to)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("want the throttled platform listed with no consumption, got %+v", totals)
	}
	got := totals[0]
	if got.TotalTokens != 0 {
		t.Errorf("total_tokens = %d, want 0", got.TotalTokens)
	}
	if got.RateLimitCount != 2 {
		t.Errorf("rate_limit_count = %d, want 2", got.RateLimitCount)
	}
	if got.OpenCount != 1 {
		t.Errorf("open_count = %d, want 1", got.OpenCount)
	}
	// Only the CLOSED episode contributes duration.
	if got.BlockedSeconds != int64((2 * time.Hour).Seconds()) {
		t.Errorf("blocked_seconds = %d, want 7200 (closed episode only)", got.BlockedSeconds)
	}
	if got.ProviderName != "智谱" {
		t.Errorf("provider_name = %q, want 智谱", got.ProviderName)
	}
}

// TestProviderUsage_ClearCooldownClosesEpisode verifies the manual "clear
// cooldown" action ends the open episode and flags it, instead of leaving it
// open (which would overstate blocked time until the next spawn).
func TestProviderUsage_ClearCooldownClosesEpisode(t *testing.T) {
	ctx := context.Background()
	q, providerID := seedProviderUsageDB(t)

	if _, err := q.CreateProviderRateLimitEvent(ctx, store.CreateProviderRateLimitEventParams{
		ProviderID: providerID, OwnerType: "user", OwnerID: 1,
		TriggeredAt: time.Now().UTC().Add(-time.Hour),
		ResetAt:     time.Now().UTC().Add(4 * time.Hour),
		Detail:      "5-hour quota",
	}); err != nil {
		t.Fatalf("CreateProviderRateLimitEvent: %v", err)
	}

	svc := &EnvProviderService{q: q}
	if err := svc.ClearCooldown(ctx, providerID); err != nil {
		t.Fatalf("ClearCooldown: %v", err)
	}

	if _, err := q.GetOpenProviderRateLimitEvent(ctx, providerID); err == nil {
		t.Fatal("episode should be closed after a manual cooldown clear")
	}
	events, err := NewProviderUsageService(q).Episodes(ctx, "user", 1,
		time.Now().UTC().Add(-24*time.Hour), time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 episode, got %d", len(events))
	}
	if !events[0].ClearedManually {
		t.Error("episode should be flagged cleared_manually")
	}
	if events[0].ResumedAt == nil {
		t.Error("manually cleared episode should carry resumed_at")
	}
}
