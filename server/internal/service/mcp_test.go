package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// TestFindLatestBinaryInDir_HostMatchPrefersArch covers the multi-arch
// regression scenario: `make build-mac` drops both darwin-arm64 and
// darwin-amd64 binaries in bin/, and the lookup must pick the host arch
// regardless of which file was written most recently.
func TestFindLatestBinaryInDir_HostMatchPrefersArch(t *testing.T) {
	dir := t.TempDir()
	hostMatch := "darwin-arm64"

	otherArch := filepath.Join(dir, "niuniu-mcp-v1.0.7-darwin-amd64")
	hostArch := filepath.Join(dir, "niuniu-mcp-v1.0.7-darwin-arm64")
	writeFile(t, otherArch)
	writeFile(t, hostArch)
	// Make the OTHER arch newer to prove mtime alone wouldn't pick correctly.
	future := time.Now().Add(1 * time.Hour)
	if err := os.Chtimes(otherArch, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got := findLatestBinaryInDir(dir, "niuniu-mcp", "", hostMatch)
	if got != hostArch {
		t.Errorf("want %q (host arch), got %q", hostArch, got)
	}
}

// TestFindLatestBinaryInDir_HostMatchFallback covers personal-mode cache
// (niuniu-mcp-<hash>.exe) and host-only `make build-mcp` output
// (niuniu-mcp-vX.Y.Z) — names that contain neither GOOS nor GOARCH. The
// hostMatch filter must fall back to "newest among all" so these layouts
// keep working.
func TestFindLatestBinaryInDir_HostMatchFallback(t *testing.T) {
	t.Run("hash-suffix cache file (personal mode)", func(t *testing.T) {
		dir := t.TempDir()
		cached := filepath.Join(dir, "niuniu-mcp-deadbeef1234abcd.exe")
		writeFile(t, cached)

		got := findLatestBinaryInDir(dir, "niuniu-mcp", ".exe", "windows-amd64")
		if got != cached {
			t.Errorf("want fallback to %q, got %q", cached, got)
		}
	})

	t.Run("host-only make build-mcp output", func(t *testing.T) {
		dir := t.TempDir()
		host := filepath.Join(dir, "niuniu-mcp-v1.0.7")
		writeFile(t, host)

		got := findLatestBinaryInDir(dir, "niuniu-mcp", "", "linux-amd64")
		if got != host {
			t.Errorf("want fallback to %q, got %q", host, got)
		}
	})
}

// TestFindLatestBinaryInDir_HostMatchPicksLatest covers the case where
// multiple host-matching candidates exist (e.g. a stale v1.0.6 left over
// from a previous build alongside v1.0.7) — within the host-matching
// subset, mtime tiebreaks to the newer one.
func TestFindLatestBinaryInDir_HostMatchPicksLatest(t *testing.T) {
	dir := t.TempDir()
	hostMatch := "linux-amd64"

	old := filepath.Join(dir, "niuniu-mcp-v1.0.6-linux-amd64")
	new := filepath.Join(dir, "niuniu-mcp-v1.0.7-linux-amd64")
	writeFile(t, old)
	writeFile(t, new)
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got := findLatestBinaryInDir(dir, "niuniu-mcp", "", hostMatch)
	if got != new {
		t.Errorf("want newer host-matching %q, got %q", new, got)
	}
}

// TestFindLatestBinaryInDir_LegacyTimestampNaming guards against breaking
// installations that still have the pre-refactor filename layout
// (niuniu-mcp-darwin-arm64-20260506045927) sitting in bin/. The hostMatch
// substring "darwin-arm64" still appears, just in a different position,
// so the filter MUST still match these files.
func TestFindLatestBinaryInDir_LegacyTimestampNaming(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "niuniu-mcp-darwin-arm64-20260506045927")
	writeFile(t, legacy)

	got := findLatestBinaryInDir(dir, "niuniu-mcp", "", "darwin-arm64")
	if got != legacy {
		t.Errorf("want legacy %q to match host-arch filter, got %q", legacy, got)
	}
}

// TestFindLatestBinaryInDir_EmptyDir guards the empty-dir case so the
// function returns "" rather than panicking on Stat of a missing pattern.
func TestFindLatestBinaryInDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if got := findLatestBinaryInDir(dir, "niuniu-mcp", "", "linux-amd64"); got != "" {
		t.Errorf("want empty string for empty dir, got %q", got)
	}
}

// TestFindLatestBinaryInDir_EmptyHostMatch falls back to the legacy
// "newest among all" semantics — useful as a safety hatch and exercised
// when callers haven't supplied a hint.
func TestFindLatestBinaryInDir_EmptyHostMatch(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "niuniu-mcp-v1.0.6-linux-amd64")
	b := filepath.Join(dir, "niuniu-mcp-v1.0.7-darwin-arm64")
	writeFile(t, a)
	writeFile(t, b)
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(a, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got := findLatestBinaryInDir(dir, "niuniu-mcp", "", "")
	if got != b {
		t.Errorf("want newest %q without filter, got %q", b, got)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// generateAndReadAPIBase runs Generate against a workspace dir and returns
// the --api-base value the generator wrote into .mcp.json. mcpBin must exist
// (Generate looks it up via FindMCPBinary), so the helper plants a stub
// niuniu-mcp under <parent>/bin/ and points cfg.Workspace.BaseDir at a
// sibling so the generator's "walk up from BaseDir for bin/" branch finds it.
func generateAndReadAPIBase(t *testing.T, cfg *config.Config) string {
	t.Helper()
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg.Workspace.BaseDir = wsBase
	gen := NewMCPConfigGenerator(cfg)
	if _, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: 42}, nil, ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var got struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal .mcp.json: %v", err)
	}
	args := got.MCPServers["niuniu"].Args
	for i, a := range args {
		if a == "--api-base" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("--api-base not found in args: %v", args)
	return ""
}

// TestGenerate_DisableToolGroupsFlag verifies a scene's disabled tool groups
// are projected into the niuniu-mcp launch args as --disable-tool-groups, so
// the gated groups never register for that workspace's agent.
func TestGenerate_DisableToolGroupsFlag(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg := &config.Config{}
	cfg.Workspace.BaseDir = wsBase
	gen := NewMCPConfigGenerator(cfg)
	opts := config.MCPGenerateOptions{WorkspaceID: 42, DisableToolGroups: []string{"multi-agent", "harness"}}
	if _, err := gen.Generate(wsDir, opts, nil, ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var got struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal .mcp.json: %v", err)
	}
	args := got.MCPServers["niuniu"].Args
	found := ""
	for i, a := range args {
		if a == "--disable-tool-groups" && i+1 < len(args) {
			found = args[i+1]
		}
	}
	if found != "multi-agent,harness" {
		t.Fatalf("expected --disable-tool-groups multi-agent,harness, got %q in args %v", found, args)
	}
}

// TestGenerate_EnableToolGroupsFlag verifies a scene's opt-in enabled tool
// groups are projected into the niuniu-mcp launch args as --enable-tool-groups,
// so an OFF-by-default group (browser-history) registers only for that workspace.
func TestGenerate_EnableToolGroupsFlag(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg := &config.Config{}
	cfg.Workspace.BaseDir = wsBase
	gen := NewMCPConfigGenerator(cfg)
	opts := config.MCPGenerateOptions{WorkspaceID: 42, EnableToolGroups: []string{"browser-history"}}
	if _, err := gen.Generate(wsDir, opts, nil, ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var got struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal .mcp.json: %v", err)
	}
	args := got.MCPServers["niuniu"].Args
	found := ""
	for i, a := range args {
		if a == "--enable-tool-groups" && i+1 < len(args) {
			found = args[i+1]
		}
	}
	if found != "browser-history" {
		t.Fatalf("expected --enable-tool-groups browser-history, got %q in args %v", found, args)
	}
}

// TestGenerate_NoDisableToolGroupsFlagWhenEmpty ensures the flag is omitted when
// no groups are disabled (every group registers by default).
func TestGenerate_NoDisableToolGroupsFlagWhenEmpty(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg := &config.Config{}
	cfg.Workspace.BaseDir = wsBase
	gen := NewMCPConfigGenerator(cfg)
	if _, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: 42}, nil, ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	if strings.Contains(string(data), "--disable-tool-groups") {
		t.Fatalf("did not expect --disable-tool-groups in .mcp.json: %s", data)
	}
}

// TestGenerate_PreservesExistingTokenWhenNoneSupplied guards the 401 regression:
// scene re-projection regenerates .mcp.json with an empty SessionToken, and must
// NOT strip the niuniu env.NIUNIU_MCP_TOKEN that a prior agent-start write put
// there — otherwise every /mcp call 401s.
func TestGenerate_PreservesExistingTokenWhenNoneSupplied(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg := &config.Config{}
	cfg.Workspace.BaseDir = wsBase
	gen := NewMCPConfigGenerator(cfg)

	// 1. Agent-start write: token present.
	if _, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: 7, SessionToken: "tok-abc"}, nil, ""); err != nil {
		t.Fatalf("Generate (with token): %v", err)
	}
	// 2. Scene re-projection: empty token + a disabled group.
	if _, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: 7, DisableToolGroups: []string{"harness"}}, nil, ""); err != nil {
		t.Fatalf("Generate (no token): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var got struct {
		MCPServers struct {
			Niuniu struct {
				Env struct {
					Token string `json:"NIUNIU_MCP_TOKEN"`
				} `json:"env"`
			} `json:"niuniu"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MCPServers.Niuniu.Env.Token != "tok-abc" {
		t.Fatalf("token not preserved across re-projection: got %q, want tok-abc; file=%s", got.MCPServers.Niuniu.Env.Token, data)
	}
}

// TestGenerate_InjectsTokenAlongsideSceneServer guards the assistant 401
// regression: when a scene has projected a non-niuniu MCP server into .mcp.json,
// the proxy/agent token-refresh call (extras==nil, SessionToken set) must STILL
// write the niuniu env token AND keep the scene server — not bail out and leave
// the agent token-less.
func TestGenerate_InjectsTokenAlongsideSceneServer(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg := &config.Config{}
	cfg.Workspace.BaseDir = wsBase
	// Pre-seed .mcp.json as scene projection would: niuniu (token-less) + a
	// scene filesystem MCP server.
	seed := `{"mcpServers":{"niuniu":{"command":"old","args":[]},"filesystem":{"command":"npx","args":["-y","fs"]}}}`
	if err := os.WriteFile(filepath.Join(wsDir, ".mcp.json"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed .mcp.json: %v", err)
	}
	gen := NewMCPConfigGenerator(cfg)
	// Proxy/agent token-refresh: extras==nil, token supplied.
	if _, err := gen.Generate(wsDir, config.MCPGenerateOptions{WorkspaceID: 7, SessionToken: "tok-xyz"}, nil, ""); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got struct {
		MCPServers map[string]struct {
			Env struct {
				Token string `json:"NIUNIU_MCP_TOKEN"`
			} `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MCPServers["niuniu"].Env.Token != "tok-xyz" {
		t.Fatalf("niuniu token not injected: %s", data)
	}
	if _, ok := got.MCPServers["filesystem"]; !ok {
		t.Fatalf("scene filesystem server dropped: %s", data)
	}
}

// TestSetWorkspaceKBReadonly covers the KB base4 read-only enforcement: bound
// dataset roots get write-deny rules in .claude/settings.json, user-authored
// deny entries survive, the set fully recomputes on rebind, and clearing
// removes only our managed entries.
func TestSetWorkspaceKBReadonly(t *testing.T) {
	wsDir := t.TempDir()
	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: t.TempDir()}}
	gen := NewMCPConfigGenerator(cfg)
	settingsPath := filepath.Join(wsDir, ".claude", "settings.json")

	// Seed a user-authored deny we must never clobber.
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userSeed := `{"permissions":{"deny":["Bash(rm -rf /)"]}}`
	if err := os.WriteFile(settingsPath, []byte(userSeed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	readPerms := func() (deny, dirs []string) {
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("read settings.json: %v", err)
		}
		var got struct {
			Permissions struct {
				Deny []string `json:"deny"`
				Dirs []string `json:"additionalDirectories"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return got.Permissions.Deny, got.Permissions.Dirs
	}
	readDeny := func() []string { d, _ := readPerms(); return d }
	has := func(deny []string, want string) bool {
		for _, d := range deny {
			if d == want {
				return true
			}
		}
		return false
	}

	rootA := filepath.Join(t.TempDir(), "kbA")
	rootB := filepath.Join(t.TempDir(), "kbB")
	globA := kbDenyPath(rootA)
	globB := kbDenyPath(rootB)
	// The deny glob must be //-anchored (absolute), not /-anchored (project root).
	if !strings.HasPrefix(globA, "//") {
		t.Fatalf("deny glob not absolute-anchored: %q", globA)
	}

	if err := gen.SetWorkspaceKBReadonly(wsDir, []string{rootA, rootB}); err != nil {
		t.Fatalf("SetWorkspaceKBReadonly: %v", err)
	}
	deny, dirs := readPerms()
	for _, tool := range kbReadonlyTools {
		if !has(deny, tool+"("+globA+")") || !has(deny, tool+"("+globB+")") {
			t.Fatalf("missing %s deny for kb roots: %+v", tool, deny)
		}
	}
	if !has(deny, "Bash(rm -rf /)") {
		t.Fatalf("user-authored deny was clobbered: %+v", deny)
	}
	// Read access granted via additionalDirectories (not --add-dir): the raw
	// absolute roots, not the deny globs.
	if !has(dirs, filepath.Clean(rootA)) || !has(dirs, filepath.Clean(rootB)) {
		t.Fatalf("kb roots not granted via additionalDirectories: %+v", dirs)
	}

	// Idempotent: a second identical call doesn't duplicate entries.
	if err := gen.SetWorkspaceKBReadonly(wsDir, []string{rootA, rootB}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := len(readDeny()); got != 1+2*len(kbReadonlyTools) {
		t.Fatalf("idempotency broke deny count: got %d (%+v)", got, readDeny())
	}

	// Rebind to only rootA: rootB's managed entries must be dropped, rootA kept,
	// user deny preserved.
	if err := gen.SetWorkspaceKBReadonly(wsDir, []string{rootA}); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	deny = readDeny()
	if has(deny, "Write("+globB+")") {
		t.Fatalf("stale rootB deny not pruned: %+v", deny)
	}
	if !has(deny, "Write("+globA+")") || !has(deny, "Bash(rm -rf /)") {
		t.Fatalf("rebind dropped rootA or user deny: %+v", deny)
	}

	// Clear: all managed entries gone, user deny survives.
	if err := gen.SetWorkspaceKBReadonly(wsDir, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	deny, dirs = readPerms()
	if has(deny, "Write("+globA+")") || len(dirs) != 0 {
		t.Fatalf("managed entries not cleared: deny=%+v dirs=%+v", deny, dirs)
	}
	if !has(deny, "Bash(rm -rf /)") {
		t.Fatalf("clear removed user deny: %+v", deny)
	}
}

// TestGenerateClaudeSettings_WritesHookConfig is the happy-path coverage
// for the .claude/settings.json injection: hooks for WorktreeCreate and
// WorktreeRemove are written, both pointing at the niuniu-mcp binary
// resolved via the same FindMCPBinary path .mcp.json uses.
func TestGenerateClaudeSettings_WritesHookConfig(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	stubPath := filepath.Join(binDir, stubName)
	writeFile(t, stubPath)
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}

	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	gen := NewMCPConfigGenerator(cfg)
	if err := gen.GenerateClaudeSettings(wsDir); err != nil {
		t.Fatalf("GenerateClaudeSettings: %v", err)
	}

	settingsPath := filepath.Join(wsDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var got struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal settings.json: %v", err)
	}

	for _, event := range []string{"WorktreeCreate", "WorktreeRemove"} {
		entries, ok := got.Hooks[event]
		if !ok || len(entries) == 0 || len(entries[0].Hooks) == 0 {
			t.Fatalf("settings.json missing %s hook entry", event)
		}
		hook := entries[0].Hooks[0]
		if hook.Type != "command" {
			t.Errorf("%s hook type want command, got %q", event, hook.Type)
		}
		// Hook command must reference the resolved niuniu-mcp binary by
		// absolute (forward-slash) path so cwd changes between Claude
		// invocations don't break it.
		expectFragment := filepath.ToSlash(stubPath)
		if !strings.Contains(hook.Command, expectFragment) {
			t.Errorf("%s hook command want fragment %q, got %q", event, expectFragment, hook.Command)
		}
		var sub string
		switch event {
		case "WorktreeCreate":
			sub = "worktree-create"
		case "WorktreeRemove":
			sub = "worktree-remove"
		}
		if !strings.Contains(hook.Command, sub) {
			t.Errorf("%s hook command want subcommand %q, got %q", event, sub, hook.Command)
		}
	}

	// PreToolUse on Read must reroute through the same binary via the
	// `read-hook` subcommand, scoped to the Read tool by matcher.
	var pre struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &pre); err != nil {
		t.Fatalf("unmarshal PreToolUse: %v", err)
	}
	if len(pre.Hooks.PreToolUse) == 0 || len(pre.Hooks.PreToolUse[0].Hooks) == 0 {
		t.Fatalf("settings.json missing PreToolUse hook entry")
	}
	entry := pre.Hooks.PreToolUse[0]
	if entry.Matcher != "Read" {
		t.Errorf("PreToolUse matcher want Read, got %q", entry.Matcher)
	}
	if cmd := entry.Hooks[0].Command; !strings.Contains(cmd, "read-hook") ||
		!strings.Contains(cmd, filepath.ToSlash(stubPath)) {
		t.Errorf("PreToolUse command want read-hook + %q, got %q", filepath.ToSlash(stubPath), cmd)
	}
}

// newClaudeSettingsGen builds an MCPConfigGenerator whose FindMCPBinary
// resolves to a planted stub niuniu-mcp, returning the generator, the
// workspace dir, and the stub's forward-slash path (as it appears in hook
// commands).
func newClaudeSettingsGen(t *testing.T) (*MCPConfigGenerator, string, string) {
	t.Helper()
	wsDir := t.TempDir()
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	stubPath := filepath.Join(binDir, stubName)
	writeFile(t, stubPath)
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	return NewMCPConfigGenerator(cfg), wsDir, filepath.ToSlash(stubPath)
}

// TestGenerateClaudeSettings_MergesAndIsIdempotent verifies the merge behavior:
// an existing settings.json keeps its user content AND gains the managed hooks,
// and a second call is a byte-identical no-op (no duplication, no churn).
func TestGenerateClaudeSettings_MergesAndIsIdempotent(t *testing.T) {
	gen, wsDir, _ := newClaudeSettingsGen(t)
	settingsPath := filepath.Join(wsDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := gen.GenerateClaudeSettings(wsDir); err != nil {
		t.Fatalf("GenerateClaudeSettings: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("merged settings not valid JSON: %v", err)
	}
	if merged["theme"] != "dark" {
		t.Errorf("user key 'theme' lost after merge: %v", merged["theme"])
	}
	hooks, _ := merged["hooks"].(map[string]any)
	for _, ev := range []string{"WorktreeCreate", "WorktreeRemove", "PreToolUse"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("managed hook %q not added on merge", ev)
		}
	}

	// Second call must be a byte-identical no-op.
	if err := gen.GenerateClaudeSettings(wsDir); err != nil {
		t.Fatalf("GenerateClaudeSettings (2nd): %v", err)
	}
	again, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read after 2nd call: %v", err)
	}
	if string(again) != string(data) {
		t.Errorf("second call mutated settings.json:\nfirst:\n%s\nsecond:\n%s", data, again)
	}
}

// TestGenerateClaudeSettings_PreservesUserHooks ensures merging into a file
// that already has a user-defined PreToolUse Bash matcher adds the managed
// PreToolUse Read hook without disturbing the user's Bash matcher.
func TestGenerateClaudeSettings_PreservesUserHooks(t *testing.T) {
	gen, wsDir, _ := newClaudeSettingsGen(t)
	settingsPath := filepath.Join(wsDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	seed := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"my-guard"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := gen.GenerateClaudeSettings(wsDir); err != nil {
		t.Fatalf("GenerateClaudeSettings: %v", err)
	}

	var got struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	data, _ := os.ReadFile(settingsPath)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var sawBash, sawRead bool
	for _, e := range got.Hooks.PreToolUse {
		switch e.Matcher {
		case "Bash":
			if len(e.Hooks) == 1 && e.Hooks[0].Command == "my-guard" {
				sawBash = true
			}
		case "Read":
			if len(e.Hooks) == 1 && strings.Contains(e.Hooks[0].Command, "read-hook") {
				sawRead = true
			}
		}
	}
	if !sawBash {
		t.Error("user's PreToolUse Bash matcher was not preserved")
	}
	if !sawRead {
		t.Error("managed PreToolUse Read hook was not added")
	}
}

// TestGenerateClaudeSettings_RefreshesStaleBinary verifies a managed hook
// pointing at a prior build's binary is updated in place to the current binary,
// with no duplicate entry created.
func TestGenerateClaudeSettings_RefreshesStaleBinary(t *testing.T) {
	gen, wsDir, stubSlash := newClaudeSettingsGen(t)
	settingsPath := filepath.Join(wsDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	seed := `{"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"\"C:/old/niuniu-mcp-deadbeef.exe\" read-hook"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := gen.GenerateClaudeSettings(wsDir); err != nil {
		t.Fatalf("GenerateClaudeSettings: %v", err)
	}

	var got struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	data, _ := os.ReadFile(settingsPath)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var readEntries int
	for _, e := range got.Hooks.PreToolUse {
		if e.Matcher != "Read" {
			continue
		}
		readEntries++
		if len(e.Hooks) != 1 || !strings.Contains(e.Hooks[0].Command, stubSlash) {
			t.Errorf("Read hook not refreshed to current binary: %+v", e.Hooks)
		}
		if len(e.Hooks) == 1 && strings.Contains(e.Hooks[0].Command, "deadbeef") {
			t.Errorf("stale binary path still present: %q", e.Hooks[0].Command)
		}
	}
	if readEntries != 1 {
		t.Errorf("want exactly 1 Read matcher entry (refreshed in place), got %d", readEntries)
	}
}

// TestGenerateClaudeSettings_NoBinaryReturnsError covers the fail-loud
// branch: when no niuniu-mcp binary can be located, the generator must
// not write a half-broken settings.json that points at a nonexistent
// command — Claude Code would fail at hook-fire time with a less
// diagnosable error.
func TestGenerateClaudeSettings_NoBinaryReturnsError(t *testing.T) {
	wsDir := t.TempDir()
	parent := t.TempDir()
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}
	// Note: no bin/ created — FindMCPBinary should fail every fallback.
	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	gen := NewMCPConfigGenerator(cfg)

	// Ensure PATH lookup also fails by clearing PATH for this test.
	t.Setenv("PATH", "")

	if err := gen.GenerateClaudeSettings(wsDir); err == nil {
		t.Fatal("want error, got nil")
	}
	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "settings.json")); err == nil {
		t.Fatal(".claude/settings.json was written despite missing binary")
	}
}

func writeLockfile(t *testing.T, dataDir, addr string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir dataDir: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"pid":     12345,
		"addr":    addr,
		"version": "v1.0.7",
	})
	if err := os.WriteFile(filepath.Join(dataDir, "server.lock"), body, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
}

// TestMCPGenerator_PrefersLockfilePort is the regression test for the personal-
// edition bug: server is launched in embedded mode with --addr 127.0.0.1:0,
// listens on a random port (e.g. 58411), and writes that port to server.lock.
// cfg.Server.Port still says 3000 (unchanged from config.yaml), but the
// generator MUST emit the actual listening port — otherwise every workspace
// gets a .mcp.json that points at a port nothing is listening on.
func TestMCPGenerator_PrefersLockfilePort(t *testing.T) {
	dataDir := t.TempDir()
	writeLockfile(t, dataDir, "127.0.0.1:58411")

	cfg := &config.Config{
		Server:  config.ServerConfig{Port: 3000, Host: "0.0.0.0"},
		DataDir: dataDir,
	}
	got := generateAndReadAPIBase(t, cfg)
	want := "http://127.0.0.1:58411"
	if got != want {
		t.Errorf("api-base want %q (lockfile port wins), got %q", want, got)
	}
}

// TestMCPGenerator_FallsBackToConfigPort covers the standalone-server path:
// no lockfile (or one that was never written), use the configured port.
func TestMCPGenerator_FallsBackToConfigPort(t *testing.T) {
	dataDir := t.TempDir() // no lockfile created

	cfg := &config.Config{
		Server:  config.ServerConfig{Port: 4242, Host: "0.0.0.0"},
		DataDir: dataDir,
	}
	got := generateAndReadAPIBase(t, cfg)
	want := "http://127.0.0.1:4242"
	if got != want {
		t.Errorf("api-base want %q (config fallback), got %q", want, got)
	}
}

// TestMCPGenerator_FallsBackOnInvalidLockfile guards against a corrupt or
// partially-written lockfile silently routing every workspace to a bogus
// port. Behavior must match the no-lockfile case: fall through to cfg.
func TestMCPGenerator_FallsBackOnInvalidLockfile(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "server.lock"), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write bad lockfile: %v", err)
	}

	cfg := &config.Config{
		Server:  config.ServerConfig{Port: 4242, Host: "0.0.0.0"},
		DataDir: dataDir,
	}
	got := generateAndReadAPIBase(t, cfg)
	want := "http://127.0.0.1:4242"
	if got != want {
		t.Errorf("api-base want %q (invalid lockfile -> fallback), got %q", want, got)
	}
}

// TestMCPGenerator_HandlesIPv6LockfileAddr — when the server is bound to [::]
// the lockfile records "[::]:58411" form. SplitHostPort handles this; we
// must extract the port and still emit 127.0.0.1:port (loopback IPC, never
// 0.0.0.0/[::] which aren't connectable in the spec).
func TestMCPGenerator_HandlesIPv6LockfileAddr(t *testing.T) {
	dataDir := t.TempDir()
	writeLockfile(t, dataDir, "[::]:58411")

	cfg := &config.Config{
		Server:  config.ServerConfig{Port: 3000, Host: "0.0.0.0"},
		DataDir: dataDir,
	}
	got := generateAndReadAPIBase(t, cfg)
	want := "http://127.0.0.1:58411"
	if got != want {
		t.Errorf("api-base want %q (ipv6 lockfile addr -> v4 loopback), got %q", want, got)
	}
}

// TestMCPConfigGenerator_Generate_WithExtras is the happy-path coverage for
// the per-workspace MCP feature: Generate must look up `extras` against
// the Claude install's registry (rooted at configDir), emit the resolved
// ones into .mcp.json alongside niuniu, and surface any unknown names via
// GenerateResult.Unavailable rather than silently dropping or failing.
func TestMCPConfigGenerator_Generate_WithExtras(t *testing.T) {
	wsDir := t.TempDir()

	// Build a fake Claude install with two global MCPs.
	configRoot := t.TempDir()
	mustWriteJSON(t, filepath.Join(configRoot, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{
			"context7":   map[string]any{"command": "npx", "args": []string{"-y", "@upstash/context7-mcp"}},
			"playwright": map[string]any{"command": "npx", "args": []string{"@playwright/mcp@latest"}},
		},
	})

	// Plant a stub niuniu-mcp binary so FindMCPBinary's walk-up-from-BaseDir
	// branch resolves successfully — same shape as generateAndReadAPIBase.
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}

	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	g := NewMCPConfigGenerator(cfg)
	g.SetRegistry(NewClaudeMCPRegistry())

	res, err := g.Generate(
		wsDir,
		config.MCPGenerateOptions{WorkspaceID: 1},
		[]string{"playwright", "nonexistent"},
		configRoot,
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(res.WrittenServers) != 2 {
		t.Errorf("WrittenServers = %v, want 2 entries (niuniu + playwright)", res.WrittenServers)
	}
	if !slices.Contains(res.WrittenServers, "niuniu") {
		t.Errorf("WrittenServers = %v, want to include niuniu", res.WrittenServers)
	}
	if !slices.Contains(res.WrittenServers, "playwright") {
		t.Errorf("WrittenServers = %v, want to include playwright", res.WrittenServers)
	}
	if !slices.Contains(res.Unavailable, "nonexistent") {
		t.Errorf("Unavailable = %v, want to include 'nonexistent'", res.Unavailable)
	}

	b, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.MCPServers["niuniu"]; !ok {
		t.Errorf(".mcp.json must always contain niuniu; got %v", doc.MCPServers)
	}
	if _, ok := doc.MCPServers["playwright"]; !ok {
		t.Errorf(".mcp.json should contain playwright (resolved via registry); got %v", doc.MCPServers)
	}
	if _, ok := doc.MCPServers["nonexistent"]; ok {
		t.Errorf(".mcp.json must NOT contain nonexistent (no registry entry); got %v", doc.MCPServers)
	}

	// Stage-and-swap leaves no .tmp-* turds behind.
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		t.Fatalf("readdir wsDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mcp.json.tmp-") {
			t.Errorf("stage-and-swap leaked temp file %q", e.Name())
		}
	}
}

// Inline ExtraMCPServers (scene-authored JSON snippets) are written verbatim
// into .mcp.json without any registry lookup, and a name carried only as an
// inline config is never marked Unavailable.
func TestMCPConfigGenerator_Generate_InlineConfig(t *testing.T) {
	wsDir := t.TempDir()
	configRoot := t.TempDir() // empty registry — nothing installed
	mustWriteJSON(t, filepath.Join(configRoot, ".claude.json"), map[string]any{"mcpServers": map[string]any{}})

	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}

	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	g := NewMCPConfigGenerator(cfg)
	g.SetRegistry(NewClaudeMCPRegistry())

	inline := map[string]any{"command": "npx", "args": []any{"-y", "my-pkg"}, "env": map[string]any{"K": "v"}}
	res, err := g.Generate(
		wsDir,
		config.MCPGenerateOptions{WorkspaceID: 1, ExtraMCPServers: map[string]map[string]any{"my-server": inline}},
		[]string{"my-server"},
		configRoot,
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if slices.Contains(res.Unavailable, "my-server") {
		t.Errorf("inline-config server must NOT be Unavailable; got %v", res.Unavailable)
	}
	if !slices.Contains(res.WrittenServers, "my-server") {
		t.Errorf("WrittenServers = %v, want to include my-server", res.WrittenServers)
	}

	b, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	got, ok := doc.MCPServers["my-server"].(map[string]any)
	if !ok {
		t.Fatalf(".mcp.json must contain my-server written verbatim; got %v", doc.MCPServers)
	}
	if got["command"] != "npx" {
		t.Errorf("inline config not written verbatim: %v", got)
	}
}

func TestMCPConfigGenerator_GenerateCodexConfigTomlWithExtras(t *testing.T) {
	wsDir := t.TempDir()
	configRoot := t.TempDir()
	mustWriteJSON(t, filepath.Join(configRoot, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{
			"context7": map[string]any{
				"command": "npx",
				"args":    []string{"-y", "@upstash/context7-mcp"},
				"env":     map[string]string{"CONTEXT7_API_KEY": "test-key"},
			},
		},
	})

	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}

	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	g := NewMCPConfigGenerator(cfg)
	g.SetRegistry(NewClaudeMCPRegistry())

	res, err := g.GenerateCodexConfigTomlWithExtras(
		wsDir,
		config.MCPGenerateOptions{WorkspaceID: 7, InboxDir: filepath.Join(wsDir, ".team", "inboxes")},
		[]string{"context7", "missing"},
		configRoot,
	)
	if err != nil {
		t.Fatalf("GenerateCodexConfigTomlWithExtras: %v", err)
	}
	if !slices.Contains(res.WrittenServers, "niuniu") || !slices.Contains(res.WrittenServers, "context7") {
		t.Fatalf("WrittenServers = %v, want niuniu and context7", res.WrittenServers)
	}
	if !slices.Contains(res.Unavailable, "missing") {
		t.Fatalf("Unavailable = %v, want missing", res.Unavailable)
	}

	b, err := os.ReadFile(filepath.Join(wsDir, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"[mcp_servers.niuniu]",
		"--workspace-id",
		"[mcp_servers.context7]",
		`command = "npx"`,
		`args = ["-y", "@upstash/context7-mcp"]`,
		"[mcp_servers.context7.env]",
		`CONTEXT7_API_KEY = "test-key"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.toml missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[mcp_servers.missing]") {
		t.Errorf("config.toml must not contain unresolved MCP server:\n%s", got)
	}
}

func TestMCPConfigGenerator_GenerateCodexConfigArgs(t *testing.T) {
	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		t.Fatalf("mkdir wsBase: %v", err)
	}

	g := NewMCPConfigGenerator(&config.Config{
		Workspace: config.WorkspaceConfig{BaseDir: wsBase},
		Server:    config.ServerConfig{Port: 4321},
	})
	args, err := g.GenerateCodexConfigArgs(config.MCPGenerateOptions{
		ProjectID:           3,
		WorkspaceID:         7,
		InboxDir:            filepath.Join(parent, "inbox"),
		SessionToken:        "tok-123",
		CodexApprovalPolicy: "never",
	})
	if err != nil {
		t.Fatalf("GenerateCodexConfigArgs: %v", err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"--config",
		"mcp_servers.niuniu.command=",
		`"--api-base", "http://127.0.0.1:4321"`,
		`"--workspace-id", "7"`,
		"mcp_servers.niuniu.env.NIUNIU_MCP_TOKEN=\"tok-123\"",
		"approval_policy=\"never\"",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("config args missing %q:\n%v", want, args)
		}
	}
}

// TestMCPConfigGenerator_Generate_PreservesHandEditedFile guards the
// extras==nil path: a pre-existing non-niuniu entry (hand-edited or
// scene-projected) must be PRESERVED, while the niuniu entry is still
// (re)written so the session token is injected — it must not be dropped.
func TestMCPConfigGenerator_Generate_PreservesHandEditedFile(t *testing.T) {
	wsDir := t.TempDir()
	existing := []byte(`{"mcpServers":{"custom-mcp":{"command":"foo","args":["bar"]}}}`)
	if err := os.WriteFile(filepath.Join(wsDir, ".mcp.json"), existing, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	_ = os.MkdirAll(wsBase, 0o755)

	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	g := NewMCPConfigGenerator(cfg)

	// extras == nil: niuniu entry is (re)written; non-niuniu entries preserved.
	res, err := g.Generate(wsDir, config.MCPGenerateOptions{SessionToken: "tok-1"}, nil, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !slices.Contains(res.WrittenServers, "niuniu") {
		t.Errorf("niuniu entry must be written, got %+v", res)
	}

	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Hand-edited / scene server preserved verbatim.
	if _, ok := got.MCPServers["custom-mcp"]; !ok {
		t.Errorf("custom-mcp server dropped: %s", data)
	}
	// niuniu entry present with the injected token.
	env, _ := got.MCPServers["niuniu"]["env"].(map[string]any)
	if env["NIUNIU_MCP_TOKEN"] != "tok-1" {
		t.Errorf("niuniu token not injected: %s", data)
	}
}

// TestMCPConfigGenerator_Generate_EmptyExtrasStillRegenerates ensures that
// once the user has touched the workspace MCP config in the UI (extras is
// `[]` not nil), Generate proceeds with normal regeneration even if the
// existing file contains foreign servers — the user's explicit empty
// selection wins over preservation.
func TestMCPConfigGenerator_Generate_EmptyExtrasStillRegenerates(t *testing.T) {
	wsDir := t.TempDir()
	existing := []byte(`{"mcpServers":{"foreign":{"command":"x"}}}`)
	_ = os.WriteFile(filepath.Join(wsDir, ".mcp.json"), existing, 0o644)

	parent := t.TempDir()
	binDir := filepath.Join(parent, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	stubName := "niuniu-mcp"
	if runtime.GOOS == "windows" {
		stubName += ".exe"
	}
	writeFile(t, filepath.Join(binDir, stubName))
	wsBase := filepath.Join(parent, "workspaces")
	_ = os.MkdirAll(wsBase, 0o755)

	cfg := &config.Config{Workspace: config.WorkspaceConfig{BaseDir: wsBase}}
	g := NewMCPConfigGenerator(cfg)

	// extras = []string{} (non-nil) bypasses legacy-compat.
	res, err := g.Generate(wsDir, config.MCPGenerateOptions{}, []string{}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.WrittenServers) != 1 || res.WrittenServers[0] != "niuniu" {
		t.Errorf("WrittenServers = %v, want [niuniu]", res.WrittenServers)
	}
	got, _ := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if bytes.Equal(got, existing) {
		t.Errorf("file should have been regenerated, but is unchanged")
	}
}
