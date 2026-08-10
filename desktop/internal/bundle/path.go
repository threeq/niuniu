package bundle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// resolveChildPath builds the PATH the embedded server child should run with.
//
// GUI apps launched from the macOS Finder/Dock (or a Linux .desktop entry)
// inherit a minimal PATH — typically /usr/bin:/bin:/usr/sbin:/sbin — that omits
// Homebrew (/usr/local/bin, /opt/homebrew/bin), nvm, and other user tool dirs.
// The embedded niuniu-server inherits that PATH, so its system-deps probe
// reports node/git/claude/codex as missing even when they ARE installed, and
// agent spawns later fail the same way. Re-running the check can't help because
// PATH is fixed at process launch.
//
// We recover the user's real PATH by asking their login shell (which sources
// the profile/rc files where nvm/Homebrew shims are set up), then union in a
// few well-known dirs as a backstop in case the shell probe fails. Only called
// on darwin/linux from spawnFromBinary (bundle_unix.go); Windows GUI apps
// inherit the full system PATH and need no fix-up.
func resolveChildPath(ctx context.Context) string {
	return mergePaths(
		loginShellPath(ctx),
		os.Getenv("PATH"),
		strings.Join(standardToolDirs(), string(os.PathListSeparator)),
	)
}

// loginShellPath runs the user's login shell and returns the PATH it exports.
// Best-effort: returns "" on any error, in which case the caller still has the
// inherited PATH plus the standard-dir backstop.
func loginShellPath(ctx context.Context) string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh" // macOS default since Catalina; sane on Linux too
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	// -i -l: interactive login shell, so ~/.zprofile / ~/.zshrc / ~/.bash_profile
	// run and set up nvm/Homebrew. Sentinels isolate the PATH value from any
	// banner text an rc file might print to stdout.
	const sentinel = "__NIUNIU_PATH__"
	script := "printf '%s%s%s' '" + sentinel + "' \"$PATH\" '" + sentinel + "'"
	out, err := exec.CommandContext(cctx, shell, "-ilc", script).Output()
	if err != nil {
		return ""
	}
	s := string(out)
	i := strings.Index(s, sentinel)
	if i < 0 {
		return ""
	}
	rest := s[i+len(sentinel):]
	j := strings.Index(rest, sentinel)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// standardToolDirs lists the common locations a GUI-inherited PATH usually
// drops. Order matters: Apple-silicon Homebrew first, then Intel Homebrew / the
// nodejs.org installer location, then user-local bins.
func standardToolDirs() []string {
	dirs := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/sbin",
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
		)
	}
	return dirs
}

// mergePaths concatenates PATH-style strings, splitting on the OS list
// separator, dropping empties, and de-duplicating while preserving first-seen
// order.
func mergePaths(parts ...string) string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range parts {
		for _, dir := range filepath.SplitList(p) {
			if dir == "" {
				continue
			}
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			out = append(out, dir)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// setEnv returns env with key set to value: it drops any existing entries for
// key (case-sensitive, as POSIX env var names are) and appends "key=value".
// This avoids leaving a duplicate key, whose getenv() resolution is
// platform-defined.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+value)
}
