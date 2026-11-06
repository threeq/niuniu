package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// The read-only guarantee is the whole point of the mailbox binding: a poll
// must not change what the user sees in their mail client. BODY.PEEK[] is the
// only body item that does not implicitly set \Seen.
func TestIMAPFetchOptionsUsePeek(t *testing.T) {
	opts := imapFetchOptions()
	if len(opts.BodySection) != 1 {
		t.Fatalf("body sections = %d, want 1", len(opts.BodySection))
	}
	section := opts.BodySection[0]
	if !section.Peek {
		t.Fatalf("body section does not use BODY.PEEK — fetching would mark mail as read")
	}
	if !opts.UID {
		t.Fatalf("UID was not requested; the cursor could not advance")
	}
	if section.Specifier != "" || len(section.Part) != 0 {
		t.Fatalf("body section = %+v, want the whole message", section)
	}
}

func TestIMAPSearchCriteriaSwitchOnUIDValidity(t *testing.T) {
	cursor := IMAPCursor{UIDValidity: 5, LastUID: 100, ResetSince: time.Now().Add(-24 * time.Hour)}

	// Same generation: fetch strictly after the cursor.
	same := imapFetchSearchCriteria(cursor, 5)
	if len(same.UID) != 1 || same.UID[0].String() != "101:*" {
		t.Fatalf("same-generation search = %+v, want UID 101:*", same)
	}
	if same.Since.IsZero() == false {
		t.Fatalf("same-generation search must not carry a SINCE window")
	}

	// New generation (UIDVALIDITY changed): the old UID means nothing.
	changed := imapFetchSearchCriteria(cursor, 9)
	if len(changed.UID) != 0 {
		t.Fatalf("reset search must not use UIDs: %+v", changed.UID)
	}
	if !changed.Since.Equal(cursor.ResetSince) {
		t.Fatalf("reset search SINCE = %v, want %v", changed.Since, cursor.ResetSince)
	}

	// First poll (no cursor at all) also uses the time window.
	first := imapFetchSearchCriteria(IMAPCursor{ResetSince: cursor.ResetSince}, 9)
	if !first.Since.Equal(cursor.ResetSince) {
		t.Fatalf("first search SINCE = %v, want %v", first.Since, cursor.ResetSince)
	}
}

func TestEmailNextBackoffDoublesAndCaps(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

	first := emailNextBackoff(store.EmailSourcePollState{}, now)
	if first != emailBackoffBase {
		t.Fatalf("first backoff = %v, want %v", first, emailBackoffBase)
	}

	// A previous window of 4m (updated_at = failure time, backoff_until = the
	// deadline) doubles to 8m.
	prev := store.EmailSourcePollState{
		LastError:    "boom",
		UpdatedAt:    now.Add(-4 * time.Minute),
		BackoffUntil: sql.NullTime{Time: now, Valid: true},
	}
	if got := emailNextBackoff(prev, now); got != 8*time.Minute {
		t.Fatalf("backoff = %v, want 8m", got)
	}

	// And the growth stops at 30 minutes.
	maxed := store.EmailSourcePollState{
		LastError:    "boom",
		UpdatedAt:    now.Add(-emailBackoffMax),
		BackoffUntil: sql.NullTime{Time: now, Valid: true},
	}
	if got := emailNextBackoff(maxed, now); got != emailBackoffMax {
		t.Fatalf("backoff = %v, want cap %v", got, emailBackoffMax)
	}
}

// A reply must resume the task its root created. The mapping lives on the
// issue's provenance row, so no separate thread table is needed.
func TestEmailResolverResumesBoundThread(t *testing.T) {
	ctx := context.Background()
	db := newEmailTestDB(t)
	q := store.New(db)

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "邮箱项目", OwnerType: "user", OwnerID: 1})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "待办", Position: 0})
	if err != nil {
		t.Fatalf("column: %v", err)
	}
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "原始任务", Position: 0})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	ref := eventRef("inbox", 3)
	extID := emailExternalID(ref, "root@example.com")
	if _, err := db.Exec(`UPDATE issues SET external_source = 'email', external_id = ? WHERE id = ?`, extID, issue.ID); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:    "ws", Path: "/tmp/ws-email", Status: "running", OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	resolver := emailResolver{db: store.Wrap(db)}
	res, err := resolver.Resolve(ctx, intake.Trigger{
		ProjectID: proj.ID,
		SourceRef: ref,
		Thread:    intake.ThreadRef{ThreadExtID: "root@example.com"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.IssueID != issue.ID || res.WorkspaceID != ws.ID || !res.Bound {
		t.Fatalf("resolution = %+v, want issue %d / workspace %d / bound", res, issue.ID, ws.ID)
	}

	// A different thread resolves to nothing, so it creates a new task.
	other, err := resolver.Resolve(ctx, intake.Trigger{
		ProjectID: proj.ID,
		SourceRef: ref,
		Thread:    intake.ThreadRef{ThreadExtID: "unrelated@example.com"},
	})
	if err != nil {
		t.Fatalf("resolve other: %v", err)
	}
	if other.IssueID != 0 || other.Bound {
		t.Fatalf("unrelated thread resolved to %+v", other)
	}
}

func newEmailTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	store.Migrate(db)
	return db
}
