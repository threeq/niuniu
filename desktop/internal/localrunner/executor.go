package localrunner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
)

// executor.go runs an authorized command inside the bound directory and streams
// its output (#477). stdout/stderr are streamed line-by-line through onLog as
// they are produced (feeding the live log frames), and also accumulated (capped)
// for the final response frame.

// maxCapture bounds how much stdout/stderr we keep for the response frame, so a
// chatty command can't blow up memory or overflow the server read limit. The
// live stream still carries everything line-by-line.
const maxCapture = 256 * 1024

// maxLineBytes caps a single streamed line so a binary blob without newlines
// can't produce one enormous frame.
const maxLineBytes = 16 * 1024

// ExecResult is the outcome of running a command.
type ExecResult struct {
	Stdout string
	Stderr string
	Exit   int
	OK     bool
	// Err is set for failures to even start/complete the process (e.g. binary
	// not found); a non-zero exit code alone is NOT an Err (OK is false, Err nil).
	Err error
}

// LogFunc receives one output line as it is produced. level is levelStdout or
// levelStderr.
type LogFunc func(level, text string)

// Execute runs command in dir, streaming output through onLog (may be nil) and
// returning the captured result. It honors ctx cancellation. Execution assumes
// the gateway has already authorized command and that dir is the bound root.
//
// The command runs through the platform shell (buildShellCmd), so the exact
// string an agent would type ("npm run build", `echo import "fmt" > x.go`) works
// verbatim — including embedded quotes, which the Windows path passes to cmd.exe
// raw instead of letting Go's os/exec mangle them (\" → broken).
func Execute(ctx context.Context, command, dir string, onLog LogFunc) ExecResult {
	cmd := buildShellCmd(ctx, command)
	cmd.Dir = dir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{Exit: -1, OK: false, Err: err}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return ExecResult{Exit: -1, OK: false, Err: err}
	}

	if err := cmd.Start(); err != nil {
		return ExecResult{Exit: -1, OK: false, Err: err}
	}

	var outBuf, errBuf cappedBuffer
	var wg sync.WaitGroup
	wg.Add(2)
	go streamPipe(stdoutPipe, levelStdout, &outBuf, onLog, &wg)
	go streamPipe(stderrPipe, levelStderr, &errBuf, onLog, &wg)
	wg.Wait()

	waitErr := cmd.Wait()
	res := ExecResult{Stdout: outBuf.String(), Stderr: errBuf.String()}
	if waitErr == nil {
		res.Exit = 0
		res.OK = true
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// Ran to completion with a non-zero status — not an engine error.
		res.Exit = exitErr.ExitCode()
		res.OK = false
		return res
	}
	// Failed to run (not found, permission, context cancelled, ...).
	res.Exit = -1
	res.OK = false
	res.Err = waitErr
	return res
}

// streamPipe scans r line-by-line, forwarding each line to onLog and appending
// to buf (capped), until EOF.
func streamPipe(r io.Reader, level string, buf *cappedBuffer, onLog LogFunc, wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Text()
		buf.WriteLine(line)
		if onLog != nil {
			onLog(level, line)
		}
	}
	// A scan error (e.g. line longer than maxLineBytes) just ends streaming for
	// this pipe; whatever was captured stands. Draining the rest avoids blocking
	// the child on a full pipe.
	_, _ = io.Copy(io.Discard, r)
}

// cappedBuffer accumulates output up to maxCapture bytes, then silently drops
// the overflow (the live stream already carried it).
type cappedBuffer struct {
	buf     bytes.Buffer
	dropped bool
}

func (c *cappedBuffer) WriteLine(line string) {
	if c.dropped {
		return
	}
	if c.buf.Len()+len(line)+1 > maxCapture {
		c.dropped = true
		return
	}
	c.buf.WriteString(line)
	c.buf.WriteByte('\n')
}

func (c *cappedBuffer) String() string { return c.buf.String() }
