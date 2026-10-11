package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrateExternalProviders_AddsBodyFingerprintColumns guards the upgrade
// path for the body fingerprint added in Epic #726 / #728. Fresh installs get
// the columns from schema.sql, but a database created before that has an
// external_api_audit table without them — and Call() now writes all three on
// every request, so a missing column would break the audit write (and, with it,
// every proxied call) on upgraded installs.
func TestMigrateExternalProviders_AddsBodyFingerprintColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	Driver = "sqlite"

	// The pre-#728 shape: no body_sha256 / body_bytes / body_fields.
	if _, err := db.Exec(`CREATE TABLE external_api_audit (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id     INTEGER NOT NULL,
		provider_id INTEGER NOT NULL,
		method      TEXT NOT NULL,
		path        TEXT NOT NULL,
		status_code INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code) VALUES (1, 1, 'GET', '/x', 200)`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := MigrateExternalProviders(db); err != nil {
		t.Fatalf("MigrateExternalProviders: %v", err)
	}

	for _, col := range []string{"body_sha256", "body_bytes", "body_fields"} {
		exists, err := columnExists(db, "external_api_audit", col)
		if err != nil {
			t.Fatalf("columnExists(%s): %v", col, err)
		}
		if !exists {
			t.Errorf("column %s was not added to external_api_audit", col)
		}
	}

	// The pre-existing row keeps its data and takes the column defaults, so an
	// upgraded feed shows it with an empty fingerprint rather than erroring.
	var sha, fields string
	var size int64
	if err := db.QueryRow(
		`SELECT body_sha256, body_bytes, body_fields FROM external_api_audit WHERE path = '/x'`,
	).Scan(&sha, &size, &fields); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if sha != "" || size != 0 || fields != "[]" {
		t.Errorf("migrated defaults = (%q, %d, %q), want (\"\", 0, \"[]\")", sha, size, fields)
	}

	// Idempotent: a second run must not error on the now-existing columns.
	if err := MigrateExternalProviders(db); err != nil {
		t.Fatalf("second MigrateExternalProviders: %v", err)
	}
}
