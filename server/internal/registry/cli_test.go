package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCLIOutput(t *testing.T) {
	output := `34 active agents

Plugin agents:
  everything-claude-code:architect · opus
  everything-claude-code:build-error-resolver · sonnet
  superpowers:code-reviewer · inherit

Built-in agents:
  claude-code-guide · haiku
  Explore · haiku
  general-purpose · inherit
`
	agents := parseCLIOutput(output)

	assert.Len(t, agents, 6)

	// Plugin agents — full qualified name
	assert.Equal(t, "everything-claude-code:architect", agents[0].Name)
	assert.Equal(t, "local", agents[0].Source)
	assert.Equal(t, "everything-claude-code", agents[0].Author)
	assert.Contains(t, agents[0].Tags, "plugin")
	assert.Contains(t, agents[0].Tags, "model:opus")

	assert.Equal(t, "everything-claude-code:build-error-resolver", agents[1].Name)
	assert.Contains(t, agents[1].Tags, "model:sonnet")

	assert.Equal(t, "superpowers:code-reviewer", agents[2].Name)
	assert.Equal(t, "superpowers", agents[2].Author)

	// Built-in agents
	assert.Equal(t, "claude-code-guide", agents[3].Name)
	assert.Equal(t, "local", agents[3].Source)
	assert.Contains(t, agents[3].Tags, "builtin")
	assert.Contains(t, agents[3].Tags, "model:haiku")

	assert.Equal(t, "Explore", agents[4].Name)
	assert.Equal(t, "general-purpose", agents[5].Name)
}

func TestParseCLIOutput_Empty(t *testing.T) {
	agents := parseCLIOutput("")
	assert.Empty(t, agents)
}

// TestRefresh_FallsBackToFilesystemWhenCLIUnavailable covers the
// scenario where `claude agents` exits non-zero (e.g. Claude Code v2.1+
// where the background-agents subcommand is gated behind a feature
// flag and prints "is not available in this environment"). Before this
// fallback existed, `GET /api/agent-registry/list` would 500 with
// "list local: run claude agents: exit status 1".
func TestRefresh_FallsBackToFilesystemWhenCLIUnavailable(t *testing.T) {
	home := t.TempDir()
	pluginDir := filepath.Join(home, ".claude", "plugins", "cache",
		"acme-publisher", "superpowers", "1.0.0", "agents")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	body := "---\nname: code-reviewer\ndescription: Reviews diffs\nmodel: opus\n---\nbody"
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "code-reviewer.md"), []byte(body), 0o644))

	// Use a CLI that always fails so we exercise the fallback branch.
	s := &CLISource{
		claudeCmd: "definitely-not-a-real-binary-name-xyzzy",
		homeDir:   home,
	}

	agents, err := s.refresh(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)

	got := agents[0]
	assert.Equal(t, "local", got.Source)
	assert.Equal(t, "superpowers:code-reviewer", got.Name)
	assert.Equal(t, "Reviews diffs", got.Description)
	assert.Equal(t, "superpowers", got.Author)
	assert.Contains(t, got.Tags, "plugin")
	assert.Contains(t, got.Tags, "model:opus")
	assert.NotEmpty(t, got.FilePath)
}
