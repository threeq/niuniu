package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestPickBranch(t *testing.T) {
	cases := []struct {
		name          string
		defaultBranch string
		branches      []string
		want          string
	}{
		{"default in branches", "main", []string{"dev", "main"}, "main"},
		{"default not in branches", "main", []string{"dev", "feat/x"}, "dev"},
		{"empty branches uses default", "main", nil, "main"},
		{"empty default and empty branches", "", nil, ""},
		{"empty default with branches", "", []string{"foo"}, "foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickBranch(c.defaultBranch, c.branches)
			if got != c.want {
				t.Fatalf("pickBranch(%q, %v) = %q, want %q",
					c.defaultBranch, c.branches, got, c.want)
			}
		})
	}
}

// fakeBranchFetcher implements branchInfoFetcher for tests.
type fakeBranchFetcher struct {
	byID map[string]BranchInfo
	errs map[string]error
}

func (f *fakeBranchFetcher) GetBranchInfo(_ context.Context, id string) (BranchInfo, error) {
	if err, ok := f.errs[id]; ok {
		return BranchInfo{}, err
	}
	return f.byID[id], nil
}

type seedRepo struct {
	name                 string
	defaultBranch        string
	projectDefaultBranch string // project_repositories.default_branch override; "" means not set
}

// seedIssueProjectRepos creates a project, a column, an issue in that column,
// N repositories, and links them via project_repositories. Returns the created
// issue ID and a map of repo name → repo ID.
// The project name is derived from the first repo name to stay unique across sub-tests.
func seedIssueProjectRepos(t *testing.T, q *store.Queries, repos []seedRepo) (issueID int64, repoIDs map[string]int64) {
	t.Helper()
	ctx := context.Background()
	projName := "proj"
	if len(repos) > 0 {
		projName = "proj-" + repos[0].name
	}
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: projName, OwnerType: "user"})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "t", Position: 0})
	require.NoError(t, err)
	repoIDs = map[string]int64{}
	for _, r := range repos {
		rep, err := q.CreateRepository(ctx, store.CreateRepositoryParams{
			Name:          r.name,
			Path:          r.name + "-path",
			DefaultBranch: sql.NullString{String: r.defaultBranch, Valid: r.defaultBranch != ""},
			OwnerType:     "user",
		})
		require.NoError(t, err)
		require.NoError(t, q.InsertProjectRepository(ctx, store.InsertProjectRepositoryParams{
			ProjectID:     proj.ID,
			RepositoryID:  rep.ID,
			DefaultBranch: r.projectDefaultBranch, // empty string = not set; service falls back to repo default
		}))
		repoIDs[r.name] = rep.ID
	}
	return issue.ID, repoIDs
}

// setupTestDB opens an in-memory SQLite database, applies the schema, and
// registers cleanup. It avoids importing internal/testing (which would create
// a cycle via isolation_suite.go → internal/service).
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	store.Driver = "sqlite"
	require.NoError(t, store.ApplySchema(db))
	store.Migrate(db)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestGetIssueDefaultRepos(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	q := store.New(db)

	t.Run("returns repos with preferred branch", func(t *testing.T) {
		issueID, ids := seedIssueProjectRepos(t, q, []seedRepo{
			{name: "alpha", defaultBranch: "main"},
			{name: "beta", defaultBranch: "trunk"},
		})
		svc := &WorkspaceService{q: q, repoSvc: &fakeBranchFetcher{
			byID: map[string]BranchInfo{
				strconv.FormatInt(ids["alpha"], 10): {Branches: []string{"main", "dev"}},
				strconv.FormatInt(ids["beta"], 10):  {Branches: []string{"feature", "trunk"}},
			},
		}}
		got, err := svc.GetIssueDefaultRepos(ctx, issueID)
		require.NoError(t, err)
		require.Len(t, got, 2)
		// ORDER BY r.name → alpha, beta
		require.Equal(t, "alpha", got[0].Repository.Name)
		require.Equal(t, "main", got[0].PreferredBranch)
		require.Equal(t, "beta", got[1].Repository.Name)
		require.Equal(t, "trunk", got[1].PreferredBranch)
	})

	t.Run("skips repos whose branch fetch fails", func(t *testing.T) {
		issueID, ids := seedIssueProjectRepos(t, q, []seedRepo{
			{name: "gamma", defaultBranch: "main"},
			{name: "delta", defaultBranch: "trunk"},
		})
		svc := &WorkspaceService{q: q, repoSvc: &fakeBranchFetcher{
			byID: map[string]BranchInfo{
				strconv.FormatInt(ids["delta"], 10): {Branches: []string{"trunk"}},
			},
			errs: map[string]error{
				strconv.FormatInt(ids["gamma"], 10): errors.New("boom"),
			},
		}}
		got, err := svc.GetIssueDefaultRepos(ctx, issueID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "delta", got[0].Repository.Name)
	})

	t.Run("returns empty for issue with no project repos", func(t *testing.T) {
		// Just an issue with no project_repositories entries.
		proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "lonely", OwnerType: "user"})
		require.NoError(t, err)
		col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
		require.NoError(t, err)
		issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "t", Position: 0})
		require.NoError(t, err)

		svc := &WorkspaceService{q: q, repoSvc: &fakeBranchFetcher{}}
		got, err := svc.GetIssueDefaultRepos(ctx, issue.ID)
		require.NoError(t, err)
		require.Len(t, got, 0)
	})

	t.Run("project-level default_branch overrides repo default", func(t *testing.T) {
		// Repo has default_branch "main"; the project pinned the binding to
		// "master" via project_repositories.default_branch. Branches list
		// contains both. Expect PreferredBranch = "master" (project-level wins).
		issueID, ids := seedIssueProjectRepos(t, q, []seedRepo{
			{name: "epsilon", defaultBranch: "main", projectDefaultBranch: "master"},
		})
		svc := &WorkspaceService{q: q, repoSvc: &fakeBranchFetcher{
			byID: map[string]BranchInfo{
				strconv.FormatInt(ids["epsilon"], 10): {Branches: []string{"main", "master", "dev"}},
			},
		}}
		got, err := svc.GetIssueDefaultRepos(ctx, issueID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "master", got[0].PreferredBranch, "project-level override must win over repo default")
	})

	t.Run("falls back to repo default when project override is empty", func(t *testing.T) {
		// Repo has default_branch "develop"; project hasn't configured an
		// override (empty string). Expect PreferredBranch = "develop".
		issueID, ids := seedIssueProjectRepos(t, q, []seedRepo{
			{name: "zeta", defaultBranch: "develop"}, // projectDefaultBranch defaults to ""
		})
		svc := &WorkspaceService{q: q, repoSvc: &fakeBranchFetcher{
			byID: map[string]BranchInfo{
				strconv.FormatInt(ids["zeta"], 10): {Branches: []string{"develop", "main"}},
			},
		}}
		got, err := svc.GetIssueDefaultRepos(ctx, issueID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "develop", got[0].PreferredBranch)
	})
}

// newWorkspaceServiceForTest opens an in-memory SQLite DB, applies the schema,
// creates a temp data dir, and returns a WorkspaceService ready for Create tests.
func newWorkspaceServiceForTest(t *testing.T) (*WorkspaceService, *sql.DB) {
	t.Helper()
	db := openWorkspaceTestDB(t)
	q := store.New(db)
	dataDir := t.TempDir()
	cfg := &config.WorkspaceConfig{
		BaseDir: filepath.Join(dataDir, "workspaces"),
	}
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		t.Fatalf("create base dir: %v", err)
	}
	svc := NewWorkspaceService(q, nil, cfg, dataDir, nil, nil)
	return svc, db
}

func TestWorkspaceService_Create_StampsCreatedByFromCaller(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)

	uid := int64(42)
	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "ws1",
		OwnerType: "user",
		OwnerID:   42,
		CreatedBy: &uid,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=?`, res.Workspace.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != 42 {
		t.Errorf("created_by: want 42, got %v", got)
	}
}

func TestWorkspaceService_Create_FallsBackToOwnerForPersonalWhenCreatedByMissing(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)

	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "ws-personal",
		OwnerType: "user",
		OwnerID:   77,
		CreatedBy: nil, // no caller info
	})
	if err != nil {
		t.Fatal(err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=?`, res.Workspace.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != 77 {
		t.Errorf("personal fallback: want 77, got %v", got)
	}
}

func TestWorkspaceService_Create_OrgWithoutCreatedByLeavesNull(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)

	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "ws-org",
		OwnerType: "org",
		OwnerID:   5,
		CreatedBy: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=?`, res.Workspace.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Errorf("org+nil: want NULL, got %v", got)
	}
}

// seedIssueWithCreator creates a project/column and one issue with the given
// created_by (0 = NULL) and optional parent, returning the issue id. Used by the
// creator-chain tests below.
func seedIssueWithCreator(t *testing.T, q *store.Queries, createdBy int64, parentID *int64) int64 {
	t.Helper()
	ctx := context.Background()
	parentMark := int64(0)
	if parentID != nil {
		parentMark = *parentID
	}
	// Unique name per call: projects are UNIQUE on (owner_type, owner_id, name).
	projName := "proj-creator-c" + strconv.FormatInt(createdBy, 10) + "-p" + strconv.FormatInt(parentMark, 10)
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: projName, OwnerType: "org"})
	if err != nil {
		t.Fatal(err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID:      col.ID,
		Title:         "t",
		Position:      0,
		ParentIssueID: toNullInt64(parentID),
		CreatedBy:     sql.NullInt64{Int64: createdBy, Valid: createdBy > 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return issue.ID
}

// Fallback 2 of the creator chain: an org-owned workspace with no explicit
// caller inherits its bound issue's creator instead of showing "未指定".
func TestWorkspaceService_Create_DerivesCreatorFromIssue(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)

	issueID := seedIssueWithCreator(t, q, 7, nil)
	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "ws-derive-issue",
		OwnerType: "org",
		OwnerID:   5,
		IssueID:   &issueID,
		CreatedBy: nil, // async path: no logged-in caller
	})
	if err != nil {
		t.Fatal(err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=?`, res.Workspace.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != 7 {
		t.Errorf("issue-creator fallback: want 7, got %v", got)
	}
}

// Fallback 3: when the bound (child) issue has no recorded creator, the workspace
// inherits the governing epic (parent issue) creator.
func TestWorkspaceService_Create_DerivesCreatorFromEpicWhenIssueCreatorMissing(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)

	epicID := seedIssueWithCreator(t, q, 9, nil)
	childID := seedIssueWithCreator(t, q, 0 /*NULL*/, &epicID)
	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "ws-derive-epic",
		OwnerType: "org",
		OwnerID:   5,
		IssueID:   &childID,
		CreatedBy: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=?`, res.Workspace.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != 9 {
		t.Errorf("epic-creator fallback: want 9, got %v", got)
	}
}

// Explicit caller (fallback 1) wins over the issue's creator.
func TestWorkspaceService_Create_ExplicitCallerWinsOverIssueCreator(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)

	issueID := seedIssueWithCreator(t, q, 7, nil)
	caller := int64(3)
	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "ws-caller-wins",
		OwnerType: "org",
		OwnerID:   5,
		IssueID:   &issueID,
		CreatedBy: &caller,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=?`, res.Workspace.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != 3 {
		t.Errorf("explicit caller precedence: want 3, got %v", got)
	}
}

// TestWorkspaceService_Create_WithMCPServers verifies that the MCPServers
// field on CreateWorkspaceInput is JSON-encoded and persisted to the
// workspaces.mcp_servers column. mcpGen is intentionally unwired here — the
// .mcp.json generation is non-fatal and tested separately in mcp_test.go;
// this test focuses on the DB persistence contract.
func TestWorkspaceService_Create_WithMCPServers(t *testing.T) {
	svc, _ := newWorkspaceServiceForTest(t)

	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:       "test-ws-with-mcp",
		OwnerType:  "user",
		OwnerID:    1,
		MCPServers: []string{"playwright", "context7"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.q.GetWorkspace(context.Background(), res.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal([]byte(got.McpServers), &names); err != nil {
		t.Fatalf("unmarshal mcp_servers: %v (raw=%q)", err, got.McpServers)
	}
	if !reflect.DeepEqual(names, []string{"playwright", "context7"}) {
		t.Errorf("mcp_servers = %v, want [playwright context7]", names)
	}
}

func TestWorkspaceService_Create_CodexWritesAgentsMD(t *testing.T) {
	svc, _ := newWorkspaceServiceForTest(t)

	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "codex-ws",
		OwnerType: "user",
		OwnerID:   1,
		CliType:   "codex",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	agentsPath := filepath.Join(res.Workspace.Path, "AGENTS.md")
	if _, err := os.Stat(agentsPath); err != nil {
		t.Fatalf("AGENTS.md was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Workspace.Path, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md should not be written for codex workspace, err=%v", err)
	}
}

// TestWorkspaceService_Create_CodexDoesNotAutoAttachSuperpowers verifies
// that creating a codex workspace no longer auto-attaches the
// `codex-superpowers` builtin scene. The scene's prompts used to be
// projected into AGENTS.md to teach codex about the `/spec` / `/plan` /
// `/coding` / `/review` flow, but users who install the superpowers plugin
// globally in their codex install already get those instructions; the
// niuniu-side auto-attach was duplicating that content. Project-default
// scenes (set via ListProjectDefaults) are still honored — only the
// codex-specific hardcoded attach is gone.
func TestWorkspaceService_Create_CodexDoesNotAutoAttachSuperpowers(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)
	// Seed a row with the historical codex-superpowers slug so we can prove
	// even when it exists in the DB, Create does NOT attach it.
	def := &SceneDefinition{Prompts: []PromptFragment{{ID: "x", Title: "x", Body: "x"}}}
	body, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertBuiltinScene(ctx, store.UpsertBuiltinSceneParams{
		Slug:        "codex-superpowers",
		DisplayName: "Codex Superpowers",
		Description: "legacy",
		Tags:        "[]",
		SourceSlug:  "codex-superpowers",
		Definition:  string(body),
		ContentHash: HashDefinition(def),
	}); err != nil {
		t.Fatalf("seed legacy builtin scene: %v", err)
	}
	projector := NewSceneProjector(db, svc.dataDir, nil, nil, nil, nil)
	svc.SetSceneLayerService(NewSceneLayerService(db, projector))
	svc.SetSceneProjector(projector)

	res, err := svc.Create(ctx, CreateWorkspaceInput{
		Name:      "codex-no-auto-scene-ws",
		OwnerType: "user",
		OwnerID:   1,
		CliType:   "codex",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	layers, err := q.ListLayersForWorkspace(ctx, res.Workspace.ID)
	if err != nil {
		t.Fatalf("ListLayersForWorkspace: %v", err)
	}
	// Only the base layer should be present — no codex-superpowers auto-attach.
	if len(layers) != 1 {
		t.Fatalf("expected only the base layer for a codex workspace with no project default scene, got %d layers: %+v", len(layers), layers)
	}
}

func TestWorkspaceService_GetWorkspaceEnvIncludesProjectDefaultSceneEnv(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)
	projector := NewSceneProjector(db, svc.dataDir, nil, nil, nil, nil)
	svc.SetSceneLayerService(NewSceneLayerService(db, projector))
	svc.SetSceneProjector(projector)

	project, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:      "scene-env-project",
		OwnerType: "org",
		OwnerID:   5,
	})
	require.NoError(t, err)
	column, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: project.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: column.ID,
		Title:    "issue",
		Position: 0,
	})
	require.NoError(t, err)
	scene, err := NewSceneService(db).Create(ctx, OwnerRef{Type: "user", ID: 1}, "shared-provider", "Shared Provider", "", nil, &SceneDefinition{
		Assets: SceneAssets{EnvPresets: []EnvPresetAsset{{
			Slug: "shared-provider-env",
			Env: map[string]string{
				"ANTHROPIC_BASE_URL":   "https://scene.example",
				"ANTHROPIC_AUTH_TOKEN": "scene-token",
			},
		}}},
	})
	require.NoError(t, err)
	_, err = q.AttachProjectDefault(ctx, store.AttachProjectDefaultParams{
		ProjectID: project.ID,
		SceneID:   scene.ID,
		Position:  0,
	})
	require.NoError(t, err)

	creator := int64(2)
	res, err := svc.Create(ctx, CreateWorkspaceInput{
		Name:      "member-workspace",
		OwnerType: "org",
		OwnerID:   5,
		IssueID:   &issue.ID,
		CreatedBy: &creator,
	})
	require.NoError(t, err)
	require.NoError(t, q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: res.Workspace.ID,
		Key:         "ANTHROPIC_AUTH_TOKEN",
		Value:       "member-token",
	}))

	env, err := svc.GetWorkspaceEnv(ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Equal(t, "https://scene.example", env["ANTHROPIC_BASE_URL"])
	require.Equal(t, "member-token", env["ANTHROPIC_AUTH_TOKEN"], "explicit workspace env must override scene env")
}

func TestWorkspaceService_GenerateWorkspaceAgentInstructions_CodexPrefersRepoAgents(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	q := store.New(db)
	svc := &WorkspaceService{q: q}

	wsDir := t.TempDir()
	repoDir := filepath.Join(wsDir, ".worktrees", "alpha")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "AGENTS.md"), []byte("# Alpha Repo\n\n- Follow repo rules first.\n- Keep tests green.\n"), 0o644))

	svc.generateWorkspaceAgentInstructions(ctx, wsDir, "codex-ws", "codex", []WorkspaceRepoResult{
		{RepositoryID: 1, WorktreePath: repoDir, Branch: "main"},
	}, false, "zh-CN")

	agents, err := os.ReadFile(filepath.Join(wsDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(agents)
	repoIdx := strings.Index(content, "## Repository Rules")
	wtIdx := strings.Index(content, "## Worktree Instructions")
	if repoIdx < 0 {
		t.Fatalf("AGENTS.md missing repository rules section:\n%s", content)
	}
	if wtIdx < 0 {
		t.Fatalf("AGENTS.md missing worktree instructions section:\n%s", content)
	}
	if repoIdx > wtIdx {
		t.Fatalf("repository rules must appear before worktree instructions:\n%s", content)
	}
	if !strings.Contains(content, "Follow repo rules first.") {
		t.Fatalf("AGENTS.md should inline repo AGENTS.md content:\n%s", content)
	}
}

// TestLanguageDirective verifies the User Language block: a recognized code
// pins a concrete default plus the follow-the-user clause, while an empty or
// unknown code falls back to the generic directive.
func TestLanguageDirective(t *testing.T) {
	zh := languageDirective("zh-CN")
	if !strings.Contains(zh, "## 用户语言 / User Language") {
		t.Fatalf("zh-CN directive missing section header:\n%s", zh)
	}
	if !strings.Contains(zh, "简体中文") || !strings.Contains(zh, "`zh-CN`") {
		t.Fatalf("zh-CN directive should name the concrete language:\n%s", zh)
	}
	if !strings.Contains(zh, "改用其他语言") {
		t.Fatalf("zh-CN directive should keep the follow-the-user clause:\n%s", zh)
	}

	en := languageDirective("en")
	if !strings.Contains(en, "English") || !strings.Contains(en, "`en`") {
		t.Fatalf("en directive should name English:\n%s", en)
	}

	for _, code := range []string{"", "fr", "xx-YY"} {
		generic := languageDirective(code)
		if !strings.Contains(generic, "## 用户语言 / User Language") {
			t.Fatalf("generic directive (%q) missing section header:\n%s", code, generic)
		}
		if strings.Contains(generic, "默认使用") {
			t.Fatalf("generic directive (%q) should not pin a concrete default:\n%s", code, generic)
		}
		if !strings.Contains(generic, "Reply in the same language the user writes in.") {
			t.Fatalf("generic directive (%q) missing fallback text:\n%s", code, generic)
		}
	}
}

// TestWorkspaceService_GenerateInstructions_InjectsLanguage verifies both the
// multi-repo and no-repo instruction files carry the User Language directive
// seeded from the creating user's language.
func TestWorkspaceService_GenerateInstructions_InjectsLanguage(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	q := store.New(db)
	svc := &WorkspaceService{q: q}

	// Multi-repo (claude) workspace.
	multiDir := t.TempDir()
	repoDir := filepath.Join(multiDir, ".worktrees", "alpha")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	svc.generateWorkspaceAgentInstructions(ctx, multiDir, "ws", "claude", []WorkspaceRepoResult{
		{RepositoryID: 1, WorktreePath: repoDir, Branch: "main"},
	}, false, "zh-CN")
	multi, err := os.ReadFile(filepath.Join(multiDir, "CLAUDE.md"))
	require.NoError(t, err)
	if !strings.Contains(string(multi), "## 用户语言 / User Language") || !strings.Contains(string(multi), "简体中文") {
		t.Fatalf("multi-repo CLAUDE.md missing language directive:\n%s", multi)
	}

	// No-repo workspace, English.
	noRepoDir := t.TempDir()
	svc.generateWorkspaceAgentInstructions(ctx, noRepoDir, "office", "claude", nil, true, "en")
	noRepo, err := os.ReadFile(filepath.Join(noRepoDir, "CLAUDE.md"))
	require.NoError(t, err)
	if !strings.Contains(string(noRepo), "## 用户语言 / User Language") || !strings.Contains(string(noRepo), "English") {
		t.Fatalf("no-repo CLAUDE.md missing language directive:\n%s", noRepo)
	}
}

// TestWorkspaceService_Create_NoRepo verifies that a no-repo workspace is a
// plain directory: any repos are dropped, zero worktree rows are created, and
// the root instruction file describes the plain-directory mode.
func TestWorkspaceService_Create_NoRepo(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)

	res, err := svc.Create(ctx, CreateWorkspaceInput{
		Name:      "office-task",
		OwnerType: "user",
		OwnerID:   1,
		NoRepo:    true,
		// Repos intentionally set to prove NoRepo drops them before the
		// worktree-provisioning loop (RepoID 999 would otherwise error).
		Repos: []RepoBranch{{RepoID: 999, Branch: "main"}},
	})
	require.NoError(t, err)
	require.Empty(t, res.Repos)
	require.Empty(t, res.Errors)

	// No worktree rows for a no-repo workspace.
	wts, err := q.ListWorktrees(ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Empty(t, wts, "no-repo workspace must have zero worktrees")

	// The root CLAUDE.md must describe the plain-directory (no-repo) mode.
	claude, err := os.ReadFile(filepath.Join(res.Workspace.Path, "CLAUDE.md"))
	require.NoError(t, err)
	require.Contains(t, string(claude), "no git worktrees")
}

// TestWorkspaceService_Create_PersistsLanguage verifies the creating user's
// language is stored on the workspace row (so epic-derived child workspaces can
// inherit it) and seeded into the generated CLAUDE.md directive.
func TestWorkspaceService_Create_PersistsLanguage(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	q := store.New(db)

	res, err := svc.Create(ctx, CreateWorkspaceInput{
		Name:      "lang-task",
		OwnerType: "user",
		OwnerID:   1,
		NoRepo:    true,
		Language:  "zh-CN",
	})
	require.NoError(t, err)

	// Persisted on the row for epic inheritance.
	ws, err := q.GetWorkspace(ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Equal(t, "zh-CN", ws.Language)

	// Seeded into the generated directive.
	claude, err := os.ReadFile(filepath.Join(res.Workspace.Path, "CLAUDE.md"))
	require.NoError(t, err)
	require.Contains(t, string(claude), "## 用户语言 / User Language")
	require.Contains(t, string(claude), "简体中文")
}
