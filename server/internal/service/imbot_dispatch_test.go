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
		{9, "  ", "#9 任务"},                                              // blank title -> 任务
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
