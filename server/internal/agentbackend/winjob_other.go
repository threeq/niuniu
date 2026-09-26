//go:build !windows

package agentbackend

// AssignKillOnCloseJob is a Windows-only lifecycle guard; on other platforms
// the backends clean up their children via process groups / signal handling.
func AssignKillOnCloseJob(pid int) (func(), error) {
	return nil, nil
}

// IsTransientSpawnError has no "file in use" spawn failure mode outside
// Windows.
func IsTransientSpawnError(err error) bool {
	return false
}
