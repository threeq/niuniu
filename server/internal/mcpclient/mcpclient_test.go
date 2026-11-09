package mcpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallToolJSON(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			ID     float64        `json:"id"`
			Params map[string]any `json:"params"`
		}
		require.NoError(t, decodeBody(r, &req))
		calls = append(calls, req.Method)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			require.Equal(t, "publish", req.Params["name"])
			args := req.Params["arguments"].(map[string]any)
			require.Equal(t, "hello", args["title"])
			require.Equal(t, "sess-1", r.Header.Get("Mcp-Session-Id"))
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"publish_id\":\"p1\"}"}]}}`))
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)

	client := New(Config{Endpoint: srv.URL, Client: srv.Client()})
	result, status, err := client.CallTool(context.Background(), "publish", map[string]any{"title": "hello"})
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, "p1", result["publish_id"])
	require.Equal(t, []string{"initialize", "notifications/initialized", "tools/call"}, calls)
}

func TestCallToolSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		require.NoError(t, decodeBody(r, &req))
		w.Header().Set("Content-Type", "text/event-stream")
		switch req.Method {
		case "initialize":
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
		case "notifications/initialized":
			fmt.Fprint(w, "event: message\ndata: {}\n\n")
		default:
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"ok\\\":true}\"}]}}\n\n")
		}
	}))
	t.Cleanup(srv.Close)

	client := New(Config{Endpoint: srv.URL, Client: srv.Client()})
	result, _, err := client.CallTool(context.Background(), "ping", nil)
	require.NoError(t, err)
	require.Equal(t, true, result["ok"])
}

func TestCallToolPropagatesToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		require.NoError(t, decodeBody(r, &req))
		if req.Method == "tools/call" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(srv.Close)

	client := New(Config{Endpoint: srv.URL, Client: srv.Client()})
	_, _, err := client.CallTool(context.Background(), "publish", nil)
	require.Error(t, err)
}
