package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/niuniu-dev/niuniu/internal/config"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var Schema string

//go:embed schema_postgres.sql
var SchemaPostgres string

// Driver holds the current storage driver ("sqlite" or "postgres").
// Set during Open() and used by migration functions.
//
// TEST-PARALLELISM WARNING: this is a package-level variable, mutated by
// test fixtures (SetupDB, SetupPGDB, NewIsolationEnv, ...) to switch the
// schema variant + migration path each fixture applies. Dual-driver tests
// using pgtest.ForEachDriver run their sqlite + postgres subtests
// serially, so the mutation is safe within one test. But if a DB-touching
// test in any package using these fixtures ever calls t.Parallel(), a
// concurrent fixture call in another goroutine will race this variable
// (and the corresponding ApplySchema dispatch). Don't add t.Parallel() to
// tests that build a DB via the testing fixtures.
var Driver = "sqlite"

// columnExists checks if a column exists in a table, works for both SQLite and PostgreSQL.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	var count int
	var err error
	if Driver == "postgres" {
		// Match the column in whatever schema the unqualified table name actually
		// resolves to via the connection's search_path — NOT a hard-coded 'public'.
		// pgtest isolates every test in its own CREATE SCHEMA + search_path schema,
		// and a real deployment may run under a non-public schema; hard-coding
		// 'public' made this existence check silently miss the column, so the caller
		// then ran ALTER TABLE ADD COLUMN against a column that already existed and
		// failed with SQLSTATE 42701. current_schemas(false) is exactly the set of
		// schemas an unqualified table name is resolved against.
		err = db.QueryRow(
			"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = ANY(current_schemas(false)) AND table_name = $1 AND column_name = $2",
			table, column,
		).Scan(&count)
	} else {
		err = db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('"+table+"') WHERE name = ?", column,
		).Scan(&count)
	}
	return count > 0, err
}

// execMigration executes a SQL statement with placeholder conversion for PostgreSQL.
func execMigration(db *sql.DB, query string, args ...any) (sql.Result, error) {
	if Driver == "postgres" {
		query = ConvertPlaceholders(query)
	}
	return db.Exec(query, args...)
}

// addLifecycleStatusColumn adds the lifecycle_status column to the issues table
// if it doesn't already exist. Safe to call on every startup.
func addLifecycleStatusColumn(db *sql.DB) error {
	exists, err := columnExists(db, "issues", "lifecycle_status")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE issues ADD COLUMN lifecycle_status TEXT NOT NULL DEFAULT 'created'")
	return err
}

// migrateAgentMessages adds missing columns to agent_messages.
func migrateAgentMessages(db *sql.DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"event_type", "ALTER TABLE agent_messages ADD COLUMN event_type TEXT NOT NULL DEFAULT 'text'"},
		{"tool_name", "ALTER TABLE agent_messages ADD COLUMN tool_name TEXT DEFAULT ''"},
		{"tool_input", "ALTER TABLE agent_messages ADD COLUMN tool_input TEXT DEFAULT ''"},
		{"tool_use_id", "ALTER TABLE agent_messages ADD COLUMN tool_use_id TEXT DEFAULT ''"},
		{"is_error", "ALTER TABLE agent_messages ADD COLUMN is_error INTEGER NOT NULL DEFAULT 0"},
		{"cost_usd", "ALTER TABLE agent_messages ADD COLUMN cost_usd REAL DEFAULT 0"},
		{"num_turns", "ALTER TABLE agent_messages ADD COLUMN num_turns INTEGER DEFAULT 0"},
		{"duration_ms", "ALTER TABLE agent_messages ADD COLUMN duration_ms INTEGER DEFAULT 0"},
		{"loop_task_id", "ALTER TABLE agent_messages ADD COLUMN loop_task_id INTEGER DEFAULT NULL"},
		{"input_tokens", "ALTER TABLE agent_messages ADD COLUMN input_tokens INTEGER DEFAULT 0"},
		{"output_tokens", "ALTER TABLE agent_messages ADD COLUMN output_tokens INTEGER DEFAULT 0"},
	}
	for _, c := range cols {
		exists, err := columnExists(db, "agent_messages", c.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrateIssueFields adds missing columns to the issues table.
func migrateIssueFields(db *sql.DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"assignee", "ALTER TABLE issues ADD COLUMN assignee TEXT DEFAULT ''"},
		{"start_date", "ALTER TABLE issues ADD COLUMN start_date TEXT DEFAULT ''"},
		{"due_date", "ALTER TABLE issues ADD COLUMN due_date TEXT DEFAULT ''"},
		{"estimate_type", "ALTER TABLE issues ADD COLUMN estimate_type TEXT DEFAULT ''"},
		{"estimate", "ALTER TABLE issues ADD COLUMN estimate REAL DEFAULT 0"},
		{"actual_time", "ALTER TABLE issues ADD COLUMN actual_time REAL DEFAULT 0"},
	}
	for _, c := range cols {
		exists, err := columnExists(db, "issues", c.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrateWorktrees adds the base_branch column and backfills it for existing records.
// For existing rows, base_branch is derived by stripping the "ws-{id}/" prefix from the branch field.
func migrateWorktrees(db *sql.DB) error {
	exists, err := columnExists(db, "worktrees", "base_branch")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE worktrees ADD COLUMN base_branch TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Backfill: extract base branch from "ws-{id}/{base}" pattern
	var backfillSQL string
	if Driver == "postgres" {
		backfillSQL = `
			UPDATE worktrees
			SET base_branch = CASE
				WHEN branch LIKE 'ws-%/%' THEN SUBSTRING(branch FROM POSITION('/' IN branch) + 1)
				ELSE branch
			END
			WHERE base_branch = ''`
	} else {
		backfillSQL = `
			UPDATE worktrees
			SET base_branch = CASE
				WHEN branch LIKE 'ws-%/%' THEN SUBSTR(branch, INSTR(branch, '/') + 1)
				ELSE branch
			END
			WHERE base_branch = ''`
	}
	_, err = db.Exec(backfillSQL)
	return err
}

// addColumnLifecycleMapping adds the lifecycle_mapping column to the columns table
// if it doesn't already exist.
func addColumnLifecycleMapping(db *sql.DB) error {
	exists, err := columnExists(db, "columns", "lifecycle_mapping")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE columns ADD COLUMN lifecycle_mapping TEXT NOT NULL DEFAULT ''")
	return err
}

// addColumnsExtensionFields adds reviewer_agent / phase_prompt / auto_advance to
// the columns table for the column-based Harness model. (executor_agent is
// retired — see migrate.go, which drops it from existing DBs.)
// Idempotent: skip if already present.
func addColumnsExtensionFields(db *sql.DB) error {
	mig := []struct {
		name string
		ddl  string
	}{
		{"reviewer_agent", "ALTER TABLE columns ADD COLUMN reviewer_agent TEXT"},
		{"phase_prompt", "ALTER TABLE columns ADD COLUMN phase_prompt TEXT"},
		{"auto_advance", "ALTER TABLE columns ADD COLUMN auto_advance INTEGER NOT NULL DEFAULT 0"},
	}
	for _, m := range mig {
		exists, err := columnExists(db, "columns", m.name)
		if err != nil {
			return fmt.Errorf("check columns.%s: %w", m.name, err)
		}
		if exists {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("add columns.%s: %w", m.name, err)
		}
	}
	return nil
}

// migrateColumnsToNewDefaults renames legacy column names and sets default lifecycle_mapping values.
func migrateColumnsToNewDefaults(db *sql.DB) error {
	// Rename "Todo" → "Spec·Plan" (only if target name doesn't already exist in that project)
	if _, err := db.Exec(`UPDATE columns SET name = 'Spec·Plan' WHERE name = 'Todo' AND project_id NOT IN (SELECT project_id FROM columns WHERE name = 'Spec·Plan')`); err != nil {
		return err
	}
	// Rename "In Progress" → "Implement" (only if target name doesn't already exist in that project)
	if _, err := db.Exec(`UPDATE columns SET name = 'Implement' WHERE name = 'In Progress' AND project_id NOT IN (SELECT project_id FROM columns WHERE name = 'Implement')`); err != nil {
		return err
	}
	// Set lifecycle_mapping by column name (only if currently empty)
	mappings := []struct {
		name    string
		mapping string
	}{
		{"Backlog", "created"},
		{"Spec·Plan", "spec,spec-review,plan,plan-review"},
		{"Implement", "implement"},
		{"Review", "implement-review"},
		{"Test", "test"},
		{"Done", "completed"},
	}
	for _, m := range mappings {
		if _, err := execMigration(db, `UPDATE columns SET lifecycle_mapping = ? WHERE name = ? AND lifecycle_mapping = ''`, m.mapping, m.name); err != nil {
			return err
		}
	}
	return nil
}

// migrateLifecycleStatusValues updates legacy lifecycle_status values in issues.
func migrateLifecycleStatusValues(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE issues SET lifecycle_status = 'implement' WHERE lifecycle_status = 'coding'`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE issues SET lifecycle_status = 'implement-review' WHERE lifecycle_status = 'code-review'`); err != nil {
		return err
	}
	return nil
}

// addTestColumnToExistingProjects inserts a "Test" column for projects that have "Done" but no "Test".
func addTestColumnToExistingProjects(db *sql.DB) error {
	// Find projects with "Done" column but no "Test" column
	rows, err := db.Query(`
		SELECT c.project_id, c.position
		FROM columns c
		WHERE c.name = 'Done'
		  AND c.project_id NOT IN (SELECT project_id FROM columns WHERE name = 'Test')
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type projectDone struct {
		projectID    int64
		donePosition int64
	}
	var projects []projectDone
	for rows.Next() {
		var pd projectDone
		if err := rows.Scan(&pd.projectID, &pd.donePosition); err != nil {
			return err
		}
		projects = append(projects, pd)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, pd := range projects {
		// Bump Done's position +1
		if _, err := execMigration(db, `UPDATE columns SET position = ? WHERE project_id = ? AND name = 'Done'`, pd.donePosition+1, pd.projectID); err != nil {
			return err
		}
		// Insert Test column at Done's old position
		if _, err := execMigration(db, `INSERT INTO columns (project_id, name, position, lifecycle_mapping) VALUES (?, 'Test', ?, 'test')`, pd.projectID, pd.donePosition); err != nil {
			return err
		}
	}
	return nil
}

// addIsTemporaryColumn adds the is_temporary column to the workspaces table
// if it doesn't already exist. Flags short-lived workspaces that should be
// hidden from the main listing.
func addIsTemporaryColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "is_temporary")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspaces ADD COLUMN is_temporary INTEGER NOT NULL DEFAULT 0")
	return err
}

// addIsArchivedColumns adds is_archived and archived_at columns to workspaces
// if they don't already exist.
func addIsArchivedColumns(db *sql.DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"is_archived", "ALTER TABLE workspaces ADD COLUMN is_archived INTEGER NOT NULL DEFAULT 0"},
		{"archived_at", "ALTER TABLE workspaces ADD COLUMN archived_at TIMESTAMP DEFAULT NULL"},
	}
	for _, c := range cols {
		exists, err := columnExists(db, "workspaces", c.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

// addWorkspaceIsStudioColumn adds the is_studio column to workspaces if it does
// not already exist (issue #232). Marks workspaces created via the studio
// "from local directory" flow so they get a preset git allowlist + IDE hint.
func addWorkspaceIsStudioColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "is_studio")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspaces ADD COLUMN is_studio INTEGER NOT NULL DEFAULT 0")
	return err
}

func addWorkspaceStrictMCPColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "strict_mcp_config")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspaces ADD COLUMN strict_mcp_config INTEGER NOT NULL DEFAULT 0")
	return err
}

// migrateScheduleDefaultMessage adds the default_message column to workspace_schedules.
func migrateScheduleDefaultMessage(db *sql.DB) error {
	exists, err := columnExists(db, "workspace_schedules", "default_message")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspace_schedules ADD COLUMN default_message TEXT NOT NULL DEFAULT ''")
	return err
}

// addProjectDefaultCliTypeColumn adds the default_cli_type column to projects
// so each project can set the agent CLI pre-selected at workspace creation and
// used verbatim for issue-auto-created workspaces. Default 'claude' preserves
// prior behavior. The CHECK constraint is enforced on fresh CREATE TABLE paths
// (schema.sql / schema_postgres.sql) only; legacy DBs ADD COLUMN without it and
// rely on service-layer validation (same pattern as workspaces.cli_type).
func addProjectDefaultCliTypeColumn(db *sql.DB) error {
	exists, err := columnExists(db, "projects", "default_cli_type")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE projects ADD COLUMN default_cli_type TEXT NOT NULL DEFAULT 'claude'")
	return err
}

// addProjectStatusColumn adds the status column to the projects table
// if it doesn't already exist. Values: "active" (default), "hidden".
func addProjectStatusColumn(db *sql.DB) error {
	exists, err := columnExists(db, "projects", "status")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE projects ADD COLUMN status TEXT NOT NULL DEFAULT 'active'")
	return err
}

// migrateProjectsColor adds the color column to projects (added 2026-05-12 for
// sidebar project-color UI). NULL means "no color assigned"; a non-NULL value
// is a color token name (e.g. "emerald", "amber") resolved by the frontend's
// project-color helper.
func migrateProjectsColor(db *sql.DB) error {
	exists, err := columnExists(db, "projects", "color")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE projects ADD COLUMN color TEXT DEFAULT NULL")
	return err
}

// addIssueExternalColumns adds the 5 external_* columns to the issues table for
// the external issue tracker integration (GitHub Issues / TAPD). Idempotent via
// columnExists. external_source = '' marks a pure niuniu issue (default for
// existing rows); only rows with external_source != '' participate in writeback.
func addIssueExternalColumns(db *sql.DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"external_source", `ALTER TABLE issues ADD COLUMN external_source TEXT DEFAULT ''`},
		{"external_id", `ALTER TABLE issues ADD COLUMN external_id TEXT DEFAULT ''`},
		{"external_url", `ALTER TABLE issues ADD COLUMN external_url TEXT DEFAULT ''`},
		{"external_snapshot_at", `ALTER TABLE issues ADD COLUMN external_snapshot_at TIMESTAMP DEFAULT NULL`},
		{"external_comments_snapshot", `ALTER TABLE issues ADD COLUMN external_comments_snapshot TEXT DEFAULT ''`},
		{"external_comments_snapshot_at", `ALTER TABLE issues ADD COLUMN external_comments_snapshot_at TIMESTAMP DEFAULT NULL`},
	}
	for _, c := range cols {
		exists, err := columnExists(db, "issues", c.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return fmt.Errorf("add %s to issues: %w", c.name, err)
		}
	}
	return nil
}

func Open(cfg *config.Config) (*sql.DB, error) {
	var db *sql.DB
	var err error

	Driver = cfg.Storage.Driver

	switch cfg.Storage.Driver {
	case "sqlite":
		// modernc.org/sqlite reads pragmas from the DSN as `_pragma=NAME(VALUE)`.
		// The mattn/CGO form (`_journal_mode=WAL&_busy_timeout=...`) is silently
		// IGNORED by this driver — it previously left the DB on the default
		// `delete` rollback journal with busy_timeout=0, synchronous=FULL and
		// foreign keys OFF. WAL + busy_timeout is the standard fix for read/write
		// "database is locked" contention: readers don't block the writer at the
		// file level, and a contended lock waits up to 5s instead of erroring.
		// synchronous=NORMAL is the safe, faster companion under WAL. foreign_keys
		// enforces the schema's ON DELETE actions (was a latent no-op). _txlock is
		// a modernc top-level param (not a pragma) so BEGIN uses BEGIN IMMEDIATE.
		mmapSize := cfg.Storage.SQLite.MmapSize
		if mmapSize < 0 {
			mmapSize = 0
		}
		db, err = sql.Open("sqlite", cfg.Storage.SQLite.Path+
			"?_pragma=journal_mode(WAL)"+
			"&_pragma=busy_timeout(5000)"+
			"&_pragma=synchronous(NORMAL)"+
			"&_pragma=foreign_keys(1)"+
			"&_pragma=cache_size(-51200)"+ // 50MB per connection
			"&_pragma=temp_store(memory)"+ // temp tables in RAM, avoids disk I/O
			fmt.Sprintf("&_pragma=mmap_size(%d)", mmapSize)+ // memory-mapped I/O for sequential reads
			"&_txlock=immediate")
		if err != nil {
			return nil, err
		}
		// Connection-pool size. SQLite still has exactly ONE writer, but with WAL
		// readers run concurrently with the writer, so a pool >1 lets read queries
		// proceed instead of serialising behind a write on a single shared conn.
		// Write safety does NOT depend on the pool size: _txlock=immediate makes
		// every db.BeginTx issue BEGIN IMMEDIATE, which takes SQLite's write lock
		// at BEGIN, so the check-then-act sections (StartRun, MoveIssueRunAware,
		// moveIssueLegacy, ...) serialise via the write lock — a contended writer
		// waits up to busy_timeout(5s) rather than racing. Default 8; set
		// storage.sqlite.max_open_conns=1 to restore strict single-connection mode.
		maxConns := cfg.Storage.SQLite.MaxOpenConns
		if maxConns <= 0 {
			maxConns = 8
		}
		db.SetMaxOpenConns(maxConns)
		db.SetConnMaxIdleTime(5 * time.Minute) // reclaim per-conn page caches when idle
	case "postgres":
		db, err = sql.Open("pgx", cfg.Storage.Postgres.DSN)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(5)
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", cfg.Storage.Driver)
	}

	if err := ApplySchema(db); err != nil {
		return nil, err
	}

	return db, nil
}

// ApplySchema runs the embedded schema (selected by Driver) plus all
// open-time column-add migrations against db. Exposed so tests can build
// an in-memory DB with the same shape as production startup.
//
// Callers must set store.Driver before invoking this function.
func ApplySchema(db *sql.DB) error {
	switch Driver {
	case "sqlite":
		if _, err := db.Exec(Schema); err != nil {
			return fmt.Errorf("failed to run schema: %w", err)
		}
	case "postgres":
		if _, err := db.Exec(SchemaPostgres); err != nil {
			return fmt.Errorf("failed to run postgres schema: %w", err)
		}
	default:
		return fmt.Errorf("unsupported storage driver: %s", Driver)
	}

	// Conditionally add lifecycle_status column to issues table if it doesn't exist.
	if err := addLifecycleStatusColumn(db); err != nil {
		return fmt.Errorf("failed to migrate issues table: %w", err)
	}

	// Add event_type, tool_name, tool_input columns to agent_messages if missing.
	if err := migrateAgentMessages(db); err != nil {
		return fmt.Errorf("failed to migrate agent_messages table: %w", err)
	}

	if err := migrateIssueFields(db); err != nil {
		return fmt.Errorf("failed to migrate issue fields: %w", err)
	}

	if err := migrateWorktrees(db); err != nil {
		return fmt.Errorf("failed to migrate worktrees table: %w", err)
	}

	if err := addColumnLifecycleMapping(db); err != nil {
		return fmt.Errorf("failed to add lifecycle_mapping to columns: %w", err)
	}

	if err := addColumnsExtensionFields(db); err != nil {
		return fmt.Errorf("failed to add columns extension fields: %w", err)
	}

	if err := migrateColumnsToNewDefaults(db); err != nil {
		return fmt.Errorf("failed to migrate columns to new defaults: %w", err)
	}

	if err := migrateLifecycleStatusValues(db); err != nil {
		return fmt.Errorf("failed to migrate lifecycle status values: %w", err)
	}

	if err := addTestColumnToExistingProjects(db); err != nil {
		return fmt.Errorf("failed to add Test column to existing projects: %w", err)
	}

	if err := migrateScheduleDefaultMessage(db); err != nil {
		return fmt.Errorf("failed to migrate schedule default_message: %w", err)
	}

	if err := addIsTemporaryColumn(db); err != nil {
		return fmt.Errorf("failed to add is_temporary to workspaces: %w", err)
	}

	if err := addProjectStatusColumn(db); err != nil {
		return fmt.Errorf("failed to add status to projects: %w", err)
	}

	if err := migrateProjectsColor(db); err != nil {
		return fmt.Errorf("failed to add color to projects: %w", err)
	}

	if err := addProjectDefaultCliTypeColumn(db); err != nil {
		return fmt.Errorf("failed to add default_cli_type to projects: %w", err)
	}

	if err := addIsArchivedColumns(db); err != nil {
		return fmt.Errorf("failed to add archive columns to workspaces: %w", err)
	}

	if err := addWorkspaceIsStudioColumn(db); err != nil {
		return fmt.Errorf("failed to add is_studio to workspaces: %w", err)
	}

	if err := addWorkspaceStrictMCPColumn(db); err != nil {
		return fmt.Errorf("failed to add strict_mcp_config to workspaces: %w", err)
	}

	if err := migrateScheduleTriggeredBy(db); err != nil {
		return fmt.Errorf("failed to add triggered_by to workspace_schedules: %w", err)
	}

	if err := migrateScheduleActionKind(db); err != nil {
		return fmt.Errorf("failed to add action_kind to workspace_schedules: %w", err)
	}

	if err := addWorkspaceClaudeAccountColumn(db); err != nil {
		return fmt.Errorf("failed to add claude_account_id to workspaces: %w", err)
	}

	if err := addWorkspaceCliTypeColumn(db); err != nil {
		return fmt.Errorf("failed to add cli_type to workspaces: %w", err)
	}

	if err := addWorkspaceCodexAccountColumn(db); err != nil {
		return fmt.Errorf("failed to add codex_account_id to workspaces: %w", err)
	}

	if err := addWorkspaceCodexSandboxColumns(db); err != nil {
		return fmt.Errorf("failed to add codex sandbox/approval columns to workspaces: %w", err)
	}

	if err := addWorkspaceLanguageColumn(db); err != nil {
		return fmt.Errorf("failed to add language to workspaces: %w", err)
	}

	if err := addIssueExternalColumns(db); err != nil {
		return fmt.Errorf("failed to add external columns to issues: %w", err)
	}

	if err := migrateUsersEmailColumn(db); err != nil {
		return fmt.Errorf("failed to add email to users: %w", err)
	}

	if err := migrateUsersAuthSecurityColumns(db); err != nil {
		return fmt.Errorf("failed to add auth-security columns to users: %w", err)
	}

	return nil
}

// migrateUsersEmailColumn adds the users.email column (per-user git
// authorship) at Open() time. It must exist BEFORE the pre-migration auth-user
// seed in cmd/niuniu, which runs ahead of store.Migrate: CreateUser RETURNs the
// email column, so on a legacy DB whose users table predates it the seed
// crashed with "no such column: email" and the server failed to start. Empty
// string = unset; the service layer falls back to "<username>@niuniu.local".
// Idempotent via columnExists.
func migrateUsersEmailColumn(db *sql.DB) error {
	exists, err := columnExists(db, "users", "email")
	if err != nil {
		return fmt.Errorf("check users.email: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add users.email: %w", err)
	}
	return nil
}

// migrateUsersAuthSecurityColumns adds the Phase A auth-security columns to the
// users table: locked_until, lockout_count, require_password_change,
// password_changed_at. Idempotent via columnExists.
func migrateUsersAuthSecurityColumns(db *sql.DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"locked_until", "ALTER TABLE users ADD COLUMN locked_until TIMESTAMP"},
		{"lockout_count", "ALTER TABLE users ADD COLUMN lockout_count INTEGER NOT NULL DEFAULT 0"},
		{"require_password_change", "ALTER TABLE users ADD COLUMN require_password_change INTEGER NOT NULL DEFAULT 0"},
		{"password_changed_at", "ALTER TABLE users ADD COLUMN password_changed_at TIMESTAMP"},
		{"mfa_enabled", "ALTER TABLE users ADD COLUMN mfa_enabled INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range cols {
		exists, err := columnExists(db, "users", c.name)
		if err != nil {
			return fmt.Errorf("check users.%s: %w", c.name, err)
		}
		if exists {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return fmt.Errorf("add users.%s: %w", c.name, err)
		}
	}
	return nil
}

// migrateScheduleTriggeredBy adds the triggered_by column to workspace_schedules.
// triggered_by is the user_id of the user that created the schedule; it is used
// by the scheduler to verify the user is still a member of the workspace owner's
// org before firing (spec §5.11). 0 means "unknown / no user".
func migrateScheduleTriggeredBy(db *sql.DB) error {
	exists, err := columnExists(db, "workspace_schedules", "triggered_by")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspace_schedules ADD COLUMN triggered_by INTEGER NOT NULL DEFAULT 0")
	return err
}

// migrateScheduleActionKind adds the action_kind column to workspace_schedules.
// 'agent_message' (default) preserves the legacy behavior of sending the queued
// or default message to the agent; 'autonomous_discovery' makes an idle trigger
// inject a self-directed "survey + write report" prompt (issue #243). No index.
func migrateScheduleActionKind(db *sql.DB) error {
	exists, err := columnExists(db, "workspace_schedules", "action_kind")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspace_schedules ADD COLUMN action_kind TEXT NOT NULL DEFAULT 'agent_message'")
	return err
}

// addWorkspaceClaudeAccountColumn adds the claude_account_id column to workspaces
// if it doesn't already exist. NULL = follow owner's active claude account.
// Driver-aware: SQLite uses INTEGER, PostgreSQL uses BIGINT.
func addWorkspaceClaudeAccountColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "claude_account_id")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	intType := "INTEGER"
	if Driver == "postgres" {
		intType = "BIGINT"
	}
	_, err = db.Exec(fmt.Sprintf(
		"ALTER TABLE workspaces ADD COLUMN claude_account_id %s REFERENCES claude_accounts(id) ON DELETE SET NULL",
		intType,
	))
	return err
}

// addWorkspaceCliTypeColumn adds the cli_type column to workspaces so each
// workspace can pick its agent CLI (claude or codex). Default 'claude' keeps
// every pre-existing row pointing at the same Claude path as before.
// The CHECK constraint is enforced by fresh CREATE TABLE paths
// (schema.sql / schema_postgres.sql); on an existing DB we just ADD COLUMN
// without the CHECK because SQLite cannot retrofit constraints. Service-layer
// validation is the safety net for legacy DBs.
func addWorkspaceCliTypeColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "cli_type")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec("ALTER TABLE workspaces ADD COLUMN cli_type TEXT NOT NULL DEFAULT 'claude'")
	return err
}

// addWorkspaceCodexAccountColumn adds the codex_account_id column to workspaces
// so codex workspaces can pin to a specific managed codex account. NULL means
// fall back to the user's global ~/.codex/ auth (back-compat for pre-M2.5 rows).
// Driver-aware (SQLite INTEGER vs PG BIGINT). FK to codex_accounts(id) with
// SET NULL on delete so deleting an account does not orphan the workspace row.
func addWorkspaceCodexAccountColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "codex_account_id")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	intType := "INTEGER"
	if Driver == "postgres" {
		intType = "BIGINT"
	}
	_, err = db.Exec(fmt.Sprintf(
		"ALTER TABLE workspaces ADD COLUMN codex_account_id %s REFERENCES codex_accounts(id) ON DELETE SET NULL",
		intType,
	))
	return err
}

// addWorkspaceCodexSandboxColumns adds the codex_sandbox_mode and
// codex_approval_policy columns so per-workspace approval/sandbox settings can
// override the default full Codex bypass. Existing codex rows that still carry
// the old defaults are moved to the new defaults so Git worktree metadata
// outside the workspace remains writable under niuniu's outer sandbox.
// CHECK constraint is enforced on fresh CREATE TABLE paths only; legacy DBs
// rely on service-layer validation (same pattern as cli_type).
func addWorkspaceCodexSandboxColumns(db *sql.DB) error {
	hasSandbox, err := columnExists(db, "workspaces", "codex_sandbox_mode")
	if err != nil {
		return err
	}
	if !hasSandbox {
		_, err = db.Exec("ALTER TABLE workspaces ADD COLUMN codex_sandbox_mode TEXT NOT NULL DEFAULT 'danger-full-access'")
		if err != nil {
			return err
		}
	}
	hasApproval, err := columnExists(db, "workspaces", "codex_approval_policy")
	if err != nil {
		return err
	}
	if !hasApproval {
		_, err = db.Exec("ALTER TABLE workspaces ADD COLUMN codex_approval_policy TEXT NOT NULL DEFAULT 'never'")
		if err != nil {
			return err
		}
	}
	if _, err = db.Exec("UPDATE workspaces SET codex_sandbox_mode = 'danger-full-access' WHERE cli_type = 'codex' AND codex_sandbox_mode = 'workspace-write'"); err != nil {
		return err
	}
	if _, err = db.Exec("UPDATE workspaces SET codex_approval_policy = 'never' WHERE cli_type = 'codex' AND codex_approval_policy = 'on-failure'"); err != nil {
		return err
	}
	return nil
}

// addWorkspaceLanguageColumn adds the language column to workspaces so the
// creating user's UI language can be persisted and inherited by epic-derived
// child workspaces (which are created async with no originating HTTP request).
// '' = unknown, yielding the generic "follow the user" directive.
func addWorkspaceLanguageColumn(db *sql.DB) error {
	exists, err := columnExists(db, "workspaces", "language")
	if err != nil {
		return err
	}
	if !exists {
		if _, err = db.Exec("ALTER TABLE workspaces ADD COLUMN language TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return nil
}

// NewQueries creates a Queries instance from a *sql.DB.
// For PostgreSQL, it wraps the DB with placeholder conversion (? → $1, $2, ...).
func NewQueries(db *sql.DB) *Queries {
	if Driver == "postgres" {
		return New(&PgDB{DB: db})
	}
	return New(db)
}
