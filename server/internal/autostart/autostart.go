// Package autostart manages an OS login item so that niuniu-desktop can be
// launched automatically when the user signs in.
//
// The personal Wails app passes its own executable path to the embedded server
// (env NIUNIU_PERSONAL_EXE); the server registers a login item that runs
// `"<exe>" --autostart`. An autostart launch shows the main window by default,
// exactly like a manual launch. The user can additionally opt into starting
// minimized to the tray; when enabled the login item becomes
// `"<exe>" --autostart --minimized` and the app stays in the tray on boot.
//
// Per-platform mechanism:
//
//	windows  HKCU\Software\Microsoft\Windows\CurrentVersion\Run value "Niuniu"
//	darwin   ~/Library/LaunchAgents/com.niuniu.personal.plist (RunAtLoad)
//	linux    ~/.config/autostart/niuniu-desktop.desktop (XDG autostart)
//
// State lives entirely in the OS login item (including the minimized choice);
// there is no DB persistence. The pure render helpers (windowsRunValue /
// darwinPlist / linuxDesktopEntry) carry no OS calls so they can be unit-tested
// on any platform; the Status/Enable/Disable methods live in build-tagged files.
package autostart

import (
	"os"
	"strings"
)

const (
	// windowsRunValueName is the HKCU ...\Run value name.
	windowsRunValueName = "Niuniu"
	// darwinLabel is the LaunchAgent label and plist base name.
	darwinLabel = "com.niuniu.personal"
	// linuxDesktopName is the XDG autostart .desktop file name.
	linuxDesktopName = "niuniu-desktop.desktop"
	// autostartFlag marks a login-triggered launch.
	autostartFlag = "--autostart"
	// minimizedFlag, when present alongside autostartFlag, tells the app to
	// start minimized to the tray instead of showing the main window.
	minimizedFlag = "--minimized"
)

// Manager registers/unregisters a single executable as a login item. Construct
// with New; the zero value is not usable.
type Manager struct {
	exePath string
	// home resolves the user home dir; injected for tests. Defaults to
	// os.UserHomeDir.
	home func() (string, error)
}

// New returns a Manager for the given absolute executable path.
func New(exePath string) *Manager {
	return &Manager{exePath: exePath, home: os.UserHomeDir}
}

// quotedCommand returns the login-item command line: the exe wrapped in literal
// double quotes (so paths with spaces survive), the autostart flag, and the
// minimized flag when requested. We do NOT use fmt %q here — that would
// Go-escape backslashes in Windows paths.
func quotedCommand(exe string, minimized bool) string {
	cmd := `"` + exe + `" ` + autostartFlag
	if minimized {
		cmd += " " + minimizedFlag
	}
	return cmd
}

// windowsRunValue is the data written to the Run registry value.
func windowsRunValue(exe string, minimized bool) string { return quotedCommand(exe, minimized) }

// darwinPlist renders the LaunchAgent plist for exe.
func darwinPlist(exe string, minimized bool) string {
	args := "\t\t<string>" + xmlEscape(exe) + "</string>\n" +
		"\t\t<string>" + autostartFlag + "</string>\n"
	if minimized {
		args += "\t\t<string>" + minimizedFlag + "</string>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + darwinLabel + `</string>
	<key>ProgramArguments</key>
	<array>
` + args + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
}

// linuxDesktopEntry renders the XDG autostart .desktop entry for exe. The exe
// is double-quoted per the Desktop Entry spec so paths with spaces parse.
func linuxDesktopEntry(exe string, minimized bool) string {
	exec := `"` + exe + `" ` + autostartFlag
	if minimized {
		exec += " " + minimizedFlag
	}
	return `[Desktop Entry]
Type=Application
Name=Niuniu Desktop
Exec=` + exec + `
Terminal=false
X-GNOME-Autostart-enabled=true
`
}

// classify matches a stored login-item value against the two canonical
// renderings (minimized / not) for our exe and reports whether the item is
// ours (enabled) and whether the minimized flag is set. eq is the platform's
// equality test (case-insensitive on Windows, exact elsewhere).
func classify(stored string, render func(minimized bool) string, eq func(a, b string) bool) (enabled, minimized bool) {
	switch {
	case eq(stored, render(true)):
		return true, true
	case eq(stored, render(false)):
		return true, false
	default:
		return false, false
	}
}

// exactEq is the default (case-sensitive) equality used by the file-based
// platforms.
func exactEq(a, b string) bool { return a == b }

// xmlEscape escapes the XML-significant characters for a plist <string> body.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
