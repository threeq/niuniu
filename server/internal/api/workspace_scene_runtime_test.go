package api

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
)

func TestDetectMissingRuntimes(t *testing.T) {
	// "go" is on PATH while running `go test`; a bogus command never is.
	proj := &service.Projection{
		MCPConfigs: map[string]map[string]any{
			"present": {"command": "go"},
			"missing": {"command": "niuniu-no-such-binary-xyz"},
			"empty":   {"args": []any{"x"}}, // no command → ignored
		},
	}
	got := detectMissingRuntimes(proj)
	if len(got) != 1 || got[0] != "niuniu-no-such-binary-xyz" {
		t.Fatalf("expected only the bogus command missing, got %v", got)
	}

	if r := detectMissingRuntimes(nil); len(r) != 0 {
		t.Fatalf("nil projection should yield empty, got %v", r)
	}
	if r := detectMissingRuntimes(&service.Projection{}); len(r) != 0 {
		t.Fatalf("no MCPConfigs should yield empty, got %v", r)
	}
}
