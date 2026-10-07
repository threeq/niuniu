package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// makeResolveHandler reuses the search-content test fixtures: an in-memory DB
// whose single workspace points at a tmpdir laid out like a live workspace.
// makeSearchHandlerForTest lives in search_content_test.go (same package).
func makeResolveHandler(t *testing.T) (*FileTreeHandler, string, int64) {
	t.Helper()
	return makeSearchHandlerForTest(t)
}

// initResolveGitRepo turns wsDir/.worktrees/<repo> into a git repo so
// listGitFiles (rungs 5-6) enumerates it via `git ls-files`.
func initResolveGitRepo(t *testing.T, wsDir, repo string) {
	t.Helper()
	repoPath := filepath.Join(wsDir, ".worktrees", repo)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-qm", "init")
}

// doResolve calls the endpoint and decodes the JSON body.
func doResolve(t *testing.T, h *FileTreeHandler, wsID int64, ref string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/workspaces/:id/resolve-file", h.ResolveFile)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/workspaces/%d/resolve-file?ref=%s", wsID, url.QueryEscape(ref)), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return w.Code, body
	}
	return w.Code, nil
}

func expectResolved(t *testing.T, h *FileTreeHandler, wsID int64, ref, wantPath, wantStrategy string) {
	t.Helper()
	code, body := doResolve(t, h, wsID, ref)
	if code != http.StatusOK {
		t.Fatalf("resolve %q: status %d, want 200", ref, code)
	}
	if body["path"] != wantPath {
		t.Fatalf("resolve %q: path %q, want %q (strategy=%v)", ref, body["path"], wantPath, body["strategy"])
	}
	if body["strategy"] != wantStrategy {
		t.Fatalf("resolve %q: strategy %v, want %q", ref, body["strategy"], wantStrategy)
	}
}

func TestResolveFile_Ladder(t *testing.T) {
	h, wsDir, wsID := makeResolveHandler(t)

	// Workspace-root file (outside any worktree).
	writeWorkspaceRootFile(t, wsDir, "CLAUDE.md", "# root")

	// Two repos with overlapping layouts, plus one with a shallow lookalike.
	writeRepoFile(t, wsDir, "alpha", "server/web/src/lib/api.ts", "package api")
	writeRepoFile(t, wsDir, "alpha", "README.md", "# alpha")
	writeRepoFile(t, wsDir, "beta", "server/web/src/lib/api.ts", "package api")
	writeRepoFile(t, wsDir, "beta", "docs/deep/README.md", "# beta deep")
	writeRepoFile(t, wsDir, "beta", "942/nested.txt", "dir named like ws")
	writeRepoFile(t, wsDir, "gamma", "note/deep/README.md", "# gamma deep")
	for _, repo := range []string{"alpha", "beta", "gamma"} {
		initResolveGitRepo(t, wsDir, repo)
	}

	t.Run("exact workspace-relative", func(t *testing.T) {
		expectResolved(t, h, wsID, "CLAUDE.md", "CLAUDE.md", "exact")
	})

	t.Run("repo-relative resolves into the first worktree alphabetically", func(t *testing.T) {
		expectResolved(t, h, wsID,
			"server/web/src/lib/api.ts",
			".worktrees/alpha/server/web/src/lib/api.ts", "worktree")
	})

	t.Run("repo-prefixed reference re-anchors under that repo", func(t *testing.T) {
		expectResolved(t, h, wsID,
			"beta/942/nested.txt",
			".worktrees/beta/942/nested.txt", "repo-prefixed")
	})

	t.Run("workspace-dir-name embedded in the reference is cut", func(t *testing.T) {
		wsBase := filepath.Base(filepath.Clean(wsDir))
		expectResolved(t, h, wsID,
			"some/other/place/"+wsBase+"/CLAUDE.md",
			"CLAUDE.md", "ws-prefixed")
	})

	t.Run("missing-middle suffix match", func(t *testing.T) {
		expectResolved(t, h, wsID,
			"src/lib/api.ts",
			".worktrees/alpha/server/web/src/lib/api.ts", "suffix")
	})

	t.Run("bare filename matches by suffix", func(t *testing.T) {
		expectResolved(t, h, wsID, "nested.txt",
			".worktrees/beta/942/nested.txt", "suffix")
	})

	t.Run("suffix ranks shorter path over deep lookalike", func(t *testing.T) {
		// Neither repo has a literal `deep/README.md` at its root, so the Stat
		// rungs miss; both index entries end with `/deep/README.md` and the
		// shorter one wins.
		expectResolved(t, h, wsID, "deep/README.md",
			".worktrees/beta/docs/deep/README.md", "suffix")
	})

	t.Run("wrong directory but right filename falls through to basename", func(t *testing.T) {
		expectResolved(t, h, wsID, "web/wrong/lib/api.ts",
			".worktrees/alpha/server/web/src/lib/api.ts", "basename")
	})

	t.Run("case-insensitive suffix", func(t *testing.T) {
		expectResolved(t, h, wsID, "SRC/LIB/API.TS",
			".worktrees/alpha/server/web/src/lib/api.ts", "suffix")
	})

	t.Run("windows separators and line suffix tolerated", func(t *testing.T) {
		expectResolved(t, h, wsID, `server\web\src\lib\api.ts:42`,
			".worktrees/alpha/server/web/src/lib/api.ts", "worktree")
	})

	t.Run("traversal rejected", func(t *testing.T) {
		code, _ := doResolve(t, h, wsID, "../../etc/passwd")
		if code != http.StatusBadRequest {
			t.Fatalf("traversal: status %d, want 400", code)
		}
	})

	t.Run("no match at all is 404", func(t *testing.T) {
		code, _ := doResolve(t, h, wsID, "no/such/file/anywhere.ts")
		if code != http.StatusNotFound {
			t.Fatalf("no-match: status %d, want 404", code)
		}
	})

	t.Run("empty ref is 400", func(t *testing.T) {
		code, _ := doResolve(t, h, wsID, "   ")
		if code != http.StatusBadRequest {
			t.Fatalf("empty ref: status %d, want 400", code)
		}
	})
}

// writeWorkspaceRootFile creates wsDir/<rel> (outside .worktrees).
func writeWorkspaceRootFile(t *testing.T, wsDir, rel, content string) string {
	t.Helper()
	full := filepath.Join(wsDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}
