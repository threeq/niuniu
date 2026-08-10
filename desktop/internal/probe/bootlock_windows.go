//go:build windows

package probe

import (
	"os"

	"golang.org/x/sys/windows"
)

func acquireOSLock(f *os.File) error {
	// LockFileEx with LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY
	const locksize = ^uint32(0)
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, locksize, locksize, &overlapped,
	)
}

func releaseOSLock(f *os.File) error {
	const locksize = ^uint32(0)
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, locksize, locksize, &overlapped,
	)
}
