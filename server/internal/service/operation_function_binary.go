package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// The sandbox helper is a SEPARATE binary (cmd/niuniu-funcbox) so the box can
// be spawned under an OS fence that applies to a fresh process. Resolution
// mirrors FindMCPBinary: same directory as the server executable (which is what
// the desktop bundle produces), then bin/ next to the working tree, then PATH.
//
// A missing helper is not fatal at startup — the service reports the fence as
// unavailable and refuses to run functions, which is the same fail-closed path
// a platform without a sandbox takes.

var (
	funcboxOnce   sync.Once
	funcboxCached string
)

func findFuncboxBinary() string {
	funcboxOnce.Do(func() {
		funcboxCached = findFuncboxUncached()
	})
	return funcboxCached
}

func findFuncboxUncached() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	hostMatch := runtime.GOOS + "-" + runtime.GOARCH

	// 1. Next to the running executable (dev builds and the packaged desktop
	//    sidecar both land here).
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if candidate := filepath.Join(dir, "niuniu-funcbox"+suffix); funcboxFileExists(candidate) {
			return candidate
		}
		if found := findLatestBinaryInDir(dir, "niuniu-funcbox", suffix, hostMatch); found != "" {
			return found
		}
	}
	// 2. bin/ relative to the working directory (repo checkout).
	if cwd, err := os.Getwd(); err == nil {
		if found := findLatestBinaryInDir(filepath.Join(cwd, "bin"), "niuniu-funcbox", suffix, hostMatch); found != "" {
			return found
		}
	}
	// 3. PATH.
	if p, err := exec.LookPath("niuniu-funcbox"); err == nil {
		return p
	}
	return ""
}

func funcboxFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
