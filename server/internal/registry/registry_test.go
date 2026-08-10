package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCLISource creates a CLISource with pre-populated cache for testing.
func newTestCLISource(agents []AgentInfo) *CLISource {
	s := &CLISource{}
	s.cached = agents
	return s
}

func TestAgentRegistry_ListAll(t *testing.T) {
	customDir := t.TempDir()

	localAgents := []AgentInfo{
		{Source: "local", Name: "everything-claude-code:reviewer", Description: "Reviews code"},
	}

	customSrc := NewCustomSource(customDir)
	_, err := customSrc.Create(context.Background(), CreateCustomAgentInput{
		Name: "my-agent", Description: "Custom", Content: "Custom content", ClonedFrom: "local:reviewer",
	})
	require.NoError(t, err)

	reg := NewAgentRegistry(
		newTestCLISource(localAgents),
		NewCommunitySource("test", "", false),
		customSrc,
		NewCuratedSource(),
	)

	result, err := reg.ListAll(context.Background())
	require.NoError(t, err)
	assert.Len(t, result["local"], 1)
	assert.Len(t, result["community"], 0)
	assert.Len(t, result["custom"], 1)
	assert.NotEmpty(t, result["curated"])
	assert.Equal(t, "everything-claude-code:reviewer", result["local"][0].Name)
	assert.Equal(t, "my-agent", result["custom"][0].Name)
}

func TestAgentRegistry_Clone(t *testing.T) {
	customDir := t.TempDir()

	// Create a temp file so Get can read content
	agentDir := t.TempDir()
	agentFile := filepath.Join(agentDir, "reviewer.md")
	err := os.WriteFile(agentFile, []byte("---\nname: reviewer\ndescription: Reviews code\n---\nYou review code."), 0644)
	require.NoError(t, err)

	localAgents := []AgentInfo{
		{Source: "local", Name: "everything-claude-code:reviewer", Description: "Reviews code", FilePath: agentFile},
	}

	customSrc := NewCustomSource(customDir)
	reg := NewAgentRegistry(
		newTestCLISource(localAgents),
		NewCommunitySource("test", "", false),
		customSrc,
		NewCuratedSource(),
	)

	info, err := reg.Clone(context.Background(), "local", "everything-claude-code:reviewer", "my-reviewer")
	require.NoError(t, err)
	assert.Equal(t, "custom", info.Source)
	assert.Equal(t, "my-reviewer", info.Name)
	assert.Equal(t, "local:everything-claude-code:reviewer", info.ClonedFrom)

	detail, err := customSrc.Get(context.Background(), "my-reviewer")
	require.NoError(t, err)
	assert.Contains(t, detail.Content, "You review code.")
}
