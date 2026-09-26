//go:build windows

package agentbackend

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AssignKillOnCloseJob puts a freshly spawned backend process into a Windows
// job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE and returns a func that
// closes the job handle. Closing that handle terminates every process still
// in the job (e.g. an agent's MCP children). When the server itself exits —
// cleanly or crashed — the OS closes its handles, the job's last handle goes
// away and the whole backend tree is killed with it: sidecar processes can
// never outlive the server and lock their own binaries against the next
// version's sidecar re-extraction.
//
// A failed assignment is reported but callers SHOULD treat it as non-fatal:
// some launchers put the whole tree in their own job, and nested-assignment
// restrictions must not break spawning.
func AssignKillOnCloseJob(pid int) (closeJob func(), err error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, uint32(windows.JobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process %d to job: %w", pid, err)
	}
	return func() { _ = windows.CloseHandle(job) }, nil
}

// IsTransientSpawnError reports whether a failed exec.Start is the Windows
// "file in use" family (sharing/lock violation) — typically a fresh binary
// still held by the extractor or an antivirus scan. A short retry clears it.
func IsTransientSpawnError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == windows.ERROR_SHARING_VIOLATION || errno == windows.ERROR_LOCK_VIOLATION
	}
	return false
}
