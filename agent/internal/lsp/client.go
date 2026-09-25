// Package lsp is a minimal, zero-dependency Language Server Protocol
// client (JSON-RPC 2.0 over stdio, Content-Length framing). It talks to
// any standards-compliant server (gopls, typescript-language-server,
// pyright, …) and exposes the four navigation operations the agent needs:
// definition, references, hover, and document symbols.
//
// Lifecycle: one server process per (config entry, workspace root), started
// lazily on first use and shared across tool calls; Shutdown reaps them.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServerConfig declares one language server (mirrors .niuniu-agent/lsp.json).
type ServerConfig struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Args       []string `json:"args,omitempty"`
	Extensions []string `json:"extensions"` // ".go", ".ts", …
}

// Location is a position in a file (LSP line/character are 0-based).
type Location struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`      // 0-based
	Character int    `json:"character"` // 0-based
}

// Symbol is one document symbol (flattened from the LSP hierarchy).
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// Client is one running language server connection.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	id     int
	mu     sync.Mutex // serializes request/response pairing
}

// Start launches the server and performs the LSP initialize handshake.
func Start(ctx context.Context, cfg ServerConfig, rootDir string) (*Client, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("lsp: empty command for server %q", cfg.Name)
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", cfg.Command, err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}

	rootURI := pathToURI(rootDir)
	var result json.RawMessage
	err = c.call(ctx, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"hover":              map[string]any{"contentFormat": []string{"plaintext"}},
				"definition":         map[string]any{},
				"references":         map[string]any{},
				"documentSymbol":     map[string]any{},
				"publishDiagnostics": map[string]any{},
			},
		},
	}, &result)
	if err != nil {
		c.Kill()
		return nil, fmt.Errorf("lsp: initialize %s: %w", cfg.Name, err)
	}
	_ = c.notify("initialized", map[string]any{})
	return c, nil
}

// call sends one request and waits for the matching response, skipping
// server-initiated notifications in between.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	id := c.id
	if err := writeMessage(c.stdin, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("lsp: %s timed out", method)
		}
		msg, err := readMessage(c.stdout)
		if err != nil {
			return fmt.Errorf("lsp: read %s response: %w", method, err)
		}
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(msg, &m); err != nil {
			continue // not a JSON-RPC message we care about
		}
		if m.ID == nil || *m.ID != id {
			continue // notification (diagnostics, logs) — skip
		}
		if m.Error != nil {
			return fmt.Errorf("lsp: %s failed: %s", method, m.Error.Message)
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}

func (c *Client) notify(method string, params any) error {
	return writeMessage(c.stdin, map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
}

// didOpen tells the server about a file we are about to query (LSP servers
// usually require the document to be open before navigation). An unreadable
// file is announced with empty text rather than failing the navigation —
// the server decides how to handle a missing document.
func (c *Client) didOpen(path string) error {
	data, _ := readFileForLSP(path)
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": pathToURI(path), "languageId": langIDOf(path),
			"version": 1, "text": data,
		},
	})
}

// Definition resolves the symbol at (0-based) line:char.
func (c *Client) Definition(ctx context.Context, path string, line, char int) ([]Location, error) {
	if err := c.didOpen(path); err != nil {
		return nil, err
	}
	var locs locationList
	err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"position":     map[string]any{"line": line, "character": char},
	}, &locs)
	if err != nil {
		return nil, err
	}
	return locs.toLocations(), nil
}

// References finds every reference to the symbol at (line, char).
func (c *Client) References(ctx context.Context, path string, line, char int) ([]Location, error) {
	if err := c.didOpen(path); err != nil {
		return nil, err
	}
	var locs locationList
	err := c.call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"position":     map[string]any{"line": line, "character": char},
		"context":      map[string]any{"includeDeclaration": true},
	}, &locs)
	if err != nil {
		return nil, err
	}
	return locs.toLocations(), nil
}

// Hover returns the hover text (signature/docs) at (line, char).
func (c *Client) Hover(ctx context.Context, path string, line, char int) (string, error) {
	if err := c.didOpen(path); err != nil {
		return "", err
	}
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := c.call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"position":     map[string]any{"line": line, "character": char},
	}, &h); err != nil {
		return "", err
	}
	return hoverText(h.Contents), nil
}

// Symbols lists the document's symbols (functions, types, …).
func (c *Client) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	if err := c.didOpen(path); err != nil {
		return nil, err
	}
	var raw []symbolRaw
	if err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
	}, &raw); err != nil {
		return nil, err
	}
	out := make([]Symbol, 0, len(raw))
	for _, s := range raw {
		out = append(out, Symbol{
			Name: s.Name, Kind: symbolKindName(s.Kind),
			Line: s.Range.Start.Line, Character: s.Range.Start.Character,
		})
		for _, ch := range s.Children {
			out = append(out, Symbol{
				Name: ch.Name, Kind: symbolKindName(ch.Kind),
				Line: ch.Range.Start.Line, Character: ch.Range.Start.Character,
			})
		}
	}
	return out, nil
}

// Shutdown performs the graceful LSP teardown; Kill force-terminates.
func (c *Client) Shutdown() {
	_ = c.notify("shutdown", nil)
	_ = c.notify("exit", nil)
	_ = c.cmd.Wait()
}

func (c *Client) Kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// ---- wire framing (Content-Length header per LSP spec) ----

func writeMessage(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // headers done
		}
		if i := strings.IndexByte(line, ':'); i >= 0 && strings.EqualFold(line[:i], "content-length") {
			length, _ = strconv.Atoi(strings.TrimSpace(line[i+1:]))
		}
	}
	if length <= 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// ---- helpers ----

func pathToURI(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + path
}

func uriToPath(uri string) string {
	return filepath.FromSlash(strings.TrimPrefix(uri, "file://"))
}

func langIDOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	default:
		return "plaintext"
	}
}

// locationOrRange tolerates servers returning Location | Location[] | null,
// and LocationLink-shaped objects.
type locationOrRange struct {
	URI   string `json:"uri"`
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
	} `json:"range"`
	// LocationLink fields (targetUri/targetSelectionRange) for robustness.
	TargetURI   string `json:"targetUri"`
	TargetRange struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
	} `json:"targetSelectionRange"`
}

// locationList names the slice so it can carry the toLocations method.
type locationList []locationOrRange

func (ls locationList) toLocations() []Location {
	out := make([]Location, 0, len(ls))
	for _, l := range ls {
		path := l.URI
		if l.TargetURI != "" {
			path, l.URI, l.Range = l.TargetURI, l.TargetURI, l.TargetRange
		}
		if path == "" {
			continue
		}
		out = append(out, Location{Path: uriToPath(path), Line: l.Range.Start.Line, Character: l.Range.Start.Character})
	}
	return out
}

type symbolRaw struct {
	Name     string `json:"name"`
	Kind     int    `json:"kind"`
	Range    span   `json:"range"`
	Location *span  `json:"location"`
	Children []struct {
		Name  string `json:"name"`
		Kind  int    `json:"kind"`
		Range span   `json:"range"`
	} `json:"children"`
}

type span struct {
	Start struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"start"`
}

// symbolKindName maps the LSP SymbolKind enum to readable names (the ones
// an agent cares about; the rest fall through to "symbol").
var symbolKindNames = map[int]string{
	1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
	6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
	11: "interface", 12: "function", 13: "variable", 14: "constant",
	15: "string", 16: "number", 17: "boolean", 23: "struct", 23 + 1: "event",
}

func symbolKindName(k int) string {
	if n, ok := symbolKindNames[k]; ok {
		return n
	}
	return "symbol"
}

// hoverText flattens MarkupContent | MarkedString | array forms into text.
func hoverText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var mc struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &mc); err == nil && mc.Value != "" {
		return mc.Value
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, a := range arr {
			if t := hoverText(a); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
