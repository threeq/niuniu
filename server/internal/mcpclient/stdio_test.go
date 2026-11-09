package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runPipeServer speaks line-delimited JSON-RPC over one end of a pipe.
func runPipeServer(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	out := bufio.NewWriter(conn)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeJSONRPC(out, req.ID, map[string]any{"protocolVersion": "2024-11-05"})
		case "notifications/initialized":
			// no response
		case "tools/call":
			name, _ := req.Params["name"].(string)
			writeJSONRPC(out, req.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": `{"tool":"` + name + `","ok":true}`}},
			})
		}
	}
}

func writeJSONRPC(out *bufio.Writer, id json.RawMessage, result map[string]any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	out.Write(payload)
	out.WriteByte('\n')
	out.Flush()
}

func TestCallToolStdioProtocol(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	go runPipeServer(serverConn)

	client := New(Config{Command: []string{"ignored"}, Stdio: clientConn})
	t.Cleanup(func() { _ = client.Close() })

	result, status, err := client.CallTool(context.Background(), "publish", map[string]any{"title": "x"})
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, "publish", result["tool"])
	require.Equal(t, true, result["ok"])
}

func TestCallToolStdioPropagatesError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		scanner := bufio.NewScanner(serverConn)
		scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
		out := bufio.NewWriter(serverConn)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if strings.HasPrefix(req.Method, "notifications/") {
				continue
			}
			if req.Method == "tools/call" {
				payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32000, "message": "boom"}})
				out.Write(payload)
				out.WriteByte('\n')
				out.Flush()
				continue
			}
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
			out.Write(payload)
			out.WriteByte('\n')
			out.Flush()
		}
	}()

	client := New(Config{Command: []string{"ignored"}, Stdio: clientConn})
	t.Cleanup(func() { _ = client.Close() })

	_, _, err := client.CallTool(context.Background(), "publish", nil)
	require.Error(t, err)
}
