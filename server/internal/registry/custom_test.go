package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomSource_Create(t *testing.T) {
	dir := t.TempDir()
	src := NewCustomSource(dir)

	info, err := src.Create(context.Background(), CreateCustomAgentInput{
		Name:        "my-agent",
		Description: "My custom agent",
		Content:     "You are my agent.",
	})
	require.NoError(t, err)
	assert.Equal(t, "custom", info.Source)
	assert.Equal(t, "my-agent", info.Name)
	assert.Equal(t, "My custom agent", info.Description)

	data, err := os.ReadFile(filepath.Join(dir, "my-agent.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: my-agent")
	assert.Contains(t, string(data), "You are my agent.")
}

func TestCustomSource_List(t *testing.T) {
	dir := t.TempDir()
	src := NewCustomSource(dir)
	ctx := context.Background()

	_, err := src.Create(ctx, CreateCustomAgentInput{
		Name: "agent-a", Description: "Agent A", Content: "content a",
	})
	require.NoError(t, err)
	_, err = src.Create(ctx, CreateCustomAgentInput{
		Name: "agent-b", Description: "Agent B", Content: "content b",
	})
	require.NoError(t, err)

	agents, err := src.List(ctx)
	require.NoError(t, err)
	assert.Len(t, agents, 2)
}

func TestCustomSource_Update(t *testing.T) {
	dir := t.TempDir()
	src := NewCustomSource(dir)
	ctx := context.Background()

	_, err := src.Create(ctx, CreateCustomAgentInput{
		Name: "my-agent", Description: "Original", Content: "original content",
	})
	require.NoError(t, err)

	err = src.Update(ctx, "my-agent", UpdateCustomAgentInput{
		Description: "Updated", Content: "updated content",
	})
	require.NoError(t, err)

	detail, err := src.Get(ctx, "my-agent")
	require.NoError(t, err)
	assert.Equal(t, "Updated", detail.Description)
	assert.Contains(t, detail.Content, "updated content")
}

func TestCustomSource_Delete(t *testing.T) {
	dir := t.TempDir()
	src := NewCustomSource(dir)
	ctx := context.Background()

	_, err := src.Create(ctx, CreateCustomAgentInput{
		Name: "my-agent", Content: "content",
	})
	require.NoError(t, err)

	err = src.Delete(ctx, "my-agent")
	require.NoError(t, err)

	_, err = src.Get(ctx, "my-agent")
	assert.Error(t, err)
}

func TestCustomSource_ClonedFrom(t *testing.T) {
	dir := t.TempDir()
	src := NewCustomSource(dir)
	ctx := context.Background()

	_, err := src.Create(ctx, CreateCustomAgentInput{
		Name: "cloned-agent", Description: "Cloned", Content: "content", ClonedFrom: "local:code-reviewer",
	})
	require.NoError(t, err)

	detail, err := src.Get(ctx, "cloned-agent")
	require.NoError(t, err)
	assert.Equal(t, "local:code-reviewer", detail.ClonedFrom)
}
