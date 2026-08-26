package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/terminal"
)

// CLILoginHandler serves the in-browser interactive login for the coding-agent
// CLIs (Claude Code / Codex).
//
// Why this exists separately from ShellHandler.ClaudeLogin: that endpoint spawns
// an OS-NATIVE terminal window on the machine running niuniu-server, so it only
// works when the server is the user's own desktop (personal/embedded mode) and
// is hard-gated to 403 otherwise. Team edition runs the server in a Docker
// container with no display and no terminal emulator, so team users had NO way
// at all to complete `claude` / `codex login` — the buttons were hidden and the
// endpoints refused (issue #677).
//
// Here we instead run the CLI inside a PTY on the server and bridge it to the
// browser over WebSocket, reusing the exact xterm.js + internal/terminal stack
// the workspace terminal already uses. The OAuth device/paste flow the CLIs use
// is fully keyboard-driven, so a browser-hosted PTY is sufficient to finish it.
//
// Admin-only by deliberate design: the container has ONE shared $HOME, so
// ~/.claude and ~/.codex credentials are server-wide, not per-user. Letting any
// member re-run login would let them silently swap the credentials every other
// member's agents run under. Route registration applies RequireAdmin.
type CLILoginHandler struct {
	// newPTY is the PTY constructor, injected for tests. Defaults to
	// terminal.NewPTYProcess.
	newPTY func(command string, args []string, workDir string, env []string) (*terminal.PTYProcess, error)
	// lookPath resolves the CLI binary; injected for tests. Defaults to exec.LookPath.
	lookPath func(string) (string, error)
	// homeDir resolves the working dir for the login process; injected for
	// tests. Defaults to os.UserHomeDir.
	homeDir func() (string, error)
}

func NewCLILoginHandler() *CLILoginHandler {
	return &CLILoginHandler{
		newPTY:   terminal.NewPTYProcess,
		lookPath: exec.LookPath,
		homeDir:  os.UserHomeDir,
	}
}

// cliLoginTarget describes how to launch the interactive login for one agent CLI.
type cliLoginTarget struct {
	// bin is the executable probed on PATH and launched in the PTY.
	bin string
	// args are passed to bin. Claude Code has no dedicated login subcommand —
	// running it bare drops the user into the TUI whose /login flow handles auth
	// — whereas codex exposes `codex login`.
	args []string
}

var cliLoginTargets = map[string]cliLoginTarget{
	"claude": {bin: "claude", args: nil},
	"codex":  {bin: "codex", args: []string{"login"}},
}

// Terminal — GET /ws/cli-login/:tool/terminal
//
// Upgrades to a WebSocket and bridges it to a PTY running the login flow for
// :tool ("claude" or "codex"). Unlike the /shell/*-login endpoints this works in
// every edition, because nothing is spawned on the user's desktop — the CLI runs
// server-side and the browser is the terminal.
//
// The PTY is closed when the socket drops (onClose returns true): a half-finished
// login left running would sit on a stale OAuth prompt forever, and the user can
// simply reopen the dialog to start a fresh attempt.
func (h *CLILoginHandler) Terminal(c *gin.Context) {
	tool := c.Param("tool")
	target, ok := cliLoginTargets[tool]
	if !ok {
		BadRequest(c, "unsupported tool")
		return
	}

	// Probe before the upgrade so a missing CLI surfaces as a plain HTTP error
	// the SPA can show, rather than a WebSocket that opens and instantly dies.
	if _, err := h.lookPath(target.bin); err != nil {
		RespondError(c, http.StatusNotFound, "CLI_NOT_FOUND",
			fmt.Sprintf("%s not found in PATH on the server", target.bin))
		return
	}

	// Run in $HOME: both CLIs write their credentials relative to the home dir
	// ($HOME/.claude, $HOME/.codex), and starting there keeps the session out of
	// any workspace so a stray keystroke can't touch repo files.
	home, err := h.homeDir()
	if err != nil {
		InternalError(c, fmt.Errorf("resolve home dir: %w", err))
		return
	}

	proc, err := h.newPTY(target.bin, target.args, home, nil)
	if err != nil {
		InternalError(c, fmt.Errorf("create PTY process: %w", err))
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		proc.Close()
		InternalError(c, err)
		return
	}

	if err := terminal.Bridge(conn, proc, func() bool {
		return true // login session is single-connection; tear the PTY down with it
	}); err != nil {
		slog.Warn("cli login terminal bridge error", "tool", tool, "error", err)
	}

	// Reap the child. Bridge's onClose already killed it via proc.Close(), but
	// Close() only sends the kill — without a Wait the process stays a zombie for
	// the lifetime of the server. That is tolerable for the workspace terminal
	// (one per workspace) but not here: an admin retrying a flaky OAuth flow can
	// open this dialog many times in a row.
	_ = proc.Wait()
}

// nativeTerminalOnce caches hostSupportsNativeTerminal's answer. The Linux probe
// walks up to ten terminal-emulator candidates through exec.LookPath, and the
// result cannot change while the process runs (a terminal emulator is not going
// to be installed into the container mid-flight), so paying for it on every
// GET /api/system-deps — which the Settings page also refetches on every
// 重新检测 — would be pure waste.
var nativeTerminalOnce = sync.OnceValue(func() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	_, err := buildCLILoginCmd(runtime.GOOS, os.TempDir(), claudeLoginSpec, nil, nil)
	return err == nil
})

// hostSupportsNativeTerminal reports whether buildCLILoginCmd could plausibly
// open a native terminal window on this host. Used to decide, on Linux servers
// (i.e. the team-edition container), that the SPA should offer the in-browser
// terminal instead of the native-window launch. Windows and macOS always have a
// usable terminal, so they report true.
//
// Note buildCLILoginCmd only *constructs* an exec.Cmd — it never starts one — so
// this is a pure probe with no side effects.
func hostSupportsNativeTerminal() bool {
	return nativeTerminalOnce()
}
