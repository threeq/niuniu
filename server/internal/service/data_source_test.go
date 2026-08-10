// External-tests for DataSourceService — exercise the encrypt-on-create /
// decrypt-on-LoadConnConfig round-trip, list redaction, owner-scope filtering,
// the M1 unsupported-kind gate, and update-preserves-config behavior.
package service_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func newTestDataSourceService(t *testing.T) (*service.DataSourceService, *niutest.IsolationEnv) {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := dataconn.NewRegistry()
	svc := service.NewDataSourceService(env.Queries(), env.DB, kr, reg)
	return svc, env
}

func TestDataSourceEncryptRoundtrip(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "secret", "database": "analytics"},
		ScopeConfig:       map[string]any{"databases": []any{"analytics"}, "tables_allow": []any{"orders"}},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// list must redact password
	list, err := svc.ListForOwner(ctx, uid, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if _, ok := list[0].Config["password"]; ok {
		t.Fatal("password must be redacted in list")
	}
	if list[0].Config["host"] != "db" {
		t.Fatalf("redacted config should still carry host, got %v", list[0].Config)
	}
	if list[0].ScopeConfig["databases"] == nil {
		t.Fatalf("scope_config should be parsed, got %v", list[0].ScopeConfig)
	}

	// decrypted load returns password
	cc, err := svc.LoadConnConfig(ctx, id, uid, nil)
	if err != nil || cc.Password != "secret" {
		t.Fatalf("decrypt: %+v %v", cc, err)
	}
	if cc.Port != 3306 || cc.Host != "db" || cc.Kind != dataconn.KindMySQL {
		t.Fatalf("connconfig mismatch: %+v", cc)
	}
}

// newWorkspaceInProject builds a workspace -> issue -> column -> project chain
// so GetProjectIDForWorkspace resolves, returning the workspace row and project.
func newWorkspaceInProject(t *testing.T, env *niutest.IsolationEnv, uid int64, name string) (store.Workspace, store.Project) {
	t.Helper()
	ctx := context.Background()
	proj := env.NewProject(t, uid, name+"-proj")
	col := env.NewColumn(t, proj.ID, "todo", "todo")
	issue := env.SeedExternalIssue(t, col.ID, "github", name)
	ws, err := env.Queries().CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      name,
		Path:      "/tmp/" + name,
		Status:    "created",
		OwnerType: "user",
		OwnerID:   uid,
		CreatedBy: sql.NullInt64{Int64: uid, Valid: true},
		CliType:   "claude",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace(%s): %v", name, err)
	}
	return ws, proj
}

// TestDataSourceBindingsVisibility locks in the project-only rule: a data
// source is visible to an agent ONLY through the project its workspace belongs
// to (like external sources). Workspace-direct bindings are rejected; a source
// bound to one project is invisible to a workspace in another project.
func TestDataSourceBindingsVisibility(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	ws, proj := newWorkspaceInProject(t, env, uid, "viz")

	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "p", "database": "a"},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	refID := strconv.FormatInt(id, 10)

	// Unbound: invisible to the workspace, and not resolvable by id or name.
	if vis, err := svc.ListForWorkspace(ctx, ws, uid, nil); err != nil || len(vis) != 0 {
		t.Fatalf("unbound source must be invisible, got %d (%v)", len(vis), err)
	}
	if _, err := svc.ResolveSourceIDForWorkspace(ctx, refID, ws, uid, nil); !errors.Is(err, service.ErrDataSourceNotFound) {
		t.Fatalf("unbound numeric id must not resolve, got %v", err)
	}

	// Workspace-direct binding is rejected (project-only).
	if err := svc.SetBindings(ctx, id, uid, nil, []service.DataSourceBinding{
		{TargetType: "workspace", TargetID: ws.ID},
	}); err == nil {
		t.Fatal("workspace-direct binding must be rejected in project-only mode")
	}

	// Bind to the workspace's PROJECT -> visible + resolvable.
	if err := svc.SetBindings(ctx, id, uid, nil, []service.DataSourceBinding{
		{TargetType: "project", TargetID: proj.ID},
	}); err != nil {
		t.Fatalf("SetBindings(project): %v", err)
	}
	if vis, err := svc.ListForWorkspace(ctx, ws, uid, nil); err != nil || len(vis) != 1 || vis[0].ID != id {
		t.Fatalf("project-bound source must be visible to its workspace, got %v (%v)", vis, err)
	}
	if got, err := svc.ResolveSourceIDForWorkspace(ctx, "analytics", ws, uid, nil); err != nil || got != id {
		t.Fatalf("project-bound name must resolve: %d %v", got, err)
	}

	// A workspace in a DIFFERENT project cannot see it.
	ws2, _ := newWorkspaceInProject(t, env, uid, "other")
	if vis, _ := svc.ListForWorkspace(ctx, ws2, uid, nil); len(vis) != 0 {
		t.Fatalf("source must not leak to a workspace in another project, got %d", len(vis))
	}
}

// TestDataSourceProjectAssociation covers the project-settings association
// path: AddBinding (idempotent), ListForProject, and RemoveBinding.
func TestDataSourceProjectAssociation(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA
	const pid = 555

	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "p", "database": "a"},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got, err := svc.ListForProject(ctx, pid, uid, nil); err != nil || len(got) != 0 {
		t.Fatalf("project should start with no sources, got %d (%v)", len(got), err)
	}

	pb := service.DataSourceBinding{TargetType: "project", TargetID: pid}
	if err := svc.AddBinding(ctx, id, uid, nil, pb); err != nil {
		t.Fatalf("AddBinding: %v", err)
	}
	// Idempotent: a second add must not error or duplicate.
	if err := svc.AddBinding(ctx, id, uid, nil, pb); err != nil {
		t.Fatalf("AddBinding (idempotent): %v", err)
	}
	if got, err := svc.ListForProject(ctx, pid, uid, nil); err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("project should list the bound source once, got %v (%v)", got, err)
	}

	if err := svc.RemoveBinding(ctx, id, uid, nil, pb); err != nil {
		t.Fatalf("RemoveBinding: %v", err)
	}
	if got, _ := svc.ListForProject(ctx, pid, uid, nil); len(got) != 0 {
		t.Fatalf("project should have no sources after removal, got %d", len(got))
	}
}

// TestCreateForWorkspace covers the MCP create_data_source path: a source
// created for a workspace is owned by the workspace owner, auto-bound to the
// workspace's project, and therefore immediately visible/resolvable to the
// workspace agent. A workspace with no project linkage is rejected.
func TestCreateForWorkspace(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	ws, _ := newWorkspaceInProject(t, env, uid, "viz")

	dto, err := svc.CreateForWorkspace(ctx, ws, uid, service.CreateDataSourceInput{
		Name: "petstore", Kind: "http",
		Config: map[string]any{
			"host":    "api.example.com",
			"options": map[string]any{"scheme": "https", "base_path": "/v1", "headers": map[string]any{"Authorization": "Bearer tok"}},
		},
		ScopeConfig:       map[string]any{"tables_allow": []any{"/pets"}},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("CreateForWorkspace: %v", err)
	}
	if dto.Kind != "http" || dto.OwnerType != ws.OwnerType || dto.OwnerID != ws.OwnerID {
		t.Fatalf("owner/kind not taken from workspace: %+v", dto)
	}
	// headers (secret) must be redacted from the DTO; scheme/base_path echoed.
	opts, _ := dto.Config["options"].(map[string]any)
	if _, leaked := opts["headers"]; leaked {
		t.Fatalf("headers must be redacted in DTO, got %v", dto.Config)
	}
	if opts["scheme"] != "https" || opts["base_path"] != "/v1" {
		t.Fatalf("non-secret options should be echoed: %v", opts)
	}

	// Auto-bound to project -> visible + resolvable to the workspace agent.
	vis, err := svc.ListForWorkspace(ctx, ws, uid, nil)
	if err != nil || len(vis) != 1 || vis[0].ID != dto.ID {
		t.Fatalf("created source must be visible to its workspace, got %v (%v)", vis, err)
	}
	if got, err := svc.ResolveSourceIDForWorkspace(ctx, "petstore", ws, uid, nil); err != nil || got != dto.ID {
		t.Fatalf("created source must resolve by name: %d %v", got, err)
	}
	// Decrypted config round-trips the secret header.
	cc, err := svc.LoadConnConfig(ctx, dto.ID, uid, nil)
	if err != nil {
		t.Fatalf("LoadConnConfig: %v", err)
	}
	hdrs, _ := cc.Options["headers"].(map[string]any)
	if hdrs["Authorization"] != "Bearer tok" {
		t.Fatalf("secret header should round-trip: %+v", cc.Options)
	}
}

func TestCreateForWorkspaceNoProject(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	// A workspace with no issue -> no project linkage.
	ws, err := env.Queries().CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "loose", Path: "/tmp/loose", Status: "created",
		OwnerType: "user", OwnerID: uid,
		CreatedBy: sql.NullInt64{Int64: uid, Valid: true}, CliType: "claude",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	_, err = svc.CreateForWorkspace(ctx, ws, uid, service.CreateDataSourceInput{
		Name: "x", Kind: "http", Config: map[string]any{"host": "h"},
	})
	if !errors.Is(err, service.ErrWorkspaceNoProject) {
		t.Fatalf("expected ErrWorkspaceNoProject, got %v", err)
	}
}

func TestDataSourceUnsupportedKind(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	_, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "queue", Kind: "kafka",
		Config: map[string]any{"host": "k", "port": 9092},
	})
	if err == nil {
		t.Fatal("expected unsupported-kind error for kafka")
	}
	if !errors.Is(err, dataconn.ErrUnsupported) {
		t.Fatalf("error must wrap dataconn.ErrUnsupported, got %v", err)
	}
}

func TestDataSourceOwnerScoping(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()

	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: env.UserA, UserID: env.UserA, Name: "a-src", Kind: "postgres",
		Config: map[string]any{"host": "h", "port": 5432, "password": "p"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// UserB cannot see or load UserA's source.
	if list, err := svc.ListForOwner(ctx, env.UserB, nil); err != nil || len(list) != 0 {
		t.Fatalf("UserB should see no sources, got %v %v", list, err)
	}
	if _, err := svc.Get(ctx, id, env.UserB, nil); !errors.Is(err, service.ErrDataSourceNotFound) {
		t.Fatalf("UserB Get should be not-found, got %v", err)
	}
	if _, err := svc.LoadConnConfig(ctx, id, env.UserB, nil); !errors.Is(err, service.ErrDataSourceNotFound) {
		t.Fatalf("UserB LoadConnConfig should be not-found, got %v", err)
	}
	if err := svc.Delete(ctx, id, env.UserB, nil); !errors.Is(err, service.ErrDataSourceNotFound) {
		t.Fatalf("UserB Delete should be not-found, got %v", err)
	}
}

func TestDataSourceUpdatePreservesConfig(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "src", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "password": "secret", "database": "d"},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update name + scope only (Config nil) -> password must survive.
	if err := svc.Update(ctx, id, uid, nil, service.UpdateDataSourceInput{
		Name:              "src-renamed",
		Config:            nil,
		ScopeConfig:       map[string]any{"databases": []any{"d"}},
		DefaultAccessMode: "read", RequireConfirm: "always",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cc, err := svc.LoadConnConfig(ctx, id, uid, nil)
	if err != nil || cc.Password != "secret" {
		t.Fatalf("password must survive a config-less update: %+v %v", cc, err)
	}
	got, err := svc.Get(ctx, id, uid, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "src-renamed" || got.RequireConfirm != "always" {
		t.Fatalf("update did not apply: %+v", got)
	}

	// Update with a new Config -> password changes.
	if err := svc.Update(ctx, id, uid, nil, service.UpdateDataSourceInput{
		Name:              "src-renamed",
		Config:            map[string]any{"host": "db", "port": 3306, "password": "newpass", "database": "d"},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	}); err != nil {
		t.Fatalf("Update#2: %v", err)
	}
	cc, err = svc.LoadConnConfig(ctx, id, uid, nil)
	if err != nil || cc.Password != "newpass" {
		t.Fatalf("password should update when Config provided: %+v %v", cc, err)
	}
}

func TestDataSourceDuplicateName(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	in := service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "dup", Kind: "mysql",
		Config: map[string]any{"host": "h"},
	}
	if _, err := svc.Create(ctx, in); err != nil {
		t.Fatalf("Create#1: %v", err)
	}
	if _, err := svc.Create(ctx, in); !errors.Is(err, service.ErrDataSourceNameTaken) {
		t.Fatalf("Create#2 should clash, got %v", err)
	}
}

func TestDataSourceNewKindsAccepted(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	newKinds := []string{
		"clickhouse", "mssql",
		"mariadb", "tidb", "oceanbase", "starrocks", "doris",
		"cockroachdb", "greenplum", "redshift", "opengauss", "polardbpg", "yugabyte",
	}
	for _, kind := range newKinds {
		t.Run(kind, func(t *testing.T) {
			_, err := svc.Create(ctx, service.CreateDataSourceInput{
				OwnerType: "user", OwnerID: uid, UserID: uid,
				Name:   "src-" + kind,
				Kind:   kind,
				Config: map[string]any{"host": "h", "port": 0, "password": "p"},
			})
			if err != nil {
				t.Fatalf("kind %s should be accepted, got: %v", kind, err)
			}
		})
	}
}

// Epic #345 Wave2 (#354) opened redis/mongo/trino/elasticsearch in the
// registry: creating such a source now succeeds (the connector itself stays a
// stub until Wave3). Kinds with no registry entry are still rejected.
func TestDataSourceNonSQLKinds(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	for _, kind := range []string{"redis", "mongo", "trino", "elasticsearch"} {
		t.Run(kind, func(t *testing.T) {
			if _, err := svc.Create(ctx, service.CreateDataSourceInput{
				OwnerType: "user", OwnerID: uid, UserID: uid,
				Name:   "new-" + kind,
				Kind:   kind,
				Config: map[string]any{"host": "h"},
			}); err != nil {
				t.Fatalf("kind %s should be accepted after Wave2, got %v", kind, err)
			}
		})
	}

	for _, kind := range []string{"kafka", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			_, err := svc.Create(ctx, service.CreateDataSourceInput{
				OwnerType: "user", OwnerID: uid, UserID: uid,
				Name:   "bad-" + kind,
				Kind:   kind,
				Config: map[string]any{"host": "h"},
			})
			if err == nil {
				t.Fatalf("kind %s should be rejected", kind)
			}
			if !errors.Is(err, dataconn.ErrUnsupported) {
				t.Fatalf("error must wrap ErrUnsupported for %s, got %v", kind, err)
			}
		})
	}
}

// TestDataSourceUpdateMergesSecrets locks the "leave blank to keep" contract:
// an edit-form config with an empty/missing password (and an empty api_key)
// inherits the stored secret instead of wiping it, an options-free config
// inherits the stored options wholesale, and an explicit options set drops an
// absent api_key (the switch-to-basic-auth signal) while inheriting scheme.
func TestDataSourceUpdateMergesSecrets(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	ctx := context.Background()
	uid := env.UserA

	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "es", Kind: "elasticsearch",
		Config: map[string]any{
			"host": "es1", "port": 9200,
			"options": map[string]any{"api_key": "K1", "scheme": "https"},
		},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Redacted DTO: non-secret options echoed, api_key stripped.
	dto, err := svc.Get(ctx, id, uid, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	opts, _ := dto.Config["options"].(map[string]any)
	if opts["scheme"] != "https" {
		t.Fatalf("non-secret option should be echoed, got %v", dto.Config)
	}
	if _, leaked := opts["api_key"]; leaked {
		t.Fatal("api_key must be redacted in DTO")
	}

	// Empty api_key = keep marker; scheme inherited when absent.
	err = svc.Update(ctx, id, uid, nil, service.UpdateDataSourceInput{
		Name:   "es",
		Config: map[string]any{"host": "es2", "port": 9200, "options": map[string]any{"api_key": ""}},
	})
	if err != nil {
		t.Fatalf("Update keep: %v", err)
	}
	cc, err := svc.LoadConnConfig(ctx, id, uid, nil)
	if err != nil {
		t.Fatalf("LoadConnConfig: %v", err)
	}
	if cc.Host != "es2" || cc.Options["api_key"] != "K1" || cc.Options["scheme"] != "https" {
		t.Fatalf("keep-marker merge failed: %+v", cc)
	}

	// Absent api_key with explicit options = drop (basic-auth switch).
	err = svc.Update(ctx, id, uid, nil, service.UpdateDataSourceInput{
		Name:   "es",
		Config: map[string]any{"host": "es2", "user": "u", "password": "pw", "options": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("Update basic: %v", err)
	}
	cc, _ = svc.LoadConnConfig(ctx, id, uid, nil)
	if _, kept := cc.Options["api_key"]; kept {
		t.Fatalf("absent api_key should drop the stored one: %+v", cc.Options)
	}
	if cc.Options["scheme"] != "https" || cc.Password != "pw" {
		t.Fatalf("scheme should survive, password should update: %+v", cc)
	}

	// Empty password + no options key = inherit both wholesale.
	err = svc.Update(ctx, id, uid, nil, service.UpdateDataSourceInput{
		Name:   "es",
		Config: map[string]any{"host": "es3", "user": "u"},
	})
	if err != nil {
		t.Fatalf("Update rename-host: %v", err)
	}
	cc, _ = svc.LoadConnConfig(ctx, id, uid, nil)
	if cc.Host != "es3" || cc.Password != "pw" || cc.Options["scheme"] != "https" {
		t.Fatalf("blank-password merge failed: %+v", cc)
	}
}
