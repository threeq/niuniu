package service

// Wave-2 wiring tests (video-creation plan §3, flow G): the media-studio
// scene declares `mcp: - name: video-gen`, and Generate must project the
// niuniu-video-mcp stdio server into .mcp.json — command + --workspace-dir /
// --data-dir args + NN_CAP_* capability env — or degrade through the
// Unavailable channel when the module binary is missing. The stub binary is
// never executed; these tests only assert the generated config.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plantStubBinary writes a fake executable named stem (+.exe on Windows) into
// <parent>/bin/ and returns its absolute path — the layout FindMCPBinary /
// FindVideoMCPBinary look for when walking up from the workspace base_dir.
// The file is never executed (only its path lands in .mcp.json).
func plantStubBinary(t *testing.T, parent, stem string) string {
	t.Helper()
	binDir := filepath.Join(parent, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	name := stem
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(binDir, name)
	require.NoError(t, os.WriteFile(path, []byte("stub"), 0o755))
	return path
}

// newVideoGenTestGenerator builds a generator whose workspace base_dir lives
// under parent, so both binary lookups walk up into <parent>/bin.
func newVideoGenTestGenerator(t *testing.T, parent, dataDir string) *MCPConfigGenerator {
	t.Helper()
	wsBase := filepath.Join(parent, "workspaces")
	require.NoError(t, os.MkdirAll(wsBase, 0o755))
	cfg := &config.Config{DataDir: dataDir}
	cfg.Workspace.BaseDir = wsBase
	return NewMCPConfigGenerator(cfg)
}

// readMCPServers decodes the generated .mcp.json into its mcpServers map.
func readMCPServers(t *testing.T, wsPath string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wsPath, ".mcp.json"))
	require.NoError(t, err, "read .mcp.json")
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &doc), "decode .mcp.json: %s", data)
	return doc.MCPServers
}

// argValue returns the value following flag in a decoded args array, or "".
func argValue(args []any, flag string) string {
	for i, a := range args {
		if s, ok := a.(string); ok && s == flag && i+1 < len(args) {
			v, _ := args[i+1].(string)
			return v
		}
	}
	return ""
}

// TestVideoGenProjection_ProjectsBinaryArgsAndCapabilityEnv covers the happy
// path: the scene declares video-gen, the module binary resolves, and the
// workspace's capability rows arrive as NN_CAP_* env on the entry.
func TestVideoGenProjection_ProjectsBinaryArgsAndCapabilityEnv(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir) // created_by = 1

	capSvc := NewCapabilityBackendService(q, db)
	_, err := capSvc.Create(ctx, CapabilityBackend{
		OwnerType: "user", OwnerID: 1, Module: videoGenModule.Name, Capability: "tts",
		Backend: "openai-compat", Name: "我的TTS", BaseURL: "https://tts.example.com/v1",
		APIKey: "sk-test", ExtraConfig: map[string]string{"model": "tts-1"},
		Enabled: true, Position: 0,
	})
	require.NoError(t, err)

	parent := t.TempDir()
	plantStubBinary(t, parent, "niuniu-mcp")
	videoStub := plantStubBinary(t, parent, videoMCPBinaryStem)
	wsDir := t.TempDir()

	gen := newVideoGenTestGenerator(t, parent, dataDir)
	gen.SetCapabilityEnv(NewCapabilityBackendEnvResolver(q, capSvc))

	res, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: ws.ID}, []string{videoGenModule.Name}, "")
	require.NoError(t, err)
	assert.Contains(t, res.WrittenServers, videoGenModule.Name)
	assert.NotContains(t, res.Unavailable, videoGenModule.Name)

	servers := readMCPServers(t, wsDir)
	vg, ok := servers[videoGenModule.Name]
	require.True(t, ok, "declared video-gen must be projected into .mcp.json")
	assert.Equal(t, videoStub, vg["command"])
	args, _ := vg["args"].([]any)
	assert.Equal(t, wsDir, argValue(args, "--workspace-dir"))
	assert.Equal(t, dataDir, argValue(args, "--data-dir"))

	env, _ := vg["env"].(map[string]any)
	require.NotNil(t, env, "capability env must be injected")
	assert.Equal(t, "openai-compat", env["NN_CAP_TTS_BACKEND"])
	assert.Equal(t, "https://tts.example.com/v1", env["NN_CAP_TTS_BASE_URL"])
	assert.Equal(t, "sk-test", env["NN_CAP_TTS_API_KEY"])
	assert.Equal(t, "tts-1", env["NN_CAP_TTS_MODEL"])

	assert.Contains(t, servers, "niuniu", "the niuniu base entry must stay")
}

// TestVideoGenProjection_UnavailableWhenBinaryMissing covers the degrade
// path: the scene declares video-gen but the module binary is not installed.
// The name lands in Unavailable and no entry is written — and the call still
// succeeds (design §7.1: 场景引用缺失模块 → 优雅降级，绝不报错).
func TestVideoGenProjection_UnavailableWhenBinaryMissing(t *testing.T) {
	// Hermetic: neutralize every lane that could find a real binary on a
	// developer machine — the cwd has no bin/, PATH is emptied, and the
	// base_dir walk-up stays inside this test's own temp tree (which holds
	// only the niuniu-mcp stub).
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())

	db := setupSceneTestDB(t)
	q := store.New(db)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	capSvc := NewCapabilityBackendService(q, db)

	root := t.TempDir()
	baseDir := filepath.Join(root, "deep", "a", "b", "c", "workspaces")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	binDir := filepath.Join(root, "deep", "a", "b", "c", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	// The base MCP must resolve (Generate errors without it); the video one
	// must not exist.
	baseName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		baseName += ".exe"
	}
	require.NoError(t, os.WriteFile(filepath.Join(binDir, baseName), []byte("stub"), 0o755))

	cfg := &config.Config{DataDir: dataDir}
	cfg.Workspace.BaseDir = baseDir
	gen := NewMCPConfigGenerator(cfg)
	gen.SetCapabilityEnv(NewCapabilityBackendEnvResolver(q, capSvc))

	wsDir := t.TempDir()
	res, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: ws.ID}, []string{videoGenModule.Name}, "")
	require.NoError(t, err, "a missing module binary must degrade, not error")
	assert.Contains(t, res.Unavailable, videoGenModule.Name)
	assert.NotContains(t, res.WrittenServers, videoGenModule.Name)

	servers := readMCPServers(t, wsDir)
	_, ok := servers[videoGenModule.Name]
	assert.False(t, ok, "no entry may be written for a missing module binary")
	assert.Contains(t, servers, "niuniu")
}

// TestVideoGenProjection_SurvivesAndRefreshesOnExtrasNil covers the
// token-refresh path (extras == nil — the caller doesn't know the scene's MCP
// set): the existing video-gen entry must not be dropped as if it were a
// hand-edited server, and it is refreshed from the current capability config
// rather than carried forward stale.
func TestVideoGenProjection_SurvivesAndRefreshesOnExtrasNil(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	capSvc := NewCapabilityBackendService(q, db)

	parent := t.TempDir()
	plantStubBinary(t, parent, "niuniu-mcp")
	videoStub := plantStubBinary(t, parent, videoMCPBinaryStem)
	wsDir := t.TempDir()

	gen := newVideoGenTestGenerator(t, parent, dataDir)
	gen.SetCapabilityEnv(NewCapabilityBackendEnvResolver(q, capSvc))

	// Scene projection: media-studio declares the module.
	_, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: ws.ID}, []string{videoGenModule.Name}, "")
	require.NoError(t, err)
	require.Contains(t, readMCPServers(t, wsDir), videoGenModule.Name)

	// The user adds a video account mid-session; the refresh call must keep
	// the entry AND pick the new env up.
	_, err = capSvc.Create(ctx, CapabilityBackend{
		OwnerType: "user", OwnerID: 1, Module: videoGenModule.Name, Capability: "video",
		Backend: "seedance", Name: "我的视频", BaseURL: "https://ark.example.com",
		APIKey: "ak-test", Enabled: true,
	})
	require.NoError(t, err)

	_, err = gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: ws.ID, SessionToken: "tok-refresh"}, nil, "")
	require.NoError(t, err)

	servers := readMCPServers(t, wsDir)
	vg, ok := servers[videoGenModule.Name]
	require.True(t, ok, "extras==nil refresh must preserve the video-gen entry")
	assert.Equal(t, videoStub, vg["command"])
	args, _ := vg["args"].([]any)
	assert.Equal(t, wsDir, argValue(args, "--workspace-dir"))

	env, _ := vg["env"].(map[string]any)
	require.NotNil(t, env, "the preserved entry must be refreshed with the current capability env")
	assert.Equal(t, "seedance", env["NN_CAP_VIDEO_BACKEND"])
	assert.Equal(t, "ak-test", env["NN_CAP_VIDEO_API_KEY"])

	// The niuniu entry still gets the refreshed session token.
	niuniuEnv, _ := servers["niuniu"]["env"].(map[string]any)
	assert.Equal(t, "tok-refresh", niuniuEnv["NIUNIU_MCP_TOKEN"])
}

// TestVideoGenProjection_OmitsEnvWithoutCapabilityRows: with the module
// installed but no accounts configured, the entry is still projected (the
// tool group exists and degrades itself) and simply omits the env block
// instead of writing a misleading empty map.
func TestVideoGenProjection_OmitsEnvWithoutCapabilityRows(t *testing.T) {
	db := setupSceneTestDB(t)
	q := store.New(db)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	capSvc := NewCapabilityBackendService(q, db)

	parent := t.TempDir()
	plantStubBinary(t, parent, "niuniu-mcp")
	videoStub := plantStubBinary(t, parent, videoMCPBinaryStem)
	wsDir := t.TempDir()

	gen := newVideoGenTestGenerator(t, parent, dataDir)
	gen.SetCapabilityEnv(NewCapabilityBackendEnvResolver(q, capSvc))

	res, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: ws.ID}, []string{videoGenModule.Name}, "")
	require.NoError(t, err)
	assert.Contains(t, res.WrittenServers, videoGenModule.Name)

	vg := readMCPServers(t, wsDir)[videoGenModule.Name]
	require.NotNil(t, vg)
	assert.Equal(t, videoStub, vg["command"])
	_, hasEnv := vg["env"]
	assert.False(t, hasEnv, "empty capability env must be omitted, not written as {}")
}
