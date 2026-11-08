package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A failed clone must not leave a staging directory behind. Before the unique
// staging dir, the retry after a transient network/auth failure failed with
// "destination already exists: <name>" even though nothing had been cloned.
func TestCloneRepository_RetryAfterFailure(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewRepositoryService(nil, nil, nil, dataDir)
	missing := filepath.Join(t.TempDir(), "does-not-exist", "repo.git")

	for i := 0; i < 2; i++ {
		_, err := svc.cloneRepository(context.Background(), missing, "aikefu", 0)
		require.Error(t, err, "attempt %d", i+1)
		require.NotContains(t, err.Error(), "destination already exists", "attempt %d", i+1)
	}

	staging := filepath.Join(dataDir, "repositories", "staging")
	entries, err := os.ReadDir(staging)
	require.NoError(t, err)
	require.Empty(t, entries, "failed clones must not leave staging dirs")
}

// A leftover staging/<name> directory from an older build must not block a new
// attempt: new clones use a unique suffix and never reuse the legacy path.
func TestCloneRepository_LegacyLeftoverDoesNotBlock(t *testing.T) {
	dataDir := t.TempDir()
	svc := NewRepositoryService(nil, nil, nil, dataDir)
	legacy := filepath.Join(dataDir, "repositories", "staging", "aikefu")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "partial"), []byte("x"), 0o644))

	missing := filepath.Join(t.TempDir(), "missing", "repo.git")
	_, err := svc.cloneRepository(context.Background(), missing, "aikefu", 0)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "destination already exists")

	// The legacy directory is never touched by the new attempt.
	_, statErr := os.Stat(filepath.Join(legacy, "partial"))
	require.NoError(t, statErr)
}
