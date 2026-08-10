package dataconn

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// SQLConnector implements Connector for mysql/postgres and all compat variants.
type SQLConnector struct {
	kind       SourceKind
	open       func(dsn string) (*sql.DB, error)
	dsn        func(c ConnConfig) string
	readOnlyTx bool
}

func (s *SQLConnector) Kind() SourceKind { return s.kind }

func (s *SQLConnector) Classify(op Operation) (AccessMode, ResourceRef, error) {
	mode, ref, err := classifySQL(op.Statement)
	if err != nil {
		return "", ResourceRef{}, err
	}
	ref.Database = op.Database
	return mode, ref, nil
}

func (s *SQLConnector) Ping(ctx context.Context, conn ConnConfig) error {
	db, err := s.open(s.dsn(conn))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}

// Execute runs the statement and normalizes rows.
// When readOnlyTx is true (MySQL/Postgres and all compat variants) the query
// is wrapped in a READ ONLY transaction as defense-in-depth; the service gate
// rejects write statements before reaching here.
// When readOnlyTx is false (ClickHouse, MSSQL) the query runs directly because
// those drivers do not support sql.TxOptions{ReadOnly:true}; the service gate
// is still the write-statement barrier.
func (s *SQLConnector) Execute(ctx context.Context, conn ConnConfig, op Operation) (*ResultSet, error) {
	db, err := s.open(s.dsn(conn))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	to := time.Duration(op.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	limit := op.RowLimit
	if limit <= 0 {
		limit = 1000
	}

	if s.readOnlyTx {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, op.Statement, op.Params...)
		if err != nil {
			return nil, err
		}
		return s.collectRows(rows, limit)
	}

	// readOnlyTx=false: run directly (service gate already blocks writes).
	rows, err := db.QueryContext(ctx, op.Statement, op.Params...)
	if err != nil {
		return nil, err
	}
	return s.collectRows(rows, limit)
}

// collectRows closes rows and builds a ResultSet tagged with this connector's engine.
func (s *SQLConnector) collectRows(rows *sql.Rows, limit int) (*ResultSet, error) {
	defer rows.Close()
	rs, err := scanRows(rows, limit)
	if err != nil {
		return nil, err
	}
	rs.Engine = string(s.kind)
	return rs, nil
}

// executeQuery is the testable inner scan path (no tx, used by sqlite tests).
func executeQuery(ctx context.Context, db *sql.DB, stmt string, params []any, limit int) (*ResultSet, error) {
	rows, err := db.QueryContext(ctx, stmt, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows, limit)
}

func scanRows(rows *sql.Rows, limit int) (*ResultSet, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	rs := &ResultSet{Columns: make([]Column, len(cols))}
	for i, c := range cols {
		rs.Columns[i] = Column{Name: c, Type: "string"}
	}
	for rows.Next() {
		if len(rs.Rows) >= limit {
			rs.Truncated = true
			break
		}
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(cols))
		for i, v := range holders {
			row[i] = normalizeCell(v, &rs.Columns[i])
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs, rows.Err()
}

// normalizeCell coerces driver values into JSON-friendly forms and refines the
// column type from the first non-null value seen.
func normalizeCell(v any, col *Column) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		col.Type = "string"
		return string(x)
	case int64, int32, int, float64, float32:
		col.Type = "number"
		return x
	case bool:
		col.Type = "bool"
		return x
	case time.Time:
		col.Type = "time"
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// NewMySQLConnector / NewPostgresConnector build the connectors for the two
// natively-supported SQL dialects. MySQL uses a custom DSN format (not URL);
// PostgreSQL uses a URL which must be built with url.UserPassword to safely
// handle passwords containing special characters (@, /, #, ?).
func NewMySQLConnector() *SQLConnector {
	return &SQLConnector{
		kind:       KindMySQL,
		readOnlyTx: true,
		open:       func(dsn string) (*sql.DB, error) { return sql.Open("mysql", dsn) },
		dsn: func(c ConnConfig) string {
			port := c.Port
			if port == 0 {
				port = 3306
			}
			return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=8s", c.User, c.Password, c.Host, port, c.Database)
		},
	}
}

func NewPostgresConnector() *SQLConnector {
	return &SQLConnector{
		kind:       KindPostgres,
		readOnlyTx: true,
		open:       func(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) },
		dsn:        postgresDSN(5432),
	}
}

// postgresDSN returns a DSN-builder for PostgreSQL-protocol drivers. Credentials
// are URL-encoded via url.UserPassword so special characters in passwords
// (@, /, #, ?) do not break the postgres:// URL.
func postgresDSN(defaultPort int) func(ConnConfig) string {
	return func(c ConnConfig) string {
		port := c.Port
		if port == 0 {
			port = defaultPort
		}
		u := &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(c.User, c.Password),
			Host:   fmt.Sprintf("%s:%d", c.Host, port),
			Path:   c.Database,
		}
		return u.String()
	}
}

// NewMySQLCompatConnector builds a connector for MySQL-protocol-compatible databases
// (MariaDB, TiDB, OceanBase, StarRocks, Doris). They share the go-sql-driver/mysql
// driver; only the kind label and default port differ.
// Panics if kind is not in the defaultPorts map — this is a programming error
// (a new SourceKind must be added to the map before using this constructor).
func NewMySQLCompatConnector(kind SourceKind) *SQLConnector {
	defaultPorts := map[SourceKind]int{
		KindMariaDB:   3306,
		KindTiDB:      4000,
		KindOceanBase: 2881,
		KindStarRocks: 9030,
		KindDoris:     9030,
	}
	port, ok := defaultPorts[kind]
	if !ok {
		panic(fmt.Sprintf("dataconn: NewMySQLCompatConnector: unknown kind %q — add to defaultPorts", kind))
	}
	return &SQLConnector{
		kind:       kind,
		readOnlyTx: true,
		open:       func(dsn string) (*sql.DB, error) { return sql.Open("mysql", dsn) },
		dsn: func(c ConnConfig) string {
			p := c.Port
			if p == 0 {
				p = port
			}
			return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=8s", c.User, c.Password, c.Host, p, c.Database)
		},
	}
}

// NewPostgresCompatConnector builds a connector for PostgreSQL-protocol-compatible
// databases (CockroachDB, Greenplum, Redshift, openGauss, PolarDB-PG, YugabyteDB).
// They reuse the already-imported pgx/v5 driver; only the kind label and default
// port differ.
// Panics if kind is not in the defaultPorts map — programming error.
func NewPostgresCompatConnector(kind SourceKind) *SQLConnector {
	defaultPorts := map[SourceKind]int{
		KindCockroachDB: 26257,
		KindGreenplum:   5432,
		KindRedshift:    5439,
		KindOpenGauss:   5432,
		KindPolarDBPG:   5432,
		KindYugabyte:    5433,
	}
	port, ok := defaultPorts[kind]
	if !ok {
		panic(fmt.Sprintf("dataconn: NewPostgresCompatConnector: unknown kind %q — add to defaultPorts", kind))
	}
	return &SQLConnector{
		kind:       kind,
		readOnlyTx: true,
		open:       func(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) },
		dsn:        postgresDSN(port),
	}
}
