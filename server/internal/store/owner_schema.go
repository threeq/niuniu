package store

import (
	"database/sql"
	"fmt"
	"log/slog"
)

func execOrWarn(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		slog.Warn("owner_schema exec failed", "query", q, "error", err)
	}
}

// execOrError runs q and logs at Error level on failure. Use for DDL whose
// absence would silently break a critical invariant (e.g. per-owner UNIQUE
// indexes that enforce multi-tenant isolation).
func execOrError(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		slog.Error("owner_schema exec failed — multi-tenant invariant may be broken", "query", q, "error", err)
	}
}

// topLevelOwnedTables lists every table that receives (owner_type, owner_id).
// Order matters only for readability.
// Note: 'harnesses' and 'teams' were dropped in Phase 7 (drop_legacy_phase7_v1)
// and are no longer listed here.
var topLevelOwnedTables = []string{
	"projects",
	"repositories",
	"workspaces",
	"env_presets",
	"quick_actions",
	// harness_specs is intentionally NOT here: it is a single GLOBAL library
	// (owner columns dropped by MigrateDropHarnessSpecOwner). Listing it would
	// re-add owner_type/owner_id on every startup via addColumnIfNotExists.
	"agents",
	"scenes", // M1 scene-based MCP/plugin management (2026-05-18)
	"data_sources", // data integration M1 (2026-06-04)
	"saved_queries", // data dashboards M1 (2026-06-04)
	"dashboards", // data dashboards M1 (2026-06-04)
	"knowledge_bases", // KB base1: owner-scoped knowledge stores (2026-06-30)
}

// addOwnerModel adds owner_type / owner_id columns + the composite index to
// every top-level table, plus the session / attribution columns. Idempotent.
// See design spec §3.2 and §3.6.
func addOwnerModel(db *sql.DB, fk string) {
	for _, tbl := range topLevelOwnedTables {
		addColumnIfNotExists(db, tbl,
			"owner_type", "TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org'))")
		addColumnIfNotExists(db, tbl,
			"owner_id", fk+" NOT NULL DEFAULT 0")

		idx := fmt.Sprintf("idx_%s_owner", tbl)
		if _, err := db.Exec(fmt.Sprintf(
			"CREATE INDEX IF NOT EXISTS %s ON %s(owner_type, owner_id)", idx, tbl)); err != nil {
			slog.Warn("create owner index failed", "table", tbl, "error", err)
		}
	}

	// Session identity on workspaces (spec §3.5)
	addColumnIfNotExists(db, "workspaces", "current_session_user_id",
		fk+" DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL")

	// Action attribution (spec §3.6)
	addColumnIfNotExists(db, "workspace_schedules", "triggered_by",
		fk+" DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL")
	addColumnIfNotExists(db, "schedule_runs", "actor_user_id",
		fk+" DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL")
	addColumnIfNotExists(db, "agent_messages", "author_user_id",
		fk+" DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL")

	// users.external_account_id (spec §3.9) — reserved for future
	addColumnIfNotExists(db, "users", "external_account_id", "TEXT DEFAULT NULL")
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_external_account
		ON users(external_account_id) WHERE external_account_id IS NOT NULL`); err != nil {
		slog.Warn("create idx_users_external_account failed", "error", err)
	}

	// Per-owner UNIQUE indexes (spec §3.7).
	//
	// The column-level UNIQUE constraints on projects.name and repositories.path
	// have been removed from schema.sql / schema_postgres.sql and are dropped on
	// existing databases via dropLegacyUniqueConstraints (called from Migrate).
	// Only the per-owner composite indexes below are enforced.
	//
	// These indexes are idempotent (CREATE ... IF NOT EXISTS).
	execOrError(db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_owner_name_unique
		ON projects(owner_type, owner_id, name)`)
	// Partial: local-only repos (empty git_remote) don't conflict with each other.
	execOrError(db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_owner_remote_unique
		ON repositories(owner_type, owner_id, git_remote) WHERE git_remote != ''`)
	execOrError(db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_quick_actions_owner_label_unique
		ON quick_actions(owner_type, owner_id, label)`)
	execOrError(db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_env_presets_owner_name_unique
		ON env_presets(owner_type, owner_id, name)`)
	// idx_teams_owner_name_unique and idx_harnesses_owner_name_unique removed in
	// Phase 7: those tables are dropped by drop_legacy_phase7_v1 migration.
	// harness_specs owner-scope index removed: it is a single global library now.
	execOrError(db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_owner_name_unique
		ON agents(owner_type, owner_id, name)`)
}
