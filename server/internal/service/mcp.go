package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// MCPConfigGenerator generates .mcp.json files for workspaces so that
// Claude Code can discover and use the niuniu-mcp server.
type MCPConfigGenerator struct {
	workspaceCfg config.WorkspaceConfig
	serverCfg    config.ServerConfig
	dataDir      string
	// registry is the (optional) Claude install registry used to resolve
	// per-workspace extra MCP names. Nil-safe — Generate lazy-inits a
	// stateless default when this is unset. SetRegistry allows DI of a
	// shared or mocked instance.
	registry *ClaudeMCPRegistry

	mcpBinMu     sync.Mutex
	cachedMCPBin string

	// localRunner (optional) gates the local-runner MCP tool group: it is hidden
	// from the generated config whenever the workspace's desktop runner is
	// offline, so the tools appear only when a runner is live (Epic #526 子B,
	// #471). Nil-safe.
	localRunner LocalRunnerGate

	// settingsMu guards concurrent read-modify-write of
	// <wsPath>/.claude/settings.json (SetWorkspaceEnabledPlugins +
	// GenerateClaudeSettings can race when a scene Apply overlaps an agent spawn).
	settingsMu sync.Mutex
}

func NewMCPConfigGenerator(cfg *config.Config) *MCPConfigGenerator {
	return &MCPConfigGenerator{
		workspaceCfg: cfg.Workspace,
		serverCfg:    cfg.Server,
		dataDir:      cfg.DataDir,
	}
}

// SetRegistry injects the Claude MCP registry used by Generate to resolve
// per-workspace extras. Optional — Generate lazy-inits a default registry
// when unset, so callers that don't care about extras can ignore this.
func (g *MCPConfigGenerator) SetRegistry(r *ClaudeMCPRegistry) { g.registry = r }

// LocalRunnerGate reports which extra niuniu-mcp tool groups to hide for a
// workspace. LocalRunnerService implements it, returning the local-runner group
// when that workspace's desktop runner is offline. Optional on the generator.
type LocalRunnerGate interface {
	DisableToolGroupsFor(wsID int64) []string
}

// SetLocalRunner injects the local-runner gate so every generated config hides
// the local-runner tool group unless a runner is live (Epic #526 子B).
func (g *MCPConfigGenerator) SetLocalRunner(gate LocalRunnerGate) { g.localRunner = gate }

// withLocalRunnerGate folds the local-runner gate's hidden groups into an
// existing disable-groups list. Nil-safe; returns groups unchanged when no gate
// is wired or the workspace has a live runner.
func (g *MCPConfigGenerator) withLocalRunnerGate(wsID int64, groups []string) []string {
	if g.localRunner == nil || wsID <= 0 {
		return groups
	}
	extra := g.localRunner.DisableToolGroupsFor(wsID)
	if len(extra) == 0 {
		return groups
	}
	return append(append([]string{}, groups...), extra...)
}

// MCPGenerateResult reports what was written and what was dropped.
//
// WrittenServers always contains "niuniu" plus any extras that were
// resolved against the registry. Unavailable lists the extra names that
// the registry could not find — callers surface these to the user so
// they know which workspace-configured MCP servers were silently skipped.
//
// Named "MCPGenerateResult" rather than "GenerateResult" to avoid
// colliding with prompt_gen.go's unrelated GenerateResult.
type MCPGenerateResult struct {
	WrittenServers []string `json:"written_servers"`
	Unavailable    []string `json:"unavailable"`
}

// Generate writes the workspace .mcp.json containing the niuniu base
// entry plus any extras that resolve against the configured Claude MCP
// registry. `extras` is the workspace.mcp_servers value (names only);
// `configDir` selects which Claude account's registry to filter against
// ("" = default account, use $HOME).
//
// Stage-and-swap: writes to <wsPath>/.mcp.json.tmp-<rand> first, then
// os.Rename onto the final path. os.Rename within the same directory is
// atomic on both POSIX and Windows, so a half-written .mcp.json can
// never be observed by Claude Code mid-write.
func (g *MCPConfigGenerator) Generate(
	wsPath string,
	opts config.MCPGenerateOptions,
	extras []string,
	configDir string,
) (*MCPGenerateResult, error) {
	// Conditional local-runner injection (#471): hide the group unless a runner
	// is live for this workspace.
	opts.DisableToolGroups = g.withLocalRunnerGate(opts.WorkspaceID, opts.DisableToolGroups)
	mcpBin := g.FindMCPBinary()
	if mcpBin == "" {
		return nil, fmt.Errorf("niuniu-mcp binary not found in any search path")
	}

	// When `extras == nil` (the proxy/agent token-refresh path, which doesn't
	// know the scene's MCP set) preserve any non-niuniu servers already in
	// .mcp.json — both hand-edited entries and scene-projected ones. Crucially
	// we do NOT return early here: the niuniu entry below MUST still be written
	// so the session token is (re)injected. Returning early whenever a non-niuniu
	// server existed (e.g. a scene's filesystem MCP) dropped NIUNIU_MCP_TOKEN and
	// 401-ed every /mcp call.
	var preservedServers map[string]map[string]any
	if extras == nil {
		preservedServers = existingNonNiuniuServers(filepath.Join(wsPath, ".mcp.json"))
	}

	// niuniu base entry — preserves existing CLI-flag construction.
	args := g.buildNiuniuArgs(opts)

	niuniuEntry := map[string]any{
		"command": mcpBin,
		"args":    args,
	}
	// Preserve an existing session token when the caller didn't supply one.
	// Scene re-projection (SceneProjector.Apply) and other non-agent-start
	// regenerations pass an empty SessionToken; without this they would strip
	// env.NIUNIU_MCP_TOKEN from .mcp.json, 401-ing every /mcp call until the next
	// agent start re-injects it.
	sessionToken := opts.SessionToken
	if sessionToken == "" {
		sessionToken = existingMCPToken(filepath.Join(wsPath, ".mcp.json"))
	}
	if sessionToken != "" {
		niuniuEntry["env"] = map[string]string{
			"NIUNIU_MCP_TOKEN": sessionToken,
		}
	}

	servers := map[string]any{
		"niuniu": niuniuEntry,
	}
	// Carry forward non-niuniu servers already on disk (extras==nil path), so a
	// token refresh doesn't drop scene-projected / hand-edited MCP servers.
	for name, cfg := range preservedServers {
		if name == "" || name == "niuniu" || len(cfg) == 0 {
			continue
		}
		servers[name] = cfg
	}
	res := &MCPGenerateResult{
		WrittenServers: []string{"niuniu"},
	}

	// Resolve extras against registry. Unknown names go into Unavailable
	// instead of failing the call — a misspelled workspace MCP shouldn't
	// block agent startup, just surface to the user.
	if len(extras) > 0 {
		registry := g.registry
		if registry == nil {
			registry = NewClaudeMCPRegistry()
		}
		known, err := registry.List(configDir)
		if err != nil {
			slog.Warn("mcp.Generate: registry list failed", "err", err)
		}
		byName := map[string]KnownMCP{}
		for _, k := range known {
			byName[k.Name] = k
		}
		for _, name := range extras {
			// Inline-config names are written verbatim below; skip registry resolution.
			if len(opts.ExtraMCPServers[name]) > 0 {
				continue
			}
			entry, ok := byName[name]
			if !ok {
				res.Unavailable = append(res.Unavailable, name)
				continue
			}
			ent := map[string]any{
				"command": entry.Command,
				"args":    entry.Args,
			}
			if len(entry.Env) > 0 {
				ent["env"] = entry.Env
			}
			servers[name] = ent
			res.WrittenServers = append(res.WrittenServers, name)
		}
	}

	// Inline server configs (e.g. scene-authored JSON snippets) are written
	// verbatim. They take precedence over a registry entry of the same name.
	for name, cfg := range opts.ExtraMCPServers {
		if name == "" || len(cfg) == 0 {
			continue
		}
		if _, dup := servers[name]; !dup {
			res.WrittenServers = append(res.WrittenServers, name)
		}
		servers[name] = cfg
	}

	doc := map[string]any{
		"mcpServers": servers,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}

	// Stage-and-swap: same-directory rename is atomic on POSIX and
	// Windows. A reader (Claude Code) opening .mcp.json mid-generation
	// either sees the previous version or the new one — never a partial
	// write.
	finalPath := filepath.Join(wsPath, ".mcp.json")
	tmpPath := finalPath + ".tmp-" + randSuffix()
	// 0600: .mcp.json may carry scene-injected secrets (e.g. an imap password
	// resolved from a ${cred:...} placeholder), a high-value long-lived
	// credential. Keep it owner-only readable. (spec §8 file permission gate)
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("write tmp .mcp.json: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("rename .mcp.json: %w", err)
	}
	return res, nil
}

// randSuffix returns a short hex token used to namespace the stage-and-swap
// temp file. crypto/rand is overkill for collision avoidance but avoids
// the math/rand seeding tax; UnixNano is the documented fallback so this
// helper never returns "" on a hardened system that blocks /dev/urandom.
func randSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// GenerateClaudeSettings ensures wsPath/.claude/settings.json carries the
// niuniu-managed Claude Code hooks:
//   - WorktreeCreate / WorktreeRemove — without these, Claude Code's
//     `--worktree` mode (and Agent({isolation:"worktree"})) crash on niuniu
//     workspaces because the workspace root is not itself a git repo (the real
//     git worktrees live one level down at .worktrees/<repo>).
//   - PreToolUse on Read — reroutes large images / binary documents to the
//     niuniu MCP fast-path tools (read_image / read_document). Fails open and
//     is loop-safe (see readhook.go).
//
// It MERGES rather than skips: an existing settings.json (user-hand-edited, or
// written by an older niuniu that predates a hook) has the managed hooks added
// if missing and refreshed in place if they point at a stale binary path,
// while every user-defined hook, matcher, and top-level key is preserved. This
// is why pre-existing workspaces gain new hooks (and self-heal a renamed
// binary) on the next spawn instead of staying silently un-hooked. It rewrites
// only when something actually changed, so it stays a no-op once current.
//
// Returns an error only if a NEW file would have to be written without a
// locatable niuniu-mcp binary, if an existing settings.json is unparseable, or
// if the write fails. When the binary is missing but a settings.json already
// exists it returns nil (can't refresh, but must not clobber/break the file).
// Callers in spawn paths log+continue, not fail the spawn.
func (g *MCPConfigGenerator) GenerateClaudeSettings(wsPath string) error {
	g.settingsMu.Lock()
	defer g.settingsMu.Unlock()

	settingsPath := filepath.Join(wsPath, ".claude", "settings.json")

	existing, readErr := os.ReadFile(settingsPath)
	fileExists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read settings.json: %w", readErr)
	}

	mcpBin := g.FindMCPBinary()
	if mcpBin == "" {
		if fileExists {
			// Can't refresh without the binary, but never clobber an existing
			// file with a half-broken one.
			return nil
		}
		return fmt.Errorf("niuniu-mcp binary not found in any search path")
	}
	// Forward slashes are universally accepted by both Go's os/exec and
	// bash on Windows; backslashes inside JSON strings would also have to
	// be doubled. Forward-slashes keep the on-disk JSON readable.
	binCmd := `"` + filepath.ToSlash(mcpBin) + `"`

	root := map[string]any{}
	if fileExists {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("parse existing settings.json: %w", err)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	changed := mergeManagedHook(hooks, "WorktreeCreate", "", "worktree-create", binCmd+" worktree-create")
	changed = mergeManagedHook(hooks, "WorktreeRemove", "", "worktree-remove", binCmd+" worktree-remove") || changed
	changed = mergeManagedHook(hooks, "PreToolUse", "Read", "read-hook", binCmd+" read-hook") || changed

	if fileExists && !changed {
		return nil // already current — avoid a needless rewrite
	}
	root["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings.json: %w", err)
	}
	return os.WriteFile(settingsPath, data, 0o644)
}

// SetWorkspaceManagedDeny rewrites the niuniu-managed subset of
// permissions.deny in <wsPath>/.claude/settings.json for one MCP server: it
// removes any prior `mcp__<serverName>__*` deny entries (and a bare
// `mcp__<serverName>`) and inserts exactly `deny`. Every other deny entry,
// permission key, hook, and top-level key is preserved. Used by the scene
// projector to gate an external MCP server's write/management tools behind a
// write-permission decision (e.g. office-mail's imap tools). Idempotent: a
// no-op write is skipped. An empty `deny` simply clears this server's managed
// entries.
func (g *MCPConfigGenerator) SetWorkspaceManagedDeny(wsPath, serverName string, deny []string) error {
	g.settingsMu.Lock()
	defer g.settingsMu.Unlock()

	settingsPath := filepath.Join(wsPath, ".claude", "settings.json")
	existing, readErr := os.ReadFile(settingsPath)
	fileExists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read settings.json: %w", readErr)
	}
	root := map[string]any{}
	if fileExists {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("parse existing settings.json: %w", err)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	var prior []any
	if raw, ok := perms["deny"].([]any); ok {
		prior = raw
	}
	prefix := "mcp__" + serverName + "__"
	bare := "mcp__" + serverName
	kept := make([]any, 0, len(prior))
	for _, e := range prior {
		s, _ := e.(string)
		if s == bare || strings.HasPrefix(s, prefix) {
			continue // drop prior managed entries for this server
		}
		kept = append(kept, e)
	}
	for _, d := range deny {
		kept = append(kept, d)
	}
	// Build the new doc and short-circuit if unchanged.
	if len(kept) == 0 {
		delete(perms, "deny")
	} else {
		perms["deny"] = kept
	}
	if len(perms) == 0 {
		delete(root, "permissions")
	} else {
		root["permissions"] = perms
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings.json: %w", err)
	}
	if fileExists && string(existing) == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	return os.WriteFile(settingsPath, data, 0o644)
}

// kbReadonlyManifest is the sidecar (under .claude/) that records exactly which
// permissions.additionalDirectories and permissions.deny entries
// SetWorkspaceKBReadonly currently manages. Tracking our own entries lets us
// fully recompute the read-only set on every spawn without ever disturbing a
// user-authored rule.
const kbReadonlyManifest = ".niuniu-kb-access.json"

// kbAccessManifest is the on-disk shape of kbReadonlyManifest.
type kbAccessManifest struct {
	Dirs []string `json:"dirs"` // managed permissions.additionalDirectories
	Deny []string `json:"deny"` // managed permissions.deny
}

// kbReadonlyTools are the mutating file tools denied on a knowledge base's
// dataset root so the agent can Read/Grep/Glob it but never write it (KB base4:
// the directory is exposed read-only). Claude deny rules take precedence over
// allow/ask AND over bypassPermissions, so this holds even in autohost mode.
// Edit governs all file-editing tools; Write and NotebookEdit are named
// explicitly. MultiEdit is intentionally omitted — it's covered by Edit and an
// unknown tool name would emit a Claude startup warning. These deny rules also
// block the file commands Claude recognizes in Bash (cat/sed/etc.); arbitrary
// subprocesses are out of scope (would need OS sandboxing).
var kbReadonlyTools = []string{"Write", "Edit", "NotebookEdit"}

// SetWorkspaceKBReadonly exposes the given KB dataset roots to the workspace
// agent read-only by rewriting two niuniu-managed sections of
// <wsPath>/.claude/settings.json:
//   - permissions.additionalDirectories grants read access to each root WITHOUT
//     loading any .claude config from it (unlike --add-dir, which would load a
//     dataset's skills/agents/plugins — unsafe for arbitrary KB content).
//   - permissions.deny write-denies Write/Edit/NotebookEdit on each root so the
//     agent can Read/Grep/Glob but never corrupt the dataset (deny wins even
//     under bypassPermissions/autohost).
//
// It is fully recomputing + idempotent: the prior managed set (tracked in the
// .claude sidecar manifest) is removed and the current set inserted, while every
// user-authored rule and other top-level key is preserved. An empty roots slice
// clears all managed entries (e.g. after the last KB is unbound). Mirrors
// SetWorkspaceManagedDeny's preserve-everything discipline.
func (g *MCPConfigGenerator) SetWorkspaceKBReadonly(wsPath string, roots []string) error {
	g.settingsMu.Lock()
	defer g.settingsMu.Unlock()

	claudeDir := filepath.Join(wsPath, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	manifestPath := filepath.Join(claudeDir, kbReadonlyManifest)

	// Desired managed sets (deduplicated, stable order).
	var wantDirs, wantDeny []string
	seenDir := map[string]struct{}{}
	seenDeny := map[string]struct{}{}
	for _, root := range roots {
		dir := strings.TrimSpace(root)
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		if _, dup := seenDir[dir]; !dup {
			seenDir[dir] = struct{}{}
			wantDirs = append(wantDirs, dir)
		}
		norm := kbDenyPath(root)
		if norm == "" {
			continue
		}
		for _, tool := range kbReadonlyTools {
			rule := tool + "(" + norm + ")"
			if _, dup := seenDeny[rule]; dup {
				continue
			}
			seenDeny[rule] = struct{}{}
			wantDeny = append(wantDeny, rule)
		}
	}

	// Prior managed sets from the sidecar manifest (best-effort).
	var prior kbAccessManifest
	if data, err := os.ReadFile(manifestPath); err == nil {
		_ = json.Unmarshal(data, &prior)
	}

	existing, readErr := os.ReadFile(settingsPath)
	fileExists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read settings.json: %w", readErr)
	}
	// Nothing to manage now or before, and no file to touch: stay inert rather
	// than materializing an empty settings.json.
	if len(wantDirs) == 0 && len(wantDeny) == 0 &&
		len(prior.Dirs) == 0 && len(prior.Deny) == 0 && !fileExists {
		return nil
	}

	root := map[string]any{}
	if fileExists {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("parse existing settings.json: %w", err)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	perms["deny"] = mergeManagedList(perms["deny"], prior.Deny, wantDeny)
	perms["additionalDirectories"] = mergeManagedList(perms["additionalDirectories"], prior.Dirs, wantDirs)
	if v, _ := perms["deny"].([]any); len(v) == 0 {
		delete(perms, "deny")
	}
	if v, _ := perms["additionalDirectories"].([]any); len(v) == 0 {
		delete(perms, "additionalDirectories")
	}
	if len(perms) == 0 {
		delete(root, "permissions")
	} else {
		root["permissions"] = perms
	}

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings.json: %w", err)
	}
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	if !(fileExists && string(existing) == string(data)) {
		if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
			return fmt.Errorf("write settings.json: %w", err)
		}
	}
	// Persist (or clear) the manifest of entries we now own.
	if len(wantDirs) == 0 && len(wantDeny) == 0 {
		if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove kb-access manifest: %w", err)
		}
		return nil
	}
	mdata, err := json.MarshalIndent(kbAccessManifest{Dirs: wantDirs, Deny: wantDeny}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal kb-access manifest: %w", err)
	}
	return os.WriteFile(manifestPath, mdata, 0o644)
}

// mergeManagedList returns priorList with our previously-managed entries removed
// and the desired entries appended, preserving every user-authored string. prior
// is the set we owned last time; want is what we own now.
func mergeManagedList(rawList any, prior, want []string) []any {
	var list []any
	if raw, ok := rawList.([]any); ok {
		list = raw
	}
	managed := make(map[string]struct{}, len(prior))
	for _, p := range prior {
		managed[p] = struct{}{}
	}
	kept := make([]any, 0, len(list)+len(want))
	for _, e := range list {
		if s, ok := e.(string); ok {
			if _, isManaged := managed[s]; isManaged {
				continue // drop our prior managed entry; re-added below if still desired
			}
		}
		kept = append(kept, e)
	}
	for _, w := range want {
		kept = append(kept, w)
	}
	return kept
}

// kbDenyPath normalizes an absolute dataset root into a Claude permission path
// glob matching the directory and everything beneath it. Claude's matcher is
// gitignore-style with anchor rules: an absolute filesystem path must use the
// "//" prefix ("/path" alone means project-root-relative), and Windows drive
// paths are normalized to POSIX form (C:\Users → /c/Users). So an absolute
// /data/docs becomes //data/docs/** and C:\kb\x becomes //c/kb/x/**.
func kbDenyPath(root string) string {
	p := strings.TrimSpace(root)
	if p == "" {
		return ""
	}
	p = strings.TrimRight(filepath.ToSlash(p), "/")
	if p == "" {
		return ""
	}
	// Windows drive letter: C:/Users/x -> /c/Users/x
	if len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		p = "/" + strings.ToLower(p[:1]) + p[2:]
	}
	// Anchor as an absolute path: ensure a single leading slash, then add the
	// "//" absolute prefix Claude requires.
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "/" + p + "/**"
}

// mergeManagedHook ensures the niuniu-managed hook for an event (optionally
// scoped to a matcher) is present and points at wantCmd. sub is the niuniu-mcp
// subcommand the hook invokes (worktree-create / worktree-remove / read-hook),
// used to recognize our own prior entry. It refreshes a stale niuniu entry in
// place (e.g. an old binary path after a rebuild) and otherwise appends a fresh
// managed group, never duplicating or disturbing user-defined hooks. Returns
// true if it mutated hooks.
func mergeManagedHook(hooks map[string]any, event, matcher, sub, wantCmd string) bool {
	list, _ := hooks[event].([]any)
	for _, grp := range list {
		gm, _ := grp.(map[string]any)
		if gm == nil {
			continue
		}
		if matcher != "" {
			if mv, _ := gm["matcher"].(string); mv != matcher {
				continue
			}
		}
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if hm == nil {
				continue
			}
			cmd, _ := hm["command"].(string)
			if !isNiuniuHookCmd(cmd, sub) {
				continue
			}
			if cmd == wantCmd {
				return false // already current
			}
			hm["command"] = wantCmd // refresh stale binary path
			return true
		}
	}
	group := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": wantCmd}},
	}
	if matcher != "" {
		group["matcher"] = matcher
	}
	hooks[event] = append(list, group)
	return true
}

// isNiuniuHookCmd reports whether cmd is a niuniu-mcp invocation of the given
// subcommand, so GenerateClaudeSettings can recognize and refresh its own
// prior entries without matching an unrelated user hook. The binary's name
// (niuniu-mcp-<hash>.exe) carries a per-build suffix, so we match on the
// "niuniu-mcp" stem plus the trailing subcommand token rather than an exact
// path.
func isNiuniuHookCmd(cmd, sub string) bool {
	c := strings.TrimSpace(cmd)
	return strings.Contains(c, "niuniu-mcp") && strings.HasSuffix(c, " "+sub)
}

// GenerateCodexConfigToml writes a `<wsPath>/.codex/config.toml` that records
// the niuniu MCP server for workspace inspection and scene projection. Codex
// spawn paths also pass GenerateCodexConfigArgs as CLI overrides because
// CODEX_HOME may need to keep pointing at an account auth directory.
//
// The TOML is hand-rolled (no external dep) because the shape is fixed:
// one [mcp_servers.niuniu] section with a command, an args array, and an
// optional env table. This keeps the server build lean and matches the
// approach used by GenerateClaudeSettings (json.MarshalIndent for a
// fixed-shape doc).
//
// Stage-and-swap: writes to `config.toml.tmp-<rand>` first, then
// os.Rename atomically. Same-directory rename is atomic on POSIX and
// Windows, so a codex reader can never observe a partial write.
//
// Returns an error if the niuniu-mcp binary cannot be located or the
// write fails. Spawn callers (service/agent.go) log+continue on this
// error: a malformed install should not block codex from spawning at
// all; it just means the codex session will run without the niuniu MCP
// wired in. Symmetric with the Claude path's Generate() error-return.
func (g *MCPConfigGenerator) GenerateCodexConfigToml(
	wsPath string,
	opts config.MCPGenerateOptions,
) error {
	_, err := g.GenerateCodexConfigTomlWithExtras(wsPath, opts, nil, "")
	return err
}

// GenerateCodexConfigArgs returns Codex CLI -c overrides for the niuniu MCP
// server. Codex does not load <workdir>/.codex/config.toml from -C, and
// CODEX_HOME may need to keep pointing at an account auth directory, so spawn
// paths pass these overrides explicitly.
func (g *MCPConfigGenerator) GenerateCodexConfigArgs(opts config.MCPGenerateOptions) ([]string, error) {
	// Conditional local-runner injection (#471): hide the group unless a runner
	// is live for this workspace.
	opts.DisableToolGroups = g.withLocalRunnerGate(opts.WorkspaceID, opts.DisableToolGroups)
	mcpBin := g.FindMCPBinary()
	if mcpBin == "" {
		return nil, fmt.Errorf("niuniu-mcp binary not found in any search path")
	}

	apiBase := g.resolveAPIBase()
	mcpArgs := []string{"--api-base", apiBase}
	if opts.ProjectID > 0 {
		mcpArgs = append(mcpArgs, "--project-id", strconv.FormatInt(opts.ProjectID, 10))
	}
	if opts.WorkspaceID > 0 {
		mcpArgs = append(mcpArgs, "--workspace-id", strconv.FormatInt(opts.WorkspaceID, 10))
	}
	if opts.InboxDir != "" {
		mcpArgs = append(mcpArgs, "--inbox-dir", opts.InboxDir)
	}
	if opts.AgentName != "" && opts.AgentName != "agent" {
		mcpArgs = append(mcpArgs, "--agent-name", opts.AgentName)
	}
	if len(opts.DisableToolGroups) > 0 {
		mcpArgs = append(mcpArgs, "--disable-tool-groups", strings.Join(opts.DisableToolGroups, ","))
	}
	if len(opts.EnableToolGroups) > 0 {
		mcpArgs = append(mcpArgs, "--enable-tool-groups", strings.Join(opts.EnableToolGroups, ","))
	}

	out := []string{
		"--config", "mcp_servers.niuniu.command=" + tomlQuote(filepath.ToSlash(mcpBin)),
		"--config", "mcp_servers.niuniu.args=" + tomlStringArray(mcpArgs),
	}
	if opts.SessionToken != "" {
		out = append(out, "--config", "mcp_servers.niuniu.env.NIUNIU_MCP_TOKEN="+tomlQuote(opts.SessionToken))
	}
	if opts.CodexApprovalPolicy != "" {
		out = append(out, "--config", "approval_policy="+tomlQuote(opts.CodexApprovalPolicy))
	}
	return out, nil
}

// GenerateCodexConfigTomlWithExtras writes Codex's config.toml and resolves
// the same per-workspace MCP extras as Generate does for Claude's .mcp.json.
func (g *MCPConfigGenerator) GenerateCodexConfigTomlWithExtras(
	wsPath string,
	opts config.MCPGenerateOptions,
	extras []string,
	configDir string,
) (*MCPGenerateResult, error) {
	// Conditional local-runner injection (#471): hide the group unless a runner
	// is live for this workspace.
	opts.DisableToolGroups = g.withLocalRunnerGate(opts.WorkspaceID, opts.DisableToolGroups)
	mcpBin := g.FindMCPBinary()
	if mcpBin == "" {
		return nil, fmt.Errorf("niuniu-mcp binary not found in any search path")
	}

	apiBase := g.resolveAPIBase()
	args := []string{"--api-base", apiBase}
	if opts.ProjectID > 0 {
		args = append(args, "--project-id", strconv.FormatInt(opts.ProjectID, 10))
	}
	if opts.WorkspaceID > 0 {
		args = append(args, "--workspace-id", strconv.FormatInt(opts.WorkspaceID, 10))
	}
	if opts.InboxDir != "" {
		args = append(args, "--inbox-dir", opts.InboxDir)
	}
	if opts.AgentName != "" && opts.AgentName != "agent" {
		args = append(args, "--agent-name", opts.AgentName)
	}
	if len(opts.DisableToolGroups) > 0 {
		args = append(args, "--disable-tool-groups", strings.Join(opts.DisableToolGroups, ","))
	}
	if len(opts.EnableToolGroups) > 0 {
		args = append(args, "--enable-tool-groups", strings.Join(opts.EnableToolGroups, ","))
	}

	res := &MCPGenerateResult{WrittenServers: []string{"niuniu"}}

	var sb strings.Builder
	sb.WriteString("# Auto-generated by niuniu. Do not edit by hand; this file is\n")
	sb.WriteString("# regenerated on every agent spawn. Anything you add will be wiped.\n")
	sb.WriteString("# Spec: docs/superpowers/specs/2026-05-19-codex-cli-support-design.md\n")
	sb.WriteString("\n")
	// Top-level Codex CLI settings come BEFORE [mcp_servers.*] sections.
	// Codex parses TOML head-first; tables (sections) terminate top-level keys.
	if opts.CodexSandboxMode != "" {
		sb.WriteString("sandbox_mode = ")
		sb.WriteString(tomlQuote(opts.CodexSandboxMode))
		sb.WriteString("\n")
	}
	if opts.CodexApprovalPolicy != "" {
		sb.WriteString("approval_policy = ")
		sb.WriteString(tomlQuote(opts.CodexApprovalPolicy))
		sb.WriteString("\n")
	}
	if opts.CodexSandboxMode != "" || opts.CodexApprovalPolicy != "" {
		sb.WriteString("\n")
	}
	writeCodexMCPServerTOML(&sb, "niuniu", mcpBin, args, nil)
	if opts.SessionToken != "" {
		sb.WriteString("\n[mcp_servers.niuniu.env]\n")
		sb.WriteString("NIUNIU_MCP_TOKEN = ")
		sb.WriteString(tomlQuote(opts.SessionToken))
		sb.WriteString("\n")
	}

	if len(extras) > 0 {
		registry := g.registry
		if registry == nil {
			registry = NewClaudeMCPRegistry()
		}
		known, err := registry.List(configDir)
		if err != nil {
			slog.Warn("mcp.GenerateCodexConfigTomlWithExtras: registry list failed", "err", err)
		}
		byName := map[string]KnownMCP{}
		for _, k := range known {
			byName[k.Name] = k
		}
		for _, name := range extras {
			// Inline-config names are written verbatim below; skip registry resolution.
			if len(opts.ExtraMCPServers[name]) > 0 {
				continue
			}
			entry, ok := byName[name]
			if !ok {
				res.Unavailable = append(res.Unavailable, name)
				continue
			}
			sb.WriteString("\n")
			writeCodexMCPServerTOML(&sb, name, entry.Command, entry.Args, entry.Env)
			res.WrittenServers = append(res.WrittenServers, name)
		}
	}

	// Inline server configs (scene-authored JSON snippets). Codex's config.toml
	// only models stdio servers (command/args/env); an inline entry without a
	// command (e.g. an http/url server) can't be expressed, so it is surfaced
	// as unavailable rather than silently dropped.
	for name, cfg := range opts.ExtraMCPServers {
		if name == "" || len(cfg) == 0 {
			continue
		}
		command, _ := cfg["command"].(string)
		if command == "" {
			res.Unavailable = append(res.Unavailable, name)
			continue
		}
		sb.WriteString("\n")
		writeCodexMCPServerTOML(&sb, name, command, anyToStringSlice(cfg["args"]), anyToStringMap(cfg["env"]))
		res.WrittenServers = append(res.WrittenServers, name)
	}

	codexDir := filepath.Join(wsPath, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return nil, fmt.Errorf("create .codex dir: %w", err)
	}
	finalPath := filepath.Join(codexDir, "config.toml")
	content := []byte(sb.String())
	if existing, err := os.ReadFile(finalPath); err == nil && string(existing) == string(content) {
		return res, nil
	}
	tmpPath := finalPath + ".tmp-" + randSuffix()
	// 0600: config.toml may carry scene-injected secrets (mirrors .mcp.json;
	// spec §8 file permission gate).
	if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
		return nil, fmt.Errorf("write tmp codex config.toml: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("rename codex config.toml: %w", err)
	}
	return res, nil
}

func writeCodexMCPServerTOML(sb *strings.Builder, name, command string, args []string, env map[string]string) {
	sb.WriteString("[mcp_servers.")
	sb.WriteString(name)
	sb.WriteString("]\n")
	sb.WriteString("command = ")
	sb.WriteString(tomlQuote(filepath.ToSlash(command)))
	sb.WriteString("\n")
	sb.WriteString("args = [")
	for i, a := range args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(tomlQuote(a))
	}
	sb.WriteString("]\n")
	if len(env) == 0 {
		return
	}
	sb.WriteString("\n[mcp_servers.")
	sb.WriteString(name)
	sb.WriteString(".env]\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(" = ")
		sb.WriteString(tomlQuote(env[k]))
		sb.WriteString("\n")
	}
}

// anyToStringSlice coerces a JSON-decoded value (typically []any of strings)
// into []string, dropping non-string elements. Used to read an inline MCP
// config's "args" array.
func anyToStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// anyToStringMap coerces a JSON-decoded value (typically map[string]any of
// strings) into map[string]string, dropping non-string values. Used to read an
// inline MCP config's "env" object.
func anyToStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, e := range m {
		if s, ok := e.(string); ok {
			out[k] = s
		}
	}
	return out
}

func tomlStringArray(items []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tomlQuote(item))
	}
	b.WriteByte(']')
	return b.String()
}

// tomlQuote returns a TOML basic-string literal. Backslashes and double
// quotes are escaped, plus the standard \n / \r / \t controls. Anything
// else is passed through verbatim (TOML basic strings allow all
// printable Unicode without escaping). Sufficient for paths, args, and
// the niuniu MCP token (hex string).
func tomlQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// FindMCPBinary locates the niuniu-mcp binary using a 4-step fallback:
// 1. Same directory as the running executable
// 2. bin/ relative to the working directory
// 3. Walk up from workspace base_dir looking for bin/
// 4. PATH lookup
func (g *MCPConfigGenerator) FindMCPBinary() string {
	g.mcpBinMu.Lock()
	defer g.mcpBinMu.Unlock()
	if g.cachedMCPBin != "" {
		return g.cachedMCPBin
	}

	found := g.findMCPBinaryUncached()
	if found != "" {
		g.cachedMCPBin = found
	}
	return found
}

func (g *MCPConfigGenerator) findMCPBinaryUncached() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	exactName := "niuniu-mcp" + suffix
	hostMatch := runtime.GOOS + "-" + runtime.GOARCH

	// 1. Same directory as the running executable.
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, exactName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if found := findLatestBinaryInDir(dir, "niuniu-mcp", suffix, hostMatch); found != "" {
			return found
		}
	}

	// 2. bin/ relative to the working directory.
	if cwd, err := os.Getwd(); err == nil {
		binDir := filepath.Join(cwd, "bin")
		if found := findLatestBinaryInDir(binDir, "niuniu-mcp", suffix, hostMatch); found != "" {
			return found
		}
	}

	// 3. Walk up from workspace base_dir.
	if g.workspaceCfg.BaseDir != "" {
		dir := filepath.Dir(g.workspaceCfg.BaseDir)
		for i := 0; i < 3; i++ {
			binDir := filepath.Join(dir, "bin")
			if found := findLatestBinaryInDir(binDir, "niuniu-mcp", suffix, hostMatch); found != "" {
				return found
			}
			dir = filepath.Dir(dir)
		}
	}

	// 4. PATH lookup.
	path, err := exec.LookPath("niuniu-mcp")
	if err == nil {
		return path
	}

	return ""
}

// findLatestBinaryInDir finds the newest binary matching prefix*suffix in dir.
//
// hostMatch (e.g. "linux-amd64") narrows the candidate set to filenames
// containing that exact substring — necessary because `make build-mac` and
// similar produce both darwin-arm64 and darwin-amd64 binaries side-by-side
// in bin/, and a pure mtime tiebreaker would happily return the wrong arch.
// When no candidate contains hostMatch (the personal-mode cache holds
// hash-suffixed names like niuniu-mcp-deadbeef.exe with no os/arch token,
// and host-only `make build-mcp` produces niuniu-mcp-vX.Y.Z without one),
// the function falls back to the previous "newest among all matches"
// behavior so those layouts keep working.
func findLatestBinaryInDir(dir, prefix, suffix, hostMatch string) string {
	pattern := filepath.Join(dir, prefix+"*"+suffix)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}

	pickLatest := func(candidates []string) string {
		var best string
		var bestTime time.Time
		for _, m := range candidates {
			info, err := os.Stat(m)
			if err != nil || info.IsDir() {
				continue
			}
			if best == "" || info.ModTime().After(bestTime) {
				best = m
				bestTime = info.ModTime()
			}
		}
		return best
	}

	if hostMatch != "" {
		var hostMatches []string
		for _, m := range matches {
			if strings.Contains(filepath.Base(m), hostMatch) {
				hostMatches = append(hostMatches, m)
			}
		}
		if len(hostMatches) > 0 {
			return pickLatest(hostMatches)
		}
	}
	return pickLatest(matches)
}

// resolveAPIBase returns the http://127.0.0.1:<port> URL the embedded
// niuniu-mcp client should use to reach the running server.
//
// In personal/embedded mode the server binds 127.0.0.1:0 (ephemeral port)
// and writes the actual port to ~/.niuniu/server.lock. cfg.Server.Port
// stays at whatever the user configured (3000 by default), so we MUST
// prefer the lockfile addr when one exists — otherwise every workspace
// gets a .mcp.json pointing at a port nothing is listening on, and every
// MCP tool call fails with a connection-refused error.
//
// Falls back to cfg.Server.Port (or 3000) when the lockfile is missing,
// unreadable, or doesn't carry a parseable addr. We always emit the
// 127.0.0.1 loopback regardless of the bind host so 0.0.0.0 / [::] don't
// leak into the URL.
func (g *MCPConfigGenerator) resolveAPIBase() string {
	port := g.detectActualPort()
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// buildNiuniuArgs assembles the niuniu-mcp CLI args for a workspace, shared by
// the .mcp.json generator (Claude) and the goose MCP-client session.
func (g *MCPConfigGenerator) buildNiuniuArgs(opts config.MCPGenerateOptions) []string {
	apiBase := g.resolveAPIBase()
	args := []string{"--api-base", apiBase}
	if opts.ProjectID > 0 {
		args = append(args, "--project-id", strconv.FormatInt(opts.ProjectID, 10))
	}
	if opts.WorkspaceID > 0 {
		args = append(args, "--workspace-id", strconv.FormatInt(opts.WorkspaceID, 10))
	}
	if opts.InboxDir != "" {
		args = append(args, "--inbox-dir", opts.InboxDir)
	}
	if opts.AgentName != "" && opts.AgentName != "agent" {
		args = append(args, "--agent-name", opts.AgentName)
	}
	if len(opts.DisableToolGroups) > 0 {
		args = append(args, "--disable-tool-groups", strings.Join(opts.DisableToolGroups, ","))
	}
	if len(opts.EnableToolGroups) > 0 {
		args = append(args, "--enable-tool-groups", strings.Join(opts.EnableToolGroups, ","))
	}
	return args
}

// NiuniuMcpServer resolves the niuniu-mcp server entry for a workspace so an
// MCP-client agent (goose) can consume niuniu's kanban / data / memory /
// document tools directly. Returns an error when the niuniu-mcp binary is
// missing (the agent then runs without the niuniu MCP surface).
func (g *MCPConfigGenerator) NiuniuMcpServer(opts config.MCPGenerateOptions) (config.McpServerEntry, error) {
	mcpBin := g.FindMCPBinary()
	if mcpBin == "" {
		return config.McpServerEntry{}, fmt.Errorf("niuniu-mcp binary not found in any search path")
	}
	entry := config.McpServerEntry{
		Name:    "niuniu",
		Command: mcpBin,
		Args:    g.buildNiuniuArgs(opts),
	}
	if opts.SessionToken != "" {
		entry.Env = map[string]string{"NIUNIU_MCP_TOKEN": opts.SessionToken}
	}
	return entry, nil
}

func (g *MCPConfigGenerator) detectActualPort() int {
	if g.dataDir != "" {
		if p, ok := readLockfilePort(filepath.Join(g.dataDir, "server.lock")); ok {
			return p
		}
	}
	if g.serverCfg.Port != 0 {
		return g.serverCfg.Port
	}
	return 3000
}

func readLockfilePort(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var info struct {
		Addr string `json:"addr"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return 0, false
	}
	if info.Addr == "" {
		return 0, false
	}
	_, portStr, err := net.SplitHostPort(info.Addr)
	if err != nil {
		return 0, false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 {
		return 0, false
	}
	return p, true
}

// SetWorkspaceEnabledPlugins merges enabledPlugins=true for the given plugin
// ids into <wsPath>/.claude/settings.json, preserving hooks and all other keys.
func (g *MCPConfigGenerator) SetWorkspaceEnabledPlugins(wsPath string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	g.settingsMu.Lock()
	defer g.settingsMu.Unlock()

	settingsPath := filepath.Join(wsPath, ".claude", "settings.json")
	root := map[string]any{}
	if b, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse settings.json: %w", err)
		}
		if root == nil {
			root = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read settings.json: %w", err)
	}
	ep, _ := root["enabledPlugins"].(map[string]any)
	if ep == nil {
		ep = map[string]any{}
	}
	changed := false
	for _, id := range ids {
		if id == "" {
			continue
		}
		// enabledPlugins is keyed by canonical name@marketplace ids only.
		// Scene sources that are git/file URLs have no marketplace id here, so
		// skip them (their per-workspace enable, if needed, must use a real id).
		if !isMarketplacePluginID(id) {
			continue
		}
		if v, ok := ep[id].(bool); !ok || !v {
			ep[id] = true
			changed = true
		}
	}
	if !changed {
		return nil
	}
	root["enabledPlugins"] = ep
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	tmp := settingsPath + ".tmp-" + randSuffix()
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath)
}

// isMarketplacePluginID reports whether s is a canonical Claude plugin id of the
// form name@marketplace (not a github:/https:/file: source).
func isMarketplacePluginID(s string) bool {
	if strings.HasPrefix(s, "github:") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "file://") {
		return false
	}
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1
}

// existingNonNiuniuServers returns the non-niuniu MCP server entries currently
// in the file at path (hand-edited or scene-projected), keyed by name. Used by
// Generate's extras==nil path to carry those servers forward while still
// rewriting the niuniu entry (with its session token). Returns nil for a
// missing/malformed file or one with only the niuniu entry — the caller then
// just writes the niuniu entry. Never errors.
func existingNonNiuniuServers(path string) map[string]map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}
	out := map[string]map[string]any{}
	for name, cfg := range doc.MCPServers {
		if name != "niuniu" && len(cfg) > 0 {
			out[name] = cfg
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// existingMCPToken returns the NIUNIU_MCP_TOKEN currently written into the
// niuniu server entry of an existing .mcp.json, or "" if absent/unreadable. Used
// to carry the session token across regenerations whose caller has no token
// (scene re-projection), so the agent's /mcp auth survives the rewrite.
func existingMCPToken(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		MCPServers struct {
			Niuniu struct {
				Env struct {
					Token string `json:"NIUNIU_MCP_TOKEN"`
				} `json:"env"`
			} `json:"niuniu"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return ""
	}
	return doc.MCPServers.Niuniu.Env.Token
}
