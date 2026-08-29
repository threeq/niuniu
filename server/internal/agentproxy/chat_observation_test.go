package agentproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// The non-Claude one-shot path has no --json-schema, so the model's answer is
// recovered by ParseOneShotOutput from whatever the CLI prints. These pin the
// shapes those CLIs actually emit; if any fails, the Agent employee silently
// never speaks on that backend (the analyzer errors and the caller stays quiet by
// design), which is exactly the failure mode that is hard to notice in production.
func TestParseOneShotOutput_NonClaudeShapes(t *testing.T) {
	type verdict struct {
		Action  string `json:"action"`
		Message string `json:"message"`
	}
	cases := []struct {
		name string
		raw  string
	}{
		{"bare object", `{"action":"notify","message":"记得交周报"}`},
		{"fenced", "```json\n{\"action\":\"notify\",\"message\":\"记得交周报\"}\n```"},
		{"prose wrapped", "Sure, here is the result:\n{\"action\":\"notify\",\"message\":\"记得交周报\"}\n"},
		{"trailing newline noise", "{\"action\":\"notify\",\"message\":\"记得交周报\"}\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v verdict
			if err := ParseOneShotOutput([]byte(tc.raw), &v); err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if v.Action != "notify" || v.Message != "记得交周报" {
				t.Errorf("got %+v", v)
			}
		})
	}
}

// A CLI that prints nothing usable must produce an error, not a zero-valued
// verdict: a silently-empty verdict would normalize to "none" and look exactly
// like a considered decision to stay quiet.
func TestParseOneShotOutput_UnusableIsAnError(t *testing.T) {
	var v struct {
		Action string `json:"action"`
	}
	for _, raw := range []string{"", "   ", "error: not logged in"} {
		if err := ParseOneShotOutput([]byte(raw), &v); err == nil {
			t.Errorf("expected an error for %q, got a silent zero verdict", raw)
		}
	}
}

// The generic path must target each CLI's real non-interactive mode. Every
// backend niuniu supports has one; the flags differ per CLI, and for omp/goose
// this mode is a different surface from the RPC/ACP protocol their interactive
// adapters speak — so these assertions pin the one-shot flags specifically.
func TestOneShotArgv_PerCLI(t *testing.T) {
	joined := func(tt adapter.Type) (string, string) {
		cmd, args := oneShotArgv(tt, "")
		return cmd, strings.Join(args, " ")
	}

	if cmd, args := joined(adapter.TypeCodex); cmd != "codex" || !strings.Contains(args, "exec") {
		t.Errorf("codex argv = %q %q", cmd, args)
	}
	if cmd, _ := joined(adapter.TypeQwen); cmd != "qwen" {
		t.Errorf("qwen command = %q", cmd)
	}
	// omp: `-p` is its documented non-interactive print mode. It must NOT be
	// `--mode rpc` (that is the interactive adapter's surface), and must not carry
	// --print-thoughts, which would interleave thinking blocks into the JSON.
	cmd, args := joined(adapter.TypeOmp)
	if cmd != "omp" || !strings.Contains(args, "-p") {
		t.Errorf("omp argv = %q %q, want the -p print mode", cmd, args)
	}
	if strings.Contains(args, "rpc") || strings.Contains(args, "print-thoughts") {
		t.Errorf("omp one-shot argv leaked an interactive/verbose flag: %q", args)
	}
	// goose: `run -i -` executes an instruction from stdin. -q keeps stdout to the
	// model response only, so the banner cannot precede the JSON; --no-session
	// avoids persisting a session for a throwaway analysis.
	cmd, args = joined(adapter.TypeGoose)
	if cmd != "goose" || !strings.Contains(args, "run") {
		t.Errorf("goose argv = %q %q, want the run subcommand", cmd, args)
	}
	for _, want := range []string{"-i", "-q", "--no-session"} {
		if !strings.Contains(args, want) {
			t.Errorf("goose one-shot argv missing %q: %q", want, args)
		}
	}
	if strings.Contains(args, "acp") {
		t.Errorf("goose one-shot argv used the ACP session surface: %q", args)
	}
	// Caller-supplied binary name wins (custom install path).
	if c, _ := oneShotArgv(adapter.TypeCodex, "/opt/codex"); c != "/opt/codex" {
		t.Errorf("command override ignored: %q", c)
	}
	// An unknown cli_type must yield no argv so the caller errors out cleanly
	// instead of spawning something arbitrary.
	if c, _ := oneShotArgv(adapter.Type("banana"), ""); c != "" {
		t.Errorf("unknown cli_type produced argv %q", c)
	}
}

// Env layering is the whole point of the provider work: a project-bound provider
// must override both the host env and the global one-shot preset.
func TestOneShotEnv_LayerPrecedence(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://host.example")
	prev := OneShotProviderEnvFunc
	OneShotProviderEnvFunc = func(ctx context.Context) []string {
		return []string{"ANTHROPIC_BASE_URL=https://global-preset.example"}
	}
	t.Cleanup(func() { OneShotProviderEnvFunc = prev })

	env := oneShotEnv(context.Background(), adapter.TypeClaude, "",
		[]string{"ANTHROPIC_BASE_URL=https://project-provider.example"})

	// Later entries win in Go's exec env semantics, so assert on the LAST match.
	last := ""
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
			last = e
		}
	}
	if last != "ANTHROPIC_BASE_URL=https://project-provider.example" {
		t.Errorf("project provider did not win env precedence: %q", last)
	}
}

// The account/config dir maps to each CLI's own variable — and is deliberately
// NOT exported for backends that scope credentials some other way.
func TestOneShotEnv_ConfigDirPerCLI(t *testing.T) {
	codex := strings.Join(oneShotEnv(context.Background(), adapter.TypeCodex, "/cfg", nil), "\n")
	if !strings.Contains(codex, "CODEX_HOME=/cfg") {
		t.Errorf("codex config dir not exported as CODEX_HOME")
	}
	claude := strings.Join(oneShotEnv(context.Background(), adapter.TypeClaude, "/cfg", nil), "\n")
	if !strings.Contains(claude, "CLAUDE_CONFIG_DIR=/cfg") {
		t.Errorf("claude config dir not exported as CLAUDE_CONFIG_DIR")
	}
	// omp (--profile) and goose (GOOSE_* via its own backend) do not read a
	// Claude/Codex account dir. Exporting one would be cargo-culted noise.
	for _, tt := range []adapter.Type{adapter.TypeOmp, adapter.TypeGoose} {
		env := strings.Join(oneShotEnv(context.Background(), tt, "/cfg", nil), "\n")
		if strings.Contains(env, "CLAUDE_CONFIG_DIR=/cfg") || strings.Contains(env, "CODEX_HOME=/cfg") {
			t.Errorf("%s got an irrelevant account dir exported", tt)
		}
	}
}
