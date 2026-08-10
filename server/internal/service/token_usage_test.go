package service

import (
	"context"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func TestTokenUsageService_WorkspaceAndOwnerSeries(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)
	ctx := context.Background()

	mkWS := func(name string, ownerID int64) int64 {
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			Name: name, Path: "/tmp/" + name, Status: "created",
			OwnerType: "org", OwnerID: ownerID,
		})
		if err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
		return ws.ID
	}
	ws1 := mkWS("svc-ws1", 5)
	ws2 := mkWS("svc-ws2", 5)
	hour := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []int64{ws1, ws2} {
		if err := q.UpsertWorkspaceTokenHourly(ctx, store.UpsertWorkspaceTokenHourlyParams{
			WorkspaceID: id, BucketHour: hour, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	svc := NewTokenUsageService(q)
	from, to := hour.Add(-time.Hour), hour.Add(time.Hour)

	wsSeries, err := svc.WorkspaceSeries(ctx, ws1, from, to)
	if err != nil || len(wsSeries) != 1 {
		t.Fatalf("workspace series: %v len=%d", err, len(wsSeries))
	}
	if wsSeries[0].InputTokens != 100 || wsSeries[0].CacheReadTokens != 40 {
		t.Fatalf("workspace bucket wrong: %+v", wsSeries[0])
	}

	ownerSeries, err := svc.OwnerSeries(ctx, "org", 5, from, to)
	if err != nil || len(ownerSeries) != 1 {
		t.Fatalf("owner series: %v len=%d", err, len(ownerSeries))
	}
	if ownerSeries[0].InputTokens != 200 || ownerSeries[0].InteractionCount != 2 {
		t.Fatalf("owner bucket should sum both workspaces: %+v", ownerSeries[0])
	}
}
