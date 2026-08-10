package probe

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// dbIsBusy attempts to open the SQLite DB at path and take an exclusive
// write transaction. Returns true if another writer is holding the
// lock (SQLITE_BUSY), false if we can get exclusive access (and
// release immediately — we are only probing).
//
// If the file does not exist, returns false (no writer yet).
func dbIsBusy(path string) (bool, error) {
	// File-not-exist: treat as not busy (no writer yet).
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	// Open with a short busy_timeout so we don't hang.
	// Note: journal_mode=DELETE is used instead of WAL to ensure exclusive-lock
	// semantics are enforced; WAL mode allows concurrent readers and can mask
	// SQLITE_BUSY on BEGIN EXCLUSIVE in some driver configurations.
	dsn := path + "?_pragma=busy_timeout(200)&_pragma=journal_mode(DELETE)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetConnMaxLifetime(1 * time.Second)

	// Attempt to acquire an EXCLUSIVE lock directly.
	// We use Exec("BEGIN EXCLUSIVE") rather than db.Begin() because db.Begin()
	// issues "BEGIN" (deferred) which only escalates to EXCLUSIVE on first write,
	// whereas "BEGIN EXCLUSIVE" acquires the lock immediately — so SQLITE_BUSY is
	// returned right away if another writer holds the file.
	_, err = db.Exec("BEGIN EXCLUSIVE")
	if err != nil {
		if isBusyErr(err) {
			return true, nil
		}
		return false, err
	}
	// Release immediately — we were only probing.
	_, _ = db.Exec("ROLLBACK")
	return false, nil
}

// isBusyErr detects SQLITE_BUSY / locked errors by substring match.
// More reliable than errors.Is against a specific sentinel because the
// modernc.org/sqlite driver doesn't export a clean error constant.
func isBusyErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "busy") || strings.Contains(s, "locked")
}
