// End-to-end scenarios for the shared model library + usage/budget feature
// (design §15.3, plan Task 11). Each scenario drives the real service layer over
// a real SQLite schema: model library -> provider row -> sceneenv.Resolve, the
// refresh preview -> import path against an httptest upstream, usage aggregation
// across every dimension, and budget enforcement through the real
// agentproxy.GetOrStartSession gate.
//
// Scenarios deliberately do not spawn an agent CLI: what must hold end-to-end is
// that saved config, env expansion, recorded usage and the budget gate agree.
// The turn -> row recording path itself is covered in the agentproxy package
// (TestRecordModelUsage*); here the rows are written in the exact shape that
// recorder produces (owner/project/workspace/cli/model/provider/hour).
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// e2eFixture is a personal-edition owner (user 1) with one project, one issue
// and no workspaces yet. Workspaces are created per scenario because cli_type is
// fixed at creation time.
type e2eFixture struct {
	db     *sql.DB
	q      *store.Queries
	ctx    context.Context
	owner  OwnerRef
	projID int64
	colID  int64
	issue  store.Issue
}

func newModelLibraryE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	ctx := context.Background()
	db := setupTestDB(t)
	q := store.New(db)
	require.NoError(t, SeedBuiltinModels(ctx, q))

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name: "模型库 E2E 项目", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: proj.ID, Name: "实现", Position: 0,
	})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col.ID, Title: "e2e issue", Position: 0,
	})
	require.NoError(t, err)

	return &e2eFixture{
		db: db, q: q, ctx: ctx,
		owner:  OwnerRef{Type: "user", ID: 1},
		projID: proj.ID, colID: col.ID, issue: issue,
	}
}

// newWorkspace creates a workspace bound to the fixture issue/project and, when
// providerID > 0, binds it to that provider for sceneenv.Resolve.
func (f *e2eFixture) newWorkspace(t *testing.T, cliType string, providerID int64) store.Workspace {
	t.Helper()
	ws, err := f.q.CreateWorkspace(f.ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: f.issue.ID, Valid: true},
		Name:    "ws-" + cliType, Path: t.TempDir(), Status: "running",
		OwnerType: "user", OwnerID: 1, CliType: cliType,
	})
	require.NoError(t, err)
	if providerID > 0 {
		require.NoError(t, f.q.SetWorkspaceEnvProvider(f.ctx, store.SetWorkspaceEnvProviderParams{
			EnvProviderID: sql.NullInt64{Int64: providerID, Valid: true},
			ID:            ws.ID,
		}))
	}
	return ws
}

func (f *e2eFixture) newProvider(t *testing.T, name string, p store.CreateEnvProviderParams) store.EnvProvider {
	t.Helper()
	p.Name = name
	p.Enabled = 1
	p.OwnerType = "user"
	p.OwnerID = 1
	created, err := f.q.CreateEnvProvider(f.ctx, p)
	require.NoError(t, err)
	return created
}

// resolveEnv flattens sceneenv.Resolve into a map for assertions.
func resolveEnv(t *testing.T, q *store.Queries, wsID int64) map[string]string {
	t.Helper()
	rows, err := sceneenv.Resolve(context.Background(), q, wsID)
	require.NoError(t, err)
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out
}

func jsonBaseURLs(t *testing.T, m map[string]string) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

// Scenario 1: a model added to the library can be chosen in the provider form,
// saved, and expands to the correct env for Claude, Codex and Qwen.
func TestE2E_ModelSelectionDrivesProviderEnvForThreeCLIs(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	lib := NewModelLibraryService(f.q)

	// "Add model" in the settings page.
	_, err := lib.Upsert(f.ctx, f.owner, "acme-chat", "Acme Chat", true)
	require.NoError(t, err)

	// The provider form's combobox reads the dropdown (enabled rows only).
	dropdown, err := lib.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Contains(t, names(dropdown), "acme-chat")

	// Selecting it writes the string into the provider and saves.
	provider := f.newProvider(t, "e2e-multi-protocol", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{
			"anthropic": "https://anthropic.example/",
			"openai":    "https://openai.example/v1",
			"codex":     "https://codex.example/v1",
		}),
		ApiKey:        "sk-e2e",
		Model:         "acme-chat",
		CodexModel:    "acme-codex",
		HaikuModel:    "acme-haiku",
		ContextWindow: 0,
	})

	// One provider, one model library, three CLIs.
	claudeWS := f.newWorkspace(t, "claude", provider.ID)
	claudeEnv := resolveEnv(t, f.q, claudeWS.ID)
	require.Equal(t, "https://anthropic.example/", claudeEnv["ANTHROPIC_BASE_URL"])
	require.Equal(t, "acme-chat", claudeEnv["ANTHROPIC_MODEL"])
	require.Equal(t, "acme-haiku", claudeEnv["ANTHROPIC_DEFAULT_HAIKU_MODEL"])
	require.Equal(t, "sk-e2e", claudeEnv["ANTHROPIC_AUTH_TOKEN"])

	codexWS := f.newWorkspace(t, "codex", provider.ID)
	codexEnv := resolveEnv(t, f.q, codexWS.ID)
	require.Equal(t, "https://codex.example/v1", codexEnv["OPENAI_BASE_URL"])
	require.Equal(t, "acme-codex", codexEnv["OPENAI_MODEL"])
	require.Equal(t, "acme-codex", codexEnv["NIUNIU_MODEL"])
	require.Equal(t, "sk-e2e", codexEnv["OPENAI_API_KEY"])

	qwenWS := f.newWorkspace(t, "qwen", provider.ID)
	qwenEnv := resolveEnv(t, f.q, qwenWS.ID)
	require.Equal(t, "https://openai.example/v1", qwenEnv["OPENAI_BASE_URL"])
	require.Equal(t, "acme-chat", qwenEnv["OPENAI_MODEL"])
	require.NotContains(t, qwenEnv, "NIUNIU_MODEL", "only codex reads the niuniu control key")
}

// Scenario 2: typing a name that is not in the library, then "add to library",
// makes it available on the next dropdown read — and the saved provider keeps
// working with it.
func TestE2E_ManualModelEntryJoinsLibrary(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	lib := NewModelLibraryService(f.q)

	before, err := lib.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.NotContains(t, names(before), "hand-typed-model")

	// The "加入模型库" action is the same owner-scoped upsert.
	added, err := lib.Upsert(f.ctx, f.owner, "hand-typed-model", "", true)
	require.NoError(t, err)
	require.Equal(t, "manual", added.Source)

	after, err := lib.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Contains(t, names(after), "hand-typed-model")

	provider := f.newProvider(t, "e2e-manual", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{"anthropic": "https://anthropic.example/"}),
		ApiKey:   "sk-e2e",
		Model:    "hand-typed-model",
	})
	ws := f.newWorkspace(t, "claude", provider.ID)
	require.Equal(t, "hand-typed-model", resolveEnv(t, f.q, ws.ID)["ANTHROPIC_MODEL"])
}

// Scenario 3: refresh preview marks what is already in the library, import adds
// only the selected new names, and an endpoint that does not serve /models
// reports the manual-entry error.
func TestE2E_RefreshPreviewImportAndUnsupportedProvider(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	lib := NewModelLibraryService(f.q)
	refresh := NewModelRefreshService(f.q, lib)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1"},{"id":"acme-refreshed"}]}`))
	}))
	t.Cleanup(upstream.Close)

	provider := f.newProvider(t, "e2e-refresh", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{"openai": upstream.URL}),
		ApiKey:   "sk-e2e",
	})
	preview, err := refresh.RefreshPreview(f.ctx, f.owner, provider.ID)
	require.NoError(t, err)
	already := map[string]bool{}
	for _, c := range preview {
		already[c.Name] = c.AlreadyInLibrary
	}
	require.True(t, already["glm-5.1"], "builtin is already in the library")
	require.False(t, already["acme-refreshed"])

	// The user checks only the new name.
	added, skipped, err := lib.Import(f.ctx, f.owner, []string{"acme-refreshed"})
	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, 0, skipped)

	dropdown, err := lib.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Contains(t, names(dropdown), "acme-refreshed")

	// A provider whose endpoints answer 404 for /models gets a clear "enter
	// names manually" signal instead of a generic failure.
	noModels := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(noModels.Close)
	broken := f.newProvider(t, "e2e-no-models", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{"openai": noModels.URL}),
	})
	_, err = refresh.RefreshPreview(f.ctx, f.owner, broken.ID)
	require.ErrorIs(t, err, ErrModelsEndpointUnsupported)
}

// Scenario 4: a turn from each of the three CLIs lands in model_usage_hourly
// under every dimension the aggregation exposes.
func TestE2E_ThreeCLIUsageAttribution(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	usage := NewModelUsageService(f.q)

	providerA := f.newProvider(t, "e2e-usage-a", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{"anthropic": "https://a.example/"}),
	})
	providerB := f.newProvider(t, "e2e-usage-b", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{"openai": "https://b.example/v1"}),
	})

	type turn struct {
		cli      string
		model    string
		provider int64
		tokens   int64
	}
	turns := []turn{
		{"claude", "glm-5.1", providerA.ID, 100},
		{"codex", "acme-codex", providerB.ID, 200},
		{"qwen", "acme-chat", providerB.ID, 300},
	}
	workspaces := map[string]int64{}
	now := time.Now().UTC()
	for _, tr := range turns {
		ws := f.newWorkspace(t, tr.cli, tr.provider)
		workspaces[tr.cli] = ws.ID
		require.Equal(t, f.projID, f.mustProjectForWorkspace(t, ws.ID),
			"workspace -> issue -> column -> project must resolve")
		require.NoError(t, f.q.UpsertModelUsageHourly(f.ctx, store.UpsertModelUsageHourlyParams{
			OwnerType: "user", OwnerID: 1,
			ProjectID: f.projID, WorkspaceID: ws.ID,
			AgentCli: tr.cli, ModelName: tr.model, ProviderID: tr.provider,
			BucketHour: now, InputTokens: tr.tokens, OutputTokens: 10,
			InteractionCount: 1,
		}))
	}

	from := now.Add(-24 * time.Hour)
	to := now.Add(24 * time.Hour)

	total, err := usage.Summary(f.ctx, f.owner, from, to, "")
	require.NoError(t, err)
	require.EqualValues(t, 3*(10)+600, total.TotalTokens)
	require.EqualValues(t, 3, total.Requests)

	expectKeys := func(rows []ModelUsageRow) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.Key
		}
		return out
	}

	byCLI, err := usage.Summary(f.ctx, f.owner, from, to, "cli")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"claude", "codex", "qwen"}, expectKeys(byCLI.ByCLI))

	byModel, err := usage.Summary(f.ctx, f.owner, from, to, "model")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"glm-5.1", "acme-codex", "acme-chat"}, expectKeys(byModel.ByModel))

	byProvider, err := usage.Summary(f.ctx, f.owner, from, to, "provider")
	require.NoError(t, err)
	require.Len(t, byProvider.ByProvider, 2)

	byProject, err := usage.Summary(f.ctx, f.owner, from, to, "project")
	require.NoError(t, err)
	require.Len(t, byProject.ByProject, 1)
	require.Equal(t, strconv.FormatInt(f.projID, 10), byProject.ByProject[0].Key)

	byWorkspace, err := usage.Summary(f.ctx, f.owner, from, to, "workspace")
	require.NoError(t, err)
	require.Len(t, byWorkspace.ByWorkspace, 3)
}

// Scenario 5: an owner over the monthly hard cap cannot start a session and is
// notified; raising the cap restores normal operation.
func TestE2E_OwnerBudgetBlocksNewSessionThenRecovers(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	hub := notify.NewNotificationHub()
	t.Cleanup(hub.Stop)
	budget := NewModelBudgetService(f.q, hub)

	// Subscribe as the owner so the hub's owner filtering lets the event through.
	events := hub.Register("e2e-window", 1, nil)

	ws := f.newWorkspace(t, "claude", 0)
	require.NoError(t, budget.Set(f.ctx, f.owner, 1000))
	require.NoError(t, f.q.UpsertModelUsageHourly(f.ctx, store.UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: 1, ProjectID: f.projID, WorkspaceID: ws.ID,
		AgentCli: "claude", ModelName: "glm-5.1", BucketHour: time.Now().UTC(),
		InputTokens: 1000, InteractionCount: 1,
	}))

	proxy := agentproxy.NewAgentProxy(f.q, &config.Config{})
	t.Cleanup(proxy.Stop)
	proxy.SetModelBudgetChecker(budget)

	_, err := proxy.GetOrStartSession(f.ctx, ws.ID, 1)
	require.Error(t, err)
	require.ErrorIs(t, err, agentproxy.ErrModelBudgetExceeded)

	select {
	case raw := <-events:
		var n struct {
			Topic  string `json:"topic"`
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &n))
		require.Equal(t, notify.TopicModelBudget, n.Topic)
		require.Equal(t, "exceeded", n.Action)
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not receive the budget-exceeded notification")
	}

	// Raising the cap lifts the block immediately (no restart, no new turn loop).
	require.NoError(t, budget.Set(f.ctx, f.owner, 5000))
	sess, err := proxy.GetOrStartSession(f.ctx, ws.ID, 1)
	require.NoError(t, err)
	require.NotNil(t, sess)
}

// Scenario 6: a project soft cap warns but never blocks.
func TestE2E_ProjectSoftCapWarnsAndAllows(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	hub := notify.NewNotificationHub()
	t.Cleanup(hub.Stop)
	budget := NewModelBudgetService(f.q, hub)
	events := hub.Register("e2e-project-window", 1, nil)

	ws := f.newWorkspace(t, "claude", 0)
	require.NoError(t, budget.SetProject(f.ctx, f.owner, f.projID, 100))
	require.NoError(t, f.q.UpsertModelUsageHourly(f.ctx, store.UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: 1, ProjectID: f.projID, WorkspaceID: ws.ID,
		AgentCli: "claude", ModelName: "glm-5.1", BucketHour: time.Now().UTC(),
		InputTokens: 150, InteractionCount: 1,
	}))

	dec, err := budget.Check(f.ctx, "user", 1, f.projID)
	require.NoError(t, err)
	require.True(t, dec.Allowed, "soft cap must not block")
	require.True(t, dec.ProjectWarning)

	select {
	case raw := <-events:
		var n struct {
			Topic  string `json:"topic"`
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &n))
		require.Equal(t, notify.TopicModelBudget, n.Topic)
		require.Equal(t, "project_warning", n.Action)
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not receive the project soft-cap warning")
	}

	// The real session gate also allows the new session.
	proxy := agentproxy.NewAgentProxy(f.q, &config.Config{})
	t.Cleanup(proxy.Stop)
	proxy.SetModelBudgetChecker(budget)
	sess, err := proxy.GetOrStartSession(f.ctx, ws.ID, 1)
	require.NoError(t, err)
	require.NotNil(t, sess)
}

// Scenario 7: with the model library untouched, provider/env resolution is
// byte-identical to the pre-feature behaviour — and changing the library (even
// dropping the table) cannot alter what sceneenv resolves.
func TestE2E_LibraryDoesNotAffectRuntimeEnv(t *testing.T) {
	f := newModelLibraryE2EFixture(t)
	lib := NewModelLibraryService(f.q)

	provider := f.newProvider(t, "e2e-baseline", store.CreateEnvProviderParams{
		BaseUrls: jsonBaseURLs(t, map[string]string{
			"anthropic": "https://anthropic.example/",
			"openai":    "https://openai.example/v1",
		}),
		ApiKey:      "sk-e2e",
		Model:       "glm-5.1",
		SonnetModel: "glm-5.1",
		ExtraEnv:    `{"NIUNIU_EXTRA":"1"}`,
	})
	ws := f.newWorkspace(t, "claude", provider.ID)

	baseline := resolveEnv(t, f.q, ws.ID)
	require.Equal(t, map[string]string{
		"ANTHROPIC_BASE_URL":             "https://anthropic.example/",
		"ANTHROPIC_AUTH_TOKEN":           "sk-e2e",
		"ANTHROPIC_MODEL":                "glm-5.1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.1",
		"NIUNIU_EXTRA":                   "1",
	}, baseline)

	// Library edits: add, rename, disable, delete.
	sysList, err := lib.List(f.ctx, f.owner, true)
	require.NoError(t, err)
	var builtinID int64
	for _, m := range sysList {
		if m.Name == "glm-5.1" && m.OwnerID == 0 {
			builtinID = m.ID
		}
	}
	require.NotZero(t, builtinID)
	_, err = lib.Upsert(f.ctx, f.owner, "e2e-transient", "", true)
	require.NoError(t, err)
	_, err = lib.Upsert(f.ctx, f.owner, "glm-5.1", "Hidden", false)
	require.NoError(t, err)

	afterEdits := resolveEnv(t, f.q, ws.ID)
	require.Equal(t, baseline, afterEdits,
		"hiding a library name must not change the env of a saved provider")

	// Structural proof: ExpandProvider has no access path to the library.
	ft := reflect.TypeOf(sceneenv.ExpandProvider)
	require.Equal(t, 4, ft.NumIn(), "ExpandProvider(provider, cliType, accounts, preserveRef)")
	for i := 0; i < ft.NumIn(); i++ {
		require.NotContains(t, ft.In(i).String(), "Queries", "runtime expansion must not query the DB")
	}

	// Strongest form: drop the table entirely; runtime env is unaffected.
	_, err = f.db.ExecContext(f.ctx, `DROP TABLE models`)
	require.NoError(t, err)
	require.Equal(t, baseline, resolveEnv(t, f.q, ws.ID),
		"runtime resolution must not read the model library")
}

// Scenario 8: the feature adds no listener or proxy process — it reuses the
// existing spawn path. Enforced by scanning every file the feature introduced.
func TestE2E_NoNewProxyOrListeningPort(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(thisFile)

	files := []string{
		"model_library.go",
		"model_refresh.go",
		"model_usage.go",
		"model_budget.go",
		"model_seed.go",
		filepath.Join("..", "agentproxy", "model_budget.go"),
		filepath.Join("..", "agentproxy", "model_usage.go"),
		filepath.Join("..", "api", "models.go"),
	}
	forbidden := []string{
		"net.Listen",
		"ListenTCP",
		"ListenAndServe",
		"ReverseProxy",
		"NewSingleHostReverseProxy",
		"httputil",
	}
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "feature file %s must exist", name)
		for _, bad := range forbidden {
			require.NotContains(t, string(body), bad,
				"%s must not open a socket or act as a proxy (%s)", name, bad)
		}
	}
}

func (f *e2eFixture) mustProjectForWorkspace(t *testing.T, wsID int64) int64 {
	t.Helper()
	id, err := f.q.GetProjectIDForWorkspace(f.ctx, wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	require.NoError(t, err)
	return id
}
