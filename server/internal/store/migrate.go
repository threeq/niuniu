package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/claudehome"
)

// fkType returns "BIGINT" for PostgreSQL (to match BIGSERIAL PKs) or "INTEGER" for SQLite.
func fkType() string {
	if Driver == "postgres" {
		return "BIGINT"
	}
	return "INTEGER"
}

func Migrate(db *sql.DB) {
	fk := fkType()

	// NOTE: workspaces.harness_id is NOT added here — it was a legacy column
	// dropped in Phase 7 (drop_legacy_phase7_v1). On existing DBs that still
	// have it, the column persists until MigrateDropLegacyPhase7 runs.

	// Add harness_run_id column to agent_messages if missing (existing databases).
	addColumnIfNotExists(db, "agent_messages", "harness_run_id", fk+" DEFAULT NULL")

	// Add harness_run_id column to workspace_costs if missing (existing databases).
	addColumnIfNotExists(db, "workspace_costs", "harness_run_id", fk+" DEFAULT NULL")

	// Add harness_run_id column to workspace_tasks if missing (existing databases).
	addColumnIfNotExists(db, "workspace_tasks", "harness_run_id", fk+" DEFAULT NULL")

	// NOTE: harness_phases.auto_advance and harness_phase_agents.agent_id are NOT
	// added here — those tables were dropped in Phase 7 (drop_legacy_phase7_v1).

	// Add repo column to comments so line-level review comments anchor to
	// repo + file_path + line in multi-repo workspaces (existing databases).
	addColumnIfNotExists(db, "comments", "repo", "TEXT NOT NULL DEFAULT ''")

	// Add attachments column (JSON) to agent_messages for file attachment metadata.
	addColumnIfNotExists(db, "agent_messages", "attachments", "TEXT DEFAULT NULL")

	// Add attachments column to workspace_queue so queued messages retain attachment metadata.
	addColumnIfNotExists(db, "workspace_queue", "attachments", "TEXT DEFAULT NULL")

	// Add driver and capabilities columns to agents for multi-agent team support.
	addColumnIfNotExists(db, "agents", "driver", "TEXT NOT NULL DEFAULT 'claude-cli'")
	addColumnIfNotExists(db, "agents", "capabilities", "TEXT NOT NULL DEFAULT ''")

	// Add workspace_agent_id to agent_messages for tracking which agent in a team sent the message.
	addColumnIfNotExists(db, "agent_messages", "workspace_agent_id", fk)

	// Memory auto-evolution signals (existing DBs that predate the feature). No
	// CHECK to retrofit; superseded_by mirrors the parent_issue_id self-FK precedent.
	addColumnIfNotExists(db, "memories", "reinforce_count", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "memories", "last_reinforced_at", "TIMESTAMP")
	addColumnIfNotExists(db, "memories", "superseded_by", fk+" DEFAULT NULL REFERENCES memories(id) ON DELETE SET NULL")

	// Staleness management signals (existing DBs that predate memory_staleness.go).
	// memory_sweep_runs is net-new so schema.sql creates it; only these columns
	// need a retrofit on already-populated databases.
	addColumnIfNotExists(db, "memories", "pending_review", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "memories", "stale_reason", "TEXT NOT NULL DEFAULT ''")

	// Per-project automatic memory-maintenance schedule (existing DBs predate it).
	// Empty = OFF (the default); a non-empty cron opts the project in.
	addColumnIfNotExists(db, "projects", "memory_sweep_cron", "TEXT NOT NULL DEFAULT ''")

	// Per-project workspace auto-cleanup policy (existing DBs predate it).
	// cleanup_enabled=0 is OFF (the default); a project opts in via settings.
	addColumnIfNotExists(db, "projects", "cleanup_enabled", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "projects", "cleanup_inactive_days", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "projects", "cleanup_statuses", "TEXT NOT NULL DEFAULT 'completed,not_started'")

	// NOTE: harness_phase_agents.agent_id is NOT added here — table dropped in Phase 7.

	// NOTE: idx_agent_messages_harness_run is intentionally NOT created here.
	// agent_messages.harness_run_id is a retained-but-dead legacy column
	// (workflow subsystem decommissioned); the index is dropped by the
	// drop_workflow_tables_v1 migration and must not be recreated.

	// NOTE: workspaces.team_id is NOT added here — it was a legacy column dropped
	// in Phase 7 (drop_legacy_phase7_v1). The REFERENCES teams(id) FK is also gone.

	// Fix workspaces.issue_id FK to cascade nullify when an issue is deleted.
	if Driver == "postgres" {
		fixWorkspacesIssueFKPostgres(db)
	} else {
		fixWorkspacesIssueFKSQLite(db)
	}

	addOwnerModel(db, fk)

	// Drop legacy column-level UNIQUE constraints on projects.name and
	// repositories.path. These were global uniqueness guards that break
	// multi-tenant use — per-owner composite indexes were already added
	// above by addOwnerModel. New installs get no UNIQUE from schema.sql;
	// this migration handles existing databases.
	dropLegacyUniqueConstraints(db)

	// 2026-05-01 issue properties phase 1: add labels / issue_labels / issue_assignees
	migrateIssueAssigneeLabelsAddTables(db)

	// 2026-05-01 issue properties phase 2: drop legacy issues.{labels,assignee}.
	// Phase 1 moved all consumers to the new join tables; this drops the dead columns
	// from existing DBs. Driver-aware (both sqlite ≥ 3.35 and Postgres support DROP COLUMN).
	migrateIssueAssigneeLabelsDropLegacyColumns(db)

	// 2026-06 decommission: the column-level executor_agent (run-template / column
	// agent-picker era) is retired — no UI sets it and nothing reads it (AI-native
	// execution keys off op_primitive == 'instruct'). Drop the dead column from
	// existing DBs; new DBs never create it. Unconstrained TEXT column, so the bare
	// DROP COLUMN suffices on both drivers.
	if err := dropColumnIfExists(db, "columns", "executor_agent"); err != nil {
		slog.Warn("drop columns.executor_agent failed (inert column remains)", "err", err)
	}

	// Backfill workspaces.claude_account_id from the owner's active account.
	// Workspaces created before strong-binding was introduced have NULL here;
	// migrate them so ResolveForWorkspace no longer needs an owner-level fallback.
	w := Wrap(db)
	if !migrationApplied(w, "workspaces_claude_account_id_backfill_v1") {
		if _, err := w.ExecContext(context.Background(),
			`UPDATE workspaces SET claude_account_id = (
				SELECT account_id FROM claude_active_account
				WHERE owner_type = workspaces.owner_type AND owner_id = workspaces.owner_id
			)
			WHERE claude_account_id IS NULL`); err != nil {
			slog.Warn("backfill workspaces.claude_account_id failed", "err", err)
		} else {
			markMigration(w, "workspaces_claude_account_id_backfill_v1")
		}
	}

	// 2026-06-01 token usage stats: widen token/duration columns to BIGINT on
	// Postgres (int4 overflows on cumulative token/duration sums), then seed
	// workspace_stats once from existing data.
	widenTokenColumnsPostgres(db)
	backfillWorkspaceStats(db)

	// Add created_by column to workspaces and backfill personal-space rows.
	addColumnIfNotExists(db, "workspaces", "created_by", fk+" DEFAULT NULL")

	// Add goal_condition to issues for autohost LLM judge.
	// Spec: docs/superpowers/specs/2026-05-14-autohost-llm-judge-design.md §2.3.1
	addColumnIfNotExists(db, "issues", "goal_condition", "TEXT NOT NULL DEFAULT ''")

	// Executable Epic (E1): parent_issue_id + issue_type + exec_wave + exec_status
	// on issues. parent_issue_id is a self-FK (NULL = top-level); issue_type marks
	// an issue as an Epic; exec_wave is the child execution wave (same wave = parallel,
	// cross wave = serial); exec_status tracks Epic execution lifecycle. The CHECK on
	// issue_type lives only in schema.sql / schema_postgres.sql for fresh DBs; on
	// existing DBs the bare column is added with a 'task' default (every row valid).
	addColumnIfNotExists(db, "issues", "parent_issue_id", fk+" DEFAULT NULL REFERENCES issues(id) ON DELETE SET NULL")
	addColumnIfNotExists(db, "issues", "issue_type", "TEXT NOT NULL DEFAULT 'task'")
	addColumnIfNotExists(db, "issues", "exec_wave", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "issues", "exec_status", "TEXT NOT NULL DEFAULT 'idle'")

	// Add created_by to issues: records the user who created the issue, used as a
	// fallback when determining a derived workspace's creator (工作空间创建人确定顺序:
	// 当前登录用户 -> issue 创建用户 -> epic 创建用户). nullable; ON DELETE SET NULL is
	// carried by schema.sql on fresh DBs and by the PG deferred FK below. No
	// backfill: pre-existing issues have no recorded creator and stay NULL (their
	// derived workspaces keep the prior owner/NULL fallback behavior).
	addColumnIfNotExists(db, "issues", "created_by", fk+" DEFAULT NULL")

	// Index for child-by-parent lookups (Epic -> children). Lives here, not in the
	// schema files, because parent_issue_id is a migration-added column on existing
	// DBs (CREATE INDEX in schema.sql would fail on a DB whose issues table predates
	// the column). Both SQLite and PostgreSQL accept CREATE INDEX IF NOT EXISTS.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_parent ON issues(parent_issue_id)`); err != nil {
		slog.Warn("create idx_issues_parent failed", "error", err)
	}

	// The autohost LLM judge has been removed; drop its event table on existing
	// DBs. DROP TABLE IF EXISTS is valid on both SQLite and PostgreSQL, so a
	// single EnsureMigration covers both drivers. Idempotent via the migration
	// key; the table CREATE was already removed from schema.sql / schema_postgres.sql.
	if err := EnsureMigration(w, "drop_autohost_judge_events",
		`DROP TABLE IF EXISTS autohost_judge_events`); err != nil {
		slog.Error("drop_autohost_judge_events migration failed", "error", err)
	}

	// Data-source visibility is now project-only (mirrors external sources):
	// scene and workspace-direct bindings were dropped (scenes are high-risk).
	// Purge any non-project bindings created while those target types were
	// briefly allowed; the CHECK in schema.sql / schema_postgres.sql now permits
	// only 'project' and the service validation rejects the rest. DELETE is
	// valid on both drivers.
	if err := EnsureMigration(w, "data_source_bindings_project_only",
		`DELETE FROM data_source_bindings WHERE target_type <> 'project'`); err != nil {
		slog.Error("data_source_bindings_project_only migration failed", "error", err)
	}

	// Expand data_sources.kind CHECK to include all 15 SQL kinds plus the
	// NoSQL/federation kinds (redis/mongo/trino/elasticsearch, Epic #345) and
	// the generic http kind (ws-670).
	if Driver == "postgres" {
		expandDataSourcesKindCheckPostgres(db)
	} else {
		expandDataSourcesKindCheckSQLite(db)
	}

	// Add mcp_servers column to workspaces for per-workspace MCP server config.
	// Spec: docs/superpowers/specs/2026-05-17-per-workspace-mcp-config-design.md
	// SQLite stores JSON as TEXT; PostgreSQL uses JSONB.
	addMcpServersToWorkspaces(context.Background(), db)

	if !migrationApplied(w, "workspaces_created_by_backfill_v1") {
		if _, err := w.ExecContext(context.Background(),
			`UPDATE workspaces SET created_by = owner_id
			 WHERE owner_type = 'user' AND created_by IS NULL`); err != nil {
			slog.Warn("backfill workspaces.created_by failed", "error", err)
		} else {
			markMigration(w, "workspaces_created_by_backfill_v1")
		}
	}

	// Index lives in schema.sql for fresh DBs; ensure it on existing DBs too.
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_workspaces_created_by ON workspaces(created_by)`); err != nil {
		slog.Warn("create idx_workspaces_created_by failed", "error", err)
	}

	// One-time best-effort backfill of the issue/workspace creator chain so the
	// new "工作空间创建人确定顺序" feature also takes effect on EXISTING data
	// (otherwise historical 未指定 cards stay 未指定). Two ordered steps, both
	// touching only NULL rows so they're idempotent under the migration guard:
	//   1) Recover an issue's creator from its (1:1) workspace's created_by — the
	//      only historical signal for who owns an issue. The workspace creator is
	//      a best-effort proxy for the issue creator; on existing data they are
	//      the same human in the overwhelmingly common case.
	//   2) Fill NULL workspaces.created_by from the now-recovered issue, else its
	//      governing epic (parent issue) — the same chain Create applies going
	//      forward. This recovers child workspaces whose epic creator was just
	//      restored in step 1. Workspaces with no recoverable creator stay NULL.
	if !migrationApplied(w, "issue_workspace_creator_backfill_v1") {
		step1ok, step2ok := true, true
		if _, err := w.ExecContext(context.Background(),
			`UPDATE issues SET created_by = (
				SELECT w.created_by FROM workspaces w
				WHERE w.issue_id = issues.id AND w.created_by IS NOT NULL
				LIMIT 1)
			WHERE created_by IS NULL
			  AND EXISTS (SELECT 1 FROM workspaces w
			              WHERE w.issue_id = issues.id AND w.created_by IS NOT NULL)`); err != nil {
			step1ok = false
			slog.Warn("backfill issues.created_by from workspace failed", "error", err)
		}
		if _, err := w.ExecContext(context.Background(),
			`UPDATE workspaces SET created_by = (
				SELECT COALESCE(i.created_by, p.created_by)
				FROM issues i LEFT JOIN issues p ON p.id = i.parent_issue_id
				WHERE i.id = workspaces.issue_id)
			WHERE created_by IS NULL AND issue_id IS NOT NULL
			  AND EXISTS (
				SELECT 1 FROM issues i LEFT JOIN issues p ON p.id = i.parent_issue_id
				WHERE i.id = workspaces.issue_id
				  AND COALESCE(i.created_by, p.created_by) IS NOT NULL)`); err != nil {
			step2ok = false
			slog.Warn("backfill workspaces.created_by from issue/epic failed", "error", err)
		}
		if step1ok && step2ok {
			markMigration(w, "issue_workspace_creator_backfill_v1")
		}
	}

	// PostgreSQL: ensure the FK constraint on created_by exists. The deferred
	// FK block at the bottom of schema_postgres.sql guards on column-existence,
	// so on a first deploy where created_by was just added by addColumnIfNotExists
	// (i.e., AFTER schema execution finished), the FK won't have been installed
	// yet. Add it here so the FK is in place after this Migrate run completes,
	// not on the next restart.
	if Driver == "postgres" {
		var dummy int
		err := w.QueryRowContext(context.Background(),
			`SELECT 1 FROM pg_constraint WHERE conname = 'fk_workspaces_created_by'`).Scan(&dummy)
		if err == sql.ErrNoRows {
			if _, err := w.ExecContext(context.Background(),
				`ALTER TABLE workspaces ADD CONSTRAINT fk_workspaces_created_by
				 FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL`); err != nil {
				slog.Warn("add fk_workspaces_created_by failed", "error", err)
			}
		}
	}

	// PostgreSQL: ensure the FK on issues.created_by exists. Same rationale as
	// workspaces above — the deferred FK block in schema_postgres.sql guards on
	// column existence, so on a first deploy where created_by was just added by
	// addColumnIfNotExists (AFTER schema execution), the FK isn't installed yet.
	if Driver == "postgres" {
		var dummy int
		err := w.QueryRowContext(context.Background(),
			`SELECT 1 FROM pg_constraint WHERE conname = 'fk_issues_created_by'`).Scan(&dummy)
		if err == sql.ErrNoRows {
			if _, err := w.ExecContext(context.Background(),
				`ALTER TABLE issues ADD CONSTRAINT fk_issues_created_by
				 FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL`); err != nil {
				slog.Warn("add fk_issues_created_by failed", "error", err)
			}
		}
	}

	// Reconcile workspaces FK on SQLite-upgraded DBs. addColumnIfNotExists
	// declares created_by without REFERENCES (FKs added via ALTER TABLE on a
	// live SQLite table aren't supported), so existing DBs upgraded across
	// this migration miss the ON DELETE SET NULL behavior the schema.sql
	// canonical declaration carries. Reconciliation does the standard SQLite
	// 12-step ALTER TABLE rebuild — driver-gated, idempotent.
	if Driver == "sqlite" {
		reconcileWorkspacesFKsSQLite(db)
	}

	// Placeholder migration step for claude_accounts. Schema is created via
	// schema.sql / schema_postgres.sql in Open(). This step exists to register
	// the migration point for future seed logic (Task 4).
	migrateClaudeAccountsStep(db)

	// Per-user git authorship: users.email column for `git commit` author email.
	// Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
	// Phase 0 (authorship only). Empty string = unset; service layer falls back
	// to "<username>@niuniu.local" so commits still succeed.
	addColumnIfNotExists(db, "users", "email", "TEXT NOT NULL DEFAULT ''")

	// External integration indexes. Live in Migrate() (not the schema files)
	// because they reference columns added by addIssueExternalColumns /
	// addIssueCommentWritebackColumns in Open(). Partial-index WHERE clauses
	// are supported by both SQLite and PostgreSQL — no driver fallback.
	if _, err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS uq_issues_external_per_project
		ON issues(external_source, external_id, column_id)
		WHERE external_source != ''
	`); err != nil {
		slog.Warn("create uq_issues_external_per_project failed", "error", err)
	}

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_issues_external_lookup
		ON issues(external_source, external_id)
		WHERE external_source != ''
	`); err != nil {
		slog.Warn("create idx_issues_external_lookup failed", "error", err)
	}

	// idx_issue_comments_writeback was an index on the now-deleted
	// external_writeback_status column; the writeback subsystem itself
	// was removed in the M2/M3 cleanup. Drop the index if present so the
	// upcoming DROP COLUMN on issue_comments doesn't trip on a residual
	// dependency. Both SQLite and PostgreSQL accept DROP INDEX IF EXISTS.
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_issue_comments_writeback`); err != nil {
		slog.Warn("drop idx_issue_comments_writeback failed", "error", err)
	}

	// Drop the writeback columns left behind by the M2/M3 writeback
	// subsystem removal. dropColumnIfExists is driver-aware and idempotent.
	// Order matters only for the index above — column drops are independent.
	for _, col := range []struct{ table, name string }{
		{"issues", "writeback_paused"},
		{"issue_comments", "external_writeback_status"},
		{"issue_comments", "external_writeback_id"},
		{"issue_comments", "external_writeback_at"},
		{"issue_comments", "external_writeback_error"},
		{"issue_comments", "external_writeback_idempotency_key"},
		{"issue_comments", "external_writeback_retry_count"},
	} {
		if err := dropColumnIfExists(db, col.table, col.name); err != nil {
			slog.Warn("drop writeback column failed", "table", col.table, "column", col.name, "error", err)
		}
	}

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_external_creds_owner
		ON external_provider_credentials(owner_type, owner_id, user_id)
	`); err != nil {
		slog.Warn("create idx_external_creds_owner failed", "error", err)
	}

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_project_external_sources_project
		ON project_external_sources(project_id)
	`); err != nil {
		slog.Warn("create idx_project_external_sources_project failed", "error", err)
	}

	// Extend provider CHECK constraints on external_provider_credentials and
	// project_external_sources to accept 'jira' (Task 6 of L4 MCP Intent
	// Tools). Driver-aware: Postgres does ALTER CONSTRAINT, SQLite does a
	// full table rebuild. Idempotent — both branches probe before mutating.
	migrateExternalProviderJiraSupport(db)

	// AI-adaptive external API proxy redesign: create external_providers,
	// external_api_audit, and external_api_write_prefs if missing.
	if err := MigrateExternalProviders(db); err != nil {
		slog.Error("MigrateExternalProviders failed", "error", err)
	}

	// External credential alias + credential_id migration.
	migrateExternalCredentialAlias(db)

	// Providers are now an open, user-creatable set; drop the hardcoded
	// CHECK (provider IN ('github','jira','tapd')) from every table carrying a
	// provider column. Runs AFTER the alias migration so the credentials and
	// sources tables already have their current column shape (alias /
	// credential_id) before this rebuild mirrors the current schema. Idempotent.
	migrateExternalProviderDropProviderCheck(db)

	// Scene-based MCP/plugin management (M1) — schema additions live in
	// schema.sql / schema_postgres.sql; these migrations cover existing DBs.
	addScenesTables(db)
	addAssetSlugColumns(db)
	backfillAssetSlugs(db)
	migrateLegacyToBaseLayer(db)

	// #256: copy existing project_learnings into the owner-scoped memories table,
	// then drop the legacy table (data already in memories). Both idempotent.
	migrateLearningsToMemory(db)
	dropProjectLearningsTable(db)

	// Legacy harness_specs migrations below all reference the scope/owner_type/
	// owner_id/project_id columns. harness_specs is now a single GLOBAL library
	// and those columns are physically dropped (MigrateDropHarnessSpecOwner /
	// new schema.sql). Guard on the `scope` column so these are skipped on fresh
	// installs and on already-dropped DBs, while still running once on legacy
	// DBs that upgrade through the pre-drop shape.
	if hasScope, _ := columnExists(db, "harness_specs", "scope"); hasScope {
		// 2026-05-20 harness spec typed-config redesign: add typed columns to
		// harness_specs and derive kind + typed values from existing rows.
		migrateHarnessSpecsTypedColumns(db)
		// Engineering standards are globally managed only. Remove old
		// project-scoped specs, collapse duplicate global names, tighten CHECK.
		migrateHarnessSpecsGlobalOnly(db)
		migrateHarnessSpecsGlobalUnique(db)
	}
	// idx_harness_specs_kind lives on a migration-added column, so it cannot go
	// in schema.sql. Create it here whenever `kind` exists (both fresh installs
	// from the new schema and legacy DBs after the typed-columns migration).
	if hasKind, _ := columnExists(db, "harness_specs", "kind"); hasKind {
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_harness_specs_kind ON harness_specs(kind)`); err != nil {
			slog.Warn("create idx_harness_specs_kind failed", "error", err)
		}
	}

	// Data dashboards M1 (2026-06-04): the three tables (saved_queries /
	// dashboards / dashboard_panels) are created by schema.sql / schema_postgres.sql
	// in Open(). Their secondary indexes live here per the repo convention that
	// secondary indexes are added in Migrate(), not in the schema files. Both
	// indexed columns exist in the base CREATE TABLE, so these are safe on fresh
	// and upgraded DBs alike. CREATE INDEX IF NOT EXISTS is cross-driver.
	//
	// 2026-06-04 static-chart pin: saved_queries gained a `snapshot` column
	// (JSON snapshot for source-less / direct-result charts) AND source_id
	// became nullable (static charts have no backing data source). Editing the
	// CREATE TABLE only helps fresh DBs; existing DBs (local/personal/demo) kept
	// source_id NOT NULL, so inserting a static saved query (source_id = NULL)
	// hit a NOT NULL constraint and 500'd. migrateSavedQueriesSourceNullable
	// drops the NOT NULL on upgraded DBs (PG: ALTER; SQLite: 12-step rebuild).
	addColumnIfNotExists(db, "saved_queries", "snapshot", "TEXT NOT NULL DEFAULT ''")
	if err := migrateSavedQueriesSourceNullable(db); err != nil {
		slog.Error("migrateSavedQueriesSourceNullable failed", "error", err)
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_dashboard_panels_dashboard ON dashboard_panels(dashboard_id)`); err != nil {
		slog.Warn("create idx_dashboard_panels_dashboard failed", "error", err)
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_saved_queries_source ON saved_queries(source_id)`); err != nil {
		slog.Warn("create idx_saved_queries_source failed", "error", err)
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_data_source_audit_source ON data_source_audit(source_id)`); err != nil {
		slog.Warn("create idx_data_source_audit_source failed", "error", err)
	}

	// Knowledge-base management UI (Epic #496 · #498) adds lifecycle + ingest
	// columns on top of #497's base knowledge_bases table. They live here (not in
	// schema.sql) per the migration-added-column rule and are accessed via raw
	// SQL in api/knowledge_base.go, so no sqlc regen is needed. Idempotent.
	addColumnIfNotExists(db, "knowledge_bases", "status", "TEXT NOT NULL DEFAULT 'enabled'")
	addColumnIfNotExists(db, "knowledge_bases", "ingest_status", "TEXT NOT NULL DEFAULT 'ready'")
	addColumnIfNotExists(db, "knowledge_bases", "ingest_progress", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "knowledge_bases", "ingest_error", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "knowledge_bases", "doc_count", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "knowledge_bases", "chunk_count", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "knowledge_bases", "last_indexed_at", "TIMESTAMP DEFAULT NULL")

	// AI-native board execution, stage 1a (data model + backfill).
	// Spec: docs/superpowers/specs/2026-06-05-ai-native-board-execution-design.md §11/§17.
	migrateAINativeBoardStage1a(db)

	// AI-native board execution, stage 4 (column-native floor gate + completion收口).
	// Spec §22.4: per-issue floor_retry_count caps the auto self-fix loop.
	migrateAINativeBoardStage4(db)

	// AI-native board execution, stage 7 (card projection + intervention + failure
	// terminal states + execution timeline). Spec §7/§19/§23.7.
	migrateAINativeBoardStage7(db)

	// AI-native board execution, stage 8 (floor gate by issue output type).
	// Spec §23.6: harness_specs.code_probe_only marks a floor spec as build/test-class
	// so the floor gate auto-N/As it for a no-code-diff (doc/research) issue.
	migrateAINativeBoardStage8(db)

	// Autohost 安全网: hidden-ref checkpoint metadata (refs/niuniu/<ws>/<issue>/<step>).
	// The git object/refs live in each worktree; this table is the queryable index
	// (timeline + per-step diff base + gate status) for the checkpoint service.
	migrateIssueCheckpoints(db)

	// Orchestration cost guardrails, stage 6 (spec 2026-06-06 section 16). The
	// workspace_start_queue table itself is created by schema.sql in Open(); its
	// secondary indexes live here per the repo convention. Both indexed columns
	// exist in the base CREATE TABLE, so these are safe on fresh and upgraded DBs.
	migrateWorkspaceStartQueueIndexes(db)

	// Ensure indexes that were missing in older versions (schema.sql CREATE INDEX IF
	// NOT EXISTS handles fresh installs; this covers upgraded DBs where schema ran
	// before these indexes were added).
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_issues_exec_status ON issues(exec_status, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_workspaces_issue ON workspaces(issue_id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			slog.Warn("ensure index failed", "stmt", stmt[:60], "err", err)
		}
	}

	// One-time cleanup: delete agent_messages and workspace_schedules whose workspace
	// was deleted in an older version where foreign_keys was disabled (no cascade).
	// Safe to run on both SQLite and PostgreSQL; the subquery is parameter-free so
	// no placeholder conversion is needed.
	if !migrationApplied(w, "cleanup_orphaned_data_v1") {
		ctx := context.Background()
		if _, err := w.ExecContext(ctx,
			`DELETE FROM agent_messages WHERE workspace_id NOT IN (SELECT id FROM workspaces)`); err != nil {
			slog.Warn("cleanup_orphaned_data: agent_messages delete failed", "err", err)
		}
		if _, err := w.ExecContext(ctx,
			`DELETE FROM workspace_schedules WHERE workspace_id NOT IN (SELECT id FROM workspaces)`); err != nil {
			slog.Warn("cleanup_orphaned_data: workspace_schedules delete failed", "err", err)
		}
		markMigration(w, "cleanup_orphaned_data_v1")
		slog.Info("cleanup_orphaned_data: completed")
	}

	// One-time cleanup: scene-provided quick actions are no longer persisted to
	// the quick_actions table — they are parsed live from the workspace scene
	// projection and shown as a separate group. Drop rows previously imported
	// from scenes so they don't appear twice (once under personal/org, once
	// under the scene group). scene_asset_imports pinpoints exactly which
	// quick_actions came from a scene. Parameter-free subquery → safe on SQLite
	// and PostgreSQL.
	if !migrationApplied(w, "drop_scene_imported_quick_actions_v1") {
		ctx := context.Background()
		_, err1 := w.ExecContext(ctx,
			`DELETE FROM quick_actions WHERE id IN (SELECT asset_id FROM scene_asset_imports WHERE asset_kind = 'quick_action')`)
		if err1 != nil {
			slog.Warn("drop_scene_imported_quick_actions: delete quick_actions failed", "err", err1)
		}
		_, err2 := w.ExecContext(ctx,
			`DELETE FROM scene_asset_imports WHERE asset_kind = 'quick_action'`)
		if err2 != nil {
			slog.Warn("drop_scene_imported_quick_actions: delete imports failed", "err", err2)
		}
		// Only record the migration once BOTH deletes succeed, so a partial
		// failure retries on next startup instead of leaving orphaned rows.
		if err1 == nil && err2 == nil {
			markMigration(w, "drop_scene_imported_quick_actions_v1")
			slog.Info("drop_scene_imported_quick_actions: completed")
		}
	}

	// One-time cleanup: scenes no longer import agents into the agents table —
	// they only REFERENCE existing agents (added on the Agents page) and the
	// referenced markdown is materialized into the workspace's .claude/agents/
	// at projection time. Drop the standalone agent rows that the old importer
	// auto-created from scenes so they don't linger on the Agents page.
	// scene_asset_imports pinpoints exactly which agents came from a scene.
	// (The orphaned ~/.niuniu/agents/*.md files are inert — nothing references
	// them once the row is gone.) Parameter-free subquery → SQLite + PostgreSQL.
	if !migrationApplied(w, "drop_scene_imported_agents_v1") {
		ctx := context.Background()
		_, err1 := w.ExecContext(ctx,
			`DELETE FROM agents WHERE id IN (SELECT asset_id FROM scene_asset_imports WHERE asset_kind = 'agent')`)
		if err1 != nil {
			slog.Warn("drop_scene_imported_agents: delete agents failed", "err", err1)
		}
		_, err2 := w.ExecContext(ctx,
			`DELETE FROM scene_asset_imports WHERE asset_kind = 'agent'`)
		if err2 != nil {
			slog.Warn("drop_scene_imported_agents: delete imports failed", "err", err2)
		}
		if err1 == nil && err2 == nil {
			markMigration(w, "drop_scene_imported_agents_v1")
			slog.Info("drop_scene_imported_agents: completed")
		}
	}

	// project_blueprints gained source / slug / is_default after its initial
	// single-content shape (2026-06-10 project-template feature). The table is
	// net-new in schema.sql, but a DB that built it from the first cut needs
	// these columns added here. (default_project_blueprints is net-new and lives
	// wholly in schema.sql.)
	addColumnIfNotExists(db, "project_blueprints", "source",
		"TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user','builtin'))")
	addColumnIfNotExists(db, "project_blueprints", "slug", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "project_blueprints", "is_default", "INTEGER NOT NULL DEFAULT 0")

	// 2026-07-07 IM Bot AI onboarding tokens (Epic #555 T1): one-time
	// credential-entry token table. New table on existing DBs; schema.sql /
	// schema_postgres.sql handle fresh installs via CREATE TABLE IF NOT EXISTS.
	migrateIMBotOnboardingTokens(db)

	// 2026-07-08 IM Bot shared bot / multi-project routing: channels become
	// owner-level (owner_type/owner_id/credential_fingerprint), chats gain a
	// nullable project_id routing target. Adds columns + indexes + one-time
	// owner/chat.project_id backfill.
	migrateIMBotSharedBot(db)

	// 2026-07-08 IM Bot: drop the now-vestigial im_bot_channels.project_id
	// ("home / default project"). A bot is owner-level; all of the owner's
	// projects are peers, routing lives on im_bot_chats.project_id. Rebuilds the
	// table on SQLite (project_id is part of the old UNIQUE and the table is FK
	// referenced), DROP COLUMN on Postgres.
	migrateIMBotChannelsDropProjectID(db)

	// 2026-07-10 IM Bot: admit the 'wechat' (微信ClawBot / iLink) channel type.
	// The channel_type / platform CHECK constraints were closed enums; relax them
	// to include 'wechat'. SQLite has no ALTER-CHECK, so the tables are rebuilt;
	// Postgres swaps the named CHECK constraint in place.
	migrateIMBotAllowWechat(db)
}

// migrateIMBotAllowWechat relaxes the im_bot_channels.channel_type and
// im_bot_onboarding_tokens.platform CHECK enums to include 'wechat'. Both are
// marker-gated and dual-driver. On fresh installs the schema files already carry
// 'wechat', so the rebuild is a one-time no-op that reproduces the same table.
func migrateIMBotAllowWechat(db *sql.DB) {
	w := Wrap(db)
	if Driver == "postgres" {
		if migrationApplied(w, "im_bot_allow_wechat_v1") {
			return
		}
		// Inline column CHECKs get the default name <table>_<column>_check.
		stmts := []string{
			`ALTER TABLE im_bot_channels DROP CONSTRAINT IF EXISTS im_bot_channels_channel_type_check`,
			`ALTER TABLE im_bot_channels ADD CONSTRAINT im_bot_channels_channel_type_check
				CHECK (channel_type IN ('lark','dingtalk','telegram','wework','wechat'))`,
			`ALTER TABLE im_bot_onboarding_tokens DROP CONSTRAINT IF EXISTS im_bot_onboarding_tokens_platform_check`,
			`ALTER TABLE im_bot_onboarding_tokens ADD CONSTRAINT im_bot_onboarding_tokens_platform_check
				CHECK (platform IN ('lark','dingtalk','telegram','wework','wechat'))`,
		}
		for _, s := range stmts {
			if _, err := db.Exec(s); err != nil {
				slog.Warn("migrateIMBotAllowWechat (pg): step failed",
					"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
				return // leave marker unset; next start retries
			}
		}
		markMigration(w, "im_bot_allow_wechat_v1")
		return
	}
	migrateIMBotAllowWechatSQLite(db, w)
}

// migrateIMBotAllowWechatSQLite rebuilds both tables with the widened CHECK.
// im_bot_channels is FK-referenced (im_bot_chats/im_bot_inbox) so it is rebuilt
// with foreign_keys OFF, preserving id and recreating its indexes — mirroring
// migrateIMBotChannelsDropProjectIDSQLite.
// tableCheckAllowsWechat reports whether a SQLite table's stored DDL already
// lists 'wechat' in its CHECK enum (true for fresh installs created from the
// current schema.sql). It reads sqlite_master.sql; a missing table or read error
// yields false so the migration proceeds cautiously.
func tableCheckAllowsWechat(db *sql.DB, table string) bool {
	var ddl string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl)
	if err != nil {
		return false
	}
	return strings.Contains(ddl, "'wechat'")
}

func migrateIMBotAllowWechatSQLite(db *sql.DB, w *DB) {
	if migrationApplied(w, "im_bot_allow_wechat_v1") {
		return
	}
	// Fresh installs already carry 'wechat' in both CHECK enums (schema.sql), so
	// there is nothing to rebuild. Skip early — crucially BEFORE toggling
	// PRAGMA foreign_keys, because the rebuild's `foreign_keys = ON` would leak
	// onto a pooled connection and change enforcement for unrelated code (unit
	// tests open SQLite with foreign_keys OFF by default, so a leaked ON breaks
	// their intentionally-dangling FKs).
	if tableCheckAllowsWechat(db, "im_bot_channels") && tableCheckAllowsWechat(db, "im_bot_onboarding_tokens") {
		markMigration(w, "im_bot_allow_wechat_v1")
		return
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("migrateIMBotAllowWechat (sqlite): disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("migrateIMBotAllowWechat (sqlite): begin tx failed", "error", err)
		return
	}
	stmts := []string{
		`CREATE TABLE im_bot_channels_wechat_new (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type      TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id        INTEGER NOT NULL DEFAULT 0,
			credential_fingerprint TEXT NOT NULL DEFAULT '',
			channel_type    TEXT NOT NULL CHECK (channel_type IN ('lark','dingtalk','telegram','wework','wechat')),
			name            TEXT NOT NULL,
			connection_mode TEXT NOT NULL DEFAULT 'stream' CHECK (connection_mode IN ('stream','webhook')),
			credential_enc  TEXT NOT NULL DEFAULT '',
			webhook_secret  TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
			created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, channel_type, name)
		)`,
		`INSERT INTO im_bot_channels_wechat_new
			(id, owner_type, owner_id, credential_fingerprint, channel_type, name,
			 connection_mode, credential_enc, webhook_secret, status, created_at, updated_at)
			SELECT id, owner_type, owner_id, credential_fingerprint, channel_type, name,
			       connection_mode, credential_enc, webhook_secret, status, created_at, updated_at
			FROM im_bot_channels`,
		`DROP TABLE im_bot_channels`,
		`ALTER TABLE im_bot_channels_wechat_new RENAME TO im_bot_channels`,
		`CREATE INDEX IF NOT EXISTS idx_im_bot_channels_owner ON im_bot_channels(owner_type, owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_im_bot_channels_owner_type_name
			ON im_bot_channels(owner_type, owner_id, channel_type, name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_im_bot_channels_owner_fingerprint
			ON im_bot_channels(owner_type, owner_id, channel_type, credential_fingerprint)
			WHERE credential_fingerprint != ''`,
		`CREATE INDEX IF NOT EXISTS idx_im_bot_channels_status_mode ON im_bot_channels(status, connection_mode)`,
		// im_bot_onboarding_tokens: not FK-referenced; straightforward rebuild.
		`CREATE TABLE im_bot_onboarding_tokens_wechat_new (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			token_hash      TEXT NOT NULL UNIQUE,
			project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			platform        TEXT NOT NULL CHECK (platform IN ('lark','dingtalk','telegram','wework','wechat')),
			channel_name    TEXT NOT NULL DEFAULT '',
			connection_mode TEXT NOT NULL DEFAULT 'stream' CHECK (connection_mode IN ('stream','webhook')),
			expires_at      TIMESTAMP NOT NULL,
			used_at         TIMESTAMP,
			created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO im_bot_onboarding_tokens_wechat_new
			(id, token_hash, project_id, platform, channel_name, connection_mode, expires_at, used_at, created_at)
			SELECT id, token_hash, project_id, platform, channel_name, connection_mode, expires_at, used_at, created_at
			FROM im_bot_onboarding_tokens`,
		`DROP TABLE im_bot_onboarding_tokens`,
		`ALTER TABLE im_bot_onboarding_tokens_wechat_new RENAME TO im_bot_onboarding_tokens`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			slog.Warn("migrateIMBotAllowWechat (sqlite): step failed",
				"error", err, "first_line", strings.SplitN(stmt, "\n", 2)[0])
			return // leave marker unset; next start retries
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("migrateIMBotAllowWechat (sqlite): commit failed", "error", err)
		return
	}
	markMigration(w, "im_bot_allow_wechat_v1")
	slog.Info("migrateIMBotAllowWechat: channel_type/platform CHECK widened for 'wechat'")
}

// migrateIMBotOnboardingTokens creates the im_bot_onboarding_tokens table on
// existing databases. The table is defined in schema.sql / schema_postgres.sql
// for fresh installs; this migration covers upgraded DBs. Uses EnsureMigration
// for idempotency and SQLite-compatible DDL (INTEGER PK / TEXT types).
func migrateIMBotOnboardingTokens(db *sql.DB) {
	w := Wrap(db)
	if err := EnsureMigration(w, "im_bot_onboarding_tokens_v1",
		`CREATE TABLE IF NOT EXISTS im_bot_onboarding_tokens (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			token_hash      TEXT NOT NULL UNIQUE,
			project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			platform        TEXT NOT NULL CHECK (platform IN ('lark','dingtalk','telegram','wework','wechat')),
			channel_name    TEXT NOT NULL DEFAULT '',
			connection_mode TEXT NOT NULL DEFAULT 'stream' CHECK (connection_mode IN ('stream','webhook')),
			expires_at      TIMESTAMP NOT NULL,
			used_at         TIMESTAMP,
			created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		slog.Error("migrateIMBotOnboardingTokens failed", "error", err)
	}
}

// migrateIMBotSharedBot implements the IM Bot shared-bot / multi-project routing
// data model (spec 2026-07-08-imbot-shared-bot-multi-project-routing-design.md):
//
//   - im_bot_channels becomes owner-level: owner_type/owner_id identify the bot's
//     owner (user|org), credential_fingerprint is a non-plaintext SHA-256 of the
//     app-identity fields used to forbid a second connection for the same app.
//     project_id stays NOT NULL (the "home / default target project").
//   - im_bot_chats gains a nullable project_id: the real routing target chosen at
//     approval time. NULL = unassigned.
//
// All four columns are added via addColumnIfNotExists (driver-aware, retrofits
// existing DBs); the schema.sql / schema_postgres.sql CREATE blocks carry them
// for fresh installs. The indexes + partial UNIQUE go here (never in schema.sql
// for migration-added columns — see the DB red lines). A one-time marker-gated
// backfill then copies each channel's project owner onto the channel and stamps
// its chats' project_id from the channel's home project. All backfill SQL is
// parameter-free and runs through Wrap(db) so it is dual-driver safe.
func migrateIMBotSharedBot(db *sql.DB) {
	fk := fkType()

	addColumnIfNotExists(db, "im_bot_channels", "owner_type",
		"TEXT NOT NULL DEFAULT 'user'")
	addColumnIfNotExists(db, "im_bot_channels", "owner_id", fk+" NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "im_bot_channels", "credential_fingerprint",
		"TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "im_bot_chats", "project_id",
		fk+" REFERENCES projects(id) ON DELETE CASCADE")

	w := Wrap(db)

	// Indexes + partial UNIQUE (dual-driver: both SQLite and PostgreSQL support
	// partial indexes). The partial WHERE keeps the UNIQUE from firing on the
	// many rows that still carry an empty fingerprint (default '').
	idxStmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_im_bot_chats_project ON im_bot_chats(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_im_bot_channels_owner ON im_bot_channels(owner_type, owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_im_bot_channels_owner_fingerprint
			ON im_bot_channels(owner_type, owner_id, channel_type, credential_fingerprint)
			WHERE credential_fingerprint != ''`,
	}
	for _, s := range idxStmts {
		if _, err := db.Exec(s); err != nil {
			slog.Warn("create im_bot shared-bot index failed",
				"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
		}
	}

	// One-time backfill: stamp each channel's owner from its home project, and
	// each chat's project_id from its channel's home project (only where unset).
	// Skipped entirely once im_bot_channels.project_id is gone (fresh installs
	// never had it; upgraded DBs have it dropped by a later migration) -- the
	// backfill queries reference project_id, so guard on its presence.
	if !migrationApplied(w, "im_bot_shared_bot_backfill_v1") {
		if hasProjectID, _ := columnExists(db, "im_bot_channels", "project_id"); !hasProjectID {
			markMigration(w, "im_bot_shared_bot_backfill_v1")
			return
		}
		ctx := context.Background()
		_, err1 := w.ExecContext(ctx,
			`UPDATE im_bot_channels
				SET owner_type = (SELECT p.owner_type FROM projects p WHERE p.id = im_bot_channels.project_id),
				    owner_id   = (SELECT p.owner_id   FROM projects p WHERE p.id = im_bot_channels.project_id)
				WHERE EXISTS (SELECT 1 FROM projects p WHERE p.id = im_bot_channels.project_id)`)
		if err1 != nil {
			slog.Warn("im_bot shared-bot backfill: channel owner failed", "err", err1)
		}
		_, err2 := w.ExecContext(ctx,
			`UPDATE im_bot_chats
				SET project_id = (SELECT ch.project_id FROM im_bot_channels ch WHERE ch.id = im_bot_chats.channel_id)
				WHERE project_id IS NULL
				  AND EXISTS (SELECT 1 FROM im_bot_channels ch WHERE ch.id = im_bot_chats.channel_id)`)
		if err2 != nil {
			slog.Warn("im_bot shared-bot backfill: chat project_id failed", "err", err2)
		}
		if err1 == nil && err2 == nil {
			markMigration(w, "im_bot_shared_bot_backfill_v1")
			slog.Info("im_bot shared-bot backfill: completed")
		}
	}
}

// migrateIMBotChannelsDropProjectID removes im_bot_channels.project_id (the old
// "home / default project"). A bot is owner-level and belongs to no project; all
// of the owner's projects are peers, and routing lives on im_bot_chats.project_id.
//
// Because sqlc generates `SELECT *` for this table, the column MUST be physically
// dropped on existing DBs or scans fail on the column-count mismatch. Marker-gated
// and dual-driver:
//   - SQLite: project_id is part of the old UNIQUE(project_id, channel_type, name)
//     and the table is FK-referenced by im_bot_chats/im_bot_inbox, so a bare DROP
//     COLUMN is unreliable. Rebuild the table (preserving id so child FKs stay
//     valid) with the new UNIQUE(owner_type, owner_id, channel_type, name), wrapped
//     in a transaction with foreign_keys OFF, then recreate the indexes.
//   - PostgreSQL: DROP COLUMN + drop the old project index + create the new owner
//     UNIQUE index directly.
func migrateIMBotChannelsDropProjectID(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "im_bot_channels_drop_project_id_v1") {
		return
	}
	if Driver == "postgres" {
		migrateIMBotChannelsDropProjectIDPostgres(db, w)
		return
	}
	migrateIMBotChannelsDropProjectIDSQLite(db, w)
}

func migrateIMBotChannelsDropProjectIDPostgres(db *sql.DB, w *DB) {
	// Idempotent, parameter-free DDL; safe on fresh (already-dropped) DBs.
	stmts := []string{
		`ALTER TABLE im_bot_channels DROP COLUMN IF EXISTS project_id`,
		`DROP INDEX IF EXISTS idx_im_bot_channels_project`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_im_bot_channels_owner_type_name
			ON im_bot_channels(owner_type, owner_id, channel_type, name)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			slog.Warn("migrateIMBotChannelsDropProjectID (pg): step failed",
				"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
			return // leave marker unset; next start retries
		}
	}
	markMigration(w, "im_bot_channels_drop_project_id_v1")
}

func migrateIMBotChannelsDropProjectIDSQLite(db *sql.DB, w *DB) {
	// Already clean (fresh install created the table without project_id)?
	exists, err := columnExists(db, "im_bot_channels", "project_id")
	if err != nil {
		slog.Warn("migrateIMBotChannelsDropProjectID (sqlite): column check failed", "error", err)
		return
	}
	if !exists {
		markMigration(w, "im_bot_channels_drop_project_id_v1")
		return
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("migrateIMBotChannelsDropProjectID (sqlite): disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("migrateIMBotChannelsDropProjectID (sqlite): begin tx failed", "error", err)
		return
	}

	stmts := []string{
		`CREATE TABLE im_bot_channels_new (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type      TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id        INTEGER NOT NULL DEFAULT 0,
			credential_fingerprint TEXT NOT NULL DEFAULT '',
			channel_type    TEXT NOT NULL CHECK (channel_type IN ('lark','dingtalk','telegram','wework','wechat')),
			name            TEXT NOT NULL,
			connection_mode TEXT NOT NULL DEFAULT 'stream' CHECK (connection_mode IN ('stream','webhook')),
			credential_enc  TEXT NOT NULL DEFAULT '',
			webhook_secret  TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
			created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, channel_type, name)
		)`,
		// Preserve id so im_bot_chats/im_bot_inbox FKs remain valid. Explicit
		// column lists (skip project_id).
		`INSERT INTO im_bot_channels_new
			(id, owner_type, owner_id, credential_fingerprint, channel_type, name,
			 connection_mode, credential_enc, webhook_secret, status, created_at, updated_at)
			SELECT id, owner_type, owner_id, credential_fingerprint, channel_type, name,
			       connection_mode, credential_enc, webhook_secret, status, created_at, updated_at
			FROM im_bot_channels`,
		`DROP TABLE im_bot_channels`,
		`ALTER TABLE im_bot_channels_new RENAME TO im_bot_channels`,
		`CREATE INDEX IF NOT EXISTS idx_im_bot_channels_owner ON im_bot_channels(owner_type, owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_im_bot_channels_owner_type_name
			ON im_bot_channels(owner_type, owner_id, channel_type, name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_im_bot_channels_owner_fingerprint
			ON im_bot_channels(owner_type, owner_id, channel_type, credential_fingerprint)
			WHERE credential_fingerprint != ''`,
		`CREATE INDEX IF NOT EXISTS idx_im_bot_channels_status_mode ON im_bot_channels(status, connection_mode)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			slog.Warn("migrateIMBotChannelsDropProjectID (sqlite): step failed",
				"error", err, "first_line", strings.SplitN(stmt, "\n", 2)[0])
			return // leave marker unset; next start retries
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("migrateIMBotChannelsDropProjectID (sqlite): commit failed", "error", err)
		return
	}
	markMigration(w, "im_bot_channels_drop_project_id_v1")
	slog.Info("migrateIMBotChannelsDropProjectID: im_bot_channels rebuilt without project_id")
}

// migrateWorkspaceStartQueueIndexes adds the secondary indexes for the
// workspace_start_queue overflow queue. The partial UNIQUE index keeps a single
// issue from being queued twice at once; both SQLite and PostgreSQL support the
// partial-index WHERE clause. CREATE INDEX IF NOT EXISTS is cross-driver and the
// statements carry no parameters, so raw db.Exec is dual-driver safe.
func migrateWorkspaceStartQueueIndexes(db *sql.DB) {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_wsq_owner_status
			ON workspace_start_queue (owner_type, owner_id, status, enqueued_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_wsq_issue_queued
			ON workspace_start_queue (issue_id) WHERE status = 'queued'`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			slog.Warn("create workspace_start_queue index failed",
				"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
		}
	}
}

// migrateAINativeBoardStage4 implements the data-model delta for stage 4 (spec
// 2026-06-05-ai-native-board-execution-design.md §22.4): a per-issue
// floor_retry_count that bounds the auto (autohost) floor-gate self-fix loop so a
// repeatedly-failing底线闸 escalates to a human instead of looping forever.
//
// Added migrate-only (NOT in schema.sql) on purpose, following the stage-1a
// convention: the issues.* sqlc queries use explicit `SELECT id, column_id, ...`
// lists expanded against schema.sql at generation time, and the Issue struct is
// likewise generated from it. Adding floor_retry_count to the CREATE block would
// widen every issues query + the Issue struct on the next `sqlc generate`. Stage 4
// reads/writes this single field via anchored raw SQL (WHERE id = ?), which is
// cross-dialect safe, so keeping it migration-only is side-effect free.
//
// The exec_status CHECK gains 'gate_checking'/'gate_blocked' (§11.3 option ①) in
// the schema.sql / schema_postgres.sql CREATE blocks rather than here: exec_status
// was itself added via an addColumnIfNotExists ALTER that carries NO CHECK (see
// above), so upgraded DBs have no exec_status CHECK at all and already accept the
// new values; only fresh DBs (which build from the CREATE block) need the wider set.
func migrateAINativeBoardStage4(db *sql.DB) {
	addColumnIfNotExists(db, "issues", "floor_retry_count", "INTEGER NOT NULL DEFAULT 0")
}

// migrateAINativeBoardStage7 backs the card-projection / intervention / failure
// terminal-state / execution-timeline stage (spec §7/§19/§23.7):
//
//   - issues.exec_status_reason (nullable TEXT): the human-readable reason behind a
//     terminal state (abandoned-with-reason, blocked-needs-human). Plain nullable
//     column, so it goes in BOTH the schema CREATE blocks (fresh DBs) AND here via
//     addColumnIfNotExists (upgraded DBs). No CHECK to retrofit.
//   - The two NEW exec_status values ('waiting_input','abandoned') need NO migration:
//     exec_status was added via a CHECK-less ALTER (see migrateAINativeBoardStage4
//     header), so upgraded DBs already accept them; only the schema CREATE blocks
//     (fresh DBs) gained the wider CHECK list.
//   - issue_exec_events: a first-class append-only per-issue execution timeline
//     (advance moves / gate results / ask_user round-trips / terminal transitions /
//     cost). A brand-new table, so its CREATE + INDEX are safe in the schema files;
//     this marker-gated CREATE covers upgraded DBs that passed the schema step at an
//     older version.
func migrateAINativeBoardStage7(db *sql.DB) {
	addColumnIfNotExists(db, "issues", "exec_status_reason", "TEXT")

	w := Wrap(db)
	if migrationApplied(w, "ai_native_board_stage7_exec_events_v1") {
		return
	}
	fk := fkType()
	pkType := "INTEGER PRIMARY KEY AUTOINCREMENT"
	realType := "REAL"
	if Driver == "postgres" {
		pkType = "BIGSERIAL PRIMARY KEY"
		realType = "DOUBLE PRECISION"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS issue_exec_events (
			id           ` + pkType + `,
			issue_id     ` + fk + ` NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			workspace_id ` + fk + `,
			kind         TEXT NOT NULL CHECK (kind IN ('advance','gate','ask_user','terminal','cost','intervention')),
			summary      TEXT NOT NULL,
			detail_json  TEXT,
			cost_usd     ` + realType + ` NOT NULL DEFAULT 0,
			created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_issue_exec_events_issue ON issue_exec_events(issue_id, created_at)`,
	}
	ctx := context.Background()
	for _, s := range stmts {
		if _, err := w.ExecContext(ctx, s); err != nil {
			slog.Error("create issue_exec_events table failed",
				"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
			return // leave marker unset; next start will retry
		}
	}
	markMigration(w, "ai_native_board_stage7_exec_events_v1")
}

// migrateIssueCheckpoints creates the issue_checkpoints table backing the
// autohost hidden-ref checkpoint system. One row per (checkpoint step, repo
// worktree): the git snapshot itself is a commit pointed at by a hidden ref
// (refs/niuniu/<ws>/<issue>/<step>) inside that repo, so this table only indexes
// the metadata needed to render the timeline, diff a step (parent_hash..commit_hash)
// and drive gate auto-revert (gate_status). Migrate-only + raw-SQL access (not
// sqlc), following the issue_exec_events / issue_route_visits convention.
func migrateIssueCheckpoints(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "issue_checkpoints_v1") {
		return
	}
	fk := fkType()
	pkType := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if Driver == "postgres" {
		pkType = "BIGSERIAL PRIMARY KEY"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS issue_checkpoints (
			id            ` + pkType + `,
			issue_id      ` + fk + ` NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			workspace_id  ` + fk + ` NOT NULL,
			repository_id ` + fk + `,
			repo_name     TEXT NOT NULL DEFAULT '',
			worktree_path TEXT NOT NULL DEFAULT '',
			step          INTEGER NOT NULL,
			kind          TEXT NOT NULL CHECK (kind IN ('advance','gate_pass','autohost_final','manual')),
			gate_status   TEXT NOT NULL DEFAULT '',
			label         TEXT NOT NULL DEFAULT '',
			git_ref       TEXT NOT NULL,
			commit_hash   TEXT NOT NULL,
			parent_hash   TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_issue_checkpoints_issue ON issue_checkpoints(issue_id, step)`,
	}
	ctx := context.Background()
	for _, s := range stmts {
		if _, err := w.ExecContext(ctx, s); err != nil {
			slog.Error("create issue_checkpoints table failed",
				"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
			return // leave marker unset; next start will retry
		}
	}
	markMigration(w, "issue_checkpoints_v1")
}

// migrateAINativeBoardStage8 implements the data-model delta for stage 8 (spec
// 2026-06-05-ai-native-board-execution-design.md §23.6): harness_specs gains
// code_probe_only, a flag marking a floor (applicability='always') spec as
// build/test-class — i.e. only meaningful when the workspace produced a code diff.
// When the floor gate finds no code diff (a pure doc/research issue), code_probe_only
// specs are auto-N/A'd (neither fail nor silently pass) and a non-code "产出非空"
// floor兜底 takes over, so doc-type issues still have a completion门槛.
//
// Migrate-only (NOT in schema.sql), following the stage-1a/4 convention: the floor
// gate reads it via anchored raw SQL (WHERE id = ? / project_id = ?), never sqlc, so
// keeping it out of the CREATE block avoids widening every harness_specs query +
// the generated Spec struct on the next `sqlc generate`. The actual flag value for
// the default code-class specs (build-test-pass / test-coverage) is set after
// SeedDefaults at boot (server.New), since those rows may not exist yet here.
func migrateAINativeBoardStage8(db *sql.DB) {
	addColumnIfNotExists(db, "harness_specs", "code_probe_only", "INTEGER NOT NULL DEFAULT 0")
}

// migrateAINativeBoardStage1a implements stage 1a of the AI-native board model
// (spec 2026-06-05-ai-native-board-execution-design.md §11/§17):
//
//   - columns gains op_primitive (the column's command primitive:
//     none/instruct/complete) and when_to_use (AI-generated routing hint, nullable).
//   - column_gate_specs gains applicability (if_routed/always). That table ALREADY
//     exists (schema.sql) with shape (column_id, spec_id, position) and has live
//     sqlc consumers + production data, so we ADD a column, NOT a CREATE TABLE, and
//     keep position. Scheme B (§5.1): no enforcement column — whether a gate blocks
//     is decided by the bound spec's severity, not per-column config.
//
// All three columns are added ONLY here via addColumnIfNotExists (driver-aware),
// deliberately NOT in the schema.sql / schema_postgres.sql CREATE blocks (§11.1).
// Two reasons:
//  1. Both fresh and existing DBs take the identical ALTER path, so the CHECK
//     constraint is enforced uniformly on SQLite and PostgreSQL — true parity,
//     without the CHECK-retrofit gap that schema-block columns carry on upgraded
//     DBs (addColumnIfNotExists cannot retrofit a CHECK to an existing table).
//  2. The columns.* / column_gate_specs.* sqlc queries use `SELECT *` / `RETURNING
//     *`, which sqlc expands against schema.sql at generation time. Adding these to
//     the CREATE blocks would silently widen every one of those queries (and the
//     generated Column struct) on the next `sqlc generate`. The sqlc wiring is a
//     later stage's job; keeping the columns migration-only here is side-effect free
//     at runtime (the already-expanded explicit column lists never select them).
//
// op_primitive is NOT left at its bare 'none' default (that would silently turn
// every existing project's columns into parking lanes and stop all AI routing —
// §11.1 / §17 review note). A one-time, marker-gated backfill derives the primitive
// from each column's lifecycle_mapping (§17):
//
//	completed             -> complete   (terminal column)
//	implement/test/review -> instruct   (working columns)
//	otherwise             -> none       (backlog/created/spec/plan/empty)
//
// In-transit reconciliation (§17): this upgrade is a niuniu restart, which drops the
// epic engine's in-memory wave state. (Historical note: in-transit harness_runs used
// to be re-attached by PipelineRunner.RecoverOnStartup, removed in the workflow/
// template-run decommission — template-run no longer exists.) Epics left
// exec_status='running' by the retired mode-A wave engine have no in-process driver
// after a restart, so we 收口 them to 'paused' (a clear, inert state) on this one-time
// upgrade. Mode-B epics are driven by an orchestration agent whose session reconnects
// on its own; this drain only matters for legacy mode-A in-transit rows. One-time,
// marker-gated.
func migrateAINativeBoardStage1a(db *sql.DB) {
	// New columns. CHECK lives in the ALTER so it is enforced identically on fresh
	// and upgraded DBs, both drivers. NOT NULL columns carry a non-NULL DEFAULT
	// (SQLite's ADD COLUMN requirement); when_to_use is nullable (AI fills it later).
	addColumnIfNotExists(db, "columns", "op_primitive",
		"TEXT NOT NULL DEFAULT 'none' CHECK (op_primitive IN ('none','instruct','complete'))")
	addColumnIfNotExists(db, "columns", "when_to_use", "TEXT")
	addColumnIfNotExists(db, "column_gate_specs", "applicability",
		"TEXT NOT NULL DEFAULT 'if_routed' CHECK (applicability IN ('if_routed','always'))")

	w := Wrap(db)

	// One-time op_primitive backfill from lifecycle_mapping. Marker-gated so a later
	// restart never clobbers a primitive the user has since edited. No bound
	// parameters — all literals — so the LIKE/CASE is identical on SQLite and PG.
	// The lifecycle vocabulary (created/spec/spec-review/plan/plan-review/implement/
	// implement-review/test/completed) makes these substrings unambiguous: 'test'
	// only ever appears in 'test', 'review' covers the *-review stages, 'implement'
	// covers implement + implement-review. Order matters: a column that reaches
	// 'completed' is terminal -> 'complete' even if it also carried earlier stages.
	if !migrationApplied(w, "columns_op_primitive_backfill_v1") {
		if _, err := w.ExecContext(context.Background(), `
			UPDATE columns SET op_primitive = CASE
				WHEN lifecycle_mapping LIKE '%completed%' THEN 'complete'
				WHEN lifecycle_mapping LIKE '%implement%'
				  OR lifecycle_mapping LIKE '%test%'
				  OR lifecycle_mapping LIKE '%review%' THEN 'instruct'
				ELSE 'none'
			END`); err != nil {
			slog.Warn("backfill columns.op_primitive failed", "error", err)
		} else {
			markMigration(w, "columns_op_primitive_backfill_v1")
		}
	}

	// One-time epic in-transit收口 (see function doc). issue_type='epic' guards the
	// UPDATE: dispatched children also use exec_status='running', but they are driven
	// independently (autohost) and the orchestration agent reconciles them on reconnect.
	if !migrationApplied(w, "epic_running_drain_v1") {
		if _, err := w.ExecContext(context.Background(),
			`UPDATE issues SET exec_status = 'paused'
			 WHERE issue_type = 'epic' AND exec_status = 'running'`); err != nil {
			slog.Warn("drain in-transit running epics failed", "error", err)
		} else {
			markMigration(w, "epic_running_drain_v1")
		}
	}
}

// migrateExternalProviderJiraSupport extends the CHECK (provider IN ...)
// constraint on external_provider_credentials and project_external_sources
// to include 'jira'. Idempotent.
//
// SQLite: rebuilds each table inside a single transaction (CREATE new →
// INSERT SELECT → DROP old → ALTER RENAME), preserving data and indexes.
// PostgreSQL: ALTER TABLE ... DROP CONSTRAINT ... ADD CONSTRAINT.
//
// The probe step tests-inserts a 'jira' row in a transaction and ROLLs
// back. If the insert succeeds the constraint already allows 'jira' and
// the migration short-circuits.
func migrateExternalProviderJiraSupport(db *sql.DB) {
	if Driver == "postgres" {
		migrateExternalProviderJiraSupportPostgres(db)
	} else {
		migrateExternalProviderJiraSupportSQLite(db)
	}
}

func migrateExternalProviderJiraSupportPostgres(db *sql.DB) {
	for _, tbl := range []string{"external_provider_credentials", "project_external_sources"} {
		// Find the existing check constraint name on the provider column.
		var constraintName string
		err := db.QueryRow(`
			SELECT cc.constraint_name
			FROM information_schema.check_constraints cc
			JOIN information_schema.constraint_column_usage ccu
			  ON cc.constraint_name = ccu.constraint_name
			WHERE ccu.table_name = $1 AND ccu.column_name = 'provider'
			LIMIT 1`, tbl).Scan(&constraintName)
		if err != nil {
			slog.Warn("jira-migration: find constraint failed", "table", tbl, "error", err)
			continue
		}
		var clause string
		_ = db.QueryRow(`
			SELECT check_clause FROM information_schema.check_constraints
			WHERE constraint_name = $1`, constraintName).Scan(&clause)
		if strings.Contains(clause, "'jira'") {
			continue // already migrated
		}
		if _, err := db.Exec(`ALTER TABLE ` + tbl + ` DROP CONSTRAINT ` + constraintName); err != nil {
			slog.Warn("jira-migration: drop old check failed", "table", tbl, "error", err)
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + tbl + ` ADD CONSTRAINT ` + constraintName +
			` CHECK (provider IN ('github','jira','tapd'))`); err != nil {
			slog.Warn("jira-migration: add new check failed", "table", tbl, "error", err)
		}
	}
}

func migrateExternalProviderJiraSupportSQLite(db *sql.DB) {
	rebuildIfNoJira := func(table, createNew, copyData string, indexes []string) {
		var ddl string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return // table absent
		}
		if strings.Contains(ddl, "'jira'") {
			return // already migrated
		}
		// Providers are now an open set: if the table no longer carries the
		// hardcoded provider enum CHECK at all, this (legacy) jira migration is
		// superseded by migrateExternalProviderDropProviderCheck and must NOT
		// rebuild the table -- its _new DDL is stale (missing alias /
		// credential_id) and would corrupt the current schema.
		if !strings.Contains(ddl, "CHECK (provider IN") {
			return
		}

		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			slog.Warn("jira-migration sqlite: disable FK failed", "table", table, "error", err)
			return
		}
		defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

		tx, err := db.Begin()
		if err != nil {
			slog.Warn("jira-migration sqlite: begin tx failed", "table", table, "error", err)
			return
		}
		stmts := []string{
			createNew,
			copyData,
			`DROP TABLE ` + table,
			`ALTER TABLE ` + table + `_new RENAME TO ` + table,
		}
		for _, s := range stmts {
			if _, err := tx.Exec(s); err != nil {
				_ = tx.Rollback()
				slog.Warn("jira-migration sqlite: step failed",
					"table", table, "error", err, "stmt_prefix", s[:min(60, len(s))])
				return
			}
		}
		for _, idx := range indexes {
			if _, err := tx.Exec(idx); err != nil {
				_ = tx.Rollback()
				slog.Warn("jira-migration sqlite: index failed", "table", table, "error", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			slog.Warn("jira-migration sqlite: commit failed", "table", table, "error", err)
			return
		}
	}

	rebuildIfNoJira(
		"external_provider_credentials",
		`CREATE TABLE external_provider_credentials_new (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type       TEXT NOT NULL CHECK (owner_type IN ('user','org')),
			owner_id         INTEGER NOT NULL,
			user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider         TEXT NOT NULL CHECK (provider IN ('github','jira','tapd')),
			config           TEXT NOT NULL DEFAULT '{}',
			last_verified_at TIMESTAMP DEFAULT NULL,
			created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, user_id, provider)
		)`,
		`INSERT INTO external_provider_credentials_new
			SELECT id, owner_type, owner_id, user_id, provider, config,
			       last_verified_at, created_at, updated_at
			FROM external_provider_credentials`,
		[]string{
			`CREATE INDEX IF NOT EXISTS idx_external_creds_owner
				ON external_provider_credentials(owner_type, owner_id, user_id)`,
		},
	)

	rebuildIfNoJira(
		"project_external_sources",
		`CREATE TABLE project_external_sources_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			provider    TEXT NOT NULL CHECK (provider IN ('github','jira','tapd')),
			source_key  TEXT NOT NULL,
			config      TEXT NOT NULL DEFAULT '{}',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, provider, source_key)
		)`,
		`INSERT INTO project_external_sources_new
			SELECT id, project_id, provider, source_key, config, created_at
			FROM project_external_sources`,
		[]string{
			`CREATE INDEX IF NOT EXISTS idx_project_external_sources_project
				ON project_external_sources(project_id)`,
		},
	)
}

// migrateExternalProviderDropProviderCheck removes the hardcoded
// CHECK (provider IN ('github','jira','tapd')) constraint from every table
// that carries a `provider` column. Providers are now an open, user-creatable
// set validated at the app layer against the external_providers table, so the
// enum CHECK must not exist on existing databases either.
//
// SQLite: rebuilds each table inside a single transaction (CREATE new ->
// INSERT SELECT -> DROP old -> ALTER RENAME), mirroring the CURRENT schema.sql
// definition exactly (minus the provider CHECK), then recreates that table's
// indexes.
// PostgreSQL: looks up the provider check-constraint name and DROPs it (no
// replacement constraint added).
//
// Idempotent on both drivers: SQLite skips when the stored DDL no longer
// contains `CHECK (provider IN`; Postgres skips when the constraint lookup
// returns no rows.
func migrateExternalProviderDropProviderCheck(db *sql.DB) {
	if Driver == "postgres" {
		migrateExternalProviderDropProviderCheckPostgres(db)
	} else {
		migrateExternalProviderDropProviderCheckSQLite(db)
	}
}

func migrateExternalProviderDropProviderCheckPostgres(db *sql.DB) {
	for _, tbl := range []string{
		"external_provider_credentials",
		"project_external_sources",
		"external_user_identities",
		"external_write_prefs",
	} {
		// Find the provider check-constraint, if any. Skip when absent (the
		// constraint was never created, or a prior run already dropped it).
		var constraintName string
		err := db.QueryRow(`
			SELECT cc.constraint_name
			FROM information_schema.check_constraints cc
			JOIN information_schema.constraint_column_usage ccu
			  ON cc.constraint_name = ccu.constraint_name
			WHERE ccu.table_name = $1 AND ccu.column_name = 'provider'
			LIMIT 1`, tbl).Scan(&constraintName)
		if err == sql.ErrNoRows {
			continue // no provider check constraint -> nothing to drop
		}
		if err != nil {
			slog.Warn("drop-provider-check: find constraint failed", "table", tbl, "error", err)
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + tbl + ` DROP CONSTRAINT ` + constraintName); err != nil {
			slog.Warn("drop-provider-check: drop constraint failed", "table", tbl, "error", err)
		}
	}
}

func migrateExternalProviderDropProviderCheckSQLite(db *sql.DB) {
	rebuildWithoutProviderCheck := func(table, createNew, copyData string, indexes []string) {
		var ddl string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return // table absent
		}
		if !strings.Contains(ddl, "CHECK (provider IN") {
			return // already migrated (or never had the provider check)
		}

		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			slog.Warn("drop-provider-check sqlite: disable FK failed", "table", table, "error", err)
			return
		}
		defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

		tx, err := db.Begin()
		if err != nil {
			slog.Warn("drop-provider-check sqlite: begin tx failed", "table", table, "error", err)
			return
		}
		stmts := []string{
			createNew,
			copyData,
			`DROP TABLE ` + table,
			`ALTER TABLE ` + table + `_new RENAME TO ` + table,
		}
		for _, s := range stmts {
			if _, err := tx.Exec(s); err != nil {
				_ = tx.Rollback()
				slog.Warn("drop-provider-check sqlite: step failed",
					"table", table, "error", err, "stmt_prefix", s[:min(60, len(s))])
				return
			}
		}
		for _, idx := range indexes {
			if _, err := tx.Exec(idx); err != nil {
				_ = tx.Rollback()
				slog.Warn("drop-provider-check sqlite: index failed", "table", table, "error", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			slog.Warn("drop-provider-check sqlite: commit failed", "table", table, "error", err)
			return
		}
	}

	// Each _new definition mirrors the CURRENT schema.sql table EXACTLY
	// (columns, defaults, NOT NULL, FK ON DELETE, UNIQUE tuple), minus the
	// provider CHECK. INSERT...SELECT lists every column explicitly.

	rebuildWithoutProviderCheck(
		"external_provider_credentials",
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
			SELECT id, owner_type, owner_id, user_id, provider, alias, config,
			       last_verified_at, created_at, updated_at
			FROM external_provider_credentials`,
		[]string{
			`CREATE INDEX IF NOT EXISTS idx_external_creds_owner
				ON external_provider_credentials(owner_type, owner_id, user_id)`,
		},
	)

	rebuildWithoutProviderCheck(
		"project_external_sources",
		`CREATE TABLE project_external_sources_new (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			provider      TEXT NOT NULL,
			source_key    TEXT NOT NULL,
			credential_id INTEGER REFERENCES external_provider_credentials(id) ON DELETE RESTRICT,
			config        TEXT NOT NULL DEFAULT '{}',
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, provider, source_key)
		)`,
		`INSERT INTO project_external_sources_new
			SELECT id, project_id, provider, source_key, credential_id, config, created_at
			FROM project_external_sources`,
		[]string{
			`CREATE INDEX IF NOT EXISTS idx_project_external_sources_project
				ON project_external_sources(project_id)`,
		},
	)

	rebuildWithoutProviderCheck(
		"external_user_identities",
		`CREATE TABLE external_user_identities_new (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider      TEXT NOT NULL,
			external_user TEXT NOT NULL,
			display_name  TEXT NOT NULL DEFAULT '',
			avatar_url    TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, provider, external_user)
		)`,
		`INSERT INTO external_user_identities_new
			SELECT id, user_id, provider, external_user, display_name, avatar_url,
			       created_at, updated_at
			FROM external_user_identities`,
		[]string{
			`CREATE INDEX IF NOT EXISTS idx_external_user_identities_provider_user
				ON external_user_identities(provider, external_user)`,
		},
	)

	rebuildWithoutProviderCheck(
		"external_write_prefs",
		`CREATE TABLE external_write_prefs_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider    TEXT NOT NULL,
			enabled     INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, provider)
		)`,
		`INSERT INTO external_write_prefs_new
			SELECT id, user_id, provider, enabled, created_at, updated_at
			FROM external_write_prefs`,
		nil,
	)
}

// migrateClaudeAccountsStep seeds the default-row pointing at ~/.claude/
// (config_dir = "") if the pool has no default row yet. Idempotent via the
// partial unique index claude_accounts_only_one_default. Probes ~/.claude.json
// for an existing OAuth login so the seed row starts in 'active' state when
// the user has already logged in via the Claude CLI.
//
// Default row uses sentinel name '__default__' (per spec §"默认行不变量");
// frontend renders i18n "默认 / Default / 默認" based on config_dir == ”.
func migrateClaudeAccountsStep(db *sql.DB) {
	var existing int
	row := db.QueryRow(`SELECT 1 FROM claude_accounts WHERE config_dir = '' LIMIT 1`)
	if err := row.Scan(&existing); err == nil {
		return // already seeded
	} else if err != sql.ErrNoRows {
		slog.Warn("migrateClaudeAccounts: probe default row failed", "err", err)
		return
	}

	email := claudehome.ReadEmailFromHomeJSON()
	status := "pending"
	if email != "" || claudehome.CredsExistInHome() {
		status = "active"
	}

	var emailVal sql.NullString
	if email != "" {
		emailVal = sql.NullString{String: email, Valid: true}
	}

	q := `INSERT INTO claude_accounts (name, email, config_dir, visibility, status, created_at)
	      VALUES ('__default__', ?, '', 'public', ?, ?)`
	if Driver == "postgres" {
		q = ConvertPlaceholders(q)
	}
	if _, err := db.Exec(q, emailVal, status, time.Now().Unix()); err != nil {
		slog.Warn("migrateClaudeAccounts: seed default row failed", "err", err)
	}
}

// reconcileWorkspacesFKsSQLite rebuilds the workspaces table on SQLite when
// the workspaces row in sqlite_master is missing the canonical `created_by ...
// REFERENCES users(id) ON DELETE SET NULL` clause. This brings upgraded DBs
// into sync with the schema.sql declaration (and with PostgreSQL's deferred-FK
// pattern). It also incidentally restores any other FK that may have been
// added by addColumnIfNotExists without REFERENCES, since the rebuild adopts
// the canonical CREATE TABLE wholesale.
//
// Idempotent on three layers:
//  1. Marker `workspaces_fk_reconcile_v1` short-circuits subsequent runs.
//  2. The DDL string check skips the rebuild on fresh DBs (where schema.sql
//     ran first and the FK is already present).
//  3. The rebuild itself is a single transaction: any step failure rolls
//     back to the original state and leaves the marker unset, so the next
//     startup retries.
func reconcileWorkspacesFKsSQLite(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "workspaces_fk_reconcile_v1") {
		return
	}

	// Detect whether the FK on created_by is present using PRAGMA
	// foreign_key_list, which returns one row per declared FK with the
	// originating column in `from`. A naive substring check on the table's
	// CREATE statement would be fooled by current_session_user_id, which
	// also REFERENCES users(id) ON DELETE SET NULL.
	fkRows, err := db.Query(`PRAGMA foreign_key_list(workspaces)`)
	if err != nil {
		slog.Warn("reconcileWorkspacesFKsSQLite: foreign_key_list query failed", "error", err)
		return
	}
	createdByFKPresent := false
	for fkRows.Next() {
		// pragma_foreign_key_list columns: id, seq, table, from, to, on_update, on_delete, match
		var (
			fkID, fkSeq                                int
			fkTable, fkFrom, fkTo, onUpd, onDel, match string
		)
		if err := fkRows.Scan(&fkID, &fkSeq, &fkTable, &fkFrom, &fkTo, &onUpd, &onDel, &match); err != nil {
			fkRows.Close()
			slog.Warn("reconcileWorkspacesFKsSQLite: scan foreign_key_list row failed", "error", err)
			return
		}
		if fkFrom == "created_by" && fkTable == "users" && onDel == "SET NULL" {
			createdByFKPresent = true
		}
	}
	fkRows.Close()

	if createdByFKPresent {
		// Already canonical. Set the marker so we don't re-check on every boot.
		markMigration(w, "workspaces_fk_reconcile_v1")
		return
	}

	// Need rebuild. Snapshot the indexes on the workspaces table so we can
	// recreate them post-rename. Auto-indexes (PRIMARY KEY, UNIQUE columns)
	// regenerate automatically and don't appear here.
	type idxRow struct{ name, sql string }
	rows, err := db.Query(
		`SELECT name, sql FROM sqlite_master WHERE type='index' AND tbl_name='workspaces' AND sql IS NOT NULL`)
	if err != nil {
		slog.Warn("reconcileWorkspacesFKsSQLite: list indexes failed", "error", err)
		return
	}
	var indexes []idxRow
	for rows.Next() {
		var r idxRow
		if err := rows.Scan(&r.name, &r.sql); err != nil {
			rows.Close()
			slog.Warn("reconcileWorkspacesFKsSQLite: scan index failed", "error", err)
			return
		}
		indexes = append(indexes, r)
	}
	rows.Close()

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("reconcileWorkspacesFKsSQLite: disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("reconcileWorkspacesFKsSQLite: begin tx failed", "error", err)
		return
	}

	// Canonical CREATE TABLE matching server/internal/store/schema.sql.
	// Kept in sync manually — if schema.sql changes, update this string too.
	// harness_id and team_id removed in Phase 7.
	//
	// mcp_servers added 2026-05-17 (per-workspace MCP config). The column is
	// omitted from copyData on purpose: when reconcileWorkspacesFKsSQLite runs
	// on a DB that pre-dates the mcp_servers migration, the old workspaces
	// table has no such column, so SELECT-ing it would error. The new table's
	// DEFAULT '[]' backfills every migrated row. On DBs where the migration
	// already added the column, the same DEFAULT still applies because we
	// don't list mcp_servers in the INSERT — the data on the old rows was
	// '[]' anyway (every existing row was filled by the column default).
	const canonicalCreate = `CREATE TABLE workspaces_new (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id       INTEGER REFERENCES issues(id) ON DELETE SET NULL,
		name           TEXT NOT NULL DEFAULT '',
		path           TEXT NOT NULL,
		status         TEXT NOT NULL DEFAULT 'created',
		agent_pid      INTEGER DEFAULT NULL,
		agent_status   TEXT DEFAULT 'idle',
		session_id     TEXT DEFAULT NULL,
		session_status TEXT DEFAULT 'idle',
		owner_type               TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
		owner_id                 INTEGER NOT NULL DEFAULT 0,
		current_session_user_id  INTEGER DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
		created_by               INTEGER DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
		created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		is_temporary   INTEGER NOT NULL DEFAULT 0,
		is_archived    INTEGER NOT NULL DEFAULT 0,
		archived_at    TIMESTAMP DEFAULT NULL,
		claude_account_id INTEGER DEFAULT NULL REFERENCES claude_accounts(id) ON DELETE SET NULL,
		mcp_servers TEXT NOT NULL DEFAULT '[]'
	)`

	// Explicit column list (both sides) — INSERT ... SELECT * is fragile if
	// columns get reordered.
	// harness_id and team_id removed in Phase 7.
	// mcp_servers intentionally omitted — see canonicalCreate comment.
	const copyData = `INSERT INTO workspaces_new (
		id, issue_id, name, path, status, agent_pid, agent_status,
		session_id, session_status,
		owner_type, owner_id, current_session_user_id, created_by,
		created_at, updated_at, is_temporary, is_archived, archived_at,
		claude_account_id
	) SELECT
		id, issue_id, name, path, status, agent_pid, agent_status,
		session_id, session_status,
		owner_type, owner_id, current_session_user_id, created_by,
		created_at, updated_at, is_temporary, is_archived, archived_at,
		claude_account_id
	FROM workspaces`

	stmts := []string{
		canonicalCreate,
		copyData,
		`DROP TABLE workspaces`,
		`ALTER TABLE workspaces_new RENAME TO workspaces`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			slog.Warn("reconcileWorkspacesFKsSQLite: rebuild step failed",
				"error", err, "stmt_prefix", stmt[:min(60, len(stmt))])
			return
		}
	}

	// Recreate the indexes that were on the old table. Each preserved its
	// CREATE INDEX SQL in sqlite_master.
	for _, idx := range indexes {
		if _, err := tx.Exec(idx.sql); err != nil {
			_ = tx.Rollback()
			slog.Warn("reconcileWorkspacesFKsSQLite: recreate index failed",
				"index", idx.name, "error", err)
			return
		}
	}

	// Verify FK integrity post-rebuild. Any rows pointing at deleted users /
	// teams / issues / harnesses surface here. We don't auto-clean — if this
	// fires, an operator should investigate before retry. ROLLBACK keeps the
	// DB in original state; marker stays unset so next startup tries again.
	checkRows, err := tx.Query(`PRAGMA foreign_key_check(workspaces)`)
	if err != nil {
		_ = tx.Rollback()
		slog.Warn("reconcileWorkspacesFKsSQLite: foreign_key_check query failed", "error", err)
		return
	}
	var violations int
	for checkRows.Next() {
		violations++
	}
	checkRows.Close()
	if violations > 0 {
		_ = tx.Rollback()
		slog.Error("reconcileWorkspacesFKsSQLite: FK check failed; aborting rebuild",
			"violations", violations,
			"hint", "existing rows reference deleted users/teams/issues/harnesses; clean orphans manually then retry on next startup")
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("reconcileWorkspacesFKsSQLite: commit failed", "error", err)
		return
	}
	markMigration(w, "workspaces_fk_reconcile_v1")
	slog.Info("reconcileWorkspacesFKsSQLite: rebuilt workspaces with canonical FKs",
		"recreated_indexes", len(indexes))
}

// migrateSavedQueriesSourceNullable drops the NOT NULL constraint on
// saved_queries.source_id so static / direct-result charts (which have no
// backing data source) can be pinned with source_id = NULL. Editing the
// CREATE TABLE in schema.sql only nullable-izes fresh DBs; existing DBs kept
// source_id NOT NULL and 500'd on a static pin. Idempotent + driver-aware:
// PG does a direct ALTER; SQLite needs the 12-step rebuild (can't drop NOT
// NULL in place). The `snapshot` column must already exist (added just above
// via addColumnIfNotExists) so the copy carries it.
func migrateSavedQueriesSourceNullable(db *sql.DB) error {
	if Driver == "postgres" {
		if _, err := db.Exec("ALTER TABLE saved_queries ALTER COLUMN source_id DROP NOT NULL"); err != nil {
			// Idempotent: ignore "already nullable" / missing-table errors.
			if !strings.Contains(err.Error(), "is not marked NOT NULL") &&
				!strings.Contains(err.Error(), "does not exist") {
				return fmt.Errorf("drop NOT NULL on saved_queries.source_id: %w", err)
			}
		}
		return nil
	}

	// SQLite: rebuild only if source_id is still NOT NULL.
	var notNull int
	err := db.QueryRow(
		`SELECT "notnull" FROM pragma_table_info('saved_queries') WHERE name='source_id'`,
	).Scan(&notNull)
	if err == sql.ErrNoRows {
		return nil // table/column not present yet (fresh DB already nullable via schema.sql)
	}
	if err != nil {
		return fmt.Errorf("sniff saved_queries.source_id NOT NULL: %w", err)
	}
	if notNull == 0 {
		return nil // already nullable
	}

	// Snapshot user-defined indexes to recreate after the rename.
	type idxRow struct{ name, sqlText string }
	rows, err := db.Query(
		`SELECT name, sql FROM sqlite_master WHERE type='index' AND tbl_name='saved_queries' AND sql IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("list saved_queries indexes: %w", err)
	}
	var indexes []idxRow
	for rows.Next() {
		var r idxRow
		if err := rows.Scan(&r.name, &r.sqlText); err != nil {
			rows.Close()
			return fmt.Errorf("scan saved_queries index: %w", err)
		}
		indexes = append(indexes, r)
	}
	rows.Close()

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable FK: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Canonical CREATE TABLE matching schema.sql (source_id nullable + snapshot).
	// Kept in sync manually with schema.sql's saved_queries block. Column order
	// is irrelevant here because the copy uses an explicit column list.
	const canonicalCreate = `CREATE TABLE saved_queries_new (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
		owner_id     INTEGER NOT NULL DEFAULT 0,
		source_id    INTEGER REFERENCES data_sources(id) ON DELETE CASCADE,
		workspace_id INTEGER REFERENCES workspaces(id) ON DELETE SET NULL,
		name         TEXT NOT NULL,
		operation    TEXT NOT NULL DEFAULT '{}',
		chart_spec   TEXT NOT NULL DEFAULT '{}',
		snapshot     TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	const copyData = `INSERT INTO saved_queries_new (
		id, owner_type, owner_id, source_id, workspace_id, name, operation, chart_spec, snapshot, created_at, updated_at
	) SELECT
		id, owner_type, owner_id, source_id, workspace_id, name, operation, chart_spec, snapshot, created_at, updated_at
	FROM saved_queries`

	for _, stmt := range []string{
		canonicalCreate,
		copyData,
		`DROP TABLE saved_queries`,
		`ALTER TABLE saved_queries_new RENAME TO saved_queries`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("rebuild saved_queries step (%.40s): %w", stmt, err)
		}
	}
	for _, idx := range indexes {
		if _, err := tx.Exec(idx.sqlText); err != nil {
			return fmt.Errorf("recreate saved_queries index %s: %w", idx.name, err)
		}
	}

	checkRows, err := tx.Query(`PRAGMA foreign_key_check(saved_queries)`)
	if err != nil {
		return fmt.Errorf("fk check saved_queries: %w", err)
	}
	var violations int
	for checkRows.Next() {
		violations++
	}
	checkRows.Close()
	if violations > 0 {
		return fmt.Errorf("saved_queries rebuild FK check found %d violations; aborting", violations)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit saved_queries rebuild: %w", err)
	}
	slog.Info("migrateSavedQueriesSourceNullable: rebuilt saved_queries with nullable source_id",
		"recreated_indexes", len(indexes))
	return nil
}

// fixWorkspacesIssueFKPostgres re-creates the workspaces_issue_id_fkey constraint
// with ON DELETE SET NULL so that deleting a project (which cascades to issues)
// does not violate the FK and instead nullifies the workspace's issue reference.
func fixWorkspacesIssueFKPostgres(db *sql.DB) {
	var cnt int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_name = 'workspaces'
		  AND constraint_name = 'workspaces_issue_id_fkey'
		  AND constraint_type = 'FOREIGN KEY'`).Scan(&cnt)
	if err != nil || cnt == 0 {
		return
	}
	var deleteRule string
	_ = db.QueryRow(`
		SELECT rc.delete_rule FROM information_schema.referential_constraints rc
		JOIN information_schema.table_constraints tc
		  ON rc.constraint_name = tc.constraint_name
		WHERE tc.table_name = 'workspaces' AND tc.constraint_name = 'workspaces_issue_id_fkey'`).Scan(&deleteRule)
	if deleteRule == "SET NULL" {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		slog.Warn("fixWorkspacesIssueFKPostgres: begin tx failed", "error", err)
		return
	}
	if _, err := tx.Exec(`ALTER TABLE workspaces DROP CONSTRAINT workspaces_issue_id_fkey`); err != nil {
		_ = tx.Rollback()
		slog.Warn("drop workspaces_issue_id_fkey failed", "error", err)
		return
	}
	if _, err := tx.Exec(`ALTER TABLE workspaces ADD CONSTRAINT workspaces_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE SET NULL`); err != nil {
		_ = tx.Rollback()
		slog.Warn("re-add workspaces_issue_id_fkey failed", "error", err)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("fixWorkspacesIssueFKPostgres: commit failed", "error", err)
	}
}

// fixWorkspacesIssueFKSQLite recreates the workspaces table with the corrected
// ON DELETE SET NULL on issue_id. SQLite does not support ALTER CONSTRAINT so a
// table-recreation is required. The operation is wrapped in a transaction with
// foreign-key enforcement disabled to avoid constraint errors during the move.
func fixWorkspacesIssueFKSQLite(db *sql.DB) {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspaces'`).Scan(&ddl); err != nil {
		return
	}
	if strings.Contains(ddl, "ON DELETE SET NULL") {
		return // already migrated
	}

	// Disable FK enforcement for the duration of the table swap.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("fixWorkspacesIssueFKSQLite: disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("fixWorkspacesIssueFKSQLite: begin tx failed", "error", err)
		return
	}

	// harness_id and team_id removed in Phase 7.
	stmts := []string{
		`CREATE TABLE workspaces_new (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id       INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			name           TEXT NOT NULL DEFAULT '',
			path           TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'created',
			agent_pid      INTEGER DEFAULT NULL,
			agent_status   TEXT DEFAULT 'idle',
			session_id     TEXT DEFAULT NULL,
			session_status TEXT DEFAULT 'idle',
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			is_temporary   INTEGER NOT NULL DEFAULT 0,
			is_archived    INTEGER NOT NULL DEFAULT 0,
			archived_at    TIMESTAMP DEFAULT NULL
		)`,
		`INSERT INTO workspaces_new
			SELECT id, issue_id, name, path, status, agent_pid, agent_status,
			       session_id, session_status,
			       created_at, updated_at, is_temporary, is_archived, archived_at
			FROM workspaces`,
		`DROP TABLE workspaces`,
		`ALTER TABLE workspaces_new RENAME TO workspaces`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			slog.Warn("fixWorkspacesIssueFKSQLite: migration step failed", "error", err, "stmt", stmt[:40])
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("fixWorkspacesIssueFKSQLite: commit failed", "error", err)
	}
}

// dropLegacyUniqueConstraints removes the global column-level UNIQUE on
// projects.name and repositories.path that predate multi-tenant ownership.
// Per-owner composite indexes from addOwnerModel are the correct replacement.
// Idempotent: SQLite variant inspects the DDL before rebuilding; Postgres
// variant checks information_schema before dropping.
func dropLegacyUniqueConstraints(db *sql.DB) {
	if Driver == "postgres" {
		dropLegacyUniqueConstraintsPostgres(db)
	} else {
		dropProjectsNameUniqueSQLite(db)
		dropRepositoriesPathUniqueSQLite(db)
	}
}

func dropLegacyUniqueConstraintsPostgres(db *sql.DB) {
	// projects.name unique constraint
	var cnt int
	_ = db.QueryRow(`
		SELECT COUNT(*) FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
		  ON tc.constraint_name = ccu.constraint_name
		WHERE tc.table_name = 'projects' AND ccu.column_name = 'name'
		  AND tc.constraint_type = 'UNIQUE'`).Scan(&cnt)
	if cnt > 0 {
		if _, err := db.Exec(`ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_name_key`); err != nil {
			slog.Warn("drop projects_name_key failed", "error", err)
		}
	}

	// repositories.path unique constraint
	_ = db.QueryRow(`
		SELECT COUNT(*) FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
		  ON tc.constraint_name = ccu.constraint_name
		WHERE tc.table_name = 'repositories' AND ccu.column_name = 'path'
		  AND tc.constraint_type = 'UNIQUE'`).Scan(&cnt)
	if cnt > 0 {
		if _, err := db.Exec(`ALTER TABLE repositories DROP CONSTRAINT IF EXISTS repositories_path_key`); err != nil {
			slog.Warn("drop repositories_path_key failed", "error", err)
		}
	}
}

func dropProjectsNameUniqueSQLite(db *sql.DB) {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&ddl); err != nil {
		return
	}
	if !strings.Contains(ddl, "UNIQUE") {
		return // already clean
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("dropProjectsNameUniqueSQLite: disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("dropProjectsNameUniqueSQLite: begin tx failed", "error", err)
		return
	}

	stmts := []string{
		`CREATE TABLE projects_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL,
			description TEXT DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'active',
			owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id    INTEGER NOT NULL DEFAULT 0,
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO projects_new SELECT id, name, description, status, owner_type, owner_id, created_at, updated_at FROM projects`,
		`DROP TABLE projects`,
		`ALTER TABLE projects_new RENAME TO projects`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			slog.Warn("dropProjectsNameUniqueSQLite: step failed", "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("dropProjectsNameUniqueSQLite: commit failed", "error", err)
		return
	}

	// Recreate per-owner indexes that were dropped with the old table.
	idxStmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects(owner_type, owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_owner_name_unique ON projects(owner_type, owner_id, name)`,
	}
	for _, stmt := range idxStmts {
		if _, err := db.Exec(stmt); err != nil {
			slog.Warn("dropProjectsNameUniqueSQLite: recreate index failed", "error", err, "stmt", stmt)
		}
	}
}

func dropRepositoriesPathUniqueSQLite(db *sql.DB) {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='repositories'`).Scan(&ddl); err != nil {
		return
	}
	if !strings.Contains(ddl, "UNIQUE") {
		return // already clean
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("dropRepositoriesPathUniqueSQLite: disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("dropRepositoriesPathUniqueSQLite: begin tx failed", "error", err)
		return
	}

	stmts := []string{
		`CREATE TABLE repositories_new (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT NOT NULL,
			path           TEXT NOT NULL,
			git_remote     TEXT DEFAULT '',
			default_branch TEXT DEFAULT 'main',
			owner_type     TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id       INTEGER NOT NULL DEFAULT 0,
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO repositories_new SELECT id, name, path, git_remote, default_branch, owner_type, owner_id, created_at, updated_at FROM repositories`,
		`DROP TABLE repositories`,
		`ALTER TABLE repositories_new RENAME TO repositories`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			slog.Warn("dropRepositoriesPathUniqueSQLite: step failed", "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("dropRepositoriesPathUniqueSQLite: commit failed", "error", err)
		return
	}

	// Recreate per-owner indexes that were dropped with the old table.
	idxStmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_repositories_owner ON repositories(owner_type, owner_id)`,
		// Partial unique index: only enforce uniqueness when git_remote is non-empty.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_owner_remote_unique ON repositories(owner_type, owner_id, path) WHERE git_remote != ''`,
	}
	for _, stmt := range idxStmts {
		if _, err := db.Exec(stmt); err != nil {
			slog.Warn("dropRepositoriesPathUniqueSQLite: recreate index failed", "error", err, "stmt", stmt)
		}
	}
}

// addMcpServersToWorkspaces adds the workspaces.mcp_servers column on
// existing databases. Driver-aware: SQLite stores the JSON array as TEXT,
// PostgreSQL as JSONB with a JSONB default cast. Idempotent via columnExists.
func addMcpServersToWorkspaces(ctx context.Context, db *sql.DB) {
	_ = ctx // ctx reserved for future use; columnExists / db.Exec don't take ctx today
	exists, err := columnExists(db, "workspaces", "mcp_servers")
	if err != nil {
		slog.Warn("check workspaces.mcp_servers existence failed", "error", err)
		return
	}
	if exists {
		return
	}
	var stmt string
	if Driver == "postgres" {
		stmt = `ALTER TABLE workspaces ADD COLUMN mcp_servers JSONB NOT NULL DEFAULT '[]'::jsonb`
	} else {
		stmt = `ALTER TABLE workspaces ADD COLUMN mcp_servers TEXT NOT NULL DEFAULT '[]'`
	}
	if _, err := db.Exec(stmt); err != nil {
		slog.Warn("add workspaces.mcp_servers failed", "error", err)
	}
}

func addColumnIfNotExists(db *sql.DB, table, column, colDef string) {
	exists, err := columnExists(db, table, column)
	if err != nil {
		slog.Warn("check column existence failed", "table", table, "column", column, "error", err)
		return
	}
	if exists {
		return
	}

	// Note: table/column/colDef are trusted internal literals, not user input.
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + colDef)
	if err != nil {
		slog.Warn("add column failed", "table", table, "column", column, "error", err)
	}
}

// dropColumnIfExists removes a column iff it exists. Idempotent and
// driver-aware: both SQLite 3.35+ and PostgreSQL accept
// `ALTER TABLE ... DROP COLUMN`, so we don't need driver branching for the
// DDL itself, but we do branch via columnExists (which already handles
// pragma_table_info vs information_schema). Errors only log — a failed
// DROP COLUMN must not crash-loop the server; the column stays around and
// the next code path that ignored it keeps working.
//
// CAVEAT: SQLite ALTER TABLE DROP COLUMN is a no-op when the column is
// part of any active CHECK / FOREIGN KEY / UNIQUE constraint or any
// non-PK index. The writeback columns we drop here have no such
// dependencies (the only writeback-related index is already dropped above),
// so the bare DROP COLUMN suffices. If you add a DROP for a constrained
// column, fall back to the SQLite recreate-table dance.
func dropColumnIfExists(db *sql.DB, table, column string) error {
	exists, err := columnExists(db, table, column)
	if err != nil {
		return fmt.Errorf("check column existence: %w", err)
	}
	if !exists {
		return nil
	}
	// Note: table/column are trusted internal literals, not user input.
	if _, err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column); err != nil {
		return fmt.Errorf("drop %s.%s: %w", table, column, err)
	}
	return nil
}

func migrateIssueAssigneeLabelsAddTables(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "issue_assignee_labels_v1") {
		return
	}

	fk := fkType()
	pkType := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if Driver == "postgres" {
		pkType = "BIGSERIAL PRIMARY KEY"
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS labels (
			id           ` + pkType + `,
			project_id   ` + fk + ` NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			name         TEXT NOT NULL,
			color        TEXT NOT NULL,
			description  TEXT NOT NULL DEFAULT '',
			created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_by   ` + fk + ` NOT NULL REFERENCES users(id),
			UNIQUE(project_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_labels_project ON labels(project_id)`,
		`CREATE TABLE IF NOT EXISTS issue_labels (
			issue_id  ` + fk + ` NOT NULL REFERENCES issues(id)  ON DELETE CASCADE,
			label_id  ` + fk + ` NOT NULL REFERENCES labels(id)  ON DELETE CASCADE,
			PRIMARY KEY (issue_id, label_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_issue_labels_label ON issue_labels(label_id)`,
		`CREATE TABLE IF NOT EXISTS issue_assignees (
			issue_id    ` + fk + ` NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id     ` + fk + ` NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
			assigned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (issue_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_issue_assignees_user ON issue_assignees(user_id)`,
	}
	ctx := context.Background()
	for _, s := range stmts {
		if _, err := w.ExecContext(ctx, s); err != nil {
			slog.Error("create issue-properties table failed",
				"first_line", strings.SplitN(s, "\n", 2)[0], "error", err)
			return // leave marker unset; next start will retry
		}
	}
	markMigration(w, "issue_assignee_labels_v1")
}

// migrateIssueAssigneeLabelsDropLegacyColumns drops the deprecated
// issues.labels / issues.assignee TEXT columns. Phase 1 migrated all consumers
// to the new join tables; phase 2 finally removes the dead columns.
//
// Hard fail (panic) on DROP COLUMN error rather than silently logging:
// a half-dropped schema would leave application code reading columns that
// don't exist. Better to fail startup loudly than to limp along.
func migrateIssueAssigneeLabelsDropLegacyColumns(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "issue_assignee_labels_drop_legacy_v1") {
		return
	}

	for _, col := range []string{"labels", "assignee"} {
		exists, err := columnExists(db, "issues", col)
		if err != nil {
			panic(fmt.Sprintf("check issues.%s exists: %v", col, err))
		}
		if !exists {
			continue
		}
		if _, err := db.Exec("ALTER TABLE issues DROP COLUMN " + col); err != nil {
			panic(fmt.Sprintf("drop issues.%s: %v", col, err))
		}
	}
	markMigration(w, "issue_assignee_labels_drop_legacy_v1")
}

// migrateLearningsToMemory copies existing project_learnings rows into the
// owner-scoped memories table (#256), deriving owner from the parent project,
// preserving timestamps, and creating a v1 version snapshot for each. category
// maps 1:1 to mem_type (pattern/gotcha/decision/error_fix are valid mem_type
// values) and source carries over (manual/mcp/extract). project_learnings is
// left intact during the transition — the .learnings.generated.md injection
// unions both stores until the legacy learning_* write paths are repointed and
// the table is retired.
//
// Idempotent two ways: a schema_migrations marker for the fast path, and a
// NOT EXISTS guard on the copy so an unmarked re-run still cannot duplicate.
// Uses INSERT...SELECT (no `?` params) so it is dual-driver safe via the wrapper.
func migrateLearningsToMemory(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "learnings_to_memory_v1") {
		return
	}
	ctx := context.Background()

	// Fresh DBs (post #256 table-drop) never create project_learnings, so there
	// is nothing to copy — mark done and return rather than erroring on a
	// missing table.
	if !projectLearningsTableExists(db) {
		markMigration(w, "learnings_to_memory_v1")
		return
	}

	if _, err := w.ExecContext(ctx, `
		INSERT INTO memories (owner_type, owner_id, project_id, workspace_id, mem_type, title, content, source, source_path, version, created_at, updated_at)
		SELECT p.owner_type, p.owner_id, pl.project_id, pl.workspace_id, pl.category, pl.title, pl.content, pl.source, '', 1, pl.created_at, pl.updated_at
		FROM project_learnings pl
		JOIN projects p ON p.id = pl.project_id
		WHERE NOT EXISTS (
			SELECT 1 FROM memories m
			WHERE m.project_id = pl.project_id AND m.mem_type = pl.category AND m.title = pl.title AND m.deleted_at IS NULL
		)`); err != nil {
		slog.Warn("migrateLearningsToMemory: copy failed", "error", err)
		return
	}

	// Backfill a v1 snapshot for any memory lacking one (covers the rows just
	// migrated; harmless for memories that already got a v1 from Create).
	if _, err := w.ExecContext(ctx, `
		INSERT INTO memory_versions (memory_id, version, mem_type, title, content, source, source_path, created_at)
		SELECT m.id, 1, m.mem_type, m.title, m.content, m.source, m.source_path, m.created_at
		FROM memories m
		WHERE NOT EXISTS (SELECT 1 FROM memory_versions v WHERE v.memory_id = m.id AND v.version = 1)`); err != nil {
		slog.Warn("migrateLearningsToMemory: version backfill failed", "error", err)
		return
	}

	markMigration(w, "learnings_to_memory_v1")
	slog.Info("migrateLearningsToMemory: project_learnings copied into memories")
}

// projectLearningsTableExists reports whether the legacy project_learnings table
// is still present (driver-aware). Used so the migration/drop steps are no-ops
// on databases that never had it.
func projectLearningsTableExists(db *sql.DB) bool {
	var n int
	if Driver == "postgres" {
		_ = db.QueryRow(
			`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='project_learnings'`).Scan(&n)
	} else {
		_ = db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='project_learnings'`).Scan(&n)
	}
	return n > 0
}

// dropProjectLearningsTable physically removes the legacy project_learnings
// table (#256 final cleanup). Runs AFTER migrateLearningsToMemory, so all data
// has been copied into memories first. Idempotent via marker + DROP IF EXISTS;
// a no-op on databases that never had the table.
func dropProjectLearningsTable(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "drop_project_learnings_v1") {
		return
	}
	if _, err := w.ExecContext(context.Background(), `DROP TABLE IF EXISTS project_learnings`); err != nil {
		slog.Warn("dropProjectLearningsTable: drop failed", "error", err)
		return
	}
	markMigration(w, "drop_project_learnings_v1")
	slog.Info("dropProjectLearningsTable: legacy project_learnings table dropped")
}

func migrationApplied(w *DB, key string) bool {
	ctx := context.Background()
	if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		slog.Error("ensure schema_migrations failed", "error", err)
		return false
	}
	var dummy string
	err := w.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = ?`, key).Scan(&dummy)
	return err == nil
}

func markMigration(w *DB, key string) {
	if _, err := w.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES (?)`, key); err != nil {
		slog.Warn("mark schema_migration failed", "key", key, "error", err)
	}
}

// widenTokenColumnsPostgres widens token/duration columns from int4 to BIGINT
// on Postgres. Fresh installs already create them as BIGINT (schema_postgres.sql);
// this only matters for a DB that created the tables from an earlier int4 build.
// No-op on SQLite (INTEGER is dynamically 64-bit). Idempotent: ALTER ... TYPE
// BIGINT on an already-BIGINT column is a cheap no-op. Each ALTER is guarded by
// the table existing (columnExists), so fresh installs without the tables yet
// (shouldn't happen — schema runs first) simply skip.
func widenTokenColumnsPostgres(db *sql.DB) {
	if Driver != "postgres" {
		return
	}
	cols := map[string][]string{
		"workspace_stats":        {"total_duration_ms", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens"},
		"workspace_token_hourly": {"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens"},
	}
	for table, names := range cols {
		for _, col := range names {
			exists, err := columnExists(db, table, col)
			if err != nil || !exists {
				continue
			}
			// table/col are trusted internal literals, not user input.
			if _, err := db.Exec("ALTER TABLE " + table + " ALTER COLUMN " + col + " TYPE BIGINT"); err != nil {
				slog.Warn("widen token column to BIGINT failed", "table", table, "column", col, "error", err)
			}
		}
	}
}

// backfillWorkspaceStats seeds workspace_stats once from existing
// agent_messages (role counts) + workspace_costs (turns/duration). Token
// lifetime totals are NOT backfilled (no historical per-type token detail);
// they accrue forward. Idempotent via schema_migrations marker.
func backfillWorkspaceStats(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "workspace_stats_backfill_v1") {
		return
	}
	_, err := w.ExecContext(context.Background(), `
INSERT INTO workspace_stats (
    workspace_id, owner_type, owner_id,
    user_message_count, ai_message_count, interaction_count,
    total_turns, total_duration_ms, last_activity_at, updated_at)
SELECT w.id, w.owner_type, w.owner_id,
       COALESCE(um.cnt,0), COALESCE(am.cnt,0), COALESCE(c.cnt,0),
       COALESCE(c.turns,0), COALESCE(c.dur,0), w.updated_at, CURRENT_TIMESTAMP
FROM workspaces w
LEFT JOIN (SELECT workspace_id, COUNT(*) AS cnt FROM agent_messages WHERE role = 'user' GROUP BY workspace_id) um ON um.workspace_id = w.id
LEFT JOIN (SELECT workspace_id, COUNT(*) AS cnt FROM agent_messages WHERE role = 'assistant' AND event_type = 'done' GROUP BY workspace_id) am ON am.workspace_id = w.id
LEFT JOIN (SELECT workspace_id, COUNT(*) AS cnt, SUM(num_turns) AS turns, SUM(duration_ms) AS dur FROM workspace_costs GROUP BY workspace_id) c ON c.workspace_id = w.id
ON CONFLICT(workspace_id) DO NOTHING`)
	if err != nil {
		slog.Warn("backfillWorkspaceStats failed", "error", err)
		return
	}
	markMigration(w, "workspace_stats_backfill_v1")
	slog.Info("backfillWorkspaceStats: seeded workspace_stats from existing data")
}

// ============================================================
// Scene-based MCP/plugin management migrations (M1, 2026-05-18)
// ============================================================

// addScenesTables is a no-op placeholder. The 5 scene-family tables (scenes,
// workspace_scene_layers, workspace_scene_projection, scene_asset_imports,
// project_default_scenes) are created by schema.sql / schema_postgres.sql via
// CREATE TABLE IF NOT EXISTS, which both fresh installs and existing DBs run
// at boot. Reserved here for future ALTER TABLE / index-on-migration-added-
// column operations specific to the scene domain.
func addScenesTables(db *sql.DB) {
	// dismissed_plugins: per-workspace list of plugin sources the user has
	// explicitly chosen to ignore, so the projection banner stops surfacing
	// their pending/failed rows. Added 2026-06-14; idempotent on fresh DBs
	// (already in schema.sql) via columnExists.
	addColumnIfNotExists(db, "workspace_scene_projection", "dismissed_plugins", "TEXT NOT NULL DEFAULT '[]'")
}

// addAssetSlugColumns adds the `slug TEXT NOT NULL DEFAULT ”` column to
// env_presets / quick_actions on existing databases.
// On fresh installs the column is already in schema.sql; this is idempotent
// via columnExists.
func addAssetSlugColumns(db *sql.DB) {
	for _, tbl := range []string{"env_presets", "quick_actions"} {
		addColumnIfNotExists(db, tbl, "slug", "TEXT NOT NULL DEFAULT ''")
	}

	// Partial unique indexes — must live in Migrate (not schema.sql) for legacy
	// DBs whose columns were just ALTER'd above. Idempotent via IF NOT EXISTS.
	idxStmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_env_presets_owner_slug
			ON env_presets(owner_type, owner_id, slug) WHERE slug != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_quick_actions_owner_slug
			ON quick_actions(owner_type, owner_id, slug) WHERE slug != ''`,
	}
	for _, stmt := range idxStmts {
		if _, err := db.Exec(stmt); err != nil {
			slog.Warn("create asset slug index failed", "stmt_prefix", stmt[:min(60, len(stmt))], "error", err)
		}
	}
}

// slugifyRe matches non-alphanumeric runs for the simple slugify routine used
// by backfillAssetSlugs. Slugify rule: lowercase, replace non-alnum runs with
// a single '-', trim leading/trailing '-'. Empty input → empty slug.
var slugifyRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = slugifyRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// backfillAssetSlugs populates `slug` on env_presets / quick_actions /
// project_templates rows whose slug is still empty, deriving the value from
// the existing name / label / name column. Idempotent via
// `schema_migrations.key = 'asset_slugs_backfilled_v1'`.
//
// Conflict handling: if two rows in the same owner scope would slugify to the
// same value, the second's slug stays empty (UNIQUE … WHERE slug != ” allows
// any number of empty slugs). The user can rename + retry; scene imports look
// up by slug so the conflicting row simply remains "not yet importable".
func backfillAssetSlugs(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "asset_slugs_backfilled_v1") {
		return
	}
	ctx := context.Background()

	type spec struct {
		table     string
		sourceCol string
		scopeCols string // e.g. "owner_type, owner_id" or "project_id"
	}
	for _, s := range []spec{
		{"env_presets", "name", "owner_type, owner_id"},
		{"quick_actions", "label", "owner_type, owner_id"},
	} {
		// Fetch all rows with empty slug.
		q := fmt.Sprintf(`SELECT id, %s FROM %s WHERE slug = ''`, s.sourceCol, s.table)
		rows, err := w.QueryContext(ctx, q)
		if err != nil {
			slog.Warn("backfillAssetSlugs: query failed", "table", s.table, "error", err)
			continue
		}
		type row struct {
			id  int64
			src string
		}
		var todo []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.src); err != nil {
				slog.Warn("backfillAssetSlugs: scan failed", "table", s.table, "error", err)
				continue
			}
			todo = append(todo, r)
		}
		rows.Close()

		// Try each candidate slug. On unique-index conflict, leave slug empty.
		updateQ := fmt.Sprintf(`UPDATE %s SET slug = ? WHERE id = ? AND slug = ''`, s.table)
		for _, r := range todo {
			candidate := slugifyName(r.src)
			if candidate == "" {
				continue
			}
			if _, err := w.ExecContext(ctx, updateQ, candidate, r.id); err != nil {
				// Unique conflict (or other) — skip; row stays unslugged.
				slog.Debug("backfillAssetSlugs: update skipped (likely conflict)",
					"table", s.table, "id", r.id, "candidate", candidate, "error", err)
			}
		}
	}
	markMigration(w, "asset_slugs_backfilled_v1")
}

// migrateLegacyToBaseLayer promotes each pre-existing workspace's local
// configuration into an implicit base layer row in workspace_scene_layers
// (is_base=1, position=0, scene_id=NULL). Only the mcp_servers JSON column
// is extracted; per architecture review (2026-05-18) workspaces hold no
// direct env_preset or template references, so those fields are empty.
//
// Idempotent via `schema_migrations.key = 'scenes_base_promoted'`. Failures
// per workspace are logged but do not abort the loop — re-running the
// migration after the marker is set is a no-op, so the marker is only set
// once we've attempted every workspace successfully. On crash mid-loop the
// next boot retries from scratch; INSERT OR IGNORE on the per-workspace base
// partial unique index makes the retry idempotent.
func migrateLegacyToBaseLayer(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "scenes_base_promoted") {
		return
	}
	ctx := context.Background()

	rows, err := w.QueryContext(ctx,
		`SELECT id, mcp_servers FROM workspaces WHERE is_archived = 0`)
	if err != nil {
		slog.Warn("migrateLegacyToBaseLayer: query workspaces failed", "error", err)
		return
	}
	type wsRow struct {
		id         int64
		mcpServers sql.NullString
	}
	var list []wsRow
	for rows.Next() {
		var r wsRow
		if err := rows.Scan(&r.id, &r.mcpServers); err != nil {
			rows.Close()
			slog.Warn("migrateLegacyToBaseLayer: scan failed", "error", err)
			return
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("migrateLegacyToBaseLayer: rows.Err", "error", err)
		return
	}

	for _, r := range list {
		baseDef := buildBaseDefinitionFromLegacy(r.mcpServers.String)
		defBytes, err := json.Marshal(baseDef)
		if err != nil {
			slog.Warn("migrateLegacyToBaseLayer: marshal base def failed",
				"workspace_id", r.id, "error", err)
			continue
		}
		// INSERT OR IGNORE → ON CONFLICT DO NOTHING on PG via wrapper. The
		// partial unique index idx_ws_scene_layers_base ensures one base layer
		// per workspace; conflicts here are normal under crash-restart retry.
		if _, err := w.ExecContext(ctx,
			`INSERT OR IGNORE INTO workspace_scene_layers
				(workspace_id, scene_id, position, is_base, base_definition)
			 VALUES (?, NULL, 0, 1, ?)`,
			r.id, string(defBytes),
		); err != nil {
			slog.Warn("migrateLegacyToBaseLayer: insert base layer failed",
				"workspace_id", r.id, "error", err)
			// keep going — best-effort; marker set only at end so partial runs retry
			continue
		}
	}
	markMigration(w, "scenes_base_promoted")
}

// buildBaseDefinitionFromLegacy converts a workspace's mcp_servers JSON
// (an array of MCP server names) into a base-layer definition shaped like a
// scene Definition with empty plugins/assets/prompts/required_credentials.
//
// Only the mcp[] field is populated. Per architecture review (2026-05-18)
// workspaces table has no direct env_preset or template foreign keys to
// extract, so the legacy extraction is mcp-only.
func buildBaseDefinitionFromLegacy(mcpJSON string) map[string]any {
	var names []string
	if mcpJSON != "" && mcpJSON != "null" {
		if err := json.Unmarshal([]byte(mcpJSON), &names); err != nil {
			// Tolerate corrupt JSON: fall back to empty list rather than fail
			// the entire migration.
			names = nil
		}
	}
	mcp := make([]map[string]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		mcp = append(mcp, map[string]string{"name": n})
	}
	return map[string]any{
		"mcp":                  mcp,
		"plugins":              []any{},
		"assets":               map[string]any{},
		"prompts":              []any{},
		"required_credentials": []any{},
	}
}

func addAuditIndexes(db *sql.DB) {
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_audit_user ON external_api_audit(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_provider ON external_api_audit(provider_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_created ON external_api_audit(created_at)`,
	} {
		if _, err := db.Exec(idx); err != nil {
			slog.Warn("audit index creation failed", "index", idx, "error", err)
		}
	}
}

// MigrateExternalProviders creates the external_providers, external_api_audit,
// and external_api_write_prefs tables if they don't exist (added for the
// AI-adaptive external API proxy redesign).
//
// On fresh installs these tables already exist via schema.sql /
// schema_postgres.sql. This migration handles existing databases that were
// created before the proxy redesign.
//
// Uses idempotent markers in schema_migrations (external_providers_v1,
// external_api_audit_v1, external_api_write_prefs_v1) so each table
// creation runs at most once.
func MigrateExternalProviders(db *sql.DB) error {
	w := Wrap(db)
	ctx := context.Background()

	idCol, intCol, tsCol := "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER", "DATETIME"
	if Driver == "postgres" {
		idCol, intCol, tsCol = "BIGSERIAL PRIMARY KEY", "BIGINT", "TIMESTAMPTZ"
	}

	// external_providers
	if !migrationApplied(w, "external_providers_v1") {
		if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_providers (
			id `+idCol+`,
			name TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL DEFAULT '',
			api_base_url TEXT NOT NULL,
			auth_type TEXT NOT NULL DEFAULT 'bearer',
			auth_header TEXT NOT NULL DEFAULT 'Authorization',
			auth_prefix TEXT NOT NULL DEFAULT 'Bearer',
			profile TEXT NOT NULL DEFAULT '',
			openapi_url TEXT NOT NULL DEFAULT '',
			whitelist TEXT NOT NULL DEFAULT '',
			enabled `+intCol+` NOT NULL DEFAULT 1,
			created_by TEXT NOT NULL DEFAULT 'user',
			created_at `+tsCol+` NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at `+tsCol+` NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
			return fmt.Errorf("create external_providers: %w", err)
		}
		markMigration(w, "external_providers_v1")
	}

	// external_api_audit
	if !migrationApplied(w, "external_api_audit_v1") {
		if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_api_audit (
			id `+idCol+`,
			user_id `+intCol+` NOT NULL,
			provider_id `+intCol+` NOT NULL,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			status_code `+intCol+` NOT NULL DEFAULT 0,
			created_at `+tsCol+` NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
			return fmt.Errorf("create external_api_audit: %w", err)
		}
		markMigration(w, "external_api_audit_v1")
	}

	// external_api_write_prefs
	if !migrationApplied(w, "external_api_write_prefs_v1") {
		if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_api_write_prefs (
			user_id `+intCol+` NOT NULL,
			provider_id `+intCol+` NOT NULL REFERENCES external_providers(id) ON DELETE CASCADE,
			enabled `+intCol+` NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, provider_id)
		)`); err != nil {
			return fmt.Errorf("create external_api_write_prefs: %w", err)
		}
		markMigration(w, "external_api_write_prefs_v1")
	}

	// Add audit indexes regardless — fresh installs need them, and
	// existing DBs that ran the migration before indexes were added
	// will pick them up here.
	addAuditIndexes(db)

	return nil
}

// migrateHarnessSpecsTypedColumns adds typed config columns to harness_specs
// and derives kind + typed field values from existing (category, name, config)
// triples on existing rows. The legacy `config` TEXT column is preserved.
// Idempotent via schema_migrations key.
func migrateHarnessSpecsTypedColumns(db *sql.DB) {
	addColumnIfNotExists(db, "harness_specs", "kind", "TEXT NOT NULL DEFAULT 'regex_match'")
	addColumnIfNotExists(db, "harness_specs", "target", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "harness_specs", "pattern", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "harness_specs", "pattern_flags", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "harness_specs", "command", "TEXT NOT NULL DEFAULT ''")
	if Driver == "postgres" {
		addColumnIfNotExists(db, "harness_specs", "timeout_sec", "BIGINT NOT NULL DEFAULT 120")
		addColumnIfNotExists(db, "harness_specs", "expected_exit_code", "BIGINT NOT NULL DEFAULT 0")
		addColumnIfNotExists(db, "harness_specs", "threshold_value", "DOUBLE PRECISION NOT NULL DEFAULT 0")
	} else {
		addColumnIfNotExists(db, "harness_specs", "timeout_sec", "INTEGER NOT NULL DEFAULT 120")
		addColumnIfNotExists(db, "harness_specs", "expected_exit_code", "INTEGER NOT NULL DEFAULT 0")
		addColumnIfNotExists(db, "harness_specs", "threshold_value", "REAL NOT NULL DEFAULT 0")
	}
	addColumnIfNotExists(db, "harness_specs", "extract_regex", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "harness_specs", "threshold_op", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "harness_specs", "file_paths", "TEXT NOT NULL DEFAULT '[]'")
	addColumnIfNotExists(db, "harness_specs", "trigger_on", "TEXT NOT NULL DEFAULT 'phase_exit'")

	// ai_judge kind: judge_prompt + judge_model columns
	addColumnIfNotExists(db, "harness_specs", "judge_prompt", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "harness_specs", "judge_model", "TEXT NOT NULL DEFAULT 'claude-haiku-4-5-20251001'")

	// cost_usd column on harness_checks (tracks per-check AI cost for ai_judge results)
	if Driver == "postgres" {
		addColumnIfNotExists(db, "harness_checks", "cost_usd", "DOUBLE PRECISION NOT NULL DEFAULT 0")
	} else {
		addColumnIfNotExists(db, "harness_checks", "cost_usd", "REAL NOT NULL DEFAULT 0")
	}

	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_harness_specs_kind ON harness_specs(kind)`); err != nil {
		slog.Warn("create idx_harness_specs_kind failed", "error", err)
	}

	w := Wrap(db)
	if migrationApplied(w, "harness_specs_typed_columns_backfill_v1") {
		return
	}

	type defaultRow struct {
		category, name, kind     string
		target, pattern, command string
		extractRegex             string
		thresholdValue           float64
		thresholdOp              string
	}
	defaults := []defaultRow{
		{
			category: "commit", name: "conventional-commits", kind: "regex_match",
			target:  "commit_message",
			pattern: `^(feat|fix|refactor|docs|test|chore|perf|ci)(\(.+\))?: .+`,
		},
		{
			category: "commit", name: "branch-name", kind: "regex_match",
			target:  "branch_name",
			pattern: `^(feat|fix|refactor|chore|docs|test|perf|ci|ws-\d+)/[a-z0-9][a-z0-9\-]*$`,
		},
		{
			category: "quality", name: "test-coverage", kind: "command_output_match",
			command:        "go test -cover ./...",
			extractRegex:   `coverage:\s+(\d+\.?\d*)%`,
			thresholdValue: 80,
			thresholdOp:    ">=",
		},
		{category: "quality", name: "linter", kind: "command_exit_code"},
		{
			category: "workflow", name: "output-pattern", kind: "regex_match",
			target: "agent_output",
		},
		{category: "workflow", name: "file-exists", kind: "file_exists"},
		{category: "workflow", name: "command-exit-code", kind: "command_exit_code"},
		{category: "workflow", name: "command-output", kind: "command_output_match"},
	}

	ctx := context.Background()
	for _, d := range defaults {
		// Restrict backfill to seeded global system rows (owner sentinel 'user'/0,
		// scope='global'). User-customised specs are never touched, even if they
		// happen to share (category, name) with a default. The kind='regex_match'
		// AND pattern='' gate additionally guards against re-running on a row
		// whose typed columns have already been hand-populated.
		if _, err := w.ExecContext(ctx, `
			UPDATE harness_specs
			SET kind = ?, target = ?, pattern = ?, command = ?,
			    extract_regex = ?, threshold_value = ?, threshold_op = ?
			WHERE category = ? AND name = ?
			  AND scope = 'global' AND owner_type = 'user' AND owner_id = 0
			  AND kind = 'regex_match' AND pattern = ''`,
			d.kind, d.target, d.pattern, d.command,
			d.extractRegex, d.thresholdValue, d.thresholdOp,
			d.category, d.name,
		); err != nil {
			slog.Warn("backfill harness_spec kind failed",
				"category", d.category, "name", d.name, "error", err)
		}
	}

	markMigration(w, "harness_specs_typed_columns_backfill_v1")
}

func migrateHarnessSpecsGlobalOnly(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "harness_specs_global_only_v1") {
		return
	}

	ctx := context.Background()
	if _, err := w.ExecContext(ctx, `
		DELETE FROM column_gate_specs
		WHERE spec_id IN (SELECT id FROM harness_specs WHERE scope <> 'global')
	`); err != nil {
		slog.Warn("delete project-scope harness gate links failed", "error", err)
		return
	}
	if _, err := w.ExecContext(ctx, `
		DELETE FROM harness_checks
		WHERE spec_id IN (SELECT id FROM harness_specs WHERE scope <> 'global')
	`); err != nil {
		slog.Warn("delete project-scope harness checks failed", "error", err)
		return
	}
	if _, err := w.ExecContext(ctx, `DELETE FROM harness_specs WHERE scope <> 'global'`); err != nil {
		slog.Warn("delete project-scope harness specs failed", "error", err)
		return
	}

	if Driver == "postgres" {
		migrateHarnessSpecsGlobalOnlyPostgres(db)
	} else {
		migrateHarnessSpecsGlobalOnlySQLite(db)
	}

	markMigration(w, "harness_specs_global_only_v1")
}

func migrateHarnessSpecsGlobalUnique(db *sql.DB) {
	w := Wrap(db)
	if migrationApplied(w, "harness_specs_global_unique_v1") {
		return
	}

	ctx := context.Background()
	duplicateIDs := `
		WITH ranked AS (
			SELECT id,
			       ROW_NUMBER() OVER (
			           PARTITION BY category, name
			           ORDER BY CASE WHEN owner_type = 'user' AND owner_id = 0 THEN 0 ELSE 1 END, id
			       ) AS rn
			FROM harness_specs
			WHERE scope = 'global'
		)
		SELECT id FROM ranked WHERE rn > 1
	`
	for _, stmt := range []string{
		`DELETE FROM column_gate_specs WHERE spec_id IN (` + duplicateIDs + `)`,
		`DELETE FROM harness_checks WHERE spec_id IN (` + duplicateIDs + `)`,
		`DELETE FROM harness_specs WHERE id IN (` + duplicateIDs + `)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_harness_specs_global_name_unique
			ON harness_specs(category, name)
			WHERE scope = 'global'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_harness_specs_owner_global_unique
			ON harness_specs(owner_type, owner_id, category, name)
			WHERE scope = 'global'`,
	} {
		if _, err := w.ExecContext(ctx, stmt); err != nil {
			slog.Warn("deduplicate global harness specs failed", "stmt", stmt, "error", err)
			return
		}
	}

	markMigration(w, "harness_specs_global_unique_v1")
}

func migrateHarnessSpecsGlobalOnlyPostgres(db *sql.DB) {
	if _, err := db.Exec(`ALTER TABLE harness_specs DROP CONSTRAINT IF EXISTS harness_specs_scope_check`); err != nil {
		slog.Warn("drop harness_specs old scope check failed", "error", err)
		return
	}
	if _, err := db.Exec(`ALTER TABLE harness_specs DROP CONSTRAINT IF EXISTS harness_specs_scope_global_check`); err != nil {
		slog.Warn("drop harness_specs global scope check failed", "error", err)
		return
	}
	if _, err := db.Exec(`ALTER TABLE harness_specs ADD CONSTRAINT harness_specs_scope_global_check CHECK (scope = 'global')`); err != nil {
		slog.Warn("add harness_specs global scope check failed", "error", err)
		return
	}
}

func migrateHarnessSpecsGlobalOnlySQLite(db *sql.DB) {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='harness_specs'`).Scan(&ddl); err != nil {
		slog.Warn("read harness_specs sqlite ddl failed", "error", err)
		return
	}
	if strings.Contains(ddl, "CHECK (scope = 'global')") {
		return
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("disable sqlite foreign keys for harness_specs rebuild failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("begin harness_specs rebuild failed", "error", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`CREATE TABLE harness_specs_new (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		scope               TEXT NOT NULL DEFAULT 'global' CHECK (scope = 'global'),
		project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
		category            TEXT NOT NULL CHECK (category IN ('commit', 'quality', 'workflow', 'agent')),
		name                TEXT NOT NULL,
		enabled             INTEGER NOT NULL DEFAULT 1,
		severity            TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('error', 'warning', 'info')),
		config              TEXT NOT NULL DEFAULT '{}',
		kind                TEXT    NOT NULL DEFAULT 'regex_match',
		target              TEXT    NOT NULL DEFAULT '',
		pattern             TEXT    NOT NULL DEFAULT '',
		pattern_flags       TEXT    NOT NULL DEFAULT '',
		command             TEXT    NOT NULL DEFAULT '',
		timeout_sec         INTEGER NOT NULL DEFAULT 120,
		expected_exit_code  INTEGER NOT NULL DEFAULT 0,
		extract_regex       TEXT    NOT NULL DEFAULT '',
		threshold_value     REAL    NOT NULL DEFAULT 0,
		threshold_op        TEXT    NOT NULL DEFAULT '',
		file_paths          TEXT    NOT NULL DEFAULT '[]',
		trigger_on          TEXT    NOT NULL DEFAULT 'phase_exit',
		judge_prompt        TEXT    NOT NULL DEFAULT '',
		judge_model         TEXT    NOT NULL DEFAULT 'claude-haiku-4-5-20251001',
		owner_type          TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
		owner_id            INTEGER NOT NULL DEFAULT 0,
		created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(scope, project_id, category, name)
	)`); err != nil {
		slog.Warn("create harness_specs_new failed", "error", err)
		return
	}

	if _, err := tx.Exec(`INSERT INTO harness_specs_new (
		id, scope, project_id, category, name, enabled, severity, config,
		kind, target, pattern, pattern_flags, command, timeout_sec,
		expected_exit_code, extract_regex, threshold_value, threshold_op,
		file_paths, trigger_on, judge_prompt, judge_model,
		owner_type, owner_id, created_at, updated_at
	)
	SELECT
		id, scope, project_id, category, name, enabled, severity, config,
		kind, target, pattern, pattern_flags, command, timeout_sec,
		expected_exit_code, extract_regex, threshold_value, threshold_op,
		file_paths, trigger_on, judge_prompt, judge_model,
		owner_type, owner_id, created_at, updated_at
	FROM harness_specs
	WHERE scope = 'global'`); err != nil {
		slog.Warn("copy harness_specs global rows failed", "error", err)
		return
	}

	for _, stmt := range []string{
		`DROP TABLE harness_specs`,
		`ALTER TABLE harness_specs_new RENAME TO harness_specs`,
		`CREATE INDEX IF NOT EXISTS idx_harness_specs_scope ON harness_specs(scope, project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_harness_specs_category ON harness_specs(category)`,
		`CREATE INDEX IF NOT EXISTS idx_harness_specs_kind ON harness_specs(kind)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_harness_specs_owner_scope_unique
			ON harness_specs(owner_type, owner_id, project_id, category, name)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			slog.Warn("rebuild harness_specs statement failed", "stmt", stmt, "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("commit harness_specs rebuild failed", "error", err)
	}
}

// expandDataSourcesKindCheckSQLite rebuilds data_sources with the expanded kind
// CHECK list. Idempotent: skips if the current DDL already mentions 'http'
// (the marker of the latest expansion; a DB that only has an older list — e.g.
// the 'mssql'- or 'trino'-era list — is rebuilt again to pick up the new kind).
func expandDataSourcesKindCheckSQLite(db *sql.DB) {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='data_sources'`).Scan(&ddl); err != nil {
		return // table not yet created; schema.sql installs the expanded CHECK on first run
	}
	if strings.Contains(ddl, "'http'") {
		return // already migrated
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		slog.Warn("expandDataSourcesKindCheckSQLite: disable FK failed", "error", err)
		return
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		slog.Warn("expandDataSourcesKindCheckSQLite: begin tx failed", "error", err)
		return
	}

	stmts := []string{
		`CREATE TABLE data_sources_new (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type          TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id            INTEGER NOT NULL DEFAULT 0,
			user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name                TEXT NOT NULL,
			kind                TEXT NOT NULL CHECK (kind IN (
				'mysql','postgres','clickhouse','mssql',
				'mariadb','tidb','oceanbase','starrocks','doris',
				'cockroachdb','greenplum','redshift','opengauss','polardbpg','yugabyte',
				'redis','mongo','trino','elasticsearch','http'
			)),
			config              TEXT NOT NULL DEFAULT '{}',
			scope_config        TEXT NOT NULL DEFAULT '{}',
			default_access_mode TEXT NOT NULL DEFAULT 'read' CHECK (default_access_mode IN ('read','readwrite')),
			require_confirm     TEXT NOT NULL DEFAULT 'writes_only' CHECK (require_confirm IN ('always','writes_only','never')),
			last_verified_at    TIMESTAMP DEFAULT NULL,
			created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, name)
		)`,
		`INSERT INTO data_sources_new
			SELECT id, owner_type, owner_id, user_id, name, kind, config, scope_config,
			       default_access_mode, require_confirm, last_verified_at, created_at, updated_at
			FROM data_sources`,
		`DROP TABLE data_sources`,
		`ALTER TABLE data_sources_new RENAME TO data_sources`,
		`CREATE INDEX IF NOT EXISTS idx_data_sources_owner ON data_sources(owner_type, owner_id)`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			preview := stmt
			if len(preview) > 40 {
				preview = preview[:40]
			}
			slog.Warn("expandDataSourcesKindCheckSQLite: step failed", "error", err, "stmt", preview)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("expandDataSourcesKindCheckSQLite: commit failed", "error", err)
	}
}

// expandDataSourcesKindCheckPostgres drops and re-adds the data_sources kind CHECK
// constraint. Idempotent: skips if the constraint already contains 'http'
// (the marker of the latest expansion; an older constraint — 'mssql'- or
// 'trino'-era — is re-expanded to pick up the new kind).
func expandDataSourcesKindCheckPostgres(db *sql.DB) {
	// Check table exists — scoped to the schemas on the search_path so a
	// data_sources in another schema (multi-schema deploys, test isolation)
	// can neither satisfy nor confuse this probe.
	var tableExists int
	if err := db.QueryRow(`SELECT 1 FROM information_schema.tables WHERE table_name = 'data_sources' AND table_schema = ANY (current_schemas(false))`).Scan(&tableExists); err != nil {
		return // table not yet created
	}

	// Find the kind constraint by matching 'clickhouse' — unique to the kind CHECK.
	var constraintName, clause string
	err := db.QueryRow(`
		SELECT cc.constraint_name, cc.check_clause
		FROM information_schema.check_constraints cc
		JOIN information_schema.constraint_column_usage ccu
		  ON cc.constraint_name = ccu.constraint_name
		 AND cc.constraint_schema = ccu.constraint_schema
		WHERE ccu.table_name = 'data_sources'
		  AND ccu.table_schema = ANY (current_schemas(false))
		  AND cc.check_clause LIKE '%clickhouse%'
		LIMIT 1`).Scan(&constraintName, &clause)
	if err != nil {
		slog.Warn("expandDataSourcesKindCheckPostgres: find constraint failed", "error", err)
		return
	}
	if strings.Contains(clause, "http") {
		return // already migrated
	}

	// Wrap DROP + ADD in a single transaction so a crash between the two
	// statements does not leave data_sources permanently without a kind CHECK.
	tx, err := db.Begin()
	if err != nil {
		slog.Warn("expandDataSourcesKindCheckPostgres: begin tx failed", "error", err)
		return
	}
	stmts := []string{
		fmt.Sprintf(`ALTER TABLE data_sources DROP CONSTRAINT IF EXISTS %q`, constraintName),
		`ALTER TABLE data_sources ADD CONSTRAINT data_sources_kind_check CHECK (kind IN (
			'mysql','postgres','clickhouse','mssql',
			'mariadb','tidb','oceanbase','starrocks','doris',
			'cockroachdb','greenplum','redshift','opengauss','polardbpg','yugabyte',
			'redis','mongo','trino','elasticsearch','http'
		))`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			preview := stmt
			if len(preview) > 40 {
				preview = preview[:40]
			}
			slog.Warn("expandDataSourcesKindCheckPostgres: step failed", "error", err, "stmt", preview)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("expandDataSourcesKindCheckPostgres: commit failed", "error", err)
	}
}
