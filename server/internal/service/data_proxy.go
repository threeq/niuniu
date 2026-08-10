// Package service — DataProxyService is the three-layer gate (spec
// docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
// section 5) that every agent-issued data query passes through before it
// reaches a real database. The gate enforces, in order:
//
//  1. source load (owner-filtered) + scope_config parse + decrypted ConnConfig.
//  2. per-kind op construction (buildOperation: statement XOR the source
//     kind's structured fields) then Classify (connector) -> AccessMode +
//     ResourceRef (touched objects).
//  3. read/write separation: a write on a source whose default_access_mode is
//     not "readwrite" is rejected (write_disabled) before any confirm/exec.
//  4. data scope (section 5.3): the touched objects must satisfy the source's
//     scope policy (databases allowlist / tables_allow / tables_deny),
//     re-interpreted per kind family (SQL tables / Redis key prefixes / Mongo
//     collections / ES indices — W1 PoC conclusions §2-3); any out-of-scope
//     or unknown object is rejected (scope_denied) WITHOUT a confirmation
//     chance (M1 is strict).
//  5. per-op confirmation (section 5.2): writes, and any source with
//     require_confirm=="always", route through PermissionService.Request
//     (the same chat permission card Claude tool calls use); a non-allow
//     decision is rejected (denied).
//  6. inject server-controlled constraints (database/row-limit/timeout) and
//     execute on the connector (read-only tx + row/time caps live in dataconn).
//  7. audit (CreateDataSourceAudit) with a redacted+truncated summary.
//
// The proxy never returns decrypted ConnConfig or full statements to callers;
// DataProxyError carries a stable machine-readable Code so the MCP shim / SPA
// can render the right message.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// DataQueryInput is the request to the data proxy (from MCP run_data_query via
// the /mcp/data-proxy/query handler). WorkspaceID/OwnerType/OwnerID/SessionID
// come from the MCP-authenticated workspace row; UserID is the session user;
// SourceID plus exactly ONE operation form — Statement (SQL family, incl.
// Trino) or the structured field group matching the source kind — is the
// agent's request. Mixing forms or omitting the kind's required fields is
// rejected with invalid_input (buildOperation).
type DataQueryInput struct {
	WorkspaceID int64
	OwnerType   string
	OwnerID     int64
	UserID      int64
	SessionID   string
	OrgIDs      []int64
	SourceID    int64

	// SQL family (mysql/postgres/.../trino): a single statement (+ params).
	Statement string
	Params    []any

	// Redis: Command + Args, e.g. Command="GET", Args=["cache:user:1"].
	Command string
	Args    []string

	// Mongo: Collection + MongoOp (find/aggregate/insertOne/...), with
	// Filter / Pipeline / Document depending on the op.
	Collection string
	MongoOp    string
	Filter     map[string]any
	Pipeline   []map[string]any
	Document   map[string]any

	// Elasticsearch: Index + ESMethod + ESPath (+ optional Query body).
	Index    string
	ESMethod string
	ESPath   string
	Query    map[string]any

	// HTTP: HTTPMethod + HTTPPath (+ optional HTTPQuery params / HTTPBody /
	// HTTPListPath — the per-request response-unpack rule).
	HTTPMethod   string
	HTTPPath     string
	HTTPQuery    map[string]any
	HTTPBody     map[string]any
	HTTPListPath string

	RowLimit int
}

// DataProxyError is a gate rejection with a stable Code:
//
//	invalid_input | write_disabled | scope_denied | denied | unsupported
//
// The handler maps invalid_input (malformed per-kind input shape) to HTTP 400
// and everything else to HTTP 403, with {message, code}; the MCP shim
// surfaces Message to the agent.
type DataProxyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *DataProxyError) Error() string { return e.Message }

// IsDataProxyError reports whether err is a *DataProxyError (and returns it).
func IsDataProxyError(err error) (*DataProxyError, bool) {
	var pe *DataProxyError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// permissionRequester is the seam DataProxyService uses for per-op
// confirmation. *PermissionService satisfies it; tests inject a fake. Keeping
// it an interface lets the proxy depend on a narrow surface (and lets nil mean
// "no confirmation backend wired" -> deny, fail-closed).
type permissionRequester interface {
	Request(ctx context.Context, workspaceID int64, ownerType string, ownerID int64,
		sessionID, toolName string, toolInput map[string]any) (AllowResp, error)
}

// dataSourceLoader is the seam over DataSourceService the proxy needs: the
// redacted metadata DTO (kind / scope / access-mode / require-confirm / name)
// plus the decrypted ConnConfig (default database + driver params). Both are
// owner-filtered. *DataSourceService satisfies it.
type dataSourceLoader interface {
	Get(ctx context.Context, id, userID int64, orgIDs []int64) (*DataSourceDTO, error)
	LoadConnConfig(ctx context.Context, id, userID int64, orgIDs []int64) (dataconn.ConnConfig, error)
	ResolveSourceID(ctx context.Context, nameOrID string, userID int64, orgIDs []int64) (int64, error)
}

// DataProxyService coordinates the three-layer gate. It holds the source
// loader (owner-filtered CRUD + decrypt), the connector registry (Classify +
// Execute), the permission requester (per-op confirm), and a wrapped store for
// audit writes.
type DataProxyService struct {
	sources  dataSourceLoader
	registry *dataconn.Registry
	perm     permissionRequester
	q        *store.Queries
}

// NewDataProxyService wires the proxy. perm may be nil in tests that never hit
// the confirm path (read + in-scope + require_confirm!=always); when the
// confirm path is reached with a nil perm the gate fails closed (denied).
func NewDataProxyService(sources *DataSourceService, reg *dataconn.Registry, perm *PermissionService, q *store.Queries) *DataProxyService {
	var p permissionRequester
	if perm != nil { // avoid a typed-nil *PermissionService satisfying the iface
		p = perm
	}
	return &DataProxyService{sources: sources, registry: reg, perm: p, q: q}
}

// NewDataProxyServiceWithSeams is the test-friendly constructor: it accepts the
// loader and permission seams directly so tests can inject fakes (a fake
// connector registry, a programmable permission gate). Production code uses
// NewDataProxyService.
func NewDataProxyServiceWithSeams(sources dataSourceLoader, reg *dataconn.Registry, perm permissionRequester, q *store.Queries) *DataProxyService {
	return &DataProxyService{sources: sources, registry: reg, perm: perm, q: q}
}

const dataQueryTimeoutMS = 15000

// maxDataQueryRowLimit caps the agent-supplied row_limit. Connectors default
// a non-positive limit to 1000; this bound stops a single call from asking
// for millions of rows (the result is materialized in server memory before
// the MCP shim sees it).
const maxDataQueryRowLimit = 10000

// buildOperation validates the per-kind input shape — exactly one of
// Statement (SQL family) or the structured field group of the source's
// protocol family may be populated, with the family's required fields present
// — and constructs the connector Operation with the server-injected
// constraints (Database/RowLimit/TimeoutMS are never agent-overridable).
// Shape violations return *DataProxyError{invalid_input} (HTTP 400).
func buildOperation(kind dataconn.SourceKind, in DataQueryInput, cc dataconn.ConnConfig) (dataconn.Operation, error) {
	op := dataconn.Operation{
		Database:  cc.Database,
		RowLimit:  min(in.RowLimit, maxDataQueryRowLimit),
		TimeoutMS: dataQueryTimeoutMS,
	}
	hasSQL := in.Statement != "" || len(in.Params) > 0
	hasRedis := in.Command != "" || len(in.Args) > 0
	hasMongo := in.Collection != "" || in.MongoOp != "" || in.Filter != nil || len(in.Pipeline) > 0 || in.Document != nil
	hasES := in.Index != "" || in.ESMethod != "" || in.ESPath != "" || in.Query != nil
	hasHTTP := in.HTTPMethod != "" || in.HTTPPath != "" || in.HTTPQuery != nil || in.HTTPBody != nil || in.HTTPListPath != ""

	reject := func(format string, a ...any) (dataconn.Operation, error) {
		return dataconn.Operation{}, &DataProxyError{Code: "invalid_input", Message: fmt.Sprintf(format, a...)}
	}
	switch dataconn.KindFamily(kind) {
	case dataconn.FamilyRedis:
		if hasSQL || hasMongo || hasES || hasHTTP {
			return reject("source kind %q expects a structured redis operation {command, args}; statement / mongo / es / http fields must be empty", kind)
		}
		if in.Command == "" {
			return reject("redis operation requires command")
		}
		op.Command, op.Args = in.Command, in.Args
	case dataconn.FamilyMongo:
		if hasSQL || hasRedis || hasES || hasHTTP {
			return reject("source kind %q expects a structured mongo operation {collection, mongo_op, filter|pipeline|document}; statement / redis / es / http fields must be empty", kind)
		}
		if in.Collection == "" || in.MongoOp == "" {
			return reject("mongo operation requires collection and mongo_op")
		}
		op.Collection, op.MongoOp = in.Collection, in.MongoOp
		op.Filter, op.Pipeline, op.Document = in.Filter, in.Pipeline, in.Document
	case dataconn.FamilyElastic:
		if hasSQL || hasRedis || hasMongo || hasHTTP {
			return reject("source kind %q expects a structured elasticsearch operation {index, es_method, es_path, query}; statement / redis / mongo / http fields must be empty", kind)
		}
		if in.Index == "" || in.ESMethod == "" || in.ESPath == "" {
			return reject("elasticsearch operation requires index, es_method and es_path")
		}
		op.Index, op.ESMethod, op.ESPath, op.Query = in.Index, in.ESMethod, in.ESPath, in.Query
	case dataconn.FamilyHTTP:
		if hasSQL || hasRedis || hasMongo || hasES {
			return reject("source kind %q expects a structured http operation {http_method, http_path, http_query, http_body}; statement / redis / mongo / es fields must be empty", kind)
		}
		if in.HTTPMethod == "" || in.HTTPPath == "" {
			return reject("http operation requires http_method and http_path")
		}
		op.HTTPMethod, op.HTTPPath = in.HTTPMethod, in.HTTPPath
		op.HTTPQuery, op.HTTPBody = in.HTTPQuery, in.HTTPBody
		op.HTTPListPath = in.HTTPListPath
	default:
		if hasRedis || hasMongo || hasES || hasHTTP {
			return reject("source kind %q expects statement; structured operation fields must be empty", kind)
		}
		if in.Statement == "" {
			return reject("statement is required")
		}
		op.Statement, op.Params = in.Statement, in.Params
	}
	return op, nil
}

// Query runs the full gate and returns the normalized ResultSet. See the
// package doc for the ordered steps. Errors are either *DataProxyError (gate
// rejection) or a plain error (classify failure -> 400, exec failure -> 5xx).
func (s *DataProxyService) Query(ctx context.Context, in DataQueryInput) (*dataconn.ResultSet, error) {
	// 0) Resolve relative-time tokens ({{now}}, {{now-10m}}, ...) to concrete
	// epoch-ms values. A LIVE saved panel re-runs its operation verbatim on every
	// open; without this a window authored as {"gte": "{{now-10m}}"} would either
	// be a frozen absolute bound (re-querying the same slice forever) or rely on
	// engine date math that a numeric-timestamp source rejects. Resolving here, up
	// front, means classify / scope / summary / audit / execute all see the same
	// resolved values for both interactive queries and panel re-runs.
	in = resolveNowTokens(in, time.Now())

	// 1) Load source (owner-filtered) + decrypted ConnConfig.
	src, err := s.sources.Get(ctx, in.SourceID, in.UserID, in.OrgIDs)
	if err != nil {
		return nil, err
	}
	cc, err := s.sources.LoadConnConfig(ctx, in.SourceID, in.UserID, in.OrgIDs)
	if err != nil {
		return nil, err
	}
	scope := parseScopeConfig(src.ScopeConfig)

	// 2) Build the connector Operation for the source's kind family (statement
	// XOR the kind's structured fields; shape violations -> invalid_input) and
	// Classify (read/write + touched objects). A classify failure is a client
	// error (bad statement / stacked / unrecognized).
	kind := dataconn.SourceKind(src.Kind)
	op, err := buildOperation(kind, in, cc)
	if err != nil {
		return nil, err
	}
	conn, err := s.registry.Get(kind)
	if err != nil {
		return nil, &DataProxyError{Code: "unsupported", Message: err.Error()}
	}
	mode, ref, err := conn.Classify(op)
	if err != nil {
		// A classify failure is never a 5xx: policy refusals (deny list /
		// whitelist miss) map to "denied" (403), malformed operations to
		// "invalid_input" (400). Agents must not see these as retryable
		// connection failures.
		code := "invalid_input"
		if errors.Is(err, dataconn.ErrDeniedByPolicy) || errors.Is(err, dataconn.ErrUnsupported) {
			code = "denied"
		}
		return nil, &DataProxyError{Code: code, Message: err.Error()}
	}
	// Classify leaves ref.Database from op.Database (source default db).
	if ref.Database == "" {
		ref.Database = cc.Database
	}

	// The redacted per-protocol summary feeds the permission card and the
	// audit row — including the audit rows for gate rejections below, so it is
	// computed before the gates (spec 5.5 / W1 §2.4).
	summary := summarizeOperation(kind, op)

	// deny records the rejected attempt in the audit trail (status = the gate
	// code) and returns the gate error. A denied out-of-scope/write attempt is
	// exactly the signal a security review wants to see; only operations that
	// fail classification (no mode/ref to record) skip the trail.
	deny := func(code, message string) (*dataconn.ResultSet, error) {
		s.audit(ctx, in, mode, ref, code, 0, 0, summary)
		return nil, &DataProxyError{Code: code, Message: message}
	}

	// 3) Read/write separation.
	if mode == dataconn.ModeWrite && src.DefaultAccessMode != "readwrite" {
		return deny("write_disabled", fmt.Sprintf("source %q is read-only; writes are disabled", src.Name))
	}

	// 4) Data scope (section 5.3) — strict; out-of-scope is rejected outright.
	if reason := checkScope(kind, scope, ref); reason != "" {
		return deny("scope_denied", fmt.Sprintf("query out of allowed data scope (%s); adjust the source scope in settings", reason))
	}

	// 5) Per-op confirmation (section 5.2): writes, or require_confirm=="always".
	if mode == dataconn.ModeWrite || src.RequireConfirm == "always" {
		if s.perm == nil {
			return deny("denied", "confirmation required but no permission backend is configured")
		}
		resp, err := s.perm.Request(ctx, in.WorkspaceID, in.OwnerType, in.OwnerID, in.SessionID, "DataQuery", map[string]any{
			"source":            src.Name,
			"kind":              src.Kind,
			"mode":              string(mode),
			"database":          ref.Database,
			"objects":           ref.Objects,
			"statement_summary": summary,
		})
		if err != nil {
			return nil, fmt.Errorf("permission request: %w", err)
		}
		if resp.Behavior != "allow" {
			msg := resp.Message
			if msg == "" {
				msg = "data query was not approved"
			}
			return deny("denied", msg)
		}
	}

	// 6) Execute with server-injected constraints already on op. When the source
	// is configured with an SSH tunnel, OpenTunnel establishes a local port
	// forward and rewrites cc.Host/Port to the loopback endpoint; a tunnel
	// failure is surfaced as a connection error (like any Execute failure) so it
	// is audited with status "err" below.
	start := time.Now()
	tcc, cleanup, execErr := dataconn.OpenTunnel(ctx, cc)
	var rs *dataconn.ResultSet
	if execErr == nil {
		defer cleanup()
		rs, execErr = conn.Execute(ctx, tcc, op)
	}
	durationMS := time.Since(start).Milliseconds()

	// 7) Audit (best-effort; an audit failure must not mask the result).
	status := "ok"
	rows := int64(0)
	if execErr != nil {
		status = "err"
	} else if rs != nil {
		rows = int64(len(rs.Rows))
	}
	s.audit(ctx, in, mode, ref, status, rows, durationMS, summary)

	if execErr != nil {
		return nil, fmt.Errorf("execute query: %w", execErr)
	}
	// Stamp the server-authoritative time the data was obtained. This drives the
	// dashboard panel's "data time": live panels show their latest re-query time,
	// and a static snapshot persists this value so it shows when its data was
	// captured (not when the user clicked pin).
	if rs != nil {
		now := time.Now().UTC()
		rs.QueriedAt = &now
	}
	return rs, nil
}

// audit writes one data_source_audit row. Objects are JSON-encoded; the summary
// is redacted+truncated (no value-bearing SQL persisted, per spec 5.5).
//
// TODO(M2): prune loop — data_source_audit grows unbounded; add a retention /
// prune loop (out of scope for M1).
func (s *DataProxyService) audit(ctx context.Context, in DataQueryInput, mode dataconn.AccessMode, ref dataconn.ResourceRef, status string, rows, durationMS int64, summary string) {
	if s.q == nil {
		return
	}
	objsJSON, _ := json.Marshal(ref.Objects)
	if len(objsJSON) == 0 {
		objsJSON = []byte("[]")
	}
	_ = s.q.CreateDataSourceAudit(ctx, store.CreateDataSourceAuditParams{
		UserID:           in.UserID,
		SourceID:         in.SourceID,
		AccessMode:       string(mode),
		DatabaseName:     ref.Database,
		Objects:          string(objsJSON),
		StatementSummary: summary,
		Status:           status,
		Rows:             rows,
		DurationMs:       durationMS,
	})
}

// scopeConfig is the parsed (non-secret) data-scope policy from a source row.
type scopeConfig struct {
	databases   []string
	tablesAllow []string
	tablesDeny  []string
	// methods is the http-only allow-list of HTTP verbs (e.g. ["GET","POST"]).
	// Empty means no method restriction beyond the read/write gate.
	methods []string
}

// parseScopeConfig extracts the string lists from the loose JSON map. It is
// tolerant: missing / wrong-typed fields become empty lists.
func parseScopeConfig(m map[string]any) scopeConfig {
	return scopeConfig{
		databases:   stringList(m["databases"]),
		tablesAllow: stringList(m["tables_allow"]),
		tablesDeny:  stringList(m["tables_deny"]),
		methods:     stringList(m["methods"]),
	}
}

// stringList coerces a JSON array (any of string elements) into []string,
// ignoring non-string elements. Returns nil for non-array input.
func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// sysSchemas lists read-only metadata schemas that are always allowed through
// the database allowlist gate. These are internal to the DB engine and contain
// no user business data, so blocking them would prevent schema introspection
// (e.g. "SELECT table_name FROM information_schema.tables") even when a
// database allowlist is configured.
var sysSchemas = map[string]bool{
	"information_schema": true,
	"performance_schema": true,
	"sys":                true,
	"pg_catalog":         true,
	"pg_toast":           true,
	"pg_temp":            true,
}

// checkScope enforces spec section 5.3, generalized per kind family (W1 PoC
// conclusions §2-3, docs/superpowers/specs/2026-06-10-nosql-w1-poc-conclusions.md).
// scope_config's databases/tables_allow/tables_deny are re-interpreted per
// family: SQL/Trino tables (case-insensitive), Mongo collections (exact,
// case-sensitive), Redis literal key prefixes (case-sensitive), ES index names
// (case-insensitive, optional trailing-* wildcard). It returns "" when the
// operation is in-scope, or a short human reason when denied.
//
// The fail-closed guard is shared: an operation that references objects
// (tables / keys / collections / indices) whose names could not be statically
// resolved must NOT be treated as in-scope. A genuinely object-less operation
// (e.g. "SELECT 1") has ReferencesTables=false and is allowed.
func checkScope(kind dataconn.SourceKind, scope scopeConfig, ref dataconn.ResourceRef) string {
	if len(ref.Objects) == 0 {
		if ref.ReferencesTables {
			return "object references could not be resolved"
		}
		return ""
	}
	switch dataconn.KindFamily(kind) {
	case dataconn.FamilyRedis:
		return checkScopeRedis(scope, ref)
	case dataconn.FamilyMongo:
		return checkScopeMongo(scope, ref)
	case dataconn.FamilyElastic:
		return checkScopeElastic(scope, ref)
	case dataconn.FamilyHTTP:
		return checkScopeHTTP(scope, ref)
	default:
		return checkScopeSQL(scope, ref)
	}
}

// checkScopeHTTP enforces the http policy: an optional `methods` allow-list of
// HTTP verbs, then path-prefix authorization. tables_allow/tables_deny are
// request path PREFIXES matched case-SENSITIVELY with strings.HasPrefix (paths
// are case-sensitive); ref.Objects is the single concrete request path. Deny
// wins; a non-empty allow list requires every object to carry an allowed
// prefix. `methods`, when non-empty, restricts which verbs may pass at all
// (case-insensitive, on top of the read/write gate). The databases list does
// not apply to HTTP and is ignored.
func checkScopeHTTP(scope scopeConfig, ref dataconn.ResourceRef) string {
	if len(scope.methods) > 0 {
		m := strings.ToUpper(strings.TrimSpace(ref.CommandCls))
		allowed := false
		for _, am := range scope.methods {
			if strings.ToUpper(strings.TrimSpace(am)) == m {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Sprintf("method %q not in allowed methods", m)
		}
	}
	allow := trimList(scope.tablesAllow)
	deny := trimList(scope.tablesDeny)
	for _, p := range ref.Objects {
		for _, d := range deny {
			if strings.HasPrefix(p, d) {
				return fmt.Sprintf("path %q matches denied prefix %q", p, d)
			}
		}
		if len(allow) == 0 {
			continue
		}
		inAllow := false
		for _, a := range allow {
			if strings.HasPrefix(p, a) {
				inAllow = true
				break
			}
		}
		if !inAllow {
			return fmt.Sprintf("path %q not under any allowed prefix", p)
		}
	}
	return ""
}

// checkScopeSQL is the original SQL-family policy:
//   - a db.table object whose db is not in scope.databases (when non-empty) -> deny.
//     Exception: system/metadata schemas (information_schema, pg_catalog, sys,
//     etc.) are always allowed through the db allowlist so schema introspection
//     queries work regardless of the database allowlist configuration.
//   - any object whose table is in tables_deny -> deny.
//   - tables_allow non-empty and any object's table not in it -> deny.
//
// Comparison is case-insensitive. An object "db.table" is split; an object with
// no "db." prefix only checks the table segment against allow/deny. Allow/deny
// entries match the bare table name, the "db.table" pair, or the full dotted
// name — the last form is how a three-part Trino "catalog.schema.table" (or
// Postgres "db.schema.table") entry pins one exact object below catalog level.
func checkScopeSQL(scope scopeConfig, ref dataconn.ResourceRef) string {
	dbSet := lowerSet(scope.databases)
	allowSet := lowerSet(scope.tablesAllow)
	denySet := lowerSet(scope.tablesDeny)

	for _, obj := range ref.Objects {
		db, table := splitDBTable(obj)
		dbl := strings.ToLower(db)
		tbl := strings.ToLower(table)
		full := strings.ToLower(obj)

		// db allowlist (only checked when the object carries a db prefix and
		// the policy restricts databases). System/metadata schemas are exempt so
		// introspection queries (information_schema, pg_catalog, etc.) always
		// work — but only for two-part schema.table names: in a three-part
		// catalog.schema.table (Trino federation) the first segment is a
		// catalog, and a catalog that happens to be named "sys"/
		// "information_schema" must not bypass the catalog allowlist.
		twoPart := strings.Count(obj, ".") == 1
		if len(dbSet) > 0 && dbl != "" && !dbSet[dbl] && !(twoPart && sysSchemas[dbl]) {
			return fmt.Sprintf("database %q not allowed", db)
		}
		// deny wins over allow.
		if denySet[tbl] || denySet[full] || (db != "" && denySet[dbl+"."+tbl]) {
			return fmt.Sprintf("table %q is denied", table)
		}
		if len(allowSet) > 0 {
			if !allowSet[tbl] && !allowSet[full] && !allowSet[dbl+"."+tbl] {
				return fmt.Sprintf("table %q not in allow-list", table)
			}
		}
	}
	return ""
}

// splitDBTable splits a dotted object name into (db, table). A bare "table"
// yields ("", "table"). For multi-part names the FIRST segment is the
// database/catalog and the LAST segment is the table/object: "db.table" ->
// ("db", "table"), and a 3-part Postgres name "analytics.public.events" ->
// ("analytics", "events") (the middle schema segment is dropped). Taking the
// first dot as the db (not the last) keeps the db-allowlist check correct for
// catalog.schema.object names.
func splitDBTable(obj string) (string, string) {
	first := strings.IndexByte(obj, '.')
	if first < 0 {
		return "", obj
	}
	last := strings.LastIndexByte(obj, '.')
	return obj[:first], obj[last+1:]
}

// checkScopeRedis: tables_allow/tables_deny are literal key prefixes
// (W1 §2.1 — no glob), matched case-SENSITIVELY with strings.HasPrefix.
// Objects are the keys touched by the command (or the literal prefix of a
// SCAN MATCH pattern); every object must carry an allow prefix when the allow
// list is non-empty, and must not carry any deny prefix (deny wins). The
// databases list does not apply to redis (the logical db is injected via
// ConnConfig, the agent cannot SELECT) and is ignored.
//
// A SCAN object is enumerative — it stands for the whole key subtree under
// the pattern's literal prefix, not a single key — so the deny check must
// also run in reverse: a deny prefix that FALLS UNDER the scan prefix
// (HasPrefix(deny, scanPrefix)) means the scan would enumerate denied key
// names (allow=["cache:"], deny=["cache:secret:"], MATCH cache:* would
// otherwise pass) and is rejected.
func checkScopeRedis(scope scopeConfig, ref dataconn.ResourceRef) string {
	allow := trimList(scope.tablesAllow)
	deny := trimList(scope.tablesDeny)
	enumerative := ref.CommandCls == "SCAN"
	for _, key := range ref.Objects {
		for _, d := range deny {
			if strings.HasPrefix(key, d) {
				return fmt.Sprintf("key %q matches denied prefix %q", key, d)
			}
			if enumerative && strings.HasPrefix(d, key) {
				return fmt.Sprintf("scan prefix %q covers denied prefix %q", key, d)
			}
		}
		if len(allow) == 0 {
			continue
		}
		inAllow := false
		for _, a := range allow {
			if strings.HasPrefix(key, a) {
				inAllow = true
				break
			}
		}
		if !inAllow {
			return fmt.Sprintf("key %q not under any allowed prefix", key)
		}
	}
	return ""
}

// checkScopeMongo: tables_allow/tables_deny are collection names, matched
// exactly and case-SENSITIVELY (W1 §2.2). Objects are bare collection names
// or, for cross-database pipeline targets ($out {db,coll} / $merge.into),
// fully qualified "db.collection" split at the FIRST dot — the remainder may
// itself contain dots, so a connector must fully qualify any collection whose
// own name contains a dot. The databases allowlist applies to the db segment
// of qualified objects only; the source's injected default database is
// trusted by configuration (same contract as the SQL branch).
func checkScopeMongo(scope scopeConfig, ref dataconn.ResourceRef) string {
	dbs := stringSet(scope.databases)
	allow := stringSet(scope.tablesAllow)
	deny := stringSet(scope.tablesDeny)
	for _, obj := range ref.Objects {
		db, coll := "", obj
		if i := strings.IndexByte(obj, '.'); i >= 0 {
			db, coll = obj[:i], obj[i+1:]
		}
		if len(dbs) > 0 && db != "" && !dbs[db] {
			return fmt.Sprintf("database %q not allowed", db)
		}
		if deny[coll] || (db != "" && deny[db+"."+coll]) {
			return fmt.Sprintf("collection %q is denied", coll)
		}
		if len(allow) > 0 && !allow[coll] && (db == "" || !allow[db+"."+coll]) {
			return fmt.Sprintf("collection %q not in allow-list", coll)
		}
	}
	return ""
}

// checkScopeElastic: tables_allow/tables_deny are index names — exact, or a
// single trailing-* wildcard for rolling indices ("logs-*") — compared
// case-insensitively (W1 §2.3). Objects must already be single concrete index
// names: anything still carrying a wildcard / comma / exclusion at this layer
// is denied (the connector rejects these earlier; this is fail-closed defense
// in depth). The databases list does not apply to ES and is ignored.
func checkScopeElastic(scope scopeConfig, ref dataconn.ResourceRef) string {
	for _, obj := range ref.Objects {
		if strings.ContainsAny(obj, "*?,") || strings.HasPrefix(obj, "-") || obj == "_all" {
			return fmt.Sprintf("index expression %q is not a single concrete index", obj)
		}
		idx := strings.ToLower(obj)
		for _, d := range scope.tablesDeny {
			if matchESIndex(d, idx) {
				return fmt.Sprintf("index %q is denied", obj)
			}
		}
		if len(scope.tablesAllow) == 0 {
			continue
		}
		inAllow := false
		for _, a := range scope.tablesAllow {
			if matchESIndex(a, idx) {
				inAllow = true
				break
			}
		}
		if !inAllow {
			return fmt.Sprintf("index %q not in allow-list", obj)
		}
	}
	return ""
}

// matchESIndex matches one scope entry against a lowercased concrete index
// name: a single trailing '*' is a prefix wildcard, anything else is exact.
// Entries with a '*' anywhere but the end are invalid per W1 §2.3 and match
// nothing (objects can never contain '*' here).
func matchESIndex(pattern, idx string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if strings.HasSuffix(p, "*") && strings.Count(p, "*") == 1 {
		return strings.HasPrefix(idx, p[:len(p)-1])
	}
	return idx == p
}

// trimList trims each entry, dropping empties, keeping case (the
// case-sensitive families' counterpart of lowerSet's TrimSpace hygiene).
func trimList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if t := strings.TrimSpace(it); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// stringSet is the case-SENSITIVE counterpart of lowerSet (redis key prefixes
// and mongo collection names are case-sensitive, W1 §2.1/§2.2).
func stringSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, it := range items {
		if t := strings.TrimSpace(it); t != "" {
			m[t] = true
		}
	}
	return m
}

func lowerSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[strings.ToLower(strings.TrimSpace(it))] = true
	}
	return m
}

var (
	// sqlStringLiteralRe matches single-quoted SQL string literals, including
	// doubled-quote escapes ('it''s'). Replaced with '?' so values like SSNs or
	// emails never reach the audit row / permission card (spec 5.5).
	sqlStringLiteralRe = regexp.MustCompile(`'(?:[^']|'')*'`)
	// sqlNumberLiteralRe matches standalone numeric literals (int/float). The
	// boundaries keep it from chewing identifiers like col1 or t2 (a digit that
	// is part of a word is preceded/followed by a word char and is skipped).
	sqlNumberLiteralRe = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
)

// summarizeOperation routes to the per-protocol redacting summarizer (W1 PoC
// conclusions §2.4): values are redacted, structural skeletons (commands /
// objects / keys / operators) are kept, and every summary is whitespace-
// collapsed and capped at 200 runes. ConnConfig and full request bodies never
// reach the audit row or the permission card (spec 5.5).
func summarizeOperation(kind dataconn.SourceKind, op dataconn.Operation) string {
	switch dataconn.KindFamily(kind) {
	case dataconn.FamilyRedis:
		return summarizeRedis(op)
	case dataconn.FamilyMongo:
		return summarizeMongo(op)
	case dataconn.FamilyElastic:
		return summarizeES(op)
	case dataconn.FamilyHTTP:
		return summarizeHTTP(op)
	default:
		return summarizeStatement(op.Statement)
	}
}

// summarizeHTTP keeps method + path and the JSON key skeleton of the query
// params / request body; all leaf values are redacted.
func summarizeHTTP(op dataconn.Operation) string {
	parts := []string{strings.ToUpper(op.HTTPMethod), op.HTTPPath}
	if op.HTTPQuery != nil {
		parts = append(parts, marshalRedacted(op.HTTPQuery))
	}
	if op.HTTPBody != nil {
		parts = append(parts, "body"+marshalRedacted(op.HTTPBody))
	}
	return capSummary(strings.Join(parts, " "))
}

// summarizeStatement produces a redacted, length-capped summary for the SQL
// family. It redacts literal values — single-quoted string literals become
// '?' and standalone numeric literals become ? — so no value-bearing SQL is
// persisted.
func summarizeStatement(stmt string) string {
	redacted := sqlStringLiteralRe.ReplaceAllString(stmt, "'?'")
	redacted = sqlNumberLiteralRe.ReplaceAllString(redacted, "?")
	return capSummary(redacted)
}

// summarizeMongo keeps mongoOp + collection + the key/$-operator skeleton of
// filter / pipeline / document; all values are redacted (a write Document
// therefore records its field-name set only).
func summarizeMongo(op dataconn.Operation) string {
	parts := []string{op.MongoOp, op.Collection}
	if op.Filter != nil {
		parts = append(parts, marshalRedacted(op.Filter))
	}
	if len(op.Pipeline) > 0 {
		arr := make([]any, len(op.Pipeline))
		for i, stage := range op.Pipeline {
			arr[i] = stage
		}
		parts = append(parts, marshalRedacted(arr))
	}
	if op.Document != nil {
		parts = append(parts, "doc"+marshalRedacted(op.Document))
	}
	return capSummary(strings.Join(parts, " "))
}

// summarizeRedis keeps the (uppercased) command and key/field arguments
// verbatim — keys are the audit's object identity and are already constrained
// by the prefix scope — and redacts value arguments per command shape.
func summarizeRedis(op dataconn.Operation) string {
	cmd := strings.ToUpper(op.Command)
	return capSummary(strings.Join(append([]string{cmd}, redactRedisArgs(cmd, op.Args)...), " "))
}

// summarizeES keeps method + index + endpoint and the JSON key / DSL-operator
// skeleton of the query body; all leaf values are redacted.
func summarizeES(op dataconn.Operation) string {
	parts := []string{strings.ToUpper(op.ESMethod), op.Index, op.ESPath}
	if op.Query != nil {
		parts = append(parts, marshalRedacted(op.Query))
	}
	return capSummary(strings.Join(parts, " "))
}

// capSummary collapses whitespace and truncates to 200 runes — the shared
// tail of every per-protocol summarizer.
func capSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 200
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}

// marshalRedacted renders the redacted skeleton of a JSON-ish value. Map keys
// sort deterministically via encoding/json.
func marshalRedacted(v any) string {
	b, err := json.Marshal(redactValue(v, 0))
	if err != nil {
		return "?"
	}
	return string(b)
}

// maxRedactDepth bounds the skeleton recursion so hostile nesting cannot blow
// up the audit row.
const maxRedactDepth = 10

// redactValue keeps map keys (field names / $ operators) and structured array
// shape but replaces every leaf value with "?". Scalar-only arrays collapse
// to a single ["?"] so element counts don't leak.
func redactValue(v any, depth int) any {
	if depth >= maxRedactDepth {
		return "?"
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = redactValue(e, depth+1)
		}
		return out
	case []any:
		structured := false
		for _, e := range t {
			switch e.(type) {
			case map[string]any, []any:
				structured = true
			}
		}
		if !structured {
			return []any{"?"}
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValue(e, depth+1)
		}
		return out
	default:
		return "?"
	}
}

// redisKeepAllArgs lists commands whose arguments are keys / fields /
// patterns / cursors only — safe to keep verbatim in the audit summary.
var redisKeepAllArgs = map[string]bool{
	"GET": true, "MGET": true, "EXISTS": true, "TYPE": true, "TTL": true,
	"PTTL": true, "STRLEN": true, "DEL": true, "PERSIST": true,
	"HGET": true, "HMGET": true, "HGETALL": true, "HLEN": true, "HKEYS": true,
	"LLEN": true, "SMEMBERS": true, "SCARD": true, "ZCARD": true,
	"SCAN": true, "HSCAN": true, "SSCAN": true, "ZSCAN": true,
}

// redactRedisArgs keeps key/field arguments and replaces value arguments with
// "?" per command shape (W1 §2.4): keep-all commands pass through; MSET keeps
// keys (even positions) and redacts values; HSET keeps key + field names and
// redacts field values; everything else keeps the first argument (the key)
// and redacts the rest (SET k v -> SET k ?).
func redactRedisArgs(cmd string, args []string) []string {
	if redisKeepAllArgs[cmd] {
		return args
	}
	out := make([]string, len(args))
	for i, a := range args {
		var keep bool
		switch cmd {
		case "MSET":
			keep = i%2 == 0
		case "HSET", "HSETNX":
			keep = i == 0 || i%2 == 1
		default:
			keep = i == 0
		}
		if keep {
			out[i] = a
		} else {
			out[i] = "?"
		}
	}
	return out
}
