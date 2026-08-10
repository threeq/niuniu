package paths

import "path/filepath"

// CodexAccountDir returns the per-account isolated config directory used as
// CODEX_HOME when spawning the Codex CLI. Layout:
//
//	<dataDir>/codex-accounts/<uuid>/
//
// Deployment-global (not owner-scoped), same pattern as ClaudeAccountDir.
// See docs/superpowers/specs/2026-05-22-codex-full-support-design.md A.
func CodexAccountDir(dataDir, uuid string) string {
	return filepath.Join(dataDir, "codex-accounts", uuid)
}
