package git

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Hidden-ref checkpoint system (spec: autohost 安全网). A checkpoint captures the
// FULL working-tree state of a worktree at a moment in time as a commit object,
// pointed to by a hidden ref under refs/niuniu/<workspace>/<issue>/<step>. These
// refs live outside refs/heads and refs/remotes, so:
//
//   - they never appear in `git branch`/`branch -a` (no branch-history pollution),
//   - `git push` (default push.default) never ships them upstream,
//   - `git gc` keeps the snapshot commits alive as long as the ref exists,
//   - each step is an independent ref, giving a per-step timeline with a real diff
//     between consecutive snapshots.
//
// The snapshot is taken with a throwaway index (GIT_INDEX_FILE), so the worktree's
// real index/HEAD are never touched: `git add -A` into a fresh temp index stages
// every tracked+untracked (gitignore-respecting) path, `write-tree` freezes it, and
// `commit-tree` chains it onto the previous checkpoint (or HEAD) so the ref graph
// reads like a timeline. Ignored files (node_modules, build output) are excluded.

// checkpointIdentity is the author/committer stamped on snapshot commits so
// commit-tree never fails on a worktree with no configured user.name/email
// (e.g. fresh test repos). It is intentionally distinct from any human identity.
var checkpointIdentity = []string{
	"GIT_AUTHOR_NAME=niuniu-checkpoint",
	"GIT_AUTHOR_EMAIL=checkpoint@niuniu.local",
	"GIT_COMMITTER_NAME=niuniu-checkpoint",
	"GIT_COMMITTER_EMAIL=checkpoint@niuniu.local",
}

// Checkpoint is a single hidden-ref snapshot: its ref, the commit it points at,
// the parent it was chained onto, the numeric step parsed from the ref, the
// snapshot message and the committer date (ISO-8601). Populated by ListCheckpoints.
type Checkpoint struct {
	Ref       string `json:"ref"`
	Commit    string `json:"commit"`
	Parent    string `json:"parent"`
	Step      int    `json:"step"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// CheckpointRefPrefix builds the hidden-ref namespace for an (workspace, issue)
// pair: refs/niuniu/<workspace>/<issue>. The step is appended by CheckpointRef.
// workspace/issue are numeric IDs at the call sites, so the result is always a
// valid ref name; sanitizeRefComponent defends the primitive against odd input.
func CheckpointRefPrefix(workspace, issue string) string {
	return "refs/niuniu/" + sanitizeRefComponent(workspace) + "/" + sanitizeRefComponent(issue)
}

// CheckpointRef builds the full hidden ref for one step.
func CheckpointRef(workspace, issue string, step int) string {
	return CheckpointRefPrefix(workspace, issue) + "/" + strconv.Itoa(step)
}

// sanitizeRefComponent maps a path component to something git accepts inside a
// ref name: git forbids space, ~, ^, :, ?, *, [, \, sequences of dots and a few
// more. Numeric IDs pass through untouched; anything exotic collapses to '-'.
func sanitizeRefComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	return out
}

// checkpointEnv returns the process env extended with the checkpoint identity and
// (optionally) a throwaway index file, plus the UTF-8 locale the rest of the
// package standardizes on.
func checkpointEnv(indexFile string) []string {
	env := append(os.Environ(), checkpointIdentity...)
	env = append(env, "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	if indexFile != "" {
		env = append(env, "GIT_INDEX_FILE="+indexFile)
	}
	return env
}

// WriteCheckpoint snapshots the entire working tree of worktreePath into a commit
// object and moves ref to point at it, WITHOUT touching the worktree's real index
// or HEAD. parent, when a resolvable revision, becomes the commit's parent so the
// checkpoint chain forms a timeline; when empty it falls back to HEAD (and if even
// HEAD does not resolve — a repo with no commits — the snapshot is parentless, a
// root commit). Returns the new snapshot commit hash AND the parent hash it was
// actually committed against (empty for a root commit) — callers persist the latter
// so stored metadata reflects the commit's real parent, not the requested one.
func WriteCheckpoint(worktreePath, ref, parent, message string) (commit string, parentHash string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("checkpoint: ref is required")
	}

	// Throwaway index: create a temp name, then remove it so git writes a fresh
	// index (an existing empty file is rejected as a malformed index).
	tmp, terr := os.CreateTemp("", "niuniu-ckpt-index-*")
	if terr != nil {
		return "", "", fmt.Errorf("checkpoint: temp index: %w", terr)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	defer os.Remove(tmpPath)

	env := checkpointEnv(tmpPath)

	// Stage the full worktree into the throwaway index. -A captures tracked and
	// untracked paths (gitignore-respected); with a fresh index everything is an
	// addition, so write-tree yields a tree of the complete worktree.
	addCmd := exec.Command("git", "-C", worktreePath, "add", "-A")
	addCmd.Env = env
	if out, aerr := addCmd.CombinedOutput(); aerr != nil {
		return "", "", fmt.Errorf("checkpoint: git add -A: %s: %w", strings.TrimSpace(string(out)), aerr)
	}

	writeTree := exec.Command("git", "-C", worktreePath, "write-tree")
	writeTree.Env = env
	treeOut, werr := writeTree.Output()
	if werr != nil {
		return "", "", fmt.Errorf("checkpoint: git write-tree: %w", werr)
	}
	tree := strings.TrimSpace(string(treeOut))
	if tree == "" {
		return "", "", fmt.Errorf("checkpoint: empty tree from write-tree")
	}

	// Resolve the parent: explicit parent first, then HEAD, else parentless.
	if p := strings.TrimSpace(parent); p != "" {
		if h, rerr := resolveRef(worktreePath, p); rerr == nil {
			parentHash = h
		}
	}
	if parentHash == "" {
		if h, rerr := resolveRef(worktreePath, "HEAD"); rerr == nil {
			parentHash = h
		}
	}

	args := []string{"-C", worktreePath, "commit-tree", tree}
	if parentHash != "" {
		args = append(args, "-p", parentHash)
	}
	if message == "" {
		message = "niuniu checkpoint"
	}
	args = append(args, "-m", message)
	commitCmd := exec.Command("git", args...)
	commitCmd.Env = checkpointEnv("") // no throwaway index for commit-tree
	commitOut, cerr := commitCmd.Output()
	if cerr != nil {
		return "", "", fmt.Errorf("checkpoint: git commit-tree: %w", cerr)
	}
	commit = strings.TrimSpace(string(commitOut))
	if commit == "" {
		return "", "", fmt.Errorf("checkpoint: empty commit from commit-tree")
	}

	upd := exec.Command("git", "-C", worktreePath, "update-ref", ref, commit)
	if out, uerr := upd.CombinedOutput(); uerr != nil {
		return "", "", fmt.Errorf("checkpoint: git update-ref %s: %s: %w", ref, strings.TrimSpace(string(out)), uerr)
	}
	return commit, parentHash, nil
}

// ListCheckpoints returns every checkpoint under prefix (e.g. the output of
// CheckpointRefPrefix), ordered by ascending step. The step is parsed from the
// last path segment of the ref; non-numeric segments sort last. Each entry
// carries its commit, first parent, message and committer date.
func ListCheckpoints(worktreePath, prefix string) ([]Checkpoint, error) {
	// %00-separated fields keep messages with spaces intact; newlines terminate rows.
	cmd := exec.Command("git", "-C", worktreePath, "for-each-ref",
		"--format=%(refname)%00%(objectname)%00%(*committerdate:iso-strict)%00%(committerdate:iso-strict)",
		prefix)
	cmd.Env = checkpointEnv("")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("checkpoint: for-each-ref: %w", err)
	}
	var cps []Checkpoint
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) < 2 {
			continue
		}
		ref := fields[0]
		commit := fields[1]
		date := ""
		if len(fields) >= 4 {
			// A checkpoint ref points at a commit (not a tag), so the annotated-tag
			// *committerdate is empty; fall back to the plain committerdate.
			date = fields[2]
			if strings.TrimSpace(date) == "" {
				date = fields[3]
			}
		}
		cp := Checkpoint{Ref: ref, Commit: commit, Step: stepFromRef(ref), CreatedAt: strings.TrimSpace(date)}
		// Enrich with parent + message from the commit object.
		if p, m, ok := commitParentMessage(worktreePath, commit); ok {
			cp.Parent = p
			cp.Message = m
		}
		cps = append(cps, cp)
	}
	sort.SliceStable(cps, func(i, j int) bool { return cps[i].Step < cps[j].Step })
	return cps, nil
}

// stepFromRef parses the trailing numeric segment of a checkpoint ref. Returns a
// large sentinel for a non-numeric tail so malformed refs sort after real steps.
func stepFromRef(ref string) int {
	seg := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		seg = ref[i+1:]
	}
	n, err := strconv.Atoi(seg)
	if err != nil {
		return 1 << 30
	}
	return n
}

// commitParentMessage returns the first parent hash and subject of a commit.
func commitParentMessage(worktreePath, commit string) (parent, message string, ok bool) {
	cmd := exec.Command("git", "-C", worktreePath, "show", "-s", "--format=%P%x00%s", commit)
	cmd.Env = checkpointEnv("")
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimRight(string(out), "\r\n"), "\x00", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	parents := strings.Fields(parts[0])
	if len(parents) > 0 {
		parent = parents[0]
	}
	return parent, parts[1], true
}

// CheckpointDiff parses the diff between two revisions (from -> to) into structured
// FileDiff objects, reusing the package diff parser. When from is empty it diffs
// the "to" commit against its first parent — i.e. the change that checkpoint itself
// introduced. Untracked/ignored handling is irrelevant here: both sides are commits.
func CheckpointDiff(worktreePath, from, to string) ([]FileDiff, error) {
	if strings.TrimSpace(to) == "" {
		return nil, fmt.Errorf("checkpoint diff: 'to' revision is required")
	}
	args := []string{"-C", worktreePath, "diff"}
	if strings.TrimSpace(from) == "" {
		// Diff against the first parent; for a root commit fall back to the empty
		// tree so the whole snapshot shows as additions.
		if p, _, ok := commitParentMessage(worktreePath, to); ok && p != "" {
			args = append(args, p, to)
		} else {
			args = append(args, emptyTreeHash, to)
		}
	} else {
		args = append(args, from, to)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = checkpointEnv("")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("checkpoint diff: git diff: %w", err)
	}
	return parseDiff(string(out))
}

// emptyTreeHash is git's well-known hash of the empty tree, used as the "from"
// side when diffing a root checkpoint (which has no parent).
const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// RevertToCheckpoint restores the worktree's files to exactly the snapshot commit's
// tree, WITHOUT rewriting history: HEAD and the branch are untouched, and every
// checkpoint ref (including LATER ones) survives, so no earlier or later change is
// lost — a subsequent revert to a newer checkpoint fully re-applies it. The change
// shows up as an ordinary working-tree modification against HEAD.
//
// Sequence (order matters): read the snapshot tree into the real index, materialize
// it over the worktree, drop files absent from the snapshot, then reset the index
// back to HEAD so `git status` reads as a normal uncommitted delta. `git clean`
// runs WITHOUT -x, so ignored files (node_modules, build artifacts) are preserved.
func RevertToCheckpoint(worktreePath, commit string) error {
	if strings.TrimSpace(commit) == "" {
		return fmt.Errorf("checkpoint revert: commit is required")
	}
	env := checkpointEnv("")

	readTree := exec.Command("git", "-C", worktreePath, "read-tree", commit)
	readTree.Env = env
	if out, err := readTree.CombinedOutput(); err != nil {
		return fmt.Errorf("checkpoint revert: read-tree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	checkout := exec.Command("git", "-C", worktreePath, "checkout-index", "-f", "-a")
	checkout.Env = env
	if out, err := checkout.CombinedOutput(); err != nil {
		return fmt.Errorf("checkpoint revert: checkout-index: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// Remove files that are NOT in the snapshot. The index currently equals the
	// snapshot tree, so those files read as untracked. -d removes empty dirs; no -x,
	// so ignored files stay.
	clean := exec.Command("git", "-C", worktreePath, "clean", "-fd")
	clean.Env = env
	if out, err := clean.CombinedOutput(); err != nil {
		return fmt.Errorf("checkpoint revert: clean: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// Reset the index back to HEAD (mixed) so the worktree delta shows naturally.
	// A repo with no HEAD (no commits) simply skips this — the snapshot is already
	// materialized.
	if _, err := resolveRef(worktreePath, "HEAD"); err == nil {
		reset := exec.Command("git", "-C", worktreePath, "reset", "-q")
		reset.Env = env
		if out, err := reset.CombinedOutput(); err != nil {
			return fmt.Errorf("checkpoint revert: reset: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

// DeleteCheckpoint removes a single checkpoint ref (the snapshot commit is left to
// git gc once unreachable). Idempotent: deleting a missing ref is not an error.
func DeleteCheckpoint(worktreePath, ref string) error {
	cmd := exec.Command("git", "-C", worktreePath, "update-ref", "-d", ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		// update-ref -d on a non-existent ref is a no-op we treat as success.
		if strings.Contains(msg, "not exist") || strings.Contains(msg, "unable to resolve") {
			return nil
		}
		return fmt.Errorf("checkpoint delete: update-ref -d %s: %s: %w", ref, msg, err)
	}
	return nil
}

// ResolveRevision resolves a revision/ref to its full commit hash, or "" if it does
// not resolve. Exported thin wrapper over the package-internal resolveRef so
// callers outside git (e.g. the checkpoint service) can validate a ref cheaply.
func ResolveRevision(worktreePath, rev string) string {
	h, err := resolveRef(worktreePath, rev)
	if err != nil {
		return ""
	}
	return h
}
