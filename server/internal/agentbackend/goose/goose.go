// Package goose implements agentbackend.Backend for Block's Goose agent,
// driven over the Agent Client Protocol (ACP) on stdio (`goose acp`).
//
// ACP is JSON-RPC 2.0 over newline-delimited stdio: the backend performs the
// `initialize` + `session/new` handshake, delivers each user turn as a
// `session/prompt` request, streams normalized agentbackend.Event frames from
// `session/update` notifications, and answers runtime→host
// `session/request_permission` notifications by calling the host's
// ResolvePermission bridge and writing a `session/reply` request.
//
// Transport decision: ACP-over-stdio mirrors the omp integration (NDJSON RPC
// on stdio, testable against a fake subprocess), exposes Goose's rich MCP
// extension surface (the killer feature), and routes permission through the
// ACP `session/request_permission` flow the integration issue calls out.
//
// Capability trimming: niuniu scopes the agent to a single workspace's
// execution + tools. It does not pass a session name (auto-title off) and
// relies on Goose's own session-file persistence; cross-workspace
// orchestration/kanban/memory remain niuniu's job.
package goose

import (
	"bufio"
	"bytes"
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

// DefaultCommand is the executable invoked when Options.Command is empty. The
// ACP agent runs as `goose acp` (the goosed HTTP daemon is the sibling
// `goose serve` transport).
const DefaultCommand = "goose"

// DefaultHandshakeTimeout bounds how long Start waits for the `initialize`
// response after spawning the process.
const DefaultHandshakeTimeout = 15 * time.Second

// Options configures the GooseBackend.
type Options struct {
	// Command is the goose executable (default "goose"). For tests this points
	// at the compiled testdata/fakegoose subprocess.
	Command string
	// Args are extra CLI args appended after "acp".
	Args []string
	// WorkDir is the workspace directory the process runs in and the session
	// is rooted at.
	WorkDir string
	// Env are additional environment variables (KEY=VALUE) for the child.
	Env []string

	// Provider and Model override Goose's model selection (GOOSE_PROVIDER /
	// GOOSE_MODEL). Empty lets Goose use its configured default. Goose has no
	// native domestic models; users wire OpenRouter/Ollama/compatible endpoints.
	Provider string
	Model    string

	// ResolvePermission bridges ACP session/request_permission notifications to
	// the host UI. When nil, permission requests are auto-denied (fail closed).
	// The host MUST return a decision; the backend writes it back as a reply.
	ResolvePermission func(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error)

	// HandshakeTimeout bounds Start's initialize/session/new wait. Defaults to
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
}

// Backend is the agentbackend.Backend implementation for Goose. Create with
// [New]; safe for one in-flight Prompt at a time.
type Backend struct {
	opts Options

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	started  bool
	closed   bool
	session  string // the ACP session id (set by Start)
	turnID   string // the in-flight turn id (set by Prompt)

	// pending maps outbound request ids to the channel that receives their
	// response. Requests are sent under mu; readLoop delivers into pending.
	pending map[int64]chan rpcResponse

	// active is the current turn's event channel (mirrors the omp backend).
	active     chan agentbackend.Event
	activeDone chan struct{}

	// lastUsage is the most recent usage_update this turn; folded into the
	// terminal EventDone for cost accounting (goose streams usage during the
	// turn rather than attaching it to a single end frame).
	lastCostUSD  float64
	lastInTokens int
	lastOutTokens int

	seq atomic.Int64 // request id counter
}

// New creates a GooseBackend from options.
func New(opts Options) *Backend {
	if opts.Command == "" {
		opts.Command = DefaultCommand
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = DefaultHandshakeTimeout
	}
	return &Backend{opts: opts, pending: make(map[int64]chan rpcResponse)}
}

// Start spawns `goose acp`, completes the initialize handshake, and creates a
// session. Idempotent.
func (b *Backend) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	if b.closed {
		b.mu.Unlock()
		return errors.New("goose backend already closed")
	}
	b.mu.Unlock()

	args := append([]string{"acp"}, b.opts.Args...)
	cmd := exec.CommandContext(ctx, b.opts.Command, args...)
	cmd.Dir = b.opts.WorkDir
	cmd.Env = b.buildEnv()
	cmd.Stderr = nil // goose writes diagnostics to stderr; stdout carries the protocol

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("goose stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("goose stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("goose start: %w", err)
	}

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.started = true
	b.mu.Unlock()

	go b.readLoop(stdout)

	// Handshake: initialize, then session/new. Both are plain request/response
	// round-trips; a response to either is the "ready" signal.
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

// buildEnv composes the child environment: the host env (required for Goose to
// find its config / credentials / model keys), explicit Env vars, and model
// selection.
//
// NOTE: a non-nil cmd.Env REPLACES the child's entire environment in Go, so this
// MUST seed from os.Environ() — starting from an empty slice would launch goose
// without PATH/HOME and hang on startup.
func (b *Backend) buildEnv() []string {
	env := os.Environ()
	env = append(env, b.opts.Env...)
	if b.opts.Provider != "" {
		env = append(env, "GOOSE_PROVIDER="+b.opts.Provider)
	}
	if b.opts.Model != "" {
		env = append(env, "GOOSE_MODEL="+b.opts.Model)
	}
	return env
}

// initialize performs the ACP `initialize` handshake.
func (b *Backend) initialize(ctx context.Context) error {
	params := initializeParams{ProtocolVersion: "v1"}
	params.ClientCapabilities = map[string]any{}
	params.ClientInfo.Name = "niuniu"
	params.ClientInfo.Version = "1.0"
	_, err := b.request(ctx, "initialize", params, b.opts.HandshakeTimeout)
	return err
}

// createSession performs `session/new`, rooted at the workspace dir.
func (b *Backend) createSession(ctx context.Context) error {
	params := newSessionParams{Cwd: b.opts.WorkDir, McpServers: []any{}}
	resp, err := b.request(ctx, "session/new", params, b.opts.HandshakeTimeout)
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

// readLoop scans stdout JSON-RPC and dispatches responses vs notifications.
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
	// stdin closed / process exited → fail any in-flight turn.
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "goose process exited"})
}

// dispatch routes one stdout object: a response (has id, no method) resolves a
// pending request; a notification (has method, no id) is handled as a
// session/update or session/request_permission.
func (b *Backend) dispatch(raw []byte) {
	var probe struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return
	}
	if probe.ID != 0 && probe.Method == "" {
		b.handleResponse(raw, probe.ID)
		return
	}
	if probe.Method != "" && probe.ID == 0 {
		b.handleNotification(raw, probe.Method)
	}
	// A response to a notification-less request or a malformed frame: ignore.
}

// handleResponse delivers an agent response to the pending request with that id.
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

// handleNotification routes an agent→host notification.
func (b *Backend) handleNotification(raw []byte, method string) {
	switch method {
	case "session/update":
		b.handleSessionUpdate(raw)
	case "session/request_permission":
		b.handleRequestPermission(raw)
	case "config/update", "session/retained", "session/set_needs_attention",
		"session/continue", "agent/availability_changed":
		// Non-critical / out-of-scope: ignored. session/continue would ask the
		// host to send another prompt; niuniu drives turns itself.
	default:
		// Unknown notification: ignore.
	}
}

// handleSessionUpdate processes one `session/update` notification: emits text /
// tool / cost events and ends the turn on a terminal status.
func (b *Backend) handleSessionUpdate(raw []byte) {
	// ACP frames `session/update` as params.update.<SessionUpdate>. Also tolerate
	// the wrapped `params.update.sessionUpdate` shape some builds emit.
	var notif struct {
		Params struct {
			Update sessionUpdate `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &notif); err != nil {
		return
	}
	upd := notif.Params.Update
	if upd.SessionID == "" {
		var wrapped struct {
			Params struct {
				Update struct {
					SessionUpdate sessionUpdate `json:"sessionUpdate"`
				} `json:"update"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &wrapped); err == nil {
			upd = wrapped.Params.Update.SessionUpdate
		}
	}
	for _, e := range upd.Events {
		b.handleSessionEvent(e, upd)
	}
	// A terminal status on the update terminates the turn even without a
	// status_update event.
	b.maybeEndOnStatus(upd.Status)
}

// maybeEndOnStatus ends the turn when the session status is terminal.
func (b *Backend) maybeEndOnStatus(status string) {
	switch status {
	case "completed":
		b.finishTurn(b.doneEvent(agentbackend.EventDone, ""))
	case "error":
		b.finishTurn(b.doneEvent(agentbackend.EventError, "goose turn failed"))
	case "cancelled":
		b.finishTurn(b.doneEvent(agentbackend.EventError, "goose turn cancelled"))
	}
}

// handleSessionEvent maps one session/update event onto agentbackend.Event.
func (b *Backend) handleSessionEvent(raw []byte, upd sessionUpdate) {
	var ev sessionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "content_update":
		if ev.Content != nil && ev.Content.Type == "text" {
			b.emit(agentbackend.Event{Type: agentbackend.EventText, Text: ev.Content.Text})
		}
	case "tool_call":
		if ev.ToolCall != nil {
			b.emit(agentbackend.Event{
				Type:      agentbackend.EventToolUse,
				ToolName:  ev.ToolCall.Name,
				ToolInput: string(compactJSON(ev.ToolCall.Input)),
				ToolUseID: ev.ToolCall.ID,
			})
		}
	case "tool_call_result":
		if ev.ToolCallResult != nil {
			b.emit(agentbackend.Event{
				Type:      agentbackend.EventToolResult,
				ToolUseID: ev.ToolCallResult.ToolCallID,
				Text:      joinTextContent(ev.ToolCallResult.Content),
				IsError:   ev.ToolCallResult.IsError,
			})
		}
	case "usage_update":
		if ev.Usage != nil {
			b.mu.Lock()
			b.lastCostUSD = ev.Usage.CostUsd
			b.lastInTokens = ev.Usage.InputTokens
			b.lastOutTokens = ev.Usage.OutputTokens
			b.mu.Unlock()
		}
	case "permission_request":
		if ev.RequestPermission != nil {
			b.answerPermission(ev.RequestPermission, upd.SessionID, upd.TurnID)
		}
	case "status_update":
		switch ev.Status {
		case "completed":
			b.finishTurn(b.doneEvent(agentbackend.EventDone, ""))
		case "error":
			b.finishTurn(b.doneEvent(agentbackend.EventError, firstNonEmpty(ev.StopReason, "goose turn failed")))
		case "cancelled":
			b.finishTurn(b.doneEvent(agentbackend.EventError, "goose turn cancelled"))
		}
	}
}

// doneEvent builds a terminal event carrying the last observed usage.
func (b *Backend) doneEvent(kind agentbackend.EventType, errText string) agentbackend.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	ev := agentbackend.Event{Type: kind, Error: errText}
	if kind == agentbackend.EventDone {
		ev.CostUSD = b.lastCostUSD
		ev.NumTurns = 1
		ev.InputTokens = b.lastInTokens
		ev.OutputTokens = b.lastOutTokens
	}
	return ev
}

// handleRequestPermission processes a `session/request_permission` notification
// and writes a `session/reply` request with the host's decision.
func (b *Backend) handleRequestPermission(raw []byte) {
	var params requestPermissionParams
	var top struct {
		Params requestPermissionParams `json:"params"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return
	}
	params = top.Params
	req := agentbackend.PermissionRequest{
		ID:      params.TurnID,
		Method:  "confirm",
		Title:   "Goose 请求权限",
		Message: summarizeZones(params.InteractionZones),
	}
	if len(params.InteractionZones) > 0 {
		z := params.InteractionZones[0]
		if z.Title != "" {
			req.Title = z.Title
		}
		if z.Message != "" {
			req.Message = z.Message
		}
		if z.Description != "" {
			req.Message = z.Description
		}
		if z.Method != "" {
			req.Method = z.Method
		}
	}
	go b.writeReply(params.SessionID, params.TurnID, req)
}

// answerPermission handles an in-update permission_request event.
func (b *Backend) answerPermission(rp *requestPermission, sessionID, turnID string) {
	req := agentbackend.PermissionRequest{
		ID:      rp.ID,
		Method:  firstNonEmpty(rp.Method, "confirm"),
		Title:   firstNonEmpty(rp.Title, "Goose 请求权限"),
		Message: firstNonEmpty(rp.Description, rp.Message, summarizeZones(rp.InteractionZones)),
	}
	go b.writeReply(sessionID, turnID, req)
}

// writeReply resolves a permission request via the host bridge and sends the
// `session/reply` request. Runs in a goroutine so readLoop keeps streaming
// while the user decides.
func (b *Backend) writeReply(sessionID, turnID string, req agentbackend.PermissionRequest) {
	var decision agentbackend.PermissionDecision
	if b.opts.ResolvePermission == nil {
		decision = agentbackend.PermissionDecision{Cancelled: true} // fail closed
	} else {
		d, err := b.opts.ResolvePermission(context.Background(), req)
		if err != nil {
			decision = agentbackend.PermissionDecision{Cancelled: true}
		} else {
			decision = d
		}
	}

	state := "deny"
	if decision.Confirmed {
		state = "allow"
	}
	params := replyParams{
		SessionID: sessionID,
		TurnID:    turnID,
		InteractionZones: []zoneReply{{
			Type:  "tools",
			State: state,
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = b.request(ctx, "session/reply", params, 10*time.Second)
}

// Prompt sends a user turn and returns its event stream.
func (b *Backend) Prompt(ctx context.Context, req agentbackend.PromptRequest) (<-chan agentbackend.Event, error) {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil, errors.New("goose backend not started")
	}
	if b.active != nil {
		b.mu.Unlock()
		return nil, errors.New("goose backend already has an in-flight turn")
	}
	active := make(chan agentbackend.Event, 256)
	turnDone := make(chan struct{})
	b.active = active
	b.activeDone = turnDone
	sessionID := b.session
	b.mu.Unlock()

	blocks := []any{map[string]any{"type": "text", "text": req.Message}}
	resp, err := b.request(ctx, "session/prompt", promptParams{SessionID: sessionID, Prompt: blocks}, 0)
	if err != nil {
		// Mirror the omp contract: a rejected prompt surfaces as an EventError on
		// the channel (nil return error), so the caller's drain loop sees it.
		b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: err.Error()})
		return active, nil
	}
	var result struct {
		TurnID string `json:"turnId"`
	}
	_ = json.Unmarshal(resp, &result)
	b.mu.Lock()
	b.turnID = result.TurnID
	b.mu.Unlock()

	// Close the stream if ctx is cancelled mid-turn via ACP cancel.
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Abort(context.Background())
		case <-turnDone:
		}
	}()
	return active, nil
}

// Abort cancels the in-flight turn with a `session/cancel` notification.
func (b *Backend) Abort(ctx context.Context) error {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil
	}
	sessionID := b.session
	turnID := b.turnID
	b.mu.Unlock()
	return b.write(rpcRequest{JSONRPC: "2.0", Method: "session/cancel", Params: cancelParams{SessionID: sessionID, TurnID: turnID}})
}

// ResolvePermission is the agentbackend.Backend method; for goose the host
// bridge is configured in Options, so this is a pass-through convenience.
func (b *Backend) ResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
	if b.opts.ResolvePermission == nil {
		return agentbackend.PermissionDecision{Cancelled: true}, nil
	}
	return b.opts.ResolvePermission(ctx, req)
}

// Close sends `session/close`, then EOF on stdin and kills the process if it
// does not exit promptly. Safe to call more than once.
func (b *Backend) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cmd := b.cmd
	stdin := b.stdin
	sessionID := b.session
	b.mu.Unlock()

	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "goose backend closed"})
	if sessionID != "" {
		_ = b.write(rpcRequest{JSONRPC: "2.0", Method: "session/close", Params: closeParams{SessionID: sessionID}})
	}
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

// request sends a JSON-RPC request and waits for its response (or a timeout).
func (b *Backend) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	b.mu.Lock()
	id := b.seq.Add(1)
	ch := make(chan rpcResponse, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	if err := b.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
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
		return nil, fmt.Errorf("goose %s timed out", method)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("goose %s error: %s", method, r.Error.Message)
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
		return errors.New("goose stdin not available")
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

// compactJSON returns a compact string form of a JSON value, or "" for nil.
func compactJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

// joinTextContent concatenates the text blocks of a tool result's content array.
func joinTextContent(content []json.RawMessage) string {
	var b strings.Builder
	for _, c := range content {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(c, &block) == nil && block.Type == "text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// summarizeZones renders the interaction zones into a short human message.
func summarizeZones(zones []interactionZone) string {
	if len(zones) == 0 {
		return "Goose 请求权限"
	}
	var parts []string
	for _, z := range zones {
		if z.Type != "" {
			parts = append(parts, z.Type)
		}
		if len(z.ToolCallIds) > 0 {
			parts = append(parts, strings.Join(z.ToolCallIds, ","))
		}
	}
	return firstNonEmpty(strings.Join(parts, " "), "Goose 请求权限")
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