package dataconn

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE orders(id INTEGER, amt REAL); INSERT INTO orders VALUES (1,10.5),(2,20.0),(3,30.0)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestExecuteRows_NormalizeAndLimit(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	rs, err := executeQuery(context.Background(), db, "SELECT id, amt FROM orders ORDER BY id", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Columns) != 2 || rs.Columns[0].Name != "id" {
		t.Fatalf("columns: %+v", rs.Columns)
	}
	if len(rs.Rows) != 2 || !rs.Truncated {
		t.Fatalf("expected 2 rows + truncated, got %d trunc=%v", len(rs.Rows), rs.Truncated)
	}
}

// TestExecuteWriteMode_RunsCommitted locks the write path: a write on a
// readwrite source must bypass the read-only transaction, execute against the
// database, and report rows_affected.
func TestExecuteWriteMode_RunsCommitted(t *testing.T) {
	// A file-backed DB: Execute opens and closes its own *sql.DB, so every
	// call must see the same committed state.
	path := filepath.Join(t.TempDir(), "orders.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`CREATE TABLE orders(id INTEGER, amt REAL); INSERT INTO orders VALUES (1,10.5),(2,20.0),(3,30.0)`); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	c := &SQLConnector{
		kind:       KindMySQL,
		readOnlyTx: true,
		open:       func(string) (*sql.DB, error) { return sql.Open("sqlite", path) },
		dsn:        func(ConnConfig) string { return "" },
	}
	rs, err := c.Execute(context.Background(), ConnConfig{}, Operation{
		Statement: "UPDATE orders SET amt = amt + 1 WHERE id <= 2",
		Mode:      ModeWrite,
	})
	if err != nil {
		t.Fatalf("write execute: %v", err)
	}
	if rs.RowsAffected != 2 {
		t.Fatalf("rows_affected: got %d want 2", rs.RowsAffected)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var updated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orders WHERE amt IN (11.5, 21.0)`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("write was not committed: %d rows updated", updated)
	}
}

func TestSQLCompatConstructors(t *testing.T) {
	cases := []struct {
		kind       SourceKind
		wantPort   int
		readOnlyTx bool
	}{
		{KindMySQL, 3306, true},
		{KindPostgres, 5432, true},
		{KindMariaDB, 3306, true},
		{KindTiDB, 4000, true},
		{KindOceanBase, 2881, true},
		{KindStarRocks, 9030, true},
		{KindDoris, 9030, true},
		{KindCockroachDB, 26257, true},
		{KindGreenplum, 5432, true},
		{KindRedshift, 5439, true},
		{KindOpenGauss, 5432, true},
		{KindPolarDBPG, 5432, true},
		{KindYugabyte, 5433, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			var c *SQLConnector
			switch tc.kind {
			case KindMySQL:
				c = NewMySQLConnector()
			case KindPostgres:
				c = NewPostgresConnector()
			case KindMariaDB, KindTiDB, KindOceanBase, KindStarRocks, KindDoris:
				c = NewMySQLCompatConnector(tc.kind)
			default:
				c = NewPostgresCompatConnector(tc.kind)
			}
			if c.Kind() != tc.kind {
				t.Errorf("kind: got %s want %s", c.Kind(), tc.kind)
			}
			if c.readOnlyTx != tc.readOnlyTx {
				t.Errorf("readOnlyTx: got %v want %v", c.readOnlyTx, tc.readOnlyTx)
			}
			dsn := c.dsn(ConnConfig{User: "u", Password: "p", Host: "h", Port: 0, Database: "db"})
			portStr := fmt.Sprintf("%d", tc.wantPort)
			if !strings.Contains(dsn, portStr) {
				t.Errorf("DSN %q should contain default port %s", dsn, portStr)
			}
		})
	}
}
