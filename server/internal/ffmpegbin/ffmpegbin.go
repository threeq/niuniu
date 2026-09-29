// Package ffmpegbin resolves the ffmpeg/ffprobe binaries that the
// video-creation capability shells out to.
//
// ffmpeg is deliberately NOT part of the shipped executables: a static
// ffmpeg+ffprobe pair is ~320 MB, far too large to embed (design v3.2 §7.3
// "系统依赖按需下载，不内嵌" — the earlier go:embed payload was retired). It is
// an optional system dependency instead, surfaced in Settings → 系统依赖 like
// tesseract/cairosvg: probed on this page, downloaded on demand into
// <dataDir>/bin/ffmpeg/<goos>-<goarch>/ by internal/service/ffmpeg_install.go,
// and gracefully degraded when absent.
//
// Resolution order, applied independently to ffmpeg and ffprobe:
//
//  1. $NIUNIU_FFMPEG / $NIUNIU_FFPROBE explicit override. An override that
//     points at a missing or non-executable file is a hard error, never a
//     silent downgrade — the user asked for that exact binary.
//  2. An installed copy under <dataDir>/bin/ffmpeg/<subdir>/<tool> — the
//     in-app download lands in one such subdir; when several hold the tool the
//     newest modification time wins.
//  3. exec.LookPath — whatever the PATH provides (dev machines).
//
// When no source yields a usable binary the returned error wraps
// ErrNotAvailable, so callers can degrade (emit assets without composing) via
// errors.Is.
package ffmpegbin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ErrNotAvailable reports that no usable ffmpeg/ffprobe could be resolved from
// any source. Callers test it with errors.Is and degrade instead of failing.
var ErrNotAvailable = errors.New("no usable ffmpeg binary available")

const (
	// envFFmpeg and envFFprobe are the explicit-override environment
	// variables. When set to a non-empty value they win over everything else.
	envFFmpeg  = "NIUNIU_FFMPEG"
	envFFprobe = "NIUNIU_FFPROBE"

	// binDirName is the tool directory under the data dir:
	// <dataDir>/bin/ffmpeg/<subdir>. The subdir is named <goos>-<goarch> by
	// the in-app downloader, but resolution scans every subdir (see
	// InstalledPath): the directory level exists so a new install can land
	// beside a running one instead of overwriting a binary in use, and the
	// scan stays name-agnostic so directories written by an older layout keep
	// resolving.
	binDirName = "ffmpeg"
)

// Resolve returns the path of a usable ffmpeg executable, per the package
// resolution order. dataDir is the application data directory (usually
// ~/.niuniu) whose bin/ffmpeg/*/ subdirectories hold in-app downloads; an
// empty dataDir disables that step entirely — used by callers that have no
// data dir, never by the desktop app.
func Resolve(dataDir string) (string, error) {
	return resolve(envFFmpeg, "ffmpeg", dataDir)
}

// ResolveFFprobe is Resolve for ffprobe: it consults $NIUNIU_FFPROBE, the
// installed copies under <dataDir>/bin/ffmpeg/*/, then PATH. ffprobe is
// resolved independently of ffmpeg so a partial installation (or a PATH that
// only provides one of the two) still works.
func ResolveFFprobe(dataDir string) (string, error) {
	return resolve(envFFprobe, "ffprobe", dataDir)
}

// Available reports whether Resolve finds a usable ffmpeg. Callers use it to
// decide between full composition and the assets-only degradation path.
func Available(dataDir string) bool {
	_, err := Resolve(dataDir)
	return err == nil
}

// BinName is the executable file name of tool on goos: "ffmpeg.exe" on
// Windows, "ffmpeg" elsewhere. Exported so the installer and the system-deps
// probe write and look for exactly the names resolution expects.
func BinName(goos, tool string) string {
	if goos == "windows" {
		return tool + ".exe"
	}
	return tool
}

// InstallRoot is the directory an in-app download for goos/goarch installs
// into: <dataDir>/bin/ffmpeg/<goos>-<goarch>. Resolution does not depend on
// this name (InstalledPath scans every subdir) but the downloader, the
// system-deps probe and any manual-install guidance share it, so it lives here
// as the single source of truth.
func InstallRoot(dataDir, goos, goarch string) string {
	return filepath.Join(dataDir, "bin", binDirName, goos+"-"+goarch)
}

// InstalledPath returns the newest usable <tool> under
// <dataDir>/bin/ffmpeg/<subdir>/ for the named platform, or "" when there is
// none (including when dataDir is empty, which disables the scan).
//
// Exported because the system-deps probe must report exactly what Resolve
// would pick: an in-app download lands OFF PATH, so a LookPath-only probe
// would keep showing 未安装 after a successful install.
func InstalledPath(dataDir, goos, tool string) string {
	if dataDir == "" {
		return ""
	}
	root := filepath.Join(dataDir, "bin", binDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	name := BinName(goos, tool)
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(root, e.Name(), name)
		if usableFile(candidate) != nil {
			continue
		}
		st, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if best == "" || st.ModTime().After(bestMod) {
			best, bestMod = candidate, st.ModTime()
		}
	}
	return best
}

func resolve(envKey, tool, dataDir string) (string, error) {
	if override := os.Getenv(envKey); override != "" {
		if err := usableFile(override); err != nil {
			return "", fmt.Errorf("ffmpegbin: %s=%q is not usable (%v): %w", envKey, override, err, ErrNotAvailable)
		}
		return override, nil
	}

	if path := InstalledPath(dataDir, runtime.GOOS, tool); path != "" {
		return path, nil
	}

	if path, err := exec.LookPath(tool); err == nil {
		return path, nil
	}
	return "", fmt.Errorf(
		"ffmpegbin: %s not found (%s unset, no copy under <dataDir>/bin/ffmpeg/*/%s, not on PATH; "+
			"download it in 设置 → 系统依赖): %w",
		tool, envKey, BinName(runtime.GOOS, tool), ErrNotAvailable)
}

// usableFile reports whether path is an existing, non-directory, executable
// file. The exec-bit check is skipped on Windows, which has no such bit.
func usableFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable (mode %04o)", path, st.Mode().Perm())
	}
	return nil
}
