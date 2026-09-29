package service

// Capability-module projection (wave-2 wiring, plan §3): the media-studio
// scene declares `mcp: - name: video-gen` (a bare name, no inline config),
// and Generate projects it into the workspace .mcp.json as the
// niuniu-video-mcp stdio server — launched with the workspace dir, the host
// data dir, and the NN_CAP_* credential env resolved from the
// capability_backends table (design §7.1/§7.2/§7.5: 场景声明挂载 + 能力配置注入,
// 绝不全局).
//
// The module deliberately does NOT ride the Claude MCP registry: it is a
// niuniu-built binary shipped alongside the app, so it resolves through its
// own file-search ladder (FindVideoMCPBinary) exactly like niuniu-mcp does,
// and a missing binary degrades through the existing Unavailable channel
// (user-visible, never a hard error) instead of failing the generation.

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// videoMCPBinaryStem is the niuniu-video-mcp executable's base file name
// (".exe" is appended on Windows). Desktop bundles unpack it next to the
// server binary — the same sidecar layout niuniu-mcp uses.
const videoMCPBinaryStem = "niuniu-video-mcp"

// CapabilityEnvResolver resolves the NN_CAP_* environment a capability
// module process should be spawned with for one workspace. Implemented in
// production by CapabilityBackendEnvResolver; an interface so the generator
// does not depend on the capability service's concrete type (and stays
// nil-safe for bare test fixtures).
type CapabilityEnvResolver interface {
	ResolveCapabilityEnv(ctx context.Context, module string, workspaceID int64) (map[string]string, error)
}

// SetCapabilityEnv injects the capability env resolver used when projecting
// capability modules (video-gen). Optional — when unset the module entry is
// still written, just without credential env; the module then degrades its
// paid tools with its own 设置→能力配置 hint while quote_estimate /
// media_compose keep working.
func (g *MCPConfigGenerator) SetCapabilityEnv(r CapabilityEnvResolver) { g.capabilityEnv = r }

// CapabilityBackendEnvResolver adapts CapabilityBackendService into
// CapabilityEnvResolver: it resolves the workspace's creator
// (workspaces.created_by — the user whose capability rows act as the global
// default, matching the ${cred:} resolution rule in scene projection) and
// asks ResolveEnv for the module's env.
type CapabilityBackendEnvResolver struct {
	q   *store.Queries
	svc *CapabilityBackendService
}

// NewCapabilityBackendEnvResolver builds the production resolver. Wired in
// server.go next to the other generator setters.
func NewCapabilityBackendEnvResolver(q *store.Queries, svc *CapabilityBackendService) *CapabilityBackendEnvResolver {
	return &CapabilityBackendEnvResolver{q: q, svc: svc}
}

// ResolveCapabilityEnv resolves the module env for one workspace. A missing
// workspace row degrades to userID 0 (the auth-off system-wide rows) rather
// than failing the .mcp.json write — the workspace lookup only selects which
// user-scope defaults apply, while ResolveEnv still returns any
// workspace-scoped rows.
func (r *CapabilityBackendEnvResolver) ResolveCapabilityEnv(ctx context.Context, module string, workspaceID int64) (map[string]string, error) {
	userID := int64(0)
	if ws, err := r.q.GetWorkspace(ctx, workspaceID); err != nil {
		slog.Debug("capability env: workspace lookup failed; resolving with user 0",
			"workspace_id", workspaceID, "err", err)
	} else if ws.CreatedBy.Valid {
		userID = ws.CreatedBy.Int64
	}
	return r.svc.ResolveEnv(ctx, userID, workspaceID, module)
}

// isCapabilityModuleName reports whether name is a capability module this
// build ships (design §7.1 装配点: 投影时查模块注册表——在则投影 stdio 命令,
// 不在则工具组缺席).
func isCapabilityModuleName(name string) bool {
	for _, m := range ListCapabilityModules() {
		if m.Name == name {
			return true
		}
	}
	return false
}

// buildVideoGenEntry assembles the .mcp.json stdio entry for the video-gen
// capability module. ok=false means its binary could not be located; the
// caller routes the name through the Unavailable channel so the user sees
// which scene-declared MCP was skipped.
//
// video-gen is the only capability module this wave ships, so binary and
// resolver are wired directly; a second module would dispatch on its registry
// entry here.
func (g *MCPConfigGenerator) buildVideoGenEntry(wsPath string, opts config.MCPGenerateOptions) (map[string]any, bool) {
	bin := g.FindVideoMCPBinary()
	if bin == "" {
		return nil, false
	}
	args := []string{"--workspace-dir", wsPath}
	// --data-dir is the host data dir (where the module looks for the
	// unpacked ffmpeg payload); it has a ~/.niuniu fallback in the module, so
	// it is omitted when the host has none configured.
	if g.dataDir != "" {
		args = append(args, "--data-dir", g.dataDir)
	}
	ent := map[string]any{
		"command": bin,
		"args":    args,
	}
	// env is omitted when empty (mirroring the niuniu entry, which only sets
	// env when it has a token): an empty map would suggest "no credentials
	// needed" while the module's own startup log already explains the
	// 设置→能力配置 fix for unconfigured capabilities.
	if env := g.resolveCapabilityEnv(videoGenModule.Name, opts.WorkspaceID); len(env) > 0 {
		ent["env"] = env
	}
	return ent, true
}

// resolveCapabilityEnv asks the injected resolver for a module's env. No
// resolver, no workspace context, or a resolver error → no env: the tool
// group must not vanish because credential resolution failed, the module
// degrades its paid tools with its own message instead.
func (g *MCPConfigGenerator) resolveCapabilityEnv(module string, wsID int64) map[string]string {
	if g.capabilityEnv == nil || wsID <= 0 || module == "" {
		return nil
	}
	env, err := g.capabilityEnv.ResolveCapabilityEnv(context.Background(), module, wsID)
	if err != nil {
		slog.Warn("mcp.Generate: capability env resolve failed", "module", module, "workspace_id", wsID, "err", err)
		return nil
	}
	return env
}

// FindVideoMCPBinary locates the niuniu-video-mcp capability-module binary,
// mirroring FindMCPBinary's fallback ladder:
//
//  1. same directory as the running executable (desktop bundles unpack
//     sidecars there), plus the newest niuniu-video-mcp* sibling for
//     hashed/arch-suffixed layouts
//  2. bin/ relative to the working directory
//  3. walk up (3 levels) from the workspace base_dir looking for bin/
//  4. PATH lookup
//
// Successful lookups are memoised; a miss is retried on the next call so a
// sidecar unpacked after the first spawn is picked up (same policy as
// FindMCPBinary).
func (g *MCPConfigGenerator) FindVideoMCPBinary() string {
	g.videoBinMu.Lock()
	defer g.videoBinMu.Unlock()
	if g.cachedVideoBin != "" {
		return g.cachedVideoBin
	}
	if found := g.findVideoMCPBinaryUncached(); found != "" {
		g.cachedVideoBin = found
	}
	return g.cachedVideoBin
}

func (g *MCPConfigGenerator) findVideoMCPBinaryUncached() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	hostMatch := runtime.GOOS + "-" + runtime.GOARCH

	// 1. Same directory as the running executable.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		exact := filepath.Join(dir, videoMCPBinaryStem+suffix)
		if _, err := os.Stat(exact); err == nil {
			return exact
		}
		if found := findLatestBinaryInDir(dir, videoMCPBinaryStem, suffix, hostMatch); found != "" {
			return found
		}
	}

	// 2. bin/ relative to the working directory.
	if cwd, err := os.Getwd(); err == nil {
		if found := findLatestBinaryInDir(filepath.Join(cwd, "bin"), videoMCPBinaryStem, suffix, hostMatch); found != "" {
			return found
		}
	}

	// 3. Walk up from the workspace base_dir.
	if g.workspaceCfg.BaseDir != "" {
		dir := filepath.Dir(g.workspaceCfg.BaseDir)
		for i := 0; i < 3; i++ {
			if found := findLatestBinaryInDir(filepath.Join(dir, "bin"), videoMCPBinaryStem, suffix, hostMatch); found != "" {
				return found
			}
			dir = filepath.Dir(dir)
		}
	}

	// 4. PATH lookup.
	if path, err := exec.LookPath(videoMCPBinaryStem); err == nil {
		return path
	}
	return ""
}
