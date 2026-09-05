// Command fakecursor is a minimal stand-in for `agent acp` used by the cursor
// backend tests. It speaks the documented Cursor ACP surface — JSON-RPC 2.0 over
// stdio, newline-framed — so the backend is exercised against the real protocol
// shape rather than mocks of its own helpers.
//
// Deliberately mirrors the shapes from cursor.com/docs/cli/acp:
//   - initialize / authenticate / session/new are request→response
//   - session/update notifications use the STANDARD flat discriminator
//     ("sessionUpdate": "agent_message_chunk" | "tool_call" | "tool_call_update")
//   - session/request_permission is a REQUEST carrying an id, which the client
//     must answer with {"outcome":{"outcome":"selected","optionId":...}}
//   - the turn ends via the session/prompt RESPONSE carrying stopReason
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

var (
	out   = bufio.NewWriter(os.Stdout)
	outMu sync.Mutex
)

func emit(v any) {
	b, _ := json.Marshal(v)
	outMu.Lock()
	defer outMu.Unlock()
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
}

func result(id any, res any) {
	emit(map[string]any{"jsonrpc": "2.0", "id": id, "result": res})
}

func notify(method string, params any) {
	emit(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func update(sessionID string, upd map[string]any) {
	notify("session/update", map[string]any{"sessionId": sessionID, "update": upd})
}

func main() {
	// FAKECURSOR_MODE selects a scenario so one binary covers several tests.
	mode := os.Getenv("FAKECURSOR_MODE")

	// permDecision receives the client's answer to session/request_permission.
	permDecision := make(chan string, 1)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sessionID := "sess-fake-1"

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}

		// A response from the client (has id + result, no method): the only one we
		// await is the permission decision.
		if msg.Method == "" && msg.Result != nil {
			var r struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			if json.Unmarshal(msg.Result, &r) == nil {
				select {
				case permDecision <- r.Outcome.Outcome + ":" + r.Outcome.OptionID:
				default:
				}
			}
			continue
		}

		switch msg.Method {
		case "initialize":
			result(msg.ID, map[string]any{
				"protocolVersion": 1,
				"authMethods":     []any{map[string]any{"id": "cursor_login"}},
			})

		case "authenticate":
			if mode == "auth-error" {
				emit(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32000, "message": "already authenticated"}})
				continue
			}
			result(msg.ID, map[string]any{})

		case "session/new":
			if mode == "session-error" {
				emit(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32001, "message": "not logged in"}})
				continue
			}
			// Echo back the mcpServers we were given so the test can assert
			// niuniu-mcp actually reached the agent.
			var p struct {
				Cwd        string           `json:"cwd"`
				McpServers []map[string]any `json:"mcpServers"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			names := []string{}
			for _, s := range p.McpServers {
				if n, ok := s["name"].(string); ok {
					names = append(names, n)
				}
			}
			// Surface them on stdout as a text chunk later; for now just record.
			os.Setenv("FAKECURSOR_MCP_SEEN", strings.Join(names, ","))
			result(msg.ID, map[string]any{"sessionId": sessionID})

		case "session/prompt":
			go runTurn(msg.ID, sessionID, mode, permDecision)

		case "session/cancel":
			// Cooperative cancel: the in-flight turn resolves with stopReason
			// "cancelled" (handled inside runTurn via the cancel channel).
			select {
			case cancelCh <- struct{}{}:
			default:
			}
		}
	}
}

var cancelCh = make(chan struct{}, 1)

func runTurn(id any, sessionID, mode string, permDecision chan string) {
	switch mode {
	case "text-only":
		update(sessionID, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "hello "},
		})
		update(sessionID, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "world"},
		})
		result(id, map[string]any{"stopReason": "end_turn"})

	case "thinking":
		update(sessionID, map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": "pondering"},
		})
		result(id, map[string]any{"stopReason": "end_turn"})

	case "refusal":
		result(id, map[string]any{"stopReason": "refusal"})

	case "cancel":
		<-cancelCh
		result(id, map[string]any{"stopReason": "cancelled"})

	case "permission":
		// Ask the client to approve a shell call, then report which decision came
		// back so the test can assert the mapping.
		emit(map[string]any{
			"jsonrpc": "2.0",
			"id":      9001,
			"method":  "session/request_permission",
			"params": map[string]any{
				"sessionId": sessionID,
				"toolCall": map[string]any{
					"toolCallId": "tc-1",
					"title":      "rm -rf /tmp/x",
					"kind":       "execute",
					"rawInput":   map[string]any{"command": "rm -rf /tmp/x"},
				},
				"options": []any{
					map[string]any{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
					map[string]any{"optionId": "allow-always", "name": "Always allow", "kind": "allow_always"},
					map[string]any{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
				},
			},
		})
		decision := <-permDecision
		update(sessionID, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "decision=" + decision},
		})
		result(id, map[string]any{"stopReason": "end_turn"})

	case "renamed-options":
		// Same flow but with non-documented option ids, to prove the fallback
		// matching (by kind, then prefix) still resolves a decision.
		emit(map[string]any{
			"jsonrpc": "2.0",
			"id":      9002,
			"method":  "session/request_permission",
			"params": map[string]any{
				"sessionId": sessionID,
				"toolCall":  map[string]any{"toolCallId": "tc-9", "title": "write file"},
				"options": []any{
					map[string]any{"optionId": "allow_v2", "name": "OK", "kind": "allow_once"},
					map[string]any{"optionId": "reject_v2", "name": "No", "kind": "reject_once"},
				},
			},
		})
		decision := <-permDecision
		update(sessionID, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "decision=" + decision},
		})
		result(id, map[string]any{"stopReason": "end_turn"})

	case "blocking-extension":
		// cursor/ask_question is a BLOCKING extension method: if the client never
		// answers, the turn hangs. The backend must respond (skipped) so this
		// completes.
		emit(map[string]any{
			"jsonrpc": "2.0",
			"id":      9100,
			"method":  "cursor/ask_question",
			"params": map[string]any{
				"toolCallId": "tc-q",
				"questions":  []any{map[string]any{"id": "q1", "prompt": "Which mode?"}},
			},
		})
		// Wait for any client frame; the backend's response unblocks us.
		result(id, map[string]any{"stopReason": "end_turn"})

	default: // "tools": a full tool_call → tool_call_update cycle
		update(sessionID, map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "tc-1",
			"title":         "Read",
			"kind":          "read",
			"status":        "pending",
			"rawInput":      map[string]any{"path": "README.md"},
		})
		// A non-terminal update must NOT produce a tool_result.
		update(sessionID, map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "tc-1",
			"status":        "in_progress",
		})
		update(sessionID, map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "tc-1",
			"status":        "completed",
			"rawOutput":     map[string]any{"content": "file body"},
		})
		update(sessionID, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "done"},
		})
		result(id, map[string]any{"stopReason": "end_turn"})
	}
	_ = fmt.Sprint()
}
