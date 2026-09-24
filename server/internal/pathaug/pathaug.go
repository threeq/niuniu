// Package pathaug augments the server process PATH on macOS and Linux
// desktop launches.
//
// A macOS app started from Finder/Dock inherits launchd's minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin). Version managers and user package
// installs live OUTSIDE that set: nvm keeps node/npm in
// ~/.nvm/versions/node/<ver>/bin (wired into PATH only by interactive-shell
// rc files), Homebrew on Apple Silicon uses /opt/homebrew/bin, and npm -g
// agent CLIs land in the same nvm bin dir.
//
// Linux has the same trap in a weaker form: apt-managed tools (/usr/bin)
// are always visible, but user-level installs are wired up in rc files —
// users who put them in ~/.bashrc/~/.zshrc instead of ~/.profile get GUI
// sessions without them.
//
// In both cases every exec.LookPath in the server — the system-deps probe,
// agent spawn, git, ripgrep — fails for tools the user CAN run in their
// terminal, and the Settings page reports node/claude as not installed.
//
// Augment runs once at server startup, darwin-only:
//  1. best-effort capture of the user's real PATH from their login shell
//     ($SHELL, then /bin/zsh, /bin/bash — short timeout per shell) —
//     authoritative, and covers any version manager (nvm/fnm/asdf/mise/volta);
//  2. fallback: append the well-known tool directories that exist on disk.
package pathaug

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// staticDirsFor returns the well-known off-default-PATH tool directories for
// a platform, in preference order. Home-relative ones (~/.local/bin, volta,
// nvm) are platform-neutral and added by buildAdditions.
func staticDirsFor(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/opt/homebrew/bin", // Apple Silicon Homebrew
			"/opt/homebrew/sbin",
			"/usr/local/bin", // Intel Homebrew + user symlinks
			"/usr/local/sbin",
		}
	case "linux":
		return []string{
			"/home/linuxbrew/.linuxbrew/bin",     // Homebrew on Linux
			"/home/linuxbrew/.linuxbrew/sbin",
			"/usr/local/bin", // manual installs (node, claude via npm prefix)
			"/usr/local/sbin",
		}
	default:
		// Windows: the Registry PATH is visible to GUI processes, and shell
		// profiles don't carry tool dirs — nothing to contribute.
		return nil
	}
}

// shellCaptureTimeout bounds the interactive-shell PATH capture: rc files
// can be slow (or hang on a prompt); a slow capture must never delay startup
// materially, and the static-dir fallback covers the failure.
const shellCaptureTimeout = 3 * time.Second

// Augment repairs the process PATH on darwin/linux desktop launches and
// returns the effective PATH ("" = unchanged / not applicable). Called once
// from server startup, before any LookPath happens, so every consumer
// (system-deps probe, agent spawn, install jobs) inherits the repaired PATH.
//
// Linux rationale: apt-managed tools sit in /usr/bin (always visible), but
// user-level installs — nvm, ~/.local/bin, Homebrew on Linux — are wired up
// in shell rc files, and users who put them in ~/.bashrc/~/.zshrc (instead
// of ~/.profile) hit the same invisible-to-GUI wall as macOS.
func Augment() string {
	goos := runtime.GOOS
	if goos != "darwin" && goos != "linux" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	base := os.Getenv("PATH")

	if captured := captureShellPath(); captured != "" {
		// The user's shell PATH is authoritative. Union it with the static
		// dirs so headless edge cases (no shell rc) still gain Homebrew etc.,
		// and with the launchd base (already a subset in practice).
		merged := mergeMissing(captured, buildAdditions(goos, home, dirExists, latestNvmBin(globNvmBins(home))))
		merged = mergeMissing(merged, splitPath(base))
		if merged != base {
			os.Setenv("PATH", merged)
			slog.Info("pathaug: replaced process PATH from login shell",
				"path", merged)
			return merged
		}
		return ""
	}

	additions := buildAdditions(goos, home, dirExists, latestNvmBin(globNvmBins(home)))
	merged := mergeMissing(base, additions)
	if merged == base {
		return ""
	}
	os.Setenv("PATH", merged)
	slog.Info("pathaug: appended well-known tool dirs to process PATH",
		"path", merged, "added", strings.Join(additions, ":"))
	return merged
}

// buildAdditions returns the candidate directories that actually exist on
// disk: platform static dirs, the user's nvm node bin (latest version), and
// the common user-local dirs. goos, exists and nvmBin are injectable for
// tests.
func buildAdditions(goos, home string, exists func(string) bool, nvmBin string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] || !exists(dir) {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, d := range staticDirsFor(goos) {
		add(d)
	}
	add(nvmBin)
	if goos == "darwin" || goos == "linux" {
		// POSIX separators deliberately: these are POSIX-platform dirs, and
		// explicit "/" keeps them identical across test platforms.
		add(home + "/.local/bin")
		add(home + "/.volta/bin")
	}
	return out
}

// mergeMissing appends dirs from add that are not already present in the
// colon-separated base, preserving both orders and skipping duplicates.
func mergeMissing(base string, add []string) string {
	seen := map[string]bool{}
	parts := splitPath(base)
	for _, p := range parts {
		seen[p] = true
	}
	out := parts
	for _, d := range add {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return strings.Join(out, ":")
}

func splitPath(p string) []string {
	if strings.TrimSpace(p) == "" {
		return nil
	}
	return strings.Split(p, ":")
}

// globNvmBins returns the bin dirs of every installed nvm node version
// (empty on any filesystem error — nvm simply not installed).
func globNvmBins(home string) []string {
	if home == "" {
		return nil
	}
	dirs, _ := filepath.Glob(home + "/.nvm/versions/node/*/bin")
	return dirs
}

// latestNvmBin picks the bin dir of the highest node version from glob
// results. Versions compare numerically per component (v9 < v20 — a lexical
// compare would get that wrong); non-version dirs are ignored. "" when none.
func latestNvmBin(dirs []string) string {
	best := ""
	var bestParts []int
	for _, d := range dirs {
		base := filepath.Base(filepath.Dir(d)) // .../node/v20.11.1/bin → v20.11.1
		parts, ok := versionParts(base)
		if !ok {
			continue
		}
		if best == "" || versionLess(bestParts, parts) {
			best = d
			bestParts = parts
		}
	}
	return best
}

// versionParts parses "v20.11.1" into [20, 11, 1]. ok=false for anything
// that is not a leading-v dotted number (prerelease suffixes like v20.0.0-next.0
// compare on their numeric prefix — fine for picking a latest install dir).
func versionParts(v string) ([]int, bool) {
	if !strings.HasPrefix(v, "v") {
		return nil, false
	}
	fields := strings.Split(v[1:], ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		digits := f
		for i := 0; i < len(digits); i++ {
			if digits[i] < '0' || digits[i] > '9' {
				digits = digits[:i]
				break
			}
		}
		if digits == "" {
			return nil, false
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, len(parts) > 0
}

// versionLess reports a < b comparing components numerically; a shorter
// prefix loses (20.1 < 20.1.1).
func versionLess(a, b []int) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

// shellCmd is one capture attempt: a shell binary plus the args that make it
// print the user's PATH.
type shellCmd struct {
	bin  string
	args []string
}

// shellCandidates lists the shells to try, in preference order: $SHELL (set
// when launched from a terminal), then the user's Directory Services default
// shell — the one macOS actually starts for them; GUI processes have no
// $SHELL, so dscl is what makes this authoritative — then the static macOS
// defaults. Shells whose binary does not exist are skipped, duplicates
// collapse.
func shellCandidates(getenv func(string) string, defaultShell func() string, exists func(string) bool) []shellCmd {
	var out []shellCmd
	seen := map[string]bool{}
	add := func(bin string) {
		if bin == "" || seen[bin] || !exists(bin) {
			return
		}
		seen[bin] = true
		out = append(out, shellCmd{bin: bin, args: argsForShell(bin)})
	}
	add(getenv("SHELL"))
	add(defaultShell())
	add("/bin/zsh")
	add("/bin/bash")
	return out
}

// argsForShell picks the args that make `shell` print a colon-joined PATH.
//
// POSIX-family shells (zsh/bash/sh/ksh/tcsh and relatives) share one form:
// interactive + login so the rc files that wire up version managers actually
// run (`echo $PATH` is colon-joined in all of them). fish is special-cased:
// its $PATH is a LIST, `echo` would print it space-separated and the parse
// would reject it, so it gets `string join :`.
//
// Contract for anything else (elvish/nushell/xonsh...): they get the POSIX
// form; if their output does not parse as a colon path list, capture falls
// through to the next candidate shell and finally to the static dirs —
// an exotic shell can never poison the PATH, only fail to contribute.
func argsForShell(shell string) []string {
	base := strings.ToLower(filepath.Base(shell))
	switch {
	case strings.Contains(base, "fish"):
		return []string{"-l", "-c", "string join : $PATH"}
	default:
		return []string{"-li", "-c", "echo $PATH"}
	}
}

// captureShellPath asks the user's shells for the PATH their terminal sees —
// including whatever version-manager rc files wire up. Tries each candidate
// in order; the first shell that yields a parseable PATH wins. Best-effort:
// anything that goes wrong (no candidates, slow/hanging rc, noise-only
// output) returns "" and the caller falls back to static dirs.
func captureShellPath() string {
	getenv := func(k string) string { return os.Getenv(k) }
	for _, c := range shellCandidates(getenv, queryDefaultShell, fileExists) {
		cctx, cancel := context.WithTimeout(context.Background(), shellCaptureTimeout)
		out, err := exec.CommandContext(cctx, c.bin, c.args...).CombinedOutput()
		cancel()
		if err != nil {
			continue
		}
		if p, ok := parseShellPathOutput(string(out)); ok {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// queryDefaultShell asks macOS Directory Services for the user's configured
// login shell (`dscl . -read /Users/<user> UserShell`). This is the shell
// macOS actually starts for the user — GUI processes have no $SHELL, so this
// query is what makes the capture honor e.g. a bash or fish user instead of
// blindly probing zsh. Best-effort: empty string on any failure. Non-darwin
// platforms never reach it (Augment gates on GOOS).
func queryDefaultShell() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	if username == "" {
		username = os.Getenv("USER")
	}
	if username == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(context.Background(), shellCaptureTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, "/usr/bin/dscl", ".", "-read", "/Users/"+username, "UserShell").Output()
	if err != nil {
		return ""
	}
	return parseUserShellOutput(string(out))
}

// parseUserShellOutput extracts the shell path from dscl's output line
// ("UserShell: /bin/zsh"). "" when the line is absent (unknown user).
func parseUserShellOutput(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "UserShell:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "UserShell:"))
		}
	}
	return ""
}

// parseShellPathOutput extracts the PATH line from shell output. Interactive
// rc files may print banners and prompts before the echo lands, so scan from
// the last line backwards and accept the first that looks like a
// colon-separated absolute-path list.
func parseShellPathOutput(out string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "/") || !strings.Contains(line, ":") || !strings.Contains(line, "/bin") {
			continue
		}
		return line, true
	}
	return "", false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
