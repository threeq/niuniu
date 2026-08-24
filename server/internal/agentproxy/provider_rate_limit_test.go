package agentproxy

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestMaybeMarkRateLimitedProvider_MarksAndRestartsOnFallback verifies that a
// 429 quota line in the agent output marks the session's provider with the
// parsed reset time and — because a healthy same-group fallback exists — flags
// the failed turn so the next spawn restarts with the fallback.
func TestMaybeMarkRateLimitedProvider_MarksAndRestartsOnFallback(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	q := s.q
	bound, err := q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱", OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider bound: %v", err)
	}
	if _, err := q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-2", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-2}", Model: "glm-5.1", GroupName: "智谱", OwnerType: "user", OwnerID: 0,
	}); err != nil {
		t.Fatalf("CreateEnvProvider fallback: %v", err)
	}
	// Bind the workspace (newDispatchTestSession already created one) to the
	// bound provider.
	ws, err := q.GetWorkspace(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if err := q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{ID: ws.ID, EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true}}); err != nil {
		t.Fatalf("SetWorkspaceEnvProvider: %v", err)
	}
	s.activeProviderID = bound.ID // session was spawned with the bound provider

	// A real 智谱-style 429 message with a reset time ~5h in the future (must be
	// future for the cooldown to be considered active). Second precision — the
	// parsed message carries no sub-second part.
	resetAt := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	line := fmt.Sprintf(
		"✗ Error: API Error: Request rejected (429) · You have exceeded the 5-hour usage quota. It will reset at %s CST.",
		resetAt.Format("2006-01-02 15:04:05 -0700"))
	s.maybeMarkRateLimitedProvider(ctx, line)

	got, err := q.GetEnvProvider(ctx, bound.ID)
	if err != nil {
		t.Fatalf("GetEnvProvider bound: %v", err)
	}
	if !got.CooldownUntil.Valid {
		t.Fatal("bound provider should be marked in cooldown")
	}
	// Stored normalized to UTC (see MarkProviderCooldown); compare instants.
	if !got.CooldownUntil.Time.Equal(resetAt) {
		t.Errorf("cooldown until = %v, want %v", got.CooldownUntil.Time, resetAt)
	}

	s.mu.Lock()
	terr := s.lastTurnError
	s.mu.Unlock()
	if !terr {
		t.Error("expected lastTurnError=true so the session restarts onto the fallback")
	}
}

// TestMaybeMarkRateLimitedProvider_NoGroupMarksButNoKill verifies a standalone
// (groupless) provider still gets its cooldown marked, but there is no fallback
// so the running process is left alone (lastTurnError stays false).
func TestMaybeMarkRateLimitedProvider_NoGroupMarksButNoKill(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	q := s.q
	bound, err := q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱", Platform: "zhipu", BaseUrls: `{"anthropic":"x"}`,
		ApiKey: "${ACCOUNT:智谱}", Model: "glm-5.1", GroupName: "", OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider: %v", err)
	}
	ws, _ := q.GetWorkspace(ctx, s.workspaceID)
	_ = q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{ID: ws.ID, EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true}})
	s.activeProviderID = bound.ID

	s.maybeMarkRateLimitedProvider(ctx,
		"✗ Error: API Error: Request rejected (429) · [1308][已达到 5 小时的使用上限。您的限额将在 2026-08-24 00:27:12 重置。]")

	got, _ := q.GetEnvProvider(ctx, bound.ID)
	if !got.CooldownUntil.Valid {
		t.Fatal("provider should be marked in cooldown")
	}
	s.mu.Lock()
	terr := s.lastTurnError
	s.mu.Unlock()
	if terr {
		t.Error("no group → no fallback → must NOT kill/restart")
	}
}

// TestMaybeMarkRateLimitedProvider_NonRateLimitLineIsNoop verifies ordinary
// output lines never touch provider cooldown state.
func TestMaybeMarkRateLimitedProvider_NonRateLimitLineIsNoop(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	q := s.q
	bound, _ := q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱", Platform: "zhipu", BaseUrls: `{"anthropic":"x"}`,
		ApiKey: "${ACCOUNT:智谱}", Model: "glm-5.1", OwnerType: "user", OwnerID: 0,
	})
	s.activeProviderID = bound.ID

	s.maybeMarkRateLimitedProvider(ctx, "Here is the analysis of the codebase...")
	got, _ := q.GetEnvProvider(ctx, bound.ID)
	if got.CooldownUntil.Valid {
		t.Error("non-rate-limit line must not mark cooldown")
	}
}
