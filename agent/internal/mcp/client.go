package mcp

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
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// mcpProtocolVersion is the MCP revision this client offers; the server's
// negotiated value from the initialize response is accepted as-is (we rely
// only on the tools subset both revisions share).
const mcpProtocolVersion = "2025-06-18"

// Client is one connected stdio MCP server.
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	closed bool

	mu      sync.Mutex
	seq     int64
	pending map[int64]chan rpcMsg
	defs    []model.ToolDef
}

// rpcMsg is the subset of a JSON-RPC response the client cares about.
type rpcMsg struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Connect spawns the server process and performs the MCP handshake
// (initialize → notifications/initialized → tools/list). The caller's ctx
// bounds the handshake; afterwards calls are bounded by their own ctx.
func Connect(ctx context.Context, name string, cfg ServerConfig) (*Client, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), envSlice(cfg.Env)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdout pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: start %s: %w", name, cfg.Command, err)
	}

	c := &Client{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[int64]chan rpcMsg),
	}
	go c.readLoop(stdout)

	var initRes struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "niuniu-agent", "version": "0.1.0"},
	}, &initRes); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: initialize: %w", name, err)
	}
	// Notification: no response expected.
	_ = c.notify("notifications/initialized")

	var listRes struct {
		Tools []model.ToolDef `json:"tools"`
	}
	if _, err := c.request(ctx, "tools/list", map[string]any{}, &listRes); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: tools/list: %w", name, err)
	}
	c.mu.Lock()
	c.defs = listRes.Tools
	c.mu.Unlock()
	return c, nil
}

// ToolDefs returns the server's advertised tools (server-local names).
func (c *Client) ToolDefs() []model.ToolDef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.defs
}

// CallTool invokes a server tool by its server-local name and returns the
// joined text content. A server-side isError result becomes a Go error
// carrying that text, so the loop backfills it as a failed tool_result.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if args == nil {
		args = json.RawMessage(`{}`)
	}
	// Per-call bound: a wedged MCP server must fail the tool call (the loop
	// backfills an error tool_result) instead of parking the whole turn.
	// NIUNIU_AGENT_MCP_TIMEOUT (Go duration) overrides; default 2 minutes.
	timeout := 2 * time.Minute
	if v := os.Getenv("NIUNIU_AGENT_MCP_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if _, err := c.request(ctx, "tools/call", map[string]any{
		"name": name, "arguments": args,
	}, &res); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, blk := range res.Content {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(blk.Text)
	}
	out := b.String()
	if res.IsError {
		return out, fmt.Errorf("mcp tool %s failed: %s", name, out)
	}
	return out, nil
}

// RegisterInto registers every server tool into reg as
// mcp__<server>__<tool>.
func (c *Client) RegisterInto(reg *tools.Registry) {
	for _, def := range c.ToolDefs() {
		reg.Register(mcpTool{client: c, def: def})
	}
}

// mcpTool adapts one MCP server tool to the tools.Tool interface.
type mcpTool struct {
	client *Client
	def    model.ToolDef
}

func (t mcpTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        ToolName(t.client.name, t.def.Name),
		Description: t.def.Description,
		InputSchema: t.def.InputSchema,
	}
}

func (t mcpTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return t.client.CallTool(ctx, t.def.Name, input)
}

// ToolName builds the registry name for one server tool:
// mcp__<server>__<tool>.
func ToolName(server, tool string) string { return "mcp__" + server + "__" + tool }

// Close shuts the server down: stdin EOF first (graceful), then SIGKILL if
// it does not exit promptly. Safe to call more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stdin, cmd := c.stdin, c.cmd
	c.mu.Unlock()

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

// request sends one JSON-RPC request and decodes the result into out.
func (c *Client) request(ctx context.Context, method string, params any, out any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("client closed")
	}
	c.seq++
	id := c.seq
	ch := make(chan rpcMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("rpc %d: %s", msg.Error.Code, msg.Error.Message)
		}
		if out != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, out); err != nil {
				return nil, fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return msg.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// notify sends a JSON-RPC notification (no id, no response).
func (c *Client) notify(method string) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method})
}

func (c *Client) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	stdin := c.stdin
	c.mu.Unlock()
	if stdin == nil {
		return errors.New("stdin not available")
	}
	_, err = stdin.Write(append(data, '\n'))
	return err
}

// readLoop scans stdout, routing responses to pending requests. Unparseable
// lines and notifications (server logs, progress) are ignored. On EOF all
// pending requests fail.
func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var probe struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &probe) != nil || len(probe.ID) == 0 || string(probe.ID) == "null" {
			continue
		}
		var id int64
		if json.Unmarshal(probe.ID, &id) != nil {
			continue
		}
		var msg rpcMsg
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
			close(ch)
		}
	}
	// EOF (server exited): fail everything still pending.
	c.mu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcMsg{Error: &rpcError{Message: "mcp server exited"}}
		close(ch)
	}
	c.mu.Unlock()
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
