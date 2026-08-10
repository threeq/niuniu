//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

// openPencilLaunchSpec locates the OpenPencil app bundle on macOS and returns an
// `open -a` argv (optionally opening filePath). `open` always exists on macOS,
// so presence is decided by whether an OpenPencil.app bundle is installed rather
// than by exec lookup.
func openPencilLaunchSpec(filePath string) ([]string, bool) {
	if !macOpenPencilInstalled() {
		return nil, false
	}
	argv := []string{"open", "-a", "OpenPencil"}
	if filePath != "" {
		argv = append(argv, filePath)
	}
	return argv, true
}

func macOpenPencilInstalled() bool {
	candidates := []string{"/Applications/OpenPencil.app"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Applications", "OpenPencil.app"))
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}
