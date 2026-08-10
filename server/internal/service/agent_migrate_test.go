package service_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupMigrateTest(t *testing.T) (*service.AgentService, *store.Queries, string, context.Context) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	tmp := t.TempDir()
	cfg := &config.Config{}
	cfg.DataDir = tmp
	svc := service.NewAgentService(q, cfg, nil)
	require.NoError(t, svc.EnsureAgentDir())
	return svc, q, filepath.Join(tmp, "agents"), context.Background()
}

// Simulates an old niuniu install: DB row points to a directory, and the
// agent content lives at <dir>/agent.md with no frontmatter.
func seedLegacyAgent(t *testing.T, q *store.Queries, agentsRoot, name, description, content string) int64 {
	t.Helper()
	dir := filepath.Join(agentsRoot, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agent.md"), []byte(content), 0o644))

	agent, err := q.CreateAgent(context.Background(), store.CreateAgentParams{
		Name:        name,
		Description: description,
		DirPath:     dir,
		FileHash:    "legacy",
		SourceUrl:   sql.NullString{},
			OwnerType: "user",
	})
	require.NoError(t, err)
	return agent.ID
}

func TestMigrateAgentLayout_FlattensLegacyDir(t *testing.T) {
	svc, q, root, ctx := setupMigrateTest(t)

	id := seedLegacyAgent(t, q, root, "architect", "system designer",
		"# Architect\n\nBody text without frontmatter.\n")

	require.NoError(t, svc.MigrateAgentLayout(ctx))

	agent, err := q.GetAgent(ctx, id)
	require.NoError(t, err)

	// DirPath now points to the flat .md file.
	assert.Equal(t, filepath.Join(root, "architect.md"), agent.DirPath)

	// Legacy dir has been removed.
	_, err = os.Stat(filepath.Join(root, "architect"))
	assert.True(t, os.IsNotExist(err), "legacy dir should be removed")

	// New file exists and has frontmatter.
	data, err := os.ReadFile(agent.DirPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "---")
	assert.Contains(t, content, "name: architect")
	assert.Contains(t, content, "description: system designer")
	assert.Contains(t, content, "Body text without frontmatter.")
}

func TestMigrateAgentLayout_Idempotent(t *testing.T) {
	svc, q, root, ctx := setupMigrateTest(t)
	id := seedLegacyAgent(t, q, root, "tester", "writes tests", "# Tester\nbody\n")

	require.NoError(t, svc.MigrateAgentLayout(ctx))
	firstAgent, err := q.GetAgent(ctx, id)
	require.NoError(t, err)
	firstData, err := os.ReadFile(firstAgent.DirPath)
	require.NoError(t, err)

	require.NoError(t, svc.MigrateAgentLayout(ctx))
	secondAgent, err := q.GetAgent(ctx, id)
	require.NoError(t, err)
	secondData, err := os.ReadFile(secondAgent.DirPath)
	require.NoError(t, err)

	assert.Equal(t, firstAgent.DirPath, secondAgent.DirPath)
	assert.Equal(t, string(firstData), string(secondData))
}

func TestMigrateAgentLayout_DirMissingAgentMd_NoOp(t *testing.T) {
	svc, q, root, ctx := setupMigrateTest(t)

	// Dir exists but no agent.md inside.
	dir := filepath.Join(root, "empty")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err := q.CreateAgent(ctx, store.CreateAgentParams{
		Name:        "empty",
		Description: "empty",
		DirPath:     dir,
		FileHash:    "x",
			OwnerType: "user",
	})
	require.NoError(t, err)

	require.NoError(t, svc.MigrateAgentLayout(ctx))

	// Dir still there, DB unchanged.
	_, err = os.Stat(dir)
	assert.NoError(t, err)
}

func TestMigrateAgentLayout_DirPathMissing_NoOp(t *testing.T) {
	svc, q, _, ctx := setupMigrateTest(t)
	_, err := q.CreateAgent(ctx, store.CreateAgentParams{
		Name:        "ghost",
		Description: "never existed",
		DirPath:     filepath.Join(t.TempDir(), "does-not-exist"),
		FileHash:    "x",
			OwnerType: "user",
	})
	require.NoError(t, err)

	// Should not error just because the path is missing.
	assert.NoError(t, svc.MigrateAgentLayout(ctx))
}

// A flat agent lacking frontmatter gets repaired in-place.
func TestMigrateAgentLayout_RepairsFlatWithoutFrontmatter(t *testing.T) {
	svc, q, root, ctx := setupMigrateTest(t)

	flat := filepath.Join(root, "dev.md")
	require.NoError(t, os.WriteFile(flat, []byte("# Dev\nbody no frontmatter\n"), 0o644))

	agent, err := q.CreateAgent(ctx, store.CreateAgentParams{
		Name:        "dev",
		Description: "writes code",
		DirPath:     flat,
		FileHash:    "x",
			OwnerType: "user",
	})
	require.NoError(t, err)

	require.NoError(t, svc.MigrateAgentLayout(ctx))

	data, err := os.ReadFile(flat)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "name: dev")
	assert.Contains(t, content, "description: writes code")
	assert.Contains(t, content, "body no frontmatter")

	got, err := q.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.NotEqual(t, "x", got.FileHash, "hash should be updated after repair")
}

// A flat agent that already has frontmatter with a name is left untouched.
func TestMigrateAgentLayout_FlatWithFrontmatterUnchanged(t *testing.T) {
	svc, q, root, ctx := setupMigrateTest(t)

	flat := filepath.Join(root, "rev.md")
	content := "---\nname: rev\ndescription: reviewer\ntools:\n  - Read\n---\nbody\n"
	require.NoError(t, os.WriteFile(flat, []byte(content), 0o644))

	_, err := q.CreateAgent(ctx, store.CreateAgentParams{
		Name:        "rev",
		Description: "reviewer",
		DirPath:     flat,
		FileHash:    "stable",
			OwnerType: "user",
	})
	require.NoError(t, err)

	require.NoError(t, svc.MigrateAgentLayout(ctx))

	data, err := os.ReadFile(flat)
	require.NoError(t, err)
	assert.Equal(t, content, string(data), "file with frontmatter should not be rewritten")
}
