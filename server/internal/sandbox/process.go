package sandbox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// exitError reports a box that exited non-zero (or died from a signal).
type exitError struct {
	pid   int
	code  int
	sig   string
	extra string
}

func (e *exitError) Error() string {
	if e.sig != "" {
		return fmt.Sprintf("box process %d terminated by signal %s", e.pid, e.sig)
	}
	if e.extra != "" {
		return fmt.Sprintf("box process %d exited with code %d (%s)", e.pid, e.code, e.extra)
	}
	return fmt.Sprintf("box process %d exited with code %d", e.pid, e.code)
}

// execBoxProcess is the box wrapper for platforms whose fence is expressed by
// wrapping the command line (macOS sandbox-exec) or by clone flags (Linux).
type execBoxProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	// killTree kills the whole process group, not just the direct child, so a
	// box that somehow forked cannot leave survivors behind.
	killTree func() error
	waitErr  error
	waited   bool
	mu       sync.Mutex
}

func (p *execBoxProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execBoxProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *execBoxProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *execBoxProcess) Pid() int              { return p.cmd.Process.Pid }

func (p *execBoxProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waited {
		return p.waitErr
	}
	p.waited = true
	p.waitErr = p.cmd.Wait()
	return p.waitErr
}

func (p *execBoxProcess) Kill() error {
	if p.killTree != nil {
		return p.killTree()
	}
	return p.cmd.Process.Kill()
}

// PeakMemoryBytes is unknown on these platforms at this granularity; the
// supervisor falls back to process accounting when it is available.
func (p *execBoxProcess) PeakMemoryBytes() int64 { return 0 }

func (p *execBoxProcess) Close() {
	for _, c := range []io.Closer{p.stdin, p.stdout, p.stderr} {
		if c != nil {
			_ = c.Close()
		}
	}
}

// startExec builds and starts the box process through os/exec, wiring stdio
// pipes. configure may adjust the command (Linux clone flags, macOS wrapper).
func startExec(path string, args []string, env []string, dir string, configure func(*exec.Cmd) (*exec.Cmd, func() error, error)) (*execBoxProcess, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("box stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("box stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("box stderr pipe: %w", err)
	}
	var killTree func() error
	if configure != nil {
		cmd, killTree, err = configure(cmd)
		if err != nil {
			return nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start box: %w", err)
	}
	return &execBoxProcess{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		killTree: killTree,
	}, nil
}

// boxWorkDir returns a directory the box may use as its working directory. It
// is the system temp dir because every fence denies writes there anyway; using
// a real path avoids the "cwd does not exist" failure mode on platforms whose
// sandbox denies stat on the original cwd.
func boxWorkDir() string { return os.TempDir() }
