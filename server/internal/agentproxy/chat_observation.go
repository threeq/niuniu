package agentproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// This file backs the Agent employee's proactive chat analysis (issue #664).
//
// Unlike the other one-shot helpers (goal-condition suggest/classify), which are
// server-wide utilities and legitimately pin `claude`, this call runs ON BEHALF OF
// a specific project: the same project whose agent will receive any task it
// decides to start. So it honors that project's configured agent backend and its
// bound provider credentials, instead of assuming a Claude CLI logged in on the
// host. A niuniu install driving Codex or Qwen through a 智谱/DeepSeek provider
// would otherwise have a silently dead employee.

// OneShotRequest is one structured-generation call parameterized by the caller's
// resolved agent context, rather than by this package's defaults.
type OneShotRequest struct {
	// Prompt is the full instruction; the schema is embedded by the caller.
	Prompt string

	// JSONSchema, when the CLI supports it, is passed so the model's output is
	// validated and re-prompted on mismatch instead of scraped from prose.
	JSONSchema string

	// CLIType selects the agent backend ("claude" | "codex" | "qwen" | ...). Empty
	// falls back to Claude, matching adapter.For's legacy "no cli_type" semantics.
	CLIType string

	// Command overrides the executable name. Empty uses the CLI's default.
	Command string

	// ConfigDir, when set, is exported as the CLI's account/config dir
	// (CLAUDE_CONFIG_DIR / CODEX_HOME) so the subprocess authenticates the way
	// that workspace's agent does.
	ConfigDir string

	// Env is the caller-resolved provider/account environment (typically from
	// sceneenv.Resolve for the target workspace) as KEY=VALUE entries. It is
	// layered ON TOP of the host env, so a bound provider's ANTHROPIC_*/OPENAI_*
	// values win over anything stale on the host.
	Env []string
}

// RunOneShotStructured executes a single structured-generation turn using the
// caller's chosen CLI and environment, then decodes the result into dst.
//
// Every backend niuniu supports has a non-interactive mode, so all of them work
// here — but each names it differently (`claude -p`, `codex exec`, bare `qwen`,
// `omp -p`, `goose run -i -`), and for omp/goose that mode is a DIFFERENT surface
// from the RPC/ACP protocol their interactive adapters speak. See oneShotArgv.
//
// An unknown cli_type returns a clear error and the caller degrades (the employee
// simply stays quiet) rather than mis-spawning a subprocess.
func RunOneShotStructured(parentCtx context.Context, req OneShotRequest, dst any) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return errors.New("empty one-shot prompt")
	}
	t := adapter.Type(strings.TrimSpace(req.CLIType))
	switch t {
	case "", adapter.TypeClaude:
		// Claude keeps the dedicated path: it is the only CLI with --json-schema
		// (server-side output validation + re-prompting), which is strictly better
		// than parsing prose, so there is no reason to route it through the generic
		// builder and lose that.
		if !ClaudeCLIAvailable() {
			return errors.New("claude CLI not available")
		}
		out, err := runOneShotCLIWithEnv(parentCtx, req.Prompt, req.JSONSchema, req.ConfigDir, req.Env)
		if err != nil {
			return err
		}
		return ParseOneShotOutput(out, dst)
	case adapter.TypeCodex, adapter.TypeQwen, adapter.TypeOmp, adapter.TypeGoose, adapter.TypeCursor:
		out, err := runGenericOneShot(parentCtx, t, req)
		if err != nil {
			return err
		}
		return ParseOneShotOutput(out, dst)
	default:
		return fmt.Errorf("one-shot structured generation not supported for cli_type %q", req.CLIType)
	}
}

// runGenericOneShot drives a non-Claude one-shot CLI. These have no
// `--json-schema`, so the prompt (built by the caller) carries the schema and the
// output is recovered by ParseOneShotOutput's envelope/bare-JSON traversal — the
// same tolerant parser the legacy Claude path already relies on.
//
// The prompt goes on STDIN rather than argv: Qwen reads it there by design, and it
// avoids a multi-KB transcript hitting platform argv limits or shell quoting.
func runGenericOneShot(parentCtx context.Context, t adapter.Type, req OneShotRequest) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parentCtx, oneShotTimeout)
	defer cancel()

	command, args := oneShotArgv(t, req.Command)
	if command == "" {
		return nil, fmt.Errorf("no one-shot command for cli_type %q", t)
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, fmt.Errorf("%s CLI not available: %w", command, err)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = os.TempDir() // neutral cwd: no project .mcp.json / AGENTS.md bleed
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = oneShotEnv(ctx, t, req.ConfigDir, req.Env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.New("one-shot call timed out")
		}
		if msg := truncateForError(strings.TrimSpace(stderr.String()), 300); msg != "" {
			return nil, fmt.Errorf("%s subprocess: %w (stderr: %s)", command, err, msg)
		}
		return nil, fmt.Errorf("%s subprocess: %w", command, err)
	}
	return stdout.Bytes(), nil
}

// oneShotArgv returns the executable + argv for a single non-interactive,
// text-output turn per CLI. Deliberately minimal: this is a pure generation call,
// so no session persistence, no MCP, no tool access is requested.
func oneShotArgv(t adapter.Type, command string) (string, []string) {
	switch t {
	case adapter.TypeCodex:
		if command == "" {
			command = "codex"
		}
		// `exec` is Codex's non-interactive mode; the prompt arrives on stdin.
		// --skip-git-repo-check because the neutral temp cwd is not a repo.
		return command, []string{"exec", "--skip-git-repo-check"}
	case adapter.TypeQwen:
		if command == "" {
			command = "qwen"
		}
		// Qwen's headless mode also reads the prompt from stdin. Plain text output
		// (not stream-json): we want the final answer, not an event stream.
		return command, []string{}
	case adapter.TypeOmp:
		if command == "" {
			command = "omp"
		}
		// `-p` is omp's non-interactive print mode: process the prompt and exit.
		// The prompt arrives on stdin, which omp accepts (it waits for EOF, and we
		// close stdin after writing). Default output mode is `text`, i.e. only the
		// final assistant message on stdout — thinking blocks are opt-in via
		// --print-thoughts, which we do NOT want polluting the JSON we parse.
		//
		// NOTE: the omp ADAPTER uses `--mode rpc` for interactive sessions; that is
		// a different surface from this one-shot call. Do not "align" them.
		return command, []string{"-p"}
	case adapter.TypeGoose:
		if command == "" {
			command = "goose"
		}
		// `goose run` executes an instruction from stdin (`-i -`) and exits.
		//   -q            only the model response on stdout (suppresses the banner
		//                 and progress chatter that would otherwise precede the JSON)
		//   --no-session  do not persist a session for a throwaway analysis
		//
		// NOTE: the goose ADAPTER uses `goose acp` (ACP over stdio) for interactive
		// sessions — again a different surface from this one-shot call.
		return command, []string{"run", "-i", "-", "-q", "--no-session"}
	case adapter.TypeCursor:
		if command == "" {
			command = "cursor-agent"
		}
		// `-p` is cursor-agent's non-interactive print mode; piped stdin becomes
		// the prompt when no positional prompt is given. Default output is plain
		// text (the final answer only) — exactly what ParseOneShotOutput wants, so
		// no --output-format flag here.
		//
		// --trust is required even for a throwaway analysis: without it cursor
		// blocks on a workspace-trust confirmation it can never receive headlessly.
		// No --force: this call is a pure generation with no tool access needed.
		//
		// NOTE: the cursor ADAPTER uses `-p --output-format stream-json` for
		// interactive sessions; that is a different surface from this one-shot
		// call. Do not "align" them.
		return command, []string{"-p", "--trust"}
	}
	return "", nil
}

// oneShotEnv layers the environment for a one-shot subprocess:
// host env -> the marked global one-shot preset -> the caller's resolved
// provider env -> the CLI's account config dir. Later layers win, so a
// workspace-bound provider overrides a global default, which overrides the host.
func oneShotEnv(ctx context.Context, t adapter.Type, configDir string, extra []string) []string {
	env := os.Environ()
	// The globally-marked one-shot preset stays as the BASE so an install that
	// configured only that (and no per-project provider) keeps working unchanged.
	if OneShotProviderEnvFunc != nil {
		env = append(env, OneShotProviderEnvFunc(ctx)...)
	}
	// The caller's per-workspace/project provider env wins over the global one.
	env = append(env, extra...)
	if configDir != "" {
		switch t {
		case adapter.TypeCodex:
			env = append(env, "CODEX_HOME="+configDir)
		case "", adapter.TypeClaude, adapter.TypeQwen:
			// Qwen's CLI is a Claude-Code fork and honors the same variable.
			env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
		default:
			// omp and goose scope credentials differently (omp via --profile, goose
			// via GOOSE_* env resolved by its own backend), so a niuniu Claude/Codex
			// account dir means nothing to them. Exporting CLAUDE_CONFIG_DIR here
			// would be cargo-culting — silently ignored at best, and misleading to
			// the next reader. Leave it unset and let the CLI use its own config.
		}
	}
	// Resolve the ANTHROPIC_AUTH_TOKEN vs ANTHROPIC_API_KEY conflict the same way
	// a real workspace spawn does, so a third-party provider's bearer token is not
	// undercut by a stale host api-key.
	return adapter.SanitizeAnthropicEnv(env)
}
