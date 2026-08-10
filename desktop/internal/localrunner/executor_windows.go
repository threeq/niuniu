//go:build windows

package localrunner

import (
	"context"
	"os/exec"
	"syscall"
)

// createNoWindow (CREATE_NO_WINDOW) tells Windows not to allocate a console for
// the child process. Without it, every command the runner spawns from the Wails
// GUI process (which has no console of its own) flashes a cmd.exe window on
// screen — the flicker users see. Setting it keeps execution fully headless.
const createNoWindow = 0x08000000

// buildShellCmd runs command through cmd.exe on Windows.
//
// It sets SysProcAttr.CmdLine explicitly instead of letting os/exec build the
// command line from Args. Go's default path escapes each arg with syscall
// EscapeArg, which turns an embedded " into \" — but cmd.exe does NOT understand
// \" (it uses "" / ^"), so any command carrying quotes (e.g. writing source with
// `echo import "fmt" > x.go`) arrived at cmd.exe mangled with stray backslashes.
// That was the "write escaping" bug: agents had to base64-encode payloads to
// dodge it.
//
// `cmd /s /c "<command>"` is the documented reliable form: with /s, cmd.exe
// strips exactly the first and last quote after /c and runs everything between
// them VERBATIM — so inner quotes, backticks, etc. survive untouched. We build
// that line ourselves so no EscapeArg runs.
func buildShellCmd(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
		CmdLine:       `cmd /s /c "` + command + `"`,
	}
	return cmd
}
