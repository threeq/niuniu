package service_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupRunServiceForLifecycleSync creates an in-memory DB + RunService wired
// with an event bus for lifecycle-sync tests.
func setupRunServiceForLifecycleSync(t *testing.T) (*service.RunService, *sql.DB, *event.Bus) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	q := store.New(db)
	bus := event.NewBus()
	svc := service.NewRunService(db, q, bus)
	return svc, db, bus
}

// seedColumnWithLifecycleMapping inserts a column with a lifecycle_mapping and
// returns the column ID. The column is inserted into the given project.
func seedColumnWithLifecycleMapping(t *testing.T, db *sql.DB, projectID int64, name, lifecycleMapping string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO columns (project_id, name, position, lifecycle_mapping) VALUES (?, ?, 0, ?)`,
		projectID, name, lifecycleMapping)
	require.NoError(t, err, "insert column with lifecycle_mapping")
	id, _ := res.LastInsertId()
	return id
}

// TestRunService_MoveIssueRunAware_DragToDoneColumn_FlipsLifecycleStatus
// verifies that when MoveIssueRunAware is used on the bare path (no active Run),
// dragging an Issue to a column with lifecycle_mapping='completed' reverse-syncs
// Issue.lifecycle_status to 'completed'.
func TestRunService_MoveIssueRunAware_DragToDoneColumn_FlipsLifecycleStatus(t *testing.T) {
	svc, db, _ := setupRunServiceForLifecycleSync(t)
	ctx := context.Background()

	uid := kanbanCreateUser(t, db, "lsync-bare-u")
	pid := kanbanCreateProject(t, db, "user", uid, "lsync-bare-p")

	// Source column has no lifecycle mapping.
	srcColID := kanbanCreateColumn(t, db, pid, "in-progress")

	// Done column maps to 'completed' lifecycle.
	doneColID := seedColumnWithLifecycleMapping(t, db, pid, "Done", "completed")

	// Issue starts in src column with lifecycle_status='implement' (default on create is 'created').
	issueID := kanbanCreateIssue(t, db, srcColID, "drag-to-done-task")

	// Verify initial lifecycle_status.
	var initialStatus string
	err := db.QueryRow(`SELECT lifecycle_status FROM issues WHERE id = ?`, issueID).Scan(&initialStatus)
	require.NoError(t, err)
	assert.Equal(t, "created", initialStatus)

	// Drag to Done column — bare path (no Run).
	_, err = svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID:        issueID,
		TargetColumnID: doneColID,
		TargetPosition: 0,
		Source:         service.MoveSourceDrag,
	})
	require.NoError(t, err)

	// lifecycle_status must now be 'completed'.
	var status string
	err = db.QueryRow(`SELECT lifecycle_status FROM issues WHERE id = ?`, issueID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "completed", status, "lifecycle_status should be reverse-synced to 'completed' on drag-to-Done")

	// column_id must be updated too.
	var colID int64
	err = db.QueryRow(`SELECT column_id FROM issues WHERE id = ?`, issueID).Scan(&colID)
	require.NoError(t, err)
	assert.Equal(t, doneColID, colID)
}
