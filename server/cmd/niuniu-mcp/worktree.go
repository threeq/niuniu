package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// worktreeHookInput is the subset of the Claude Code hook stdin payload
// (https://code.claude.com/docs/en/hooks) we care about for WorktreeCreate
// and WorktreeRemove. Other fields (transcript_path, hook_event_name, etc.)
// are accepted via UnknownFields = ignored — the JSON decoder discards them.
type worktreeHookInput struct {
	SessionID    string `json:"session_id"`
	CWD          string `json:"cwd"`
	WorktreePath string `json:"worktree_path"`
}

// sourceMarkerRel is the workspace-relative path of the file we drop into
// every isolated worktree pointing back at the canonical "source" workspace.
// Nested calls (sub-agent inside an isolated worktree spawning its own
// sub-agent) read this marker and create new worktrees as siblings of the
// existing isolated copies, rather than recursively under them — keeps the
// directory hierarchy flat.
const sourceMarkerRel = ".workspace-hooks/source"

// resolveSourceWorkspace returns the canonical source workspace dir.
// When cwd is itself an isolated worktree (.workspace-hooks/source exists),
// the marker's value wins so cascaded isolation collapses back to the
// original source instead of nesting forever.
func resolveSourceWorkspace(cwd string) string {
	if data, err := os.ReadFile(filepath.Join(cwd, sourceMarkerRel)); err == nil {
		if src := strings.TrimSpace(string(data)); src != "" {
			return src
		}
	}
	return cwd
}

// runWorktreeCreate is the Go implementation of the WorktreeCreate hook.
// Reads the JSON payload from stdin, builds a real isolated workspace at
// worktree_path by `git worktree add`-ing each repo under
// <source>/.worktrees/<repo>, copies workspace-level shared files
// (CLAUDE.md, .mcp.json, .team, ...), drops the nesting marker, and prints
// the absolute worktree path to stdout — and ONLY the path, no trailing
// newline. Any non-zero return blocks Claude's worktree creation.
func runWorktreeCreate(stdin io.Reader, stdout, stderr io.Writer) int {
	var input worktreeHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil {
		fmt.Fprintf(stderr, "worktree-create: invalid stdin JSON: %v\n", err)
		return 1
	}
	if input.WorktreePath == "" || input.CWD == "" {
		fmt.Fprint(stderr, "worktree-create: missing worktree_path or cwd in input\n")
		return 1
	}

	sourceWS := resolveSourceWorkspace(input.CWD)

	if err := os.MkdirAll(input.WorktreePath, 0o755); err != nil {
		fmt.Fprintf(stderr, "worktree-create: mkdir worktree path: %v\n", err)
		return 1
	}

	if err := createSubWorktrees(sourceWS, input.WorktreePath, input.SessionID, stderr); err != nil {
		// createSubWorktrees already wrote diagnostics; abort the hook so
		// Claude surfaces the error rather than handing the agent a
		// half-built workspace.
		return 1
	}

	copyWorkspaceFiles(sourceWS, input.WorktreePath, stderr)

	if err := writeSourceMarker(input.WorktreePath, sourceWS); err != nil {
		// Marker is required for nested-isolation collapse; warn but do
		// not abort — the hook still produced a usable single-level
		// isolated worktree.
		fmt.Fprintf(stderr, "worktree-create: write source marker: %v\n", err)
	}

	fmt.Fprint(stdout, input.WorktreePath)
	return 0
}

// runWorktreeRemove is the Go implementation of the WorktreeRemove hook.
// Per the docs this hook cannot block — failures are advisory. We do our
// best to deregister each git worktree from its source repo (so
// `git worktree list` stays clean) and rm -rf the directory.
func runWorktreeRemove(stdin io.Reader, stdout, stderr io.Writer) int {
	var input worktreeHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil {
		// Cannot block; swallow the parse error.
		return 0
	}
	if input.WorktreePath == "" {
		return 0
	}

	sourceWS := resolveSourceWorkspace(input.CWD)
	removeSubWorktrees(sourceWS, input.WorktreePath)
	pruneSourceWorktrees(sourceWS)
	_ = os.RemoveAll(input.WorktreePath)
	return 0
}

// createSubWorktrees creates one `git worktree add -b agent/<repo>-<id>`
// per source repo under <source>/.worktrees/<repo>. Branches use a
// per-call short id derived from the session id (or random on miss) so
// repeated calls within the same session don't collide on the branch name.
func createSubWorktrees(sourceWS, isolatedRoot, sessionID string, stderr io.Writer) error {
	srcRepoRoot := filepath.Join(sourceWS, ".worktrees")
	entries, err := os.ReadDir(srcRepoRoot)
	if err != nil {
		// No .worktrees/ at the source — nothing to clone, not an error.
		// Workspaces without git repos still get an isolated dir + copied
		// workspace files; the agent just has no code to work on.
		if os.IsNotExist(err) {
			return nil
		}
		fmt.Fprintf(stderr, "worktree-create: read source .worktrees: %v\n", err)
		return err
	}

	newRepoRoot := filepath.Join(isolatedRoot, ".worktrees")
	if err := os.MkdirAll(newRepoRoot, 0o755); err != nil {
		fmt.Fprintf(stderr, "worktree-create: mkdir isolated .worktrees: %v\n", err)
		return err
	}

	id := shortID(sessionID)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		srcRepo := filepath.Join(srcRepoRoot, e.Name())
		// .git can be either a directory (canonical worktree) or a file
		// (linked worktree gitlink). Both pass os.Stat.
		if _, err := os.Stat(filepath.Join(srcRepo, ".git")); err != nil {
			continue
		}
		newRepo := filepath.Join(newRepoRoot, e.Name())
		branch := "agent/" + e.Name() + "-" + id
		cmd := exec.Command("git", "-C", srcRepo, "worktree", "add",
			"-b", branch, newRepo, "HEAD")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(stderr, "worktree-create: git worktree add %s: %s: %v\n",
				e.Name(), strings.TrimSpace(string(out)), err)
			return err
		}
	}
	return nil
}

// copyWorkspaceFiles shallow-copies workspace-level shared files into the
// isolated worktree. .team is intentionally copied (not symlinked) so the
// isolated workspace has its own snapshot of inboxes; the .mcp.json that
// goes with it still points at the source workspace's inboxes via
// absolute paths, which is what we want — the niuniu MCP server keys
// inbox storage by workspace_id at the server side anyway.
//
// .claude is copied so the isolated workspace inherits the same hooks
// (the agent inside can spawn its own sub-agents and the WorktreeCreate
// hook fires correctly there too — limitation 2 fix).
func copyWorkspaceFiles(sourceWS, dst string, stderr io.Writer) {
	items := []string{
		"CLAUDE.md",
		".mcp.json",
		".learnings.generated.md",
		".team",
		".claude",
	}
	for _, name := range items {
		srcPath := filepath.Join(sourceWS, name)
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		dstPath := filepath.Join(dst, name)
		if err := copyTree(srcPath, dstPath); err != nil {
			fmt.Fprintf(stderr, "worktree-create: copy %s: %v\n", name, err)
		}
	}
}

func writeSourceMarker(isolatedRoot, sourceWS string) error {
	markerDir := filepath.Join(isolatedRoot, ".workspace-hooks")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(markerDir, "source"), []byte(sourceWS), 0o644)
}

// removeSubWorktrees deregisters every git worktree we created at the
// source repo. Failures are tolerated — the directory is rm'd unconditionally
// at the end of runWorktreeRemove.
func removeSubWorktrees(sourceWS, isolatedRoot string) {
	subRoot := filepath.Join(isolatedRoot, ".worktrees")
	entries, err := os.ReadDir(subRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subRepo := filepath.Join(subRoot, e.Name())
		srcRepo := filepath.Join(sourceWS, ".worktrees", e.Name())
		if _, err := os.Stat(filepath.Join(srcRepo, ".git")); err != nil {
			continue
		}
		// Best-effort; suppress output and errors. `git worktree prune`
		// runs after to clean up the registry if remove failed.
		_ = exec.Command("git", "-C", srcRepo, "worktree", "remove",
			"--force", subRepo).Run()
	}
}

// pruneSourceWorktrees runs `git worktree prune` against every source repo
// to clean up registry entries left dangling by failed removes (the on-disk
// dir gets rm-rf'd unconditionally, so the registry always needs a sweep).
func pruneSourceWorktrees(sourceWS string) {
	entries, err := os.ReadDir(filepath.Join(sourceWS, ".worktrees"))
	if err != nil {
		return
	}
	for _, e := range entries {
		srcRepo := filepath.Join(sourceWS, ".worktrees", e.Name())
		if _, err := os.Stat(filepath.Join(srcRepo, ".git")); err != nil {
			continue
		}
		_ = exec.Command("git", "-C", srcRepo, "worktree", "prune").Run()
	}
}

// shortID derives a filesystem-safe short id used in agent branch names.
// Prefers the leading [a-zA-Z0-9] from the session id (typically the first
// 8 chars of the Claude session UUID after stripping dashes); falls back to
// crypto/rand on miss. A 4-char random tail is always appended so branch
// names within the same session stay unique across repeated spawns.
func shortID(sessionID string) string {
	var head strings.Builder
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			head.WriteRune(r)
			if head.Len() >= 8 {
				break
			}
		}
	}
	if head.Len() == 0 {
		head.WriteString(fmt.Sprintf("%x", time.Now().UnixNano()&0xffffffff))
	}
	tail := make([]byte, 2)
	if _, err := rand.Read(tail); err != nil {
		// crypto/rand should never fail on supported platforms; degrade
		// to a nanosecond suffix so we still produce a valid branch name.
		return head.String() + "-" + fmt.Sprintf("%04x", time.Now().UnixNano()&0xffff)
	}
	return head.String() + "-" + hex.EncodeToString(tail)
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if symErr := os.Symlink(target, dst); symErr == nil {
			return nil
		}
		// Fall through to file copy if the platform refuses symlinks
		// (Windows w/o developer mode is the common case).
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
