package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeCLI drops an executable "claude" stub that exits 0 — enough for the
// plugin installer's CLI shell-out to succeed. Cross-platform: a .bat on
// Windows (Go's os/exec runs it via cmd.exe), a POSIX script elsewhere.
func writeFakeCLI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	var p string
	if runtime.GOOS == "windows" {
		p = filepath.Join(dir, "claude.bat")
		require.NoError(t, os.WriteFile(p, []byte("@echo off\r\n"+body+"\r\nexit /b 0\r\n"), 0o644))
	} else {
		p = filepath.Join(dir, "claude")
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\nexit 0\n"), 0o755))
	}
	return p
}

// TestApply_AutoInstallsScenePlugins verifies the "场景自带 skill 自动安装"
// behavior: scene Apply for a claude workspace actually runs the installer for
// scene-declared plugins (status installed/skipped/failed — never the manual
// "pending" state), instead of just planning and waiting for an SPA click.
func TestApply_AutoInstallsScenePlugins(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)

	projector := NewSceneProjector(db, dataDir, nil, NewPluginInstaller(writeFakeCLI(t, "")), nil, nil)
	svc := NewSceneLayerService(db, projector)

	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "demo", "Demo", "", nil, &SceneDefinition{
		Plugins: []PluginDecl{{Source: "document-skills@fake-skills-mp"}},
	})
	require.NoError(t, err)

	got, err := svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.Len(t, got.InstallFailures, 1, "scene-declared plugin must be attempted")
	row := got.InstallFailures[0]
	assert.Equal(t, "document-skills@fake-skills-mp", row.Source)
	// Auto-install means the fake installer ran (exits 0) → "installed"; it must
	// NOT have been left in the manual "pending" state.
	assert.Equal(t, PluginInstallStatusInstalled, row.Status)
	assert.NotEqual(t, PluginInstallStatusPending, row.Status)

	// The projection cache persisted the same picture so the SPA banner (which
	// hides when there is nothing pending/failed) has nothing left to show.
	cached, err := store.New(db).GetProjection(ctx, ws.ID)
	require.NoError(t, err)
	require.Contains(t, cached.InstallFailures, "installed")
}

// TestReconcileInstallStatus covers the "重新打开自动检查是否安装完成" path:
// GetProjection calls ReconcileInstallStatus, which flips stale pending/failed
// rows to "skipped" once the plugin is actually present on disk — so the banner
// disappears once everything is safely installed.
func TestReconcileInstallStatus_FlipsNowInstalledToSkipped(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, nil)
	// Test seam: only "doc@skills-mp" is considered present on disk.
	projector.installCheck = func(_ context.Context, _ string, p PluginDecl) (bool, error) {
		return p.Source == "doc@skills-mp", nil
	}

	seed := []PluginInstallResult{
		{Source: "doc@skills-mp", Status: PluginInstallStatusFailed, Stderr: "stale error"},
		{Source: "other@mp", Status: PluginInstallStatusPending},
		{Source: "done@mp", Status: PluginInstallStatusInstalled},
	}
	require.NoError(t, store.New(db).UpsertProjection(ctx, store.UpsertProjectionParams{
		WorkspaceID:         ws.ID,
		Digest:              "d",
		ProjectedDefinition: `{"mcp":[]}`,
		MissingCredentials:  "[]",
		InstallFailures:     InstallResultsToJSON(seed),
		RestartRequired:     0,
	}))

	got, changed, err := projector.ReconcileInstallStatus(ctx, ws.ID)
	require.NoError(t, err)
	assert.True(t, changed, "reconcile must report that a stale row changed")

	bySource := map[string]PluginInstallResult{}
	for _, r := range got {
		bySource[r.Source] = r
	}
	// Now-installed plugin flips to skipped, stale stderr dropped.
	assert.Equal(t, PluginInstallStatusSkipped, bySource["doc@skills-mp"].Status)
	assert.Empty(t, bySource["doc@skills-mp"].Stderr)
	// Not-yet-installed plugin keeps its pending status.
	assert.Equal(t, PluginInstallStatusPending, bySource["other@mp"].Status)
	// Already-installed rows are untouched.
	assert.Equal(t, PluginInstallStatusInstalled, bySource["done@mp"].Status)

	// The refreshed state was persisted for the next GET.
	cached, err := store.New(db).GetProjection(ctx, ws.ID)
	require.NoError(t, err)
	require.Contains(t, cached.InstallFailures, "skipped")
}

// TestReconcileInstallStatus_NoChangeWhenNothingInstalled ensures a failed row
// whose plugin is still absent is left intact (its stderr is preserved for the
// retry affordance), and an all-clear cache reports no change.
func TestReconcileInstallStatus_KeepsGenuineFailures(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, nil)
	projector.installCheck = func(_ context.Context, _ string, _ PluginDecl) (bool, error) {
		return false, nil // nothing installed on disk
	}

	seed := []PluginInstallResult{
		{Source: "doc@skills-mp", Status: PluginInstallStatusFailed, Stderr: "marketplace not found"},
	}
	require.NoError(t, store.New(db).UpsertProjection(ctx, store.UpsertProjectionParams{
		WorkspaceID:         ws.ID,
		Digest:              "d",
		ProjectedDefinition: `{"mcp":[]}`,
		MissingCredentials:  "[]",
		InstallFailures:     InstallResultsToJSON(seed),
		RestartRequired:     0,
	}))

	got, changed, err := projector.ReconcileInstallStatus(ctx, ws.ID)
	require.NoError(t, err)
	assert.False(t, changed, "nothing changed when the plugin is still absent")
	require.Len(t, got, 1)
	assert.Equal(t, PluginInstallStatusFailed, got[0].Status)
	assert.Equal(t, "marketplace not found", got[0].Stderr)
}

// TestReconcileInstallStatus_CodexIsSkipped verifies codex workspaces never go
// through the reconcile path (their plugins are MCP servers, not CLI-installed).
func TestReconcileInstallStatus_CodexIsSkipped(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, nil)
	// Driver-aware wrapper so the UPDATE survives Postgres too (bare ? would not).
	_, err := projector.db.ExecContext(ctx, `UPDATE workspaces SET cli_type = 'codex' WHERE id = ?`, ws.ID)
	require.NoError(t, err)

	projector.installCheck = func(_ context.Context, _ string, _ PluginDecl) (bool, error) {
		t.Fatal("installCheck must not be called for codex workspaces")
		return false, nil
	}

	seed := []PluginInstallResult{
		{Source: "doc@skills-mp", Status: PluginInstallStatusUnsupported},
	}
	require.NoError(t, store.New(db).UpsertProjection(ctx, store.UpsertProjectionParams{
		WorkspaceID:         ws.ID,
		Digest:              "d",
		ProjectedDefinition: `{"mcp":[]}`,
		MissingCredentials:  "[]",
		InstallFailures:     InstallResultsToJSON(seed),
		RestartRequired:     0,
	}))

	got, changed, err := projector.ReconcileInstallStatus(ctx, ws.ID)
	require.NoError(t, err)
	assert.False(t, changed)
	require.Len(t, got, 1)
	assert.Equal(t, PluginInstallStatusUnsupported, got[0].Status)
}

// TestReconcileInstallStatus_NoProjectionIsNoop ensures a workspace without a
// cached projection row reconciles to a no-op (GetProjection falls back to the
// empty response before even calling this).
func TestReconcileInstallStatus_NoProjectionIsNoop(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, nil)
	projector.installCheck = func(_ context.Context, _ string, _ PluginDecl) (bool, error) {
		t.Fatal("installCheck must not be called without a cached projection")
		return false, nil
	}

	got, changed, err := projector.ReconcileInstallStatus(ctx, ws.ID)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, got)
}

// TestDecodeInstallResults covers the persisted-install-failures parser used by
// ReconcileInstallStatus.
func TestDecodeInstallResults(t *testing.T) {
	assert.Nil(t, DecodeInstallResults(""))
	assert.Nil(t, DecodeInstallResults("not json"))
	got := DecodeInstallResults(`[{"source":"a@mp","status":"failed"}]`)
	require.Len(t, got, 1)
	assert.Equal(t, "a@mp", got[0].Source)
	assert.Equal(t, PluginInstallStatusFailed, got[0].Status)
}
