package eval

import "runtime"

// sandboxShell mirrors tools.Bash's shell selection.
func sandboxShellName() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sh"
}

func sandboxShellArg(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"/c", command}
	}
	return []string{"-c", command}
}
