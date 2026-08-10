//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// openPencilLaunchSpec locates the OpenPencil desktop app on Windows and returns
// the argv to launch it (optionally opening filePath), plus whether it was
// found. It probes PATH first, then the default per-user / per-machine install
// locations a Tauri (NSIS/MSI) installer uses.
func openPencilLaunchSpec(filePath string) ([]string, bool) {
	for _, name := range []string{"OpenPencil", "open-pencil", "openpencil"} {
		if p, err := exec.LookPath(name); err == nil {
			return openPencilArgv(p, filePath), true
		}
	}
	for _, p := range windowsOpenPencilCandidates() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return openPencilArgv(p, filePath), true
		}
	}
	return nil, false
}

// windowsOpenPencilCandidates lists the default OpenPencil.exe install paths.
func windowsOpenPencilCandidates() []string {
	var out []string
	add := func(base string) {
		if base == "" {
			return
		}
		out = append(out,
			filepath.Join(base, "OpenPencil", "OpenPencil.exe"),
			filepath.Join(base, "Programs", "OpenPencil", "OpenPencil.exe"),
		)
	}
	add(os.Getenv("LOCALAPPDATA"))
	add(os.Getenv("ProgramFiles"))
	add(os.Getenv("ProgramFiles(x86)"))
	return out
}

func openPencilArgv(exe, filePath string) []string {
	if filePath != "" {
		return []string{exe, filePath}
	}
	return []string{exe}
}
