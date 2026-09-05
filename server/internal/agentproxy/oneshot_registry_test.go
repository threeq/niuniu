package agentproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// TestOneShotRegistryCoversEveryOneShotAdapter is the invariant that makes the
// registry safe: every adapter declaring ProcessOneShot MUST have a builder
// registered, or buildOneShotExec fails at runtime with "no one-shot exec
// builder registered".
//
// Before the registry this was an if-chain in codex_exec.go; adding a one-shot
// engine and forgetting the branch silently fell through to the Codex builder
// and spawned the WRONG BINARY. Now a missing registration fails here instead.
func TestOneShotRegistryCoversEveryOneShotAdapter(t *testing.T) {
	// Every cli_type niuniu admits (keep in sync with service.ValidCliTypes and
	// the workspaces.cli_type CHECK).
	all := []adapter.Type{
		adapter.TypeClaude,
		adapter.TypeCodex,
		adapter.TypeQwen,
		adapter.TypeOmp,
		adapter.TypeGoose,
		adapter.TypeCursor,
	}
	for _, tp := range all {
		a := adapter.For(tp)
		_, registered := oneShotExecBuilders[tp]
		switch a.ProcessMode() {
		case adapter.ProcessOneShot:
			if !registered {
				t.Errorf("cli_type %q is ProcessOneShot but has no builder in oneShotExecBuilders", tp)
			}
		case adapter.ProcessLongRunning:
			if registered {
				t.Errorf("cli_type %q is ProcessLongRunning but is registered as a one-shot builder", tp)
			}
		}
	}
}

// TestOneShotRegistryRejectsLongRunningEngine locks the fail-loud contract: a
// long-running engine reaching the one-shot runner is a dispatch bug in Send,
// and must surface as an error rather than silently spawning another engine's
// binary (the pre-refactor behavior fell through to Codex).
func TestOneShotRegistryRejectsLongRunningEngine(t *testing.T) {
	s := &WorkspaceSession{cliAdapter: adapter.GooseAdapter{}}
	_, _, _, err := s.buildOneShotExec(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a long-running engine, got nil")
	}
	if !strings.Contains(err.Error(), "no one-shot exec builder registered") {
		t.Fatalf("error = %q, want it to name the missing registration", err)
	}
	if !strings.Contains(err.Error(), string(adapter.TypeGoose)) {
		t.Fatalf("error = %q, want it to name the offending cli_type", err)
	}
}

// TestOneShotRegistryDispatchesToRegisteredBuilder verifies the table is
// actually consulted (not bypassed) and that each engine's builder is the one
// invoked — asserted via the default binary name each produces.
//
// Uses a nil-store session, so builders that query the DB would panic; instead
// of a live DB this drives the pure part by swapping in a stub table, which also
// documents that dispatch is table-driven and therefore testable in isolation.
func TestOneShotRegistryDispatchesToRegisteredBuilder(t *testing.T) {
	orig := oneShotExecBuilders
	defer func() { oneShotExecBuilders = orig }()

	called := ""
	oneShotExecBuilders = map[adapter.Type]oneShotExecBuilder{
		adapter.TypeCursor: func(_ *WorkspaceSession, _ context.Context, _ string) (string, []string, []string, error) {
			called = "cursor"
			return "cursor-agent", nil, nil, nil
		},
		adapter.TypeQwen: func(_ *WorkspaceSession, _ context.Context, _ string) (string, []string, []string, error) {
			called = "qwen"
			return "qwen", nil, nil, nil
		},
	}

	s := &WorkspaceSession{cliAdapter: adapter.CursorAdapter{}}
	cmd, _, _, err := s.buildOneShotExec(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("buildOneShotExec err: %v", err)
	}
	if called != "cursor" || cmd != "cursor-agent" {
		t.Fatalf("dispatched to %q returning %q, want the cursor builder", called, cmd)
	}
}

// TestOneShotExecNilAdapterFallsBackToCodex pins the legacy contract that
// buildCodexAppServerRuntime (codex_appserver.go) depends on: a session with no
// adapter resolves as Codex. Asserted without touching the DB by confirming
// dispatch does NOT go through the table (a nil adapter has no Type to look up).
func TestOneShotExecNilAdapterFallsBackToCodex(t *testing.T) {
	orig := oneShotExecBuilders
	defer func() { oneShotExecBuilders = orig }()

	tableConsulted := false
	oneShotExecBuilders = map[adapter.Type]oneShotExecBuilder{
		adapter.TypeCodex: func(_ *WorkspaceSession, _ context.Context, _ string) (string, []string, []string, error) {
			tableConsulted = true
			return "", nil, nil, nil
		},
	}

	s := &WorkspaceSession{cliAdapter: nil}
	// The real Codex builder queries the store, so this call is expected to fail
	// on the nil store rather than complete — what matters is that it did NOT
	// return the "no builder registered" dispatch error, i.e. it took the
	// direct-fallback path instead of a table lookup on a nil adapter.
	func() {
		defer func() { _ = recover() }()
		_, _, _, err := s.buildOneShotExec(context.Background(), t.TempDir())
		if err != nil && strings.Contains(err.Error(), "no one-shot exec builder registered") {
			t.Errorf("nil adapter took the table path and failed dispatch; want direct Codex fallback")
		}
	}()
	if tableConsulted {
		t.Error("nil adapter went through the registry table; want a direct buildCodexOneShotExec call")
	}
}
