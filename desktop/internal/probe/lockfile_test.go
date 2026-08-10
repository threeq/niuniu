package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeLock(t *testing.T, dir string, info map[string]any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	b, err := json.Marshal(info)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.lock"), b, 0o644))
}

func TestReadLockfileAlivePID(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, map[string]any{
		"pid":        os.Getpid(),
		"addr":       "127.0.0.1:54321",
		"started_at": time.Now().UTC(),
	})
	info, err := readLockfile(dir)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "127.0.0.1:54321", info.Addr)
	require.True(t, pidAlive(info.PID))
}

func TestReadLockfileDeadPID(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, map[string]any{
		"pid":  99999999,
		"addr": "127.0.0.1:1",
	})
	info, err := readLockfile(dir)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.False(t, pidAlive(info.PID))
}

func TestReadLockfileAbsent(t *testing.T) {
	dir := t.TempDir()
	info, err := readLockfile(dir)
	require.NoError(t, err)
	require.Nil(t, info)
}

func TestReadLockfileMalformed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "server.lock"), []byte("{garbage"), 0o644))
	info, err := readLockfile(dir)
	require.NoError(t, err)
	require.Nil(t, info, "malformed file treated as no lock")
}
