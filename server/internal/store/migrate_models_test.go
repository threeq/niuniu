package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelsSchemaFreshAndRepeatable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)

	for _, tbl := range []string{"models", "model_usage_hourly", "model_budget_settings"} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		require.Equal(t, 1, n, "table %s missing", tbl)
	}
	ok, err := columnExists(db, "model_usage_hourly", "estimated")
	require.NoError(t, err)
	require.True(t, ok)
}

// TestUpsertModelUsageHourlyAccumulates exercises the ON CONFLICT path over the
// eight-column unique key: repeated writes in the same hour must add tokens and
// interactions, and estimated must stick once set.
func TestUpsertModelUsageHourlyAccumulates(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	q := New(db)
	ctx := context.Background()
	hour := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	first := UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: 1, ProjectID: 2, WorkspaceID: 3,
		AgentCli: "claude", ModelName: "glm-5.1", ProviderID: 4,
		BucketHour:   hour,
		InputTokens:  10, OutputTokens: 5, CacheCreationTokens: 2, CacheReadTokens: 1,
		InteractionCount: 1, Estimated: 0,
	}
	require.NoError(t, q.UpsertModelUsageHourly(ctx, first))

	second := first
	second.InputTokens = 20
	second.OutputTokens = 0
	second.CacheCreationTokens = 0
	second.CacheReadTokens = 0
	second.InteractionCount = 3
	second.Estimated = 1
	require.NoError(t, q.UpsertModelUsageHourly(ctx, second))

	var input, output, cc, cr, interactions, estimated int64
	require.NoError(t, db.QueryRow(
		`SELECT input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
		        interaction_count, estimated
		 FROM model_usage_hourly WHERE owner_id = 1`).
		Scan(&input, &output, &cc, &cr, &interactions, &estimated))
	require.Equal(t, int64(30), input)
	require.Equal(t, int64(5), output)
	require.Equal(t, int64(2), cc)
	require.Equal(t, int64(1), cr)
	require.Equal(t, int64(4), interactions)
	require.Equal(t, int64(1), estimated)

	total, err := q.SumModelUsageByOwner(ctx, SumModelUsageByOwnerParams{
		OwnerType: "user", OwnerID: 1,
		BucketHour: hour, BucketHour_2: hour.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, int64(38), total)
}
