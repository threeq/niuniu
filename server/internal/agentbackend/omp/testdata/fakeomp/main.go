// Command fakeomp emulates `omp --mode rpc` for unit-testing the OMP backend
// without requiring the real omp binary. It reads NDJSON commands from stdin
// and emits the documented protocol frames on stdout:
//
//   - a `ready` handshake frame at startup,
//   - an immediate `prompt` ack + streaming text / tool / extension_ui_request
//     events, waiting for the host's `extension_ui_response` before finishing
//     the turn with a terminal `agent_end` carrying cost telemetry,
//   - `abort` acknowledgement and a clean exit on stdin close.
//
// Env controls:
//   - FAKE_OMP_DELAY_MS: delay before the ready frame (to exercise timeouts).
//   - FAKE_OMP_FAIL_PROMPT=1: reply to `prompt` with success:false.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	w := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}

	if d, _ := strconv.Atoi(os.Getenv("FAKE_OMP_DELAY_MS")); d > 0 {
		time.Sleep(time.Duration(d) * time.Millisecond)
	}
	w(map[string]any{"type": "ready", "protocolVersion": 1, "supportedProtocolVersions": []int{1, 2}})

	failPrompt := os.Getenv("FAKE_OMP_FAIL_PROMPT") == "1"
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var head struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(sc.Bytes(), &head)

		switch head.Type {
		case "prompt":
			if failPrompt {
				w(map[string]any{"id": head.ID, "type": "response", "command": "prompt", "success": false, "error": "model unavailable"})
				continue
			}
			w(map[string]any{"id": head.ID, "type": "response", "command": "prompt", "success": true, "data": map[string]any{"agentInvoked": true}})
			w(map[string]any{"type": "agent_start"})
			w(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "Hello from fake omp. "}})
			w(map[string]any{"type": "tool_execution_start", "toolExecution": map[string]any{"id": "toolu_1", "name": "fake_tool", "input": map[string]any{"q": head.Message}}})
			w(map[string]any{"type": "extension_ui_request", "id": "ui_1", "method": "confirm", "title": "Confirm", "message": "Continue?"})

			// Block until the host answers the UI request.
			confirmed := false
			for sc.Scan() {
				var ui struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					Confirmed bool   `json:"confirmed"`
					Cancelled bool   `json:"cancelled"`
				}
				_ = json.Unmarshal(sc.Bytes(), &ui)
				if ui.Type == "extension_ui_response" && ui.ID == "ui_1" {
					confirmed = ui.Confirmed && !ui.Cancelled
					break
				}
			}
			w(map[string]any{"type": "tool_execution_end", "toolExecution": map[string]any{"id": "toolu_1", "name": "fake_tool", "output": map[string]any{"confirmed": confirmed}, "isError": false}})
			w(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": "done"}})
			w(map[string]any{"type": "agent_end", "isTerminal": true, "telemetry": map[string]any{"costUsd": 0.0123, "numTurns": 1, "durationMs": 500, "inputTokens": 100, "outputTokens": 50}})
		case "abort":
			w(map[string]any{"id": head.ID, "type": "response", "command": "abort", "success": true})
		case "extension_ui_response":
			// handled in the prompt branch; ignore stray responses.
		default:
			fmt.Fprintf(os.Stderr, "fakeomp: ignoring %s\n", head.Type)
		}
	}
	os.Exit(0)
}
