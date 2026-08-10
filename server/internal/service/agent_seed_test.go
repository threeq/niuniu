package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAgentSeedTest(t *testing.T) (*service.AgentService, *store.Queries, context.Context) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	tmpDir := t.TempDir()
	cfg := &config.Config{}
	cfg.DataDir = tmpDir
	svc := service.NewAgentService(q, cfg, nil)
	require.NoError(t, svc.EnsureAgentDir())
	return svc, q, context.Background()
}

func TestSeedDefaultAgents_CreatesAgents(t *testing.T) {
	svc, q, ctx := setupAgentSeedTest(t)

	err := svc.SeedDefaultAgents(ctx)
	require.NoError(t, err)

	agents, err := q.ListAgents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 6, len(agents), "should create 6 default agents")

	expectedNames := []string{"architect", "coordinator", "developer", "devops", "reviewer", "tester"}
	actualNames := make([]string, len(agents))
	for i, a := range agents {
		actualNames[i] = a.Name
	}
	assert.ElementsMatch(t, expectedNames, actualNames, "all 6 default agents should exist")
}

func TestSeedDefaultAgents_CreatesAgentFiles(t *testing.T) {
	svc, _, ctx := setupAgentSeedTest(t)

	err := svc.SeedDefaultAgents(ctx)
	require.NoError(t, err)

	agentNames := []string{"coordinator", "architect", "developer", "reviewer", "tester", "devops"}
	for _, name := range agentNames {
		detail, err := svc.GetByName(ctx, name)
		require.NoError(t, err, "agent %s should exist", name)
		assert.NotEmpty(t, detail.Content, "agent %s should have content", name)

		// DirPath is the agent .md file itself (flat layout).
		_, err = os.Stat(detail.DirPath)
		assert.NoError(t, err, ".md file should exist for %s", name)
		assert.Equal(t, filepath.Join(filepath.Dir(detail.DirPath), name+".md"), detail.DirPath)
		assert.True(t, strings.HasPrefix(detail.Content, "---\n"), "agent %s should start with frontmatter", name)
		assert.Contains(t, detail.Content, "name: "+name, "agent %s should declare name in frontmatter", name)
	}
}

func TestSeedDefaultAgents_Idempotent(t *testing.T) {
	svc, q, ctx := setupAgentSeedTest(t)

	require.NoError(t, svc.SeedDefaultAgents(ctx))
	require.NoError(t, svc.SeedDefaultAgents(ctx))

	agents, err := q.ListAgents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 6, len(agents), "calling twice should not duplicate agents")
}

func TestSeedDefaultAgents_SelfHealing(t *testing.T) {
	svc, q, ctx := setupAgentSeedTest(t)

	// Manually create a custom agent — seed should still create the 6 defaults
	_, err := svc.Create(ctx, service.CreateAgentInput{
		Name:        "custom-agent",
		Description: "A custom agent",
		Content:     "custom content",
	}, "user", 0)
	require.NoError(t, err)

	require.NoError(t, svc.SeedDefaultAgents(ctx))

	agents, err := q.ListAgents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 7, len(agents), "should create 6 defaults + 1 custom")
}


func TestSeedDefaultAgents_AgentDescriptionsNotEmpty(t *testing.T) {
	svc, q, ctx := setupAgentSeedTest(t)

	require.NoError(t, svc.SeedDefaultAgents(ctx))

	agents, err := q.ListAgents(ctx)
	require.NoError(t, err)
	for _, a := range agents {
		assert.NotEmpty(t, a.Description, "agent %s should have a description", a.Name)
	}
}

