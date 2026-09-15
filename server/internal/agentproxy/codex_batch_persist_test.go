package agentproxy

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// --- events channel capacity -------------------------------------------------

// TestCodexEventsChannelCapacity pins the widened notification buffer: 128
// saturated during long turns (high-frequency deltas + a slow per-event DB
// consumer), which is what made terminal turn/completed delivery backpressure
// in the first place. 512 gives the consumer far more slack, and terminal
// notifications are delivered with a BLOCKING send (see readLoop) so they are
// never dropped even under saturation.
func TestCodexEventsChannelCapacity(t *testing.T) {
	if codexEventsBuffer != 512 {
		t.Fatalf("codexEventsBuffer = %d, want 512", codexEventsBuffer)
	}
}

// --- burst dispatch persists every row ---------------------------------------

// TestCodexBurstDispatchPersistsAllToolResults pins the burst-drain contract:
// a batch of N tool_result events dispatched by ONE flushCodexBatch call must
// land every row exactly once. (The transaction-window variant of this test
// was removed together with beginPersistBatch: wrapping handleEvent's
// heterogeneous writes in an external tx deadlocked SQLite's single
// connection — the tx held the only conn while handleEvent's non-tx writes
// waited for it — and split writes across tx/non-tx on PostgreSQL. Write
// batching, if ever needed again, must live INSIDE persistEvent, never around
// handleEvent.)
func TestCodexBurstDispatchPersistsAllToolResults(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	s.cliAdapter = adapter.CodexAdapter{}
	ctx := context.Background()

	evs := make([]adapter.ParsedEvent, 0, 8)
	for i := 0; i < 8; i++ {
		evs = append(evs, adapter.ParsedEvent{
			Type: "user",
			ToolResults: []adapter.ToolResultBlock{{
				ToolUseId: "call-" + string(rune('a'+i)),
				Content:   "output-" + string(rune('a'+i)),
			}},
		})
	}
	s.dispatchCodexEvents(ctx, evs)

	rows, err := s.q.ListAgentMessages(ctx, store.ListAgentMessagesParams{
		WorkspaceID: s.workspaceID,
		Limit:       100,
		Offset:      0,
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	toolResults := 0
	seen := map[string]bool{}
	for _, r := range rows {
		if r.EventType == "tool_result" {
			toolResults++
			seen[r.Content] = true
		}
	}
	if toolResults != 8 {
		t.Fatalf("persisted %d tool_result rows, want 8 — burst dispatch must not lose rows", toolResults)
	}
	for _, want := range []string{"output-a", "output-b", "output-c", "output-d", "output-e", "output-f", "output-g", "output-h"} {
		if !seen[want] {
			t.Fatalf("missing tool_result content %q", want)
		}
	}
}
