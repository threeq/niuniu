package adapter

import (
	"strings"
	"testing"
)

func TestForReturnsCursorAdapter(t *testing.T) {
	a := For(TypeCursor)
	if _, ok := a.(CursorAdapter); !ok {
		t.Fatalf("For(TypeCursor) = %T, want CursorAdapter", a)
	}
	if a.Type() != TypeCursor {
		t.Fatalf("Type() = %q, want %q", a.Type(), TypeCursor)
	}
}

// TestCursorProcessModeIsLongRunning pins the routing contract: cursor is driven
// by agentbackend/cursor over ACP, not by the one-shot stdout runner. If this
// ever flips to ProcessOneShot, agentproxy.Send would fall through to
// runOneShotTurn and buildOneShotExec would fail with "no one-shot exec builder
// registered" (see oneshot_registry_test.go, which asserts the same invariant
// from the other side).
func TestCursorProcessModeIsLongRunning(t *testing.T) {
	if got := (CursorAdapter{}).ProcessMode(); got != ProcessLongRunning {
		t.Fatalf("ProcessMode() = %q, want %q", got, ProcessLongRunning)
	}
}

func TestCursorDisplayName(t *testing.T) {
	cases := map[string]string{
		// An unset command must NOT report "agent": Cursor's binary is named
		// `agent`, which is a uselessly generic label in the UI.
		"":                            "cursor",
		"cursor-agent":                "cursor-agent",
		"/usr/local/bin/cursor-agent": "cursor-agent",
		"C:\\bin\\cursor-agent.exe":   "cursor-agent",
	}
	for cmd, want := range cases {
		if got := (CursorAdapter{}).DisplayName(cmd); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// TestCursorBuildSpawnIsACP documents that the marker adapter targets `agent acp`
// and never the headless print mode. The real argv is assembled by
// agentbackend/cursor (which must place --model BEFORE the acp subcommand).
func TestCursorBuildSpawnIsACP(t *testing.T) {
	cmd, args := (CursorAdapter{}).BuildSpawn(SpawnOptions{})
	if cmd != "agent" {
		t.Fatalf("command = %q, want agent", cmd)
	}
	joined := strings.Join(args, " ")
	if joined != "acp" {
		t.Fatalf("args = %v, want exactly [acp]", args)
	}
	// The old headless integration is gone; these must never come back here.
	for _, banned := range []string{"-p", "--output-format", "--force", "--trust"} {
		if strings.Contains(joined, banned) {
			t.Errorf("args %v must not contain headless flag %q", args, banned)
		}
	}
}

// TestCursorParseLineUnused guards the marker contract: parsing lives in the ACP
// backend, so a stray stdout line must never be turned into an event here.
func TestCursorParseLineUnused(t *testing.T) {
	evs, err := (CursorAdapter{}).ParseLine(`{"type":"assistant","message":{}}`)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("ParseLine produced %d events, want 0 (ACP backend owns parsing)", len(evs))
	}
}

// TestCursorPermissionArgsNil pins that permissions are negotiated in-protocol
// (session/request_permission), not via --force on the command line.
func TestCursorPermissionArgsNil(t *testing.T) {
	for _, mode := range []string{"", "default", "plan", "acceptEdits", "bypassPermissions", AutohostMode} {
		if got := (CursorAdapter{}).PermissionArgs(PermissionOptions{Mode: mode}); got != nil {
			t.Errorf("PermissionArgs(mode=%q) = %v, want nil", mode, got)
		}
	}
}

func TestCursorInjectEnvPassthrough(t *testing.T) {
	base := []string{"PATH=/bin", "CURSOR_API_KEY=key_x"}
	out := (CursorAdapter{}).InjectEnv(base, EnvOptions{
		WorkspaceEnv: []EnvVar{{Key: "FOO", Value: "bar"}},
	})
	if strings.Join(out, "\n") != strings.Join(base, "\n") {
		t.Fatalf("InjectEnv should pass the base env through unchanged, got %v", out)
	}
}
