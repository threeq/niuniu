package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func newModelUsageRow(t *testing.T, q *store.Queries, p store.UpsertModelUsageHourlyParams) {
	t.Helper()
	require.NoError(t, q.UpsertModelUsageHourly(context.Background(), p))
}

func TestModelUsageSummary(t *testing.T) {
	q := store.New(setupTestDB(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	base := store.UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: 1, ProjectID: 2, WorkspaceID: 3,
		AgentCli: "claude", ModelName: "glm-5.1", ProviderID: 4,
		BucketHour: at, InteractionCount: 1,
	}
	a := base
	a.InputTokens, a.OutputTokens = 100, 20
	newModelUsageRow(t, q, a)

	b := base
	b.WorkspaceID, b.ModelName, b.AgentCli, b.ProjectID = 5, "gpt-5", "codex", 6
	b.InputTokens, b.CacheReadTokens, b.InteractionCount = 200, 50, 2
	newModelUsageRow(t, q, b)

	// Another owner must never leak into the summary.
	other := base
	other.OwnerID, other.InteractionCount = 2, 9
	other.InputTokens = 999
	newModelUsageRow(t, q, other)

	svc := NewModelUsageService(q)
	from, to := at.Add(-time.Hour), at.Add(time.Hour)

	totals, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, from, to, "")
	require.NoError(t, err)
	require.EqualValues(t, 370, totals.TotalTokens)
	require.EqualValues(t, 3, totals.Requests)
	require.Empty(t, totals.ByModel)

	byModel, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, from, to, "model")
	require.NoError(t, err)
	require.Len(t, byModel.ByModel, 2)
	require.Equal(t, "gpt-5", byModel.ByModel[0].Key) // ordered by tokens desc
	require.EqualValues(t, 250, byModel.ByModel[0].TotalTokens)
	require.EqualValues(t, 370, byModel.TotalTokens)

	byCLI, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, from, to, "cli")
	require.NoError(t, err)
	require.Len(t, byCLI.ByCLI, 2)

	byProject, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, from, to, "project")
	require.NoError(t, err)
	require.Len(t, byProject.ByProject, 2)

	byWorkspace, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, from, to, "workspace")
	require.NoError(t, err)
	require.Len(t, byWorkspace.ByWorkspace, 2)

	byProvider, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, from, to, "provider")
	require.NoError(t, err)
	require.Len(t, byProvider.ByProvider, 1)
	require.EqualValues(t, 370, byProvider.ByProvider[0].TotalTokens)
}

func TestModelUsageSummary_TimeWindowAndValidation(t *testing.T) {
	q := store.New(setupTestDB(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	newModelUsageRow(t, q, store.UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: 1, BucketHour: at, InputTokens: 100, InteractionCount: 1,
	})

	svc := NewModelUsageService(q)
	outside, err := svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, at.Add(24*time.Hour), at.Add(48*time.Hour), "")
	require.NoError(t, err)
	require.EqualValues(t, 0, outside.TotalTokens)
	require.EqualValues(t, 0, outside.Requests)

	_, err = svc.Summary(ctx, OwnerRef{Type: "user", ID: 0}, at, at.Add(time.Hour), "")
	require.Error(t, err)

	_, err = svc.Summary(ctx, OwnerRef{Type: "user", ID: 1}, at, at.Add(time.Hour), "bogus")
	require.Error(t, err)
}
