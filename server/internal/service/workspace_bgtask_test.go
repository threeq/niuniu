package service

import (
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func TestFillBgTaskMeta_NoSession_NoOp(t *testing.T) {
	ap := agentproxy.NewAgentProxy(nil, nil)
	t.Cleanup(ap.Stop)
	meta := &WorkspaceSidebarMeta{Workspace: store.Workspace{ID: 1}}
	fillBgTaskMeta(meta, ap)
	if meta.BgAgentBusy || meta.BgBashCount != 0 || meta.BgHighlight != nil {
		t.Fatalf("expected zero values, got %+v", meta)
	}
}

func TestBgHighlightPartition_BashWinsOverWakeup(t *testing.T) {
	// Tier 1 (non-wakeup) always beats tier 2 (wakeup), regardless of timing.
	bashStarted := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	wakeAt := bashStarted.Add(1 * time.Second) // wakeup is "newer" by StartedAt

	tasks := []agentproxy.BgTask{
		{Kind: agentproxy.BgTaskBash, ToolUseID: "tu_b", Title: "deploy", StartedAt: bashStarted},
		{Kind: agentproxy.BgTaskWakeup, ToolUseID: "tu_w", Title: "check", StartedAt: wakeAt, ScheduledFor: wakeAt.Add(10 * time.Minute)},
	}
	highlight := pickBgHighlight(tasks)
	if highlight == nil {
		t.Fatal("expected non-nil highlight")
	}
	if highlight.Kind != "bash" {
		t.Fatalf("expected bash to win, got kind=%q", highlight.Kind)
	}
}

func TestBgHighlightPartition_TwoWakeups_SoonestWins(t *testing.T) {
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	tasks := []agentproxy.BgTask{
		{Kind: agentproxy.BgTaskWakeup, ToolUseID: "later", Title: "later", StartedAt: now, ScheduledFor: now.Add(20 * time.Minute)},
		{Kind: agentproxy.BgTaskWakeup, ToolUseID: "soon", Title: "soon", StartedAt: now, ScheduledFor: now.Add(5 * time.Minute)},
	}
	highlight := pickBgHighlight(tasks)
	if highlight == nil {
		t.Fatal("expected non-nil highlight")
	}
	if highlight.Title != "soon" {
		t.Fatalf("expected soonest wakeup to win, got title=%q", highlight.Title)
	}
}

func TestBgHighlightPartition_TwoBashes_NewestWins(t *testing.T) {
	older := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	newer := older.Add(5 * time.Minute)
	tasks := []agentproxy.BgTask{
		{Kind: agentproxy.BgTaskBash, ToolUseID: "old", Title: "old", StartedAt: older},
		{Kind: agentproxy.BgTaskBash, ToolUseID: "new", Title: "new", StartedAt: newer},
	}
	highlight := pickBgHighlight(tasks)
	if highlight == nil {
		t.Fatal("expected non-nil highlight")
	}
	if highlight.Title != "new" {
		t.Fatalf("expected newest bash to win, got title=%q", highlight.Title)
	}
}
