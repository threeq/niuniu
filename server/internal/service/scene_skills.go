// Package service: vendored-skill projection.
//
// A scene may declare `skills:` — vendored (in-repo) Claude skills it projects
// into the workspace's <wsDir>/.claude/skills/<name>/ directory so the agent
// auto-discovers them (Claude Code reads SKILL.md frontmatter from there) and
// double-lingual trigger keywords activate on demand.
//
// Unlike plugins (which run `claude plugin install` — network + CLI — and are
// gated behind an explicit SPA "Install" click), skills are PURE LOCAL FILE
// COPIES. They are materialized automatically on scene-enable, exactly like the
// agent markdown (materializeWorkspaceAgents): enabling the scene IS the user's
// explicit action, and no installer is ever spawned. Detaching the scene removes
// the niuniu-managed skill dirs again, so "未启用则不影响" holds.
//
// The skill payloads ship embedded in the binary (builtin_skills/, mirrored from
// docs/scenes/skills/ via `make builtin-skills-sync`). docs/ lives outside the
// server module root, so — same as builtin_scenes — it cannot be embedded
// directly and the committed mirror is what //go:embed picks up.
package service

import (
	"embed"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

//go:embed builtin_skills
var builtinSkillsFS embed.FS

// builtinSkillsRoot is the directory inside builtinSkillsFS holding the
// per-skill subdirectories.
const builtinSkillsRoot = "builtin_skills"

// niuniuManagedSkillMarker is dropped into every skill dir niuniu materializes so
// cleanManagedSkillsDir can tell scene-projected skills apart from skills the
// user authored or installed by hand (which must be preserved).
const niuniuManagedSkillMarker = ".niuniu-managed"

// materializeWorkspaceSkills copies each scene-declared skill's vendored payload
// into <wsDir>/.claude/skills/<name>/, stamped with a .niuniu-managed marker so
// it is cleaned up on the next recompute. It first removes all previously
// niuniu-managed skill dirs so that skills from detached scenes disappear; skills
// the user authored themselves (no marker) are left untouched. Codex workspaces
// have no .claude/skills discovery, so only the cleanup runs there.
func (p *SceneProjector) materializeWorkspaceSkills(ws store.Workspace, wsDir string, proj *Projection) {
	skillsDir := filepath.Join(wsDir, ".claude", "skills")
	// Always clear stale niuniu-managed skills first (user skills are preserved).
	if err := cleanManagedSkillsDir(skillsDir); err != nil {
		slog.Warn("materialize skills: cleanup failed", "workspace_id", ws.ID, "err", err)
	}

	if ws.CliType == "codex" || len(proj.Skills) == 0 {
		return
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		slog.Warn("materialize skills: mkdir failed", "workspace_id", ws.ID, "err", err)
		return
	}
	for _, ref := range proj.Skills {
		name := strings.TrimSpace(ref.Name)
		if !isSafeSkillName(name) {
			slog.Warn("materialize skills: unsafe skill name skipped", "workspace_id", ws.ID, "name", ref.Name)
			continue
		}
		src := path.Join(builtinSkillsRoot, name)
		if _, err := fs.Stat(builtinSkillsFS, src); err != nil {
			// Unknown / not-vendored skill — warn and skip (an optional skill not
			// present in this build must not abort the rest of the projection).
			slog.Warn("materialize skills: vendored skill not found", "workspace_id", ws.ID, "name", name)
			continue
		}
		dst := filepath.Join(skillsDir, name)
		// Replace any pre-existing dir of this name with a fresh copy so content
		// updates (and a previous user dir of the same name) don't leave stale files.
		if err := os.RemoveAll(dst); err != nil {
			slog.Warn("materialize skills: remove stale dir failed", "workspace_id", ws.ID, "name", name, "err", err)
			continue
		}
		if err := copyEmbeddedDir(builtinSkillsFS, src, dst); err != nil {
			slog.Warn("materialize skills: copy failed", "workspace_id", ws.ID, "name", name, "err", err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, niuniuManagedSkillMarker), []byte("managed_by: niuniu\n"), 0o644); err != nil {
			slog.Warn("materialize skills: write marker failed", "workspace_id", ws.ID, "name", name, "err", err)
		}
	}
}

// cleanManagedSkillsDir removes every immediate sub-directory of skillsDir that
// carries the niuniuManagedSkillMarker, leaving user-authored skills untouched.
// A missing directory is a no-op.
func cleanManagedSkillsDir(skillsDir string) error {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(skillsDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, niuniuManagedSkillMarker)); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}

// copyEmbeddedDir walks srcRoot inside fsys (an embed.FS — forward-slash paths)
// and writes the tree under dst on the real filesystem.
func copyEmbeddedDir(fsys fs.FS, srcRoot, dst string) error {
	return fs.WalkDir(fsys, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, srcRoot)
		rel = strings.TrimPrefix(rel, "/")
		target := dst
		if rel != "" {
			target = filepath.Join(dst, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
