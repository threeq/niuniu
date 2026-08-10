package api

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/event"
)

// ShellHandler exposes host-shell operations that help the frontend bridge
// operations that can't be expressed in the webview alone — most notably
// opening an external URL (Stripe checkout) in the user's default browser.
//
// These endpoints only make sense when niuniu-server is running locally on
// the same machine as the user (personal/bundled mode). In a hosted /
// multi-tenant deployment opening a browser on the server host is useless,
// so callers in that mode should fall back to window.open on the frontend.
type ShellHandler struct {
	// personalMode gates ops that only make sense when niuniu-server runs on
	// the user's own machine (claude-login / codex-login).
	personalMode bool
	// starter is the cmd-start hook injected for tests; defaults to
	// (*exec.Cmd).Start in NewShellHandler.
	starter func(*exec.Cmd) error
	// homeDir resolves the user home dir; injected for tests. Defaults to
	// os.UserHomeDir.
	homeDir func() (string, error)
	// buildCmd composes the claude-login *exec.Cmd; injected for tests.
	// Defaults to buildClaudeLoginCmd with nil lookPath/getEnv.
	buildCmd func(goos, home string) (*exec.Cmd, error)
	// buildCodexCmd composes the codex-login *exec.Cmd; injected for tests.
	// Defaults to buildCodexLoginCmd with nil lookPath/getEnv.
	buildCodexCmd func(goos, home string) (*exec.Cmd, error)

	// EventBus, when set, lets OpenAIWindow publish the desktop "open_ai_window"
	// signal. nil in tests → OpenAIWindow still returns 204 (no-op publish).
	EventBus *event.Bus
}

// NewShellHandler returns a handler. `personalMode` should be `!cfg.Auth.Enabled`.
func NewShellHandler(personalMode bool) *ShellHandler {
	return &ShellHandler{
		personalMode: personalMode,
		starter:      func(cmd *exec.Cmd) error { return cmd.Start() },
		homeDir:      os.UserHomeDir,
		buildCmd: func(goos, home string) (*exec.Cmd, error) {
			return buildClaudeLoginCmd(goos, home, nil, nil)
		},
		buildCodexCmd: func(goos, home string) (*exec.Cmd, error) {
			return buildCodexLoginCmd(goos, home, nil, nil)
		},
	}
}

type openExternalReq struct {
	URL string `json:"url" binding:"required"`
}

// OpenExternal — POST /api/shell/open-external
// Body: {"url": "https://..."}. Only http(s) schemes are accepted so a
// crafted body can't trigger arbitrary protocol handlers (file://, javascript:, etc.).
// NOTE: bypasses h.starter — this handler predates the test seam in
// ClaudeLogin and isn't worth refactoring without a concrete reason.
func (h *ShellHandler) OpenExternal(c *gin.Context) {
	var req openExternalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scheme"})
		return
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// `rundll32 url.dll,FileProtocolHandler <url>` is the
		// Windows idiom that avoids cmd's % expansion on the URL.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", req.URL)
	case "darwin":
		cmd = exec.Command("open", req.URL)
	default:
		cmd = exec.Command("xdg-open", req.URL)
	}
	if err := cmd.Start(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// We don't Wait — the launcher exits quickly once it's handed the URL
	// off to the OS. Wait() would block the request while the browser loads.
	go func() { _ = cmd.Wait() }()
	c.Status(http.StatusNoContent)
}

// OpenAIWindow — POST /api/shell/open-ai-window
//
// Personal/embedded mode only. Publishes an "open_ai_window" signal on the
// event bus; the desktop shell's local SSE listener receives it and raises the
// native AI-aggregation ("AI 直达") window. This is the SPA→desktop bridge for
// the always-visible top-nav button — the SPA can't call the Wails runtime
// directly (it's served from the local server origin, not the Wails asset
// origin), so it round-trips through the server the desktop already listens to.
// Hosted/team deployments have no desktop to receive it → 403 (the SPA also
// hides the button off personal mode).
func (h *ShellHandler) OpenAIWindow(c *gin.Context) {
	if !h.personalMode {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_supported_in_hosted_mode"})
		return
	}
	if h.EventBus != nil {
		// WorkspaceId 0 → the events stream broadcasts it to every client
		// unfiltered (see events.go), which is what we want: the local desktop
		// shell is the sole consumer that acts on it.
		h.EventBus.Publish(event.OutputEvent{Type: "open_ai_window"})
	}
	c.Status(http.StatusNoContent)
}

type openPathReq struct {
	Path string `json:"path" binding:"required"`
}

// OpenPath — POST /api/shell/open-path
// Body: {"path": "<absolute path>"}. Reveals the path in the host OS file
// manager (Explorer / Finder / xdg-open). Like OpenExternal this is only
// meaningful in personal/bundled mode where the server runs on the same
// machine as the user; in a hosted deployment the path doesn't exist on
// the server host. The path must be absolute and must already exist —
// callers always pass workspace.path which the server itself created, so
// any miss here is a logic error rather than an attack surface.
// NOTE: bypasses h.starter — this handler predates the test seam in
// ClaudeLogin and isn't worth refactoring without a concrete reason.
func (h *ShellHandler) OpenPath(c *gin.Context) {
	var req openPathReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	if !filepath.IsAbs(req.Path) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path_not_absolute"})
		return
	}
	info, err := os.Stat(req.Path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "path_not_found"})
		return
	}
	target := req.Path
	if !info.IsDir() {
		target = filepath.Dir(target)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// explorer.exe returns exit code 1 on success, so we ignore Wait's error.
	go func() { _ = cmd.Wait() }()
	c.Status(http.StatusNoContent)
}

// ClaudeLogin — POST /api/shell/claude-login
//
// Opens a new OS-native terminal window in the user's home directory and runs
// `claude`. No request body. Personal/embedded mode only — returns 403 in
// hosted/team-edition deployments because spawning a terminal on the server
// host is meaningless there. Frontend additionally hides the button via
// info.personal_mode, but the server gate is the source of truth.
//
// The handler invokes h.starter (defaults to (*exec.Cmd).Start, injected for
// tests) so the lifecycle is completely deterministic in unit tests without
// spawning real processes.
func (h *ShellHandler) ClaudeLogin(c *gin.Context) {
	h.runLogin(c, h.buildCmd)
}

// CodexLogin — POST /api/shell/codex-login
//
// Mirror of ClaudeLogin but spawns `codex login` in the new terminal. Same
// personal/embedded gate: returns 403 in hosted deployments because spawning
// a terminal on the server host is meaningless there.
func (h *ShellHandler) CodexLogin(c *gin.Context) {
	h.runLogin(c, h.buildCodexCmd)
}

// runLogin is the shared spawn-a-terminal entry used by ClaudeLogin /
// CodexLogin. Keeps the personal-mode gate, home-dir resolution, and
// starter/Wait lifecycle in one place so the two endpoints stay in lockstep.
func (h *ShellHandler) runLogin(c *gin.Context, build func(string, string) (*exec.Cmd, error)) {
	if !h.personalMode {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_supported_in_hosted_mode"})
		return
	}
	home, err := h.homeDir()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no_home_dir"})
		return
	}
	cmd, err := build(runtime.GOOS, home)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.starter(cmd); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Don't Wait synchronously — the launcher exits as soon as the terminal
	// window is up. We still drain the goroutine to release zombie state when
	// the spawned terminal eventually closes.
	go func() { _ = cmd.Wait() }()
	c.Status(http.StatusNoContent)
}

// lookPathFn matches exec.LookPath; injectable for tests.
type lookPathFn func(string) (string, error)

// cliLoginSpec describes the CLI invocation to open a fresh interactive login
// for one of the supported coding agents. Each per-OS branch in
// buildCLILoginCmd splices fields of this struct rather than carrying a switch
// over agent names — the only thing that varies between `claude` and
// `codex login` is the literal command tokens and the docs URL we surface
// when the binary is missing from PATH.
type cliLoginSpec struct {
	// winArgs is the argv slice to pass after `cmd /K` on Windows. For a
	// multi-word CLI invocation each token is a separate element.
	winArgs []string
	// unixCmd is the bash-friendly literal (e.g. "claude" or "codex login")
	// spliced into the macOS osascript and Linux bash payloads.
	unixCmd string
	// probeBin is the binary name probed with `command -v` in the Linux
	// payload to decide whether to run the CLI or print the "not found"
	// help line.
	probeBin string
	// docsURL is shown in the "not found in PATH" fallback for Linux. Plain
	// ASCII only (em-dashes don't render well in every terminal).
	docsURL string
}

var (
	claudeLoginSpec = cliLoginSpec{
		winArgs:  []string{"claude"},
		unixCmd:  "claude",
		probeBin: "claude",
		docsURL:  "https://docs.claude.com/en/docs/claude-code/setup",
	}
	codexLoginSpec = cliLoginSpec{
		// `codex login` is the user-facing form; codex CLI accepts the short
		// alias and routes it to `codex auth login` internally.
		winArgs:  []string{"codex", "login"},
		unixCmd:  "codex login",
		probeBin: "codex",
		docsURL:  "https://github.com/openai/codex",
	}
)

// buildClaudeLoginCmd is the back-compat wrapper used by existing shell_test.go
// cases. New callers should use buildCLILoginCmd directly with a spec.
func buildClaudeLoginCmd(goos, home string, lookPath lookPathFn, getEnv func(string) string) (*exec.Cmd, error) {
	return buildCLILoginCmd(goos, home, claudeLoginSpec, lookPath, getEnv)
}

// buildCodexLoginCmd is the codex twin of buildClaudeLoginCmd.
func buildCodexLoginCmd(goos, home string, lookPath lookPathFn, getEnv func(string) string) (*exec.Cmd, error) {
	return buildCLILoginCmd(goos, home, codexLoginSpec, lookPath, getEnv)
}

// buildCLILoginCmd constructs an *exec.Cmd that opens a new OS-native
// terminal window cd'd into `home` and runs the CLI named by `spec`. Per-OS
// strategy:
//
//	windows  prefer wt.exe (Windows Terminal); fall back to cmd /C start
//	darwin   osascript driving Terminal.app (login shell, PATH inherited from rc files)
//	linux    $TERMINAL env first; else probe a curated priority list
//
// `lookPath` defaults to exec.LookPath; `getEnv` defaults to os.Getenv.
// Both injectable for tests.
func buildCLILoginCmd(goos, home string, spec cliLoginSpec, lookPath lookPathFn, getEnv func(string) string) (*exec.Cmd, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if getEnv == nil {
		getEnv = os.Getenv
	}

	switch goos {
	case "windows":
		wtArgs := append([]string{"wt", "-d", home, "cmd", "/K"}, spec.winArgs...)
		if _, err := lookPath("wt"); err == nil {
			return exec.Command(wtArgs[0], wtArgs[1:]...), nil
		}
		// Empty-string title slot is required: `start` parses the first quoted
		// argument as the window title. Go's argv→Windows-commandline serializer
		// won't preserve quotes on a non-spaces token, so `start "Claude" /D ...`
		// would render as `start Claude /D ...` and `start` would interpret
		// "Claude" as the program to launch. The empty string forces `start` to
		// recognize the slot and treat /D as the next arg.
		startArgs := append([]string{"cmd", "/C", "start", "", "/D", home, "cmd", "/K"}, spec.winArgs...)
		return exec.Command(startArgs[0], startArgs[1:]...), nil

	case "darwin":
		// AppleScript string: escape `\` first, then `"`. Reversed order would
		// re-escape the just-added `\"` into `\\"` (broken).
		esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(home)
		script := `tell application "Terminal" to do script "cd \"` + esc + `\" && ` + spec.unixCmd + `"`
		return exec.Command("osascript", "-e", script), nil

	case "linux":
		payload := "cd " + shellQuote(home) +
			` && (command -v ` + spec.probeBin + ` >/dev/null && ` + spec.unixCmd +
			` || echo "` + spec.probeBin + ` not found in PATH inside this shell — see ` + spec.docsURL + `"); exec bash`

		// $TERMINAL env var wins if set and resolvable. Common form: just the
		// terminal binary name. Use the same -e style as gnome-terminal.
		if termEnv := strings.TrimSpace(getEnv("TERMINAL")); termEnv != "" {
			if _, err := lookPath(termEnv); err == nil {
				return exec.Command(termEnv, "-e", "bash", "-lc", payload), nil
			}
		}

		// Priority order: modern dev terminals first, x-terminal-emulator (a
		// Debian alternative whose underlying -e semantics shift across xfce4/
		// gnome wrappers) is the last resort.
		candidates := []struct {
			bin  string
			args []string
		}{
			{"kitty", []string{"-d", home, "bash", "-lc", payload}},
			{"alacritty", []string{"--working-directory", home, "-e", "bash", "-lc", payload}},
			{"wezterm", []string{"start", "--cwd", home, "--", "bash", "-lc", payload}},
			{"foot", []string{"--", "bash", "-lc", payload}},
			{"gnome-terminal", []string{"--working-directory=" + home, "--", "bash", "-lc", payload}},
			{"konsole", []string{"--workdir", home, "-e", "bash", "-lc", payload}},
			// tilix -e is argv-style on >=1.6 (2017+). Older tilix used system(3)
			// semantics — if anyone reports the launcher silently dropping args
			// on a stale install, fall back to --command="bash -lc <quoted>".
			{"tilix", []string{"--working-directory=" + home, "-e", "bash", "-lc", payload}},
			// xfce4-terminal -e takes a single string (system(3)-style), not exec
			// argv. We must quote the whole `bash -lc payload` invocation as one
			// shell command, otherwise -e silently drops everything after the
			// first space.
			{"xfce4-terminal", []string{"--working-directory=" + home, "-e", "bash -lc " + shellQuote(payload)}},
			{"xterm", []string{"-e", "bash", "-lc", payload}},
			{"x-terminal-emulator", []string{"-e", "bash", "-lc", payload}},
		}
		for _, c := range candidates {
			if _, err := lookPath(c.bin); err == nil {
				return exec.Command(c.bin, c.args...), nil
			}
		}
		return nil, errors.New("no_terminal_found")
	}
	return nil, errors.New("unsupported_os")
}

// shellQuote single-quotes a string for bash, escaping any embedded single
// quotes via the standard close-quote / backslash-quote / open-quote idiom.
// Used for cd targets we splice into a `bash -lc` payload.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
