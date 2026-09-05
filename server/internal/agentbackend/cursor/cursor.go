package cursor

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

// DefaultCommand is Cursor's CLI binary. `agent` is the current primary name;
// `cursor-agent` remains a backwards-compatible alias, so either works.
const DefaultCommand = "agent"

// DefaultHandshakeTimeout bounds the initialize/authenticate/session-new
// round-trips after spawn.
const DefaultHandshakeTimeout = 30 * time.Second

// Options configures the Backend.
type Options struct {
	// Command is the Cursor CLI executable (default "agent").
	Command string
	// Args are extra CLI args appended after "acp".
	Args []string
	// WorkDir is the workspace directory the process runs in and the ACP session
	// is rooted at.
	WorkDir string
	// Env are additional environment variables (KEY=VALUE) for the child.
	Env []string

	// Model overrides Cursor's model selection, passed as `--model` on the root
	// command. Empty uses the account default.
	Model string

	// Mode is the ACP session mode: "" / "agent" (full tools), "plan" or "ask"
	// (both read-only). Maps from niuniu's permission mode.
	Mode string

	// McpServers are MCP servers cursor consumes as a client (niuniu-mcp), handed
	// over at session/new. This is why ACP is used instead of headless `-p`:
	// passing them here avoids the reported "-p does not inject MCP tools" defect.
	McpServers []McpServer

	// ResolvePermission bridges session/request_permission to the host UI. When
	// nil, requests are rejected (fail closed).
	ResolvePermission func(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error)

	// HandshakeTimeout bounds Start's handshake. Defaults to
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
}

// McpServer is an MCP server definition in the ACP `mcpServers` shape.
type McpServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// Backend is the agentbackend.Backend implementation for Cursor CLI over ACP.
// Create with [New]; safe for one in-flight Prompt at a time.
type Backend struct {
	opts Options

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started bool
	closed  bool
	session string

	// pending maps outbound request ids to the channel receiving their response.
	pending map[int64]chan rpcResponse

	// active is the current turn's event channel.
	active     chan agentbackend.Event
	activeDone chan struct{}

	// openToolCalls tracks tool calls opened by a `tool_call` update so a later
	// `tool_call_update` can be emitted as a tool_result carrying the right name.
	openToolCalls map[string]string

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
	return &Backend{
		opts:          opts,
		pending:       make(map[int64]chan rpcResponse),
		openToolCalls: make(map[string]string),
	}
}

// Start spawns `agent acp`, completes the ACP handshake (initialize →
// authenticate → session/new) and is idempotent.
func (b *Backend) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	if b.closed {
		b.mu.Unlock()
		return errors.New("cursor backend already closed")
	}
	b.mu.Unlock()

	// Model is a ROOT-command flag, so it precedes the `acp` subcommand
	// (`agent --model X acp`), matching the documented `agent --api-key X acp`
	// ordering. Putting it after `acp` is rejected by the CLI.
	args := []string{}
	if b.opts.Model != "" {
		args = append(args, "--model", b.opts.Model)
	}
	args = append(args, "acp")
	args = append(args, b.opts.Args...)

	cmd := exec.CommandContext(ctx, b.opts.Command, args...)
	cmd.Dir = b.opts.WorkDir
	cmd.Env = b.buildEnv()
	// stdout carries the protocol; the docs note logs may go to stderr.
	cmd.Stderr = nil

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("cursor stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cursor stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cursor start: %w", err)
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
	if err := b.authenticate(ctx); err != nil {
		_ = b.Close(context.Background())
		return err
	}
	if err := b.createSession(ctx); err != nil {
		_ = b.Close(context.Background())
		return err
	}
	return nil
}

// buildEnv composes the child environment.
//
// NOTE: a non-nil cmd.Env REPLACES the child's whole environment in Go, so this
// MUST seed from os.Environ() — otherwise the CLI launches without PATH/HOME and
// cannot find its own credentials (~/.local/share/cursor-agent) or resolve DNS.
func (b *Backend) buildEnv() []string {
	env := os.Environ()
	return append(env, b.opts.Env...)
}

func (b *Backend) initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: 1,
		ClientCapabilities: clientCaps{
			// niuniu does not proxy file ops for the agent: it runs inside the
			// worktree and uses its own tools.
			FS:       fsCaps{ReadTextFile: false, WriteTextFile: false},
			Terminal: false,
		},
		ClientInfo: clientInfo{Name: "niuniu", Version: "1.0"},
	}
	_, err := b.request(ctx, "initialize", params, b.opts.HandshakeTimeout)
	return err
}

// authenticate selects Cursor's advertised auth method. Credentials come from a
// prior `agent login` or CURSOR_API_KEY / CURSOR_AUTH_TOKEN in the env.
//
// A failure here is tolerated: when the CLI is already authenticated some builds
// reject a redundant `authenticate` call, and treating that as fatal would make
// a perfectly usable session unusable. A genuinely unauthenticated CLI fails at
// session/new instead, with a clearer message.
func (b *Backend) authenticate(ctx context.Context) error {
	if _, err := b.request(ctx, "authenticate", authenticateParams{MethodID: "cursor_login"}, b.opts.HandshakeTimeout); err != nil {
		slog.Info("cursor acp: authenticate declined; continuing (already logged in?)",
			"workDir", b.opts.WorkDir, "err", err)
	}
	return nil
}

func (b *Backend) createSession(ctx context.Context) error {
	params := newSessionParams{
		Cwd:        b.opts.WorkDir,
		McpServers: mcpServersParam(b.opts.McpServers),
	}
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

// SessionID exposes the ACP session id so the host can persist it for resume.
func (b *Backend) SessionID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session
}

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
	// Process exited / stdout closed → fail any in-flight turn so the host's
	// drain loop terminates instead of hanging forever.
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "cursor process exited"})
}

// dispatch routes one stdout frame. Three shapes matter:
//
//	{id, result|error}  → a response to one of our requests
//	{id, method}         → an agent→client REQUEST we must answer (permissions)
//	{method}             → a notification (session/update, cursor/* extensions)
//
// The middle case is what separates this from the goose variant, where
// request_permission is a notification. Treating a permission REQUEST as a
// notification leaves the agent waiting forever on a response that never comes —
// the docs are explicit: "If your client does not answer permission requests,
// tool execution can block."
func (b *Backend) dispatch(raw []byte) {
	var probe struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return
	}
	switch {
	case probe.ID != nil && probe.Method == "":
		b.handleResponse(raw, *probe.ID)
	case probe.ID != nil && probe.Method != "":
		b.handleAgentRequest(raw, probe.Method, *probe.ID)
	case probe.Method != "":
		b.handleNotification(raw, probe.Method)
	}
}

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

// handleAgentRequest answers an agent→client request. Permission requests are
// resolved on a goroutine so readLoop keeps draining stdout while the user
// decides (a blocking resolve would deadlock the stream).
func (b *Backend) handleAgentRequest(raw []byte, method string, id int64) {
	switch method {
	case "session/request_permission":
		var top struct {
			Params requestPermissionParams `json:"params"`
		}
		if err := json.Unmarshal(raw, &top); err != nil {
			b.respondError(id, "malformed request_permission params")
			return
		}
		go b.answerPermission(id, top.Params)
	case "cursor/ask_question", "cursor/create_plan":
		// Blocking Cursor extension methods. niuniu has no card UI for these yet;
		// skipping/rejecting keeps the turn moving instead of hanging the agent,
		// which is what would happen if we never responded.
		b.respondSkipped(id, method)
	default:
		// Unknown blocking request: answer rather than hang. An unrecognized
		// method left unanswered stalls the turn indefinitely.
		b.respondError(id, "unsupported method "+method)
	}
}

func (b *Backend) handleNotification(raw []byte, method string) {
	switch method {
	case "session/update":
		b.handleSessionUpdate(raw)
	case "cursor/update_todos", "cursor/task", "cursor/generate_image":
		// Fire-and-forget Cursor extensions; no response required and niuniu has
		// no dedicated rendering for them yet.
	}
}

// handleSessionUpdate maps one standard ACP update onto a neutral event.
func (b *Backend) handleSessionUpdate(raw []byte) {
	var top struct {
		Params sessionUpdateNotification `json:"params"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return
	}
	upd := top.Params.Update

	switch upd.SessionUpdate {
	case "agent_message_chunk":
		if upd.Content != nil && upd.Content.Text != "" {
			b.emit(agentbackend.Event{Type: agentbackend.EventText, Text: upd.Content.Text})
		}
	case "agent_thought_chunk":
		if upd.Content != nil && upd.Content.Text != "" {
			b.emit(agentbackend.Event{Type: agentbackend.EventThinking, Thinking: upd.Content.Text})
		}
	case "tool_call":
		name := firstNonEmpty(upd.Title, upd.Kind, "Tool")
		if upd.ToolCallID != "" {
			b.mu.Lock()
			b.openToolCalls[upd.ToolCallID] = name
			b.mu.Unlock()
		}
		b.emit(agentbackend.Event{
			Type:      agentbackend.EventToolUse,
			ToolName:  name,
			ToolUseID: upd.ToolCallID,
			ToolInput: string(compactJSON(upd.RawInput)),
		})
	case "tool_call_update":
		// Only a terminal status closes the call; ACP also sends in-progress
		// updates, and emitting a tool_result for those would paint a premature
		// result in the SPA.
		if !isTerminalToolStatus(upd.Status) {
			return
		}
		b.mu.Lock()
		name := b.openToolCalls[upd.ToolCallID]
		delete(b.openToolCalls, upd.ToolCallID)
		b.mu.Unlock()
		b.emit(agentbackend.Event{
			Type:      agentbackend.EventToolResult,
			ToolName:  name,
			ToolUseID: upd.ToolCallID,
			Text:      string(compactJSON(upd.RawOutput)),
			IsError:   upd.Status == "failed" || upd.Status == "error",
		})
	}
}

// isTerminalToolStatus reports whether a tool_call_update status ends the call.
func isTerminalToolStatus(status string) bool {
	switch status {
	case "completed", "failed", "error", "cancelled":
		return true
	}
	return false
}

// answerPermission bridges a permission request to the host and responds with
// the selected optionId.
func (b *Backend) answerPermission(id int64, params requestPermissionParams) {
	req := agentbackend.PermissionRequest{
		Method:  "confirm",
		Title:   "Cursor 请求权限",
		Message: "Cursor 需要批准一次工具调用",
	}
	if params.ToolCall != nil {
		req.ID = params.ToolCall.ToolCallID
		if params.ToolCall.Title != "" {
			req.Title = params.ToolCall.Title
		}
		req.Message = summarizeToolCall(params.ToolCall)
	}
	for _, o := range params.Options {
		req.Options = append(req.Options, o.Name)
	}

	decision := agentbackend.PermissionDecision{Cancelled: true} // fail closed
	if b.opts.ResolvePermission != nil {
		if d, err := b.opts.ResolvePermission(context.Background(), req); err != nil {
			slog.Warn("cursor acp: resolve permission failed; rejecting",
				"tool", req.Title, "err", err)
		} else {
			decision = d
		}
	} else {
		slog.Warn("cursor acp: no permission bridge wired; rejecting", "tool", req.Title)
	}

	optionID := chooseOption(params.Options, decision.Confirmed)
	if optionID == "" {
		// No option matched: cancel rather than guess, so the agent aborts the
		// call instead of receiving an id it never offered.
		b.respondResult(id, permissionResult{Outcome: permissionOutcome{Outcome: "cancelled"}})
		return
	}
	b.respondResult(id, permissionResult{Outcome: permissionOutcome{
		Outcome:  "selected",
		OptionID: optionID,
	}})
}

// chooseOption picks the option id matching the decision. It prefers the
// documented ids ("allow-once" / "reject-once"), then the spec's `kind`
// classifier, then a prefix match — so a renamed id degrades to a best-effort
// match rather than hanging the turn.
func chooseOption(options []permissionOption, allow bool) string {
	wantID, wantKind, wantPrefix := optRejectOnce, "reject_once", "reject"
	if allow {
		wantID, wantKind, wantPrefix = optAllowOnce, "allow_once", "allow"
	}
	for _, o := range options {
		if o.OptionID == wantID {
			return o.OptionID
		}
	}
	for _, o := range options {
		if o.Kind == wantKind {
			return o.OptionID
		}
	}
	for _, o := range options {
		if strings.HasPrefix(o.OptionID, wantPrefix) || strings.HasPrefix(o.Kind, wantPrefix) {
			return o.OptionID
		}
	}
	return ""
}

// Prompt sends one user turn and returns its event stream.
//
// Unlike the goose variant, the terminal signal is the `session/prompt` RESPONSE
// (stopReason), not a status field inside session/update — so the request is
// awaited on a goroutine and its stopReason closes the turn.
func (b *Backend) Prompt(ctx context.Context, req agentbackend.PromptRequest) (<-chan agentbackend.Event, error) {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil, errors.New("cursor backend not started")
	}
	if b.active != nil {
		b.mu.Unlock()
		return nil, errors.New("cursor backend already has an in-flight turn")
	}
	active := make(chan agentbackend.Event, 256)
	turnDone := make(chan struct{})
	b.active = active
	b.activeDone = turnDone
	sessionID := b.session
	b.mu.Unlock()

	blocks := []any{map[string]any{"type": "text", "text": req.Message}}
	go func() {
		// No timeout: a turn legitimately runs for many minutes. Cancellation
		// arrives via ctx (below) or Abort.
		resp, err := b.request(ctx, "session/prompt", promptParams{
			SessionID: sessionID,
			Prompt:    blocks,
		}, 0)
		if err != nil {
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: err.Error()})
			return
		}
		var result promptResult
		_ = json.Unmarshal(resp, &result)
		switch result.StopReason {
		case "", "end_turn", "max_tokens", "max_turn_requests":
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventDone})
		case "cancelled":
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventDone})
		case "refusal":
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "model refused the request"})
		default:
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventDone})
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
			_ = b.Abort(context.Background())
		case <-turnDone:
		}
	}()
	return active, nil
}

// Abort cancels the in-flight turn via `session/cancel` (a notification).
func (b *Backend) Abort(ctx context.Context) error {
	b.mu.Lock()
	sessionID := b.session
	inFlight := b.active != nil
	b.mu.Unlock()
	if !inFlight || sessionID == "" {
		return nil
	}
	return b.notify("session/cancel", cancelParams{SessionID: sessionID})
}

// ResolvePermission satisfies agentbackend.Backend. The bridge is supplied via
// Options.ResolvePermission and invoked from the readLoop, so this direct entry
// point simply delegates (or fails closed when unwired).
func (b *Backend) ResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
	if b.opts.ResolvePermission == nil {
		return agentbackend.PermissionDecision{Cancelled: true}, nil
	}
	return b.opts.ResolvePermission(ctx, req)
}

// Close tears down the process. Safe to call more than once.
func (b *Backend) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cmd, stdin := b.cmd, b.stdin
	b.cmd, b.stdin = nil, nil
	b.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "cursor backend closed"})
	return nil
}

// --- JSON-RPC plumbing ---

// request sends a JSON-RPC request and waits for its response. timeout<=0 waits
// until ctx is done.
func (b *Backend) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := b.seq.Add(1)
	ch := make(chan rpcResponse, 1)

	b.mu.Lock()
	if b.closed || b.stdin == nil {
		b.mu.Unlock()
		return nil, errors.New("cursor backend not running")
	}
	b.pending[id] = ch
	b.mu.Unlock()

	if err := b.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, err
	}

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s: connection closed", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %w", method, resp.Error)
		}
		return resp.Result, nil
	case <-timer:
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("%s: timed out after %s", method, timeout)
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (b *Backend) notify(method string, params any) error {
	return b.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// respondResult answers an agent→client request with a result.
func (b *Backend) respondResult(id int64, result any) {
	if err := b.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		slog.Warn("cursor acp: write response failed", "id", id, "err", err)
	}
}

// respondError answers an agent→client request with a JSON-RPC error, so the
// agent stops waiting.
func (b *Backend) respondError(id int64, message string) {
	if err := b.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32601, "message": message},
	}); err != nil {
		slog.Warn("cursor acp: write error response failed", "id", id, "err", err)
	}
}

// respondSkipped answers a blocking cursor/* extension with its documented
// "skipped" outcome so the agent proceeds without host UI support.
func (b *Backend) respondSkipped(id int64, method string) {
	b.respondResult(id, map[string]any{
		"outcome": map[string]any{
			"outcome": "skipped",
			"reason":  "niuniu has no UI for " + method,
		},
	})
}

func (b *Backend) write(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stdin == nil {
		return errors.New("cursor backend stdin closed")
	}
	_, err = b.stdin.Write(payload)
	return err
}

// emit delivers an event to the in-flight turn, dropping it when no turn is
// active (late frames after a turn settled).
func (b *Backend) emit(ev agentbackend.Event) {
	b.mu.Lock()
	ch := b.active
	b.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
		slog.Warn("cursor acp: event channel full; dropping", "type", ev.Type)
	}
}

// finishTurn settles the in-flight turn with a terminal event and closes its
// channel exactly once.
func (b *Backend) finishTurn(ev agentbackend.Event) {
	b.mu.Lock()
	ch := b.active
	done := b.activeDone
	b.active = nil
	b.activeDone = nil
	// A settled turn must not leak tool state into the next one.
	b.openToolCalls = make(map[string]string)
	b.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
	}
	close(ch)
	if done != nil {
		close(done)
	}
}

// --- helpers ---

// mcpServersParam renders MCP servers in the ACP `mcpServers` shape.
func mcpServersParam(servers []McpServer) []any {
	out := make([]any, 0, len(servers))
	for _, s := range servers {
		entry := map[string]any{
			"name":    s.Name,
			"command": s.Command,
		}
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			// ACP expresses env as a name/value array rather than an object.
			envArr := make([]any, 0, len(s.Env))
			for k, v := range s.Env {
				envArr = append(envArr, map[string]any{"name": k, "value": v})
			}
			entry["env"] = envArr
		}
		out = append(out, entry)
	}
	return out
}

// compactJSON returns raw when it is valid JSON, else "{}" — so a tool input or
// output is always a parseable JSON string for the SPA.
func compactJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 || !json.Valid(raw) {
		return []byte("{}")
	}
	return raw
}

// summarizeToolCall renders a short human-readable description of the tool call
// awaiting approval.
func summarizeToolCall(tc *permissionToolCall) string {
	var b strings.Builder
	if tc.Kind != "" {
		b.WriteString(tc.Kind)
	}
	if tc.Title != "" {
		if b.Len() > 0 {
			b.WriteString(": ")
		}
		b.WriteString(tc.Title)
	}
	if len(tc.RawInput) > 0 && json.Valid(tc.RawInput) {
		s := string(tc.RawInput)
		if len(s) > 300 {
			s = s[:300] + "…"
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	if b.Len() == 0 {
		return "Cursor 需要批准一次工具调用"
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
