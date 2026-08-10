package adapter

import (
	"encoding/json"
	"strings"
)

// QwenAdapter implements Adapter for the Qwen Code CLI (Alibaba, Apache-2.0),
// niuniu's third agent engine and the first that runs no foreign closed-source
// binary (see docs/superpowers/specs/2026-06-29-domestic-general-agent-engine-selection.md).
//
// Qwen Code is a Gemini-CLI fork whose headless mode
// (`qwen --output-format stream-json --include-partial-messages`) deliberately
// mirrors the Anthropic stream-json wire shape, so ParseLine reuses the Claude
// stream-json parser (ParseStreamLine) with three small normalizations:
//
//  1. the per-turn `system`/`session_start` init line is remapped to the
//     `system`/`init` subtype the session layer keys session capture on;
//  2. raw top-level Anthropic partial events (message_start / content_block_*)
//     are wrapped into the {"type":"stream_event","event":...} envelope the
//     Claude parser expects, so the adapter is robust whether a Qwen build
//     emits them Claude-Code-wrapped or unwrapped;
//  3. a `result`/`error` subtype is surfaced as IsError.
//
// Each `qwen` headless invocation is a single turn (Gemini-fork heritage: no
// --input-format stream-json multi-turn stdin), so ProcessMode is one-shot and
// continuity comes from --resume <sessionId>.
type QwenAdapter struct{}

func (QwenAdapter) Type() Type { return TypeQwen }

func (QwenAdapter) ProcessMode() ProcessMode { return ProcessOneShot }

func (QwenAdapter) DisplayName(command string) string {
	return cliBaseName(command, "qwen")
}

// qwenPartialEventTypes are the raw Anthropic streaming event types Qwen may
// emit at the top level (unwrapped) instead of inside a Claude-Code
// {"type":"stream_event","event":...} envelope. We wrap them so ParseStreamLine
// reaches its stream_event branch.
var qwenPartialEventTypes = map[string]struct{}{
	"message_start":       {},
	"message_delta":       {},
	"message_stop":        {},
	"content_block_start": {},
	"content_block_delta": {},
	"content_block_stop":  {},
}

func (QwenAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, nil
	}
	// Peek at the top-level type/subtype to decide whether the line needs the
	// stream_event envelope wrap (unwrapped raw-Anthropic shape) before the
	// shared Claude parser can read it.
	var head struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Event   json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal([]byte(trimmed), &head); err != nil {
		return nil, err
	}
	parseTarget := trimmed
	if _, partial := qwenPartialEventTypes[head.Type]; partial && len(head.Event) == 0 {
		parseTarget = `{"type":"stream_event","event":` + trimmed + `}`
	}
	ev, err := ParseStreamLine(parseTarget)
	if err != nil {
		return nil, err
	}
	normalizeQwenEvent(&ev, head.Type, head.Subtype)
	return []ParsedEvent{ev}, nil
}

// normalizeQwenEvent maps Qwen-specific subtype spellings onto the niuniu
// session layer's expectations. It only touches the top-level system/result
// lines; wrapped partial events keep the stream_event type ParseStreamLine set.
func normalizeQwenEvent(ev *ParsedEvent, rawType, rawSubtype string) {
	switch rawType {
	case "system":
		switch rawSubtype {
		case "session_start", "session_started", "init":
			// handleEvent captures session_id only on subtype=="init".
			ev.Subtype = "init"
		}
	case "result":
		if rawSubtype == "error" || rawSubtype == "failure" {
			ev.IsError = true
		}
	}
}

func (QwenAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "qwen"
	}
	// Headless stream-json. The prompt is delivered on stdin (the one-shot
	// runner pipes it and closes the writer, matching `echo ... | qwen`), so no
	// -p/<prompt> arg is appended here.
	args := []string{
		"--output-format", "stream-json",
		"--include-partial-messages",
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if len(opts.WorktreeDirs) > 0 {
		// --include-directories takes a single comma-separated list.
		args = append(args, "--include-directories", strings.Join(opts.WorktreeDirs, ","))
	}
	args = append(args, opts.ExtraArgs...)
	return command, args
}

func (QwenAdapter) InjectEnv(base []string, opts EnvOptions) []string {
	// Qwen has no niuniu-managed per-account home dir (unlike Claude's
	// CLAUDE_CONFIG_DIR / Codex's CODEX_HOME), so pass an empty accountKey to
	// skip that injection. The provider base_url / key / model reach Qwen
	// through workspace_env / env presets (OpenAI-compatible OPENAI_* or
	// DashScope vars), filtered of NIUNIU_* control keys by injectCLIEnv.
	return injectCLIEnv(base, opts, "")
}

func (QwenAdapter) PermissionArgs(opts PermissionOptions) []string {
	return BuildQwenPermissionArgs(opts.Mode)
}

// BuildQwenPermissionArgs maps niuniu's permission mode to Qwen Code approval
// flags. Every auto-run mode collapses to --yolo (auto-approve every tool),
// which mirrors how the Codex adapter defaults to danger-full-access/never:
// niuniu's outer workspace isolation is the real sandbox, and a headless
// (non-TTY) qwen run without --yolo would hang waiting for an interactive
// approval it can never receive. "plan" (read-only exploration) returns nil so
// it keeps Qwen's conservative default. A finer per-tool approval bridge (Qwen
// has no --permission-prompt-tool equivalent yet) is a follow-up.
func BuildQwenPermissionArgs(mode string) []string {
	switch mode {
	case "plan":
		return nil
	default:
		return []string{"--yolo"}
	}
}
