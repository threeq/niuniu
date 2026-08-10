package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestGetIssueDefaults_BadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	cases := []struct {
		name string
		url  string
	}{
		{"missing", "/api/workspaces/issue-defaults"},
		{"empty", "/api/workspaces/issue-defaults?issue_id="},
		{"non-numeric", "/api/workspaces/issue-defaults?issue_id=abc"},
		{"zero", "/api/workspaces/issue-defaults?issue_id=0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", c.url, nil)
			srv.Engine().ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
		})
	}
}

func TestGetIssueDefaults_NotFoundIssue(t *testing.T) {
	// An issue id that doesn't exist. When no auth user is set on the request
	// (auth_user_id == 0), the handler skips Authz and goes straight to the
	// service layer. In that path the service returns an empty list (no rows),
	// which is 200.
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/workspaces/issue-defaults?issue_id=999999", nil)
	srv.Engine().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		Repos []map[string]any `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Repos, 0)
}

func TestGetIssueDefaults_EmptyForIssueWithoutRepos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p", OwnerType: "user"})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "t", Position: 0})
	require.NoError(t, err)

	url := "/api/workspaces/issue-defaults?issue_id=" + strconv.FormatInt(issue.ID, 10)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", url, nil)
	srv.Engine().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		Repos []map[string]any `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Repos, 0)
}

// TestCreateFromDirectory_E2E covers issues #232/#233/#235 end to end: a plain
// local folder becomes an auto-init'd repo + a studio workspace in one call,
// and a git Bash allowlist row is preset.
func TestCreateFromDirectory_E2E(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Hermetic global git identity so the auto-init commit succeeds regardless
	// of the host's config.
	gitcfg := filepath.Join(t.TempDir(), ".gitconfig")
	require.NoError(t, os.WriteFile(gitcfg,
		[]byte("[user]\n\tname = Studio Tester\n\temail = studio@example.com\n"), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", gitcfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	srv, db := testutil.SetupTestServer(t)

	// A plain (non-git) directory with a media file.
	dir := filepath.Join(t.TempDir(), "my-notes")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("fake"), 0o644))

	reqBody, _ := json.Marshal(map[string]any{"dir": dir, "name": "studio-e2e"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/workspaces/from-directory", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())

	var resp struct {
		ID           string `json:"id"`
		RepositoryID string `json:"repository_id"`
		IsStudio     bool   `json:"is_studio"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	require.NotEmpty(t, resp.RepositoryID)
	require.True(t, resp.IsStudio, "expected is_studio=true")

	wsID, _ := strconv.ParseInt(resp.ID, 10, 64)

	// The directory is now a git repo with a default .gitignore.
	require.FileExists(t, filepath.Join(dir, ".gitignore"))

	// The workspace is flagged studio in the DB.
	var isStudio int64
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT is_studio FROM workspaces WHERE id = ?", wsID).Scan(&isStudio))
	require.Equal(t, int64(1), isStudio)

	// A git Bash allowlist row was preset (#235).
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM agent_permission_allowlist WHERE workspace_id = ? AND tool_name = 'Bash' AND matcher_value LIKE 'git%'",
		wsID).Scan(&n))
	require.GreaterOrEqual(t, n, 1, "expected a git Bash allowlist entry")
}

// TestCreateFromDirectory_ReusesExistingRepo covers re-selecting a directory
// that is already a registered repository: instead of failing the studio
// create with REPO_NAME_EXISTS (base-name collision), the second call reuses
// the existing repository and just builds another workspace on it.
func TestCreateFromDirectory_ReusesExistingRepo(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gitcfg := filepath.Join(t.TempDir(), ".gitconfig")
	require.NoError(t, os.WriteFile(gitcfg,
		[]byte("[user]\n\tname = Studio Tester\n\temail = studio@example.com\n"), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", gitcfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	srv, db := testutil.SetupTestServer(t)

	dir := filepath.Join(t.TempDir(), "shared-repo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# hi"), 0o644))

	create := func(name string) (string, string, int) {
		reqBody, _ := json.Marshal(map[string]any{"dir": dir, "name": name})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/workspaces/from-directory", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		srv.Engine().ServeHTTP(w, req)
		var resp struct {
			ID           string `json:"id"`
			RepositoryID string `json:"repository_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.ID, resp.RepositoryID, w.Code
	}

	// First call auto-inits the directory and registers it as a repository.
	wsID1, repoID1, code1 := create("first")
	require.Equal(t, http.StatusCreated, code1)
	require.NotEmpty(t, repoID1)

	// Second call on the SAME directory must succeed (not REPO_NAME_EXISTS) and
	// reuse the same repository, producing a distinct workspace.
	wsID2, repoID2, code2 := create("second")
	require.Equal(t, http.StatusCreated, code2)
	require.Equal(t, repoID1, repoID2, "expected the existing repository to be reused")
	require.NotEqual(t, wsID1, wsID2, "expected a new workspace")

	// Exactly one repository row exists for that path.
	var repoCount int
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM repositories").Scan(&repoCount))
	require.Equal(t, 1, repoCount, "expected the directory to be registered once")
}
