// Package ffmpegbin resolves the ffmpeg/ffprobe binaries that the video-creation
// capability shells out to.
//
// Release desktop builds carry static ffmpeg/ffprobe binaries inside the
// niuniu-video-mcp executable: the ffmpeg_bundled build tag gates a go:embed of
// the payload staged by `make ffmpeg-stage` into dist/<goos>-<goarch>/, and the
// first use materialises it under <dataDir>/bin/ffmpeg/<fingerprint>/ — end
// users never install ffmpeg by hand. Development builds (no tag) carry no
// payload and only need $NIUNIU_FFMPEG / PATH.
//
// Resolution order, applied independently to ffmpeg and ffprobe:
//
//  1. $NIUNIU_FFMPEG / $NIUNIU_FFPROBE explicit override. An override that
//     points at a missing or non-executable file is a hard error, never a
//     silent downgrade — the user asked for that exact binary.
//  2. The extracted bundled copy <dataDir>/bin/ffmpeg/<fingerprint>/<tool>,
//     when this build embeds a payload containing that tool.
//  3. exec.LookPath — whatever the PATH provides (dev machines).
//
// When no source yields a usable binary the returned error wraps
// ErrNotAvailable, so callers can degrade (emit assets without composing) via
// errors.Is.
package ffmpegbin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// ErrNotAvailable reports that no usable ffmpeg/ffprobe could be resolved from
// any source. Callers test it with errors.Is and degrade instead of failing.
var ErrNotAvailable = errors.New("no usable ffmpeg binary available")

const (
	// envFFmpeg and envFFprobe are the explicit-override environment
	// variables. When set to a non-empty value they win over everything else.
	envFFmpeg  = "NIUNIU_FFMPEG"
	envFFprobe = "NIUNIU_FFPROBE"

	// markerFile, inside the extraction directory, records the fingerprint of
	// the payload the directory was materialised from. Extraction is skipped
	// when the recorded fingerprint matches the running build's.
	markerFile = ".fp"

	// binDirName is the tool directory under the data dir:
	// <dataDir>/bin/ffmpeg/<fingerprint>/.
	binDirName = "ffmpeg"

	// extractDirPerm is the mode of the extraction directory; binaries inside
	// are written 0755 (non-Windows only — Windows has no exec bit).
	extractDirPerm = 0o755
)

// embeddedPayload returns the static binaries baked into this build for the
// running platform, keyed by file name (e.g. "ffmpeg.exe" / "ffprobe.exe" on
// Windows). It is a variable so tests can substitute a fake payload without a
// real ffmpeg; embed_bundled.go and embed_stub.go provide the real
// implementations and production code never reassigns it.
var embeddedPayload = payload

var (
	// payloadOnce/payloadCache memoise the (possibly large) embedded payload
	// so repeated Resolve calls do not re-read it from the embedded FS.
	payloadOnce  sync.Once
	payloadCache map[string][]byte

	// fpOnce/fpValue memoise Fingerprint.
	fpOnce  sync.Once
	fpValue string
)

// Fingerprint returns a short content hash identifying the embedded payload for
// the running platform. It names the extraction directory
// <dataDir>/bin/ffmpeg/<fingerprint>/ so that a rebuilt payload (new ffmpeg
// version) lands in a fresh directory instead of overwriting a running copy.
//
// It returns "" when the build carries no payload for this platform — always
// the case for !ffmpeg_bundled builds, and for a dist/ tree staged for a
// different GOOS/GOARCH.
func Fingerprint() string {
	fpOnce.Do(func() {
		files := payloadFiles()
		if len(files) == 0 {
			return
		}
		h := sha256.New()
		for _, name := range sortedNames(files) {
			// Length-prefix names and contents so that e.g. renaming a file
			// or moving bytes between two files changes the hash.
			fmt.Fprintf(h, "%s\x00%d\x00", name, len(files[name]))
			h.Write(files[name])
		}
		fpValue = hex.EncodeToString(h.Sum(nil))[:16]
	})
	return fpValue
}

// Resolve returns the path of a usable ffmpeg executable, per the package
// resolution order. dataDir is the application data directory (usually
// ~/.niuniu) under which the bundled payload is extracted; an empty dataDir
// disables the bundled-copy step (and extraction) entirely — used by callers
// that have no data dir, never by the desktop app.
func Resolve(dataDir string) (string, error) {
	return resolve(envFFmpeg, "ffmpeg", dataDir)
}

// ResolveFFprobe is Resolve for ffprobe: it consults $NIUNIU_FFPROBE, the
// extracted bundled copy, then PATH. ffprobe is not required to be present in
// the payload; a missing bundled ffprobe falls back to PATH like any other.
func ResolveFFprobe(dataDir string) (string, error) {
	return resolve(envFFprobe, "ffprobe", dataDir)
}

// Available reports whether Resolve finds a usable ffmpeg. Callers use it to
// decide between full composition and the assets-only degradation path.
func Available(dataDir string) bool {
	_, err := Resolve(dataDir)
	return err == nil
}

func resolve(envKey, tool, dataDir string) (string, error) {
	if override := os.Getenv(envKey); override != "" {
		if err := usableFile(override); err != nil {
			return "", fmt.Errorf("ffmpegbin: %s=%q is not usable (%v): %w", envKey, override, err, ErrNotAvailable)
		}
		return override, nil
	}

	if dataDir != "" {
		if fp := Fingerprint(); fp != "" {
			if _, bundled := payloadFiles()[binName(tool)]; bundled {
				dir, err := extractPayload(dataDir, fp)
				if err != nil {
					// A bundled payload that cannot be materialised is a
					// packaging/disk problem, not a "no ffmpeg" situation:
					// report it verbatim (no ErrNotAvailable) so it is
					// investigated instead of silently degraded.
					return "", fmt.Errorf("ffmpegbin: extract bundled %s payload: %w", tool, err)
				}
				candidate := filepath.Join(dir, binName(tool))
				if err := usableFile(candidate); err != nil {
					return "", fmt.Errorf("ffmpegbin: bundled %s unusable: %w", tool, err)
				}
				return candidate, nil
			}
		}
	}

	if path, err := exec.LookPath(tool); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("ffmpegbin: %s not found (%s unset, not bundled, not on PATH): %w", tool, envKey, ErrNotAvailable)
}

// extractPayload materialises the embedded payload into
// <dataDir>/bin/ffmpeg/<fp>/ and returns that directory. It is idempotent:
// when the .fp marker matches and every payload file is present the directory
// is left untouched. Each file is written to a temp file next to its target
// and renamed into place, so a concurrently starting process can never observe
// a half-written binary; extractMu serialises callers inside this process.
func extractPayload(dataDir, fp string) (string, error) {
	extractMu.Lock()
	defer extractMu.Unlock()

	files := payloadFiles()
	if fp == "" || len(files) == 0 {
		// Unreachable via resolve (which gates on Fingerprint), but guard the
		// invariant rather than silently writing a marker with no payload.
		return "", errors.New("no embedded payload for this platform")
	}

	dir := extractedDir(dataDir, fp)
	if markerMatches(dir, fp) && payloadPresent(dir, files) {
		return dir, nil
	}
	if err := os.MkdirAll(dir, extractDirPerm); err != nil {
		return "", err
	}
	for _, name := range sortedNames(files) {
		if err := writeFileAtomic(filepath.Join(dir, name), files[name], 0o755); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}
	// The marker goes last: it is the "directory is complete" signal, so a
	// crash mid-extraction leaves a marker-less directory that is redone.
	if err := writeFileAtomic(filepath.Join(dir, markerFile), []byte(fp), 0o644); err != nil {
		return "", fmt.Errorf("write %s marker: %w", markerFile, err)
	}
	return dir, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so readers only ever see the complete file. On write
// failure the temp file is removed. The temp file is chmod'ed to mode before
// the rename on platforms with exec bits.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpName, mode); err != nil {
			os.Remove(tmpName)
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// markerMatches reports whether dir carries a .fp marker recording fp.
func markerMatches(dir, fp string) bool {
	b, err := os.ReadFile(filepath.Join(dir, markerFile))
	return err == nil && strings.TrimSpace(string(b)) == fp
}

// payloadPresent reports whether every payload file already exists in dir.
func payloadPresent(dir string, files map[string][]byte) bool {
	for name := range files {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.IsDir() {
			return false
		}
	}
	return true
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

// payloadFiles returns the memoised embedded payload (see embeddedPayload).
func payloadFiles() map[string][]byte {
	payloadOnce.Do(func() { payloadCache = embeddedPayload() })
	return payloadCache
}

// extractedDir is the directory the payload for fp is materialised into:
// <dataDir>/bin/ffmpeg/<fp>.
func extractedDir(dataDir, fp string) string {
	return filepath.Join(dataDir, "bin", binDirName, fp)
}

// platformDir is the dist/ subdirectory holding the payload for a GOOS/GOARCH
// pair (dist/<goos>-<goarch>/), matching scripts/fetch-ffmpeg.sh.
func platformDir(goos, goarch string) string {
	return goos + "-" + goarch
}

// binName returns the executable file name of tool on the running platform.
func binName(tool string) string {
	return binNameFor(runtime.GOOS, tool)
}

// binNameFor returns the executable file name of tool on goos.
func binNameFor(goos, tool string) string {
	if goos == "windows" {
		return tool + ".exe"
	}
	return tool
}

// sortedNames returns the payload file names in deterministic order.
func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// extractMu serialises payload extraction inside this process. Cross-process
// safety comes from the rename-based atomic writes plus the completion marker.
var extractMu sync.Mutex
