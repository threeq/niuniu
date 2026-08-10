package git

import (
	"context"
	"fmt"
	"os/exec"
)

// MediaLFSPatterns is the default set of large-binary glob patterns niuniu
// tracks with Git LFS on auto-init. Generic (non-dev) task directories commonly
// hold video/audio/image assets; tracking them via LFS keeps the repo from
// ballooning. Kept in sync with the issue #233 spec.
var MediaLFSPatterns = []string{
	"*.mp4", "*.mov", "*.wav", "*.mp3",
	"*.png", "*.jpg", "*.jpeg", "*.psd", "*.zip",
}

// LFSAvailable reports whether the git-lfs extension is reachable on this host.
// Callers treat a false result as "skip LFS, warn the user" rather than an error
// (mirrors the WebView2/WebKitGTK missing-dependency soft-fail style).
func LFSAvailable(ctx context.Context) bool {
	return exec.CommandContext(ctx, "git", "lfs", "version").Run() == nil
}

// EnableLFS installs git-lfs hooks scoped to the repo at path (--local, so the
// host's other repos are untouched) and tracks patterns, generating
// .gitattributes. The caller must have already `git init`'d path. Intended to
// run exactly once on a freshly initialised repo, before the first commit, so
// the generated .gitattributes is captured in it.
func EnableLFS(ctx context.Context, path string, patterns []string) error {
	if out, err := runIn(ctx, path, "git", "lfs", "install", "--local"); err != nil {
		return fmt.Errorf("git lfs install --local: %s: %w", out, err)
	}
	if len(patterns) > 0 {
		args := append([]string{"lfs", "track"}, patterns...)
		if out, err := runIn(ctx, path, "git", args...); err != nil {
			return fmt.Errorf("git lfs track: %s: %w", out, err)
		}
	}
	return nil
}
