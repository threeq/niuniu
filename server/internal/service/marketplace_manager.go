// Package service: thin wrapper around `claude plugin marketplace add` used
// by the chat-input skill-install dialog to let users register additional
// plugin marketplaces from the niuniu UI without dropping to a terminal.
//
// Companion to PluginInstaller — both shell out to the `claude` CLI, both
// honor CLAUDE_CONFIG_DIR for scope routing (global vs workspace's bound
// claude-account ConfigDir). Kept in its own file so PluginInstaller stays
// focused on the install / plan / IsInstalled lifecycle.
package service

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

// MarketplaceManager registers Claude Code plugin marketplaces.
type MarketplaceManager struct {
	claudeBin string

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-configDir lock, mirrors PluginInstaller
}

// NewMarketplaceManager constructs the manager. Pass "" for claudeBin to use
// `claude` on PATH (the default — see PluginInstaller for the same pattern).
func NewMarketplaceManager(claudeBin string) *MarketplaceManager {
	return &MarketplaceManager{
		claudeBin: claudeBin,
		locks:     map[string]*sync.Mutex{},
	}
}

// AddResult is the outcome of a single marketplace add call. Stderr is
// truncated at 4 KB.
type AddResult struct {
	Source string // canonical source the caller passed in (e.g. "owner/repo")
	OK     bool
	Stderr string
}

// Add is the backward-compat default-to-claude entry point. New callers
// should use AddForCLI to pick "claude" or "codex" explicitly.
func (m *MarketplaceManager) Add(ctx context.Context, configDir, source string) AddResult {
	return m.AddForCLI(ctx, "claude", configDir, source)
}

// AddForCLI runs `<cli> plugin marketplace add <source>` against the
// resolved CLAUDE_CONFIG_DIR (claude) or CODEX_HOME (codex). cliType
// "" falls back to "claude" for backward compat.
//
// Idempotent at the CLI level: re-adding an existing marketplace exits
// non-zero with a descriptive message, surfaced as OK=false + stderr so
// the SPA can show it without treating it as a hard error.
//
// Source forms accepted: a URL, a local path, or a GitHub repo shorthand
// "owner/repo". This wrapper does not pre-validate — let the CLI's own
// parser do that.
func (m *MarketplaceManager) AddForCLI(ctx context.Context, cliType, configDir, source string) AddResult {
	lock := m.lockFor(configDir)
	lock.Lock()
	defer lock.Unlock()

	binary, envVar := "claude", "CLAUDE_CONFIG_DIR"
	if cliType == "codex" {
		binary, envVar = "codex", "CODEX_HOME"
	}
	bin := binary
	if binary == "claude" && m.claudeBin != "" {
		bin = m.claudeBin
	}
	cmd := exec.CommandContext(ctx, bin, "plugin", "marketplace", "add", source)
	if configDir != "" {
		cmd.Env = append(os.Environ(), envVar+"="+configDir)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return AddResult{Source: source, OK: false, Stderr: truncStderr(out)}
	}
	return AddResult{Source: source, OK: true, Stderr: ""}
}

func (m *MarketplaceManager) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	m.locks[key] = l
	return l
}
