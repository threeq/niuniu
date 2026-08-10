package agentproxy

// RunOneShotCLI and its supporting infrastructure are the shared backend for
// every one-shot structured AI generation call (SuggestGoalCondition,
// SuggestColumnOpFields, ClassifyIssueForKickoff, ...).  All subprocess
// building, execution, error extraction, and JSON envelope parsing lives here;
// domain callers only need to supply a prompt, a JSON schema, and an optional
// Claude account configDir.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

const (
	// oneShotTimeout caps how long a one-shot subprocess may run.
	// 60 s allows for cache-miss CLI init (~25 s) + --json-schema retry
	// rounds on top of the actual API round-trip.
	oneShotTimeout = 60 * time.Second
)

// claudeBinary is the executable name used by buildOneShotCmd. Overridable at
// the package level for tests that need to substitute a stub without touching
// PATH.
var claudeBinary = "claude"

// OneShotProviderEnvFunc, when set by the server wiring, returns extra KEY=VALUE
// env entries for every one-shot subprocess — the env of the preset an owner
// marked with service.OneShotProviderMarker. It lets one-shot AI helpers
// (goal-condition suggest/classify, column-op suggest) reach a third-party
// provider (e.g. 智谱) that is configured ONLY in a niuniu env preset, since
// one-shot calls run outside any workspace and otherwise see only the host env.
//
// Kept as a package var and resolved INSIDE buildOneShotCmd so no caller has to
// thread a provider parameter through. nil = host env only. Set once in
// server.New.
var OneShotProviderEnvFunc func(ctx context.Context) []string

// RunOneShotCLI spawns a non-interactive `claude -p` subprocess for a single
// structured AI generation call. It is the shared backend for
// SuggestGoalCondition, SuggestColumnOpFields, ClassifyIssueForKickoff, and
// any future one-shot helpers.
//
// The subprocess runs with the minimum capability surface for a JSON-only
// structured-generation task:
//   - `-p` / print mode: non-interactive, single-turn, no conversation loop
//   - `--model haiku`: fast and cheap; sufficient for structured generation
//   - `--output-format json`: machine-parseable envelope
//   - `--json-schema <schema>`: Claude validates output and re-prompts on
//     mismatch — eliminates prose-scraping on the happy path
//   - `--no-session-persistence`: stateless; no prior history loaded or saved
//   - `--disable-slash-commands`: prevent accidental meta-commands in prompts
//   - Working dir = os.TempDir(): neutral; avoids loading project-level
//     .mcp.json, CLAUDE.md, or any other context files; project-scoped MCP
//     servers are not started as a side-effect
//
// configDir, when non-empty, is injected as CLAUDE_CONFIG_DIR so the
// subprocess uses the same account credentials as workspace agents. Empty =
// fall back to the CLI's native ~/.claude credentials.
//
// The marked one-shot provider preset (if any) is injected internally via
// OneShotProviderEnvFunc — callers do not pass provider env.
//
// Returns the raw stdout bytes on success; the caller uses ParseOneShotOutput
// or a domain-specific parser to extract the typed result from the envelope.
func RunOneShotCLI(ctx context.Context, prompt, jsonSchema, configDir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, oneShotTimeout)
	defer cancel()

	cmd := buildOneShotCmd(ctx, prompt, jsonSchema, configDir)

	// Wire stdin to /dev/null (or NUL on Windows) explicitly so the child
	// reads EOF immediately rather than inheriting the parent's stdin handle
	// (which can behave inconsistently in Wails-spawned server processes).
	if devNull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devNull
		defer devNull.Close()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	out := stdoutBuf.Bytes()
	stderrTxt := strings.TrimSpace(stderrBuf.String())

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.New("one-shot call timed out")
		}
		// Claude exits non-zero when the envelope has is_error=true (e.g.
		// 403 Failed to authenticate). The envelope is still on stdout;
		// surface the API error instead of a useless "exit status 1".
		if apiErr := extractEnvelopeAPIError(out); apiErr != "" {
			return nil, fmt.Errorf("claude subprocess: %s", apiErr)
		}
		slog.Warn("one-shot subprocess non-zero exit, no envelope error extracted",
			"err", err, "stdout", string(out), "stderr", stderrTxt)
		stdoutSnippet := truncateForError(string(out), 400)
		if stderrTxt != "" {
			stderrSnippet := truncateForError(stderrTxt, 300)
			if stdoutSnippet != "" {
				return nil, fmt.Errorf("claude subprocess: %w (stdout: %s | stderr: %s)", err, stdoutSnippet, stderrSnippet)
			}
			return nil, fmt.Errorf("claude subprocess: %w (stderr: %s)", err, stderrSnippet)
		}
		if stdoutSnippet != "" {
			return nil, fmt.Errorf("claude subprocess: %w (stdout: %s)", err, stdoutSnippet)
		}
		return nil, fmt.Errorf("claude subprocess: %w (no stdout/stderr captured)", err)
	}
	return out, nil
}

// ParseOneShotOutput extracts the structured result from a claude
// --output-format json envelope and unmarshals it into dst (a non-nil
// pointer). It is the generic companion to RunOneShotCLI.
//
// Priority order:
//  1. envelope.structured_output (canonical --json-schema path, CLI 2.1+)
//  2. envelope.result decoded as JSON (legacy path for older CLI versions)
//  3. Bare top-level JSON object matching dst (no envelope)
// stripJSONFence removes any ```json ... ``` fencing the model might add
// despite a "no markdown" instruction, returning the inner payload trimmed.
func stripJSONFence(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return []byte(strings.TrimSpace(s))
}

func ParseOneShotOutput(raw []byte, dst any) error {
	objs := extractAllJSONObjects(stripJSONFence(raw))

	// Priority 1: structured_output — canonical when --json-schema is used.
	for _, obj := range objs {
		var env struct {
			IsError          bool            `json:"is_error"`
			StructuredOutput json.RawMessage `json:"structured_output"`
		}
		if json.Unmarshal(obj, &env) != nil || env.IsError || len(env.StructuredOutput) == 0 {
			continue
		}
		if err := json.Unmarshal(env.StructuredOutput, dst); err == nil {
			return nil
		}
	}

	// Priority 2: result field (JSON payload encoded as a string).
	for _, obj := range objs {
		var env struct {
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if json.Unmarshal(obj, &env) != nil || env.IsError || env.Result == "" {
			continue
		}
		inner := stripJSONFence([]byte(env.Result))
		for _, innerObj := range extractAllJSONObjects(inner) {
			if err := json.Unmarshal(innerObj, dst); err == nil {
				return nil
			}
		}
	}

	// Priority 3: bare top-level JSON (model emitted without envelope).
	for _, obj := range objs {
		if err := json.Unmarshal(obj, dst); err == nil {
			return nil
		}
	}

	return errors.New("no matching structured output found in claude response")
}

// ---------------------------------------------------------------------------
// Command building
// ---------------------------------------------------------------------------

// buildOneShotCmd composes the *exec.Cmd for RunOneShotCLI. Extracted so
// tests can assert argv / cwd / env without spawning a subprocess.
func buildOneShotCmd(ctx context.Context, prompt, schema, configDir string) *exec.Cmd {
	bin := resolveClaudeBinary(claudeBinary)
	cmd := exec.CommandContext(ctx, bin,
		"-p", prompt,
		"--model", "haiku",
		"--output-format", "json",
		"--json-schema", schema,
		"--no-session-persistence",
		"--disable-slash-commands",
		// --no-mcp omitted: not supported in all Claude CLI versions.
		// Running from os.TempDir() already avoids loading project-level
		// .mcp.json; that is sufficient isolation for pure generation calls.
	)
	cmd.Dir = os.TempDir() // neutral cwd: no project .mcp.json / CLAUDE.md bleed
	cmd.Env = os.Environ()
	if configDir != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	// Inject the owner's marked one-shot provider preset (resolved internally via
	// the package hook — no caller threads it). Appended after the host env so the
	// preset's ANTHROPIC_* values win on duplicate keys; this is what lets a
	// one-shot probe reach e.g. 智谱 when the provider is configured only in a
	// niuniu preset, not in the host environment.
	if OneShotProviderEnvFunc != nil {
		cmd.Env = append(cmd.Env, OneShotProviderEnvFunc(ctx)...)
	}
	// Drop any leftover ANTHROPIC_API_KEY when a third-party provider's Bearer
	// token is configured, so this one-shot probe authenticates the same way the
	// workspace agent does instead of failing with "403 invalid api-key" on hosts
	// that carry a stale key. See adapter.SanitizeAnthropicEnv.
	cmd.Env = adapter.SanitizeAnthropicEnv(cmd.Env)
	return cmd
}

// resolveClaudeBinary looks up name via PATH and, on Windows, transparently
// redirects the npm .cmd shim to the underlying claude.exe. The .cmd shim
// routes through cmd.exe which mangles embedded JSON args (curly braces,
// quotes, multi-line prompts); calling the .exe directly bypasses that layer.
func resolveClaudeBinary(name string) string {
	if runtime.GOOS != "windows" {
		return name
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	low := strings.ToLower(resolved)
	if !strings.HasSuffix(low, ".cmd") && !strings.HasSuffix(low, ".bat") {
		return resolved
	}
	dir := filepath.Dir(resolved)
	candidate := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate
	}
	return resolved
}

// ---------------------------------------------------------------------------
// JSON envelope utilities
// ---------------------------------------------------------------------------

// extractAllJSONObjects walks raw and returns every balanced top-level `{...}`
// substring. String literals and escaped quotes are tracked. Claude stdout
// routinely contains multiple objects (plugin sync / auto-memory / hook
// handshakes emit status frames around the real result envelope).
func extractAllJSONObjects(raw []byte) [][]byte {
	var out [][]byte
	start := -1
	depth := 0
	inStr := false
	escape := false
	for i, b := range raw {
		if escape {
			escape = false
			continue
		}
		if inStr {
			switch b {
			case '\\':
				escape = true
			case '"':
				inStr = false
			}
			continue
		}
		switch b {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, raw[start:i+1])
				start = -1
			}
		}
	}
	return out
}

// extractEmbeddedJSONObject returns the first balanced object from raw.
func extractEmbeddedJSONObject(raw []byte) []byte {
	all := extractAllJSONObjects(raw)
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

// extractEnvelopeAPIError pulls the human-readable API error from a claude
// --output-format json envelope when the CLI exited non-zero. Returns "" when
// stdout doesn't contain a parseable is_error=true envelope.
func extractEnvelopeAPIError(raw []byte) string {
	objs := extractAllJSONObjects(raw)
	if len(objs) == 0 {
		objs = [][]byte{raw}
	}
	for _, obj := range objs {
		var env struct {
			IsError        bool     `json:"is_error"`
			APIErrorStatus int      `json:"api_error_status"`
			Subtype        string   `json:"subtype"`
			Result         string   `json:"result"`
			Errors         []string `json:"errors"`
		}
		if err := json.Unmarshal(obj, &env); err != nil || !env.IsError {
			continue
		}
		if env.APIErrorStatus > 0 && env.Result != "" {
			return fmt.Sprintf("API %d: %s", env.APIErrorStatus, env.Result)
		}
		if env.Result != "" {
			return env.Result
		}
		if len(env.Errors) > 0 {
			return strings.Join(env.Errors, "; ")
		}
		if env.Subtype != "" {
			return env.Subtype
		}
	}
	return ""
}

// truncateForError trims whitespace then caps to maxRunes with a "...(truncated)"
// marker. Used to bound stdout/stderr snippets in user-facing error messages.
func truncateForError(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	rr := []rune(s)
	if len(rr) <= maxRunes {
		return s
	}
	return string(rr[:maxRunes]) + "...(truncated)"
}
