//go:build windows

package terminal

import (
	"errors"
	"testing"
)

// resolveCommandLine: .exe path is used as-is (CreateProcess can run it directly).
func TestResolveCommandLine_Exe_NoWrap(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "node" {
			return `C:\Program Files\nodejs\node.exe`, nil
		}
		return "", errors.New("not found")
	}
	got := resolveCommandLine("node", []string{"--version"}, lookPath)
	want := `"C:\Program Files\nodejs\node.exe" --version`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// resolveCommandLine: .cmd batch file gets wrapped with cmd.exe /s /c so that
// CreateProcess (which can't execute .cmd directly) launches the cmd.exe
// interpreter instead. This is the regression case driving the fix.
func TestResolveCommandLine_CmdFile_WrappedWithComSpec(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "claude" {
			return `C:\Users\me\AppData\Roaming\npm\claude.cmd`, nil
		}
		return "", errors.New("not found")
	}
	t.Setenv("COMSPEC", `C:\Windows\System32\cmd.exe`)
	got := resolveCommandLine("claude", []string{"/login"}, lookPath)
	want := `C:\Windows\System32\cmd.exe /s /c "C:\Users\me\AppData\Roaming\npm\claude.cmd /login"`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Path containing spaces must remain quoted inside the cmd.exe /s /c wrapper;
// /s tells cmd.exe to strip ONLY the outermost pair of quotes, preserving the
// inner quoting of the path.
func TestResolveCommandLine_CmdFile_PathWithSpaces_Quoted(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "claude" {
			return `C:\Program Files\npm\claude.cmd`, nil
		}
		return "", errors.New("not found")
	}
	t.Setenv("COMSPEC", `C:\Windows\System32\cmd.exe`)
	got := resolveCommandLine("claude", []string{"/login"}, lookPath)
	want := `C:\Windows\System32\cmd.exe /s /c ""C:\Program Files\npm\claude.cmd" /login"`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// .bat is treated identically to .cmd.
func TestResolveCommandLine_BatFile_Wrapped(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "tool" {
			return `C:\tools\tool.bat`, nil
		}
		return "", errors.New("not found")
	}
	t.Setenv("COMSPEC", `cmd.exe`)
	got := resolveCommandLine("tool", nil, lookPath)
	want := `cmd.exe /s /c "C:\tools\tool.bat"`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// LookPath miss is non-fatal: we hand the original command back so the eventual
// conpty.Start error matches pre-fix behavior for a truly missing binary
// (instead of silently masking the failure with a wrapped cmd.exe call).
func TestResolveCommandLine_LookPathFails_FallsBackUnchanged(t *testing.T) {
	lookPath := func(name string) (string, error) {
		return "", errors.New("not found")
	}
	got := resolveCommandLine("missing", []string{"arg1"}, lookPath)
	want := "missing arg1"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Empty COMSPEC env falls back to bare "cmd.exe" (which then resolves via the
// system PATH at conpty.Start time — this is the unattended-service / minimal
// test environment case).
func TestResolveCommandLine_NoComspec_FallsBackToCmdExe(t *testing.T) {
	lookPath := func(name string) (string, error) {
		return `C:\tools\tool.cmd`, nil
	}
	t.Setenv("COMSPEC", "")
	got := resolveCommandLine("tool", nil, lookPath)
	want := `cmd.exe /s /c "C:\tools\tool.cmd"`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
