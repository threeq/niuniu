// Package mcpclient is a minimal MCP (Model Context Protocol) client for
// server-side tool calls over Streamable HTTP. It performs the initialize
// handshake, then tools/call, and normalizes the text/JSON content a tool
// returns into a map so callers can record and act on it.
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// Config describes one MCP server. Exactly one transport is used: an HTTP
// endpoint (Streamable HTTP) or a stdio command.
type Config struct {
	Endpoint string
	Headers  map[string]string
	// Client must be SSRF-guarded by the caller (HTTP transport only).
	Client *http.Client

	// Command runs a stdio MCP server: argv (no shell), optional env and cwd.
	Command []string
	Env     map[string]string
	WorkDir string
	// Stdio injects an existing stream pair instead of spawning Command. Used
	// by tests; production always spawns a process.
	Stdio io.ReadWriteCloser
}

// Client calls tools on one MCP server.
type Client struct {
	cfg Config

	mu        sync.Mutex
	sessionID string
	ready     bool
	nextID    int64

	// stdio transport state
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
}

func New(cfg Config) *Client {
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	return &Client{cfg: cfg}
}

// CallTool runs one tool and returns its normalized result plus the HTTP
// status of the tools/call response.
func (c *Client) CallTool(ctx context.Context, tool string, args map[string]any) (map[string]any, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.cfg.Command) > 0 {
		return c.callToolStdio(ctx, tool, args)
	}

	if !c.ready {
		if err := c.initialize(ctx); err != nil {
			return nil, 0, err
		}
		c.ready = true
	}

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id(),
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	}
	status, raw, err := c.post(ctx, body)
	if err != nil {
		// A stale session is the common cause; reset so the next call re-inits.
		c.ready = false
		c.sessionID = ""
		return nil, status, err
	}
	result, err := decodeResult(raw)
	if err != nil {
		return nil, status, err
	}
	return result, status, nil
}

// Close terminates a stdio server process held by the client.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopStdioLocked()
}

func (c *Client) callToolStdio(ctx context.Context, tool string, args map[string]any) (map[string]any, int, error) {
	if !c.ready {
		if err := c.startStdioLocked(ctx); err != nil {
			return nil, 0, err
		}
		c.ready = true
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id(),
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	}
	envelope, err := c.stdioRoundTripLocked(ctx, body)
	if err != nil {
		_ = c.stopStdioLocked()
		return nil, 0, err
	}
	normalized, err := normalizeResult(envelope)
	if err != nil {
		return nil, 200, err
	}
	return normalized, 200, nil
}

func (c *Client) startStdioLocked(ctx context.Context) error {
	if c.cfg.Stdio != nil {
		c.stdin = c.cfg.Stdio
		c.reader = bufio.NewReaderSize(c.cfg.Stdio, 1<<20)
		return c.handshakeStdioLocked(ctx)
	}
	argv := c.cfg.Command
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = c.cfg.WorkDir
	env := os.Environ()
	keys := make([]string, 0, len(c.cfg.Env))
	for key := range c.cfg.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+c.cfg.Env[key])
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp: start %s: %w", argv[0], err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.reader = bufio.NewReaderSize(stdout, 1<<20)
	return c.handshakeStdioLocked(ctx)
}

func (c *Client) handshakeStdioLocked(ctx context.Context) error {
	initBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id(),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "niuniu-opsbot", "version": "1"},
		},
	}
	if _, err := c.stdioRoundTripLocked(ctx, initBody); err != nil {
		_ = c.stopStdioLocked()
		return err
	}
	_ = c.writeStdioLocked(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return nil
}

func (c *Client) stopStdioLocked() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
	c.cmd = nil
	c.stdin = nil
	c.reader = nil
	c.ready = false
	return nil
}

func (c *Client) writeStdioLocked(payload map[string]any) error {
	if c.stdin == nil {
		return fmt.Errorf("mcp: stdio server is not running")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) stdioRoundTripLocked(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := c.writeStdioLocked(payload); err != nil {
		return nil, err
	}
	wantID := fmt.Sprintf("%v", payload["id"])
	type lineResult struct {
		line []byte
		err  error
	}
	for {
		ch := make(chan lineResult, 1)
		go func() {
			line, err := c.reader.ReadBytes('\n')
			ch <- lineResult{line: line, err: err}
		}()
		select {
		case <-ctx.Done():
			_ = c.stopStdioLocked()
			return nil, ctx.Err()
		case res := <-ch:
			if res.err != nil {
				return nil, fmt.Errorf("mcp: stdio read: %w", res.err)
			}
			var envelope map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(res.line), &envelope); err != nil {
				continue
			}
			if fmt.Sprintf("%v", envelope["id"]) != wantID {
				continue
			}
			return envelope, nil
		}
	}
}

func (c *Client) initialize(ctx context.Context) error {
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id(),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "niuniu-opsbot", "version": "1"},
		},
	}
	if _, _, err := c.post(ctx, body); err != nil {
		return err
	}
	// Best-effort initialized notification; servers that require it accept an
	// empty response.
	_, _, _ = c.post(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return nil
}

func (c *Client) id() int64 {
	c.nextID++
	return c.nextID
}

func (c *Client) post(ctx context.Context, payload map[string]any) (int, []byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range c.cfg.Headers {
		req.Header.Set(key, value)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, raw, fmt.Errorf("mcp: http %d", resp.StatusCode)
	}
	return resp.StatusCode, raw, nil
}

// normalizeResult unwraps a JSON-RPC envelope and flattens tools/call content.
func normalizeResult(envelope map[string]any) (map[string]any, error) {
	if errObj, ok := envelope["error"].(map[string]any); ok {
		return nil, fmt.Errorf("mcp: tool error %v: %v", errObj["code"], errObj["message"])
	}
	result, _ := envelope["result"].(map[string]any)
	if result == nil {
		return map[string]any{}, nil
	}
	return flattenToolResult(result), nil
}

// flattenToolResult maps {content:[{type:text,text:"<json>"}]} to the parsed
// object; other result shapes pass through.
func flattenToolResult(result map[string]any) map[string]any {
	if content, ok := result["content"].([]any); ok {
		for _, item := range content {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				var object map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &object); err == nil {
					return object
				}
				return map[string]any{"text": text}
			}
		}
	}
	return result
}

// decodeResult accepts either a plain JSON-RPC body or an SSE stream whose
// data lines carry JSON-RPC messages, and returns the tool result object.
func decodeResult(raw []byte) (map[string]any, error) {
	payload := raw
	if contentTypeSSE(raw) {
		payload = lastJSONRPCData(raw)
	}
	var envelope struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("mcp: decode response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("mcp: tool error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Result == nil {
		return map[string]any{}, nil
	}
	return flattenToolResult(envelope.Result), nil
}

func contentTypeSSE(raw []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(raw), []byte("event:")) ||
		bytes.HasPrefix(bytes.TrimSpace(raw), []byte("data:"))
}

func lastJSONRPCData(raw []byte) []byte {
	var last []byte
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if strings.HasPrefix(candidate, "{") {
			last = []byte(candidate)
		}
	}
	if last == nil {
		return raw
	}
	return last
}
