// External tests for DataProxyService — exercise the three-layer gate matrix
// (read in-scope passes; out-of-scope read -> scope_denied; write on read-only
// -> write_disabled; require_confirm=always routes through the permission
// backend). A fake connector (in-memory SQLite) is registered under KindMySQL
// so Execute runs without a live database; Classify reuses the real SQL
// classifier so read/write + touched-object parsing is genuine.
package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// fakeConnector classifies via the real SQL classifier (so read/write + object
// parsing is exercised for real) but executes against an in-memory SQLite DB.
type fakeConnector struct {
	real dataconn.Connector // for Classify
	db   *sql.DB
}

func (f *fakeConnector) Kind() dataconn.SourceKind { return dataconn.KindMySQL }
func (f *fakeConnector) Ping(ctx context.Context, conn dataconn.ConnConfig) error {
	return f.db.PingContext(ctx)
}
func (f *fakeConnector) Classify(op dataconn.Operation) (dataconn.AccessMode, dataconn.ResourceRef, error) {
	return f.real.Classify(op)
}
func (f *fakeConnector) Execute(ctx context.Context, conn dataconn.ConnConfig, op dataconn.Operation) (*dataconn.ResultSet, error) {
	rows, err := f.db.QueryContext(ctx, op.Statement, op.Params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	rs := &dataconn.ResultSet{Engine: "mysql"}
	for _, c := range cols {
		rs.Columns = append(rs.Columns, dataconn.Column{Name: c, Type: "string"})
	}
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rs.Rows = append(rs.Rows, holders)
	}
	return rs, rows.Err()
}

// fakePermission records the last Request and returns a programmed AllowResp.
type fakePermission struct {
	resp   service.AllowResp
	called int
	lastIn map[string]any
}

func (p *fakePermission) Request(ctx context.Context, workspaceID int64, ownerType string, ownerID int64,
	sessionID, toolName string, toolInput map[string]any) (service.AllowResp, error) {
	p.called++
	p.lastIn = toolInput
	return p.resp, nil
}

type proxyDeps struct {
	svc          *service.DataProxyService
	readSourceID int64
	perm         *fakePermission
	uid          int64
	db           *sql.DB
}

func newTestDataProxy(t *testing.T, requireConfirm string, accessMode string) proxyDeps {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := dataconn.NewRegistry()

	// Register a fake mysql connector backed by in-memory SQLite.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE orders(id INTEGER, amt REAL); INSERT INTO orders VALUES (1,10.5),(2,20.0)`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	reg.Register(dataconn.KindMySQL, &fakeConnector{real: dataconn.NewMySQLConnector(), db: db})

	dsSvc := service.NewDataSourceService(env.Queries(), env.DB, kr, reg)
	uid := env.UserA
	id, err := dsSvc.Create(context.Background(), service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "secret", "database": "analytics"},
		ScopeConfig:       map[string]any{"databases": []any{}, "tables_allow": []any{"orders"}},
		DefaultAccessMode: accessMode, RequireConfirm: requireConfirm,
	})
	if err != nil {
		t.Fatalf("Create source: %v", err)
	}

	perm := &fakePermission{resp: service.AllowResp{Behavior: "allow"}}
	svc := service.NewDataProxyServiceWithSeams(dsSvc, reg, perm, env.Queries())
	return proxyDeps{svc: svc, readSourceID: id, perm: perm, uid: uid, db: env.DB}
}

func TestDataProxyGate(t *testing.T) {
	deps := newTestDataProxy(t, "writes_only", "read")
	ctx := context.Background()
	base := func(stmt string) service.DataQueryInput {
		return service.DataQueryInput{
			WorkspaceID: 1, OwnerType: "user", OwnerID: deps.uid, UserID: deps.uid, SessionID: "s",
			SourceID: deps.readSourceID, Statement: stmt,
		}
	}

	// in-scope read -> passes silently (require_confirm=writes_only, read).
	out, err := deps.svc.Query(ctx, base("SELECT id FROM orders"))
	if err != nil || out == nil {
		t.Fatalf("in-scope read should pass: out=%v err=%v", out, err)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(out.Rows))
	}
	if deps.perm.called != 0 {
		t.Fatalf("read in-scope must not call permission, called=%d", deps.perm.called)
	}

	// out-of-scope read -> scope_denied.
	_, err = deps.svc.Query(ctx, base("SELECT * FROM secrets"))
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
		t.Fatalf("out-of-scope should be scope_denied, got %v", err)
	}

	// write on read-only source -> write_disabled (before any confirm).
	_, err = deps.svc.Query(ctx, base("DELETE FROM orders"))
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "write_disabled" {
		t.Fatalf("write on read-only should be write_disabled, got %v", err)
	}

	// C2: a backtick-quoted table not in tables_allow -> scope_denied (the
	// quoted ident must be extracted, not silently treated as in-scope).
	_, err = deps.svc.Query(ctx, base("SELECT * FROM `secret`"))
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
		t.Fatalf("backtick out-of-scope table should be scope_denied, got %v", err)
	}

	// C2: a CTE whose alias (t) is not in tables_allow but whose body reads an
	// allowed base table (orders) -> allowed (alias excluded from objects).
	out, err = deps.svc.Query(ctx, base("WITH t AS (SELECT id FROM orders) SELECT id FROM t"))
	if err != nil || out == nil {
		t.Fatalf("CTE reading an allowed base table should pass: out=%v err=%v", out, err)
	}

	// C2: a table-referencing statement whose object can't be parsed (FROM a
	// subquery, no captured ident) -> scope_denied (fail closed).
	_, err = deps.svc.Query(ctx, base("SELECT * FROM (SELECT 1) sub"))
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
		t.Fatalf("unparseable table reference should be scope_denied, got %v", err)
	}
}

// captureConnector records the Operation passed to Classify/Execute and
// returns a programmed classification — lets dispatch tests assert exactly
// what op the gateway constructed for each source kind without a live store
// (the schema kind CHECK doesn't admit elasticsearch until #354).
type captureConnector struct {
	kind dataconn.SourceKind
	mode dataconn.AccessMode
	ref  dataconn.ResourceRef
	got  dataconn.Operation
}

func (c *captureConnector) Kind() dataconn.SourceKind                                { return c.kind }
func (c *captureConnector) Ping(ctx context.Context, conn dataconn.ConnConfig) error { return nil }
func (c *captureConnector) Classify(op dataconn.Operation) (dataconn.AccessMode, dataconn.ResourceRef, error) {
	c.got = op
	return c.mode, c.ref, nil
}
func (c *captureConnector) Execute(ctx context.Context, conn dataconn.ConnConfig, op dataconn.Operation) (*dataconn.ResultSet, error) {
	return &dataconn.ResultSet{Engine: string(c.kind)}, nil
}

// classifyReal runs a real connector's Classify (so read/write mapping is
// genuine) with a no-op Execute — lets gate tests exercise real classification
// without a live backend.
type classifyReal struct {
	kind dataconn.SourceKind
	real dataconn.Connector
}

func (c *classifyReal) Kind() dataconn.SourceKind                                { return c.kind }
func (c *classifyReal) Ping(ctx context.Context, conn dataconn.ConnConfig) error { return nil }
func (c *classifyReal) Classify(op dataconn.Operation) (dataconn.AccessMode, dataconn.ResourceRef, error) {
	return c.real.Classify(op)
}
func (c *classifyReal) Execute(ctx context.Context, conn dataconn.ConnConfig, op dataconn.Operation) (*dataconn.ResultSet, error) {
	return &dataconn.ResultSet{Engine: string(c.kind)}, nil
}

// fakeSourceLoader satisfies the proxy's loader seam without data_sources rows.
type fakeSourceLoader struct {
	dto *service.DataSourceDTO
	cc  dataconn.ConnConfig
}

func (l *fakeSourceLoader) Get(ctx context.Context, id, userID int64, orgIDs []int64) (*service.DataSourceDTO, error) {
	return l.dto, nil
}
func (l *fakeSourceLoader) LoadConnConfig(ctx context.Context, id, userID int64, orgIDs []int64) (dataconn.ConnConfig, error) {
	return l.cc, nil
}
func (l *fakeSourceLoader) ResolveSourceID(ctx context.Context, nameOrID string, userID int64, orgIDs []int64) (int64, error) {
	return l.dto.ID, nil
}

// newKindProxy wires a DataProxyService for one fake source of the given kind.
func newKindProxy(kind dataconn.SourceKind, scope map[string]any, conn *captureConnector) *service.DataProxyService {
	reg := dataconn.NewRegistry()
	reg.Register(kind, conn)
	loader := &fakeSourceLoader{
		dto: &service.DataSourceDTO{
			ID: 1, Name: "src", Kind: string(kind),
			ScopeConfig: scope, DefaultAccessMode: "read", RequireConfirm: "writes_only",
		},
		cc: dataconn.ConnConfig{Kind: kind, Database: "defaultdb"},
	}
	perm := &fakePermission{resp: service.AllowResp{Behavior: "allow"}}
	return service.NewDataProxyServiceWithSeams(loader, reg, perm, nil)
}

func TestDataProxyDispatchByKind(t *testing.T) {
	ctx := context.Background()
	base := service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: 7, UserID: 7, SessionID: "s",
		SourceID: 1, RowLimit: 50,
	}

	t.Run("redis", func(t *testing.T) {
		conn := &captureConnector{kind: dataconn.KindRedis, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"cache:user:1"}, ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindRedis, map[string]any{"tables_allow": []any{"cache:"}}, conn)
		in := base
		in.Command, in.Args = "GET", []string{"cache:user:1"}
		if _, err := svc.Query(ctx, in); err != nil {
			t.Fatalf("redis dispatch failed: %v", err)
		}
		if conn.got.Command != "GET" || len(conn.got.Args) != 1 || conn.got.Args[0] != "cache:user:1" {
			t.Fatalf("redis op fields not forwarded: %+v", conn.got)
		}
		if conn.got.Database != "defaultdb" || conn.got.RowLimit != 50 || conn.got.TimeoutMS == 0 {
			t.Fatalf("server-injected constraints missing: %+v", conn.got)
		}
		if conn.got.Statement != "" {
			t.Fatalf("redis op must not carry a statement: %+v", conn.got)
		}
	})

	t.Run("mongo", func(t *testing.T) {
		conn := &captureConnector{kind: dataconn.KindMongo, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"orders"}, ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindMongo, map[string]any{"tables_allow": []any{"orders"}}, conn)
		in := base
		in.Collection, in.MongoOp = "orders", "find"
		in.Filter = map[string]any{"status": "paid"}
		if _, err := svc.Query(ctx, in); err != nil {
			t.Fatalf("mongo dispatch failed: %v", err)
		}
		if conn.got.Collection != "orders" || conn.got.MongoOp != "find" || conn.got.Filter["status"] != "paid" {
			t.Fatalf("mongo op fields not forwarded: %+v", conn.got)
		}
	})

	t.Run("elasticsearch", func(t *testing.T) {
		conn := &captureConnector{kind: dataconn.KindElasticsearch, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"orders"}, ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindElasticsearch, map[string]any{"tables_allow": []any{"orders"}}, conn)
		in := base
		in.Index, in.ESMethod, in.ESPath = "orders", "GET", "_search"
		in.Query = map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
		if _, err := svc.Query(ctx, in); err != nil {
			t.Fatalf("es dispatch failed: %v", err)
		}
		if conn.got.Index != "orders" || conn.got.ESMethod != "GET" || conn.got.ESPath != "_search" || conn.got.Query == nil {
			t.Fatalf("es op fields not forwarded: %+v", conn.got)
		}
	})

	t.Run("http", func(t *testing.T) {
		conn := &captureConnector{kind: dataconn.KindHTTP, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"/users"}, CommandCls: "GET", ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindHTTP, map[string]any{"tables_allow": []any{"/users"}}, conn)
		in := base
		in.HTTPMethod, in.HTTPPath = "GET", "/users"
		in.HTTPQuery = map[string]any{"active": true}
		in.HTTPListPath = "data.list"
		if _, err := svc.Query(ctx, in); err != nil {
			t.Fatalf("http dispatch failed: %v", err)
		}
		if conn.got.HTTPMethod != "GET" || conn.got.HTTPPath != "/users" || conn.got.HTTPQuery["active"] != true {
			t.Fatalf("http op fields not forwarded: %+v", conn.got)
		}
		if conn.got.HTTPListPath != "data.list" {
			t.Fatalf("http_list_path not forwarded: %+v", conn.got)
		}
		if conn.got.Statement != "" || conn.got.Index != "" {
			t.Fatalf("http op must not carry statement/es fields: %+v", conn.got)
		}
	})
}

// TestDataProxyHTTPGate covers the HTTP-specific gate behaviours: path-prefix
// scope denial, write_disabled on a read-only source, and a redacted summary on
// the permission card.
func TestDataProxyHTTPGate(t *testing.T) {
	ctx := context.Background()
	base := service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: 7, UserID: 7, SessionID: "s", SourceID: 1,
	}

	t.Run("path out of allowed prefix -> scope_denied", func(t *testing.T) {
		conn := &captureConnector{kind: dataconn.KindHTTP, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"/admin/secrets"}, CommandCls: "GET", ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindHTTP, map[string]any{"tables_allow": []any{"/users"}}, conn)
		in := base
		in.HTTPMethod, in.HTTPPath = "GET", "/admin/secrets"
		_, err := svc.Query(ctx, in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
			t.Fatalf("out-of-prefix path should be scope_denied, got %v", err)
		}
	})

	t.Run("denied prefix wins -> scope_denied", func(t *testing.T) {
		conn := &captureConnector{kind: dataconn.KindHTTP, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"/users/secret"}, CommandCls: "GET", ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindHTTP, map[string]any{
			"tables_allow": []any{"/users"}, "tables_deny": []any{"/users/secret"},
		}, conn)
		in := base
		in.HTTPMethod, in.HTTPPath = "GET", "/users/secret"
		_, err := svc.Query(ctx, in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
			t.Fatalf("denied prefix should win, got %v", err)
		}
	})

	t.Run("write on read-only source -> write_disabled", func(t *testing.T) {
		// DELETE is an unambiguous write (POST is a read for http).
		conn := &captureConnector{kind: dataconn.KindHTTP, mode: dataconn.ModeWrite,
			ref: dataconn.ResourceRef{Objects: []string{"/users/1"}, CommandCls: "DELETE", ReferencesTables: true}}
		svc := newKindProxy(dataconn.KindHTTP, map[string]any{"tables_allow": []any{"/users"}}, conn)
		in := base
		in.HTTPMethod, in.HTTPPath = "DELETE", "/users/1"
		_, err := svc.Query(ctx, in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "write_disabled" {
			t.Fatalf("write on read-only http source should be write_disabled, got %v", err)
		}
	})

	t.Run("GET and POST pass on a read-only source (POST is a read)", func(t *testing.T) {
		for _, method := range []string{"GET", "POST"} {
			reg := dataconn.NewRegistry()
			reg.Register(dataconn.KindHTTP, &classifyReal{kind: dataconn.KindHTTP, real: dataconn.NewHTTPConnector()})
			loader := &fakeSourceLoader{
				dto: &service.DataSourceDTO{ID: 1, Name: "api", Kind: "http",
					ScopeConfig:       map[string]any{"tables_allow": []any{"/search"}},
					DefaultAccessMode: "read", RequireConfirm: "writes_only"},
				cc: dataconn.ConnConfig{Kind: dataconn.KindHTTP},
			}
			svc := service.NewDataProxyServiceWithSeams(loader, reg, &fakePermission{resp: service.AllowResp{Behavior: "allow"}}, nil)
			in := base
			in.HTTPMethod, in.HTTPPath = method, "/search"
			if method == "POST" {
				in.HTTPBody = map[string]any{"q": "x"}
			}
			if _, err := svc.Query(ctx, in); err != nil {
				t.Fatalf("%s on a read-only http source should pass: %v", method, err)
			}
		}
	})

	t.Run("PUT is blocked on a read-only source (write)", func(t *testing.T) {
		reg := dataconn.NewRegistry()
		reg.Register(dataconn.KindHTTP, &classifyReal{kind: dataconn.KindHTTP, real: dataconn.NewHTTPConnector()})
		loader := &fakeSourceLoader{
			dto: &service.DataSourceDTO{ID: 1, Name: "api", Kind: "http",
				ScopeConfig:       map[string]any{"tables_allow": []any{"/items"}},
				DefaultAccessMode: "read", RequireConfirm: "writes_only"},
			cc: dataconn.ConnConfig{Kind: dataconn.KindHTTP},
		}
		svc := service.NewDataProxyServiceWithSeams(loader, reg, &fakePermission{resp: service.AllowResp{Behavior: "allow"}}, nil)
		in := base
		in.HTTPMethod, in.HTTPPath = "PUT", "/items/1"
		in.HTTPBody = map[string]any{"name": "x"}
		_, err := svc.Query(ctx, in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "write_disabled" {
			t.Fatalf("PUT on read-only http source should be write_disabled, got %v", err)
		}
	})

	t.Run("methods allow-list restricts verbs", func(t *testing.T) {
		// scope_config.methods = ["GET"] -> GET passes, POST denied (even though
		// POST is a read and the source is read-only).
		newSvc := func() *service.DataProxyService {
			reg := dataconn.NewRegistry()
			reg.Register(dataconn.KindHTTP, &classifyReal{kind: dataconn.KindHTTP, real: dataconn.NewHTTPConnector()})
			loader := &fakeSourceLoader{
				dto: &service.DataSourceDTO{ID: 1, Name: "api", Kind: "http",
					ScopeConfig:       map[string]any{"tables_allow": []any{"/search"}, "methods": []any{"GET"}},
					DefaultAccessMode: "read", RequireConfirm: "writes_only"},
				cc: dataconn.ConnConfig{Kind: dataconn.KindHTTP},
			}
			return service.NewDataProxyServiceWithSeams(loader, reg, &fakePermission{resp: service.AllowResp{Behavior: "allow"}}, nil)
		}
		in := base
		in.HTTPMethod, in.HTTPPath = "GET", "/search"
		if _, err := newSvc().Query(ctx, in); err != nil {
			t.Fatalf("GET should pass when methods=[GET]: %v", err)
		}
		in = base
		in.HTTPMethod, in.HTTPPath = "POST", "/search"
		in.HTTPBody = map[string]any{"q": "x"}
		_, err := newSvc().Query(ctx, in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
			t.Fatalf("POST should be scope_denied when methods=[GET], got %v", err)
		}
	})

	t.Run("missing method -> invalid_input", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindHTTP, nil, &captureConnector{kind: dataconn.KindHTTP})
		in := base
		in.HTTPPath = "/users" // http_method missing
		_, err := svc.Query(ctx, in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "invalid_input" {
			t.Fatalf("missing http_method should be invalid_input, got %v", err)
		}
	})

	t.Run("require_confirm always -> redacted summary on card", func(t *testing.T) {
		reg := dataconn.NewRegistry()
		conn := &captureConnector{kind: dataconn.KindHTTP, mode: dataconn.ModeRead,
			ref: dataconn.ResourceRef{Objects: []string{"/users"}, CommandCls: "GET", ReferencesTables: true}}
		reg.Register(dataconn.KindHTTP, conn)
		loader := &fakeSourceLoader{
			dto: &service.DataSourceDTO{ID: 1, Name: "api", Kind: "http",
				ScopeConfig: map[string]any{"tables_allow": []any{"/users"}},
				DefaultAccessMode: "read", RequireConfirm: "always"},
			cc: dataconn.ConnConfig{Kind: dataconn.KindHTTP},
		}
		perm := &fakePermission{resp: service.AllowResp{Behavior: "allow"}}
		svc := service.NewDataProxyServiceWithSeams(loader, reg, perm, nil)
		in := base
		in.HTTPMethod, in.HTTPPath = "GET", "/users"
		in.HTTPBody = map[string]any{"token": "supersecret"}
		if _, err := svc.Query(ctx, in); err != nil {
			t.Fatalf("require_confirm=always allow should pass: %v", err)
		}
		sum, _ := perm.lastIn["statement_summary"].(string)
		if !strings.Contains(sum, "GET /users") || strings.Contains(sum, "supersecret") {
			t.Fatalf("summary must keep skeleton and redact values, got %q", sum)
		}
	})
}

func TestDataProxyStructuredScopeDenied(t *testing.T) {
	conn := &captureConnector{kind: dataconn.KindRedis, mode: dataconn.ModeRead,
		ref: dataconn.ResourceRef{Objects: []string{"jobs:1"}, ReferencesTables: true}}
	svc := newKindProxy(dataconn.KindRedis, map[string]any{"tables_allow": []any{"cache:"}}, conn)
	_, err := svc.Query(context.Background(), service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: 7, UserID: 7, SessionID: "s",
		SourceID: 1, Command: "GET", Args: []string{"jobs:1"},
	})
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "scope_denied" {
		t.Fatalf("out-of-prefix redis key should be scope_denied, got %v", err)
	}
}

func TestDataProxyInvalidInput(t *testing.T) {
	ctx := context.Background()
	base := service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: 7, UserID: 7, SessionID: "s", SourceID: 1,
	}
	expectInvalid := func(t *testing.T, err error) {
		t.Helper()
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "invalid_input" {
			t.Fatalf("expected invalid_input, got %v", err)
		}
	}

	t.Run("statement on redis source", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindRedis, nil, &captureConnector{kind: dataconn.KindRedis})
		in := base
		in.Statement = "GET cache:user:1"
		_, err := svc.Query(ctx, in)
		expectInvalid(t, err)
	})
	t.Run("structured fields on sql source", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindMySQL, nil, &captureConnector{kind: dataconn.KindMySQL})
		in := base
		in.Command, in.Args = "GET", []string{"k"}
		_, err := svc.Query(ctx, in)
		expectInvalid(t, err)
	})
	t.Run("statement and structured both set", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindRedis, nil, &captureConnector{kind: dataconn.KindRedis})
		in := base
		in.Statement, in.Command = "SELECT 1", "GET"
		_, err := svc.Query(ctx, in)
		expectInvalid(t, err)
	})
	t.Run("mongo missing op", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindMongo, nil, &captureConnector{kind: dataconn.KindMongo})
		in := base
		in.Collection = "orders" // mongo_op missing
		_, err := svc.Query(ctx, in)
		expectInvalid(t, err)
	})
	t.Run("es missing method", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindElasticsearch, nil, &captureConnector{kind: dataconn.KindElasticsearch})
		in := base
		in.Index, in.ESPath = "orders", "_search" // es_method missing
		_, err := svc.Query(ctx, in)
		expectInvalid(t, err)
	})
	t.Run("empty input on sql source", func(t *testing.T) {
		svc := newKindProxy(dataconn.KindMySQL, nil, &captureConnector{kind: dataconn.KindMySQL})
		_, err := svc.Query(ctx, base)
		expectInvalid(t, err)
	})
}

func TestDataProxyRequireConfirmAlways(t *testing.T) {
	deps := newTestDataProxy(t, "always", "read")
	ctx := context.Background()

	out, err := deps.svc.Query(ctx, service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: deps.uid, UserID: deps.uid, SessionID: "s",
		SourceID: deps.readSourceID, Statement: "SELECT id FROM orders",
	})
	if err != nil || out == nil {
		t.Fatalf("require_confirm=always read should pass when permission allows: %v", err)
	}
	if deps.perm.called != 1 {
		t.Fatalf("require_confirm=always must call permission exactly once, called=%d", deps.perm.called)
	}
	if deps.perm.lastIn["mode"] != "read" || deps.perm.lastIn["source"] != "analytics" {
		t.Fatalf("permission tool input shape wrong: %+v", deps.perm.lastIn)
	}
}

func TestDataProxyConfirmDeny(t *testing.T) {
	deps := newTestDataProxy(t, "always", "read")
	deps.perm.resp = service.AllowResp{Behavior: "deny", Message: "no"}
	ctx := context.Background()

	_, err := deps.svc.Query(ctx, service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: deps.uid, UserID: deps.uid, SessionID: "s",
		SourceID: deps.readSourceID, Statement: "SELECT id FROM orders",
	})
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "denied" {
		t.Fatalf("denied confirmation should be DataProxyError{denied}, got %v", err)
	}
}

// TestDataProxyClassifyPolicyMapping locks the classify-error contract: policy
// refusals (deny list / whitelist miss) surface as DataProxyError{denied}
// (HTTP 403) and malformed operations as {invalid_input} (400) — never as a
// plain error that the handler would map to a retryable 502.
func TestDataProxyClassifyPolicyMapping(t *testing.T) {
	reg := dataconn.NewRegistry()
	reg.Register(dataconn.KindRedis, dataconn.NewRedisConnector())
	loader := &fakeSourceLoader{
		dto: &service.DataSourceDTO{
			ID: 1, Name: "kv", Kind: "redis",
			ScopeConfig: map[string]any{"tables_allow": []any{"cache:"}},
			DefaultAccessMode: "read", RequireConfirm: "writes_only",
		},
		cc: dataconn.ConnConfig{Kind: dataconn.KindRedis, Database: "0"},
	}
	svc := service.NewDataProxyServiceWithSeams(loader, reg, &fakePermission{resp: service.AllowResp{Behavior: "allow"}}, nil)
	base := service.DataQueryInput{
		WorkspaceID: 1, OwnerType: "user", OwnerID: 7, UserID: 7, SessionID: "s", SourceID: 1,
	}

	for _, c := range []struct {
		cmd  string
		args []string
	}{
		{"KEYS", []string{"*"}},          // deny list
		{"FLUSHALL", nil},                // deny list
		{"GETDEL", []string{"cache:x"}},  // whitelist miss
		{"CONFIG GET", []string{"dir"}},  // deny list, folded subcommand
	} {
		in := base
		in.Command, in.Args = c.cmd, c.args
		_, err := svc.Query(context.Background(), in)
		if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "denied" {
			t.Fatalf("%s: want DataProxyError{denied}, got %v", c.cmd, err)
		}
	}

	// Malformed (whitelisted command, bad shape) -> invalid_input.
	in := base
	in.Command, in.Args = "SCAN", []string{"0"} // SCAN without MATCH
	_, err := svc.Query(context.Background(), in)
	if pe, ok := service.IsDataProxyError(err); !ok || pe.Code != "invalid_input" {
		t.Fatalf("SCAN without MATCH: want DataProxyError{invalid_input}, got %v", err)
	}
}

// TestDataProxyDeniedAudit verifies gate rejections leave an audit trail: a
// denied out-of-scope / write attempt is recorded with the gate code as the
// row status (a security review wants exactly these signals).
func TestDataProxyDeniedAudit(t *testing.T) {
	deps := newTestDataProxy(t, "writes_only", "read")
	ctx := context.Background()
	base := func(stmt string) service.DataQueryInput {
		return service.DataQueryInput{
			WorkspaceID: 1, OwnerType: "user", OwnerID: deps.uid, UserID: deps.uid, SessionID: "s",
			SourceID: deps.readSourceID, Statement: stmt,
		}
	}

	if _, err := deps.svc.Query(ctx, base("SELECT * FROM secrets")); err == nil {
		t.Fatal("out-of-scope read should be rejected")
	}
	if _, err := deps.svc.Query(ctx, base("DELETE FROM orders")); err == nil {
		t.Fatal("write on read-only source should be rejected")
	}

	counts := map[string]int{}
	rows, err := deps.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM data_source_audit GROUP BY status`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		counts[status] = n
	}
	if counts["scope_denied"] != 1 || counts["write_disabled"] != 1 {
		t.Fatalf("denied attempts not audited: %v", counts)
	}
}
