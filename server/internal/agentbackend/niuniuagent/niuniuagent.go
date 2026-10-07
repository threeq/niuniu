// Package niuniuagent implements agentbackend.Backend for niuniu's own agent
// (agent/ module), driven over the Agent Client Protocol (ACP) on stdio
// (`niuniu-agent acp`).
//
// This is the engine niuniu controls end-to-end: the host spawns the binary,
// performs the initialize + session/new handshake, delivers each turn as
// `session/prompt`, streams `session/update` notifications into normalized
// agentbackend.Events, and answers `session/request_permission` requests via
// the host's ResolvePermission bridge (same approval-card flow the other
// backend-driven engines use).
package niuniuagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
)

// DefaultCommand is the executable invoked when Options.Command is empty.
const DefaultCommand = "niuniu-agent"

// DefaultHandshakeTimeout bounds how long Start waits for the initialize +
// session/new round-trips.
const DefaultHandshakeTimeout = 15 * time.Second

// spawnRetryDelay waits out a transient binary lock (extractor handle /
// antivirus scan) between spawn retries; spawnRetryAttempts bounds them.
const spawnRetryDelay = 2 * time.Second
const spawnRetryAttempts = 3

// Options configures the Backend.
type Options struct {
	// Command is the niuniu-agent executable (default "niuniu-agent"). For
	// tests this points at a scripted fake subprocess.
	Command string
	// Args are extra CLI args appended after "acp".
	Args []string
	// WorkDir is the workspace directory the session is rooted at.
	WorkDir string
	// Env are additional environment variables (KEY=VALUE) for the child;
	// the agent reads ANTHROPIC_*/OPENAI_*/NIUNIU_AGENT_PROVIDER from them.
	Env []string

	// ResolvePermission bridges ACP session/request_permission requests to
	// the host UI. Nil → requests are auto-denied (fail closed).
	ResolvePermission func(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error)

	// HandshakeTimeout bounds Start's handshake wait. Defaults to
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
}

// Backend is the agentbackend.Backend implementation for niuniu-agent.
// Create with [New]; safe for one in-flight Prompt at a time.
type Backend struct {
	opts Options

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	started  bool
	closed   bool
	exited   bool
	exitErr  error
	session  string
	jobClose func()

	pending map[int64]chan rpcResponse

	active     *turnStream
	activeDone chan struct{}

	seq atomic.Int64
}

// New creates a Backend from options.
func New(opts Options) *Backend {
	if opts.Command == "" {
		opts.Command = DefaultCommand
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = DefaultHandshakeTimeout
	}
	return &Backend{opts: opts, pending: make(map[int64]chan rpcResponse)}
}

// Start spawns `niuniu-agent acp`, performs the initialize handshake, and
// creates a session rooted at WorkDir. Idempotent.
func (b *Backend) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	if b.closed {
		b.mu.Unlock()
		return errors.New("niuniu-agent backend already closed")
	}
	b.mu.Unlock()

	args := append([]string{"acp"}, b.opts.Args...)
	start := func() (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
		cmd := exec.CommandContext(ctx, b.opts.Command, args...)
		cmd.Dir = b.opts.WorkDir
		// Non-nil cmd.Env replaces the child's environment entirely — seed from
		// os.Environ() so PATH/HOME survive.
		cmd.Env = append(os.Environ(), b.opts.Env...)

		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("niuniu-agent stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("niuniu-agent stdout pipe: %w", err)
		}
		// Capture the child's stderr into the server log — panics, model-client
		// errors, and crash stacks were previously inherited by the server's
		// terminal (invisible when run as a service), leaving a hung turn
		// undiagnosable.
		cmd.Stderr = &stderrLogger{backend: "niuniu-agent"}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, err
		}
		return cmd, stdin, stdout, nil
	}

	cmd, stdin, stdout, err := start()
	// Windows: a just-replaced sidecar binary can still be held for a while by
	// the extractor or an antivirus scan (sharing violation) — bounded retries
	// clear it instead of failing the turn.
	for attempt := 1; err != nil && agentbackend.IsTransientSpawnError(err) && attempt < spawnRetryAttempts; attempt++ {
		slog.Warn("niuniu-agent: spawn hit a locked binary, retrying", "path", b.opts.Command, "attempt", attempt, "err", err)
		time.Sleep(spawnRetryDelay)
		cmd, stdin, stdout, err = start()
	}
	if err != nil {
		return fmt.Errorf("niuniu-agent start: %w", err)
	}

	// Windows: tie the agent (and its MCP children) to the server's lifetime —
	// a crashed or force-killed server must not leave orphan processes holding
	// the sidecar binaries. Non-fatal on failure (nested job restrictions).
	if closeJob, jobErr := agentbackend.AssignKillOnCloseJob(cmd.Process.Pid); jobErr != nil {
		slog.Warn("niuniu-agent: job-object assignment failed (orphan protection disabled)", "err", jobErr)
	} else {
		b.mu.Lock()
		b.jobClose = closeJob
		b.mu.Unlock()
	}

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.started = true
	b.mu.Unlock()

	go b.readLoop(stdout)

	// Reap the process: without Wait the exit is never observed (and the
	// handle leaks); with it, Dead() can report death accurately and exitErr
	// explains WHY the agent went away.
	go func() {
		waitErr := cmd.Wait()
		b.mu.Lock()
		b.exited = true
		if waitErr != nil && b.exitErr == nil {
			b.exitErr = waitErr
		}
		b.mu.Unlock()
	}()

	if err := b.initialize(ctx); err != nil {
		_ = b.Close(context.Background())
		return err
	}
	if err := b.createSession(ctx); err != nil {
		_ = b.Close(context.Background())
		return err
	}
	return nil
}

// initialize performs the ACP `initialize` handshake.
func (b *Backend) initialize(ctx context.Context) error {
	params := initializeParams{ProtocolVersion: 1}
	_, err := b.request(ctx, "initialize", params, b.opts.HandshakeTimeout)
	return err
}

// createSession performs `session/new` rooted at the workspace dir.
func (b *Backend) createSession(ctx context.Context) error {
	resp, err := b.request(ctx, "session/new", newSessionParams{CWD: b.opts.WorkDir}, b.opts.HandshakeTimeout)
	if err != nil {
		return err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	if result.SessionID == "" {
		return errors.New("session/new returned empty sessionId")
	}
	b.mu.Lock()
	b.session = result.SessionID
	b.mu.Unlock()
	return nil
}

// readLoop scans stdout JSON-RPC and dispatches responses vs requests.
func (b *Backend) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		b.dispatch([]byte(line))
	}
	// stdout EOF = the process is gone. Mark death immediately (the Wait
	// goroutine refines exitErr when it lands) so Dead() is accurate and the
	// next write fails with a clear reason instead of a raw broken pipe.
	b.mu.Lock()
	if !b.exited {
		b.exited = true
		b.exitErr = errors.New("stdout closed")
	}
	b.mu.Unlock()
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "niuniu-agent process exited"})
}

// Dead implements agentbackend.DeadChecker: the agent process exited (crash,
// watchdog kill, manual stop) or the backend was closed — it cannot serve
// further turns. The session layer drops a dead backend and Starts a fresh
// one (the Abort hard-kill path relies on this to deliver its promised
// respawn).
func (b *Backend) Dead() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exited || b.closed
}

// dispatch routes one stdout frame: response (id, no method) resolves a
// pending request; request (id + method) is the permission sub-protocol;
// notification (method, no id) is a session/update.
func (b *Backend) dispatch(raw []byte) {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return
	}
	hasID := len(probe.ID) > 0 && string(probe.ID) != "null"
	if hasID && probe.Method == "" {
		var id int64
		if json.Unmarshal(probe.ID, &id) == nil {
			b.handleResponse(raw, id)
		}
		return
	}
	if hasID && probe.Method != "" {
		if probe.Method == "session/request_permission" {
			// Off the readLoop: the host bridge blocks on the user's decision,
			// and event streaming must not stall meanwhile.
			go b.handleRequestPermission(raw)
		}
		return
	}
	if probe.Method == "session/update" {
		b.handleSessionUpdate(raw)
	}
	// Unknown notifications: ignored.
}

// handleResponse delivers an agent response to the pending request.
func (b *Backend) handleResponse(raw []byte, id int64) {
	b.mu.Lock()
	ch := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if ch == nil {
		return
	}
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err == nil {
		ch <- resp
	}
	close(ch)
}

// handleSessionUpdate maps one `session/update` notification onto events.
func (b *Backend) handleSessionUpdate(raw []byte) {
	var notif struct {
		Params struct {
			Update updateBody `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &notif); err != nil {
		return
	}
	upd := notif.Params.Update
	switch upd.SessionUpdate {
	case "agent_message_chunk":
		if upd.Content != nil {
			b.emit(agentbackend.Event{Type: agentbackend.EventText, Text: upd.Content.Text})
		}
	case "agent_thought_chunk":
		// Reasoning (chain-of-thought) surfaced by the agent — same shaped
		// event the goose/claude engines emit for thinking content.
		if upd.Content != nil {
			b.emit(agentbackend.Event{Type: agentbackend.EventThinking, Thinking: upd.Content.Text})
		}
	case "tool_call":
		b.emit(agentbackend.Event{
			Type:      agentbackend.EventToolUse,
			ToolName:  upd.Title,
			ToolUseID: upd.ToolCallID,
		})
	case "tool_call_update":
		if upd.Status == "completed" || upd.Status == "failed" {
			out := ""
			if upd.Output != nil {
				out = upd.Output.Text
			}
			b.emit(agentbackend.Event{
				Type:      agentbackend.EventToolResult,
				ToolUseID: upd.ToolCallID,
				Text:      out,
				IsError:   upd.Status == "failed",
			})
		}
	}
}

// handleRequestPermission answers the agent's `session/request_permission`
// request via the host bridge and writes the outcome back as the response.
func (b *Backend) handleRequestPermission(raw []byte) {
	var top struct {
		ID     json.RawMessage         `json:"id"`
		Params requestPermissionParams `json:"params"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return
	}

	req := agentbackend.PermissionRequest{
		ID:     top.Params.ToolCallID,
		Method: "confirm",
		Title:  firstNonEmpty(top.Params.Title, "niuniu-agent 请求权限"),
		Message: firstNonEmpty(top.Params.Title, "niuniu-agent 请求权限") +
			"（" + firstNonEmpty(top.Params.Kind, "工具") + "）",
	}

	confirmed := false
	if b.opts.ResolvePermission != nil {
		if d, err := b.opts.ResolvePermission(context.Background(), req); err == nil {
			confirmed = d.Confirmed
		}
	}
	optionID := "reject_once"
	if confirmed {
		optionID = "allow_once"
	}
	outcome := map[string]any{
		"outcome": map[string]string{"outcome": "selected", "optionId": optionID},
	}
	result, _ := json.Marshal(outcome)
	resp := rpcResponse{JSONRPC: "2.0", ID: top.ID, Result: result}
	_ = b.write(resp)
}

// turnStream is one turn's outbound event channel. The mutex serializes send
// against finish: a turn can be torn down (timeout / Close) while the
// readLoop is mid-dispatch, and a bare channel send after close would panic.
type turnStream struct {
	mu     sync.Mutex
	ch     chan agentbackend.Event
	closed bool
}

func newTurnStream(buffer int) *turnStream {
	return &turnStream{ch: make(chan agentbackend.Event, buffer)}
}

func (ts *turnStream) send(ev agentbackend.Event) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		return
	}
	ts.ch <- ev
}

// finish appends the terminal event and closes the stream. Idempotent.
func (ts *turnStream) finish(ev agentbackend.Event) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		return
	}
	ts.closed = true
	ts.ch <- ev
	close(ts.ch)
}

// Prompt sends a user turn and returns its event stream. The channel is
// handed to the caller immediately: niuniu-agent answers session/prompt only
// when the whole turn settles (minutes for real tasks) while session/update
// deltas stream throughout, so waiting for the response before returning
// would leave the buffer unread during the turn — emit wedges the readLoop,
// the OS pipe backpressures, and the agent freezes mid-generation until
// teardown dumps every buffered event at once.
func (b *Backend) Prompt(ctx context.Context, req agentbackend.PromptRequest) (<-chan agentbackend.Event, error) {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil, errors.New("niuniu-agent backend not started")
	}
	if b.active != nil {
		b.mu.Unlock()
		return nil, errors.New("niuniu-agent backend already has an in-flight turn")
	}
	active := newTurnStream(256)
	turnDone := make(chan struct{})
	b.active = active
	b.activeDone = turnDone
	sessionID := b.session
	b.mu.Unlock()

	blocks := []promptBlock{{Type: "text", Text: req.Message}}
	started := time.Now()
	// No per-request deadline (-1): a session/prompt spans the WHOLE agent
	// turn, which the turn-level INACTIVITY watchdog upstream (turnCtx →
	// Abort below) bounds. A fixed cap here re-introduced the hard-ceiling
	// bug the watchdog exists to prevent — legitimate long turns died with
	// "context deadline exceeded" mid-flight.
	go func() {
		result, err := b.request(ctx, "session/prompt", promptParams{SessionID: sessionID, Prompt: blocks}, -1)
		if err != nil {
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: err.Error()})
			return
		}
		// The prompt response IS the turn end for niuniu-agent (stopReason and
		// usage ride the response).
		var pr promptResult
		_ = json.Unmarshal(result, &pr) // unparseable usage → zero-token done event
		b.finishTurn(b.doneEvent("", pr.Usage, time.Since(started)))
	}()

	go func() {
		select {
		case <-ctx.Done():
			_ = b.Abort(context.Background())
		case <-turnDone:
		}
	}()
	return active.ch, nil
}

// doneEvent builds the terminal event. Usage (when the agent reported it in
// the session/prompt result) feeds the same cost/token columns the other
// engines use; CacheReadTokens drives the context-occupancy pill.
func (b *Backend) doneEvent(errText string, usage *usageBody, dur time.Duration) agentbackend.Event {
	kind := agentbackend.EventDone
	if errText != "" {
		kind = agentbackend.EventError
	}
	ev := agentbackend.Event{Type: kind, Error: errText, NumTurns: 1, DurationMs: dur.Milliseconds()}
	if usage != nil {
		ev.InputTokens = usage.InputTokens
		ev.OutputTokens = usage.OutputTokens
		ev.CacheReadTokens = usage.CacheReadTokens
	}
	return ev
}

// ResolvePermission is the agentbackend.Backend method; for niuniu-agent the
// host bridge is configured in Options, so this is a pass-through convenience.
func (b *Backend) ResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
	if b.opts.ResolvePermission == nil {
		return agentbackend.PermissionDecision{Cancelled: true}, nil
	}
	return b.opts.ResolvePermission(ctx, req)
}

// Abort cancels the in-flight turn with a `session/cancel` notification.
// Hard-kill fallback: if the agent has not exited within 5s of the cancel
// (e.g. wedged on a stalled gateway socket that swallows the ctx
// cancellation), the process is killed — readLoop EOF surfaces EventError,
// the turn settles and the session unlocks. A respawned agent replaces it on
// the next message, so killing is always safe.
func (b *Backend) Abort(ctx context.Context) error {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil
	}
	sessionID := b.session
	cmd := b.cmd
	b.mu.Unlock()
	if err := b.write(rpcRequest{JSONRPC: "2.0", Method: "session/cancel", Params: cancelParams{SessionID: sessionID}}); err != nil {
		return err
	}
	go func() {
		time.Sleep(5 * time.Second)
		b.mu.Lock()
		alive := b.started && !b.closed
		b.mu.Unlock()
		if alive && cmd.Process != nil {
			slog.Warn("niuniu-agent: still alive 5s after session/cancel — killing process",
				"session", sessionID, "pid", cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
	}()
	return nil
}

// Close EOFs stdin and kills the process if it does not exit promptly.
// Safe to call more than once.
func (b *Backend) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cmd := b.cmd
	stdin := b.stdin
	b.mu.Unlock()

	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "niuniu-agent backend closed"})
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil {
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	// Drop the job handle: kills any processes the agent left behind (its MCP
	// children), so nothing from this backend can hold the sidecar binaries.
	b.mu.Lock()
	jobClose := b.jobClose
	b.jobClose = nil
	b.mu.Unlock()
	if jobClose != nil {
		jobClose()
	}
	return nil
}

// request sends a JSON-RPC request and waits for its response.
func (b *Backend) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	b.mu.Lock()
	id := b.seq.Add(1)
	ch := make(chan rpcResponse, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	if err := b.write(rpcRequest{JSONRPC: "2.0", ID: jsonRawInt(id), Method: method, Params: params}); err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, err
	}

	var r rpcResponse
	// timeout > 0 arms a per-request deadline; negative means none — the
	// caller's ctx is the only bound (used for session/prompt, whose real
	// bound is the upstream turn-level inactivity watchdog).
	var timeoutC <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(nonZero(timeout, 30*time.Second))
		defer t.Stop()
		timeoutC = t.C
	}
	select {
	case r = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeoutC:
		return nil, fmt.Errorf("niuniu-agent %s timed out", method)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("niuniu-agent %s error: %s", method, r.Error.Message)
	}
	return r.Result, nil
}

// write marshals and writes one outbound JSON-RPC frame to stdin.
func (b *Backend) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.mu.Lock()
	stdin := b.stdin
	exited, exitErr := b.exited, b.exitErr
	b.mu.Unlock()
	if exited {
		return fmt.Errorf("niuniu-agent process has exited (%v); a fresh agent is started on the next message", exitErr)
	}
	if stdin == nil {
		return errors.New("niuniu-agent stdin not available")
	}
	_, err = stdin.Write(append(data, '\n'))
	return err
}

// emit pushes an event to the active turn stream, if any.
func (b *Backend) emit(ev agentbackend.Event) {
	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	if active != nil {
		active.send(ev)
	}
}

// finishTurn pushes a terminal event and closes the active channel + done
// signal. Safe to call when no turn is in flight.
func (b *Backend) finishTurn(ev agentbackend.Event) {
	b.mu.Lock()
	active := b.active
	done := b.activeDone
	b.active = nil
	b.activeDone = nil
	b.mu.Unlock()
	if active != nil {
		active.finish(ev)
	}
	if done != nil {
		close(done)
	}
}

func jsonRawInt(id int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%d", id))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonZero(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
