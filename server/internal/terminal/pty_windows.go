//go:build windows

package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
)

// PTYProcess on Windows uses the ConPTY (Pseudo Console) API for proper
// terminal emulation with echo, line editing, and ANSI escape sequences.
type PTYProcess struct {
	cpty *conpty.ConPty
	mu   sync.Mutex
	done chan struct{}

	// cancel stops the Wait goroutine
	cancel context.CancelFunc
}

// NewPTYProcess creates a PTY process using Windows ConPTY API.
func NewPTYProcess(command string, args []string, workDir string, env []string) (*PTYProcess, error) {
	// ConPTY's Start() calls Win32 CreateProcess directly, which (a) does NOT
	// search PATHEXT and (b) cannot execute .cmd/.bat batch files. Resolve
	// the binary up front and wrap batch files with cmd.exe so npm-installed
	// tools (claude.cmd, etc.) launch correctly.
	commandLine := resolveCommandLine(command, args, exec.LookPath)

	// Build environment
	cmdEnv := os.Environ()
	if env != nil {
		cmdEnv = append(cmdEnv, env...)
	}
	cmdEnv = append(cmdEnv, "TERM=xterm-256color")

	// ConPTY options
	opts := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(120, 30),
		conpty.ConPtyWorkDir(workDir),
		conpty.ConPtyEnv(cmdEnv),
	}

	cpty, err := conpty.Start(commandLine, opts...)
	if err != nil {
		return nil, fmt.Errorf("conpty start: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &PTYProcess{
		cpty:   cpty,
		done:   make(chan struct{}),
		cancel: cancel,
	}

	// Wait for process exit in background
	go func() {
		cpty.Wait(ctx)
		p.mu.Lock()
		defer p.mu.Unlock()
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}()

	return p, nil
}

// resolveCommandLine resolves command via PATH+PATHEXT and wraps .cmd/.bat
// batch files with cmd.exe /s /c, returning a command line string suitable
// for ConPTY's Start() (which calls CreateProcess directly without doing
// either of those things itself).
//
// LookPath failure is non-fatal: we fall back to the unresolved command so
// the eventual CreateProcess error matches the pre-fix behavior for a truly
// missing binary. The lookPath parameter is injectable for tests.
func resolveCommandLine(command string, args []string, lookPath func(string) (string, error)) string {
	resolved := command
	if p, err := lookPath(command); err == nil {
		resolved = p
	}
	ext := strings.ToLower(filepath.Ext(resolved))
	if ext == ".cmd" || ext == ".bat" {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		// cmd.exe /s /c "<inner>": with /s, cmd.exe unconditionally strips
		// the outer quote pair, so an inner string that itself contains
		// quoted paths-with-spaces parses correctly. See `cmd.exe /?`
		// ("If /C or /K is specified, then the remainder of the command
		// line ...").
		inner := buildCommandLine(resolved, args)
		return fmt.Sprintf(`%s /s /c "%s"`, comspec, inner)
	}
	return buildCommandLine(resolved, args)
}

// buildCommandLine constructs a Windows command line string from command and args.
func buildCommandLine(command string, args []string) string {
	parts := []string{command}
	parts = append(parts, args...)
	// Quote parts that contain spaces
	for i, part := range parts {
		if strings.Contains(part, " ") && !strings.HasPrefix(part, "\"") {
			parts[i] = "\"" + part + "\""
		}
	}
	return strings.Join(parts, " ")
}

// Read from the PTY output.
func (p *PTYProcess) Read(buf []byte) (int, error) {
	return p.cpty.Read(buf)
}

// Write to the PTY input.
func (p *PTYProcess) Write(data []byte) (int, error) {
	return p.cpty.Write(data)
}

// Resize the PTY window.
func (p *PTYProcess) Resize(rows, cols uint16) error {
	return p.cpty.Resize(int(cols), int(rows))
}

// Close terminates the process and releases resources.
func (p *PTYProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	default:
		close(p.done)
	}
	p.cancel()
	return p.cpty.Close()
}

// Wait for process to exit.
func (p *PTYProcess) Wait() error {
	<-p.done
	return nil
}

// Pid returns the process ID.
func (p *PTYProcess) Pid() int {
	return p.cpty.Pid()
}

// Done returns a channel that's closed when the process exits.
func (p *PTYProcess) Done() <-chan struct{} {
	return p.done
}
