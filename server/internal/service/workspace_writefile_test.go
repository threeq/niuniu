package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// makeWorktreeWorkspace inserts a workspace row whose Path points at a temp dir
// containing a single (non-git) worktree subdir, for exercising file-write
// plumbing that does not need a real git repo.
func makeWorktreeWorkspace(t *testing.T, q *store.Queries) (workspaceID int64, worktreeName, wsDir string) {
	t.Helper()
	wsDir = t.TempDir()
	worktreeName = "repo-wt"
	if err := os.MkdirAll(filepath.Join(wsDir, ".worktrees", worktreeName), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "canvas-ws",
		Path:      wsDir,
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return ws.ID, worktreeName, wsDir
}

func TestWriteWorktreeFile_WritesAndReturnsRelativePath(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)
	wsID, wt, wsDir := makeWorktreeWorkspace(t, q)
	svc := newWorkspaceServiceForGitStatus(t, q)

	content := `{"type":"excalidraw","elements":[]}`
	got, err := svc.WriteWorktreeFile(context.Background(), wsID, wt, "docs/canvas/annotation.excalidraw", content)
	if err != nil {
		t.Fatalf("WriteWorktreeFile: %v", err)
	}

	wantRel := ".worktrees/" + wt + "/docs/canvas/annotation.excalidraw"
	if got != wantRel {
		t.Errorf("returned path = %q, want %q", got, wantRel)
	}

	onDisk := filepath.Join(wsDir, filepath.FromSlash(wantRel))
	data, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", data, content)
	}
}

func TestWriteWorktreeFile_RejectsBadInput(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)
	wsID, wt, _ := makeWorktreeWorkspace(t, q)
	svc := newWorkspaceServiceForGitStatus(t, q)

	cases := []struct {
		name    string
		wt      string
		relPath string
	}{
		{"into .git", wt, ".git/config"},
		{"empty path", wt, ""},
		{"dot path", wt, "."},
		{"missing worktree", "no-such-wt", "foo.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.WriteWorktreeFile(context.Background(), wsID, tc.wt, tc.relPath, "x")
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// Traversal attempts must never escape the worktree. The path is anchored at
// the worktree root and collapsed, so "../escape" is neutralized to an
// in-worktree write rather than escaping — assert containment (the real
// security invariant), not a specific error.
func TestWriteWorktreeFile_TraversalStaysContained(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)
	wsID, wt, wsDir := makeWorktreeWorkspace(t, q)
	svc := newWorkspaceServiceForGitStatus(t, q)

	worktreeRoot := filepath.Join(wsDir, ".worktrees", wt)
	for _, relPath := range []string{"../escape.txt", "a/b/../../../escape.txt", "..\\escape.txt"} {
		got, err := svc.WriteWorktreeFile(context.Background(), wsID, wt, relPath, "x")
		if err != nil {
			continue // rejecting outright is also acceptable
		}
		// Returned path must be under the worktree, and the file must exist
		// there and nowhere above it.
		if !strings.HasPrefix(got, ".worktrees/"+wt+"/") {
			t.Errorf("input %q wrote outside worktree: %q", relPath, got)
		}
		onDisk := filepath.Join(wsDir, filepath.FromSlash(got))
		if within, _ := filepath.Rel(worktreeRoot, onDisk); strings.HasPrefix(within, "..") {
			t.Errorf("input %q resolved outside worktree root: %q", relPath, onDisk)
		}
		if _, err := os.Stat(filepath.Join(wsDir, "escape.txt")); err == nil {
			t.Errorf("input %q escaped to workspace root", relPath)
		}
	}
}

func TestWriteWorktreeFile_RejectsOversized(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)
	wsID, wt, _ := makeWorktreeWorkspace(t, q)
	svc := newWorkspaceServiceForGitStatus(t, q)

	big := strings.Repeat("a", maxWorktreeFileBytes+1)
	if _, err := svc.WriteWorktreeFile(context.Background(), wsID, wt, "big.excalidraw", big); err == nil {
		t.Fatal("expected oversized write to be rejected")
	}
}
