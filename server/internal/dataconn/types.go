// Package dataconn defines protocol-agnostic connectors for external data
// sources (the SQL family incl. Trino, plus redis, mongo and elasticsearch).
// Connectors classify an operation as read/write, enforce row/time limits,
// and normalize results into ResultSet. ConnConfig carries decrypted
// connection params and MUST NOT escape the service layer.
package dataconn

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type SourceKind string

const (
	KindMySQL       SourceKind = "mysql"
	KindPostgres    SourceKind = "postgres"
	KindClickHouse  SourceKind = "clickhouse"
	KindMSSQL       SourceKind = "mssql"
	KindMariaDB     SourceKind = "mariadb"
	KindTiDB        SourceKind = "tidb"
	KindOceanBase   SourceKind = "oceanbase"
	KindStarRocks   SourceKind = "starrocks"
	KindDoris       SourceKind = "doris"
	KindCockroachDB SourceKind = "cockroachdb"
	KindGreenplum   SourceKind = "greenplum"
	KindRedshift    SourceKind = "redshift"
	KindOpenGauss   SourceKind = "opengauss"
	KindPolarDBPG   SourceKind = "polardbpg"
	KindYugabyte    SourceKind = "yugabyte"
	KindRedis       SourceKind = "redis"
	KindMongo       SourceKind = "mongo"
	KindTrino       SourceKind = "trino"
	// KindElasticsearch keeps the full product name as the wire value to match
	// the data_sources.kind CHECK and the W1 PoC conclusions doc.
	KindElasticsearch SourceKind = "elasticsearch"
	// KindHTTP is a generic HTTP/JSON REST data source: the agent issues a
	// method + relative path (+ query/body) against a configured base URL, and
	// the connector normalizes the JSON response into a ResultSet. It is its own
	// protocol family (FamilyHTTP) so the gate treats scope as path prefixes.
	KindHTTP SourceKind = "http"
)

// Family groups SourceKinds by protocol family. The data-proxy gateway
// branches op construction, scope interpretation, and audit redaction on the
// family. It lives next to the SourceKind constants so adding a kind and
// classifying its family happen in the same file: a non-SQL kind missing from
// KindFamily would silently get SQL gate semantics (statement-based ops,
// case-insensitive scope matching).
type Family int

const (
	FamilySQL     Family = iota // statement-based, incl. Trino (catalog.schema.table)
	FamilyRedis                 // command+args, key-prefix scope
	FamilyMongo                 // collection+op, collection scope
	FamilyElastic               // method+path+body, index scope
	FamilyHTTP                  // method+path+query+body, path-prefix scope
)

// KindFamily maps a SourceKind to its protocol family. Every kind not listed
// is FamilySQL — when adding a non-SQL kind, extend this switch in the same
// change that adds the constant.
func KindFamily(kind SourceKind) Family {
	switch kind {
	case KindRedis:
		return FamilyRedis
	case KindMongo:
		return FamilyMongo
	case KindElasticsearch:
		return FamilyElastic
	case KindHTTP:
		return FamilyHTTP
	default:
		return FamilySQL
	}
}

type AccessMode string

const (
	ModeRead  AccessMode = "read"
	ModeWrite AccessMode = "write"
)

// ErrUnsupported is returned by connectors not yet implemented.
var ErrUnsupported = errors.New("dataconn: connector not implemented")

// ErrDeniedByPolicy marks a Classify refusal that is a policy decision (a
// command/op/endpoint outside the whitelist, or on the explicit deny list) as
// opposed to a malformed operation. The gateway maps it to a 403 "denied"
// rejection instead of a 400 "invalid_input" — and never to a 5xx: a policy
// refusal is not a connection failure, so agents must not retry it.
var ErrDeniedByPolicy = errors.New("dataconn: operation denied by policy")

// ConnConfig is the decrypted connection parameters. Service-layer only.
type ConnConfig struct {
	Kind     SourceKind
	Host     string
	Port     int
	User     string
	Password string
	Database string
	Options  map[string]any
	// SSH, when non-nil and Enabled, routes the connection to Host:Port through
	// an SSH tunnel (local port forward — see OpenTunnel). Protocol-agnostic:
	// the connector still dials Host:Port, which OpenTunnel has rewritten to a
	// loopback endpoint. Service-layer only, like the rest of ConnConfig.
	SSH *SSHConfig
}

// Operation is one agent-requested operation; only fields for the source's
// protocol are populated. Database/RowLimit/TimeoutMS are injected by the
// service layer and are NOT agent-overridable.
type Operation struct {
	Statement  string
	Params     []any
	Command    string
	Args       []string
	Collection string
	MongoOp    string
	Filter     map[string]any
	Pipeline   []map[string]any
	Document   map[string]any
	// Elasticsearch (populated only for elasticsearch sources): Index is one
	// concrete target index (no wildcards/commas — rolling patterns live in
	// scope entries like "logs-*"); ESMethod+ESPath select the endpoint
	// (e.g. GET {Index}/_search -> ESMethod "GET", ESPath "_search");
	// Query is the JSON request body, if any.
	Index    string
	ESMethod string
	ESPath   string
	Query    map[string]any
	// HTTP (populated only for http sources): HTTPMethod is the request verb
	// (GET/HEAD read, POST/PUT/PATCH/DELETE write); HTTPPath is a relative path
	// resolved against the source's configured base URL (no scheme/host);
	// HTTPQuery are query-string parameters; HTTPBody is the JSON request body.
	HTTPMethod string
	HTTPPath   string
	HTTPQuery  map[string]any
	HTTPBody   map[string]any
	// HTTPListPath is the per-request "unpack" rule: a dotted path to the array
	// inside the JSON response to expand into rows (e.g. "data.list"). Empty
	// falls back to the source's options.list_path, and if that is also empty the
	// response is returned as-is (top-level array -> rows, object -> one row).
	HTTPListPath string
	Database     string
	RowLimit     int
	TimeoutMS    int
}

type Column struct {
	Name string `json:"name"`
	Type string `json:"type"` // string|number|bool|time|json
}

type ResultSet struct {
	Columns      []Column        `json:"columns"`
	Rows         [][]any         `json:"rows"`
	RowsAffected int64           `json:"rows_affected,omitempty"`
	Truncated    bool            `json:"truncated"`
	DurationMS   int64           `json:"duration_ms"`
	Engine       string          `json:"engine"`
	Raw          json.RawMessage `json:"raw,omitempty"`
	// QueriedAt is when the data was actually obtained from the source (set by
	// the data proxy on a successful query). Pointer + omitempty so connectors
	// that don't stamp it serialize no field. Surfaced to the dashboard as the
	// panel's "data time": a live panel shows its latest re-query time; a static
	// snapshot carries the time its data was captured.
	QueriedAt *time.Time `json:"queried_at,omitempty"`
}

type ResourceRef struct {
	Database   string
	Objects    []string
	CommandCls string
	// ReferencesTables is true when the statement references one or more tables
	// (it contains a FROM/JOIN/INTO/UPDATE/TABLE keyword). The scope gate uses
	// this to fail closed: a table-referencing statement whose objects could not
	// be parsed (empty Objects) is out-of-scope rather than silently in-scope.
	// A genuinely table-less read (e.g. "SELECT 1") has ReferencesTables=false.
	ReferencesTables bool
}

type Connector interface {
	Kind() SourceKind
	Ping(ctx context.Context, conn ConnConfig) error
	Classify(op Operation) (AccessMode, ResourceRef, error)
	Execute(ctx context.Context, conn ConnConfig, op Operation) (*ResultSet, error)
}
