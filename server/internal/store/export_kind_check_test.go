package store

import "database/sql"

// ExpandDataSourcesKindCheckPostgresForTest exposes the unexported PG kind
// CHECK migration to the external store_test package (the PG harness lives in
// internal/pgtest, which imports store — an internal test would cycle).
func ExpandDataSourcesKindCheckPostgresForTest(db *sql.DB) {
	expandDataSourcesKindCheckPostgres(db)
}
