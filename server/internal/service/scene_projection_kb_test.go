package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSceneProjector_ExpandsMcpKnowledgeBases verifies that a scene selecting an
// mcp-kind knowledge base projects it as an inline MCP server (Authorization
// header resolved from credstore at write time) plus its required credential -
// the unified replacement for the old hand-configured scene "kb" MCP server.
func TestSceneProjector_ExpandsMcpKnowledgeBases(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	q := store.New(db)

	// Seed an mcp-kind KB owned by the workspace's owner, plus an inert local KB
	// (which must NOT become an MCP server - it's reached via kb_search/mount).
	_, err := q.CreateKnowledgeBase(ctx, store.CreateKnowledgeBaseParams{
		OwnerType: "user", OwnerID: 1, Name: "remote-kb", SourceKind: "mcp",
		SourceAddr: "https://kb.example.com/mcp", SourceConfig: `{"cred_alias":"kb-42"}`,
	})
	require.NoError(t, err)
	_, err = q.CreateKnowledgeBase(ctx, store.CreateKnowledgeBaseParams{
		OwnerType: "user", OwnerID: 1, Name: "local-kb", SourceKind: "local", SourceAddr: "/tmp/x",
	})
	require.NoError(t, err)

	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "kb-scene", "KB Scene", "", nil, &SceneDefinition{
		KnowledgeBases: []SceneKBRef{
			{Name: "remote-kb", Purpose: "product search"},
			{Name: "local-kb"},
		},
	})
	require.NoError(t, err)

	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	got, err := svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, got)

	// The mcp-kind KB became an inline MCP server named after its slug.
	assert.Contains(t, got.Projection.MCPNames, "remote-kb")
	cfg := got.Projection.MCPConfigs["remote-kb"]
	require.NotNil(t, cfg, "mcp-kind KB must be projected as an inline MCP server")
	assert.Equal(t, "https://kb.example.com/mcp", cfg["url"])
	headers, _ := cfg["headers"].(map[string]any)
	require.NotNil(t, headers)
	assert.Equal(t, "Bearer ${cred:kb-42.token}", headers["Authorization"])

	// The local-kind KB did NOT become an MCP server.
	assert.NotContains(t, got.Projection.MCPNames, "local-kb")

	// The credstore alias is declared as a required credential.
	var foundCred bool
	for _, c := range got.Projection.RequiredCredentials {
		if c.Alias == "kb-42" && c.Provider == "knowledge-base" {
			foundCred = true
		}
	}
	assert.True(t, foundCred, "mcp-kind KB must declare its credstore credential")
}