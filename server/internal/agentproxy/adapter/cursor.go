package adapter

import (
	"encoding/json"
	"strings"
)

// CursorAdapter implements Adapter for Cursor's headless agent CLI
// (`cursor-agent`), niuniu's sixth agent engine.
//
// Unlike Qwen Code — whose stream-json deliberately mirrors the Anthropic wire
// shape, letting QwenAdapter reuse ParseStreamLine — cursor-agent emits its OWN
// NDJSON schema. It is Claude-Code-*shaped* at the top level (`type` +
// `session_id`, and assistant messages carry `message.content[]` text blocks)
// but diverges in two ways that make reusing the Claude parser wrong:
//
//  1. tool calls are NOT `tool_use` content blocks. They are separate
//     `{"type":"tool_call","subtype":"started"|"completed"}` lines whose
//     `tool_call` object holds exactly one typed key (`readToolCall`,
//     `shellToolCall`, …) rather than a `name` field.
//  2. the terminal `result` line has no `usage` / `total_cost_usd`; it carries
//     `duration_ms`, `duration_api_ms`, `is_error` and the assembled `result`
//     text.
//
// So ParseLine is a hand-written mapper onto ParsedEvent. Each `cursor-agent`
// invocation is one turn (the prompt arrives on stdin), so ProcessMode is
// one-shot and continuity comes from `--resume <chatId>`.
//
// Streaming note: we deliberately do NOT pass --stream-partial-output. With it,
// cursor emits three flavors of `assistant` event (a true delta, a pre-tool-call
// buffered flush, and an end-of-turn flush) distinguishable only by the presence
// of `timestamp_ms` / `model_call_id`; mis-classifying them double-prints the
// reply. Without it, each `assistant` line is exactly one complete message
// segment, which the one-shot runner renders verbatim — correct by construction.
type CursorAdapter struct{}

func (CursorAdapter) Type() Type { return TypeCursor }

func (CursorAdapter) ProcessMode() ProcessMode { return ProcessOneShot }

func (CursorAdapter) DisplayName(command string) string {
	return cliBaseName(command, "cursor-agent")
}

// cursorToolNames maps cursor-agent's typed tool_call keys onto the Claude tool
// names the SPA already renders icons and summaries for. Unknown keys fall back
// to the raw key with the "ToolCall" suffix trimmed, so a tool added by a future
// cursor release still shows a sensible label instead of blank.
var cursorToolNames = map[string]string{
	"readToolCall":   "Read",
	"writeToolCall":  "Write",
	"editToolCall":   "Edit",
	"listToolCall":   "LS",
	"shellToolCall":  "Bash",
	"searchToolCall": "Grep",
	"globToolCall":   "Glob",
	"deleteToolCall": "Delete",
	"todoToolCall":   "TodoWrite",
	"webToolCall":    "WebFetch",
	"mcpToolCall":    "MCP",
}

// cursorLine is the union of the fields cursor-agent's stream-json emits across
// its event types. Only the ones niuniu consumes are declared.
type cursorLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`

	Message *struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`

	CallID   string                     `json:"call_id"`
	ToolCall map[string]json.RawMessage `json:"tool_call"`

	IsError       bool   `json:"is_error"`
	Result        string `json:"result"`
	DurationMs    int64  `json:"duration_ms"`
	DurationAPIMs int64  `json:"duration_api_ms"`
}

func (CursorAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, nil
	}
	var raw cursorLine
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, err
	}

	switch raw.Type {
	case "system":
		// handleEvent captures session_id only on subtype=="init"; cursor already
		// spells it that way, but normalize defensively so a future
		// "session_start" spelling still seeds the resume id.
		subtype := raw.Subtype
		switch subtype {
		case "session_start", "session_started", "":
			subtype = "init"
		}
		return []ParsedEvent{{
			Type:      "system",
			Subtype:   subtype,
			SessionID: raw.SessionID,
		}}, nil

	case "user":
		// An echo of the prompt niuniu just wrote to stdin. The SPA already
		// rendered the user's message optimistically, so forwarding this would
		// paint it twice.
		return nil, nil

	case "assistant":
		ev := ParsedEvent{Type: "assistant", SessionID: raw.SessionID}
		if raw.Message != nil {
			for _, c := range raw.Message.Content {
				if c.Type == "text" && c.Text != "" {
					ev.TextBlocks = append(ev.TextBlocks, TextBlock{Text: c.Text})
				}
			}
		}
		if len(ev.TextBlocks) == 0 {
			// A content-less flush (cursor emits these around tool calls); nothing
			// to render, and forwarding an empty assistant event would append a
			// blank bubble.
			return nil, nil
		}
		return []ParsedEvent{ev}, nil

	case "tool_call":
		return parseCursorToolCall(raw), nil

	case "result":
		return []ParsedEvent{{
			Type:       "result",
			Subtype:    raw.Subtype,
			SessionID:  raw.SessionID,
			Result:     raw.Result,
			IsError:    raw.IsError || raw.Subtype == "error" || raw.Subtype == "failure",
			DurationMs: raw.DurationMs,
		}}, nil
	}

	// Unknown event type (e.g. a "thinking" line from a newer build): ignore
	// rather than error, so one unrecognized line never aborts a turn.
	return nil, nil
}

// parseCursorToolCall maps a tool_call line onto a tool-use (started) or
// tool-result (completed) ParsedEvent. cursor's tool_call object carries exactly
// one typed key plus, when completed, a "result" key — so the tool name is the
// first non-"result" key present.
func parseCursorToolCall(raw cursorLine) []ParsedEvent {
	toolKey, argsRaw := cursorToolEntry(raw.ToolCall)
	name := cursorToolNames[toolKey]
	if name == "" {
		name = strings.TrimSuffix(toolKey, "ToolCall")
	}
	if name == "" {
		name = "Tool"
	}

	switch raw.Subtype {
	case "completed":
		content := ""
		isErr := false
		if raw.ToolCall != nil {
			if res, ok := raw.ToolCall["result"]; ok {
				content = string(res)
				// cursor wraps a successful outcome in {"success":{…}}; anything
				// else (notably {"error":{…}}) is a failure.
				var probe map[string]json.RawMessage
				if json.Unmarshal(res, &probe) == nil {
					if _, ok := probe["success"]; !ok {
						isErr = true
					}
				}
			}
		}
		return []ParsedEvent{{
			Type:      "user", // tool_result blocks ride on a user-role event
			SessionID: raw.SessionID,
			ToolResults: []ToolResultBlock{{
				ToolUseId: raw.CallID,
				Content:   content,
				IsError:   isErr,
			}},
		}}
	case "started", "":
		input := "{}"
		if len(argsRaw) > 0 {
			input = string(argsRaw)
		}
		return []ParsedEvent{{
			Type:      "assistant",
			SessionID: raw.SessionID,
			ToolUseBlocks: []ToolUseBlock{{
				Id:    raw.CallID,
				Name:  name,
				Input: input,
			}},
		}}
	}
	return nil
}

// cursorToolEntry returns the tool's typed key and its "args" payload from a
// tool_call object, skipping the "result" key. The args are unwrapped from the
// {"<tool>ToolCall":{"args":{…}}} nesting so the SPA sees the same flat input
// object it gets for Claude tool_use blocks.
func cursorToolEntry(tc map[string]json.RawMessage) (string, json.RawMessage) {
	for k, v := range tc {
		if k == "result" {
			continue
		}
		var inner struct {
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(v, &inner) == nil && len(inner.Args) > 0 {
			return k, inner.Args
		}
		return k, v
	}
	return "", nil
}

// BuildSpawn returns the cursor-agent headless invocation. The prompt is
// delivered on stdin by the one-shot runner (cursor treats piped stdin as the
// prompt when no positional prompt is given), so no prompt arg is appended.
func (CursorAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "cursor-agent"
	}
	args := []string{
		"-p",
		"--output-format", "stream-json",
	}
	if opts.WorkDir != "" {
		args = append(args, "--workspace", opts.WorkDir)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	// cursor-agent has no --add-dir equivalent: --workspace takes a single root.
	// niuniu's workspace dir already contains every worktree as a subdirectory,
	// so the extra dirs are reachable without a flag.
	args = append(args, opts.ExtraArgs...)
	return command, args
}

// InjectEnv passes workspace env through (filtered of NIUNIU_* control keys) and
// injects git identity. accountKey is empty: cursor-agent stores its login under
// ~/.local/share/cursor-agent (no niuniu-managed per-account config dir), and
// API-key auth flows through the CURSOR_API_KEY env var supplied as a workspace
// env var / provider secret.
func (CursorAdapter) InjectEnv(base []string, opts EnvOptions) []string {
	return injectCLIEnv(base, opts, "")
}

func (CursorAdapter) PermissionArgs(opts PermissionOptions) []string {
	return BuildCursorPermissionArgs(opts.Mode)
}

// BuildCursorPermissionArgs maps niuniu's permission mode onto cursor-agent
// approval flags. Rationale mirrors BuildQwenPermissionArgs: a headless
// (non-TTY) cursor-agent run that hits an approval prompt it can never answer
// would hang the turn, and niuniu's outer workspace isolation is the real
// sandbox — so every auto-run mode passes --force (auto-approve tools) together
// with --trust (skip the workspace-trust confirmation, which otherwise blocks
// before the first token on a directory cursor has not seen before).
//
// "plan" maps to cursor's native read-only planning mode (--mode plan) instead
// of granting write access, and still needs --trust to get past the trust gate.
func BuildCursorPermissionArgs(mode string) []string {
	if mode == "plan" {
		return []string{"--trust", "--mode", "plan"}
	}
	return []string{"--trust", "--force"}
}
