package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLockfileWriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.lock")

	err := writeLockfile(path, LockfileInfo{
		PID:     12345,
		Addr:    "127.0.0.1:54321",
		Version: "v1.0.0",
	})
	require.NoError(t, err)

	b, err := os.ReadFile(path)
	require.NoError(t, err)

	var got LockfileInfo
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, 12345, got.PID)
	require.Equal(t, "127.0.0.1:54321", got.Addr)
	require.NotZero(t, got.StartedAt)
	require.Equal(t, "v1.0.0", got.Version)
}

func TestLockfileRemoveIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.lock")
	require.NoError(t, removeLockfile(path)) // absent — no error
	require.NoError(t, writeLockfile(path, LockfileInfo{PID: 1}))
	require.NoError(t, removeLockfile(path))
	require.NoError(t, removeLockfile(path)) // second remove no-op
}
