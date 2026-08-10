//go:build !windows

package terminal

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

type PTYProcess struct {
	cmd  *exec.Cmd
	pty  *os.File
	mu   sync.Mutex
	done chan struct{}
}

func NewPTYProcess(command string, args []string, workDir string, env []string) (*PTYProcess, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = workDir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	} else {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	return &PTYProcess{
		cmd:  cmd,
		pty:  ptmx,
		done: make(chan struct{}),
	}, nil
}

func (p *PTYProcess) Read(buf []byte) (int, error)   { return p.pty.Read(buf) }
func (p *PTYProcess) Write(data []byte) (int, error) { return p.pty.Write(data) }
func (p *PTYProcess) Resize(rows, cols uint16) error {
	return pty.Setsize(p.pty, &pty.Winsize{Rows: rows, Cols: cols})
}
func (p *PTYProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	default:
		close(p.done)
	}
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	return p.pty.Close()
}
func (p *PTYProcess) Wait() error           { return p.cmd.Wait() }
func (p *PTYProcess) Pid() int              { return p.cmd.Process.Pid }
func (p *PTYProcess) Done() <-chan struct{} { return p.done }
