package agentproxy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// stubGate captures PermissionService.Request invocations.
type stubGate struct {
	mu       sync.Mutex
	calls    int
	gotTool  string
	behavior string
	err      error
}

func (g *stubGate) Request(
	ctx context.Context,
	workspaceID int64, ownerType string, ownerID int64,
	sessionID, toolName string, toolInput map[string]any,
) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.gotTool = toolName
	return g.behavior, g.err
}

// TestHandleCodexApprovalRequest_ApprovedFlow verifies a happy-path approval:
// PermissionGate returns "allow", handler encodes approved response on stdin.
func TestHandleCodexApprovalRequest_ApprovedFlow(t *testing.T) {
	gate := &stubGate{behavior: "allow"}
	s := &WorkspaceSession{
		workspaceID:    1,
		ownerType:      "user",
		ownerID:        42,
		permissionGate: gate,
		cliAdapter:     adapter.CodexAdapter{},
	}
	req := &adapter.ApprovalRequest{
		ID:   float64(7), // JSON numbers decode as float64
		Tool: "exec",
		Args: map[string]any{"command": "ls -la"},
	}
	var written []byte
	writer := func(b []byte) error {
		written = append(written, b...)
		return nil
	}
	s.handleCLIApprovalRequest(context.Background(), req, writer)

	if gate.calls != 1 {
		t.Fatalf("expected 1 PermissionGate call, got %d", gate.calls)
	}
	if gate.gotTool != "codex:exec" {
		t.Errorf("toolName=%q want codex:exec", gate.gotTool)
	}

	var got map[string]any
	if err := json.Unmarshal(written, &got); err != nil {
		t.Fatalf("written stdin not valid JSON: %v\nbytes=%q", err, written)
	}
	if got["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc=%v want 2.0", got["jsonrpc"])
	}
	result, _ := got["result"].(map[string]any)
	if result["decision"] != "approved" {
		t.Errorf("decision=%v want approved", result["decision"])
	}
	if written[len(written)-1] != '\n' {
		t.Errorf("response must be newline-terminated; got bytes=%q", written)
	}
}

// TestHandleCodexApprovalRequest_DeniedOnGateError: when PermissionService
// errors, we must auto-deny rather than crash.
func TestHandleCodexApprovalRequest_DeniedOnGateError(t *testing.T) {
	gate := &stubGate{err: errors.New("service unavailable")}
	s := &WorkspaceSession{
		workspaceID:    1,
		ownerType:      "user",
		ownerID:        42,
		permissionGate: gate,
		cliAdapter:     adapter.CodexAdapter{},
	}
	req := &adapter.ApprovalRequest{ID: "abc", Tool: "patch"}
	var written []byte
	s.handleCLIApprovalRequest(context.Background(), req, func(b []byte) error {
		written = append(written, b...)
		return nil
	})
	var got map[string]any
	_ = json.Unmarshal(written, &got)
	result, _ := got["result"].(map[string]any)
	if result["decision"] != "denied" {
		t.Errorf("decision=%v want denied (gate erred)", result["decision"])
	}
	// Round-trip the id (string) too.
	if got["id"] != "abc" {
		t.Errorf("id=%v want abc", got["id"])
	}
}

// TestHandleCodexApprovalRequest_DeniesWithNoGate: PermissionGate nil →
// deny by default (safe failure mode for tests / partial wiring).
func TestHandleCodexApprovalRequest_DeniesWithNoGate(t *testing.T) {
	s := &WorkspaceSession{workspaceID: 1, ownerType: "user", ownerID: 42, cliAdapter: adapter.CodexAdapter{}}
	req := &adapter.ApprovalRequest{ID: float64(1), Tool: "web"}
	var written []byte
	s.handleCLIApprovalRequest(context.Background(), req, func(b []byte) error {
		written = append(written, b...)
		return nil
	})
	var got map[string]any
	_ = json.Unmarshal(written, &got)
	result, _ := got["result"].(map[string]any)
	if result["decision"] != "denied" {
		t.Errorf("decision=%v want denied (no gate)", result["decision"])
	}
}
