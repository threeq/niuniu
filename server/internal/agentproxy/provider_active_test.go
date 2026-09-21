package agentproxy

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
)

// TestRecordActiveProvider_PersistsBoundName verifies the spawn-time record:
// the workspace row carries the name of the provider ActiveProvider resolves.
func TestRecordActiveProvider_PersistsBoundName(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	bound, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider: %v", err)
	}
	if err := s.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{ID: s.workspaceID, EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true}}); err != nil {
		t.Fatalf("SetWorkspaceEnvProvider: %v", err)
	}

	s.recordActiveProvider(ctx)

	ws, err := s.q.GetWorkspace(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.ActiveEnvProviderName != "智谱-1" {
		t.Errorf("active provider name = %q, want 智谱-1", ws.ActiveEnvProviderName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProviderID != bound.ID {
		t.Errorf("activeProviderID = %d, want %d", s.activeProviderID, bound.ID)
	}
}

// TestRecordActiveProvider_FollowsGroupFallback verifies the record follows
// the group's healthy member: once the bound member is rate-limited, the next
// spawn-time record points at the fallback — each workspace independently
// re-aligns at its own process starts.
func TestRecordActiveProvider_FollowsGroupFallback(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	bound, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider bound: %v", err)
	}
	if _, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-2", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-2}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 0,
	}); err != nil {
		t.Fatalf("CreateEnvProvider fallback: %v", err)
	}
	if err := s.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{ID: s.workspaceID, EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true}}); err != nil {
		t.Fatalf("SetWorkspaceEnvProvider: %v", err)
	}
	resetAt := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	if err := sceneenv.MarkProviderCooldown(ctx, s.q, bound.ID, resetAt); err != nil {
		t.Fatalf("MarkProviderCooldown: %v", err)
	}

	s.recordActiveProvider(ctx)

	ws, err := s.q.GetWorkspace(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.ActiveEnvProviderName != "智谱-2" {
		t.Errorf("after cooldown the record must follow the fallback: %q", ws.ActiveEnvProviderName)
	}
}
