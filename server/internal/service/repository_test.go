package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreate_RejectsPathInsideDataDir is the server-side backstop for the
// directory picker: a local-path repository whose path falls inside the
// ~/.niuniu data dir must be rejected before any DB / filesystem work. The
// guard runs ahead of store access, so a nil store is fine here.
func TestCreate_RejectsPathInsideDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".niuniu")
	svc := NewRepositoryService(nil, nil, nil, dataDir)

	for _, p := range []string{
		dataDir,
		filepath.Join(dataDir, "workspaces", "ws-1"),
	} {
		_, _, err := svc.Create(context.Background(), CreateRepositoryInput{Path: p})
		if err == nil || !strings.Contains(err.Error(), "PATH_FORBIDDEN") {
			t.Errorf("Create(path=%q) error = %v, want PATH_FORBIDDEN", p, err)
		}
	}
}
