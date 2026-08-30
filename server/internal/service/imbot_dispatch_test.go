package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// setActiveIssueForTest points a chat's active pointer at an issue (the no-thread
// binding the dispatcher scopes outbound pushes on).
func (f *imbotFixture) setActiveIssueForTest(t *testing.T, chat store.ImBotChat, issueID int64) {
	t.Helper()
	if _, err := f.q.UpdateIMBotChat(context.Background(), store.UpdateIMBotChatParams{
		BindMode: chat.BindMode, PinnedIssueID: chat.PinnedIssueID,
		ActiveIssueID: sql.NullInt64{Int64: issueID, Valid: true},
		Status:        chat.Status, ID: chat.ID,
	}); err != nil {
		t.Fatalf("set active issue: %v", err)
	}
}

// waitForPushes polls the record adapter until it has at least n pushes or 2s.
func waitForPushes(a *recordAdapter, n int) []imbot.OutboundMessage {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		got := append([]imbot.OutboundMessage(nil), a.pushes...)
		a.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]imbot.OutboundMessage(nil), a.pushes...)
}

// snapshotPushes returns the pushes recorded so far after a short settle window —
// used to assert that NO push happened.
func snapshotPushes(a *recordAdapter) []imbot.OutboundMessage {
	time.Sleep(150 * time.Millisecond)
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]imbot.OutboundMessage(nil), a.pushes...)
}

func TestDispatcher_AgentDone_ForwardsRealReply_Titled(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	f.setActiveIssueForTest(t, chat, f.issueID)

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "这是真实的助手回复", WorkspaceId: f.wsID})

	pushes := waitForPushes(f.adapter, 1)
	if len(pushes) != 1 {
		t.Fatalf("expected 1 push, got %d", len(pushes))
	}
	got := pushes[0].Text
	if !strings.Contains(got, "这是真实的助手回复") {
		t.Fatalf("push should carry the real reply, got %q", got)
	}
	if !strings.Contains(got, "#"+itoa(f.issueID)+" 现有任务") {
		t.Fatalf("push should be titled `#<id> 名字`, got %q", got)
	}
	if pushes[0].ChatExtID != "oc_a" {
		t.Fatalf("push chat = %q, want oc_a", pushes[0].ChatExtID)
	}
}

func TestOutboundTitle_FullTitleNoTruncation(t *testing.T) {
	cases := []struct {
		id    int64
		title string
		want  string
	}{
		{506, "分析牛牛优势", "#506 分析牛牛优势"},
		{7, "一二三四五六七八九十再多几个字", "#7 一二三四五六七八九十再多几个字"}, // no cap on the outbound header
		{9, "  ", "#9 任务"}, // blank title -> 任务
	}
	for _, c := range cases {
		if got := outboundTitle(c.id, c.title); got != c.want {
			t.Errorf("outboundTitle(%d, %q) = %q, want %q", c.id, c.title, got, c.want)
		}
	}
}

func TestDispatcher_AgentDone_UnboundIssue_NoPush(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a") // active pointer NOT set to any issue

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "reply", WorkspaceId: f.wsID})

	if pushes := snapshotPushes(f.adapter); len(pushes) != 0 {
		t.Fatalf("an issue with no IM binding must not push, got %+v", pushes)
	}
}

// A chat routed to a DIFFERENT project than the finished issue's project must
// not receive the push: shared-bot dispatch is scoped by chat.project_id, not by
// the channel. (The issue belongs to f.projectID; reassign the chat elsewhere.)
func TestDispatcher_AgentDone_OtherProjectChat_NoPush(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	f.setActiveIssueForTest(t, chat, f.issueID)

	// Route this chat to a different (same-owner) project so it no longer owns
	// f.issueID's conversation for dispatch purposes.
	other, err := f.q.CreateProject(context.Background(), store.CreateProjectParams{Name: "别的项目", OwnerType: "user", OwnerID: 1})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := f.q.ReassignIMBotChat(context.Background(), store.ReassignIMBotChatParams{
		ProjectID: sql.NullInt64{Int64: other.ID, Valid: true}, ID: chat.ID,
	}); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "reply", WorkspaceId: f.wsID})

	if pushes := snapshotPushes(f.adapter); len(pushes) != 0 {
		t.Fatalf("a chat routed to another project must not receive the push, got %+v", pushes)
	}
}

func TestDispatcher_AgentDone_ThreadBound_PushesToThread(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	if _, err := f.q.CreateIMBotThread(context.Background(), store.CreateIMBotThreadParams{
		ChatID: chat.ID, ThreadExtID: "th-1", IssueID: f.issueID, WorkspaceID: f.wsID,
	}); err != nil {
		t.Fatalf("bind thread: %v", err)
	}

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "线程回复", WorkspaceId: f.wsID})

	pushes := waitForPushes(f.adapter, 1)
	if len(pushes) != 1 || pushes[0].ThreadExtID != "th-1" {
		t.Fatalf("expected push routed to thread th-1, got %+v", pushes)
	}
}

func TestDispatcher_PermissionRequest_TitledWithButtons(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	f.setActiveIssueForTest(t, chat, f.issueID)

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{
		Type: event.EventPermissionRequest, WorkspaceId: f.wsID,
		PermissionRequest: &event.PermissionRequestData{RequestID: 77},
	})

	pushes := waitForPushes(f.adapter, 1)
	if len(pushes) != 1 {
		t.Fatalf("expected 1 push, got %d", len(pushes))
	}
	if len(pushes[0].Buttons) != 3 {
		t.Fatalf("expected approve/always/deny buttons, got %+v", pushes[0].Buttons)
	}
	if !strings.Contains(pushes[0].Text, "#"+itoa(f.issueID)+" 现有任务") {
		t.Fatalf("permission push should be titled, got %q", pushes[0].Text)
	}
}

// --- agent failure reaches the chat (issue #680) -------------------------------

// TestDispatcher_AgentFailed_ForwardsReason is issue #680: agent_failed was absent
// from the forward whitelist, so a crashed agent produced NO chat message at all.
// The only visible change was the 🐂 marker disappearing, which is indistinguishable
// from a turn that is still running — the user waits forever for a reply that will
// never come.
func TestDispatcher_AgentFailed_ForwardsReason(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_fail")
	f.setActiveIssueForTest(t, chat, f.issueID)

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{
		Type:        event.EventAgentFailed,
		Content:     "claude CLI not available: exec: \"claude\": executable file not found in $PATH",
		WorkspaceId: f.wsID,
	})

	pushes := waitForPushes(f.adapter, 1)
	if len(pushes) != 1 {
		t.Fatalf("agent_failed pushed %d messages, want 1", len(pushes))
	}
	got := pushes[0].Text
	// The reason is the actionable half — a generic "出错了" only tells someone to go
	// look elsewhere.
	if !strings.Contains(got, "executable file not found") {
		t.Errorf("failure push = %q, want it to carry the underlying reason", got)
	}
	// Still titled, so the message is attributable to a task like every other push.
	if !strings.Contains(got, "#"+itoa(f.issueID)) {
		t.Errorf("failure push = %q, want the `#<id>` header", got)
	}
	// Must not read as success.
	if strings.Contains(got, "✅") {
		t.Errorf("failure push = %q, must not look like a success", got)
	}
}

// TestDispatcher_AgentFailed_EmptyReasonStillSpeaks: a failure with no reason text
// must still produce a message. Silence is the bug being fixed here, so an empty
// Content falling back to silence would reintroduce it.
func TestDispatcher_AgentFailed_EmptyReasonStillSpeaks(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_fail_empty")
	f.setActiveIssueForTest(t, chat, f.issueID)

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentFailed, Content: "   ", WorkspaceId: f.wsID})

	pushes := waitForPushes(f.adapter, 1)
	if len(pushes) != 1 {
		t.Fatalf("agent_failed with blank reason pushed %d messages, want 1", len(pushes))
	}
	if !strings.Contains(pushes[0].Text, "失败") {
		t.Errorf("push = %q, want it to say the turn failed", pushes[0].Text)
	}
}

// TestDispatcher_AgentFailed_UnboundIssue_NoPush: the failure path must respect the
// same scoping as every other event — a pure-WebUI task has no IM binding, so IM
// stays quiet rather than broadcasting failures to unrelated chats.
func TestDispatcher_AgentFailed_UnboundIssue_NoPush(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_unbound") // active chat, but its pointer is NOT this issue

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentFailed, Content: "boom", WorkspaceId: f.wsID})

	if pushes := snapshotPushes(f.adapter); len(pushes) != 0 {
		t.Fatalf("unbound failure must not push, got %+v", pushes)
	}
}

// TestDispatcher_AgentFailed_ClearsProcessingMarker pins the marker half of the
// fix. A failed turn is just as over as a successful one, so the 🐂 "正在执行中"
// marker must come off — leaving it would say "still working" forever, which is
// the same "cannot tell running from crashed" confusion this issue is about, just
// relocated from the message list to the reaction.
//
// Asserted through the DISPATCHER (not by calling clearProcessingReactions
// directly, which the inbound test already does): the routing decision "an
// agent_failed event clears the marker" is exactly what regressed, and a direct
// call cannot catch it. Verified by reverting the `|| EventAgentFailed` clause —
// without this test the whole suite still passes.
func TestDispatcher_AgentFailed_ClearsProcessingMarker(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	// Deliver a message so a 🐂 marker is placed and recorded under the workspace.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_task",
		Text: "帮我做张表", Kind: "message", EventID: "e1",
	})
	if len(f.adapter.reactions) != 1 {
		t.Fatalf("expected the 🐂 marker to be placed, got %d", len(f.adapter.reactions))
	}

	bus := event.NewBus()
	d := NewIMBotDispatcher(bus, f.q, f.svc, f.svc.adapters)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{
		Type: event.EventAgentFailed, Content: "boom", WorkspaceId: f.wsID,
	})

	// The failure message lands; the marker is cleared on the way there. Waiting on
	// the push keeps this deterministic — clearing happens before the push, so a
	// delivered message means clearing has already run.
	if pushes := waitForPushes(f.adapter, 1); len(pushes) != 1 {
		t.Fatalf("agent_failed pushed %d messages, want 1", len(pushes))
	}
	f.adapter.mu.Lock()
	removed := append([]string(nil), f.adapter.removed...)
	f.adapter.mu.Unlock()
	if len(removed) != 1 || removed[0] != "rid-om_task" {
		t.Fatalf("agent_failed must clear the 🐂 marker, removed=%+v", removed)
	}
}

// TestRenderOutbound_FailureClipsLongTrace: an agent failure can be a multi-screen
// stack trace. The chat gets the actionable head of it, not the whole thing.
func TestRenderOutbound_FailureClipsLongTrace(t *testing.T) {
	long := strings.Repeat("堆栈", failureDetailMaxRunes)
	text, buttons := renderOutbound(event.OutputEvent{
		Type: event.EventAgentFailed, Content: long,
	}, 42, "任务")

	if buttons != nil {
		t.Errorf("failure message carried %d buttons, want none", len(buttons))
	}
	if r := []rune(text); len(r) > failureDetailMaxRunes+120 {
		t.Errorf("failure message is %d runes; want the trace clipped near %d",
			len(r), failureDetailMaxRunes)
	}
}

// TestInterestedIn_ForwardsFailureNotWorkspaceCompleted pins both halves of the
// whitelist decision: agent_failed must be forwarded (that is this fix), while
// workspace_completed must remain excluded — it duplicates the agent_done reply and
// re-creates the old "『…』已完成" spam.
func TestInterestedIn_ForwardsFailureNotWorkspaceCompleted(t *testing.T) {
	if !interestedIn(event.EventAgentFailed) {
		t.Error("agent_failed is not forwarded; a crashed agent stays silent in chat")
	}
	if interestedIn(event.EventWorkspaceCompleted) {
		t.Error("workspace_completed is forwarded; it duplicates agent_done")
	}
}
