package probe

import "os"

// BootLock holds an OS-level exclusive lock on the boot lockfile,
// serializing the probe+spawn window across concurrent niuniu-desktop
// launches. Released by calling Release().
type BootLock struct {
	file *os.File
}

// AcquireBootLock opens (creating if needed) path and takes a non-
// blocking exclusive lock. Returns an error if the lock is already
// held by another process.
func AcquireBootLock(path string) (*BootLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := acquireOSLock(f); err != nil {
		f.Close()
		return nil, err
	}
	return &BootLock{file: f}, nil
}

// Release drops the lock and closes the backing file. Safe to call
// multiple times.
func (l *BootLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = releaseOSLock(l.file)
	err := l.file.Close()
	l.file = nil
	return err
}
