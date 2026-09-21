package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// TestAgentManagerRecordActiveProvider covers the PTY spawn path's per-workspace
// provider record (same semantics as the agentproxy chat path): with a provider
// bound on a real-DB workspace the row carries the resolved name; with no
// binding it records an empty name. The hub is nil, and the broadcast must
// tolerate that.
func TestAgentManagerRecordActiveProvider(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	dataDir := t.TempDir()
	m := NewAgentManager(q, &config.AgentConfig{})

	t.Run("bound provider name is persisted", func(t *testing.T) {
		ws := createTestWorkspace(t, db, dataDir)
		bound, err := q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
			Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
			ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 1,
		})
		require.NoError(t, err)
		require.NoError(t, q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{
			ID:            ws.ID,
			EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true},
		}))

		m.recordActiveProvider(ctx, ws.ID, ws.OwnerType, ws.OwnerID)

		got, err := q.GetWorkspace(ctx, ws.ID)
		require.NoError(t, err)
		require.Equal(t, "智谱-1", got.ActiveEnvProviderName)
	})

	t.Run("no binding records empty name", func(t *testing.T) {
		ws := createTestWorkspace(t, db, dataDir)

		m.recordActiveProvider(ctx, ws.ID, ws.OwnerType, ws.OwnerID)

		got, err := q.GetWorkspace(ctx, ws.ID)
		require.NoError(t, err)
		require.Empty(t, got.ActiveEnvProviderName)
	})
}
