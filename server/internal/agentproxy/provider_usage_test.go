package agentproxy

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// newUsageTestProvider creates a standalone (ungrouped) enabled provider.
func newUsageTestProvider(t *testing.T, s *WorkspaceSession, name string) store.EnvProvider {
	t.Helper()
	p, err := s.q.CreateEnvProvider(context.Background(), store.CreateEnvProviderParams{
		Name: name, Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:" + name + "}", Model: "glm-5.1", Enabled: 1, OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider %s: %v", name, err)
	}
	return p
}

// TestRecordProviderTokens_AccumulatesIntoHourBucket verifies that two turns in
// the same hour add up in one bucket (rather than the second overwriting the
// first) and that interaction_count tracks the number of turns.
func TestRecordProviderTokens_AccumulatesIntoHourBucket(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	p := newUsageTestProvider(t, s, "智谱")
	at := time.Date(2026, 8, 24, 15, 30, 0, 0, time.UTC)

	recordProviderTokens(ctx, s.q, p.ID, "user", 7, at, 100, 20, 5, 50)
	// A later minute of the SAME hour must land in the same bucket.
	recordProviderTokens(ctx, s.q, p.ID, "user", 7, at.Add(20*time.Minute), 10, 2, 1, 3)

	rows, err := s.q.ListProviderTokenHourly(ctx, store.ListProviderTokenHourlyParams{
		ProviderID: p.ID, OwnerType: "user", OwnerID: 7,
		BucketHour:   at.Add(-time.Hour),
		BucketHour_2: at.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListProviderTokenHourly: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 hourly bucket, got %d", len(rows))
	}
	r := rows[0]
	if r.InputTokens != 110 || r.OutputTokens != 22 || r.CacheCreationTokens != 6 || r.CacheReadTokens != 53 {
		t.Errorf("tokens not accumulated: in=%d out=%d cc=%d cr=%d",
			r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens)
	}
	if r.InteractionCount != 2 {
		t.Errorf("interaction_count = %d, want 2", r.InteractionCount)
	}
	if !r.BucketHour.Equal(at.Truncate(time.Hour)) {
		t.Errorf("bucket_hour = %v, want %v", r.BucketHour, at.Truncate(time.Hour))
	}
}

// TestRecordProviderTokens_SkipsNoProviderAndEmptyTurns guards the two cases
// that must NOT create a row: a workspace on the host login (no provider bound)
// and a turn that reported no tokens (which would still bump interaction_count
// and imply quota use that never happened).
func TestRecordProviderTokens_SkipsNoProviderAndEmptyTurns(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	p := newUsageTestProvider(t, s, "智谱")
	at := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)

	recordProviderTokens(ctx, s.q, 0, "user", 7, at, 100, 20, 0, 0) // no provider bound
	recordProviderTokens(ctx, s.q, p.ID, "user", 7, at, 0, 0, 0, 0) // all-zero turn

	rows, err := s.q.ListProviderTokenHourly(ctx, store.ListProviderTokenHourlyParams{
		ProviderID: p.ID, OwnerType: "user", OwnerID: 7,
		BucketHour:   at.Add(-time.Hour),
		BucketHour_2: at.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListProviderTokenHourly: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no rows written, got %d", len(rows))
	}
}

// TestRecordProviderTokens_SeparatesOwners verifies the reason owner is in the
// PK: two owners sharing one seeded provider row must not have their
// consumption merged.
func TestRecordProviderTokens_SeparatesOwners(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	p := newUsageTestProvider(t, s, "智谱")
	at := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)

	recordProviderTokens(ctx, s.q, p.ID, "user", 1, at, 100, 0, 0, 0)
	recordProviderTokens(ctx, s.q, p.ID, "user", 2, at, 900, 0, 0, 0)

	for _, tc := range []struct {
		owner int64
		want  int64
	}{{1, 100}, {2, 900}} {
		rows, err := s.q.ListProviderTokenHourly(ctx, store.ListProviderTokenHourlyParams{
			ProviderID: p.ID, OwnerType: "user", OwnerID: tc.owner,
			BucketHour:   at.Add(-time.Hour),
			BucketHour_2: at.Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatalf("ListProviderTokenHourly owner %d: %v", tc.owner, err)
		}
		if len(rows) != 1 || rows[0].InputTokens != tc.want {
			t.Errorf("owner %d: got %+v, want single row with input=%d", tc.owner, rows, tc.want)
		}
	}
}

// TestProviderRateLimitEpisode_Lifecycle walks the full flow the UI reports on:
// a 429 opens an episode, a burst of further 429s does NOT open duplicates, a
// later reset extends the same row, and using the provider again closes it with
// a resumed_at that yields the blocked duration.
func TestProviderRateLimitEpisode_Lifecycle(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	p := newUsageTestProvider(t, s, "百炼")

	triggered := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	reset := triggered.Add(5 * time.Hour)
	openProviderRateLimitEvent(ctx, s.q, p.ID, s.workspaceID, "user", 7, triggered, reset, "429 quota exhausted")

	// Burst: same episode, must not create a second row.
	openProviderRateLimitEvent(ctx, s.q, p.ID, s.workspaceID, "user", 7, triggered.Add(time.Second), reset, "429 again")
	// Platform re-reports a LATER reset: same episode, extended.
	laterReset := reset.Add(2 * time.Hour)
	openProviderRateLimitEvent(ctx, s.q, p.ID, s.workspaceID, "user", 7, triggered.Add(2*time.Second), laterReset, "429 weekly quota")

	ev, err := s.q.GetOpenProviderRateLimitEvent(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetOpenProviderRateLimitEvent: %v", err)
	}
	if !ev.ResetAt.Equal(laterReset) {
		t.Errorf("reset_at = %v, want extended to %v", ev.ResetAt, laterReset)
	}
	if ev.ResumedAt.Valid {
		t.Error("episode should still be open")
	}

	spans, err := s.q.ListOwnerProviderRateLimitSpans(ctx, store.ListOwnerProviderRateLimitSpansParams{
		OwnerType: "user", OwnerID: 7,
		TriggeredAt:   triggered.Add(-time.Hour),
		TriggeredAt_2: triggered.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListOwnerProviderRateLimitSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("want exactly 1 episode after a 429 burst, got %d", len(spans))
	}

	// Provider used again 6h after the trigger -> episode closes, duration known.
	resumed := triggered.Add(6 * time.Hour)
	closeProviderRateLimitEvents(ctx, s.q, p.ID, resumed, false)

	if _, err := s.q.GetOpenProviderRateLimitEvent(ctx, p.ID); err == nil {
		t.Fatal("episode should be closed after the provider was used again")
	}
	spans, err = s.q.ListOwnerProviderRateLimitSpans(ctx, store.ListOwnerProviderRateLimitSpansParams{
		OwnerType: "user", OwnerID: 7,
		TriggeredAt:   triggered.Add(-time.Hour),
		TriggeredAt_2: triggered.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListOwnerProviderRateLimitSpans after resume: %v", err)
	}
	if len(spans) != 1 || !spans[0].ResumedAt.Valid {
		t.Fatalf("want 1 closed span, got %+v", spans)
	}
	if got := spans[0].ResumedAt.Time.Sub(spans[0].TriggeredAt); got != 6*time.Hour {
		t.Errorf("blocked duration = %v, want 6h", got)
	}
}

// TestMaybeMarkRateLimitedProvider_OpensEpisodeLog verifies the 429 detector
// writes the durable episode log (not just the mutable cooldown_until cell),
// with reset_at matching the time parsed out of the platform's message.
func TestMaybeMarkRateLimitedProvider_OpensEpisodeLog(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	p := newUsageTestProvider(t, s, "智谱-log")

	ws, err := s.q.GetWorkspace(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if err := s.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{
		ID: ws.ID, EnvProviderID: sql.NullInt64{Int64: p.ID, Valid: true},
	}); err != nil {
		t.Fatalf("SetWorkspaceEnvProvider: %v", err)
	}
	s.activeProviderID = p.ID

	resetAt := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	line := fmt.Sprintf(
		"✗ Error: API Error: Request rejected (429) · You have exceeded the 5-hour usage quota. It will reset at %s CST.",
		resetAt.Format("2006-01-02 15:04:05 -0700"))
	s.maybeMarkRateLimitedProvider(ctx, line, p.ID)

	ev, err := s.q.GetOpenProviderRateLimitEvent(ctx, p.ID)
	if err != nil {
		t.Fatalf("expected an open episode after a 429: %v", err)
	}
	if !ev.ResetAt.Equal(resetAt.UTC()) {
		t.Errorf("reset_at = %v, want %v (parsed from the 429 text)", ev.ResetAt, resetAt.UTC())
	}
	if ev.Detail == "" {
		t.Error("detail should carry an excerpt of the platform message")
	}
	// The episode is attributed to the consuming workspace's owner. The test
	// harness leaves session.ownerType empty, which normalizeOwnerType coerces to
	// 'user' so the row still satisfies the schema CHECK instead of being lost.
	if ev.OwnerType != normalizeOwnerType(s.ownerType) || ev.OwnerID != s.ownerID {
		t.Errorf("episode owner = %s:%d, want %s:%d",
			ev.OwnerType, ev.OwnerID, normalizeOwnerType(s.ownerType), s.ownerID)
	}
}

// TestTrimRateLimitDetail_BoundsAndRuneSafety checks the excerpt is collapsed to
// one line and truncated on a rune boundary (the platform messages are partly
// Chinese, so byte slicing could store invalid UTF-8).
func TestTrimRateLimitDetail_BoundsAndRuneSafety(t *testing.T) {
	if got := trimRateLimitDetail("  429  quota\n exhausted \t"); got != "429 quota exhausted" {
		t.Errorf("whitespace not collapsed: %q", got)
	}
	long := ""
	for i := 0; i < rateLimitDetailMax+50; i++ {
		long += "限"
	}
	got := trimRateLimitDetail(long)
	if r := []rune(got); len(r) != rateLimitDetailMax {
		t.Errorf("truncated to %d runes, want %d", len(r), rateLimitDetailMax)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("truncation split a multi-byte rune")
		}
	}
}
