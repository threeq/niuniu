package store

import (
	"context"
	"testing"
	"time"
)

// seedWorkspaceForStats inserts a workspace with the given owner and returns its id.
func seedWorkspaceForStats(t *testing.T, q *Queries, ownerType string, ownerID int64, name string) int64 {
	t.Helper()
	ws, err := q.CreateWorkspace(context.Background(), CreateWorkspaceParams{
		Name:      name,
		Path:      "/tmp/" + name,
		Status:    "created",
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return ws.ID
}

func TestWorkspaceTokenHourlyUpsertAccumulates(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	q := New(db)
	ctx := context.Background()
	wsID := seedWorkspaceForStats(t, q, "user", 1, "ws-acc")
	hour := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if err := q.UpsertWorkspaceTokenHourly(ctx, UpsertWorkspaceTokenHourlyParams{
			WorkspaceID:         wsID,
			BucketHour:          hour,
			InputTokens:         100,
			OutputTokens:        10,
			CacheCreationTokens: 5,
			CacheReadTokens:     50,
		}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	rows, err := q.ListWorkspaceTokenHourly(ctx, ListWorkspaceTokenHourlyParams{
		WorkspaceID:  wsID,
		BucketHour:   hour.Add(-time.Hour),
		BucketHour_2: hour.Add(time.Hour),
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %v rows=%d", err, len(rows))
	}
	if rows[0].InputTokens != 300 || rows[0].OutputTokens != 30 ||
		rows[0].CacheCreationTokens != 15 || rows[0].CacheReadTokens != 150 ||
		rows[0].InteractionCount != 3 {
		t.Fatalf("acc wrong: %+v", rows[0])
	}

	// A different hour lands in a separate bucket.
	if err := q.UpsertWorkspaceTokenHourly(ctx, UpsertWorkspaceTokenHourlyParams{
		WorkspaceID: wsID, BucketHour: hour.Add(time.Hour), InputTokens: 7,
	}); err != nil {
		t.Fatalf("upsert next hour: %v", err)
	}
	rows, _ = q.ListWorkspaceTokenHourly(ctx, ListWorkspaceTokenHourlyParams{
		WorkspaceID: wsID, BucketHour: hour.Add(-time.Hour), BucketHour_2: hour.Add(2 * time.Hour),
	})
	if len(rows) != 2 {
		t.Fatalf("want 2 buckets, got %d", len(rows))
	}
}

func TestListOwnerTokenHourlySumsAcrossWorkspaces(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	q := New(db)
	ctx := context.Background()
	hour := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	ws1 := seedWorkspaceForStats(t, q, "org", 42, "ws-o1")
	ws2 := seedWorkspaceForStats(t, q, "org", 42, "ws-o2")
	wsOther := seedWorkspaceForStats(t, q, "user", 9, "ws-other")

	for _, id := range []int64{ws1, ws2} {
		if err := q.UpsertWorkspaceTokenHourly(ctx, UpsertWorkspaceTokenHourlyParams{
			WorkspaceID: id, BucketHour: hour, InputTokens: 100, OutputTokens: 10,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// Different owner must not be counted.
	_ = q.UpsertWorkspaceTokenHourly(ctx, UpsertWorkspaceTokenHourlyParams{
		WorkspaceID: wsOther, BucketHour: hour, InputTokens: 999,
	})

	rows, err := q.ListOwnerTokenHourly(ctx, ListOwnerTokenHourlyParams{
		OwnerType: "org", OwnerID: 42,
		BucketHour: hour.Add(-time.Hour), BucketHour_2: hour.Add(time.Hour),
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("owner list: %v rows=%d", err, len(rows))
	}
	if rows[0].InputTokens != 200 || rows[0].OutputTokens != 20 || rows[0].InteractionCount != 2 {
		t.Fatalf("owner sum wrong: %+v", rows[0])
	}
}

func TestPruneRemovesOldBuckets(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	q := New(db)
	ctx := context.Background()
	wsID := seedWorkspaceForStats(t, q, "user", 1, "ws-prune")
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)

	_ = q.UpsertWorkspaceTokenHourly(ctx, UpsertWorkspaceTokenHourlyParams{WorkspaceID: wsID, BucketHour: old, InputTokens: 1})
	_ = q.UpsertWorkspaceTokenHourly(ctx, UpsertWorkspaceTokenHourlyParams{WorkspaceID: wsID, BucketHour: recent, InputTokens: 2})

	cutoff := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := q.PruneWorkspaceTokenHourly(ctx, cutoff); err != nil {
		t.Fatalf("prune: %v", err)
	}
	rows, _ := q.ListWorkspaceTokenHourly(ctx, ListWorkspaceTokenHourlyParams{
		WorkspaceID: wsID, BucketHour: old.Add(-time.Hour), BucketHour_2: recent.Add(time.Hour),
	})
	if len(rows) != 1 || rows[0].InputTokens != 2 {
		t.Fatalf("after prune want 1 recent row, got %+v", rows)
	}
}

func TestUpsertWorkspaceStatsAIAccumulatesAndOwnerStable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	q := New(db)
	ctx := context.Background()
	wsID := seedWorkspaceForStats(t, q, "org", 7, "ws-stats")

	for i := 0; i < 2; i++ {
		if err := q.UpsertWorkspaceStatsAI(ctx, UpsertWorkspaceStatsAIParams{
			WorkspaceID: wsID, OwnerType: "org", OwnerID: 7,
			TotalTurns: 3, TotalDurationMs: 1000,
			InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 5, CacheReadTokens: 50,
		}); err != nil {
			t.Fatalf("upsert ai %d: %v", i, err)
		}
	}
	if err := q.IncrWorkspaceStatsUserMessage(ctx, IncrWorkspaceStatsUserMessageParams{
		WorkspaceID: wsID, OwnerType: "org", OwnerID: 7,
	}); err != nil {
		t.Fatalf("incr user: %v", err)
	}

	st, err := q.GetWorkspaceStats(ctx, wsID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if st.AiMessageCount != 2 || st.UserMessageCount != 1 || st.InteractionCount != 2 {
		t.Fatalf("counts wrong: %+v", st)
	}
	if st.InputTokens != 200 || st.OutputTokens != 40 || st.CacheReadTokens != 100 {
		t.Fatalf("token totals wrong: %+v", st)
	}
	if st.TotalTurns != 6 || st.TotalDurationMs != 2000 {
		t.Fatalf("turns/duration wrong: %+v", st)
	}
	if st.OwnerType != "org" || st.OwnerID != 7 {
		t.Fatalf("owner not stable: %+v", st)
	}
}
