package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The IMAP cursor table is created by the embedded schema (CREATE TABLE IF NOT
// EXISTS) and its index by Migrate. Running both twice must be a no-op — the
// migration is applied on every startup.
func TestEmailSourcePollStateSchema(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='email_source_poll_state'`).Scan(&n))
	require.Equal(t, 1, n)

	for _, col := range []string{"project_id", "binding_id", "mailbox", "uid_validity", "last_uid", "backoff_until", "last_error"} {
		ok, err := columnExists(db, "email_source_poll_state", col)
		require.NoError(t, err)
		require.True(t, ok, "column %s missing", col)
	}

	var idx int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_email_poll_project'`).Scan(&idx))
	require.Equal(t, 1, idx)
}
