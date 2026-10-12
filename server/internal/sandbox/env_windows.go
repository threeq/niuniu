//go:build windows

package sandbox

import "os"

// boxEnv is the box environment.
//
// Windows refuses to start an AppContainer process from a custom environment
// block unless the block carries the profile variables the loader resolves for
// the container — it fails with ERROR_ENVVAR_NOT_FOUND otherwise. The values
// below are therefore present, and they are all NEUTRAL PLACEHOLDERS: a
// directory the container cannot write anyway, and a fabricated user profile.
// Nothing is copied from the server's environment — no token, no API key, no
// per-user variable — which is what the capability layer promises the function
// cannot reach. The helper additionally clears its own environment before it
// loads any function code.
func boxEnv() []string {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	// A path the AppContainer has no write access to, used only to satisfy the
	// loader. It is never the host's real profile directory.
	neutral := systemRoot + `\Temp`
	return []string{
		"SystemRoot=" + systemRoot,
		"windir=" + systemRoot,
		"PATH=" + systemRoot + `\System32`,
		"USERPROFILE=" + neutral,
		"LOCALAPPDATA=" + neutral,
		"APPDATA=" + neutral,
		"TEMP=" + neutral,
		"TMP=" + neutral,
		"HOME=",
		"LANG=C",
		"LC_ALL=C",
	}
}
