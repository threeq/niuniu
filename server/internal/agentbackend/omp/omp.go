// Package omp implements agentbackend.Backend for oh-my-pi (omp), driven over
// stdio with the NDJSON RPC protocol (`omp --mode rpc`), a.k.a. the "rpc-ui"
// mode referenced in the integration issue.
//
// The backend owns one omp process: it spawns it, completes the `ready`
// handshake, sends `prompt` commands, streams normalized agentbackend.Event
// frames back, and answers runtime→host `extension_ui_request` frames by
// calling the host's ResolvePermission bridge and writing `extension_ui_response`.
//
// Capability trimming (memory / collab / title / advisor): omp's RPC host
// defaults already disable automatic session-title generation and cover
// memory/advisor as host-defaulted settings. niuniu additionally leaves
// PI_RPC_EMIT_TITLE unset (no title events) and does not expose collab or
// host-URI host hooks, so the agent stays scoped to a single workspace's
// execution + tools. See [Options] for the full env surface.
package omp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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

// DefaultCommand is the binary invoked when Options.Command is empty.
const DefaultCommand = "omp"

// DefaultHandshakeTimeout bounds how long Start waits for the `ready` frame
// after spawning the process.
const DefaultHandshakeTimeout = 15 * time.Second

// Options configures the OMPBackend.
type Options struct {
	// Command is the omp executable. Defaults to "omp".
	Command string
	// Args are extra CLI args appended after "--mode rpc".
	Args []string
	// WorkDir is the workspace directory the process runs in.
	WorkDir string
	// Env are additional environment variables (KEY=VALUE) for the child.
	Env []string

	// Provider and Model select the omp model (e.g. provider "glm",
	// model "glm-4.6"). Empty lets omp use its configured default.
	Provider string
	Model    string

	// ResolvePermission bridges runtime→host extension_ui_request frames to the
	// host UI. When nil, extension_ui_request frames are auto-cancelled (fail
	// closed). The host MUST return a decision; the backend writes it back.
	ResolvePermission func(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error)

	// HandshakeTimeout bounds Start's ready-frame wait. Defaults to
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
}

// Backend is the agentbackend.Backend implementation for omp. Create with
// [New]; safe for one in-flight Prompt at a time.
type Backend struct {
	opts Options

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started bool
	closed  bool

	// active is the current turn's event channel. readLoop pushes to it and
	// finishes the turn (closing both active and activeDone) on completion.
	// nil when no turn is in flight.
	active     chan agentbackend.Event
	activeDone chan struct{}

	seq      atomic.Int64  // command id counter
	ready    chan struct{} // closed when the ready frame is seen
	startErr error
}

// New creates an OMPBackend from options.
func New(opts Options) *Backend {
	if opts.Command == "" {
		opts.Command = DefaultCommand
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = DefaultHandshakeTimeout
	}
	return &Backend{opts: opts, ready: make(chan struct{})}
}

// Start spawns `omp --mode rpc` and waits for the ready handshake. Idempotent.
func (b *Backend) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	if b.closed {
		b.mu.Unlock()
		return errors.New("omp backend already closed")
	}
	b.mu.Unlock()

	args := append([]string{"--mode", "rpc"}, b.opts.Args...)
	cmd := exec.CommandContext(ctx, b.opts.Command, args...)
	cmd.Dir = b.opts.WorkDir
	cmd.Env = b.buildEnv()
	cmd.Stderr = nil // omp writes diagnostics to stderr; we ignore it (stdout is the protocol)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("omp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("omp stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("omp start: %w", err)
	}

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.started = true
	b.mu.Unlock()

	go b.readLoop(stdout)

	// Wait for the ready frame (or startErr).
	select {
	case <-b.ready:
		return nil
	case <-time.After(b.opts.HandshakeTimeout):
		_ = b.Close(context.Background())
		return fmt.Errorf("omp: timed out waiting for ready frame (is %q installed and configured?)", b.opts.Command)
	case <-ctx.Done():
		_ = b.Close(context.Background())
		return ctx.Err()
	}
}

// buildEnv composes the child environment: the host environment (required for
// omp to locate its config / credentials / toolchain), explicit Env vars, model
// selection, and capability-trimming defaults.
//
// NOTE: a non-nil cmd.Env REPLACES the child's entire environment in Go, so this
// MUST seed from os.Environ() — starting from an empty slice would launch omp
// without PATH/HOME and hang on startup.
func (b *Backend) buildEnv() []string {
	env := os.Environ()
	env = append(env, b.opts.Env...)
	if b.opts.Provider != "" {
		env = append(env, "OMP_PROVIDER="+b.opts.Provider)
	}
	if b.opts.Model != "" {
		env = append(env, "OMP_MODEL="+b.opts.Model)
	}
	// Trimming: keep automatic title emission off (RPC suppresses it unless
	// PI_RPC_EMIT_TITLE=1) and leave collab/memory to omp's RPC host defaults.
	// No explicit collab/memory opt-ins are injected here.
	return env
}

// readLoop scans stdout NDJSON and dispatches each frame.
func (b *Backend) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		b.dispatch([]byte(line))
	}
	// stdin closed / process exited → fail any in-flight turn.
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "omp process exited"})
}

// dispatch routes one stdout JSON object by its "type" field.
func (b *Backend) dispatch(raw []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return
	}
	switch head.Type {
	case "ready":
		select {
		case <-b.ready:
		default:
			close(b.ready)
		}
	case "response":
		b.handleResponse(raw)
	case "extension_ui_request":
		b.handleExtensionUI(raw)
	case "agent_start", "turn_start", "message_start", "message_update", "message_end",
		"tool_execution_start", "tool_execution_update", "tool_execution_end",
		"agent_end", "turn_end", "auto_compaction_start", "auto_compaction_end",
		"auto_retry_start", "auto_retry_end", "model_changed", "thinking_level_changed",
		"todo_reminder", "notice", "goal_updated", "retry_fallback_applied", "retry_fallback_succeeded":
		b.handleSessionEvent(raw)
	case "prompt_result", "extension_error", "available_commands_update",
		"rpc_chunk", "subagent_lifecycle", "subagent_progress", "subagent_event",
		"host_tool_call", "host_tool_cancel", "host_uri_request", "host_uri_cancel",
		"command_output", "session_info_update", "config_update":
		// Non-critical / out-of-scope frames: ignored. host_tool_* and
		// host_uri_* are not exercised because niuniu exposes its own MCP
		// surface rather than omp host tools.
	default:
		// Unknown frame: ignore.
	}
}

// handleResponse processes a `response` frame, failing the active prompt on error.
func (b *Backend) handleResponse(raw []byte) {
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	if resp.Command != "prompt" {
		return
	}
	if resp.Success {
		return
	}
	// Prompt scheduling failed → end the active turn with an error.
	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "omp prompt failed: " + resp.Error})
}

// handleSessionEvent maps an AgentSessionEvent onto agentbackend.Event and
// pushes it to the active turn channel.
func (b *Backend) handleSessionEvent(raw []byte) {
	var ev sessionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "message_update":
		if ev.AssistantMessageEvent != nil {
			delta := ev.AssistantMessageEvent
			switch delta.Type {
			case "text_delta":
				b.emit(agentbackend.Event{Type: agentbackend.EventText, Text: delta.Delta})
			case "thinking_delta":
				b.emit(agentbackend.Event{Type: agentbackend.EventThinking, Thinking: delta.Delta})
			}
		}
	case "tool_execution_start":
		if ev.ToolExecution != nil {
			b.emit(agentbackend.Event{
				Type:      agentbackend.EventToolUse,
				ToolName:  ev.ToolExecution.Name,
				ToolInput: string(compactJSON(ev.ToolExecution.Input)),
				ToolUseID: ev.ToolExecution.ID,
			})
		}
	case "tool_execution_end":
		if ev.ToolExecution != nil {
			b.emit(agentbackend.Event{
				Type:      agentbackend.EventToolResult,
				ToolUseID: ev.ToolExecution.ID,
				Text:      string(compactJSON(ev.ToolExecution.Output)),
				IsError:   ev.ToolExecution.IsError,
			})
		}
	case "agent_end":
		terminal := ev.IsTerminal == nil || *ev.IsTerminal
		if !terminal {
			// Async/maintenance work scheduled; the turn is not yet over.
			return
		}
		done := agentbackend.Event{Type: agentbackend.EventDone}
		if ev.Error != "" {
			done = agentbackend.Event{Type: agentbackend.EventError, Error: ev.Error}
		}
		if ev.Telemetry != nil {
			if ev.Telemetry.CostUSD != nil {
				done.CostUSD = *ev.Telemetry.CostUSD
			}
			if ev.Telemetry.NumTurns != nil {
				done.NumTurns = *ev.Telemetry.NumTurns
			}
			if ev.Telemetry.DurationMS != nil {
				done.DurationMs = *ev.Telemetry.DurationMS
			}
			if ev.Telemetry.InputTokens != nil {
				done.InputTokens = *ev.Telemetry.InputTokens
			}
			if ev.Telemetry.OutputTokens != nil {
				done.OutputTokens = *ev.Telemetry.OutputTokens
			}
			if ev.Telemetry.CachedTokens != nil {
				done.CacheReadTokens = *ev.Telemetry.CachedTokens
			}
		}
		b.finishTurn(done)
	}
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

// handleExtensionUI answers an extension_ui_request via the host bridge. Runs
// in a goroutine so readLoop keeps streaming while the user decides.
func (b *Backend) handleExtensionUI(raw []byte) {
	var req extensionUIRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return
	}
	req2 := agentbackend.PermissionRequest{
		ID:        req.ID,
		Method:    req.Method,
		Title:     req.Title,
		Message:   req.Message,
		Options:   req.Options,
		TimeoutMS: req.Timeout,
	}
	go b.answerExtensionUI(req2)
}

func (b *Backend) answerExtensionUI(req agentbackend.PermissionRequest) {
	var decision agentbackend.PermissionDecision
	if b.opts.ResolvePermission == nil {
		// Fail closed: no host bridge → cancel the request.
		decision = agentbackend.PermissionDecision{Cancelled: true}
	} else {
		d, err := b.opts.ResolvePermission(context.Background(), req)
		if err != nil {
			decision = agentbackend.PermissionDecision{Cancelled: true}
		} else {
			decision = d
		}
	}
	resp := extensionUIResponse{Type: "extension_ui_response", ID: req.ID}
	switch {
	case decision.Cancelled:
		resp.Cancelled = true
		resp.TimedOut = decision.TimedOut
	case req.Method == "confirm":
		resp.Confirmed = decision.Confirmed
	default:
		resp.Value = decision.Value
	}
	b.write(resp)
}

// Prompt sends a user turn and returns its event stream.
func (b *Backend) Prompt(ctx context.Context, req agentbackend.PromptRequest) (<-chan agentbackend.Event, error) {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil, errors.New("omp backend not started")
	}
	if b.active != nil {
		b.mu.Unlock()
		return nil, errors.New("omp backend already has an in-flight turn")
	}
	active := make(chan agentbackend.Event, 256)
	turnDone := make(chan struct{})
	b.active = active
	b.activeDone = turnDone
	b.mu.Unlock()

	id := fmt.Sprintf("req_%d", b.seq.Add(1))
	cmd := promptCommand{
		ID:                id,
		Type:              "prompt",
		Message:           req.Message,
		StreamingBehavior: req.StreamingBehavior,
	}
	for _, img := range req.Images {
		if img.Data != "" {
			cmd.Images = append(cmd.Images, img.Data)
		}
	}
	if err := b.write(cmd); err != nil {
		b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: err.Error()})
		return active, err
	}
	// Close the stream if ctx is cancelled mid-turn.
	go func() {
		select {
		case <-ctx.Done():
			b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "prompt cancelled"})
		case <-turnDone:
		}
	}()
	return active, nil
}

// Abort cancels the in-flight turn.
func (b *Backend) Abort(ctx context.Context) error {
	b.mu.Lock()
	if !b.started || b.closed {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	return b.write(abortCommand{Type: "abort"})
}

// ResolvePermission is the agentbackend.Backend method; for omp the host bridge
// is configured in Options, so this is a pass-through convenience for callers
// that want to drive it explicitly.
func (b *Backend) ResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
	if b.opts.ResolvePermission == nil {
		return agentbackend.PermissionDecision{Cancelled: true}, nil
	}
	return b.opts.ResolvePermission(ctx, req)
}

// Close sends EOF on stdin (omp exits 0 on stdin close) and kills the process
// if it does not exit promptly.
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

	b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "omp backend closed"})
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

// write marshals and writes one inbound NDJSON frame to stdin.
func (b *Backend) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return errors.New("omp stdin not available")
	}
	_, err = stdin.Write(append(data, '\n'))
	return err
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

// base64Decode is retained for v2 chunk reassembly (not negotiated here).
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
