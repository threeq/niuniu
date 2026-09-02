package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// makeSearchHandlerForTest builds a FileTreeHandler over in-memory sqlite whose
// single workspace row points at a real tmpdir laid out like a live workspace
// (`.worktrees/<repo>/…`). userID=0 in the gin context bypasses the Authz gate.
func makeSearchHandlerForTest(t *testing.T) (*FileTreeHandler, string, int64) {
	t.Helper()

	wsDir := t.TempDir()
	// t.TempDir() on macOS hands back /var/... which is a symlink to /private/var.
	// The handler resolves the workspace root, so resolve here too or every
	// boundary assertion in these tests would compare mismatched prefixes.
	if resolved, err := filepath.EvalSymlinks(wsDir); err == nil {
		wsDir = resolved
	}

	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	store.Migrate(db)

	var wsID int64
	if err := db.QueryRow(
		`INSERT INTO workspaces (path, name) VALUES (?, ?) RETURNING id`,
		wsDir, "search-test-workspace",
	).Scan(&wsID); err != nil {
		res, err2 := db.Exec(`INSERT INTO workspaces (path, name) VALUES (?, ?)`, wsDir, "search-test-workspace")
		if err2 != nil {
			t.Fatalf("insert workspace: %v", err2)
		}
		wsID, _ = res.LastInsertId()
	}

	return NewFileTreeHandler(store.New(db)), wsDir, wsID
}

// writeRepoFile creates wsDir/.worktrees/<repo>/<rel> with the given content.
func writeRepoFile(t *testing.T, wsDir, repo, rel, content string) string {
	t.Helper()
	full := filepath.Join(wsDir, ".worktrees", repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// initGitRepo turns wsDir/.worktrees/<repo> into a git repo with everything
// committed, which the git-grep fallback requires.
func initGitRepo(t *testing.T, wsDir, repo string) {
	t.Helper()
	dir := filepath.Join(wsDir, ".worktrees", repo)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
}

// doSearch drives the handler through a real gin context and decodes the body.
func doSearch(t *testing.T, h *FileTreeHandler, wsID int64, rawQuery string) (int, ContentSearchResponse) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", wsID)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/?"+rawQuery, nil)

	h.SearchContent(c)

	var body ContentSearchResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (raw: %s)", err, w.Body.String())
		}
	}
	return w.Code, body
}

// findFile returns the result entry for a workspace-relative path.
func findFile(res ContentSearchResponse, path string) *contentFileResult {
	for i := range res.Files {
		if res.Files[i].Path == path {
			return &res.Files[i]
		}
	}
	return nil
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed on this host")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed on this host")
	}
}

// requireSymlinks skips when the host cannot create symlinks. Testing the
// capability beats checking runtime.GOOS: unprivileged Windows fails, but
// Windows with Developer Mode succeeds and should run these tests.
func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
}

// withLookPath swaps the engine-discovery hook for one test.
func withLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := contentLookPath
	contentLookPath = fn
	t.Cleanup(func() { contentLookPath = orig })
}

// onlyGitLookPath makes ripgrep look absent so the git-grep fallback is taken.
func onlyGitLookPath(t *testing.T) {
	t.Helper()
	withLookPath(t, func(name string) (string, error) {
		if name == "rg" {
			return "", errors.New("not found")
		}
		return exec.LookPath(name)
	})
}

// --- basic behaviour -------------------------------------------------------

func TestSearchContent_FindsMatchesWithContext(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "src/app.go", "package main\n\nfunc needleFunc() {}\n\nvar x = 1\n")

	code, res := doSearch(t, h, wsID, "q=needleFunc")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if res.Engine != engineRipgrep {
		t.Errorf("engine = %q, want %q", res.Engine, engineRipgrep)
	}
	if res.TotalMatches != 1 {
		t.Fatalf("totalMatches = %d, want 1", res.TotalMatches)
	}

	f := findFile(res, ".worktrees/repo1/src/app.go")
	if f == nil {
		t.Fatalf("expected hit in .worktrees/repo1/src/app.go, got %+v", res.Files)
	}
	if f.Repo != "repo1" {
		t.Errorf("repo = %q, want repo1", f.Repo)
	}
	m := f.Matches[0]
	if m.Line != 3 {
		t.Errorf("line = %d, want 3", m.Line)
	}
	if !strings.Contains(m.Text, "needleFunc") {
		t.Errorf("text = %q, want it to contain the match", m.Text)
	}
	if len(m.Columns) == 0 {
		t.Error("expected column offsets for highlighting")
	}
	// context=1 by default, so line 2 (blank) precedes and line 4 (blank) follows.
	if len(m.Before) != 1 || len(m.After) != 1 {
		t.Errorf("context = %d before / %d after, want 1/1", len(m.Before), len(m.After))
	}
}

func TestSearchContent_NoMatchesIsEmptyNotError(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "a.txt", "nothing here\n")

	code, res := doSearch(t, h, wsID, "q=zzz_no_such_token_zzz")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if res.TotalMatches != 0 || len(res.Files) != 0 {
		t.Errorf("expected no matches, got %+v", res)
	}
	if res.Truncated {
		t.Error("a complete empty result must not be flagged truncated")
	}
}

func TestSearchContent_RequiresQuery(t *testing.T) {
	h, _, wsID := makeSearchHandlerForTest(t)
	for _, q := range []string{"", "q=", "q=%20%20"} {
		if code, _ := doSearch(t, h, wsID, q); code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, code)
		}
	}
}

func TestSearchContent_RejectsOverlongQuery(t *testing.T) {
	h, _, wsID := makeSearchHandlerForTest(t)
	code, _ := doSearch(t, h, wsID, "q="+strings.Repeat("a", contentMaxQueryLen+1))
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an overlong pattern", code)
	}
}

// --- engine availability ---------------------------------------------------

// The acceptance criterion the issue calls out explicitly: with no engine on
// PATH the endpoint must say "cannot search", never return an empty result set
// that reads as "nothing found".
func TestSearchContent_NoEngineReturnsExplicitError(t *testing.T) {
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "a.txt", "needle\n")

	withLookPath(t, func(string) (string, error) { return "", errors.New("not found") })

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", wsID)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/?q=needle", nil)
	h.SearchContent(c)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var errBody ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "SEARCH_ENGINE_MISSING" {
		t.Errorf("code = %q, want SEARCH_ENGINE_MISSING", errBody.Error.Code)
	}
	// Guard against the silent-empty regression directly.
	if strings.Contains(w.Body.String(), `"files":[]`) {
		t.Error("missing-engine response must not look like an empty result set")
	}
}

func TestSearchContent_FallsBackToGitGrep(t *testing.T) {
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "src/app.go", "package main\n\nfunc needleFunc() {}\n")
	initGitRepo(t, wsDir, "repo1")

	onlyGitLookPath(t)

	code, res := doSearch(t, h, wsID, "q=needleFunc")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if res.Engine != engineGitGrep {
		t.Fatalf("engine = %q, want %q", res.Engine, engineGitGrep)
	}
	f := findFile(res, ".worktrees/repo1/src/app.go")
	if f == nil {
		t.Fatalf("git grep fallback found nothing; files = %+v", res.Files)
	}
	if f.Matches[0].Line != 3 {
		t.Errorf("line = %d, want 3", f.Matches[0].Line)
	}
}

// --- limits ----------------------------------------------------------------

func TestSearchContent_TotalResultLimit(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	// Spread well past contentMaxResults across enough files that no single one
	// is capped first — this must exercise the GLOBAL ceiling.
	perFile := contentMaxPerFile
	files := (contentMaxResults / perFile) + 5
	for i := 0; i < files; i++ {
		var b strings.Builder
		for j := 0; j < perFile; j++ {
			fmt.Fprintf(&b, "widespread token line %d\n", j)
		}
		writeRepoFile(t, wsDir, "repo1", fmt.Sprintf("f%03d.txt", i), b.String())
	}

	code, res := doSearch(t, h, wsID, "q=widespread")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if res.TotalMatches > contentMaxResults {
		t.Errorf("totalMatches = %d, exceeds cap %d", res.TotalMatches, contentMaxResults)
	}
	if !res.Truncated {
		t.Error("hitting the global cap must set truncated")
	}
	if res.TruncatedReason != truncReasonLimit {
		t.Errorf("truncatedReason = %q, want %q", res.TruncatedReason, truncReasonLimit)
	}
}

func TestSearchContent_PerFileLimit(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	var b strings.Builder
	for i := 0; i < contentMaxPerFile*3; i++ {
		fmt.Fprintf(&b, "crowded %d\n", i)
	}
	writeRepoFile(t, wsDir, "repo1", "big.txt", b.String())

	_, res := doSearch(t, h, wsID, "q=crowded")
	f := findFile(res, ".worktrees/repo1/big.txt")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	if len(f.Matches) > contentMaxPerFile {
		t.Errorf("matches = %d, exceeds per-file cap %d", len(f.Matches), contentMaxPerFile)
	}
	if !f.Truncated {
		t.Error("a file over the per-file cap must be flagged truncated")
	}
	if !res.Truncated {
		t.Error("per-file truncation must also surface at the response level")
	}
}

// The per-file cap boundary is exact: ripgrep runs with --max-count = cap+1 so
// the handler can tell "exactly full" from "there were more". Off-by-one here
// would either hide a truncation or cry wolf on a complete file.
func TestSearchContent_ExactlyAtPerFileCapIsNotTruncated(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	var b strings.Builder
	for i := 0; i < contentMaxPerFile; i++ {
		fmt.Fprintf(&b, "exactcap %d\n", i)
	}
	writeRepoFile(t, wsDir, "repo1", "exact.txt", b.String())

	_, res := doSearch(t, h, wsID, "q=exactcap")
	f := findFile(res, ".worktrees/repo1/exact.txt")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	if len(f.Matches) != contentMaxPerFile {
		t.Errorf("matches = %d, want exactly %d", len(f.Matches), contentMaxPerFile)
	}
	if f.Truncated || res.Truncated {
		t.Error("a file exactly at the cap lost nothing and must not be flagged truncated")
	}
}

func TestSearchContent_OneOverPerFileCapIsTruncated(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	var b strings.Builder
	for i := 0; i < contentMaxPerFile+1; i++ {
		fmt.Fprintf(&b, "overcap %d\n", i)
	}
	writeRepoFile(t, wsDir, "repo1", "over.txt", b.String())

	_, res := doSearch(t, h, wsID, "q=overcap")
	f := findFile(res, ".worktrees/repo1/over.txt")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	if !f.Truncated {
		t.Errorf("one match over the cap must be flagged truncated (got %d matches)", len(f.Matches))
	}
}

func TestSearchContent_LineLengthLimit(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	// A minified-bundle style line: the match sits early, megabytes of tail follow.
	writeRepoFile(t, wsDir, "repo1", "bundle.js", "longtoken"+strings.Repeat("x", 50_000)+"\n")

	_, res := doSearch(t, h, wsID, "q=longtoken")
	f := findFile(res, ".worktrees/repo1/bundle.js")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	m := f.Matches[0]
	if len([]rune(m.Text)) > contentMaxLineLen {
		t.Errorf("line length = %d runes, exceeds cap %d", len([]rune(m.Text)), contentMaxLineLen)
	}
	if !m.LineTruncated {
		t.Error("a clipped line must carry lineTruncated")
	}
}

func TestSearchContent_WideQueryStaysBounded(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	// The issue's stated worst case: a one-letter query over a large tree.
	for i := 0; i < 60; i++ {
		var b strings.Builder
		for j := 0; j < 200; j++ {
			fmt.Fprintf(&b, "the quick brown fox jumps over the lazy dog %d\n", j)
		}
		writeRepoFile(t, wsDir, "repo1", fmt.Sprintf("dir%02d/file.txt", i), b.String())
	}

	code, res := doSearch(t, h, wsID, "q=e")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if res.TotalMatches > contentMaxResults {
		t.Errorf("totalMatches = %d, exceeds cap %d", res.TotalMatches, contentMaxResults)
	}
	if !res.Truncated {
		t.Error("a query matching 12k lines must report truncation")
	}
	for _, f := range res.Files {
		if len(f.Matches) > contentMaxPerFile {
			t.Fatalf("%s: %d matches exceeds per-file cap", f.Path, len(f.Matches))
		}
	}
}

func TestSearchContent_ContextClampedToMax(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		if i == 20 {
			b.WriteString("the anchor line\n")
			continue
		}
		fmt.Fprintf(&b, "filler %d\n", i)
	}
	writeRepoFile(t, wsDir, "repo1", "ctx.txt", b.String())

	_, res := doSearch(t, h, wsID, "q=anchor&context=99")
	f := findFile(res, ".worktrees/repo1/ctx.txt")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	m := f.Matches[0]
	if len(m.Before) > contentMaxContext || len(m.After) > contentMaxContext {
		t.Errorf("context = %d/%d, exceeds cap %d", len(m.Before), len(m.After), contentMaxContext)
	}
}

func TestSearchContent_RejectsNegativeContext(t *testing.T) {
	h, _, wsID := makeSearchHandlerForTest(t)
	if code, _ := doSearch(t, h, wsID, "q=x&context=-1"); code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

// --- search options --------------------------------------------------------

func TestSearchContent_CaseSensitivity(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "case.txt", "MixedCaseToken\nmixedcasetoken\n")

	_, insensitive := doSearch(t, h, wsID, "q=mixedcasetoken")
	if insensitive.TotalMatches != 2 {
		t.Errorf("case-insensitive (default): %d matches, want 2", insensitive.TotalMatches)
	}

	_, sensitive := doSearch(t, h, wsID, "q=mixedcasetoken&case=1")
	if sensitive.TotalMatches != 1 {
		t.Errorf("case-sensitive: %d matches, want 1", sensitive.TotalMatches)
	}
}

func TestSearchContent_WholeWord(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "w.txt", "count\ncounter\nrecount\n")

	_, loose := doSearch(t, h, wsID, "q=count")
	if loose.TotalMatches != 3 {
		t.Errorf("substring: %d matches, want 3", loose.TotalMatches)
	}

	_, word := doSearch(t, h, wsID, "q=count&word=1")
	if word.TotalMatches != 1 {
		t.Errorf("whole-word: %d matches, want 1", word.TotalMatches)
	}
}

func TestSearchContent_LiteralByDefaultRegexOptIn(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "r.txt", "a.b\naxb\n")

	// Default is fixed-strings, so "a.b" must not match "axb".
	_, literal := doSearch(t, h, wsID, "q=a.b")
	if literal.TotalMatches != 1 {
		t.Errorf("literal: %d matches, want 1 (the dot must not be a wildcard)", literal.TotalMatches)
	}

	_, re := doSearch(t, h, wsID, "q=a.b&regex=1")
	if re.TotalMatches != 2 {
		t.Errorf("regex: %d matches, want 2", re.TotalMatches)
	}
}

// A pattern that would pin an NFA-with-backtracking engine. ripgrep is RE2 so it
// is fast; the point is that the request completes rather than hanging.
func TestSearchContent_PathologicalRegexDoesNotHang(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "evil.txt", strings.Repeat("a", 5000)+"b\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		doSearch(t, h, wsID, "q=%28a%2B%29%2B%24&regex=1") // (a+)+$
	}()

	select {
	case <-done:
	case <-timeAfterTestTimeout():
		t.Fatal("catastrophic-backtracking pattern hung the request")
	}
}

// --- binary / gitignore ----------------------------------------------------

func TestSearchContent_SkipsBinaryAndGitignored(t *testing.T) {
	requireRipgrep(t)
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)

	writeRepoFile(t, wsDir, "repo1", ".gitignore", "ignored/\n*.log\n")
	writeRepoFile(t, wsDir, "repo1", "visible.txt", "shared_token here\n")
	writeRepoFile(t, wsDir, "repo1", "ignored/hidden.txt", "shared_token here\n")
	writeRepoFile(t, wsDir, "repo1", "noisy.log", "shared_token here\n")
	// NUL bytes make this binary to both engines.
	writeRepoFile(t, wsDir, "repo1", "blob.bin", "shared_token\x00\x00\x00binary\n")
	initGitRepo(t, wsDir, "repo1")

	_, res := doSearch(t, h, wsID, "q=shared_token")

	if findFile(res, ".worktrees/repo1/visible.txt") == nil {
		t.Error("tracked text file should match")
	}
	for _, blocked := range []string{
		".worktrees/repo1/ignored/hidden.txt",
		".worktrees/repo1/noisy.log",
		".worktrees/repo1/blob.bin",
	} {
		if findFile(res, blocked) != nil {
			t.Errorf("%s should have been skipped (gitignored or binary)", blocked)
		}
	}
}

func TestSearchContent_SkipsGitInternals(t *testing.T) {
	requireRipgrep(t)
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "a.txt", "plain file\n")
	initGitRepo(t, wsDir, "repo1")

	// A token inside .git/config must not surface: --hidden is on (the workspace
	// lives under a dot-dir), so .git has to be excluded explicitly.
	cfg := filepath.Join(wsDir, ".worktrees", "repo1", ".git", "config")
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if err := os.WriteFile(cfg, append(body, []byte("\n# gitinternaltoken\n")...), 0o644); err != nil {
		t.Fatalf("write .git/config: %v", err)
	}

	_, res := doSearch(t, h, wsID, "q=gitinternaltoken")
	for _, f := range res.Files {
		if strings.Contains(f.Path, "/.git/") {
			t.Errorf("git internals leaked into results: %s", f.Path)
		}
	}
}

// --- path safety -----------------------------------------------------------

func TestSearchContent_DoesNotEscapeWorkspaceViaSymlink(t *testing.T) {
	requireRipgrep(t)
	requireSymlinks(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)

	// A secret outside the workspace, reachable only through a symlink inside it.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("escaped_secret_token\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	writeRepoFile(t, wsDir, "repo1", "placeholder.txt", "nothing\n")
	if err := os.Symlink(outside, filepath.Join(wsDir, ".worktrees", "repo1", "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, res := doSearch(t, h, wsID, "q=escaped_secret_token")
	if len(res.Files) != 0 {
		t.Errorf("search escaped the workspace boundary via symlink: %+v", res.Files)
	}
}

func TestSearchContent_DoesNotEscapeViaParentSymlink(t *testing.T) {
	requireRipgrep(t)
	requireSymlinks(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)

	// Symlink pointing at the workspace's own parent — following it would expose
	// sibling workspaces.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sibling.txt"), []byte("sibling_secret_token\n"), 0o644); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	writeRepoFile(t, wsDir, "repo1", "placeholder.txt", "nothing\n")
	if err := os.Symlink(outside, filepath.Join(wsDir, "up")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, res := doSearch(t, h, wsID, "q=sibling_secret_token")
	if len(res.Files) != 0 {
		t.Errorf("search reached outside the workspace: %+v", res.Files)
	}
}

// verifyWithinRoot is the last line of defence on every result path, so it is
// tested directly against traversal shapes an engine could theoretically emit.
func TestVerifyWithinRoot_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !verifyWithinRoot(root, "inside.txt") {
		t.Error("a real in-root file must be accepted")
	}

	for _, bad := range []string{
		"../outside.txt",
		"../../etc/passwd",
		"sub/../../outside.txt",
		"..",
	} {
		if verifyWithinRoot(root, bad) {
			t.Errorf("traversal path %q must be rejected", bad)
		}
	}

	abs := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if verifyWithinRoot(root, abs) {
		t.Error("an absolute path outside the root must be rejected")
	}
}

func TestSearchContent_RejectsBadWorkspaceID(t *testing.T) {
	h, _, _ := makeSearchHandlerForTest(t)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "not-a-number"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/?q=x", nil)
	h.SearchContent(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- timeout ---------------------------------------------------------------

// An already-cancelled context must produce a prompt, labelled response rather
// than a hung request.
func TestSearchContent_CancelledContextDoesNotHang(t *testing.T) {
	requireRipgrep(t)
	_, wsDir, _ := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "a.txt", "needle\n")
	root, err := resolveWorkspaceRoot(wsDir)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := runContentSearch(ctx, root, contentSearchOpts{Query: "needle", Context: 1})
		if err != nil {
			return // a hard failure on a dead context is acceptable
		}
		if !res.Truncated || res.TruncatedReason != truncReasonTimeout {
			t.Errorf("cancelled search must report a timeout truncation, got %+v", res)
		}
	}()

	select {
	case <-done:
	case <-timeAfterTestTimeout():
		t.Fatal("cancelled search hung")
	}
}

// --- helpers ---------------------------------------------------------------

// timeAfterTestTimeout bounds the "must not hang" assertions. Generous enough
// that a slow CI box can't flake it, tight enough that a genuine hang fails the
// test rather than the whole package's deadline.
func timeAfterTestTimeout() <-chan time.Time {
	return time.After(60 * time.Second)
}

func isValidUTF8(s string) bool { return utf8.ValidString(s) }

func TestClipLine(t *testing.T) {
	if got, cut := clipLine("hello\n"); got != "hello" || cut {
		t.Errorf("clipLine(short) = %q, %v; want %q, false", got, cut, "hello")
	}
	if got, cut := clipLine("hello\r\n"); got != "hello" || cut {
		t.Errorf("clipLine(crlf) = %q, %v", got, cut)
	}
	long := strings.Repeat("x", contentMaxLineLen+50)
	got, cut := clipLine(long)
	if !cut || len([]rune(got)) != contentMaxLineLen {
		t.Errorf("clipLine(long) = %d runes, cut=%v; want %d, true", len([]rune(got)), cut, contentMaxLineLen)
	}
	// Multi-byte content must be cut on a rune boundary, never mid-character.
	cjk := strings.Repeat("中", contentMaxLineLen+10)
	got, cut = clipLine(cjk)
	if !cut || len([]rune(got)) != contentMaxLineLen {
		t.Errorf("clipLine(cjk) = %d runes, cut=%v", len([]rune(got)), cut)
	}
	if invalid, _ := clipLine("bad\xff\xfebytes"); !isValidUTF8(invalid) {
		t.Errorf("clipLine must sanitise invalid UTF-8, got %q", invalid)
	}
}

func TestRepoFromRelPath(t *testing.T) {
	cases := map[string]string{
		".worktrees/niuniu-main/src/app.go": "niuniu-main",
		".worktrees/repo1/a.txt":            "repo1",
		"CLAUDE.md":                         "",
		".worktrees/onlyrepo":               "",
		"":                                  "",
	}
	for in, want := range cases {
		if got := repoFromRelPath(in); got != want {
			t.Errorf("repoFromRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseGitGrepLine(t *testing.T) {
	path, line, text, ok := parseGitGrepLine("src/app.go\x0042\x00func main() {}")
	if !ok || path != "src/app.go" || line != 42 || text != "func main() {}" {
		t.Errorf("got (%q, %d, %q, %v)", path, line, text, ok)
	}

	// A path containing a colon is exactly why -z is used; it must survive.
	path, line, _, ok = parseGitGrepLine("weird:name.txt\x007\x00body")
	if !ok || path != "weird:name.txt" || line != 7 {
		t.Errorf("colon path: got (%q, %d, %v)", path, line, ok)
	}

	for _, bad := range []string{"", "no-nul-at-all", "path\x00notanumber\x00body", "path\x0012"} {
		if _, _, _, ok := parseGitGrepLine(bad); ok {
			t.Errorf("parseGitGrepLine(%q) should have failed", bad)
		}
	}
}

func TestNormalizeRel(t *testing.T) {
	cases := map[string]string{
		"./src/app.go":   "src/app.go",
		".\\src\\app.go": "src/app.go",
		"src/app.go":     "src/app.go",
		"../escape":      "",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeRel(in); got != want {
			t.Errorf("normalizeRel(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- context-attachment regressions ----------------------------------------
//
// ripgrep emits context lines with no marker saying which match they belong to.
// A later match's LEADING context arrives after the earlier match, so naive
// "attach to the most recent match" logic files it as that match's TRAILING
// context — showing the reader lines that don't actually surround the hit.
// These three tests pin the contiguity rule that prevents it.

func TestSearchContent_ContextNotLeakedBetweenMatches(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	// Two matches far apart, so their context windows cannot overlap.
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		if i == 5 || i == 15 {
			b.WriteString("NEEDLE here\n")
			continue
		}
		fmt.Fprintf(&b, "filler line %d\n", i)
	}
	writeRepoFile(t, wsDir, "repo1", "two.txt", b.String())

	_, res := doSearch(t, h, wsID, "q=NEEDLE&context=1")
	f := findFile(res, ".worktrees/repo1/two.txt")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	if len(f.Matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(f.Matches))
	}

	for i, want := range []struct {
		line          int
		before, after string
	}{
		{5, "filler line 4", "filler line 6"},
		{15, "filler line 14", "filler line 16"},
	} {
		m := f.Matches[i]
		if m.Line != want.line {
			t.Errorf("match[%d].Line = %d, want %d", i, m.Line, want.line)
		}
		if len(m.Before) != 1 || m.Before[0] != want.before {
			t.Errorf("match[%d].Before = %q, want [%q]", i, m.Before, want.before)
		}
		if len(m.After) != 1 || m.After[0] != want.after {
			t.Errorf("match[%d].After = %q, want [%q]", i, m.After, want.after)
		}
	}
}

// Filling the global cap exactly means nothing was dropped — a complete result
// must not carry a truncation flag, or the UI cries wolf on every search that
// happens to land on a round number.
func TestSearchContent_ExactlyAtGlobalCapIsNotTruncated(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	files := contentMaxResults / contentMaxPerFile
	for i := 0; i < files; i++ {
		var b strings.Builder
		for j := 0; j < contentMaxPerFile; j++ {
			fmt.Fprintf(&b, "exactglobal %d\n", j)
		}
		writeRepoFile(t, wsDir, "repo1", fmt.Sprintf("g%02d.txt", i), b.String())
	}

	_, res := doSearch(t, h, wsID, "q=exactglobal")
	if res.TotalMatches != contentMaxResults {
		t.Fatalf("totalMatches = %d, want exactly %d", res.TotalMatches, contentMaxResults)
	}
	if res.Truncated {
		t.Errorf("nothing was dropped, but truncated=true reason=%q", res.TruncatedReason)
	}
	for _, f := range res.Files {
		if f.Truncated {
			t.Errorf("file %s flagged truncated with %d matches", f.Path, len(f.Matches))
		}
	}
}

// Once a file hits the per-file cap, the context lines still streaming past
// belong to matches that were DROPPED — they must not pile onto the last match
// that was kept.
func TestSearchContent_NoContextAttachedAfterPerFileCap(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	var b strings.Builder
	for i := 0; i < contentMaxPerFile+5; i++ {
		fmt.Fprintf(&b, "capped %d\nspacer-%d\n", i, i)
	}
	writeRepoFile(t, wsDir, "repo1", "cap.txt", b.String())

	_, res := doSearch(t, h, wsID, "q=capped&context=1")
	f := findFile(res, ".worktrees/repo1/cap.txt")
	if f == nil {
		t.Fatalf("expected a hit; files = %+v", res.Files)
	}
	last := f.Matches[len(f.Matches)-1]
	if len(last.After) > 1 {
		t.Errorf("context=1 but last match has %d trailing lines: %q", len(last.After), last.After)
	}
}

// The caller's requested radius is the ceiling, not contentMaxContext: asking
// for 1 must never yield 2.
func TestSearchContent_ContextRespectsRequestedRadius(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		if i == 15 {
			b.WriteString("radiusanchor\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	writeRepoFile(t, wsDir, "repo1", "radius.txt", b.String())

	for _, radius := range []int{0, 1, 2} {
		_, res := doSearch(t, h, wsID, fmt.Sprintf("q=radiusanchor&context=%d", radius))
		f := findFile(res, ".worktrees/repo1/radius.txt")
		if f == nil {
			t.Fatalf("radius %d: expected a hit", radius)
		}
		m := f.Matches[0]
		if len(m.Before) != radius || len(m.After) != radius {
			t.Errorf("context=%d: got %d before / %d after, want %d/%d",
				radius, len(m.Before), len(m.After), radius, radius)
		}
	}
}

// --- ignore-file scoping ---------------------------------------------------
//
// Each worktree is searched as its OWN root. If instead one walk started at the
// workspace root, the root's ignore files would apply to everything beneath it,
// and a root `.gitignore`/`.ignore` naming `.worktrees/` would silently hide
// every repo — returning zero results that read as "your code doesn't contain
// this".

// initGitRepoAt turns an arbitrary directory into a committed git repo.
func initGitRepoAt(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v (%s)", args, dir, err, out)
		}
	}
}

func TestSearchContent_RootGitignoreDoesNotHideWorktrees(t *testing.T) {
	requireRipgrep(t)
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "src/app.go", "ignoredworktreetoken\n")
	// The workspace root is itself a repo that ignores the worktrees dir — the
	// real niuniu layout, since worktrees are checkouts, not source.
	if err := os.WriteFile(filepath.Join(wsDir, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	initGitRepoAt(t, wsDir)

	_, res := doSearch(t, h, wsID, "q=ignoredworktreetoken")
	if findFile(res, ".worktrees/repo1/src/app.go") == nil {
		t.Errorf("root .gitignore hid every worktree from content search; files = %+v", res.Files)
	}
}

func TestSearchContent_RootIgnoreFileDoesNotHideWorktrees(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "src/app.go", "dotignoretoken\n")
	if err := os.WriteFile(filepath.Join(wsDir, ".ignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatalf("write .ignore: %v", err)
	}

	_, res := doSearch(t, h, wsID, "q=dotignoretoken")
	if findFile(res, ".worktrees/repo1/src/app.go") == nil {
		t.Errorf("root .ignore hid every worktree from content search; files = %+v", res.Files)
	}
}

// A worktree's OWN .gitignore must still be honoured — scoping the search per
// root must not turn into ignoring ignore files altogether.
func TestSearchContent_WorktreeOwnGitignoreStillApplies(t *testing.T) {
	requireRipgrep(t)
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", ".gitignore", "build/\n")
	writeRepoFile(t, wsDir, "repo1", "src/keep.go", "scopedtoken\n")
	writeRepoFile(t, wsDir, "repo1", "build/gen.go", "scopedtoken\n")
	initGitRepo(t, wsDir, "repo1")

	_, res := doSearch(t, h, wsID, "q=scopedtoken")
	if findFile(res, ".worktrees/repo1/src/keep.go") == nil {
		t.Error("tracked file should match")
	}
	if findFile(res, ".worktrees/repo1/build/gen.go") != nil {
		t.Error("the worktree's own .gitignore must still exclude build/")
	}
}

// --- workspace-root files --------------------------------------------------

// Root files (CLAUDE.md, .mcp.json) are part of the workspace and must be
// searchable — under BOTH engines, not just ripgrep.
func TestSearchContent_FindsWorkspaceRootFiles(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "placeholder.txt", "unrelated\n")
	if err := os.WriteFile(filepath.Join(wsDir, "CLAUDE.md"), []byte("# doc\nrootleveltoken here\n"), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	_, res := doSearch(t, h, wsID, "q=rootleveltoken")
	f := findFile(res, "CLAUDE.md")
	if f == nil {
		t.Fatalf("workspace-root file not searched; files = %+v", res.Files)
	}
	if f.Repo != "" {
		t.Errorf("root file repo = %q, want empty", f.Repo)
	}
}

func TestSearchContent_GitGrepFindsWorkspaceRootFiles(t *testing.T) {
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	writeRepoFile(t, wsDir, "repo1", "placeholder.txt", "unrelated\n")
	initGitRepo(t, wsDir, "repo1")
	if err := os.WriteFile(filepath.Join(wsDir, "CLAUDE.md"), []byte("rootleveltoken here\n"), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	initGitRepoAt(t, wsDir)

	onlyGitLookPath(t)

	_, res := doSearch(t, h, wsID, "q=rootleveltoken")
	if res.Engine != engineGitGrep {
		t.Fatalf("engine = %q, want %q", res.Engine, engineGitGrep)
	}
	if findFile(res, "CLAUDE.md") == nil {
		t.Errorf("git-grep fallback never searched the workspace-root file; files = %+v", res.Files)
	}
}

// --- "could not search" is not "no matches" --------------------------------

// git grep cannot search a plain directory. Returning 200-with-no-results there
// would tell the user their code lacks the string when nothing was ever read.
func TestSearchContent_GitGrepNonRepoIsAnErrorNotSilence(t *testing.T) {
	requireGit(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	// A worktree directory that is NOT a git repo.
	writeRepoFile(t, wsDir, "repo1", "a.txt", "presenttoken here\n")

	onlyGitLookPath(t)

	code, res := doSearch(t, h, wsID, "q=presenttoken")
	if code == http.StatusOK && res.TotalMatches == 0 && !res.Truncated {
		t.Error("un-searchable workspace returned a clean empty result, which reads as 'no matches'")
	}
}

// --- non-UTF-8 content -----------------------------------------------------

// ripgrep sends a line containing invalid UTF-8 as base64 `bytes` with no
// `text`. Reading only `text` would store an empty string and render a blank
// row — a match the user can see the line number of but not the content.
func TestSearchContent_NonUTF8LineStillHasText(t *testing.T) {
	requireRipgrep(t)
	h, wsDir, wsID := makeSearchHandlerForTest(t)
	// Latin-1 bytes around the match: not valid UTF-8, but not binary either.
	full := filepath.Join(wsDir, ".worktrees", "repo1", "latin1.txt")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	body := append([]byte("caf\xe9 mojibaketoken caf\xe9\n"), []byte("plain\n")...)
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}

	_, res := doSearch(t, h, wsID, "q=mojibaketoken")
	f := findFile(res, ".worktrees/repo1/latin1.txt")
	if f == nil {
		t.Skip("ripgrep treated the latin-1 file as binary; nothing to assert")
	}
	m := f.Matches[0]
	if m.Text == "" {
		t.Error("non-UTF-8 line stored with empty text — the result row would render blank")
	}
	if !utf8.ValidString(m.Text) {
		t.Errorf("stored text is not valid UTF-8: %q", m.Text)
	}
	if !strings.Contains(m.Text, "mojibaketoken") {
		t.Errorf("text = %q, want it to contain the match", m.Text)
	}
}

func TestRgLineText(t *testing.T) {
	if got := rgLineText(rgText{Text: "hello"}); got != "hello" {
		t.Errorf("text branch = %q", got)
	}
	// base64("caf\xe9\n") — invalid UTF-8 delivered as bytes.
	if got := rgLineText(rgText{Bytes: "Y2Fm6Qo="}); got == "" {
		t.Error("bytes branch returned empty; the row would render blank")
	}
	if got := rgLineText(rgText{}); got != "" {
		t.Errorf("empty union = %q, want empty", got)
	}
	if got := rgLineText(rgText{Bytes: "!!!not-base64!!!"}); got != "" {
		t.Errorf("undecodable bytes = %q, want empty", got)
	}
}
