package store

import "testing"

// Equivalent SQLite vs PostgreSQL column definitions must NOT be reported as
// drift. Each pair below is a token the two schemas express differently but that
// is semantically identical; before canonicalizeTypes was applied to both sides
// (and before the type table was completed) every one of these read as a false
// DIFFER.
func TestSchemaDiff_EquivalentTypesAreNotDrift(t *testing.T) {
	sqlite := `
CREATE TABLE IF NOT EXISTS sample (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    flag          BOOLEAN DEFAULT FALSE,
    cost          REAL NOT NULL DEFAULT 0,
    payload       TEXT NOT NULL DEFAULT '[]',
    secret        BLOB,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`
	pg := `
CREATE TABLE IF NOT EXISTS sample (
    id            BIGSERIAL PRIMARY KEY,
    owner_id      BIGINT NOT NULL,
    flag          BOOLEAN DEFAULT FALSE,
    cost          DOUBLE PRECISION NOT NULL DEFAULT 0,
    payload       JSONB NOT NULL DEFAULT '[]'::jsonb,
    secret        BYTEA,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`
	// owner_id's FK is inline in SQLite and (in the real schema) a deferred
	// ALTER on the PG side; stripInlineFKs makes the column comparable.
	sqliteOnly, pgOnly, differing := SchemaDiff(sqlite, pg)
	if len(sqliteOnly) != 0 || len(pgOnly) != 0 {
		t.Fatalf("unexpected table presence diff: sqliteOnly=%v pgOnly=%v", sqliteOnly, pgOnly)
	}
	if len(differing) != 0 {
		t.Fatalf("equivalent definitions reported as drift: %v", differing)
	}
}

// Genuine structural differences (a nullability flip, a different default) MUST
// still be reported — the canonicalization must not paper over real drift.
func TestSchemaDiff_RealDriftIsReported(t *testing.T) {
	cases := map[string][2]string{
		"nullability": {
			`CREATE TABLE IF NOT EXISTS t (expires_at TIMESTAMP NOT NULL);`,
			`CREATE TABLE IF NOT EXISTS t (expires_at TIMESTAMPTZ);`,
		},
		"default": {
			`CREATE TABLE IF NOT EXISTS t (base TEXT NOT NULL DEFAULT '');`,
			`CREATE TABLE IF NOT EXISTS t (base TEXT NOT NULL DEFAULT '{}');`,
		},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, differing := SchemaDiff(pair[0], pair[1])
			if _, ok := differing["t"]; !ok {
				t.Fatalf("expected real drift to be reported for %q, got none", name)
			}
		})
	}
}
