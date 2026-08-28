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

// The generic path must target each CLI's real non-interactive mode, and must
// refuse the session-protocol backends rather than mis-spawning them.
func TestOneShotArgv_PerCLI(t *testing.T) {
	if cmd, args := oneShotArgv(adapter.TypeCodex, ""); cmd != "codex" ||
		!strings.Contains(strings.Join(args, " "), "exec") {
		t.Errorf("codex argv = %q %v", cmd, args)
	}
	if cmd, _ := oneShotArgv(adapter.TypeQwen, ""); cmd != "qwen" {
		t.Errorf("qwen command = %q", cmd)
	}
	// Caller-supplied binary name wins (custom install path).
	if cmd, _ := oneShotArgv(adapter.TypeCodex, "/opt/codex"); cmd != "/opt/codex" {
		t.Errorf("command override ignored: %q", cmd)
	}
	// omp/goose are RPC/ACP session protocols with no print mode.
	for _, tt := range []adapter.Type{adapter.TypeOmp, adapter.TypeGoose} {
		if cmd, _ := oneShotArgv(tt, ""); cmd != "" {
			t.Errorf("%s should have no one-shot argv, got %q", tt, cmd)
		}
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

// The account/config dir maps to each CLI's own variable.
func TestOneShotEnv_ConfigDirPerCLI(t *testing.T) {
	codex := strings.Join(oneShotEnv(context.Background(), adapter.TypeCodex, "/cfg", nil), "\n")
	if !strings.Contains(codex, "CODEX_HOME=/cfg") {
		t.Errorf("codex config dir not exported as CODEX_HOME")
	}
	claude := strings.Join(oneShotEnv(context.Background(), adapter.TypeClaude, "/cfg", nil), "\n")
	if !strings.Contains(claude, "CLAUDE_CONFIG_DIR=/cfg") {
		t.Errorf("claude config dir not exported as CLAUDE_CONFIG_DIR")
	}
}
