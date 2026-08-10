//go:build !windows

package localrunner

import (
	"context"
	"os/exec"
)

// buildShellCmd runs command through sh on non-Windows platforms. os/exec passes
// the command as a single argv entry with no intermediate shell quoting, so
// embedded quotes/backticks reach sh verbatim — there is no EscapeArg mangling
// to work around (that is a Windows-cmd.exe-only problem). No console window
// exists to hide either.
func buildShellCmd(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}
