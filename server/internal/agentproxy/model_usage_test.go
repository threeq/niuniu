package agentproxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func modelUsageSum(t *testing.T, s *WorkspaceSession, ownerID int64, from, to time.Time) int64 {
	t.Helper()
	total, err := s.q.SumModelUsageByOwner(context.Background(), store.SumModelUsageByOwnerParams{
		OwnerType:    "user",
		OwnerID:      ownerID,
		BucketHour:   from,
		BucketHour_2: to,
	})
	require.NoError(t, err)
	return total
}

func TestRecordModelUsage(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	recordModelUsage(ctx, s.q, modelUsageParams{
		OwnerType: "user", OwnerID: 1, ProjectID: 2, WorkspaceID: s.workspaceID,
		AgentCLI: "claude", ModelName: "glm-5.1", ProviderID: 4,
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5,
		InteractionCount: 1, At: at,
	})

	total := modelUsageSum(t, s, 1, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	require.EqualValues(t, 125, total)
}

// A turn that reported no usage still counts as one request, flagged estimated
// so the dashboard can present the window as a floor rather than a measurement.
func TestRecordModelUsage_NoTokensMarksEstimated(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	recordModelUsage(ctx, s.q, modelUsageParams{
		OwnerType: "user", OwnerID: 1, WorkspaceID: s.workspaceID,
		AgentCLI: "codex", ModelName: "gpt-5", InteractionCount: 3, At: at,
	})

	var estimated, interactions int64
	err := s.db.QueryRowContext(ctx,
		`SELECT estimated, interaction_count FROM model_usage_hourly WHERE owner_id = 1`).
		Scan(&estimated, &interactions)
	require.NoError(t, err)
	require.EqualValues(t, 1, estimated)
	require.EqualValues(t, 3, interactions)
	require.EqualValues(t, 0, modelUsageSum(t, s, 1, at.Add(-time.Hour), at.Add(time.Hour)))
}

// Two turns in the same hour, same dimensions, accumulate into one bucket.
func TestRecordModelUsage_AccumulatesIntoHourBucket(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 10, 5, 0, 0, time.UTC)
	p := modelUsageParams{
		OwnerType: "user", OwnerID: 1, WorkspaceID: s.workspaceID,
		AgentCLI: "claude", ModelName: "glm-5.1", ProviderID: 4, At: at,
	}
	p.InputTokens, p.InteractionCount = 100, 1
	recordModelUsage(ctx, s.q, p)
	p.InputTokens, p.InteractionCount = 10, 1
	p.At = at.Add(41 * time.Minute) // same hour
	recordModelUsage(ctx, s.q, p)

	rows, err := s.q.ListModelUsageByModel(ctx, store.ListModelUsageByModelParams{
		OwnerType: "user", OwnerID: 1,
		BucketHour: at.Add(-time.Hour), BucketHour_2: at.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 110, rows[0].TotalTokens)
	require.EqualValues(t, 2, rows[0].Requests)

	var buckets int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM model_usage_hourly WHERE owner_id = 1`).Scan(&buckets)
	require.NoError(t, err)
	require.Equal(t, 1, buckets)
}

// Every grouping dimension must resolve to the same underlying row.
func TestRecordModelUsage_Dimensions(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	from, to := at.Add(-time.Hour), at.Add(time.Hour)

	recordModelUsage(ctx, s.q, modelUsageParams{
		OwnerType: "user", OwnerID: 1, ProjectID: 2, WorkspaceID: s.workspaceID,
		AgentCLI: "qwen", ModelName: "qwen3-coder", ProviderID: 4,
		InputTokens: 100, OutputTokens: 20, InteractionCount: 2, At: at,
	})

	byModel, err := s.q.ListModelUsageByModel(ctx, store.ListModelUsageByModelParams{
		OwnerType: "user", OwnerID: 1, BucketHour: from, BucketHour_2: to,
	})
	require.NoError(t, err)
	require.Len(t, byModel, 1)
	require.Equal(t, "qwen3-coder", byModel[0].ModelName)

	byCLI, err := s.q.ListModelUsageByCLI(ctx, store.ListModelUsageByCLIParams{
		OwnerType: "user", OwnerID: 1, BucketHour: from, BucketHour_2: to,
	})
	require.NoError(t, err)
	require.Len(t, byCLI, 1)
	require.Equal(t, "qwen", byCLI[0].AgentCli)

	byProject, err := s.q.ListModelUsageByProject(ctx, store.ListModelUsageByProjectParams{
		OwnerType: "user", OwnerID: 1, BucketHour: from, BucketHour_2: to,
	})
	require.NoError(t, err)
	require.Len(t, byProject, 1)
	require.EqualValues(t, 2, byProject[0].ProjectID)

	byWorkspace, err := s.q.ListModelUsageByWorkspace(ctx, store.ListModelUsageByWorkspaceParams{
		OwnerType: "user", OwnerID: 1, BucketHour: from, BucketHour_2: to,
	})
	require.NoError(t, err)
	require.Len(t, byWorkspace, 1)
	require.EqualValues(t, s.workspaceID, byWorkspace[0].WorkspaceID)

	byProvider, err := s.q.ListModelUsageByProvider(ctx, store.ListModelUsageByProviderParams{
		OwnerType: "user", OwnerID: 1, BucketHour: from, BucketHour_2: to,
	})
	require.NoError(t, err)
	require.Len(t, byProvider, 1)
	require.EqualValues(t, 4, byProvider[0].ProviderID)
}

// The model name falls back to the bound provider's configured model, and to
// "unknown" only when neither the session override nor the provider has one.
func TestResolveUsageModelName(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()

	require.Equal(t, "unknown", s.resolveUsageModelName(ctx, 0), "no provider, no override")

	p := newUsageTestProvider(t, s, "智谱")
	require.Equal(t, "glm-5.1", s.resolveUsageModelName(ctx, p.ID), "provider model field")

	codex, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "codex-prov", Platform: "openai", BaseUrls: `{}`, ApiKey: "${ACCOUNT:x}",
		Model: "ignored-for-codex", CodexModel: "gpt-5-codex", Enabled: 1, OwnerType: "user", OwnerID: 0,
	})
	require.NoError(t, err)
	s.cliType = "codex"
	require.Equal(t, "gpt-5-codex", s.resolveUsageModelName(ctx, codex.ID), "codex reads codex_model")

	s.modelName = "explicit-model"
	require.Equal(t, "explicit-model", s.resolveUsageModelName(ctx, p.ID), "session override wins")
}

// recordTurnModelUsage wires the session fields (owner/project/workspace/cli)
// into the row, and a turn with no token report still counts one request.
func TestRecordTurnModelUsage_UsesSessionFields(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()
	s.ownerType, s.ownerID = "user", 9
	s.projectID = 7
	s.cliType = "claude"
	s.modelName = "glm-5.1"

	s.recordTurnModelUsage(ctx, 0, 0, 0, 0, 0, 0)

	rows, err := s.q.ListModelUsageByProject(ctx, store.ListModelUsageByProjectParams{
		OwnerType: "user", OwnerID: 9,
		BucketHour: time.Now().UTC().Add(-2 * time.Hour), BucketHour_2: time.Now().UTC().Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 7, rows[0].ProjectID)
	require.EqualValues(t, 1, rows[0].Requests)
}
