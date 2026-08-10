package probe

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootLockAcquiresWhenFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "personal.boot.lock")

	lock, err := AcquireBootLock(path)
	require.NoError(t, err)
	require.NotNil(t, lock)
	require.NoError(t, lock.Release())
}

func TestBootLockBlocksSecondAcquire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "personal.boot.lock")

	lock1, err := AcquireBootLock(path)
	require.NoError(t, err)
	defer lock1.Release()

	// Second acquire in same process must fail immediately.
	lock2, err := AcquireBootLock(path)
	require.Error(t, err, "expected second acquire to fail while first holds the lock")
	require.Nil(t, lock2)
}

func TestBootLockReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "personal.boot.lock")

	lock1, err := AcquireBootLock(path)
	require.NoError(t, err)
	require.NoError(t, lock1.Release())

	lock2, err := AcquireBootLock(path)
	require.NoError(t, err)
	require.NoError(t, lock2.Release())
}
