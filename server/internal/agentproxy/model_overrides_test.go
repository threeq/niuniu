package agentproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeSettings(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(content), 0o644))
}

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(raw, &root))
	return root
}

// A third-party model set (--model flag + provider env) lands one override
// entry per distinct name under distinct identity keys, and pre-existing
// user settings (hooks etc.) survive the merge.
func TestEnsureClaudeModelOverrides_Merges(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"hooks":{"WorktreeCreate":[]},"modelOverrides":{"claude-fable-5-1":"user-model"}}`)

	env := []string{
		"PATH=x",
		"ANTHROPIC_MODEL=deepseek-v4-flash[1m]",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=deepseek-v4-flash",
		"ANTHROPIC_MODEL_IGNORED=not-a-model-key", // non-model key must not leak in
	}
	require.NoError(t, EnsureClaudeModelOverrides(dir, "deepseek-v4-flash[1m]", env))

	ov := readSettings(t, dir)["modelOverrides"].(map[string]any)
	require.Equal(t, "user-model", ov["claude-fable-5-1"], "user mapping preserved")
	require.Equal(t, "deepseek-v4-flash[1m]", ov["claude-sonnet-4-5"])
	require.Equal(t, "deepseek-v4-flash", ov["claude-haiku-4-5"])
	require.Len(t, ov, 3, "duplicate --model/env name collapsed; non-model env key ignored")
	require.Contains(t, readSettings(t, dir), "hooks", "user settings preserved")
}

// Catalog claude-* names are already recognized — no entries, no rewrite.
func TestEnsureClaudeModelOverrides_SkipsClaudeModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	require.NoError(t, EnsureClaudeModelOverrides(dir, "claude-sonnet-4-5",
		[]string{"ANTHROPIC_MODEL=claude-haiku-4-5"}))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "no settings.json should be created for catalog models")
}

// A second run with identical inputs is a no-op (idempotent, no rewrite).
func TestEnsureClaudeModelOverrides_Idempotent(t *testing.T) {
	dir := t.TempDir()
	env := []string{"ANTHROPIC_MODEL=deepseek-v4-flash[1m]"}
	require.NoError(t, EnsureClaudeModelOverrides(dir, "", env))
	path := filepath.Join(dir, ".claude", "settings.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, EnsureClaudeModelOverrides(dir, "", env))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

// An unparseable settings.json is an error and the file is left untouched —
// never clobber a file we can't read (spawn logs + continues).
func TestEnsureClaudeModelOverrides_BadJSONFailsOpen(t *testing.T) {
	dir := t.TempDir()
	bad := "{not json"
	writeSettings(t, dir, bad)
	err := EnsureClaudeModelOverrides(dir, "deepseek-v4-flash[1m]", nil)
	require.Error(t, err)
	raw, readErr := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, readErr)
	require.Equal(t, bad, string(raw), "file must not be clobbered")
}

// A non-string modelOverrides value is rejected rather than silently dropped,
// and a value already covered by any entry (user's or ours) is not re-added.
func TestEnsureClaudeModelOverrides_RejectsNonStringAndRespectsCoverage(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"modelOverrides":{"claude-sonnet-4-5":42}}`)
	require.Error(t, EnsureClaudeModelOverrides(dir, "deepseek-v4-flash[1m]", nil))

	dir2 := t.TempDir()
	writeSettings(t, dir2, `{"modelOverrides":{"claude-sonnet-4-5":"deepseek-v4-flash[1m]"}}`)
	require.NoError(t, EnsureClaudeModelOverrides(dir2, "deepseek-v4-flash[1m]",
		[]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL=deepseek-v4-flash"}))
	ov := readSettings(t, dir2)["modelOverrides"].(map[string]any)
	require.Len(t, ov, 2, "covered name skipped; only the uncovered one added")
	require.Equal(t, "deepseek-v4-flash", ov["claude-haiku-4-5"])
}
