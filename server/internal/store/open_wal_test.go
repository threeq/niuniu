package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// TestOpenSQLiteEnablesWAL guards against regressing the SQLite DSN back to the
// mattn/CGO pragma syntax (`_journal_mode=WAL&...`), which modernc.org/sqlite
// silently ignores — leaving the DB on the default rollback journal with
// busy_timeout=0 and foreign keys OFF. modernc only honors `_pragma=NAME(VALUE)`.
func TestOpenSQLiteEnablesWAL(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Driver = "sqlite"
	cfg.Storage.SQLite.Path = filepath.Join(t.TempDir(), "wal_probe.db")

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	want := map[string]string{
		"journal_mode": "wal",
		"busy_timeout": "5000",
		"foreign_keys": "1",
		"synchronous":  "1", // NORMAL
	}
	for pragma, exp := range want {
		var got string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != exp {
			t.Errorf("PRAGMA %s = %q, want %q (DSN pragmas not applied — likely reverted to mattn-style syntax)", pragma, got, exp)
		}
	}
}

// TestOpenSQLiteDefaultPoolSizeIsEight pins the default pool to 8 and verifies
// storage.sqlite.max_open_conns overrides it.
func TestOpenSQLiteDefaultPoolSizeIsEight(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Driver = "sqlite"
	cfg.Storage.SQLite.Path = filepath.Join(t.TempDir(), "pool.db")
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("default MaxOpenConnections = %d, want 8", got)
	}

	cfg2 := &config.Config{}
	cfg2.Storage.Driver = "sqlite"
	cfg2.Storage.SQLite.Path = filepath.Join(t.TempDir(), "pool2.db")
	cfg2.Storage.SQLite.MaxOpenConns = 1
	db2, err := Open(cfg2)
	if err != nil {
		t.Fatalf("Open(1): %v", err)
	}
	defer db2.Close()
	if got := db2.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("override MaxOpenConnections = %d, want 1", got)
	}
}

// TestSQLiteConcurrencySemantics locks in the WAL+pool design at pool>1:
//   - a write transaction does NOT block concurrent autocommit reads (WAL), and
//   - a second write transaction serialises (waits for the first to commit via
//     BEGIN IMMEDIATE + busy_timeout) rather than racing or deadlocking.
//
// This is the invariant that makes a pool >1 safe for the check-then-act
// sections (StartRun, MoveIssueRunAware, ...).
func TestSQLiteConcurrencySemantics(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Driver = "sqlite"
	cfg.Storage.SQLite.Path = filepath.Join(t.TempDir(), "conc.db")
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v INT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec("INSERT INTO t(id,v) VALUES (1,0)"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const hold = 400 * time.Millisecond
	var wg sync.WaitGroup
	wg.Add(1)
	aHolds := make(chan struct{})
	go func() {
		defer wg.Done()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Errorf("writer A begin: %v", err)
			close(aHolds)
			return
		}
		if _, err := tx.Exec("UPDATE t SET v=v+1 WHERE id=1"); err != nil {
			t.Errorf("writer A update: %v", err)
		}
		close(aHolds)
		time.Sleep(hold)
		if err := tx.Commit(); err != nil {
			t.Errorf("writer A commit: %v", err)
		}
	}()
	<-aHolds
	time.Sleep(30 * time.Millisecond) // ensure A is mid-tx

	// Concurrent autocommit read must NOT block on the held write lock (WAL).
	readStart := time.Now()
	var v int
	if err := db.QueryRow("SELECT v FROM t WHERE id=1").Scan(&v); err != nil {
		t.Fatalf("concurrent read: %v", err)
	}
	if d := time.Since(readStart); d > hold/2 {
		t.Errorf("concurrent read blocked %v (>%v) — reads should not wait on the writer under WAL", d, hold/2)
	}

	// Second write tx must serialise: wait for A to commit, then succeed.
	writeStart := time.Now()
	txB, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("writer B begin: %v", err)
	}
	if _, err := txB.Exec("UPDATE t SET v=v+1 WHERE id=1"); err != nil {
		t.Fatalf("writer B update (should wait then succeed, not BUSY): %v", err)
	}
	if d := time.Since(writeStart); d < hold/2 {
		t.Errorf("writer B did not wait (%v) — expected to serialise behind writer A's BEGIN IMMEDIATE lock", d)
	}
	if err := txB.Commit(); err != nil {
		t.Fatalf("writer B commit: %v", err)
	}
	wg.Wait()

	if err := db.QueryRow("SELECT v FROM t WHERE id=1").Scan(&v); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if v != 2 {
		t.Errorf("v = %d, want 2 (both serialised writes applied)", v)
	}
}
