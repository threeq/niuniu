package bundle

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// extractToCache writes r's content to cacheDir/<stem>-<key><ext>, where
// <stem> and <ext> are name split at its final extension (name="niuniu-mcp.exe"
// → stem="niuniu-mcp", ext=".exe"). Keeping the extension at the end is
// required so downstream glob patterns like "niuniu-mcp*.exe" match.
// If the target file already exists (same key), it short-circuits and
// returns the existing path. The file is chmod'd 0o755 so it is directly
// executable on Unix (Windows ignores the mode).
// key must be a precomputed content-hash prefix (see hashKey in bundle.go).
func extractToCache(cacheDir, name, key string, r io.Reader) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	target := filepath.Join(cacheDir, fmt.Sprintf("%s-%s%s", stem, key, ext))

	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("%w: %v", ErrExtract, err)
	}

	// First-time extraction for this content-hash (i.e. version upgrade or
	// fresh install): wipe residual older binaries sharing the same prefix
	// before writing. Errors are best-effort — the extract itself is the
	// load-bearing step.
	_, _ = gcOldCacheEntries(cacheDir, stem+"-", target, 0)

	// Write to temp file in same dir, then rename for atomicity.
	tmp, err := os.CreateTemp(cacheDir, name+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExtract, err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("%w: %v", ErrExtract, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("%w: %v", ErrExtract, err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("%w: %v", ErrExtract, err)
	}
	// On Windows os.Rename cannot replace an existing file; best-effort remove.
	_ = os.Remove(target)
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("%w: %v", ErrExtract, err)
	}
	return target, nil
}

// gcOldCacheEntries deletes entries in cacheDir whose name starts with
// prefix, excluding keepPath, that are older than maxAge. A maxAge of 0
// matches every existing file (cutoff = now), which is how
// extractToCache wipes residual older versions on a fresh extract.
// Returns number of entries deleted. Errors on individual entries are
// ignored (logged upstream); only directory-read errors propagate.
func gcOldCacheEntries(cacheDir, prefix, keepPath string, maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		full := filepath.Join(cacheDir, name)
		if full == keepPath {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(full); err == nil {
			removed++
		}
	}
	return removed, nil
}
