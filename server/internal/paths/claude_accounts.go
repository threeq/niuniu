// Package paths centralizes filesystem path construction for niuniu
// resources that don't fit the per-owner OwnerRef pattern.
package paths

import "path/filepath"

// ClaudeAccountDir returns the per-account isolated config directory used as
// CLAUDE_CONFIG_DIR when spawning the Claude CLI. Layout:
//
//	<dataDir>/claude-accounts/<uuid>/
//
// Deployment-global (not owner-scoped) — see
// docs/superpowers/specs/2026-05-08-claude-multi-account-design.md §"路径 helper".
func ClaudeAccountDir(dataDir, uuid string) string {
	return filepath.Join(dataDir, "claude-accounts", uuid)
}
