//go:build !windows

package main

import (
	"log/slog"

	"github.com/niuniu-dev/niuniu-desktop/internal/localrunner"
)

// runnerapprover_other.go: on non-Windows desktops we do not yet have a verified
// native modal wired to the gateway, so we FAIL SAFE — an unlisted command is
// denied rather than silently run. This upholds the security bottom line
// ("default deny") on every platform; the personal desktop ships Windows-first,
// and the whitelist + always-allow path still covers the common commands there.
type nativeApprover struct{ lang string }

func newNativeApprover(lang string) localrunner.Approver { return nativeApprover{lang: lang} }

func (a nativeApprover) Approve(req localrunner.ApprovalRequest) localrunner.ApprovalResult {
	slog.Warn("local runner: no native approval dialog on this platform — denying unlisted command",
		"command", req.Command, "dir", req.WorkingDir)
	return localrunner.ApprovalResult{Allow: false}
}
