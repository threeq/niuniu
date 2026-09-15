package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// TestHelperMCPServer is re-execved as a child process (the classic go-test
// helper-process pattern) and plays a minimal MCP stdio server:
// initialize → tools/list (one "echo" tool) → tools/call. Cross-platform
// without shipping or building a separate binary.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv("NIUNIU_TEST_MCP_SERVER") != "1" {
		return
	}
	runFakeMCPServer(os.Stdin, os.Stdout)
	os.Exit(0)
}

// runFakeMCPServer answers the three methods the client speaks.
func runFakeMCPServer(in *os.File, out *os.File) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var req struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		// No id → notification (e.g. notifications/initialized): ignore.
		if req.ID == nil || req.Method == "" {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "0.0.1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo the given text back.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{"type": "string"},
					},
					"required": []string{"text"},
				},
			}}}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name != "echo" {
				result = map[string]any{"isError": true, "content": []map[string]any{
					{"type": "text", "text": "unknown tool " + p.Name}}}
				break
			}
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Arguments, &args)
			result = map[string]any{"content": []map[string]any{
				{"type": "text", "text": "echo:" + args.Text}}}
		default:
			fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no method %s"}}`+"\n",
				*req.ID, req.Method)
			continue
		}
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		fmt.Fprintf(out, "%s\n", res)
	}
}

// fakeServerConfig points a ServerConfig at this test binary playing the
// helper server.
func fakeServerConfig(t *testing.T) ServerConfig {
	t.Helper()
	return ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperMCPServer$"},
		Env:     map[string]string{"NIUNIU_TEST_MCP_SERVER": "1"},
	}
}

func TestClientInitializeToolsListAndCall(t *testing.T) {
	c, err := Connect(context.Background(), "test", fakeServerConfig(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	tds := c.ToolDefs()
	if len(tds) != 1 || tds[0].Name != "echo" || tds[0].Description != "Echo the given text back." {
		t.Fatalf("tools = %+v", tds)
	}
	if !strings.Contains(string(tds[0].InputSchema), `"text"`) {
		t.Errorf("inputSchema = %s", tds[0].InputSchema)
	}

	out, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if out != "echo:hi" {
		t.Errorf("CallTool = %q, want echo:hi", out)
	}

	// isError results surface as Go errors carrying the server's text.
	if _, err := c.CallTool(context.Background(), "nope", json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "unknown tool nope") {
		t.Errorf("err = %v, want server-side unknown-tool error", err)
	}
}

func TestConnectBrokenCommand(t *testing.T) {
	_, err := Connect(context.Background(), "broken", ServerConfig{Command: "definitely-not-a-real-binary-xyz"})
	if err == nil {
		t.Fatal("want error for nonexistent command")
	}
}

func TestConnectHandshakeTimeout(t *testing.T) {
	// A server that never answers initialize — the handshake must time out
	// instead of hanging. sleep/ping produce no valid JSON lines.
	cfg := ServerConfig{Command: "sleep", Args: []string{"30"}}
	if os.PathSeparator == '\\' {
		cfg = ServerConfig{Command: "ping", Args: []string{"-n", "30", "127.0.0.1"}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, "slow", cfg)
	if err == nil {
		t.Fatal("want initialize timeout error")
	}
}

// —— 注册进工具表后的集成：经 Registry.Execute 真实调用回填 ——

func TestRegisteredToolCalledThroughRegistry(t *testing.T) {
	c, err := Connect(context.Background(), "test", fakeServerConfig(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	reg := tools.NewRegistry()
	c.RegisterInto(reg)

	defs := reg.Defs()
	if len(defs) != 1 || defs[0].Name != "mcp__test__echo" {
		t.Fatalf("defs = %+v, want single mcp__test__echo", defs)
	}
	out, err := reg.Execute(context.Background(), "mcp__test__echo", json.RawMessage(`{"text":"via-loop"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "echo:via-loop" {
		t.Errorf("out = %q", out)
	}
	// Unknown tools must still error through the registry path.
	if _, err := reg.Execute(context.Background(), "mcp__test__nope", json.RawMessage(`{}`)); err == nil {
		t.Error("want error for unknown mcp tool")
	}
}
