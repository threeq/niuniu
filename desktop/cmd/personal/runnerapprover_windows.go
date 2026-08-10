//go:build windows

package main

import "github.com/niuniu-dev/niuniu-desktop/internal/localrunner"

// runnerapprover_windows.go is the native security-gateway prompt (#526·子D /
// #473). An unlisted command pops a native Win32 MessageBox showing the FULL
// command and working directory; the user chooses 始终允许 / 仅本次 / 拒绝. It
// reuses the synchronous messageBoxW binding from webview2_check_windows.go —
// blocking the runner's request goroutine until the user answers is exactly the
// intended flow (the exec waits for consent).

const (
	mbYesNoCancel uintptr = 0x00000003
	mbIconWarning uintptr = 0x00000030
	idYes                 = 6
	idNo                  = 7
)

type nativeApprover struct{ lang string }

func newNativeApprover(lang string) localrunner.Approver { return nativeApprover{lang: lang} }

func (a nativeApprover) Approve(req localrunner.ApprovalRequest) localrunner.ApprovalResult {
	var title, body string
	if a.lang == "zh" {
		title = "本地执行器：命令需要授权"
		body = "远端 Agent 请求在本地执行以下命令：\n\n" +
			req.Command + "\n\n工作目录：\n" + req.WorkingDir + "\n\n" +
			"「是」= 始终允许该命令（记住）\n「否」= 仅本次允许\n「取消」= 拒绝"
	} else {
		title = "Local runner: command needs authorization"
		body = "A remote agent wants to run this command locally:\n\n" +
			req.Command + "\n\nWorking directory:\n" + req.WorkingDir + "\n\n" +
			"[Yes] = Always allow this command (remember)\n[No] = Allow once\n[Cancel] = Deny"
	}

	// Cancel / closing the box both fall through to deny — fail-safe.
	switch messageBoxW(title, body, mbYesNoCancel|mbIconWarning|mbSetForeground) {
	case idYes:
		return localrunner.ApprovalResult{Allow: true, Always: true}
	case idNo:
		return localrunner.ApprovalResult{Allow: true, Always: false}
	default:
		return localrunner.ApprovalResult{Allow: false}
	}
}
