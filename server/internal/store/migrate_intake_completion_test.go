package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIntakeCompletionSchemaFreshAndRepeatable applies the current schema (fresh
// DB) and runs Migrate twice: every intake/completion table, column and index
// must exist, and re-running the migration must be a no-op.
func TestIntakeCompletionSchemaFreshAndRepeatable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)

	Migrate(db)
	Migrate(db)

	for _, tbl := range []string{
		"intake_settings", "intake_rules", "intake_policy_versions",
		"intake_events", "intake_decisions", "task_briefs",
		"completion_settings", "completion_profiles",
	} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		require.Equal(t, 1, n, "table %s missing", tbl)
	}

	for _, c := range []struct{ table, column string }{
		{"issue_checklists", "kind"},
		{"issue_checklists", "brief_id"},
		{"issues", "completion_profile_id"},
	} {
		ok, err := columnExists(db, c.table, c.column)
		require.NoError(t, err)
		require.True(t, ok, "column %s.%s missing", c.table, c.column)
	}

	for _, idx := range []string{
		"idx_intake_events_project",
		"idx_intake_decisions_pending",
		"idx_task_briefs_issue",
		"idx_issue_checklists_brief",
		"idx_issues_completion_profile",
	} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n))
		require.Equal(t, 1, n, "index %s missing", idx)
	}

	// The Execution Timeline must admit intake events on a fresh DB.
	_, err := db.Exec(`INSERT INTO projects (id, name) VALUES (1, 'p')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO columns (id, project_id, name) VALUES (1, 1, 'todo')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO issues (id, column_id, title) VALUES (1, 1, 'task')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO issue_exec_events (issue_id, kind, summary) VALUES (1, 'intake', 'accepted')`)
	require.NoError(t, err, "issue_exec_events.kind must admit 'intake'")
}

// TestIntakeExecEventKindMigration builds an old-shape issue_exec_events table
// (kind CHECK without 'intake'), then widens it. Existing rows and the index
// must survive and the new value must be accepted; a second run is a no-op.
func TestIntakeExecEventKindMigration(t *testing.T) {
	Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE issue_exec_events (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER NOT NULL,
			workspace_id INTEGER,
			kind         TEXT NOT NULL CHECK (kind IN ('advance','gate','ask_user','terminal','cost','intervention')),
			summary      TEXT NOT NULL,
			detail_json  TEXT,
			cost_usd     REAL NOT NULL DEFAULT 0,
			created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX idx_issue_exec_events_issue ON issue_exec_events(issue_id, created_at)`,
		`INSERT INTO issue_exec_events (issue_id, kind, summary) VALUES (7, 'advance', 'legacy')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}

	migrateIntakeExecEventKind(db)
	migrateIntakeExecEventKind(db) // repeatable

	var summary string
	require.NoError(t, db.QueryRow(
		`SELECT summary FROM issue_exec_events WHERE issue_id = 7 AND kind = 'advance'`).Scan(&summary))
	require.Equal(t, "legacy", summary)

	_, err = db.Exec(`INSERT INTO issue_exec_events (issue_id, kind, summary) VALUES (7, 'intake', 'new')`)
	require.NoError(t, err, "widened CHECK must admit 'intake'")

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_issue_exec_events_issue'`).Scan(&n))
	require.Equal(t, 1, n, "index must survive the table rebuild")
}

// TestMigrateIMBotIntakeMode covers the §7.1 semantics migration:
//
//	agent_mode='command' → intake_mode='off'
//	agent_mode='observe' → intake_mode='auto'
//
// The backfill runs once, only when the column is first added. A value the user
// chose afterwards must survive a restart, and chats created later get the
// schema default 'observe' (dry-run), not the legacy mapping.
func TestMigrateIMBotIntakeMode(t *testing.T) {
	Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE im_bot_chats (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_mode TEXT NOT NULL DEFAULT 'command')`)
	require.NoError(t, err)
	for _, mode := range []string{"command", "observe", ""} {
		_, err = db.Exec(`INSERT INTO im_bot_chats (agent_mode) VALUES (?)`, mode)
		require.NoError(t, err)
	}

	migrateIMBotIntakeMode(db)

	got := map[int64]string{}
	rows, err := db.Query(`SELECT id, intake_mode FROM im_bot_chats ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		var mode string
		require.NoError(t, rows.Scan(&id, &mode))
		got[id] = mode
	}
	require.NoError(t, rows.Close())
	require.Equal(t, "off", got[1], "command must migrate to off")
	require.Equal(t, "auto", got[2], "observe must migrate to auto")
	require.Equal(t, "off", got[3], "an empty legacy agent_mode degrades to command → off")

	// A chat created after the migration defaults to observe (dry-run).
	res, err := db.Exec(`INSERT INTO im_bot_chats (agent_mode) VALUES ('observe')`)
	require.NoError(t, err)
	newID, err := res.LastInsertId()
	require.NoError(t, err)
	var newMode string
	require.NoError(t, db.QueryRow(`SELECT intake_mode FROM im_bot_chats WHERE id = ?`, newID).Scan(&newMode))
	require.Equal(t, "observe", newMode, "a new chat must default to observe, not the legacy mapping")

	// A user-chosen mode must not be overwritten by a later startup.
	_, err = db.Exec(`UPDATE im_bot_chats SET intake_mode = 'auto' WHERE id = 1`)
	require.NoError(t, err)
	migrateIMBotIntakeMode(db)
	var kept string
	require.NoError(t, db.QueryRow(`SELECT intake_mode FROM im_bot_chats WHERE id = 1`).Scan(&kept))
	require.Equal(t, "auto", kept, "the backfill must run only once")
}

// TestIntakeCompletionMigration_KeepsLegacyRows builds a pre-feature checklist
// and issue table, then runs the retrofit migration directly. Legacy rows must
// survive untouched and read back as kind='task' / NULL brief / NULL profile,
// i.e. exactly the pre-existing behavior. Running it twice must be idempotent.
func TestIntakeCompletionMigration_KeepsLegacyRows(t *testing.T) {
	Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE issues (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL)`,
		`CREATE TABLE issue_checklists (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			is_completed INTEGER NOT NULL DEFAULT 0,
			position INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE task_briefs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			UNIQUE(issue_id, version))`,
		`CREATE TABLE completion_profiles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			name TEXT NOT NULL)`,
		`INSERT INTO issues (id, title) VALUES (1, 'legacy issue')`,
		`INSERT INTO issue_checklists (issue_id, title, is_completed, position) VALUES
			(1, 'manual one', 1, 0), (1, 'manual two', 0, 1)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}

	migrateIntakeCompletion(db)
	migrateIntakeCompletion(db) // repeatable

	var kind string
	var briefID sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT kind, brief_id FROM issue_checklists WHERE issue_id = 1 ORDER BY position LIMIT 1`).
		Scan(&kind, &briefID))
	require.Equal(t, "task", kind)
	require.False(t, briefID.Valid)

	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM issue_checklists WHERE issue_id = 1`).Scan(&n))
	require.Equal(t, 2, n, "migration must not drop or duplicate checklist rows")

	var completed int
	require.NoError(t, db.QueryRow(
		`SELECT is_completed FROM issue_checklists WHERE issue_id = 1 AND title = 'manual one'`).
		Scan(&completed))
	require.Equal(t, 1, completed)

	var profileID sql.NullInt64
	require.NoError(t, db.QueryRow(`SELECT completion_profile_id FROM issues WHERE id = 1`).Scan(&profileID))
	require.False(t, profileID.Valid)
}

// TestIntakeCompletionQueries exercises the generated queries once end-to-end
// against SQLite: idempotent events, settings upsert, append-only brief
// versions, acceptance materialization and checklist stats.
func TestIntakeCompletionQueries(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	q := Wrap(db).Queries()
	ctx := context.Background()

	_, err := db.Exec(`INSERT INTO projects (id, name) VALUES (1, 'p')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO columns (id, project_id, name) VALUES (1, 1, 'todo')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO issues (id, column_id, title) VALUES (1, 1, 'task')`)
	require.NoError(t, err)

	// Settings upsert: one row per (project, source), mode updated in place.
	_, err = q.UpsertIntakeSetting(ctx, UpsertIntakeSettingParams{
		ProjectID: 1, SourceKind: "im_bot", Enabled: 1, Mode: "observe", DailyCap: 4,
		AllowListJson: "[]", BotListJson: "[]",
	})
	require.NoError(t, err)
	got, err := q.UpsertIntakeSetting(ctx, UpsertIntakeSettingParams{
		ProjectID: 1, SourceKind: "im_bot", Enabled: 1, Mode: "auto", DailyCap: 6,
		AllowListJson: "[]", BotListJson: "[]",
	})
	require.NoError(t, err)
	require.Equal(t, "auto", got.Mode)
	require.EqualValues(t, 6, got.DailyCap)

	// Event dedupe: second delivery returns ErrNoRows, lookup finds the first.
	ev, err := q.CreateIntakeEvent(ctx, CreateIntakeEventParams{
		ProjectID: 1, SourceKind: "im_bot", SourceRef: "chat:1", EventExtID: "e1",
		EventKind: "message", ActorExtID: "u1", ActorName: "Ann", ThreadExtID: "t1",
		PayloadJson: "{}",
	})
	require.NoError(t, err)
	_, err = q.CreateIntakeEvent(ctx, CreateIntakeEventParams{
		ProjectID: 1, SourceKind: "im_bot", SourceRef: "chat:1", EventExtID: "e1",
		EventKind: "message", ActorExtID: "u1", ActorName: "Ann", ThreadExtID: "t1",
		PayloadJson: "{}",
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
	again, err := q.GetIntakeEventByKey(ctx, GetIntakeEventByKeyParams{
		SourceKind: "im_bot", SourceRef: "chat:1", EventExtID: "e1",
	})
	require.NoError(t, err)
	require.Equal(t, ev.ID, again.ID)

	// Policy versions increment per (project, source).
	v1, err := q.CreateIntakePolicyVersion(ctx, CreateIntakePolicyVersionParams{
		ProjectID: 1, SourceKind: "im_bot", ProjectID_2: 1, SourceKind_2: "im_bot",
		SnapshotJson: "{}",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, v1.Version)
	v2, err := q.CreateIntakePolicyVersion(ctx, CreateIntakePolicyVersionParams{
		ProjectID: 1, SourceKind: "im_bot", ProjectID_2: 1, SourceKind_2: "im_bot",
		SnapshotJson: "{}",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, v2.Version)

	// Dry-run decision appears in the pending queue; accepting it records the
	// resulting issue and removes it from the queue.
	d, err := q.CreateIntakeDecision(ctx, CreateIntakeDecisionParams{
		EventID: ev.ID, ProjectID: 1, ModeAtDecision: "observe", Strength: "explicit",
		Decision: "accept", Executed: 0, MatchedRuleIdsJson: "[]",
		WouldDoJson: `{"title":"x"}`,
	})
	require.NoError(t, err)
	pending, err := q.ListPendingIntakeDecisions(ctx, ListPendingIntakeDecisionsParams{
		ProjectID: 1, Decision: "accept",
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	executed, err := q.SetIntakeDecisionExecuted(ctx, SetIntakeDecisionExecutedParams{
		IssueID: sql.NullInt64{Int64: 1, Valid: true}, WorkspaceID: sql.NullInt64{}, ID: d.ID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, executed.Executed)
	pending, err = q.ListPendingIntakeDecisions(ctx, ListPendingIntakeDecisionsParams{
		ProjectID: 1, Decision: "accept",
	})
	require.NoError(t, err)
	require.Empty(t, pending)

	// Brief versions are append-only; latest is MAX(version).
	b1, err := q.CreateTaskBrief(ctx, CreateTaskBriefParams{
		IssueID: 1, IssueID_2: 1, Goal: "g1", AcceptanceJson: "[]",
		ScopeInJson: "[]", ScopeOutJson: "[]", DeliveryJson: "{}",
		ProvenanceJson: "{}", IntakeDecisionID: sql.NullInt64{Int64: d.ID, Valid: true},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, b1.Version)
	b2, err := q.CreateTaskBrief(ctx, CreateTaskBriefParams{
		IssueID: 1, IssueID_2: 1, Goal: "g2", AcceptanceJson: "[]",
		ScopeInJson: "[]", ScopeOutJson: "[]", DeliveryJson: "{}",
		ProvenanceJson: "{}",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, b2.Version)
	latest, err := q.GetLatestTaskBrief(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "g2", latest.Goal)
	require.Len(t, mustListBriefs(t, q), 2)

	// Acceptance materialization touches only kind='acceptance' rows.
	_, err = q.CreateChecklist(ctx, CreateChecklistParams{IssueID: 1, Title: "manual", IssueID_2: 1})
	require.NoError(t, err)
	acc, err := q.CreateChecklistWithKind(ctx, CreateChecklistWithKindParams{
		IssueID: 1, Title: "acc-1", IssueID_2: 1, Kind: "acceptance",
		BriefID: sql.NullInt64{Int64: b2.ID, Valid: true},
	})
	require.NoError(t, err)
	require.Equal(t, "acceptance", acc.Kind)
	require.NoError(t, q.DeleteAcceptanceChecklistsForIssue(ctx, 1))
	tasks, err := q.ListChecklistsByIssueAndKind(ctx, ListChecklistsByIssueAndKindParams{IssueID: 1, Kind: "task"})
	require.NoError(t, err)
	require.Len(t, tasks, 1, "manual task row must survive acceptance cleanup")
	require.Equal(t, "manual", tasks[0].Title)

	// Completion settings/profile defaults.
	cs, err := q.UpsertCompletionSettings(ctx, UpsertCompletionSettingsParams{
		ProjectID: 1, AutohostEnabled: 1, MaxRounds: 20, TokenBudget: 0,
		StopOnDecision: 1, RequireReview: 0,
	})
	require.NoError(t, err)
	require.EqualValues(t, 20, cs.MaxRounds)
	prof, err := q.CreateCompletionProfile(ctx, CreateCompletionProfileParams{
		ProjectID: 1, Name: "standard", AutohostEnabled: 1, MaxRounds: 12,
		TokenBudget: 0, StopOnDecision: 1, RequireReview: 0,
	})
	require.NoError(t, err)
	_, err = q.SetIssueCompletionProfile(ctx, SetIssueCompletionProfileParams{
		CompletionProfileID: sql.NullInt64{Int64: prof.ID, Valid: true}, ID: 1,
	})
	require.NoError(t, err)
	n, err := q.CountIssuesByCompletionProfile(ctx, sql.NullInt64{Int64: prof.ID, Valid: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func mustListBriefs(t *testing.T, q *Queries) []TaskBrief {
	t.Helper()
	briefs, err := q.ListTaskBriefs(context.Background(), 1)
	require.NoError(t, err)
	return briefs
}
