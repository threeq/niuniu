// Command niuniu-funcbox is the in-box runtime for operation functions
// (issue #739). It is spawned by the server for exactly one invocation and
// destroyed afterwards.
//
// It is deliberately tiny. It reads one InvokeMessage from stdin, applies the
// half of the OS fence that belongs to the box (Landlock + seccomp on Linux),
// runs the function with goja, and answers http_request frames. It never opens
// a socket, never touches the filesystem, and passes no environment through to
// the function.
//
// Run with --probe it applies the self-fence and exits: that is how the host's
// startup self-check verifies the fence for real instead of checking that a
// mechanism merely exists.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
)

// Exit codes. The supervisor reports the code alongside the structured error,
// so they are part of the diagnostic surface.
const (
	exitOK           = 0
	exitSelfFence    = 3
	exitProtocol     = 4
	exitRuntime      = 5
	exitInterrupted  = 6
	exitInternalFail = 7
)

func main() {
	memMB := flag.Int("mem-mb", 128, "memory ceiling passed to the in-box fence")
	probe := flag.Bool("probe", false, "apply the self-fence and exit (used by the host self-check)")
	selfTest := flag.Bool("fence-selftest", false, "attempt a file read, a socket and a subprocess and report which the OS fence refused")
	probeFile := flag.String("file", "", "path the fence self-test tries to read")
	probeAddr := flag.String("addr", "", "host:port the fence self-test tries to connect to")
	flag.Parse()

	// The fence goes on BEFORE anything else runs. From here on the process can
	// not open a file, create a socket or spawn a process, and function code has
	// not been evaluated yet, so there is no unconfined window.
	if err := sandbox.ApplySelfFence(*memMB); err != nil {
		fmt.Fprintf(os.Stderr, "funcbox: self-fence failed: %v\n", err)
		os.Exit(exitSelfFence)
	}
	if *probe {
		os.Exit(exitOK)
	}
	if *selfTest {
		out := bufio.NewWriter(os.Stdout)
		_ = sandbox.WriteFrame(out, fenceSelfTest(*probeFile, *probeAddr))
		finish(out)
		os.Exit(exitOK)
	}

	in := bufio.NewReaderSize(os.Stdin, 64<<10)
	out := bufio.NewWriterSize(os.Stdout, 64<<10)

	kind, body, err := sandbox.ReadFrame(in)
	if err != nil {
		fail(out, exitProtocol, sandbox.ErrProtocol, "the box received no invocation: "+err.Error())
	}
	if kind != sandbox.KindInvoke {
		fail(out, exitProtocol, sandbox.ErrProtocol,
			fmt.Sprintf("the box expected an invoke frame but received %q", kind))
	}
	var invoke sandbox.InvokeMessage
	if err := jsonUnmarshal(body, &invoke); err != nil {
		fail(out, exitProtocol, sandbox.ErrProtocol, "undecodable invoke frame")
	}

	br := &bridge{out: out, in: in, maxCalls: invoke.Limits.MaxEgress}

	logs := &logSink{max: invoke.Limits.MaxLogBytes}
	result, runErr := runFunction(invoke, br, logs)
	finish(out)

	if runErr != nil {
		_ = sandbox.WriteFrame(out, sandbox.ResultMessage{
			Kind:        sandbox.KindResult,
			OK:          false,
			Error:       runErr,
			Logs:        logs.lines,
			EgressCalls: br.calls,
		})
		finish(out)
		os.Exit(exitRuntime)
	}
	_ = sandbox.WriteFrame(out, sandbox.ResultMessage{
		Kind:        sandbox.KindResult,
		OK:          true,
		Output:      result,
		Logs:        logs.lines,
		EgressCalls: br.calls,
	})
	finish(out)
	os.Exit(exitOK)
}

// fail reports a terminal protocol error and exits.
func fail(out *bufio.Writer, code int, errCode, msg string) {
	_ = sandbox.WriteFrame(out, sandbox.ResultMessage{
		Kind:  sandbox.KindResult,
		OK:    false,
		Error: &sandbox.BoxError{Code: errCode, Message: msg},
	})
	finish(out)
	os.Exit(code)
}

func finish(out *bufio.Writer) { _ = out.Flush() }

// logSink accumulates console output within the invocation's log budget.
type logSink struct {
	lines []string
	bytes int
	max   int
}

func (l *logSink) add(s string) {
	if l.max > 0 && l.bytes+len(s) > l.max {
		return
	}
	l.bytes += len(s)
	if len(l.lines) < 2000 {
		l.lines = append(l.lines, s)
	}
}
