package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// GenerateMemoryFile (#256) sources the injected context file solely from
// memories (project_learnings has been retired). Entries are grouped by type
// and deduped by (type, title).
func TestGenerateMemoryFile_FromMemories(t *testing.T) {
	db := setupMemoryTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	memSvc := NewMemoryService(q, db, "claude")

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p", OwnerType: "user"})
	require.NoError(t, err)
	pid := proj.ID

	_, err = memSvc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "decision", Title: "use tx.Queries()", Content: "never WithTx on pgx",
	})
	require.NoError(t, err)
	_, err = memSvc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "gotcha", Title: "pgx 42P18", Content: "anchor every param",
	})
	require.NoError(t, err)

	dir := t.TempDir()
	path := memSvc.GenerateMemoryFile(ctx, pid, dir)
	require.NotEmpty(t, path)
	require.Equal(t, filepath.Join(dir, ".learnings.generated.md"), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)

	require.Contains(t, content, "use tx.Queries()")
	require.Contains(t, content, "pgx 42P18")
	require.Equal(t, 1, strings.Count(content, "use tx.Queries()"), "no duplication")
}

// stubClaudeResolver is a minimal claudeConfigResolver for extraction env tests.
type stubClaudeResolver struct {
	configDir string
	err       error
}

func (r stubClaudeResolver) ResolveForWorkspace(_ context.Context, _, _ int64) (*ResolvedAccount, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &ResolvedAccount{ConfigDir: r.configDir}, nil
}

// Extraction must spawn `claude` with the workspace's bound CLAUDE_CONFIG_DIR
// and provider credentials — otherwise a bare `claude -p` fails with
// "exit status 1" on hosts whose creds live in a managed config dir. It must
// also drop a stale inherited ANTHROPIC_API_KEY when a third-party auth token
// is configured (same SanitizeAnthropicEnv behavior as the agent path).
func TestBuildExtractEnv_InjectsAccountAndCredentials(t *testing.T) {
	db := setupMemoryTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	memSvc := NewMemoryService(q, db, "claude")
	memSvc.SetClaudeAccountService(stubClaudeResolver{configDir: "/managed/claude"})

	const wsID = int64(42)
	require.NoError(t, q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: wsID, Key: "ANTHROPIC_AUTH_TOKEN", Value: "zhipu-real-key",
	}))
	require.NoError(t, q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: wsID, Key: "ANTHROPIC_BASE_URL", Value: "https://open.bigmodel.cn/api/anthropic",
	}))
	// Host carries a leftover key from a prior official-Claude setup.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stale")

	env := memSvc.buildExtractEnv(ctx, wsID, 0)

	require.Contains(t, env, "CLAUDE_CONFIG_DIR=/managed/claude")
	require.Contains(t, env, "ANTHROPIC_AUTH_TOKEN=zhipu-real-key")
	require.Contains(t, env, "ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic")
	for _, e := range env {
		require.NotContains(t, e, "ANTHROPIC_API_KEY=", "stale api key must be stripped when auth token is set")
	}
}

// With no resolver wired and no managed account, the subprocess still gets the
// host env (CLI falls back to ~/.claude) — never a nil env that would wipe PATH.
func TestBuildExtractEnv_FallsBackToHostEnv(t *testing.T) {
	db := setupMemoryTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	memSvc := NewMemoryService(q, db, "claude")

	env := memSvc.buildExtractEnv(ctx, 1, 0)
	require.NotEmpty(t, env)
}

// Async extraction tracks running -> finished state in memory so the UI can
// render a spinner. With no session messages the run completes quickly with
// extracted=0 and no error, and the onDone callback fires with the project id.
func TestStartExtractionAsync_TracksStatus(t *testing.T) {
	db := setupMemoryTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	memSvc := NewMemoryService(q, db, "claude")

	// Unknown workspace is idle (zero value).
	require.False(t, memSvc.GetExtractStatus(99).Running)

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p", OwnerType: "user"})
	require.NoError(t, err)

	const wsID = int64(7)
	done := make(chan int64, 1)
	started := memSvc.StartExtractionAsync(wsID, proj.ID, 0, func(pid int64) { done <- pid })
	require.True(t, started)

	select {
	case pid := <-done:
		require.Equal(t, proj.ID, pid)
	case <-time.After(5 * time.Second):
		t.Fatal("extraction did not finish")
	}

	st := memSvc.GetExtractStatus(wsID)
	require.False(t, st.Running)
	require.Equal(t, 0, st.Extracted) // no session messages -> nothing extracted
	require.Empty(t, st.Error)
}

// Empty knowledge base removes any stale generated file.
func TestGenerateMemoryFile_EmptyRemovesStaleFile(t *testing.T) {
	db := setupMemoryTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	memSvc := NewMemoryService(q, db, "claude")

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "empty", OwnerType: "user"})
	require.NoError(t, err)

	dir := t.TempDir()
	stale := filepath.Join(dir, ".learnings.generated.md")
	require.NoError(t, os.WriteFile(stale, []byte("old"), 0o644))

	path := memSvc.GenerateMemoryFile(ctx, proj.ID, dir)
	require.Empty(t, path)
	_, statErr := os.Stat(stale)
	require.True(t, os.IsNotExist(statErr), "stale file should be removed when there is nothing to write")
}
