//go:build !windows && !darwin

package main

import "os/exec"

// openPencilLaunchSpec locates the OpenPencil binary on Linux/other Unix (deb /
// AppImage installs put it on PATH) and returns the argv to launch it,
// optionally opening filePath.
func openPencilLaunchSpec(filePath string) ([]string, bool) {
	for _, name := range []string{"open-pencil", "openpencil", "OpenPencil"} {
		if p, err := exec.LookPath(name); err == nil {
			if filePath != "" {
				return []string{p, filePath}, true
			}
			return []string{p}, true
		}
	}
	return nil, false
}
