package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestBuiltinScenes_KBPresetShape asserts the user-configured knowledge-base MCP
// preset scenes (issue B: 用户自配知识库 MCP + 行业预设) ship embedded and carry the
// contract the projection + credential-injection path depends on:
//
//   - an http MCP server named "kb" whose auth header references a credstore
//     credential via the ${cred:kb-api.token} placeholder (never a plaintext token);
//   - a required_credentials entry (alias kb-api, provider knowledge-base) so the
//     projector knows which credential to decrypt and reports it missing when unbound;
//   - the knowledge-base tag so the SPA can surface these as one-click industry presets.
//
// This is the orchestration contract: niuniu wires the MCP + injects the token,
// the data itself stays behind the user's MCP.
func TestBuiltinScenes_KBPresetShape(t *testing.T) {
	presets := []struct {
		file     string
		industry string // an industry-specific tag expected on the scene
	}{
		{"kb-ecommerce.yaml", "ecommerce"},
		{"kb-legal.yaml", "legal"},
		{"kb-medical.yaml", "medical"},
		{"kb-custom.yaml", "custom"},
	}
	for _, p := range presets {
		t.Run(p.file, func(t *testing.T) {
			raw, err := builtinScenesFS.ReadFile("builtin_scenes/" + p.file)
			require.NoError(t, err, "preset must ship embedded (run make builtin-scenes-sync)")

			var doc builtinSceneYAML
			require.NoError(t, yaml.Unmarshal(raw, &doc), "preset YAML must parse")

			// Validates like any scene the seeder would accept.
			def := SceneDefinition{
				MCP: doc.MCP, Plugins: doc.Plugins, Skills: doc.Skills,
				Assets: doc.Assets, Prompts: doc.Prompts,
				RequiredCredentials: doc.RequiredCredentials,
			}
			require.NoError(t, ValidateSceneDefinition(&def), "preset must pass scene validation")

			// One-click industry preset: discoverable via the shared + industry tags.
			assert.Contains(t, doc.Tags, "knowledge-base", "must carry the knowledge-base tag")
			assert.Contains(t, doc.Tags, p.industry, "must carry its industry tag")

			// The KB MCP server: http transport + credstore-injected auth header.
			require.Len(t, doc.MCP, 1, "exactly one KB MCP server")
			kb := doc.MCP[0]
			assert.Equal(t, "kb", kb.Name)
			require.NotNil(t, kb.Config, "KB MCP must carry an inline config")
			assert.Equal(t, "http", kb.Config["type"], "KB MCP is an http server")
			assert.NotEmpty(t, kb.Config["url"], "KB MCP needs an endpoint url")

			headers, ok := kb.Config["headers"].(map[string]any)
			require.True(t, ok, "KB MCP must declare auth headers")
			authz, _ := headers["Authorization"].(string)
			assert.Contains(t, authz, "${cred:kb-api.token}",
				"auth token must be a credstore placeholder, never plaintext")

			// The projector resolves ${cred:kb-api.token} via this declaration.
			require.Len(t, doc.RequiredCredentials, 1)
			rc := doc.RequiredCredentials[0]
			assert.Equal(t, "kb-api", rc.Alias)
			assert.Equal(t, "knowledge-base", rc.Provider)
			assert.False(t, rc.Optional, "the KB token is required for the server to project")

			// A retrieval quick-action so OPC users have a guided entry to search.
			assert.NotEmpty(t, doc.Assets.QuickActions, "preset ships at least one KB quick action")
		})
	}
}
