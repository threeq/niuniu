package agentproxy

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

type fakeStatusHook struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeStatusHook) OnAgentEvent(ctx context.Context, workspaceID int64, eventType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, eventType)
}

func (f *fakeStatusHook) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *fakeStatusHook) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) == 0 {
		return ""
	}
	return f.events[len(f.events)-1]
}

func TestResolveAgentID(t *testing.T) {
	s := &WorkspaceSession{
		topLevelAgentID: 100,
	}
	// resolveAgentID always returns the session's top-level agent.
	for _, ptid := range []string{"", "toolu_ABC", "toolu_UNK"} {
		if got := s.resolveAgentID(ptid); got != 100 {
			t.Errorf("resolveAgentID(%q) = %d, want 100", ptid, got)
		}
	}
}

func TestResultEventDefersWorkspaceStatusUntilSendLoopFinalDecision(t *testing.T) {
	hook := &fakeStatusHook{}
	s := newDispatchTestSession(t)
	s.statusHook = hook
	s.running = true
	s.turnDone = make(chan struct{}, 1)
	s.turnMsgId = "turn-1"

	s.handleEvent(context.Background(), ParsedEvent{
		Type:    "result",
		IsError: false,
		Result:  "done",
	}, "turn-1")

	if got := hook.count(); got != 0 {
		t.Fatalf("status hook fired before SendLoop final decision: got %d calls", got)
	}
	if s.lastTurnError || s.lastTurnResult != "done" {
		t.Fatalf("turn result was not recorded: error=%v result=%q", s.lastTurnError, s.lastTurnResult)
	}

	s.finalizeSendLoopTurn(context.Background(), false, s.lastTurnResult, false)
	if got := hook.last(); got != "done" {
		t.Fatalf("final status hook event = %q, want done", got)
	}
}

// TestResultEventKeepsRunningLoopScoped is the regression guard for the
// "send a message while the LLM judge is running wedges the workspace" bug:
// running/status is loop-scoped (owned by SendLoop), so a turn's result event
// must NOT clear running. Previously it did, which made the session look "idle"
// during the post-turn autohost judge and let a concurrent send spawn a second
// SendLoop on the same session (unrecoverable without a server restart).
func TestResultEventKeepsRunningLoopScoped(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = true
	s.status = StatusRunning
	s.turnDone = make(chan struct{}, 1)
	s.turnMsgId = "turn-1"

	s.handleEvent(context.Background(), ParsedEvent{Type: "result", IsError: false, Result: "done"}, "turn-1")

	s.mu.Lock()
	running, status := s.running, s.status
	s.mu.Unlock()
	if !running {
		t.Fatal("result event must NOT clear running — it is loop-scoped (owned by SendLoop)")
	}
	if status != StatusRunning {
		t.Fatalf("status must stay StatusRunning between turns, got %v", status)
	}
	select {
	case <-s.turnDone:
	default:
		t.Fatal("result event must still signal turnDone so Send() unblocks")
	}
}

// TestEnqueueQueuesWhileLoopRunning: a message sent while a SendLoop is active
// (including during the post-turn LLM judge, when running stays true) must be
// QUEUED, not reported as "idle" (which would make the dispatcher start a second
// loop).
func TestEnqueueQueuesWhileLoopRunning(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = true // a SendLoop (or its judge) is active

	queued, _, err := s.Enqueue(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !queued {
		t.Fatal("message sent during an active loop must be queued, not dispatched as a new loop")
	}
}

// TestEnqueueQueuesDuringAutohostScheduledWait: when the live SendLoop has
// returned (s.running=false) but an autohost paced bg-wait resume is scheduled
// (autohostScheduledWait=true, the workspace still shows agent_status='running'),
// an incoming message must be QUEUED, not dispatched as a fresh loop. The
// scheduled resume re-enters SendLoop and drains the queue. This is the
// "曾在运行中" window that drag-into-instruct dispatch (and manual send) hit.
func TestEnqueueQueuesDuringAutohostScheduledWait(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = false             // no live loop
	s.autohostScheduledWait = true // but a paced resume is scheduled to re-drive

	queued, _, err := s.Enqueue(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !queued {
		t.Fatal("message sent during an autohost paced-wait must be queued (the scheduled resume drains it), not dispatched as a new loop")
	}
}

// TestEnqueueQueuesWithPendingFutureWakeup: same as above but for the non-autohost
// path — a pending future agent wakeup keeps the workspace "running" (finalize
// defers needs_review), so an incoming message must queue and be drained when the
// wakeup re-drives the loop.
func TestEnqueueQueuesWithPendingFutureWakeup(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = false
	s.inflight = NewInflightTracker()
	// Future wakeup: ScheduledFor = now + 10m.
	s.inflight.AddWakeup("toolu_wake", "poll later", time.Now(), 10*time.Minute)

	queued, _, err := s.Enqueue(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !queued {
		t.Fatal("message sent while a future wakeup is pending must be queued (the wakeup re-drives and drains it), not dispatched as a new loop")
	}
}

// TestStopClearsPendingWakeup: a user interrupt (Stop) must drop a lingering
// future wakeup so the Enqueue gate reopens — a manual send right after Stop runs
// immediately instead of re-queueing behind the agent's stale "check back later"
// timer. This is the fix for the "message only queues, can't interrupt" trap when
// agent_status is idle but a ScheduleWakeup still sits in inflight. Tests the
// clearPendingWakeups helper that Stop() calls (Stop() itself needs a full
// session harness: DB, hub, statusHook).
func TestStopClearsPendingWakeup(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = false
	s.autohostScheduledWait = false
	s.inflight = NewInflightTracker()
	s.inflight.AddWakeup("toolu_wake", "poll later", time.Now(), 10*time.Minute)

	// Precondition: the pending wakeup holds the gate closed, so a send queues.
	if !s.hasPendingFutureWakeup() {
		t.Fatal("precondition: expected a pending future wakeup")
	}
	queued, _, err := s.Enqueue(context.Background(), "before", "")
	if err != nil {
		t.Fatalf("Enqueue (before): %v", err)
	}
	if !queued {
		t.Fatal("precondition: a send while a future wakeup is pending must queue")
	}

	// User interrupt clears the wakeup.
	s.clearPendingWakeups()

	if s.hasPendingFutureWakeup() {
		t.Fatal("clearPendingWakeups must drop the pending future wakeup")
	}
	queued2, _, err := s.Enqueue(context.Background(), "after", "")
	if err != nil {
		t.Fatalf("Enqueue (after): %v", err)
	}
	if queued2 {
		t.Fatal("after Stop cleared the wakeup, a manual send must run immediately, not queue")
	}
}

// TestUserTakeoverReopensGateOnIdleScheduledWait is the regression guard for the
// "工作空间空闲却发消息不能启动、自动进入队列排队" bug: after an autohost paced
// bg-wait or an agent ScheduleWakeup, the live loop returns and the session shows
// idle, but autohostScheduledWait / a pending future wakeup keeps the Enqueue gate
// closed — so a manual send silently queues until the scheduled resume fires. A
// genuine user send must TAKE OVER (userTakeoverClearWait): clear the gate-holding
// state so the very next Enqueue runs immediately instead of queueing. Covers both
// gate-holding sources and the no-op-while-running guard. parent is nil here, so
// the scheduler CancelAutoResume call is skipped (covered by scheduler tests).
func TestUserTakeoverReopensGateOnIdleScheduledWait(t *testing.T) {
	t.Run("autohostScheduledWait", func(t *testing.T) {
		s := newDispatchTestSession(t)
		s.running = false
		s.autohostScheduledWait = true
		s.inflight = NewInflightTracker()

		queued, _, err := s.Enqueue(context.Background(), "before", "")
		if err != nil {
			t.Fatalf("Enqueue (before): %v", err)
		}
		if !queued {
			t.Fatal("precondition: a send during an autohost paced-wait must queue")
		}

		if !s.userTakeoverClearWait(context.Background()) {
			t.Fatal("user takeover must report it cleared a gate-holding wait")
		}
		queued2, _, err := s.Enqueue(context.Background(), "after", "")
		if err != nil {
			t.Fatalf("Enqueue (after): %v", err)
		}
		if queued2 {
			t.Fatal("after user takeover, a manual send must run immediately, not queue")
		}
	})

	t.Run("pendingFutureWakeup", func(t *testing.T) {
		s := newDispatchTestSession(t)
		s.running = false
		s.inflight = NewInflightTracker()
		s.inflight.AddWakeup("toolu_wake", "poll later", time.Now(), 10*time.Minute)

		if !s.userTakeoverClearWait(context.Background()) {
			t.Fatal("user takeover must clear a pending future wakeup")
		}
		if s.hasPendingFutureWakeup() {
			t.Fatal("userTakeoverClearWait must drop the pending future wakeup")
		}
		queued, _, err := s.Enqueue(context.Background(), "after", "")
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if queued {
			t.Fatal("after user takeover cleared the wakeup, a manual send must run immediately")
		}
	})

	t.Run("noTakeoverWhileLoopRunning", func(t *testing.T) {
		s := newDispatchTestSession(t)
		s.running = true // a live SendLoop owns the session
		s.autohostScheduledWait = true

		if s.userTakeoverClearWait(context.Background()) {
			t.Fatal("a live loop is running — takeover must be a no-op so the send queues behind it")
		}
		s.mu.Lock()
		stillWaiting := s.autohostScheduledWait
		s.mu.Unlock()
		if !stillWaiting {
			t.Fatal("takeover must NOT clear autohostScheduledWait while a loop is running")
		}
	})

	t.Run("noTakeoverWhenTrulyIdle", func(t *testing.T) {
		s := newDispatchTestSession(t)
		s.running = false
		s.autohostScheduledWait = false
		s.inflight = NewInflightTracker()

		if s.userTakeoverClearWait(context.Background()) {
			t.Fatal("nothing was holding the gate — takeover must report no-op")
		}
	})
}

// TestEnqueueDoesNotQueueWhenTrulyIdle: the negative case — no live loop, no
// scheduled resume, no pending wakeup — must NOT queue (queuing would strand the
// message since nothing will drain it); Deliver starts a fresh loop instead.
func TestEnqueueDoesNotQueueWhenTrulyIdle(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = false
	s.autohostScheduledWait = false
	s.inflight = NewInflightTracker() // empty: no pending wakeup

	queued, _, err := s.Enqueue(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if queued {
		t.Fatal("a truly idle session must NOT queue (nothing would drain it) — Deliver should start a fresh loop")
	}
}

// TestSendLoopRejectsConcurrentStart: starting a SendLoop while one is already
// active must abort immediately (before resetting autohost state) and leave the
// existing loop's running flag untouched — no two loops on one session.
func TestSendLoopRejectsConcurrentStart(t *testing.T) {
	s := newDispatchTestSession(t)
	s.running = true // existing loop active
	s.autohostConsec = 7

	s.SendLoop(context.Background(), t.TempDir(), "msg", "")

	s.mu.Lock()
	consec, running := s.autohostConsec, s.running
	s.mu.Unlock()
	if consec != 7 {
		t.Fatalf("SendLoop must abort before autohostReset when a loop is active; autohostConsec=%d, want 7", consec)
	}
	if !running {
		t.Fatal("the existing loop's running flag must be left untouched")
	}
}

// TestFinalizeSendLoopTurn_NonAutohostDefersForPendingWakeup locks the flicker
// fix (commit 74643cc3): outside autohost, a still-pending agent wakeup means
// the agent will be re-woken, so finalize must NOT flip the workspace to
// needs_review yet.
func TestFinalizeSendLoopTurn_NonAutohostDefersForPendingWakeup(t *testing.T) {
	hook := &fakeStatusHook{}
	s := newDispatchTestSession(t)
	s.statusHook = hook
	s.inflight = NewInflightTracker()
	// Future wakeup: ScheduledFor = now + 10m.
	s.inflight.AddWakeup("toolu_wake", "poll later", time.Now(), 10*time.Minute)

	s.finalizeSendLoopTurn(context.Background(), false, "done", false /*autohostTerminalStop*/)

	if got := hook.count(); got != 0 {
		t.Fatalf("expected status flip deferred while a future wakeup is pending, got %d hook calls", got)
	}
}

// TestFinalizeSendLoopTurn_DefersWhenAutohostScheduledWait: when the autohost
// watchdog scheduled a paced resume to wait for bg work, finalize must keep the
// workspace "running" (the resume will re-drive it), not flip to needs_review.
func TestFinalizeSendLoopTurn_DefersWhenAutohostScheduledWait(t *testing.T) {
	hook := &fakeStatusHook{}
	s := newDispatchTestSession(t)
	s.statusHook = hook
	s.mu.Lock()
	s.autohostScheduledWait = true
	s.mu.Unlock()

	s.finalizeSendLoopTurn(context.Background(), false, "done", false /*autohostTerminalStop*/)

	if got := hook.count(); got != 0 {
		t.Fatalf("expected status flip deferred while an autohost paced resume is pending, got %d hook calls", got)
	}
}

// TestFinalizeSendLoopTurn_BareBashBgDoesNotDefer guards against the non-autohost
// regression: a bare Bash[bg] with NO pending wakeup and NO scheduled resume must
// still flip to needs_review (there is no resume mechanism to clear it otherwise).
func TestFinalizeSendLoopTurn_BareBashBgDoesNotDefer(t *testing.T) {
	hook := &fakeStatusHook{}
	s := newDispatchTestSession(t)
	s.statusHook = hook
	s.inflight = NewInflightTracker()
	s.inflight.Add(BgTaskBash, "toolu_bash", "long build", time.Now())

	s.finalizeSendLoopTurn(context.Background(), false, "done", false /*autohostTerminalStop*/)

	if got := hook.last(); got != "done" {
		t.Fatalf("bare Bash[bg] (no wakeup / no scheduled resume) must still flip; status hook = %q, want done", got)
	}
}

// TestFinalizeSendLoopTurn_AutohostStopTransitionsDespitePendingWakeup is the
// regression guard for the reported bug: in autohost mode the watchdog drives
// continuation (continue-prompts), not wakeups. When it terminally stops
// (LLM judge met / [AUTOHOST_DONE] / budget), no wakeup will re-trigger a Send,
// so a stale pending wakeup must NOT block the needs_review transition.
func TestFinalizeSendLoopTurn_AutohostStopTransitionsDespitePendingWakeup(t *testing.T) {
	hook := &fakeStatusHook{}
	s := newDispatchTestSession(t)
	s.statusHook = hook
	s.inflight = NewInflightTracker()
	// Same stale future wakeup as the non-autohost case — only the terminal-stop
	// flag differs.
	s.inflight.AddWakeup("toolu_wake", "poll later", time.Now(), 10*time.Minute)

	s.finalizeSendLoopTurn(context.Background(), false, "done", true /*autohostTerminalStop*/)

	if got := hook.last(); got != "done" {
		t.Fatalf("autohost terminal stop must transition despite pending wakeup; status hook = %q, want done", got)
	}
}

// TestAutoPinUserMessage verifies the default auto-pin: a dispatched user
// message lands in pinned_messages keyed by its server messageId, with the
// preview whitespace-collapsed, and re-pinning the same id upserts (no dupes).
func TestAutoPinUserMessage(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()

	s.autoPinUserMessage(ctx, "umsg-1", "  hello   world  ")

	pins, err := s.q.ListPinnedMessages(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("ListPinnedMessages: %v", err)
	}
	if len(pins) != 1 {
		t.Fatalf("pin count = %d, want 1", len(pins))
	}
	if pins[0].MessageID != "umsg-1" {
		t.Errorf("message_id = %q, want umsg-1", pins[0].MessageID)
	}
	if pins[0].Role != "user" {
		t.Errorf("role = %q, want user", pins[0].Role)
	}
	if pins[0].Preview != "hello world" {
		t.Errorf("preview = %q, want %q (whitespace collapsed)", pins[0].Preview, "hello world")
	}

	// Re-pinning the same messageId refreshes the preview, never duplicates.
	s.autoPinUserMessage(ctx, "umsg-1", "updated text")
	pins, err = s.q.ListPinnedMessages(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("ListPinnedMessages (re-pin): %v", err)
	}
	if len(pins) != 1 {
		t.Fatalf("after re-pin, count = %d, want 1 (upsert)", len(pins))
	}
	if pins[0].Preview != "updated text" {
		t.Errorf("re-pin preview = %q, want %q", pins[0].Preview, "updated text")
	}
}

// TestPinPreviewTruncatesRuneSafe locks the rune-safe truncation: a long
// multi-byte string is clipped to pinPreviewMaxLen runes and stays valid UTF-8
// (never splits a char mid-byte).
func TestPinPreviewTruncatesRuneSafe(t *testing.T) {
	long := strings.Repeat("中", pinPreviewMaxLen+50)
	got := pinPreview(long)
	if n := utf8.RuneCountInString(got); n != pinPreviewMaxLen {
		t.Fatalf("rune count = %d, want %d", n, pinPreviewMaxLen)
	}
	if !utf8.ValidString(got) {
		t.Fatal("preview must remain valid UTF-8 after truncation")
	}
}

func TestStreamEventsPersistIntermediateMessages(t *testing.T) {
	s := newDispatchTestSession(t)
	ctx := context.Background()
	msgID := "turn-stream"

	s.handleEvent(ctx, ParsedEvent{
		Type:            "stream_event",
		StreamEventType: "content_block_start",
		BlockIndex:      0,
		BlockType:       "text",
	}, msgID)
	s.handleEvent(ctx, ParsedEvent{
		Type:            "stream_event",
		StreamEventType: "content_block_delta",
		BlockIndex:      0,
		DeltaType:       "text_delta",
		DeltaText:       "hello",
	}, msgID)
	s.handleEvent(ctx, ParsedEvent{
		Type:            "stream_event",
		StreamEventType: "content_block_delta",
		BlockIndex:      0,
		DeltaType:       "text_delta",
		DeltaText:       " world",
	}, msgID)
	s.handleEvent(ctx, ParsedEvent{
		Type:            "stream_event",
		StreamEventType: "content_block_stop",
		BlockIndex:      0,
	}, msgID)
	s.handleEvent(ctx, ParsedEvent{
		Type:            "stream_event",
		StreamEventType: "content_block_start",
		BlockIndex:      1,
		BlockType:       "tool_use",
		ToolUseName:     "Bash",
		ToolUseId:       "tool-1",
	}, msgID)
	s.handleEvent(ctx, ParsedEvent{
		Type:            "stream_event",
		StreamEventType: "content_block_delta",
		BlockIndex:      2,
		DeltaType:       "thinking_delta",
		DeltaText:       "thinking",
	}, msgID)
	s.handleEvent(ctx, ParsedEvent{
		Type:    "result",
		IsError: false,
	}, msgID)

	msgs, err := s.q.ListAgentMessages(ctx, store.ListAgentMessagesParams{
		WorkspaceID: s.workspaceID,
		Limit:       100,
		Offset:      0,
	})
	if err != nil {
		t.Fatalf("ListAgentMessages: %v", err)
	}

	var textCount, toolCount, thinkingCount int
	for _, m := range msgs {
		switch m.EventType {
		case EventText:
			if m.Content == "hello world" {
				textCount++
			}
		case EventToolUse:
			if m.ToolUseID.String == "tool-1" && m.ToolName.String == "Bash" {
				toolCount++
			}
		case EventThinking:
			if m.Content == "thinking" {
				thinkingCount++
			}
		}
	}
	if textCount != 1 {
		t.Fatalf("persisted text delta count = %d, want 1", textCount)
	}
	if toolCount != 1 {
		t.Fatalf("persisted tool_use count = %d, want 1", toolCount)
	}
	if thinkingCount != 1 {
		t.Fatalf("persisted thinking delta count = %d, want 1", thinkingCount)
	}
}

// setupDispatchDB opens an in-memory SQLite DB with the schema applied. We
// can't import internal/testing here because it pulls internal/server which
// would create an import cycle (server → agentproxy).
func setupDispatchDB(t *testing.T) *store.Queries {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return store.New(db)
}

// newDispatchTestSession builds a minimal WorkspaceSession wired enough that
// handleEvent's tool_use / tool_result branches can run without nil-panicking
// on persistEvent / hub broadcasts.
func newDispatchTestSession(t *testing.T) *WorkspaceSession {
	t.Helper()
	q := setupDispatchDB(t)
	// Need a real workspaces row because agent_messages.workspace_id has a FK.
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "dispatch-test-ws",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	hub := NewSessionHub()
	t.Cleanup(hub.Stop)
	return &WorkspaceSession{
		workspaceID:         ws.ID,
		topLevelAgentID:     100,
		q:                   q,
		hub:                 hub,
		toolUseNames:        map[string]string{},
		toolUseIds:          map[int]string{},
		toolInputBufs:       map[int]string{},
		textBlockBufs:       map[int]string{},
		textBlockMessageIDs: map[int]string{},
		textBlockPersisted:  map[int]bool{},
	}
}

// TestEmitActivity_FiltersNonEnvelopeKinds verifies that only tool_use /
// tool_result / thinking envelopes cause a team:agent_activity broadcast, and
// that agentID==0 short-circuits (no subagent to attribute to).
func TestEmitActivity_FiltersNonEnvelopeKinds(t *testing.T) {
	hub := NewSessionHub()
	t.Cleanup(hub.Stop)
	sub := hub.Subscribe(1, "test")
	defer hub.Unsubscribe(1, "test")

	s := &WorkspaceSession{
		workspaceID: 1,
		hub:         hub,
	}

	s.emitActivity(42, OutputEvent{Type: EventToolUse, ToolName: "Bash"})
	s.emitActivity(42, OutputEvent{Type: EventToolResult, ToolName: "Bash"})
	s.emitActivity(42, OutputEvent{Type: EventThinking})
	s.emitActivity(42, OutputEvent{Type: EventText, Content: "hi"})   // filtered
	s.emitActivity(42, OutputEvent{Type: EventDone})                  // filtered
	s.emitActivity(0, OutputEvent{Type: EventToolUse, ToolName: "X"}) // filtered (agentID==0)

	// Drain the sub channel with a short timeout
	got := []string{}
	timeout := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-sub:
			if ev.Type == EventTeamAgentActivity && ev.AgentActivity != nil {
				got = append(got, ev.AgentActivity.Activity.Kind)
			}
		case <-timeout:
			break drain
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 activity events (tool_use, tool_result, thinking), got %d: %v", len(got), got)
	}
}
