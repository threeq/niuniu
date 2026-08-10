//go:build windows

package bundle

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// spawnFromBinary — Windows variant.
//
// Race-free Job Object assignment:
//  1. CreateProcess with CREATE_SUSPENDED — child starts suspended, no user code runs.
//  2. AssignProcessToJobObject — child bound before it can execute.
//  3. ResumeThread(mainThread) — child begins executing, already inside the job.
//
// Without CREATE_SUSPENDED there is a window between Start and Assign where
// the child is running outside the job object; a parent crash in that window
// leaves the child as an orphan.
func spawnFromBinary(ctx context.Context, bin string, spec Spec) (*Handle, error) {
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	args := []string{"--embedded", "--addr=127.0.0.1:0"}
	args = append(args, spec.ExtraArgs...)

	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if spec.DataDir != "" {
		// Redirect server's home so os.UserHomeDir() + "/.niuniu" == spec.DataDir
		cmd.Env = append(os.Environ(), "USERPROFILE="+spec.DataDir+"\\..")
	} else {
		cmd.Env = os.Environ()
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	var stderrW *rotatingWriter
	if spec.LogFile != "" {
		retention := spec.LogRetentionDays
		if retention == 0 {
			retention = 30
		}
		stderrW = newRotatingWriter(spec.LogFile, retention)
		cmd.Stderr = stderrW
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start child: %w", err)
	}

	// Child is suspended. Assign to Job Object BEFORE resume.
	job, jobErr := createJobWithKillOnClose()
	if jobErr == nil {
		if err := assignProcessToJob(job, cmd.Process); err != nil {
			// Non-fatal: if assignment fails we still want the child to run.
			// The heartbeat pipe remains as parent-death guard.
			windows.CloseHandle(job)
			job = 0
		}
	}

	// Resume the child's main thread. Must happen AFTER job assignment.
	if err := resumeMainThread(cmd.Process.Pid); err != nil {
		// Child can't run — clean up and fail.
		stdin.Close()
		stdout.Close()
		if job != 0 {
			windows.CloseHandle(job)
		}
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("resume child thread: %w", err)
	}

	h := &Handle{
		cmd:          cmd,
		stdinPipe:    stdin,
		exitedCh:     make(chan struct{}),
		onCrash:      spec.OnCrash,
		stopCtxWatch: make(chan struct{}),
	}
	if stderrW != nil {
		h.stderrWriter = stderrW
	}
	h.platformClose = func() {
		if cmd.Process != nil {
			// CTRL_BREAK_EVENT to process group for graceful shutdown.
			_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
		}
		if job != 0 {
			windows.CloseHandle(job) // releasing the handle also terminates the job
			job = 0
		}
	}

	go h.waitLoop()

	addr, err := readReady(stdout, timeout)
	if err != nil {
		_ = h.Shutdown()
		return nil, err
	}
	h.Addr = addr

	// Stdout after ready: drain and tee to log file to prevent child from
	// blocking on a full stdout pipe.
	var sink io.Writer = io.Discard
	if cmd.Stderr != nil {
		sink = cmd.Stderr
	}
	go func() { _, _ = io.Copy(sink, stdout) }()

	// ctx-watcher: graceful Shutdown on context cancellation
	go func() {
		select {
		case <-ctx.Done():
			_ = h.Shutdown()
		case <-h.stopCtxWatch:
		}
	}()

	return h, nil
}

func (h *Handle) waitLoop() {
	h.exitErr = h.cmd.Wait()
	if h.stderrWriter != nil {
		_ = h.stderrWriter.Close()
	}
	close(h.exitedCh)
	if h.expectedExit.Load() == 0 && h.onCrash != nil {
		h.onCrash(h.exitErr)
	}
}

func (h *Handle) Shutdown() error {
	h.shutdownOnce.Do(func() {
		close(h.stopCtxWatch)
		h.expectedExit.Store(1)
		if h.platformClose != nil {
			h.platformClose()
		}
		if h.stdinPipe != nil {
			_ = h.stdinPipe.Close()
		}
		select {
		case <-h.exitedCh:
		case <-time.After(3 * time.Second):
			if h.cmd.Process != nil {
				_ = h.cmd.Process.Kill()
			}
			<-h.exitedCh
		}
	})
	return nil
}

// createJobWithKillOnClose creates a Windows Job Object whose only limit
// flag is JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. Parent exit → handle released
// → all processes in the job are terminated by the kernel.
func createJobWithKillOnClose() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	const JobObjectExtendedLimitInformation = 9
	type jobObjectBasicLimitInformation struct {
		PerProcessUserTimeLimit int64
		PerJobUserTimeLimit     int64
		LimitFlags              uint32
		MinimumWorkingSetSize   uintptr
		MaximumWorkingSetSize   uintptr
		ActiveProcessLimit      uint32
		Affinity                uintptr
		PriorityClass           uint32
		SchedulingClass         uint32
	}
	type ioCounters struct {
		ReadOperationCount  uint64
		WriteOperationCount uint64
		OtherOperationCount uint64
		ReadTransferCount   uint64
		WriteTransferCount  uint64
		OtherTransferCount  uint64
	}
	type jobObjectExtendedLimitInformation struct {
		BasicLimitInformation jobObjectBasicLimitInformation
		IoInfo                ioCounters
		ProcessMemoryLimit    uintptr
		JobMemoryLimit        uintptr
		PeakProcessMemoryUsed uintptr
		PeakJobMemoryUsed     uintptr
	}
	var info jobObjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	_, err = windows.SetInformationJobObject(
		job,
		JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignProcessToJob(job windows.Handle, proc *os.Process) error {
	ph, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(proc.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(job, ph)
}

// resumeMainThread iterates the process's thread snapshot and resumes
// the main thread (lowest TID in the process). Children created with
// CREATE_SUSPENDED have exactly one thread at this point, so "first
// one found matching our PID" is the main thread.
func resumeMainThread(pid int) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		return err
	}
	for {
		if te.OwnerProcessID == uint32(pid) {
			h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
			if err != nil {
				return err
			}
			_, err = windows.ResumeThread(h)
			windows.CloseHandle(h)
			return err
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			return err
		}
	}
}
