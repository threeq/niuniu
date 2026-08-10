package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrInvalidIdentity is returned by SetGlobalIdentity when name or email
// fails validation.
var ErrInvalidIdentity = errors.New("invalid git identity")

// ErrIdentityMissing is returned by EnsureInitialCommit when the global
// git identity is unset and a commit is therefore impossible.
var ErrIdentityMissing = errors.New("git user.name / user.email not configured")

var emailRegex = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// GetGlobalIdentity reads git config --global user.name / user.email.
// Returns ("", "", nil) when both keys are unset; only returns err for
// actual exec failures (git not on PATH, etc.).
func GetGlobalIdentity(ctx context.Context) (name, email string, err error) {
	name, err = readGlobalKey(ctx, "user.name")
	if err != nil {
		return "", "", err
	}
	email, err = readGlobalKey(ctx, "user.email")
	if err != nil {
		return "", "", err
	}
	return name, email, nil
}

func readGlobalKey(ctx context.Context, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--global", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		// `git config --get` exits 1 when the key is unset; that is not an
		// error from our perspective. Anything else (e.g. binary not found)
		// bubbles up.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SetGlobalIdentity writes both keys to --global config. Validates non-empty
// name and a minimal email regex; returns ErrInvalidIdentity on validation
// failure.
func SetGlobalIdentity(ctx context.Context, name, email string) error {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" {
		return ErrInvalidIdentity
	}
	if !emailRegex.MatchString(email) {
		return ErrInvalidIdentity
	}
	if strings.ContainsAny(name, "\n\r\x00") {
		return ErrInvalidIdentity
	}
	if strings.ContainsAny(email, "\n\r\x00") {
		return ErrInvalidIdentity
	}
	if err := writeGlobalKey(ctx, "user.name", name); err != nil {
		return err
	}
	if err := writeGlobalKey(ctx, "user.email", email); err != nil {
		return err
	}
	return nil
}

func writeGlobalKey(ctx context.Context, key, value string) error {
	cmd := exec.CommandContext(ctx, "git", "config", "--global", key, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// keepNiuniuFileName is the placeholder created in an otherwise-empty
// directory so the first commit has tracked content.
const keepNiuniuFileName = ".keep-niuniu"

const keepNiuniuFileBody = "# placeholder created by niuniu on first init; safe to delete after adding real content\n"

// initialCommitMessage is the fixed message used for niuniu's auto first commit.
const initialCommitMessage = "chore: niuniu init"

// Identity is the author/committer pair used to override repo-local and
// global git config on a per-invocation basis. Both fields must be non-empty
// for the identity to be applied; a zero-valued Identity means "fall back to
// existing repo / global config" (preserves pre-per-user-identity behavior).
type Identity struct {
	Name  string
	Email string
}

// IsZero reports whether the Identity carries no override.
func (i Identity) IsZero() bool { return i.Name == "" && i.Email == "" }

// validateIdentity returns ErrInvalidIdentity when only one of Name/Email is
// set, or when either contains characters that would break the `git -c` arg.
func validateIdentity(id Identity) error {
	if id.IsZero() {
		return nil
	}
	if id.Name == "" || id.Email == "" {
		return ErrInvalidIdentity
	}
	if !emailRegex.MatchString(id.Email) {
		return ErrInvalidIdentity
	}
	if strings.ContainsAny(id.Name, "\n\r\x00") {
		return ErrInvalidIdentity
	}
	if strings.ContainsAny(id.Email, "\n\r\x00") {
		return ErrInvalidIdentity
	}
	return nil
}

// CommitAs stages all changes under `path` (when addAll is true) and creates
// a commit with `msg`. When `id` is non-zero it is injected via `git -c
// user.name=... -c user.email=...`, overriding any repo-local or global git
// config — this is how niuniu attributes commits to the niuniu user that
// triggered the operation. When `id` is zero, falls through to git's normal
// resolution (repo-local then global). Returns ErrIdentityMissing if both id
// is zero AND no repo/global identity is configured.
func CommitAs(ctx context.Context, path string, id Identity, msg string, addAll bool) error {
	if err := validateIdentity(id); err != nil {
		return err
	}
	if id.IsZero() {
		// Preserve legacy contract: surface a clean error rather than letting
		// `git commit` fail with a cryptic "Please tell me who you are".
		repoName, repoEmail, err := repoIdentity(ctx, path)
		if err != nil {
			return err
		}
		if repoName == "" || repoEmail == "" {
			return ErrIdentityMissing
		}
	}
	if addAll {
		if out, err := runIn(ctx, path, "git", "add", "-A"); err != nil {
			return fmt.Errorf("git add -A: %s: %w", out, err)
		}
	}
	args := []string{"-C", path}
	if !id.IsZero() {
		args = append(args, "-c", "user.name="+id.Name, "-c", "user.email="+id.Email)
	}
	args = append(args, "commit", "-m", msg)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// EnsureInitialCommit creates a `chore: niuniu init` commit on `path` when no
// HEAD exists yet. If the working tree has tracked or untracked content,
// `git add -A` everything and commit. If empty, write .keep-niuniu first.
// No-op if HEAD already exists.
//
// When `id` is non-zero, it overrides repo/global git config for the commit
// (per-user attribution). When `id` is zero, falls back to repo/global config
// and returns ErrIdentityMissing if neither is set (commit would fail with a
// cryptic error otherwise; surfaced cleanly).
func EnsureInitialCommit(ctx context.Context, path string, id Identity) error {
	if hasHEAD(ctx, path) {
		return nil
	}
	if err := validateIdentity(id); err != nil {
		return err
	}
	if id.IsZero() {
		repoName, repoEmail, err := repoIdentity(ctx, path)
		if err != nil {
			return err
		}
		if repoName == "" || repoEmail == "" {
			return ErrIdentityMissing
		}
	}
	dirty, err := hasUntrackedOrModified(ctx, path)
	if err != nil {
		return err
	}
	if !dirty {
		keep := filepath.Join(path, keepNiuniuFileName)
		if err := os.WriteFile(keep, []byte(keepNiuniuFileBody), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", keepNiuniuFileName, err)
		}
	}
	if out, err := runIn(ctx, path, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %s: %w", out, err)
	}
	args := []string{"-C", path}
	if !id.IsZero() {
		args = append(args, "-c", "user.name="+id.Name, "-c", "user.email="+id.Email)
	}
	args = append(args, "commit", "-m", initialCommitMessage)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func hasHEAD(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--verify", "HEAD")
	return cmd.Run() == nil
}

// repoIdentity reads user.name / user.email scoped to `path`, which respects
// repo-local overrides as well as global config.
func repoIdentity(ctx context.Context, path string) (string, string, error) {
	name, err := readScopedKey(ctx, path, "user.name")
	if err != nil {
		return "", "", err
	}
	email, err := readScopedKey(ctx, path, "user.email")
	if err != nil {
		return "", "", err
	}
	return name, email, nil
}

func readScopedKey(ctx context.Context, path, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func hasUntrackedOrModified(ctx context.Context, path string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// runIn runs `git -C path <args>` (the helper assumes name=="git", which all
// callers in this file use). Returns trimmed combined output and the err.
func runIn(ctx context.Context, path, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, append([]string{"-C", path}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
