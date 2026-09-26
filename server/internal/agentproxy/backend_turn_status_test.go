package agentproxy

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestBackendTurn_StatusFlipsRunningToIdle covers the niuniu-agent (and
// goose/cursor/omp) chat-input badge bug: backend engines own long-lived
// processes, so nothing on their turn path ever wrote workspace.agent_status —
// the badge showed a stale state for the whole turn and the × stop button
// (rendered only for running/busy) never appeared. The shared
// markAgentRunning/signalGooseTurnDone pair must drive running→idle per turn,
// the same contract claude gets from its one-shot process spawn/exit monitor.
func TestBackendTurn_StatusFlipsRunningToIdle(t *testing.T) {
	q := setupDispatchDB(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name:      "backend-status-test",
		Path:      t.TempDir(),
		Status:    "running",
		OwnerType: "user",
		OwnerID:   42,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	hub := NewSessionHub()
	t.Cleanup(hub.Stop)
	s := &WorkspaceSession{
		workspaceID: ws.ID,
		q:           q,
		hub:         hub,
		// inflight/notifyHub nil: emitBgTaskNotify degrades gracefully.
	}

	// Turn entry: badge must be running (this is what makes the × stop button
	// render in chat-input).
	s.markAgentRunning(ctx)
	got, err := q.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.AgentStatus.String != "running" {
		t.Fatalf("agent_status at turn entry = %q, want running", got.AgentStatus.String)
	}

	// Turn end: signalGooseTurnDone (shared by goose/cursor/niuniu-agent) must
	// flip the badge back to idle.
	s.signalGooseTurnDone(ctx, "msg-1", false, "")
	got, err = q.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.AgentStatus.String != "idle" {
		t.Fatalf("agent_status after turn done = %q, want idle", got.AgentStatus.String)
	}
}
