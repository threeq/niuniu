package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// The upgrade path for the review-comment anchor + resolved columns (#683 wave 1).
//
// The trap this guards (learned the hard way on #679): `CREATE TABLE IF NOT
// EXISTS` is a NO-OP on a database that already has the table, so a column added
// only in schema.sql never reaches an existing install. Every new column needs
// its own addColumnIfNotExists, and the defaults it lands with must make legacy
// rows read back sensibly rather than NULL-panic a Scan.
func TestMigrate_CommentsAnchorAndResolvedColumns(t *testing.T) {
	db := openSchemaSeededDB(t)

	// Simulate a pre-#683 database: drop the seven new columns so the table has
	// exactly the legacy shape (id..created_at, with repo from the earlier
	// migration). SQLite 3.35+ supports DROP COLUMN and none of these are indexed.
	for _, col := range []string{
		"side", "commit_sha", "blob_sha", "context_lines",
		"resolved", "resolved_at", "resolved_by",
	} {
		if _, err := db.Exec(`ALTER TABLE comments DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s to build legacy shape: %v", col, err)
		}
	}

	// Seed rows the way a legacy install has them: a plain line comment and one
	// already delivered to the agent.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id)
		VALUES (1, 'ws', '/ws', 'user', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO comments (id, workspace_id, repo, file_path, line_number, content, sent_to_agent) VALUES
			(10, 1, 'niuniu', 'a.go', 42, 'fix this', FALSE),
			(11, 1, 'niuniu', 'b.go', 7,  'and this', TRUE)`); err != nil {
		t.Fatal(err)
	}

	Migrate(db)

	// Legacy rows must read back with usable defaults through the sqlc scan path
	// (which scans every column, including the seven just added) — not NULL.
	q := New(db)
	rows, err := q.ListCommentsByWorkspace(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListCommentsByWorkspace after migrate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 comments, got %d", len(rows))
	}
	for _, c := range rows {
		if c.Side != "new" {
			t.Errorf("comment %d side: want %q (legacy comments anchored new-side), got %q", c.ID, "new", c.Side)
		}
		if c.CommitSha != "" || c.BlobSha != "" || c.ContextLines != "" {
			t.Errorf("comment %d: want empty anchors on a legacy row, got commit=%q blob=%q ctx=%q",
				c.ID, c.CommitSha, c.BlobSha, c.ContextLines)
		}
		if c.Resolved {
			t.Errorf("comment %d: legacy rows carry no verdict, want resolved=false", c.ID)
		}
		if c.ResolvedAt.Valid {
			t.Errorf("comment %d: want NULL resolved_at, got %v", c.ID, c.ResolvedAt.Time)
		}
		if c.ResolvedBy != "" {
			t.Errorf("comment %d: want empty resolved_by, got %q", c.ID, c.ResolvedBy)
		}
	}

	// The delivered row keeps its delivery flag: the migration must not conflate
	// sent_to_agent with the new verdict.
	if got := rows[1]; !got.SentToAgent.Valid || !got.SentToAgent.Bool {
		t.Errorf("comment 11: sent_to_agent must survive the migration, got %v", got.SentToAgent)
	}

	// Repeatable: a second pass is a no-op and preserves written values.
	if _, err := db.Exec(`UPDATE comments SET resolved = TRUE, side = 'old' WHERE id = 10`); err != nil {
		t.Fatal(err)
	}
	Migrate(db)
	var resolved bool
	var side string
	if err := db.QueryRow(`SELECT resolved, side FROM comments WHERE id = 10`).Scan(&resolved, &side); err != nil {
		t.Fatalf("re-read after second Migrate: %v", err)
	}
	if !resolved || side != "old" {
		t.Errorf("second Migrate clobbered data: resolved=%v side=%q", resolved, side)
	}
}

// A fresh database (schema.sql only, no legacy shape) must produce the same
// columns the migration adds — otherwise fresh and upgraded installs diverge and
// a query that works on one breaks on the other.
func TestSchema_CommentsHasAnchorAndResolvedColumns(t *testing.T) {
	db := openSchemaSeededDB(t)

	rows, err := db.Query(`SELECT name FROM pragma_table_info('comments')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		have[name] = true
	}
	for _, col := range []string{
		"side", "commit_sha", "blob_sha", "context_lines",
		"resolved", "resolved_at", "resolved_by",
	} {
		if !have[col] {
			t.Errorf("fresh schema.sql is missing comments.%s", col)
		}
	}
}

// resolved and sent_to_agent are ORTHOGONAL: delivery is not a verdict. This is
// the store-level half of that guarantee — writing either one must leave the
// other exactly as it was.
func TestComments_ResolvedAndSentToAgentAreIndependent(t *testing.T) {
	db := openSchemaSeededDB(t)
	Migrate(db)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id)
		VALUES (1, 'ws', '/ws', 'user', 1)`); err != nil {
		t.Fatal(err)
	}
	q := New(db)
	ctx := t.Context()

	c, err := q.CreateComment(ctx, CreateCommentParams{
		WorkspaceID: 1, Repo: "niuniu", FilePath: "a.go",
		LineNumber: sql.NullInt64{Int64: 42, Valid: true},
		Content:    "fix this", Side: "new",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Delivering must NOT resolve — the bug this whole split exists to kill.
	if err := q.MarkCommentSent(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetComment(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SentToAgent.Valid || !got.SentToAgent.Bool {
		t.Error("MarkCommentSent did not set sent_to_agent")
	}
	if got.Resolved {
		t.Error("delivery marked the comment resolved: sent_to_agent must not imply a verdict")
	}

	// Resolving must not disturb delivery state.
	got, err = q.ResolveComment(ctx, ResolveCommentParams{ResolvedBy: "alice", ID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Resolved || got.ResolvedBy != "alice" || !got.ResolvedAt.Valid {
		t.Errorf("ResolveComment: want resolved by alice with a timestamp, got resolved=%v by=%q at=%v",
			got.Resolved, got.ResolvedBy, got.ResolvedAt)
	}
	if !got.SentToAgent.Valid || !got.SentToAgent.Bool {
		t.Error("resolving cleared sent_to_agent")
	}

	// Reopening clears the audit fields so a stale reviewer/timestamp never
	// outlives the verdict it belonged to — and still leaves delivery alone.
	got, err = q.UnresolveComment(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolved || got.ResolvedBy != "" || got.ResolvedAt.Valid {
		t.Errorf("UnresolveComment left residue: resolved=%v by=%q at=%v",
			got.Resolved, got.ResolvedBy, got.ResolvedAt)
	}
	if !got.SentToAgent.Valid || !got.SentToAgent.Bool {
		t.Error("reopening cleared sent_to_agent")
	}
}
