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

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started bool
	closed  bool
	session string

	pending map[int64]chan rpcResponse

	active     chan agentbackend.Event
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
	cmd := exec.CommandContext(ctx, b.opts.Command, args...)
	cmd.Dir = b.opts.WorkDir
	// Non-nil cmd.Env replaces the child's environment entirely — seed from
	// os.Environ() so PATH/HOME survive.
	cmd.Env = append(os.Environ(), b.opts.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("niuniu-agent stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("niuniu-agent stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("niuniu-agent start: %w", err)
	}

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.started = true
	b.mu.Unlock()

	go b.readLoop(stdout)

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
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "niuniu-agent process exited"})
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

// Prompt sends a user turn and returns its event stream.
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
	active := make(chan agentbackend.Event, 256)
	turnDone := make(chan struct{})
	b.active = active
	b.activeDone = turnDone
	sessionID := b.session
	b.mu.Unlock()

	blocks := []promptBlock{{Type: "text", Text: req.Message}}
	started := time.Now()
	// niuniu-agent answers session/prompt only when the turn settles, which
	// for real coding tasks is minutes — the per-turn inactivity watchdog
	// upstream (turnCtx) is the real bound, so this is a generous backstop.
	result, err := b.request(ctx, "session/prompt", promptParams{SessionID: sessionID, Prompt: blocks}, 30*time.Minute)
	if err != nil {
		b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: err.Error()})
		return active, nil
	}

	// The prompt response IS the turn end for niuniu-agent (stopReason and
	// usage ride the response).
	var pr promptResult
	_ = json.Unmarshal(result, &pr) // unparseable usage → zero-token done event
	b.finishTurn(b.doneEvent("", pr.Usage, time.Since(started)))

	go func() {
		select {
		case <-ctx.Done():
			_ = b.Abort(context.Background())
		case <-turnDone:
		}
	}()
	return active, nil
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
func (b *Backend) Abort(ctx context.Context) error {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil
	}
	sessionID := b.session
	b.mu.Unlock()
	return b.write(rpcRequest{JSONRPC: "2.0", Method: "session/cancel", Params: cancelParams{SessionID: sessionID}})
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
	select {
	case r = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(nonZero(timeout, 30*time.Second)):
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
	b.mu.Unlock()
	if stdin == nil {
		return errors.New("niuniu-agent stdin not available")
	}
	_, err = stdin.Write(append(data, '\n'))
	return err
}

// emit pushes an event to the active turn channel, if any.
func (b *Backend) emit(ev agentbackend.Event) {
	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	if active != nil {
		active <- ev
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
		active <- ev
		close(active)
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
