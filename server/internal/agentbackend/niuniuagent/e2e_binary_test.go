package niuniuagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
)

// TestBackendAgainstRealBinary drives the full integration: this Backend
// spawning the real `niuniu-agent acp` binary, which talks to the configured
// model gateway. Guarded by NIUNIU_AGENT_E2E=1 plus provider credentials, so
// hermetic CI skips it.
func TestBackendAgainstRealBinary(t *testing.T) {
	if os.Getenv("NIUNIU_AGENT_E2E") == "" {
		t.Skip("set NIUNIU_AGENT_E2E=1 to run the real-binary e2e")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		t.Skip("no provider credentials in env")
	}

	// Build the agent binary from the sibling agent/ module (repo root is four
	// levels up from this package).
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "agent", "go.mod")); err != nil {
		t.Skipf("agent module not found at %s: %v", root, err)
	}
	bin := filepath.Join(t.TempDir(), "niuniu-agent.exe")
	build := exec.Command("go", "build", "-o", bin, filepath.Join(root, "agent", "cmd", "niuniu-agent"))
	build.Dir = filepath.Join(root, "agent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build niuniu-agent: %v\n%s", err, out)
	}

	// The host env may carry a decorated model id ("GLM-5.3-Flash[1m]") that
	// gateways reject; normalize it for the child.
	env := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "ANTHROPIC_MODEL=") {
			continue
		}
		env = append(env, e)
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		if i := strings.IndexByte(m, '['); i > 0 {
			m = m[:i]
		}
		env = append(env, "ANTHROPIC_MODEL="+m)
	}

	// Fixture workspace: a go.mod for the agent to find.
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/e2e-fixture\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var decided int
	b := New(Options{
		Command: bin,
		WorkDir: ws,
		Env:     env,
		ResolvePermission: func(_ context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
			decided++
			return agentbackend.PermissionDecision{Confirmed: true}, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = b.Close(context.Background()) }()

	ch, err := b.Prompt(ctx, agentbackend.PromptRequest{
		Message: "用 Read 工具读取 go.mod，然后只回复模块名本身，不要其他内容。",
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	sawTool, sawDone, final := false, false, ""
	for ev := range ch {
		switch ev.Type {
		case agentbackend.EventToolUse:
			sawTool = true
		case agentbackend.EventText:
			final += ev.Text
		case agentbackend.EventDone:
			sawDone = true
		case agentbackend.EventError:
			t.Fatalf("turn error: %s", ev.Error)
		}
	}
	if !sawTool {
		t.Error("no tool_use event — model did not call Read")
	}
	if !sawDone {
		t.Error("no done event")
	}
	if !strings.Contains(final, "example.com/e2e-fixture") {
		t.Errorf("final text = %q, want the fixture module name", final)
	}
}
