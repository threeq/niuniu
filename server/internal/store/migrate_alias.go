package store

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// migrateExternalCredentialAlias switches external_provider_credentials from
// single-credential (UNIQUE on owner/user/provider) to multi-credential + alias
// (UNIQUE on owner/user/provider/alias), and adds credential_id to
// project_external_sources. Idempotent via schema_migrations marker; the marker
// is only recorded when the driver-specific migration succeeds, so a partial
// failure retries on the next startup (all steps are individually idempotent).
func migrateExternalCredentialAlias(db *sql.DB) {
	const key = "external_credential_alias_v1"
	w := Wrap(db)
	if migrationApplied(w, key) {
		return
	}
	var err error
	if Driver == "postgres" {
		err = migrateExternalCredentialAliasPostgres(db)
	} else {
		err = migrateExternalCredentialAliasSQLite(db)
	}
	if err != nil {
		slog.Warn("external_credential_alias_v1 not applied (retries next start)", "error", err)
		return
	}
	markMigration(w, key)
}

func migrateExternalCredentialAliasSQLite(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Add alias column if missing
	addColumnIfNotExistsSQLite(tx, "external_provider_credentials", "alias", "TEXT NOT NULL DEFAULT ''")

	// 2. Backfill alias
	if _, err := tx.Exec(`UPDATE external_provider_credentials SET alias = CASE provider
		WHEN 'github' THEN 'GitHub' WHEN 'tapd' THEN 'TAPD' WHEN 'jira' THEN 'Jira' ELSE provider END
		WHERE alias = ''`); err != nil {
		return fmt.Errorf("backfill alias: %w", err)
	}

	// 3. Rebuild table to switch UNIQUE constraint (shadow-table swap)
	stmts := []string{
		`CREATE TABLE external_provider_credentials_new (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type       TEXT NOT NULL CHECK (owner_type IN ('user','org')),
			owner_id         INTEGER NOT NULL,
			user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider         TEXT NOT NULL,
			alias            TEXT NOT NULL DEFAULT '',
			config           TEXT NOT NULL DEFAULT '{}',
			last_verified_at TIMESTAMP DEFAULT NULL,
			created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, user_id, provider, alias)
		)`,
		`INSERT INTO external_provider_credentials_new
			(id, owner_type, owner_id, user_id, provider, alias, config, last_verified_at, created_at, updated_at)
		 SELECT id, owner_type, owner_id, user_id, provider, alias, config, last_verified_at, created_at, updated_at
		   FROM external_provider_credentials`,
		`DROP TABLE external_provider_credentials`,
		`ALTER TABLE external_provider_credentials_new RENAME TO external_provider_credentials`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild external_provider_credentials (%q...): %w", s[:min(60, len(s))], err)
		}
	}

	// 4. Add credential_id to project_external_sources
	addColumnIfNotExistsSQLite(tx, "project_external_sources", "credential_id", "INTEGER REFERENCES external_provider_credentials(id) ON DELETE RESTRICT")

	// 5. Backfill credential_id
	if _, err := tx.Exec(`UPDATE project_external_sources AS pes
		SET credential_id = (
			SELECT epc.id FROM external_provider_credentials epc
			JOIN projects p ON p.id = pes.project_id
			WHERE epc.owner_type = p.owner_type
			  AND epc.owner_id = p.owner_id
			  AND epc.provider = pes.provider
			ORDER BY epc.user_id ASC LIMIT 1
		) WHERE credential_id IS NULL`); err != nil {
		return fmt.Errorf("backfill credential_id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// 6. Log orphaned sources
	rows, err := db.Query(`SELECT id, project_id, provider, source_key FROM project_external_sources WHERE credential_id IS NULL`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, pid int64
			var prov, key string
			if err := rows.Scan(&id, &pid, &prov, &key); err == nil {
				slog.Warn("project_external_source orphaned after alias migration", "id", id, "project_id", pid, "provider", prov, "source_key", key)
			}
		}
	}

	// 7. Recreate indexes after table rebuild
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_external_creds_owner ON external_provider_credentials(owner_type, owner_id, user_id)`); err != nil {
		slog.Warn("recreate idx_external_creds_owner after alias migration failed", "error", err)
	}
	return nil
}

func migrateExternalCredentialAliasPostgres(db *sql.DB) error {
	// 1. Add alias column
	if _, err := db.Exec(`ALTER TABLE external_provider_credentials ADD COLUMN IF NOT EXISTS alias TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add alias column: %w", err)
	}

	// 2. Backfill alias
	if _, err := db.Exec(`UPDATE external_provider_credentials SET alias = CASE provider
		WHEN 'github' THEN 'GitHub' WHEN 'tapd' THEN 'TAPD' WHEN 'jira' THEN 'Jira' ELSE provider END
		WHERE alias = ''`); err != nil {
		return fmt.Errorf("backfill alias: %w", err)
	}

	// 3. Switch UNIQUE constraint
	var oldName string
	row := db.QueryRow(`SELECT tc.constraint_name FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu ON tc.constraint_name = ccu.constraint_name
		WHERE tc.table_name = 'external_provider_credentials' AND tc.constraint_type = 'UNIQUE'
		  AND ccu.column_name = 'provider'
		LIMIT 1`)
	if err := row.Scan(&oldName); err == nil && oldName != "" {
		if _, err := db.Exec(`ALTER TABLE external_provider_credentials DROP CONSTRAINT ` + oldName); err != nil {
			slog.Warn("PG drop old unique failed", "error", err, "constraint", oldName)
		}
	}
	// Postgres has no ADD CONSTRAINT IF NOT EXISTS — probe pg_constraint and
	// add it plainly when absent.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pg_constraint
		WHERE conname = 'external_provider_credentials_alias_unique'`).Scan(&n); err != nil {
		return fmt.Errorf("probe alias unique constraint: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE external_provider_credentials
			ADD CONSTRAINT external_provider_credentials_alias_unique
			UNIQUE (owner_type, owner_id, user_id, provider, alias)`); err != nil {
			return fmt.Errorf("add alias unique: %w", err)
		}
	}

	// 4. Add credential_id column
	if _, err := db.Exec(`ALTER TABLE project_external_sources
		ADD COLUMN IF NOT EXISTS credential_id BIGINT REFERENCES external_provider_credentials(id) ON DELETE RESTRICT`); err != nil {
		return fmt.Errorf("add credential_id: %w", err)
	}

	// 5. Backfill credential_id
	if _, err := db.Exec(`UPDATE project_external_sources AS pes
		SET credential_id = sub.cred_id FROM (
			SELECT pes2.id AS source_id, epc.id AS cred_id
			FROM project_external_sources pes2
			JOIN projects p ON p.id = pes2.project_id
			JOIN external_provider_credentials epc ON epc.owner_type = p.owner_type
				AND epc.owner_id = p.owner_id AND epc.provider = pes2.provider
			WHERE pes2.credential_id IS NULL
		) sub WHERE pes.id = sub.source_id`); err != nil {
		return fmt.Errorf("backfill credential_id: %w", err)
	}

	// 6. Log orphaned sources
	rows, err := db.Query(`SELECT id, project_id, provider, source_key FROM project_external_sources WHERE credential_id IS NULL`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, pid int64
			var prov, key string
			if err := rows.Scan(&id, &pid, &prov, &key); err == nil {
				slog.Warn("project_external_source orphaned after alias migration", "id", id, "project_id", pid, "provider", prov, "source_key", key)
			}
		}
	}
	return nil
}

// addColumnIfNotExistsSQLite adds a column inside an active transaction using
// PRAGMA table_info to probe. Does nothing if the column already exists.
func addColumnIfNotExistsSQLite(tx *sql.Tx, table, column, colDef string) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil {
			if name == column {
				return // already exists
			}
		}
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + colDef); err != nil {
		slog.Warn("add column failed", "table", table, "column", column, "error", err)
	}
}