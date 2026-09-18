package agentproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var spillNow = time.Date(2026, 9, 18, 10, 30, 5, 123000000, time.UTC)

func runes(n int) string {
	return strings.Repeat("汉", n) // 3 bytes/char in UTF-8: proves counting is by rune
}

// Within the limit (including exactly at it) the text is returned untouched
// and no spill file is created.
func TestRewriteLargeInput_UnderLimitUntouched(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []int{1, 1000, largeInputRuneLimit} {
		got := rewriteLargeInput(1, dir, runes(n), spillNow)
		require.Equal(t, runes(n), got)
	}
	_, err := os.Stat(filepath.Join(dir, ".team"))
	require.True(t, os.IsNotExist(err), "no spill dir should be created")
}

// Just over the limit: the returned prompt points at a file whose content is
// the verbatim original; the prompt itself is short (the whole point).
func TestRewriteLargeInput_OverLimitSpillsToFile(t *testing.T) {
	dir := t.TempDir()
	original := runes(largeInputRuneLimit + 1)
	got := rewriteLargeInput(7, dir, original, spillNow)

	require.NotEqual(t, original, got)
	require.Less(t, len(got), 500, "prompt must stay tiny")
	require.Contains(t, got, ".team/prompts/prompt-20260918-103005.123.txt")
	require.NotContains(t, got, "\\", "prompt path must use forward slashes")

	raw, err := os.ReadFile(filepath.Join(dir, ".team", "prompts", "prompt-20260918-103005.123.txt"))
	require.NoError(t, err)
	require.Equal(t, original, string(raw), "spilled file must be the verbatim original")
}

// An unwritable spill location fails open: the original text is sent rather
// than the send being blocked.
func TestRewriteLargeInput_FailOpenOnFSError(t *testing.T) {
	dir := t.TempDir()
	// A FILE where the spill DIR must be created makes MkdirAll fail.
	blocker := filepath.Join(dir, ".team")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))

	original := runes(largeInputRuneLimit + 1)
	got := rewriteLargeInput(7, dir, original, spillNow)
	require.Equal(t, original, got)
}
