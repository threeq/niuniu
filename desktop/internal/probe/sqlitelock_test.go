package probe

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

func TestDBBusy_FreeDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "niuniu.db")
	// Create an empty DB file, then close.
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE x(i INTEGER)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	busy, err := dbIsBusy(dbPath)
	require.NoError(t, err)
	require.False(t, busy, "closed DB should not be reported busy")
}

func TestDBBusy_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")
	busy, err := dbIsBusy(dbPath)
	require.NoError(t, err)
	require.False(t, busy, "nonexistent DB should not be reported busy")
}

func TestDBBusy_HeldByAnother(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "niuniu.db")

	// Hold an exclusive lock via a long-running write transaction.
	holder, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer holder.Close()
	_, err = holder.Exec("CREATE TABLE y(i INTEGER)")
	require.NoError(t, err)

	// BEGIN IMMEDIATE acquires RESERVED lock; BEGIN EXCLUSIVE upgrades to EXCLUSIVE.
	tx, err := holder.Begin()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO y(i) VALUES (1)")
	require.NoError(t, err)
	defer tx.Rollback()

	busy, err := dbIsBusy(dbPath)
	require.NoError(t, err)
	// Documentation: modernc.org/sqlite uses WAL or rollback-journal based on PRAGMA;
	// an open write transaction SHOULD prevent another connection from taking EXCLUSIVE.
	// If this test flakes with the modernc driver, try adjusting the holder to open
	// the DB with journal_mode=DELETE and see if exclusive-lock semantics change.
	require.True(t, busy, "DB should be reported busy while another tx holds write lock")
}
