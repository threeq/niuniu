package probe

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

// TestDecide_EmptyFreshDir verifies that with nothing going on, we get
// an empty Decision (caller will spawn).
func TestDecide_EmptyFreshDir(t *testing.T) {
	dir := t.TempDir()
	d, err := Decide(dir, "v1.0.0")
	require.NoError(t, err)
	require.Nil(t, d.Reuse)
	require.Empty(t, d.Refuse)
}

// TestDecide_LockfileReachable: lockfile present, pid alive, DB busy,
// /api/health 200 with matching version → Reuse.
func TestDecide_LockfileReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/health") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"version":"v1.0.5"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	addr := "127.0.0.1:" + u.Port()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "niuniu.db")
	heldUntil := holdDBLock(t, dbPath)
	defer heldUntil()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.lock"),
		[]byte(`{"pid":`+strconv.Itoa(os.Getpid())+`,"addr":"`+addr+`","version":"v1.0.5"}`),
		0o644))

	d, err := Decide(dir, "v1.0.3")
	require.NoError(t, err)
	require.NotNil(t, d.Reuse)
	require.Equal(t, addr, d.Reuse.Addr)
	require.Equal(t, "lockfile", d.Reuse.Source)
}

// TestDecide_VersionMismatch: DB busy + server reachable + version incompatible → Refuse.
func TestDecide_VersionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/health") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"version":"v2.0.0"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	addr := "127.0.0.1:" + u.Port()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "niuniu.db")
	heldUntil := holdDBLock(t, dbPath)
	defer heldUntil()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.lock"),
		[]byte(`{"pid":`+strconv.Itoa(os.Getpid())+`,"addr":"`+addr+`","version":"v2.0.0"}`),
		0o644))

	d, err := Decide(dir, "v1.0.0")
	require.NoError(t, err)
	require.Nil(t, d.Reuse)
	require.Contains(t, d.Refuse, "incompatible")
}

// TestDecide_BusyButUnreachable: DB busy but no way to find the server's addr → Refuse.
func TestDecide_BusyButUnreachable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "niuniu.db")
	heldUntil := holdDBLock(t, dbPath)
	defer heldUntil()

	// No lockfile, no config.yaml. Default port 3000 may be occupied on the
	// test machine (e.g. by a running niuniu server). Skip that case gracefully
	// rather than failing — the important assertion is that we always Refuse
	// (never Reuse) when there is no valid compatible server to connect to.
	d, err := Decide(dir, "v1.0.0")
	require.NoError(t, err)
	require.Nil(t, d.Reuse)
	// Either "cannot be reached" (no server on 3000) or a version/health error
	// (something is on 3000 but incompatible) — both are valid Refuse strings.
	require.NotEmpty(t, d.Refuse)
}

// holdDBLock opens a SQLite DB at dbPath, starts an EXCLUSIVE transaction,
// and returns a release function the test calls on cleanup.
func holdDBLock(t *testing.T, dbPath string) func() {
	t.Helper()
	// Use the same DSN shape as dbIsBusy
	db, err := openSQLiteForTestHold(dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS x(i INTEGER)")
	require.NoError(t, err)
	_, err = db.Exec("BEGIN EXCLUSIVE")
	require.NoError(t, err)
	return func() {
		_, _ = db.Exec("ROLLBACK")
		db.Close()
	}
}

func openSQLiteForTestHold(dbPath string) (*sql.DB, error) {
	// Same journal_mode as dbIsBusy for consistency
	dsn := dbPath + "?_pragma=busy_timeout(200)&_pragma=journal_mode(DELETE)"
	return sql.Open("sqlite", dsn)
}
