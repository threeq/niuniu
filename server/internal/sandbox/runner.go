package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// EgressFunc performs one outbound call on the box's behalf. The service wires
// this to the ordinary external-proxy path, so operation authorization,
// credential injection, the SSRF guard and the audit row all apply exactly as
// they do for a direct call_external_api — the box is never a way around them.
type EgressFunc func(ctx context.Context, req *HTTPRequestMessage) *HTTPResponseMessage

// boxPIDKey carries the box's process id on the context handed to EgressFunc.
type boxPIDKey struct{}

// BoxPID returns the running box's process id from inside an egress callback.
// It exists so a caller can name the box in diagnostics — and so the
// crash-resilience test can kill it and prove the supervisor survives.
func BoxPID(ctx context.Context) (int, bool) {
	pid, ok := ctx.Value(boxPIDKey{}).(int)
	return pid, ok && pid > 0
}

// RunRequest is one invocation.
type RunRequest struct {
	Spec   Spec
	Input  map[string]any
	Egress EgressFunc
	// EgressTimeout bounds ONE outbound call. Zero means 20s.
	EgressTimeout time.Duration
}

// RunResult is the outcome of one invocation, including the resource figures
// the audit row records.
type RunResult struct {
	OK              bool
	Output          any
	Error           *BoxError
	Logs            []string
	LogsTruncated   bool
	DurationMS      int64
	EgressCalls     int
	FenceBackend    string
	FenceComponents []ComponentStatus
	ExitCode        int
	PeakMemoryBytes int64
	// Killed reports that the supervisor had to terminate the box rather than
	// the box exiting on its own — the tell-tale of a runaway or a leak.
	Killed bool
	// StderrTail is the last of the box's stderr, for diagnosis. The box writes
	// nothing there in normal operation, so a non-empty tail means the runtime
	// itself failed.
	StderrTail string
}

// Run invokes an operation function once, in a fresh fenced process, and
// destroys the process afterwards whatever the outcome.
func Run(ctx context.Context, req RunRequest) *RunResult {
	started := time.Now()
	spec, err := req.Spec.Normalize()
	if err != nil {
		return &RunResult{Error: &BoxError{Code: ErrLintFailed, Message: err.Error()},
			DurationMS: time.Since(started).Milliseconds()}
	}
	avail := SelfCheck(false)
	if !avail.Available {
		reason := avail.Reason
		if reason == "" {
			reason = "the OS sandbox for this platform is unavailable"
		}
		return &RunResult{
			Error:           &BoxError{Code: ErrFenceUnavailable, Message: reason},
			FenceBackend:    avail.Backend,
			FenceComponents: avail.Components,
			DurationMS:      time.Since(started).Milliseconds(),
		}
	}
	helper := HelperPath()
	if helper == "" {
		return &RunResult{Error: &BoxError{Code: ErrSpawnFailed,
			Message: "the sandbox helper binary is not configured"}, DurationMS: time.Since(started).Milliseconds()}
	}
	lim := spec.Limits.normalized()

	proc, err := currentFence().Spawn(SpawnRequest{
		Path:        helper,
		Args:        []string{"--mem-mb", fmt.Sprint(lim.MemoryMB)},
		Env:         boxEnv(),
		Dir:         boxWorkDir(),
		MemoryBytes: lim.MemoryBytes(),
	})
	if err != nil {
		return &RunResult{
			Error:           &BoxError{Code: ErrSpawnFailed, Message: err.Error()},
			FenceBackend:    avail.Backend,
			FenceComponents: avail.Components,
			DurationMS:      time.Since(started).Milliseconds(),
		}
	}
	res := &RunResult{FenceBackend: avail.Backend, FenceComponents: avail.Components}
	// "执行完即销毁": the box never survives the call, on any path out of here.
	defer func() {
		if err := proc.Kill(); err != nil && !errors.Is(err, io.EOF) {
			// A box that already exited is the normal case; anything else is
			// worth a line, because a survivor is a containment failure.
			if !res.OK || res.Killed {
				res.StderrTail = strings.TrimSpace(res.StderrTail)
			}
		}
		proc.Close()
	}()

	cctx, cancel := context.WithTimeout(ctx, lim.Timeout())
	defer cancel()
	cctx = context.WithValue(cctx, boxPIDKey{}, proc.Pid())

	// Drain stderr so a box that floods it cannot block on a full pipe.
	stderrDone := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, io.LimitReader(proc.Stderr(), 8<<10))
		_, _ = io.Copy(io.Discard, proc.Stderr())
		stderrDone <- buf.String()
	}()

	if err := WriteFrame(proc.Stdin(), InvokeMessage{
		Kind:    KindInvoke,
		Name:    spec.Name,
		Runtime: spec.Runtime,
		Code:    spec.Code,
		Entry:   spec.Entry,
		Input:   req.Input,
		Limits:  lim,
	}); err != nil {
		res.Error = &BoxError{Code: ErrSpawnFailed, Message: "the box did not accept its invocation: " + err.Error()}
		res.DurationMS = time.Since(started).Milliseconds()
		proc.Kill()
		<-stderrDone
		return res
	}

	frames := make(chan frameOrErr, 8)
	go pumpFrames(proc.Stdout(), frames)

	mem := time.NewTicker(100 * time.Millisecond)
	defer mem.Stop()
	egressBudget := lim.MaxEgress
	logCap := lim.MaxLogBytes
	logBytes := 0

	finish := func() {
		res.DurationMS = time.Since(started).Milliseconds()
		res.PeakMemoryBytes = sampleMemory(proc)
		if err := proc.Wait(); err != nil {
			var ee *exitError
			if errors.As(err, &ee) {
				res.ExitCode = ee.code
			} else {
				res.ExitCode = -1
			}
		}
	}

	for {
		select {
		case <-cctx.Done():
			res.Killed = true
			_ = proc.Kill()
			if res.Error == nil {
				res.Error = &BoxError{Code: ErrTimeout,
					Message: fmt.Sprintf("the function exceeded its %dms budget and was terminated", lim.TimeoutMS)}
			}
			finish()
			<-stderrDone
			res.StderrTail = collectStderr(stderrDone, res.StderrTail)
			return res

		case <-mem.C:
			if used := sampleMemory(proc); used > 0 && used > lim.MemoryBytes() {
				res.Killed = true
				_ = proc.Kill()
				res.Error = &BoxError{Code: ErrMemoryLimit,
					Message: fmt.Sprintf("the function exceeded its %d MB ceiling and was terminated", lim.MemoryMB)}
				finish()
				res.StderrTail = collectStderr(stderrDone, "")
				return res
			}

		case fr := <-frames:
			if fr.err != nil {
				// EOF here is normal when the box already sent its result and
				// exited; anything else means the box died mid-protocol.
				if res.Error == nil && !fr.eof {
					res.Error = &BoxError{Code: ErrProtocol, Message: fr.err.Error()}
				}
				if res.Error == nil {
					res.Error = &BoxError{Code: ErrKilled,
						Message: "the box exited without returning a result"}
				}
				res.Killed = true
				_ = proc.Kill()
				finish()
				res.StderrTail = collectStderr(stderrDone, "")
				return res
			}
			switch fr.kind {
			case KindLog:
				var m LogMessage
				if json.Unmarshal(fr.body, &m) == nil {
					line := strings.TrimRight(m.Text, "\r\n")
					if logBytes+len(line) > logCap {
						res.LogsTruncated = true
						continue
					}
					logBytes += len(line)
					if len(res.Logs) < 2000 {
						res.Logs = append(res.Logs, line)
					}
				}
			case KindHTTPRequest:
				var m HTTPRequestMessage
				if err := json.Unmarshal(fr.body, &m); err != nil {
					// An unreadable request cannot be answered, so the box would
					// wait forever: end the run instead of hanging until timeout.
					res.Error = &BoxError{Code: ErrProtocol, Message: "undecodable http_request frame"}
					res.Killed = true
					_ = proc.Kill()
					finish()
					res.StderrTail = collectStderr(stderrDone, "")
					return res
				}
				if egressBudget <= 0 {
					res.Error = &BoxError{Code: ErrEgressLimit,
						Message: fmt.Sprintf("the function exceeded its %d outbound call budget", lim.MaxEgress)}
					_ = proc.Kill()
					res.Killed = true
					finish()
					res.StderrTail = collectStderr(stderrDone, "")
					return res
				}
				egressBudget--
				res.EgressCalls++
				reply := serviceEgress(cctx, req, spec, &m, lim, req.EgressTimeout)
				if err := WriteFrame(proc.Stdin(), reply); err != nil {
					res.Error = &BoxError{Code: ErrProtocol, Message: "the box stopped accepting egress replies"}
				}
			case KindResult:
				var m ResultMessage
				if err := json.Unmarshal(fr.body, &m); err != nil {
					res.Error = &BoxError{Code: ErrProtocol, Message: "undecodable result frame"}
				} else {
					res.OK = m.OK
					res.Output = m.Output
					res.Error = m.Error
					for _, l := range m.Logs {
						if logBytes+len(l) > logCap {
							res.LogsTruncated = true
							break
						}
						logBytes += len(l)
						res.Logs = append(res.Logs, l)
					}
					if m.EgressCalls > res.EgressCalls {
						res.EgressCalls = m.EgressCalls
					}
				}
				if res.OK {
					if n := jsonSize(res.Output); n > lim.MaxOutputBytes {
						res.OK = false
						res.Output = nil
						res.Error = &BoxError{Code: ErrOutputLimit,
							Message: fmt.Sprintf("the result was %d bytes, over the %d byte limit", n, lim.MaxOutputBytes)}
					}
				}
				_ = proc.Kill()
				finish()
				res.StderrTail = collectStderr(stderrDone, "")
				return res
			}
		}
	}
}

// serviceEgress runs one outbound call, enforcing the two box-side gates that
// the host owns: the spec's declared operation set, and the size ceiling.
func serviceEgress(ctx context.Context, req RunRequest, spec Spec, m *HTTPRequestMessage, lim Limits, timeout time.Duration) *HTTPResponseMessage {
	if !spec.Allows(m) {
		return &HTTPResponseMessage{Kind: KindHTTPResponse, ID: m.ID, OK: false,
			Error: fmt.Sprintf("the function is not authorized to call operation %q; only its declared operations are reachable", m.Operation)}
	}
	if n := jsonSize(m.Body); n > lim.MaxRequestBytes {
		return &HTTPResponseMessage{Kind: KindHTTPResponse, ID: m.ID, OK: false,
			Error: fmt.Sprintf("request body of %d bytes exceeds the %d byte limit", n, lim.MaxRequestBytes)}
	}
	if req.Egress == nil {
		return &HTTPResponseMessage{Kind: KindHTTPResponse, ID: m.ID, OK: false,
			Error: "no egress handler is configured"}
	}
	callCtx := ctx
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := req.Egress(callCtx, m)
	if out == nil {
		return &HTTPResponseMessage{Kind: KindHTTPResponse, ID: m.ID, OK: false, Error: "the egress handler returned nothing"}
	}
	out.Kind = KindHTTPResponse
	out.ID = m.ID
	if len(out.Body) > lim.MaxResponseBytes {
		out.Body = json.RawMessage(fmt.Sprintf(
			`{"truncated":true,"reason":"response exceeded the %d byte limit"}`, lim.MaxResponseBytes))
	}
	return out
}

// frameOrErr is one frame (or a terminal read error) from the box.
type frameOrErr struct {
	kind Kind
	body []byte
	err  error
	eof  bool
}

func pumpFrames(r io.Reader, out chan<- frameOrErr) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		kind, body, err := ReadFrame(br)
		if err != nil {
			out <- frameOrErr{err: err, eof: errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)}
			return
		}
		out <- frameOrErr{kind: kind, body: body}
	}
}

func collectStderr(ch <-chan string, fallback string) string {
	select {
	case s := <-ch:
		return strings.TrimSpace(s)
	case <-time.After(2 * time.Second):
		return strings.TrimSpace(fallback)
	}
}

func jsonSize(v any) int {
	if v == nil {
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// sampleMemory prefers the fence's own accounting and falls back to reading the
// process's resident set. A zero means "unknown", never "zero bytes used".
func sampleMemory(p boxProcess) int64 {
	if v := p.PeakMemoryBytes(); v > 0 {
		return v
	}
	return platformRSS(p.Pid())
}
