package service

// Codex-side capability-module projection (flow K, video-creation plan §3):
// Claude's .mcp.json path projects scene-declared capability modules
// (video-gen) and preserves non-niuniu servers on extras==nil refreshes;
// GenerateCodexConfigTomlWithExtras must do the same for .codex/config.toml,
// or a codex workspace simply never sees the video tool group. These tests
// assert the generated TOML only — the stub binaries are never executed.
//
// They also pin the extras==nil carry-forward: a spawn must not wipe
// non-niuniu [mcp_servers.*] sections (scene-projected or hand-added), and a
// capability-module section is rebuilt in place from the current binary +
// NN_CAP_* env, kept verbatim when the binary is gone.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readCodexTOML returns the generated .codex/config.toml as text.
func readCodexTOML(t *testing.T, wsPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wsPath, ".codex", "config.toml"))
	require.NoError(t, err, "read .codex/config.toml")
	return string(b)
}

// codexSectionBlock returns the raw text of [mcp_servers.<name>] plus its
// [mcp_servers.<name>.env] sub-table — the header line through to the next
// unrelated section header (or EOF). Empty when the section is absent.
func codexSectionBlock(toml, name string) string {
	start := strings.Index(toml, "[mcp_servers."+name+"]\n")
	if start < 0 {
		return ""
	}
	rest := toml[start:]
	offset := 0
	for {
		nl := strings.Index(rest[offset+1:], "\n[")
		if nl < 0 {
			return rest
		}
		nl += offset + 1
		if strings.HasPrefix(rest[nl+1:], "[mcp_servers."+name+".env]") {
			offset = nl
			continue
		}
		return rest[:nl+1]
	}
}

// writeCodexTOMListub writes a pre-existing .codex/config.toml (as if a
// previous projection or the user had left one).
func writeCodexTOMListub(t *testing.T, wsPath, content string) {
	t.Helper()
	dir := filepath.Join(wsPath, ".codex")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600))
}

// TestCodexVideoGen_ProjectsBinaryArgsAndCapabilityEnv covers the happy path:
// the scene declares video-gen, the module binary resolves, and the
// workspace's capability rows arrive as the NN_CAP_* env table — the same
// entry the Claude path projects into .mcp.json.
func TestCodexVideoGen_ProjectsBinaryArgsAndCapabilityEnv(t *testing.T) {
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

	res, err := gen.GenerateCodexConfigTomlWithExtras(wsDir,
		config.MCPGenerateOptions{WorkspaceID: ws.ID, SessionToken: "tok-1"},
		[]string{videoGenModule.Name}, "")
	require.NoError(t, err)
	assert.Contains(t, res.WrittenServers, videoGenModule.Name)
	assert.NotContains(t, res.Unavailable, videoGenModule.Name)

	toml := readCodexTOML(t, wsDir)
	block := codexSectionBlock(toml, videoGenModule.Name)
	require.NotEmpty(t, block, "declared video-gen must be projected into config.toml:\n%s", toml)
	assert.Contains(t, block, "command = "+tomlQuote(filepath.ToSlash(videoStub)))
	assert.Contains(t, block, "args = "+tomlStringArray([]string{"--workspace-dir", wsDir, "--data-dir", dataDir}))

	require.Contains(t, block, "[mcp_servers.video-gen.env]", "capability env must be injected:\n%s", block)
	assert.Contains(t, block, `NN_CAP_TTS_BACKEND = "openai-compat"`)
	assert.Contains(t, block, `NN_CAP_TTS_BASE_URL = "https://tts.example.com/v1"`)
	assert.Contains(t, block, `NN_CAP_TTS_API_KEY = "sk-test"`)
	assert.Contains(t, block, `NN_CAP_TTS_MODEL = "tts-1"`)

	assert.Contains(t, codexSectionBlock(toml, "niuniu"), `NIUNIU_MCP_TOKEN = "tok-1"`,
		"the niuniu base table must stay")
}

// TestCodexVideoGen_UnavailableWhenBinaryMissing covers the degrade path: the
// scene declares video-gen but the module binary is not installed. The name
// lands in Unavailable and no section is written, and the call still succeeds
// (design §7.1: 场景引用缺失模块 → 优雅降级，绝不报错).
func TestCodexVideoGen_UnavailableWhenBinaryMissing(t *testing.T) {
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
	// The base MCP must resolve (generation errors without it); the video one
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
	res, err := gen.GenerateCodexConfigTomlWithExtras(wsDir,
		config.MCPGenerateOptions{WorkspaceID: ws.ID}, []string{videoGenModule.Name}, "")
	require.NoError(t, err, "a missing module binary must degrade, not error")
	assert.Contains(t, res.Unavailable, videoGenModule.Name)
	assert.NotContains(t, res.WrittenServers, videoGenModule.Name)

	toml := readCodexTOML(t, wsDir)
	assert.NotContains(t, toml, "[mcp_servers.video-gen", "no section may be written for a missing module binary")
	assert.Contains(t, toml, "[mcp_servers.niuniu]")
}

// TestCodexVideoGen_ExtrasNilSpawnPreservesAndRefreshes covers the spawn path
// (extras == nil — the caller doesn't know the scene's MCP set): the
// video-gen section written by scene projection must survive, refreshed from
// the current capability config rather than carried forward stale.
func TestCodexVideoGen_ExtrasNilSpawnPreservesAndRefreshes(t *testing.T) {
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
	_, err := gen.GenerateCodexConfigTomlWithExtras(wsDir,
		config.MCPGenerateOptions{WorkspaceID: ws.ID, SessionToken: "tok-scene"},
		[]string{videoGenModule.Name}, "")
	require.NoError(t, err)
	require.Contains(t, readCodexTOML(t, wsDir), "[mcp_servers.video-gen]")

	// The user adds a video account mid-session; the next spawn regeneration
	// (extras == nil) must keep the section AND pick the new env up.
	_, err = capSvc.Create(ctx, CapabilityBackend{
		OwnerType: "user", OwnerID: 1, Module: videoGenModule.Name, Capability: "video",
		Backend: "seedance", Name: "我的视频", BaseURL: "https://ark.example.com",
		APIKey: "ak-test", Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, gen.GenerateCodexConfigToml(wsDir, config.MCPGenerateOptions{
		WorkspaceID: ws.ID, SessionToken: "tok-spawn",
	}))

	toml := readCodexTOML(t, wsDir)
	block := codexSectionBlock(toml, videoGenModule.Name)
	require.NotEmpty(t, block, "extras==nil spawn must preserve the video-gen section:\n%s", toml)
	assert.Contains(t, block, "command = "+tomlQuote(filepath.ToSlash(videoStub)))
	assert.Contains(t, block, "args = "+tomlStringArray([]string{"--workspace-dir", wsDir, "--data-dir", dataDir}))
	assert.Contains(t, block, `NN_CAP_VIDEO_BACKEND = "seedance"`,
		"the preserved section must be refreshed with the current capability env:\n%s", block)
	assert.Contains(t, block, `NN_CAP_VIDEO_API_KEY = "ak-test"`)

	niuniu := codexSectionBlock(toml, "niuniu")
	require.NotEmpty(t, niuniu, "the niuniu base table must always be written")
	assert.Contains(t, niuniu, "--workspace-id")
	assert.Contains(t, niuniu, `NIUNIU_MCP_TOKEN = "tok-spawn"`)
}

// TestCodexExtrasNilSpawn_KeepsHandWrittenServers: a hand-added [mcp_servers.*]
// section (with its env sub-table) survives an extras==nil regeneration
// verbatim, alongside the rewritten niuniu table.
func TestCodexExtrasNilSpawn_KeepsHandWrittenServers(t *testing.T) {
	parent := t.TempDir()
	plantStubBinary(t, parent, "niuniu-mcp")
	wsDir := t.TempDir()

	// A hand-edited file: a user-added fetch server with env, plus a stale
	// top-level key. Top-level keys are regenerated from opts only (the file
	// header documents that hand-added non-MCP content is wiped); the
	// [mcp_servers.*] sections are what must survive.
	writeCodexTOMListub(t, wsDir, strings.Join([]string{
		"# hand-written by the user",
		`sandbox_mode = "workspace-write"`,
		"",
		"[mcp_servers.fetch]",
		`command = "uvx"`,
		`args = ["mcp-server-fetch"]`,
		"",
		"[mcp_servers.fetch.env]",
		`FETCH_TIMEOUT = "30"`,
		"",
	}, "\n"))

	gen := newVideoGenTestGenerator(t, parent, t.TempDir())
	require.NoError(t, gen.GenerateCodexConfigToml(wsDir, config.MCPGenerateOptions{
		WorkspaceID: 1, SessionToken: "tok-1",
	}))

	toml := readCodexTOML(t, wsDir)
	block := codexSectionBlock(toml, "fetch")
	require.NotEmpty(t, block, "hand-written fetch must survive an extras==nil spawn:\n%s", toml)
	assert.Contains(t, block, `command = "uvx"`)
	assert.Contains(t, block, `args = ["mcp-server-fetch"]`)
	require.Contains(t, block, "[mcp_servers.fetch.env]")
	assert.Contains(t, block, `FETCH_TIMEOUT = "30"`)

	assert.Contains(t, codexSectionBlock(toml, "niuniu"), "--workspace-id")
	assert.NotContains(t, toml, `sandbox_mode = "workspace-write"`,
		"top-level keys are regenerated from opts, never carried over")
}

// TestCodexExtrasNilSpawn_TopLevelSettingsStayAtHead: sandbox_mode /
// approval_policy are written before the first [mcp_servers.*] table (TOML
// top-level keys must precede tables), and preserved sections are emitted
// after them.
func TestCodexExtrasNilSpawn_TopLevelSettingsStayAtHead(t *testing.T) {
	parent := t.TempDir()
	plantStubBinary(t, parent, "niuniu-mcp")
	wsDir := t.TempDir()
	// Seed a section so the preservation path actually emits text too.
	writeCodexTOMListub(t, wsDir, "[mcp_servers.fetch]\ncommand = \"uvx\"\n")

	gen := newVideoGenTestGenerator(t, parent, t.TempDir())
	require.NoError(t, gen.GenerateCodexConfigToml(wsDir, config.MCPGenerateOptions{
		WorkspaceID:         1,
		SessionToken:        "tok-1",
		CodexSandboxMode:    "read-only",
		CodexApprovalPolicy: "on-request",
	}))

	toml := readCodexTOML(t, wsDir)
	sandboxIdx := strings.Index(toml, `sandbox_mode = "read-only"`)
	approvalIdx := strings.Index(toml, `approval_policy = "on-request"`)
	firstSectionIdx := strings.Index(toml, "[mcp_servers.")
	require.NotEqual(t, -1, sandboxIdx, "sandbox_mode missing:\n%s", toml)
	require.NotEqual(t, -1, approvalIdx, "approval_policy missing:\n%s", toml)
	require.NotEqual(t, -1, firstSectionIdx, "no mcp_servers section written:\n%s", toml)
	assert.Less(t, sandboxIdx, firstSectionIdx, "sandbox_mode must precede the first table")
	assert.Less(t, approvalIdx, firstSectionIdx, "approval_policy must precede the first table")
	assert.NotEmpty(t, codexSectionBlock(toml, "fetch"), "preserved section must not be lost")
}

// TestCodexVideoGen_ExtrasNilSpawnKeepsSectionWhenBinaryGone: the module
// binary disappeared but the workspace still carries a projected section — the
// spawn regeneration keeps the previous text verbatim instead of silently
// stripping the tool group.
func TestCodexVideoGen_ExtrasNilSpawnKeepsSectionWhenBinaryGone(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	baseDir := filepath.Join(root, "deep", "a", "b", "c", "workspaces")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	binDir := filepath.Join(root, "deep", "a", "b", "c", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	baseName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		baseName += ".exe"
	}
	require.NoError(t, os.WriteFile(filepath.Join(binDir, baseName), []byte("stub"), 0o755))

	wsDir := t.TempDir()
	writeCodexTOMListub(t, wsDir, "[mcp_servers.video-gen]\ncommand = \"/opt/stale/niuniu-video-mcp\"\nargs = [\"--workspace-dir\", \"x\"]\n")

	cfg := &config.Config{DataDir: t.TempDir()}
	cfg.Workspace.BaseDir = baseDir
	gen := NewMCPConfigGenerator(cfg)

	require.NoError(t, gen.GenerateCodexConfigToml(wsDir, config.MCPGenerateOptions{
		WorkspaceID: 1, SessionToken: "tok-1",
	}))

	toml := readCodexTOML(t, wsDir)
	block := codexSectionBlock(toml, videoGenModule.Name)
	require.NotEmpty(t, block, "a section whose module binary is gone must be kept verbatim:\n%s", toml)
	assert.Contains(t, block, `command = "/opt/stale/niuniu-video-mcp"`)
}
