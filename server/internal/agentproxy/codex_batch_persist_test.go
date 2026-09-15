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
// in the first place. 512 gives the consumer far more slack; the REAL fix for
// saturation remains the batched transactional consumer below.
func TestCodexEventsChannelCapacity(t *testing.T) {
	if codexEventsBuffer != 512 {
		t.Fatalf("codexEventsBuffer = %d, want 512", codexEventsBuffer)
	}
}

// --- batch persistence: a burst of tool_results lands in one transaction -----

// TestCodexBatchPersistPersistsAllToolResults drives the batched write path:
// N tool_result events dispatched inside ONE persist-batch window must land
// every row exactly once, in order. This is the write-throughput fix for the
// team-edition queue stall: each event used to be its own transaction (one
// commit per row — one network round-trip to PostgreSQL each), which made the
// consumer slower than the producer and saturated the events channel.
func TestCodexBatchPersistPersistsAllToolResults(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	if f.db == nil {
		t.Fatal("fixture must expose db for batch windows")
	}
	s.db = f.db
	s.cliAdapter = adapter.CodexAdapter{}
	ctx := context.Background()

	// Build 8 tool_result events the way codex exec-end notifications parse.
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

	// ONE batch window for all 8 events → one commit.
	s.beginPersistBatch(ctx)
	s.dispatchCodexEvents(ctx, evs)
	s.commitPersistBatch(ctx)

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
		t.Fatalf("persisted %d tool_result rows, want 8 — the batch window must not lose rows", toolResults)
	}
	// Every row present (row order is uuid-based within the same committed
	// instant; delivery order is carried by the SSE broadcasts, not the table).
	for _, want := range []string{"output-a", "output-b", "output-c", "output-d", "output-e", "output-f", "output-g", "output-h"} {
		if !seen[want] {
			t.Fatalf("missing tool_result content %q", want)
		}
	}
}

// TestCodexUnbatchedPersistStillWorks pins the default path: without a batch
// window (claude readLoop, etc.) writes go straight through s.q as before.
func TestCodexUnbatchedPersistStillWorks(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	s.cliAdapter = adapter.CodexAdapter{}
	ctx := context.Background()

	evs := []adapter.ParsedEvent{{
		Type: "user",
		ToolResults: []adapter.ToolResultBlock{{
			ToolUseId: "call-x",
			Content:   "output-x",
		}},
	}}
	s.dispatchCodexEvents(ctx, evs)

	rows, err := s.q.ListAgentMessages(ctx, store.ListAgentMessagesParams{
		WorkspaceID: s.workspaceID,
		Limit:       100,
		Offset:      0,
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(rows) != 1 || rows[0].Content != "output-x" {
		t.Fatalf("unbatched path broken: %d rows", len(rows))
	}
}
