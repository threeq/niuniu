package agentproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
)

// codexAppServerClient is a small JSONL client for
// `codex app-server --listen stdio://`. Unlike exec-server, app-server exposes
// Codex's thread/turn protocol. Agentproxy keeps one client per workspace
// session so a single app-server process can serve multiple turns.
type codexAppServerClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error
	nextID    atomic.Int64
	mu        sync.Mutex
	pending   map[int64]chan codexAppServerResponse
	events    chan codexAppServerNotification
	done      chan struct{}
}

// codexEventsBuffer is the notification buffer between the app-server reader
// and the session's event consumer. 512 (was 128): a long turn produces a
// dense delta/item stream while the consumer persists each tool_result to the
// database — the wider buffer rides out consumer hiccups; the REAL guarantee
// is that terminal notifications (turn/completed etc.) block instead of being
// shed (see readLoop's deliver).
const codexEventsBuffer = 512

type codexAppServerResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type codexAppServerNotification struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type codexAppServerThreadStartParams struct {
	Cwd                string   `json:"cwd"`
	Ephemeral          bool     `json:"ephemeral"`
	SessionStartSource string   `json:"sessionStartSource"`
	Model              string   `json:"model,omitempty"`
	Sandbox            string   `json:"sandbox,omitempty"`
	ApprovalPolicy     string   `json:"approvalPolicy,omitempty"`
	RuntimeRoots       []string `json:"runtimeWorkspaceRoots,omitempty"`
}

type codexAppServerThreadResumeParams struct {
	ThreadID       string   `json:"threadId"`
	Cwd            string   `json:"cwd,omitempty"`
	Model          string   `json:"model,omitempty"`
	Sandbox        string   `json:"sandbox,omitempty"`
	ApprovalPolicy string   `json:"approvalPolicy,omitempty"`
	RuntimeRoots   []string `json:"runtimeWorkspaceRoots,omitempty"`
	ExcludeTurns   bool     `json:"excludeTurns,omitempty"`
}

type codexAppServerThreadStartResponse struct {
	Thread struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
		Status    struct {
			Type string `json:"type"`
		} `json:"status"`
	} `json:"thread"`
	Model          string   `json:"model"`
	Cwd            string   `json:"cwd"`
	RuntimeRoots   []string `json:"runtimeWorkspaceRoots"`
	ApprovalPolicy string   `json:"approvalPolicy"`
}

type codexAppServerTurnStartParams struct {
	ThreadID       string           `json:"threadId"`
	Input          []map[string]any `json:"input"`
	Cwd            string           `json:"cwd,omitempty"`
	Model          string           `json:"model,omitempty"`
	ApprovalPolicy string           `json:"approvalPolicy,omitempty"`
	SandboxPolicy  map[string]any   `json:"sandboxPolicy,omitempty"`
	RuntimeRoots   []string         `json:"runtimeWorkspaceRoots,omitempty"`
}

type codexAppServerTurnStartResponse struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

func startCodexAppServerClient(ctx context.Context, command string, env []string) (*codexAppServerClient, error) {
	if command == "" {
		command = "codex"
	}
	cmdCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cmdCtx, command, "app-server", "--listen", "stdio://")
	if len(env) > 0 {
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("app-server start: %w", err)
	}

	c := &codexAppServerClient{
		cmd:     cmd,
		stdin:   stdin,
		cancel:  cancel,
		pending: make(map[int64]chan codexAppServerResponse),
		events:  make(chan codexAppServerNotification, codexEventsBuffer),
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *codexAppServerClient) initialize(ctx context.Context) error {
	var out struct {
		UserAgent      string `json:"userAgent"`
		CodexHome      string `json:"codexHome"`
		PlatformFamily string `json:"platformFamily"`
		PlatformOS     string `json:"platformOs"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "niuniu",
			"title":   "Niuniu",
			"version": "0.0.0",
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &out); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func (c *codexAppServerClient) StartThread(ctx context.Context, params codexAppServerThreadStartParams) (codexAppServerThreadStartResponse, error) {
	var out codexAppServerThreadStartResponse
	err := c.call(ctx, "thread/start", params, &out)
	return out, err
}

func (c *codexAppServerClient) ResumeThread(ctx context.Context, params codexAppServerThreadResumeParams) (codexAppServerThreadStartResponse, error) {
	var out codexAppServerThreadStartResponse
	err := c.call(ctx, "thread/resume", params, &out)
	return out, err
}

func (c *codexAppServerClient) StartTurn(ctx context.Context, params codexAppServerTurnStartParams) (codexAppServerTurnStartResponse, error) {
	var out codexAppServerTurnStartResponse
	err := c.call(ctx, "turn/start", params, &out)
	return out, err
}

func (c *codexAppServerClient) Events() <-chan codexAppServerNotification {
	return c.events
}

func (c *codexAppServerClient) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.stdin.Close()
	})
	<-c.done
	c.waitOnce.Do(func() {
		c.waitErr = c.cmd.Wait()
	})
	return c.waitErr
}

func (c *codexAppServerClient) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan codexAppServerResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.writeJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("app-server %s: %s", method, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("app-server %s response decode: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		return fmt.Errorf("app-server %s: process exited", method)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *codexAppServerClient) notify(method string, params any) error {
	return c.writeJSON(map[string]any{"method": method, "params": params})
}

func (c *codexAppServerClient) respond(id json.RawMessage, result any) error {
	return c.writeJSON(map[string]any{"id": id, "result": result})
}

func (c *codexAppServerClient) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("app-server write: %w", err)
	}
	return nil
}

// isTerminalCodexNotification reports whether a notification ENDS a turn (or
// the process). These are load-bearing for the session lifecycle — losing one
// strands the turn's waitForTurnComplete forever, which wedges the workspace
// in "running" and queues every later message. They must be delivered with a
// blocking send.
func isTerminalCodexNotification(method string) bool {
	switch method {
	case "turn/completed", "turn/failed", "turn/aborted", "process/exited", "error":
		return true
	}
	return false
}

func (c *codexAppServerClient) readLoop(r io.Reader) {
	defer close(c.done)
	defer close(c.events)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var envelope struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			continue
		}
		if len(envelope.ID) > 0 && envelope.Method != "" {
			c.deliver(codexAppServerNotification{ID: envelope.ID, Method: envelope.Method, Params: envelope.Params})
			continue
		}
		if len(envelope.ID) > 0 {
			var id int64
			if err := json.Unmarshal(envelope.ID, &id); err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- codexAppServerResponse{ID: id, Result: envelope.Result, Error: envelope.Error}
			}
			continue
		}
		if envelope.Method != "" {
			c.deliver(codexAppServerNotification{Method: envelope.Method, Params: envelope.Params})
		}
	}
}

// deliver hands one app-server notification to the session's event consumer.
// Terminal notifications use a BLOCKING send: dropping turn/completed strands
// the in-flight turn forever (the "Done never fires, everything queues"
// incident), so the reader backpressures instead. Lossy stream traffic
// (deltas) may still be shed under saturation — losing a delta only drops a
// few characters on screen.
func (c *codexAppServerClient) deliver(n codexAppServerNotification) {
	if isTerminalCodexNotification(n.Method) {
		select {
		case c.events <- n:
		case <-c.done:
		}
		return
	}
	select {
	case c.events <- n:
	default:
		// Shed under load. Deltas are cosmetic; anything else getting shed is
		// worth a warn so protocol drift shows up in the logs instead of
		// silently eating a turn's events.
		if n.Method != "item/agentMessage/delta" && n.Method != "agentMessage/delta" &&
			n.Method != "command/exec/outputDelta" && n.Method != "process/outputDelta" &&
			n.Method != "exec_command_output_delta" && n.Method != "command_output_delta" &&
			n.Method != "response.output_text.delta" {
			slog.Warn("codex app-server: events channel saturated — notification shed",
				"method", n.Method)
		}
	}
}
