// Command fakegoose emulates the Goose ACP agent (`goose acp`) for unit-testing
// the goose backend without requiring the real goose binary. It speaks JSON-RPC
// 2.0 over newline-delimited stdio:
//
//   - answers `initialize` and `session/new` requests,
//   - on `session/prompt` acks the turn and streams `session/update`
//     notifications (text / tool_call / usage / terminal status), pausing on a
//     `session/request_permission` notification until the host's `session/reply`
//     request arrives, then emitting the tool result,
//   - answers `session/reply` / `session/close`, and exits on stdin close.
//
// Env controls:
//   - FAKE_GOOSE_DELAY_MS: delay before the initialize response (timeouts).
//   - FAKE_GOOSE_FAIL_PROMPT=1: reject `session/prompt` with an error.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

const sessionID = "fake-session-1"

func main() {
	out := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	w := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}

	if d, _ := strconv.Atoi(os.Getenv("FAKE_GOOSE_DELAY_MS")); d > 0 {
		time.Sleep(time.Duration(d) * time.Millisecond)
	}

	failPrompt := os.Getenv("FAKE_GOOSE_FAIL_PROMPT") == "1"
	failAfterPermission := os.Getenv("FAKE_GOOSE_FAIL_AFTER_PERMISSION") == "1"

	for sc.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}

		// Notifications (host→agent, no id) are acks; nothing to emit.
		if req.Method != "" && req.ID == 0 {
			continue
		}

		switch req.Method {
		case "initialize":
			w(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion":   "v1",
				"agentCapabilities": map[string]any{"loadSession": false},
			}})
		case "session/new":
			w(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"sessionId": sessionID,
			}})
		case "session/prompt":
			if failPrompt {
				w(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "model unavailable"}})
				continue
			}
			w(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"sessionId": sessionID,
				"turnId":    "turn_1",
			}})

			emit := func(events ...any) {
				w(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"update": map[string]any{
						"sessionId": sessionID,
						"turnId":    "turn_1",
						"status":    "running",
						"events":    events,
					},
				}})
			}
			emit(map[string]any{"type": "content_update", "content": map[string]any{"type": "text", "text": "Hello from fake goose. "}})
			emit(map[string]any{"type": "tool_call", "toolCall": map[string]any{"id": "toolu_1", "name": "fake_tool", "input": map[string]any{"q": "x"}, "status": "pending"}})

			// Permission request: pause until the host replies.
			w(map[string]any{"jsonrpc": "2.0", "method": "session/request_permission", "params": map[string]any{
				"sessionId": sessionID,
				"turnId":    "turn_1",
				"interactionZones": []any{map[string]any{
					"type":        "tools",
					"toolCallIds": []string{"toolu_1"},
					"title":       "Confirm",
					"message":     "Allow fake_tool?",
				}},
			}})
			allowed := false
			for sc.Scan() {
				var reply struct {
					ID  int64  `json:"id"`
					Method     string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				_ = json.Unmarshal(sc.Bytes(), &reply)
				if reply.Method == "session/reply" && reply.ID > 0 {
					var p struct {
						InteractionZones []struct {
							State string `json:"state"`
						} `json:"interactionZones"`
					}
					_ = json.Unmarshal(reply.Params, &p)
					allowed = len(p.InteractionZones) > 0 && p.InteractionZones[0].State == "allow"
					w(map[string]any{"jsonrpc": "2.0", "id": reply.ID, "result": map[string]any{}})
					break
				}
			}
			if failAfterPermission {
				w(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"update": map[string]any{
						"sessionId": sessionID, "turnId": "turn_1", "status": "running",
						"events": []any{map[string]any{"type": "status_update", "status": "error", "stopReason": "model unavailable"}},
					},
				}})
				continue
			}
			emit(map[string]any{"type": "tool_call_result", "toolCallResult": map[string]any{
				"id": "toolu_1", "toolCallId": "toolu_1", "status": "success", "isError": false,
				"content": []any{map[string]any{"type": "text", "text": `{"confirmed":` + strconv.FormatBool(allowed) + `}`}},
			}})
			emit(map[string]any{"type": "usage_update", "usage": map[string]any{"costUsd": 0.0123, "inputTokens": 100, "outputTokens": 50}})
			w(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"update": map[string]any{
					"sessionId": sessionID, "turnId": "turn_1", "status": "completed",
					"events": []any{map[string]any{"type": "status_update", "status": "completed"}},
				},
			}})
		case "session/reply":
			w(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		case "session/close":
			w(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
			_ = out.Flush()
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "fakegoose: ignoring %s\n", req.Method)
		}
	}
	os.Exit(0)
}