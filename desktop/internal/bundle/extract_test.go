package bundle

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExtractToCacheWritesOnce(t *testing.T) {
	cache := t.TempDir()
	payload := []byte("#!/fake\nbinary\n")

	path, err := extractToCache(cache, "fake-server", "abc123", bytes.NewReader(payload))
	require.NoError(t, err)
	require.FileExists(t, path)

	// Second call with identical bytes must short-circuit (same path)
	path2, err := extractToCache(cache, "fake-server", "abc123", bytes.NewReader(payload))
	require.NoError(t, err)
	require.Equal(t, path, path2)

	// Different bytes (different key) produce a different cache entry
	payload2 := []byte("#!/fake\nbinaryv2\n")
	path3, err := extractToCache(cache, "fake-server", "def456", bytes.NewReader(payload2))
	require.NoError(t, err)
	require.NotEqual(t, path, path3)
	require.FileExists(t, path3)
}

func TestExtractToCachePreservesExtension(t *testing.T) {
	cache := t.TempDir()
	path, err := extractToCache(cache, "niuniu-mcp.exe", "deadbeef", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cache, "niuniu-mcp-deadbeef.exe"), path,
		"extension must be preserved at the end so glob 'niuniu-mcp*.exe' matches")
}

func TestExtractToCacheIsExecutable(t *testing.T) {
	cache := t.TempDir()
	path, err := extractToCache(cache, "fake-server", "exec01", bytes.NewReader([]byte("x")))
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	// chmod 0o755 check is Unix-only; Windows reports a different bitset.
	if filepath.Separator == '/' {
		require.NotZero(t, info.Mode()&0o100, "owner execute bit must be set")
	}

	// Content round-trip
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "x", string(b))
}

func TestExtractToCache_RemovesResidualOldVersions(t *testing.T) {
	cache := t.TempDir()

	// Pre-existing residual binaries from previous app versions, plus an
	// unrelated cache entry that must NOT be touched.
	residualA := filepath.Join(cache, "niuniu-server-aaaa")
	residualB := filepath.Join(cache, "niuniu-server-bbbb")
	unrelated := filepath.Join(cache, "niuniu-mcp-cccc")
	for _, p := range []string{residualA, residualB, unrelated} {
		require.NoError(t, os.WriteFile(p, []byte("old"), 0o755))
	}

	// Extract a new "version" (different content key).
	path, err := extractToCache(cache, "niuniu-server", "newkey", bytes.NewReader([]byte("new")))
	require.NoError(t, err)
	require.FileExists(t, path)

	// Residual same-prefix entries are gone; differently-prefixed entry stays.
	require.NoFileExists(t, residualA)
	require.NoFileExists(t, residualB)
	require.FileExists(t, unrelated)
}

func TestExtractToCache_ShortCircuitDoesNotWipeResiduals(t *testing.T) {
	cache := t.TempDir()

	// Seed the target so extraction short-circuits on the next call.
	target := filepath.Join(cache, "niuniu-server-key1")
	require.NoError(t, os.WriteFile(target, []byte("current"), 0o755))

	// A residual older entry next to it.
	residual := filepath.Join(cache, "niuniu-server-old")
	require.NoError(t, os.WriteFile(residual, []byte("older"), 0o755))

	got, err := extractToCache(cache, "niuniu-server", "key1", bytes.NewReader([]byte("current")))
	require.NoError(t, err)
	require.Equal(t, target, got)

	// No extraction happened, so cleanup must NOT have run — residual is
	// still there until the post-spawn 7-day GC eventually catches it.
	require.FileExists(t, residual)
}

func TestGCOldCacheEntries(t *testing.T) {
	cache := t.TempDir()

	// Create three fake cached entries with varied mtimes.
	old1 := filepath.Join(cache, "niuniu-server-aaaa")
	old2 := filepath.Join(cache, "niuniu-server-bbbb")
	curr := filepath.Join(cache, "niuniu-server-cccc")
	for _, p := range []string{old1, old2, curr} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o755))
	}
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	recentTime := time.Now().Add(-1 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(old1, oldTime, oldTime))
	require.NoError(t, os.Chtimes(old2, oldTime, oldTime))
	require.NoError(t, os.Chtimes(curr, recentTime, recentTime))

	// GC everything older than 7 days, preserving curr
	removed, err := gcOldCacheEntries(cache, "niuniu-server-", curr, 7*24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 2, removed, "expected both old entries removed")
	require.NoFileExists(t, old1)
	require.NoFileExists(t, old2)
	require.FileExists(t, curr)
}

func TestGCOldCacheEntries_NoPrefixMatch(t *testing.T) {
	cache := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cache, "unrelated-file"), []byte("x"), 0o644))
	removed, err := gcOldCacheEntries(cache, "niuniu-server-", "", time.Hour)
	require.NoError(t, err)
	require.Zero(t, removed)
}
