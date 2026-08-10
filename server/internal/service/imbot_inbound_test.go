package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/imbot/lark"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// --- test doubles -----------------------------------------------------------

type reactionCall struct {
	messageExtID string
	reaction     imbot.Reaction
}

type replyCall struct {
	messageExtID string
	text         string
}

type recordAdapter struct {
	mu        sync.Mutex
	pushes    []imbot.OutboundMessage
	reactions []reactionCall
	removed   []string // reaction ids passed to RemoveReaction
	replies   []replyCall
}

// Reply records anchored task-marker replies so tests can assert the `#<id>
// <标题>` marker is posted on the message being processed.
func (a *recordAdapter) Reply(_ context.Context, _ imbot.Credential, messageExtID, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.replies = append(a.replies, replyCall{messageExtID, text})
	return nil
}

func (a *recordAdapter) Type() imbot.ChannelType { return imbot.ChannelLark }
func (a *recordAdapter) Connect(ctx context.Context, _ imbot.Credential, _ imbot.InboundHandler) error {
	<-ctx.Done()
	return nil
}
func (a *recordAdapter) Push(_ context.Context, _ imbot.Credential, msg imbot.OutboundMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushes = append(a.pushes, msg)
	return nil
}

// React records the mark-processing reaction so tests can assert the 🐂 marker
// is applied exactly on the messages the bot commits to processing. It returns a
// deterministic reaction id derived from the message id so RemoveReaction can be
// asserted.
func (a *recordAdapter) React(_ context.Context, _ imbot.Credential, messageExtID string, reaction imbot.Reaction) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reactions = append(a.reactions, reactionCall{messageExtID, reaction})
	return "rid-" + messageExtID, nil
}

// RemoveReaction records cleared reaction ids so tests can assert the 🐂 marker
// is removed once the agent finishes.
func (a *recordAdapter) RemoveReaction(_ context.Context, _ imbot.Credential, _ , reactionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removed = append(a.removed, reactionID)
	return nil
}

// FetchResource returns deterministic fake bytes so tests can assert inbound
// attachments are downloaded and materialized into the workspace.
func (a *recordAdapter) FetchResource(_ context.Context, _ imbot.Credential, _ string, att imbot.InboundAttachment) ([]byte, error) {
	return []byte("bytes-of-" + att.ResourceID), nil
}
func (a *recordAdapter) VerifyWebhook(*http.Request, imbot.Credential) (imbot.InboundEvent, error) {
	return imbot.InboundEvent{}, nil
}
func (a *recordAdapter) Challenge(*http.Request) ([]byte, bool) { return nil, false }

// fakeRouter returns queued PlanTargets and records how it was called. It also
// satisfies TaskDeleter so SetInbound captures it as the /delete backend; delete
// calls are recorded (the fake does not touch the DB — the cascade itself is
// covered by TestAssistantDispatch_DeleteTask).
type fakeRouter struct {
	mu      sync.Mutex
	queue   []PlanTarget
	calls   int
	lastPID int64
	lastText string
	lastHint RouteHint

	deleteCalls []deleteTaskCall
	deleteErr   error

	// startCalls records StartWorkspaceForExistingIssue invocations. When q is set
	// (wired by the fixture) the fake provisions a real workspace row for the issue
	// so the downstream delivery (which loads the workspace) sees it — mirroring the
	// real dispatch service. startErr, when set, is returned instead.
	q          *store.Queries
	startCalls []int64
	startErr   error
}

type deleteTaskCall struct {
	projectID int64
	issueID   int64
}

func (r *fakeRouter) StartWorkspaceForExistingIssue(ctx context.Context, _ OwnerRef, projectID, issueID int64) (PlanTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return PlanTarget{}, r.startErr
	}
	r.startCalls = append(r.startCalls, issueID)
	wsID := int64(0)
	if r.q != nil {
		ws, err := r.q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			IssueID: sql.NullInt64{Int64: issueID, Valid: true},
			Name:    "started", Path: "/tmp/ws-started-" + itoa(issueID),
			Status: "running", OwnerType: "user", OwnerID: 1,
		})
		if err != nil {
			return PlanTarget{}, err
		}
		wsID = ws.ID
	}
	return PlanTarget{IssueID: issueID, WorkspaceID: wsID, ProjectID: projectID}, nil
}

func (r *fakeRouter) DeleteTask(_ context.Context, projectID, issueID int64, _ func(context.Context, int64)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleteCalls = append(r.deleteCalls, deleteTaskCall{projectID: projectID, issueID: issueID})
	return nil
}

func (r *fakeRouter) RouteInProject(_ context.Context, _ OwnerRef, projectID int64, text string, hint RouteHint) (PlanTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastPID = projectID
	r.lastText = text
	r.lastHint = hint
	if len(r.queue) == 0 {
		return PlanTarget{}, nil
	}
	t := r.queue[0]
	r.queue = r.queue[1:]
	return t, nil
}

type deliverCall struct {
	workspaceID int64
	workDir     string
	content     string
	attachments string
}

type fakeDeliverer struct {
	mu      sync.Mutex
	calls   []deliverCall
	stopped []int64
}

func (d *fakeDeliverer) Deliver(_ context.Context, workspaceID int64, workDir, content, attachments string) (bool, int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, deliverCall{workspaceID, workDir, content, attachments})
	return false, 0, nil
}

// RemoveSession makes the deliverer double also satisfy AgentSessionStopper, so
// SetInbound captures it as the /stop + /delete session stopper. Stopped
// workspace ids are recorded for assertions.
func (d *fakeDeliverer) RemoveSession(_ context.Context, workspaceID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = append(d.stopped, workspaceID)
}

type permCall struct {
	reqID   int64
	allow   bool
	always  bool
	matcher bool // whether an allowlist matcher was attached
}

type fakePerm struct {
	mu    sync.Mutex
	calls []permCall
}

func (p *fakePerm) Decide(_ context.Context, requestID int64, d Decision) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, permCall{requestID, d.Allow, d.Always, d.Matcher != nil})
	return nil
}

type askUserCall struct {
	reqID  int64
	labels []string
}

type fakeAskUser struct {
	mu    sync.Mutex
	calls []askUserCall
	err   error
}

func (a *fakeAskUser) Decide(_ context.Context, requestID int64, d AskUserDecision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	labels := []string{}
	if len(d.Answers) > 0 {
		labels = d.Answers[0].Labels
	}
	a.calls = append(a.calls, askUserCall{requestID, labels})
	return nil
}

// --- fixture ----------------------------------------------------------------

type imbotFixture struct {
	svc       *IMBotService
	q         *store.Queries
	db        *sql.DB
	adapter   *recordAdapter
	router    *fakeRouter
	deliverer *fakeDeliverer
	perm      *fakePerm
	askUser   *fakeAskUser
	projectID int64
	channelID int64
	wsID      int64
	issueID   int64
}

func newIMBotFixture(t *testing.T) *imbotFixture {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	store.Migrate(db)
	q := store.New(db)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "工程项目", OwnerType: "user", OwnerID: 1})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "待办", Position: 0})
	if err != nil {
		t.Fatalf("column: %v", err)
	}
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "现有任务", Position: 0})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws", Path: "/tmp/ws-existing", Status: "running", OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	ch, err := q.CreateIMBotChannel(ctx, store.CreateIMBotChannelParams{
		OwnerType: "user", OwnerID: 1, ChannelType: "lark", Name: "bot", ConnectionMode: "stream",
		CredentialEnc: "", WebhookSecret: "", Status: "active",
	})
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	adapter := &recordAdapter{}
	svc := NewIMBotService(q, db, nil, nil, map[imbot.ChannelType]imbot.ChannelAdapter{imbot.ChannelLark: adapter})
	router := &fakeRouter{q: q}
	deliverer := &fakeDeliverer{}
	perm := &fakePerm{}
	askUser := &fakeAskUser{}
	svc.SetInbound(router, deliverer, perm)
	svc.SetAskUserDecider(askUser)

	return &imbotFixture{
		svc: svc, q: q, db: db, adapter: adapter, router: router, deliverer: deliverer, perm: perm, askUser: askUser,
		projectID: proj.ID, channelID: ch.ID, wsID: ws.ID, issueID: issue.ID,
	}
}

// activeChat seeds an approved chat under the fixture channel, routed to the
// fixture project (shared-bot: routing target is chat.project_id).
func (f *imbotFixture) activeChat(t *testing.T, chatExtID string) store.ImBotChat {
	t.Helper()
	ctx := context.Background()
	chat, err := f.q.CreateIMBotChat(ctx, store.CreateIMBotChatParams{
		ChannelID: f.channelID, ChatExtID: chatExtID, ChatName: "", Status: "active",
	})
	if err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	chat, err = f.q.ReassignIMBotChat(ctx, store.ReassignIMBotChatParams{
		ProjectID: sql.NullInt64{Int64: f.projectID, Valid: true}, ID: chat.ID,
	})
	if err != nil {
		t.Fatalf("route chat: %v", err)
	}
	return chat
}

// newWorkspace seeds an extra issue+workspace and returns their ids, used to
// give the router distinct targets per thread.
func (f *imbotFixture) newWorkspace(t *testing.T, title, path string) (issueID, wsID int64) {
	t.Helper()
	ctx := context.Background()
	cols, _ := f.q.ListColumnsByProject(ctx, f.projectID)
	issue, err := f.q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: cols[0].ID, Title: title, Position: 0})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	ws, err := f.q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issue.ID, Valid: true}, Name: title, Path: path,
		Status: "running", OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return issue.ID, ws.ID
}

// newIssue seeds an issue with no backing workspace and returns its id, used to
// exercise the full-issue listing and the `#<id>` start-workspace path.
func (f *imbotFixture) newIssue(t *testing.T, title, description string) int64 {
	t.Helper()
	ctx := context.Background()
	cols, _ := f.q.ListColumnsByProject(ctx, f.projectID)
	iss, err := f.q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: cols[0].ID, Title: title, Position: 0,
		Description: sql.NullString{String: description, Valid: description != ""},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return iss.ID
}

// --- tests ------------------------------------------------------------------

func TestHandleInbound_UnknownChat_SeedsPending(t *testing.T) {
	f := newIMBotFixture(t)
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_new", Text: "hi", Kind: "message", EventID: "e1",
	})
	chat, err := f.q.GetIMBotChatByExt(context.Background(), store.GetIMBotChatByExtParams{ChannelID: f.channelID, ChatExtID: "oc_new"})
	if err != nil {
		t.Fatalf("expected pending chat seeded: %v", err)
	}
	if chat.Status != "pending" {
		t.Fatalf("chat status = %q, want pending", chat.Status)
	}
	if len(f.deliverer.calls) != 0 || f.router.calls != 0 {
		t.Fatalf("unknown chat must not route/deliver")
	}
}

func TestHandleInbound_PendingChat_NoRoute(t *testing.T) {
	f := newIMBotFixture(t)
	_, _ = f.q.CreateIMBotChat(context.Background(), store.CreateIMBotChatParams{ChannelID: f.channelID, ChatExtID: "oc_p", Status: "pending"})
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{ChannelID: f.channelID, ChatExtID: "oc_p", Text: "hi", Kind: "message", EventID: "e1"})
	if len(f.deliverer.calls) != 0 || f.router.calls != 0 {
		t.Fatalf("pending chat must not route/deliver")
	}
}

// An active chat with no project assigned (project_id NULL) is not routable:
// HandleInbound must nudge and never route/deliver business messages.
func TestHandleInbound_ActiveChat_Unassigned_NoRoute(t *testing.T) {
	f := newIMBotFixture(t)
	// Seed an active chat WITHOUT routing it to a project (project_id stays NULL).
	if _, err := f.q.CreateIMBotChat(context.Background(), store.CreateIMBotChatParams{
		ChannelID: f.channelID, ChatExtID: "oc_u", ChatName: "", Status: "active",
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_u", Text: "帮我做张表", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("active-but-unassigned chat must not route/deliver: router=%d deliver=%d", f.router.calls, len(f.deliverer.calls))
	}
	if len(f.adapter.pushes) != 1 {
		t.Fatalf("expected one nudge push, got %d", len(f.adapter.pushes))
	}
}

func TestHandleInbound_ActiveChat_RoutesAndDelivers(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "帮我做张表", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 1 || f.router.lastPID != f.projectID {
		t.Fatalf("router not called for bound project: calls=%d pid=%d want %d", f.router.calls, f.router.lastPID, f.projectID)
	}
	if len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != f.wsID || f.deliverer.calls[0].workDir != "/tmp/ws-existing" {
		t.Fatalf("deliver mismatch: %+v", f.deliverer.calls)
	}
	// The no-thread chat's active pointer should now point at the routed task.
	got, _ := f.q.GetIMBotChat(context.Background(), chat.ID)
	if !got.ActiveIssueID.Valid || got.ActiveIssueID.Int64 != f.issueID {
		t.Fatalf("active issue pointer not set: %+v", got.ActiveIssueID)
	}
}

func TestHandleInbound_MarksProcessingReaction(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_task",
		Text: "帮我做张表", Kind: "message", EventID: "e1",
	})

	if len(f.adapter.reactions) != 1 {
		t.Fatalf("expected one 🐂 mark-processing reaction, got %d", len(f.adapter.reactions))
	}
	if got := f.adapter.reactions[0]; got.messageExtID != "om_task" || got.reaction != imbot.ReactionProcessing {
		t.Fatalf("reaction = %+v, want {om_task, processing}", got)
	}
}

func TestClearProcessingReactions_RemovesMarkerOnDone(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	// Deliver a message → the 🐂 marker is placed and recorded under the workspace.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_task",
		Text: "帮我做张表", Kind: "message", EventID: "e1",
	})
	if len(f.adapter.reactions) != 1 {
		t.Fatalf("expected the 🐂 marker to be placed, got %d", len(f.adapter.reactions))
	}

	// Simulate agent_done → the marker is removed by its recorded reaction id.
	f.svc.clearProcessingReactions(context.Background(), f.wsID)

	if len(f.adapter.removed) != 1 || f.adapter.removed[0] != "rid-om_task" {
		t.Fatalf("expected marker rid-om_task removed, got %+v", f.adapter.removed)
	}
	// A second clear (redelivered agent_done) is a no-op — nothing left to remove.
	f.svc.clearProcessingReactions(context.Background(), f.wsID)
	if len(f.adapter.removed) != 1 {
		t.Fatalf("clear must be idempotent, removed=%+v", f.adapter.removed)
	}
}

func TestHandleInbound_RepliesTaskMarker_OnlyOnNewConversation(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	// First message → a new conversation is created → exactly one marker reply.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_1",
		Text: "帮我做张表", Kind: "message", EventID: "e1",
	})
	if len(f.adapter.replies) != 1 {
		t.Fatalf("expected one task-marker reply on new conversation, got %d", len(f.adapter.replies))
	}
	// Fixture issue is titled 现有任务 (id f.issueID) → full-title marker plus the
	// friendly working line (no 9-char cap now that it's posted only once).
	wantText := "#" + itoa(f.issueID) + " 现有任务\n[敲键盘]牛牛正在为您工作。"
	if got := f.adapter.replies[0]; got.messageExtID != "om_1" || got.text != wantText {
		t.Fatalf("task marker = %+v, want on om_1 %q", got, wantText)
	}

	// Follow-up message continues the same task (active pointer set) → NO new
	// route → NO extra reply (this is what prevents per-message reply spam).
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_2",
		Text: "继续做", Kind: "message", EventID: "e2",
	})
	if len(f.adapter.replies) != 1 {
		t.Fatalf("follow-up must not reply-mark again, got %d replies", len(f.adapter.replies))
	}
}

func TestHandleInbound_DoesNotMarkCommandsOrPending(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	// A slash command is not agent work — it must not get the 🐂 marker.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_cmd",
		Text: "/issues", Kind: "message", EventID: "e1",
	})
	// An unknown chat only seeds pairing — nothing is being processed yet.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_unknown", MessageExtID: "om_new",
		Text: "hi", Kind: "message", EventID: "e2",
	})

	if len(f.adapter.reactions) != 0 {
		t.Fatalf("commands / pending chats must not be marked, got %+v", f.adapter.reactions)
	}
}

func TestHandleInbound_ImageMessage_MaterializesAttachment(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	wsDir := t.TempDir()
	ctx := context.Background()
	cols, _ := f.q.ListColumnsByProject(ctx, f.projectID)
	iss, _ := f.q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: cols[0].ID, Title: "图片任务", Position: 0})
	ws, _ := f.q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: iss.ID, Valid: true}, Name: "wsimg", Path: wsDir,
		Status: "running", OwnerType: "user", OwnerID: 1,
	})
	f.router.queue = []PlanTarget{{IssueID: iss.ID, WorkspaceID: ws.ID}}

	// An attachment-only image message (no caption).
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_img",
		Kind: "message", EventID: "e1",
		Attachments: []imbot.InboundAttachment{{Kind: "image", ResourceID: "img_v2_k"}},
	})

	if len(f.deliverer.calls) != 1 {
		t.Fatalf("expected one delivery, got %d", len(f.deliverer.calls))
	}
	call := f.deliverer.calls[0]
	// Content gets a placeholder caption + the [附件: ...] suffix; attachments
	// JSON carries the workspace-relative path and type.
	if !strings.Contains(call.content, "[附件:") || !strings.Contains(call.content, ".attachments/") {
		t.Fatalf("content missing attachment reference: %q", call.content)
	}
	if !strings.Contains(call.attachments, ".attachments/") || !strings.Contains(call.attachments, `"type":"image"`) {
		t.Fatalf("attachments json unexpected: %q", call.attachments)
	}
	// The bytes must be written under <ws>/.attachments/ for the agent to read.
	entries, _ := os.ReadDir(filepath.Join(wsDir, ".attachments"))
	if len(entries) != 1 {
		t.Fatalf("expected one saved file, got %d", len(entries))
	}
	got, _ := os.ReadFile(filepath.Join(wsDir, ".attachments", entries[0].Name()))
	if string(got) != "bytes-of-img_v2_k" {
		t.Fatalf("saved bytes = %q", got)
	}
}

func TestHandleInbound_Idempotent(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}
	ev := imbot.InboundEvent{ChannelID: f.channelID, ChatExtID: "oc_a", Text: "hi", Kind: "message", EventID: "dup-1"}

	f.svc.HandleInbound(context.Background(), ev)
	f.svc.HandleInbound(context.Background(), ev) // same EventID -> deduped

	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("redelivered event not deduped: router=%d deliver=%d", f.router.calls, len(f.deliverer.calls))
	}
}

func TestHandleInbound_ThreadRouting_NoCrosstalk(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	issA, wsA := f.newWorkspace(t, "任务A", "/tmp/ws-A")
	issB, wsB := f.newWorkspace(t, "任务B", "/tmp/ws-B")
	f.router.queue = []PlanTarget{
		{IssueID: issA, WorkspaceID: wsA},
		{IssueID: issB, WorkspaceID: wsB},
	}

	// Thread t1 -> creates + binds task A.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t1", Text: "开始A", Kind: "message", EventID: "e1"})
	// Thread t2 -> creates + binds task B.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t2", Text: "开始B", Kind: "message", EventID: "e2"})
	// Follow-up on thread t1 -> must continue task A (no new route), deliver to wsA.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t1", Text: "继续A", Kind: "message", EventID: "e3"})

	if f.router.calls != 2 {
		t.Fatalf("router should create exactly 2 tasks, got %d", f.router.calls)
	}
	if len(f.deliverer.calls) != 3 {
		t.Fatalf("expected 3 deliveries, got %d", len(f.deliverer.calls))
	}
	got := []int64{f.deliverer.calls[0].workspaceID, f.deliverer.calls[1].workspaceID, f.deliverer.calls[2].workspaceID}
	if got[0] != wsA || got[1] != wsB || got[2] != wsA {
		t.Fatalf("thread crosstalk: deliveries=%v want [%d %d %d]", got, wsA, wsB, wsA)
	}
}

func TestHandleInbound_PermissionCallback(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	// A pending permission request whose workspace resolves to this project.
	reqID := f.seedPermissionRequest(t, f.wsID)

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Kind: "action_callback",
		CallbackData: "permission:approve:" + itoa(reqID), EventID: "cb1",
	})

	if len(f.perm.calls) != 1 || f.perm.calls[0].reqID != reqID || !f.perm.calls[0].allow {
		t.Fatalf("permission not decided correctly: %+v", f.perm.calls)
	}
}

func TestHandleInbound_PermissionCallback_CrossProjectRejected(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	// Seed a second project + workspace and a permission request against it.
	ctx := context.Background()
	proj2, _ := f.q.CreateProject(ctx, store.CreateProjectParams{Name: "别的项目", OwnerType: "user", OwnerID: 1})
	col2, _ := f.q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj2.ID, Name: "待办", Position: 0})
	iss2, _ := f.q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col2.ID, Title: "x", Position: 0})
	ws2, _ := f.q.CreateWorkspace(ctx, store.CreateWorkspaceParams{IssueID: sql.NullInt64{Int64: iss2.ID, Valid: true}, Name: "ws2", Path: "/tmp/ws2", Status: "running", OwnerType: "user", OwnerID: 1})
	reqID := f.seedPermissionRequest(t, ws2.ID)

	// Callback arrives via THIS channel's chat (project 1) — must be rejected.
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Kind: "action_callback",
		CallbackData: "permission:approve:" + itoa(reqID), EventID: "cb2",
	})
	if len(f.perm.calls) != 0 {
		t.Fatalf("cross-project permission callback must be rejected, got %+v", f.perm.calls)
	}
}

func TestHandleWebhook_ChallengeAndMessage(t *testing.T) {
	f := newIMBotFixture(t)
	// The webhook path parses real Feishu JSON via the real Lark adapter.
	f.svc.adapters[imbot.ChannelLark] = lark.New()
	f.activeChat(t, "oc_hook")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}
	ctx := context.Background()
	// The public webhook entry serves webhook-mode channels only (design §5.2),
	// so flip the fixture channel to webhook mode for this path. A webhook-mode
	// channel MUST carry a secret (the Lark Verification Token); the adapter
	// authenticates every event against it, so configure the same token the
	// event bodies below carry.
	if _, err := f.q.UpdateIMBotChannel(ctx, store.UpdateIMBotChannelParams{
		Name: "bot", ConnectionMode: "webhook", WebhookSecret: "vtok", Status: "active", ID: f.channelID,
	}); err != nil {
		t.Fatalf("switch to webhook mode: %v", err)
	}

	// 1. URL verification challenge is echoed back.
	chReq := httptest.NewRequest("POST", "/api/imbot/webhook/1",
		strings.NewReader(`{"type":"url_verification","challenge":"zzz"}`))
	echo, isChallenge, err := f.svc.HandleWebhook(ctx, f.channelID, chReq)
	if err != nil || !isChallenge || !strings.Contains(string(echo), "zzz") {
		t.Fatalf("challenge: echo=%s isCh=%v err=%v", echo, isChallenge, err)
	}

	// 2. A real message event body routes + delivers through HandleInbound.
	body := `{"schema":"2.0","header":{"event_id":"wh-1","event_type":"im.message.receive_v1","token":"vtok"},
	  "event":{"sender":{"sender_id":{"open_id":"ou_x"}},
	  "message":{"message_id":"om","chat_id":"oc_hook","message_type":"text","content":"{\"text\":\"通过webhook建任务\"}"}}}`
	msgReq := httptest.NewRequest("POST", "/api/imbot/webhook/1", strings.NewReader(body))
	if _, isCh, err := f.svc.HandleWebhook(ctx, f.channelID, msgReq); err != nil || isCh {
		t.Fatalf("message webhook: isCh=%v err=%v", isCh, err)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != f.wsID {
		t.Fatalf("webhook did not drive routing+delivery: router=%d deliver=%+v", f.router.calls, f.deliverer.calls)
	}
}

// sigFailAdapter always fails webhook signature verification, exercising the
// adapter -> service ErrWebhookUnauthorized (-> HTTP 401) mapping.
type sigFailAdapter struct{ recordAdapter }

func (a *sigFailAdapter) VerifyWebhook(*http.Request, imbot.Credential) (imbot.InboundEvent, error) {
	return imbot.InboundEvent{}, imbot.ErrWebhookUnauthorized
}

// TestHandleWebhook_RejectsStreamChannel asserts the public webhook entry serves
// webhook-mode channels only: a stream-mode channel (the LAN default) must not be
// drivable by an unauthenticated public POST (design §5.2 / §8).
func TestHandleWebhook_RejectsStreamChannel(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_s")
	req := httptest.NewRequest("POST", "/api/imbot/webhook/1", strings.NewReader(`{}`))
	if _, _, err := f.svc.HandleWebhook(context.Background(), f.channelID, req); !errors.Is(err, ErrIMBotNotFound) {
		t.Fatalf("stream-channel webhook err=%v, want ErrIMBotNotFound", err)
	}
}

// TestHandleWebhook_BadSignatureUnauthorized asserts an adapter signature failure
// surfaces as ErrWebhookUnauthorized so the handler answers 401 (not a 200 ack).
func TestHandleWebhook_BadSignatureUnauthorized(t *testing.T) {
	f := newIMBotFixture(t)
	f.svc.adapters[imbot.ChannelLark] = &sigFailAdapter{}
	ctx := context.Background()
	if _, err := f.q.UpdateIMBotChannel(ctx, store.UpdateIMBotChannelParams{
		Name: "bot", ConnectionMode: "webhook", WebhookSecret: "s", Status: "active", ID: f.channelID,
	}); err != nil {
		t.Fatalf("webhook mode: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/imbot/webhook/1", strings.NewReader(`<xml/>`))
	if _, _, err := f.svc.HandleWebhook(ctx, f.channelID, req); !errors.Is(err, ErrWebhookUnauthorized) {
		t.Fatalf("bad-signature webhook err=%v, want ErrWebhookUnauthorized", err)
	}
}

func TestHandleInbound_IssuesCommand_ListsAllIssues(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	// Two extra workspace-backed issues, plus the fixture's existing one, plus one
	// issue that has NO workspace — the /issues listing must show them all.
	f.newWorkspace(t, "任务A", "/tmp/ws-A")
	f.newWorkspace(t, "任务B", "/tmp/ws-B")
	noWS := f.newIssue(t, "未启动任务", "做一份报表")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/issues", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("/issues must not route or deliver")
	}
	if len(f.adapter.pushes) != 1 {
		t.Fatalf("expected one reply push, got %d", len(f.adapter.pushes))
	}
	reply := f.adapter.pushes[0].Text
	// Titles of every issue (backed and not), the column name (所在列), and the
	// workspace-status flags must all appear.
	for _, want := range []string{"现有任务", "任务A", "任务B", "未启动任务", "待办", "已启动", "未启动工作空间"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply missing %q: %s", want, reply)
		}
	}
	// The workspace-less issue is listed by its stable #id so the user can start it.
	if !strings.Contains(reply, "#"+itoa(noWS)) {
		t.Errorf("reply should list workspace-less issue #%d: %s", noWS, reply)
	}
	// Rendered as CommonMark so it doesn't collapse into a run-on paragraph on
	// markdown adapters (DingTalk/Lark): bold section headers + "- " list items.
	if !strings.Contains(reply, "**待办**") || !strings.Contains(reply, "\n- #") {
		t.Errorf("reply should be markdown (bold headers + '- ' bullets): %s", reply)
	}
}

func TestHandleInbound_UseCommand_SwitchesActivePointer(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	// Task list order: [现有任务(fixture), 任务A, 任务B].
	issA, wsA := f.newWorkspace(t, "任务A", "/tmp/ws-A")
	_, _ = f.newWorkspace(t, "任务B", "/tmp/ws-B")

	// /use 2 selects 任务A (1-based index into the /issues listing).
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/use 2", Kind: "message", EventID: "e1",
	})
	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("/use must not route or deliver")
	}
	got, _ := f.q.GetIMBotChat(context.Background(), chat.ID)
	if !got.ActiveIssueID.Valid || got.ActiveIssueID.Int64 != issA {
		t.Fatalf("active pointer = %+v, want issue %d (任务A)", got.ActiveIssueID, issA)
	}

	// A follow-up bare message now continues 任务A without a new route.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "继续做", Kind: "message", EventID: "e2",
	})
	if f.router.calls != 0 {
		t.Fatalf("continuation should not create a new task, router calls=%d", f.router.calls)
	}
	if len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != wsA {
		t.Fatalf("follow-up delivered to %+v, want workspace %d", f.deliverer.calls, wsA)
	}
}

func TestHandleInbound_UseCommand_InvalidIndex(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/use 99", Kind: "message", EventID: "e1",
	})
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "/issues") {
		t.Fatalf("expected a guidance reply for an out-of-range index, got %+v", f.adapter.pushes)
	}
}

func TestParseSlashCommand_StripsBotMention(t *testing.T) {
	cases := []struct {
		in       string
		wantCmd  string
		wantRest string
	}{
		{"/new@MyBot 做张表", "new", "做张表"},
		{"/issues@MyBot", "issues", ""},
		{"/use 2", "use", "2"},
		{"/STATUS", "status", ""},
		{"hello", "", "hello"},
	}
	for _, c := range cases {
		cmd, rest := parseSlashCommand(c.in)
		if cmd != c.wantCmd || rest != c.wantRest {
			t.Errorf("parseSlashCommand(%q) = (%q,%q), want (%q,%q)", c.in, cmd, rest, c.wantCmd, c.wantRest)
		}
	}
}

func TestHandleInbound_HashSwitch_ExistingConversation_Delivers(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	issA, wsA := f.newWorkspace(t, "任务A", "/tmp/ws-A")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "#" + itoa(issA) + " 继续做A", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 {
		t.Fatalf("switch to an existing conversation must not create a new task, router=%d", f.router.calls)
	}
	if len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != wsA || f.deliverer.calls[0].content != "继续做A" {
		t.Fatalf("expected delivery of 继续做A to wsA=%d, got %+v", wsA, f.deliverer.calls)
	}
	got, _ := f.q.GetIMBotChat(context.Background(), chat.ID)
	if !got.ActiveIssueID.Valid || got.ActiveIssueID.Int64 != issA {
		t.Fatalf("active pointer = %+v, want %d", got.ActiveIssueID, issA)
	}
}

func TestHandleInbound_HashSwitch_BareId_ConfirmsSwitchOnly(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	issA, _ := f.newWorkspace(t, "任务A", "/tmp/ws-A")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "#" + itoa(issA), Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("bare #id must only switch, not route/deliver: router=%d deliver=%d", f.router.calls, len(f.deliverer.calls))
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "任务A") {
		t.Fatalf("expected a switch confirmation mentioning 任务A, got %+v", f.adapter.pushes)
	}
	got, _ := f.q.GetIMBotChat(context.Background(), chat.ID)
	if !got.ActiveIssueID.Valid || got.ActiveIssueID.Int64 != issA {
		t.Fatalf("active pointer not switched: %+v", got.ActiveIssueID)
	}
}

func TestHandleInbound_HashStart_IssueWithoutWorkspace_StartsAndWorks(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	// An issue that exists on the board but has no workspace yet.
	issID := f.newIssue(t, "报表任务", "做一份月度报表")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_s",
		Text: "#" + itoa(issID) + " 注意用中文", Kind: "message", EventID: "e1",
	})

	// It starts a workspace for the EXISTING issue — never routes a new task.
	if f.router.calls != 0 {
		t.Fatalf("starting an existing issue must not create a new task, router=%d", f.router.calls)
	}
	if len(f.router.startCalls) != 1 || f.router.startCalls[0] != issID {
		t.Fatalf("StartWorkspaceForExistingIssue not called for issue %d: %+v", issID, f.router.startCalls)
	}
	// The issue's own content is delivered so the agent works from the card, with
	// the trailing instruction appended.
	if len(f.deliverer.calls) != 1 {
		t.Fatalf("expected one delivery to the started workspace, got %+v", f.deliverer.calls)
	}
	if got := f.deliverer.calls[0].content; !strings.Contains(got, "做一份月度报表") || !strings.Contains(got, "注意用中文") {
		t.Fatalf("delivered content should carry the issue content + trailing text, got %q", got)
	}
	// The chat is bound to the started task so follow-ups continue it.
	got, _ := f.q.GetIMBotChat(context.Background(), chat.ID)
	if !got.ActiveIssueID.Valid || got.ActiveIssueID.Int64 != issID {
		t.Fatalf("active pointer = %+v, want %d", got.ActiveIssueID, issID)
	}
}

func TestHandleInbound_HashStart_BareId_IssueWithoutWorkspace_UsesIssueContent(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	issID := f.newIssue(t, "报表任务", "做一份月度报表")

	// A bare `#id` (no trailing text) still starts work using the issue's content.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", MessageExtID: "om_b",
		Text: "#" + itoa(issID), Kind: "message", EventID: "e1",
	})

	if len(f.router.startCalls) != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("bare #id on a workspace-less issue should start + deliver: start=%+v deliver=%+v", f.router.startCalls, f.deliverer.calls)
	}
	if got := f.deliverer.calls[0].content; got != "做一份月度报表" {
		t.Fatalf("delivered content = %q, want the issue description", got)
	}
}

func TestHandleInbound_HashSwitch_UnknownId_StartsNewConversation(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "#999999 全新任务", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 1 || !f.router.lastHint.ForceNew {
		t.Fatalf("unknown #id should force a new conversation: calls=%d hint=%+v", f.router.calls, f.router.lastHint)
	}
	if f.router.lastText != "全新任务" {
		t.Fatalf("new-conversation text = %q, want 全新任务", f.router.lastText)
	}
}

func TestHandleInbound_HashSwitch_NoId_StartsNewConversation(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "#做个新东西", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 1 || !f.router.lastHint.ForceNew || f.router.lastText != "做个新东西" {
		t.Fatalf("`#text` with no id should start a new conversation: calls=%d text=%q hint=%+v", f.router.calls, f.router.lastText, f.router.lastHint)
	}
}

func TestHandleInbound_HashSwitch_BareHash_AsksForDescription(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "#", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("bare # must not route/deliver")
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "/issues") {
		t.Fatalf("expected a guidance reply, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_IssuesCommand_ShowsConversationIds(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	issA, _ := f.newWorkspace(t, "任务A", "/tmp/ws-A")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/issues", Kind: "message", EventID: "e1",
	})

	if len(f.adapter.pushes) != 1 {
		t.Fatalf("expected one reply, got %d", len(f.adapter.pushes))
	}
	if reply := f.adapter.pushes[0].Text; !strings.Contains(reply, "#"+itoa(issA)) {
		t.Fatalf("reply should list conversation id #%d: %s", issA, reply)
	}
}

func TestParseHashSwitch(t *testing.T) {
	cases := []struct {
		in, id, body string
		ok           bool
	}{
		{"#42", "42", "", true},
		{"#42 继续做", "42", "继续做", true},
		{"#新任务", "", "新任务", true},
		{"#", "", "", true},
		{"#12ab", "", "12ab", true},
		{"hello", "", "hello", false},
	}
	for _, c := range cases {
		id, body, ok := parseHashSwitch(c.in)
		if id != c.id || body != c.body || ok != c.ok {
			t.Errorf("parseHashSwitch(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, id, body, ok, c.id, c.body, c.ok)
		}
	}
}

func TestHandleInbound_DeleteCommand_ByIndex(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	// Task listing order: [现有任务(fixture), 任务A]; /delete 2 targets 任务A.
	issA, wsA := f.newWorkspace(t, "任务A", "/tmp/ws-A")
	// Point this chat's active pointer + a thread at 任务A so cleanup is observable.
	f.svc.setActiveIssue(context.Background(), chat, issA)
	if _, err := f.q.CreateIMBotThread(context.Background(), store.CreateIMBotThreadParams{
		ChatID: chat.ID, ThreadExtID: "th-A", IssueID: issA, WorkspaceID: wsA,
	}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/delete 2", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("/delete must not route or deliver")
	}
	if len(f.router.deleteCalls) != 1 || f.router.deleteCalls[0].issueID != issA || f.router.deleteCalls[0].projectID != f.projectID {
		t.Fatalf("delete not invoked for 任务A (issue %d): %+v", issA, f.router.deleteCalls)
	}
	// Active pointer to the deleted task is cleared.
	got, _ := f.q.GetIMBotChat(context.Background(), chat.ID)
	if got.ActiveIssueID.Valid {
		t.Fatalf("active pointer should be cleared, got %+v", got.ActiveIssueID)
	}
	// Thread binding to the deleted task is removed (no silent-drop on later replies).
	if _, err := f.q.GetIMBotThreadByExt(context.Background(), store.GetIMBotThreadByExtParams{ChatID: chat.ID, ThreadExtID: "th-A"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("thread binding should be removed, err=%v", err)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "任务A") || !strings.Contains(f.adapter.pushes[0].Text, "已删除") {
		t.Fatalf("expected a delete confirmation mentioning 任务A, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_DeleteCommand_ByHashId(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	issA, _ := f.newWorkspace(t, "任务A", "/tmp/ws-A")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/delete #" + itoa(issA), Kind: "message", EventID: "e1",
	})

	if len(f.router.deleteCalls) != 1 || f.router.deleteCalls[0].issueID != issA {
		t.Fatalf("delete by #id not invoked for issue %d: %+v", issA, f.router.deleteCalls)
	}
}

func TestHandleInbound_DeleteCommand_InvalidRef_Guides(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/delete 99", Kind: "message", EventID: "e1",
	})

	if len(f.router.deleteCalls) != 0 {
		t.Fatalf("an out-of-range index must not delete anything: %+v", f.router.deleteCalls)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "/issues") {
		t.Fatalf("expected a guidance reply for an unknown ref, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_DeleteCommand_NoArg_Guides(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/delete", Kind: "message", EventID: "e1",
	})

	if len(f.router.deleteCalls) != 0 {
		t.Fatalf("/delete with no argument must not delete: %+v", f.router.deleteCalls)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "/delete") {
		t.Fatalf("expected a usage reply, got %+v", f.adapter.pushes)
	}
}

// --- item 1/2/3/5/6: IM control-surface tests --------------------------------

func TestHandleInbound_PermissionCallback_Always_WritesAllowlistMatcher(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	// A non-high-risk tool -> always-allow attaches an 'any' matcher.
	reqID := f.seedPermissionRequestWithTool(t, f.wsID, "Grep", `{}`)

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Kind: "action_callback",
		CallbackData: "permission:always:" + itoa(reqID), EventID: "cb1",
	})

	if len(f.perm.calls) != 1 {
		t.Fatalf("expected one decision, got %+v", f.perm.calls)
	}
	if got := f.perm.calls[0]; !got.allow || !got.always || !got.matcher {
		t.Fatalf("always decision = %+v, want allow+always+matcher", got)
	}
}

func TestHandleInbound_PermissionCallback_Always_HighRiskNoField_DegradesToOnce(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	// Bash with no command field can't be pinned to a matcher -> degrade to once.
	reqID := f.seedPermissionRequestWithTool(t, f.wsID, "Bash", `{}`)

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Kind: "action_callback",
		CallbackData: "permission:always:" + itoa(reqID), EventID: "cb1",
	})

	if got := f.perm.calls[0]; !got.allow || got.always || got.matcher {
		t.Fatalf("high-risk-no-field always = %+v, want allow-once (no always/matcher)", got)
	}
}

func TestHandleInbound_AskUserCallback_Answers(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	q := `[{"question":"选哪个方案","options":[{"label":"方案A"},{"label":"方案B"}]}]`
	reqID := f.seedAskUserRequest(t, f.wsID, q)

	// Tap option index 1 (方案B).
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Kind: "action_callback",
		CallbackData: "askuser:" + itoa(reqID) + ":1", EventID: "cb1",
	})

	if len(f.askUser.calls) != 1 || f.askUser.calls[0].reqID != reqID {
		t.Fatalf("ask-user not answered: %+v", f.askUser.calls)
	}
	if got := f.askUser.calls[0].labels; len(got) != 1 || got[0] != "方案B" {
		t.Fatalf("answered labels = %v, want [方案B]", got)
	}
}

func TestHandleInbound_AskUserCallback_CrossProjectRejected(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	ctx := context.Background()
	proj2, _ := f.q.CreateProject(ctx, store.CreateProjectParams{Name: "别的项目", OwnerType: "user", OwnerID: 1})
	col2, _ := f.q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj2.ID, Name: "待办", Position: 0})
	iss2, _ := f.q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col2.ID, Title: "x", Position: 0})
	ws2, _ := f.q.CreateWorkspace(ctx, store.CreateWorkspaceParams{IssueID: sql.NullInt64{Int64: iss2.ID, Valid: true}, Name: "ws2", Path: "/tmp/ws2", Status: "running", OwnerType: "user", OwnerID: 1})
	reqID := f.seedAskUserRequest(t, ws2.ID, `[{"question":"q","options":[{"label":"A"}]}]`)

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Kind: "action_callback",
		CallbackData: "askuser:" + itoa(reqID) + ":0", EventID: "cb2",
	})
	if len(f.askUser.calls) != 0 {
		t.Fatalf("cross-project ask-user answer must be rejected, got %+v", f.askUser.calls)
	}
}

func TestHandleInbound_StopCommand_StopsActiveTask(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	f.svc.setActiveIssue(context.Background(), chat, f.issueID)

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/stop", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("/stop must not route or deliver")
	}
	if len(f.deliverer.stopped) != 1 || f.deliverer.stopped[0] != f.wsID {
		t.Fatalf("expected RemoveSession for ws %d, got %+v", f.wsID, f.deliverer.stopped)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "已停止") {
		t.Fatalf("expected a stop confirmation, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_StopCommand_NoActiveTask_Nudges(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/stop", Kind: "message", EventID: "e1",
	})
	if len(f.deliverer.stopped) != 0 {
		t.Fatalf("no active task -> nothing to stop, got %+v", f.deliverer.stopped)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "没有正在进行") {
		t.Fatalf("expected a no-task nudge, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_DetailCommand_ShowsIssueCard(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	issID := f.newIssue(t, "报表任务", "做一份月度报表")

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "/detail #" + itoa(issID), Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("/detail must not route or deliver")
	}
	if len(f.adapter.pushes) != 1 {
		t.Fatalf("expected one detail reply, got %d", len(f.adapter.pushes))
	}
	reply := f.adapter.pushes[0].Text
	for _, want := range []string{"#" + itoa(issID), "报表任务", "待办", "未启动", "做一份月度报表"} {
		if !strings.Contains(reply, want) {
			t.Errorf("detail missing %q: %s", want, reply)
		}
	}
}

func TestRenderOutbound_PermissionHasAlwaysButton(t *testing.T) {
	ev := event.OutputEvent{Type: event.EventPermissionRequest, PermissionRequest: &event.PermissionRequestData{RequestID: 7, ToolName: "Bash"}}
	text, buttons := renderOutbound(ev, 12, "任务")
	if len(buttons) != 3 || buttons[1].Value != "permission:always:7" {
		t.Fatalf("expected 允许/始终允许/拒绝 buttons, got %+v", buttons)
	}
	if !strings.Contains(text, "Bash") {
		t.Errorf("permission text should mention the tool: %s", text)
	}
}

func TestRenderOutbound_AskUserButtons(t *testing.T) {
	ev := event.OutputEvent{Type: event.EventAskUserRequest, AskUserRequest: &event.AskUserRequestData{
		RequestID: 9,
		Questions: []event.AskUserQuestion{{Question: "选哪个", Options: []event.AskUserQuestionOption{{Label: "A"}, {Label: "B"}}}},
	}}
	text, buttons := renderOutbound(ev, 3, "任务")
	if len(buttons) != 2 || buttons[0].Value != "askuser:9:0" || buttons[1].Value != "askuser:9:1" {
		t.Fatalf("ask-user option buttons wrong: %+v", buttons)
	}
	if !strings.Contains(text, "选哪个") {
		t.Errorf("ask-user text should carry the question: %s", text)
	}
}

func TestRenderOutbound_AskUserMultiSelectFallsBack(t *testing.T) {
	ev := event.OutputEvent{Type: event.EventAskUserRequest, AskUserRequest: &event.AskUserRequestData{
		RequestID: 9, Questions: []event.AskUserQuestion{{Question: "多选", MultiSelect: true, Options: []event.AskUserQuestionOption{{Label: "A"}}}},
	}}
	if _, buttons := renderOutbound(ev, 3, "任务"); buttons != nil {
		t.Fatalf("multi-select should fall back to a nudge (no buttons), got %+v", buttons)
	}
}

func TestRenderOutbound_GateResult(t *testing.T) {
	fail := event.OutputEvent{Type: event.EventGateDone, GateDone: &event.GateDonePayload{Passed: false, FailureCount: 3}}
	if text, _ := renderOutbound(fail, 1, "任务"); !strings.Contains(text, "未通过") || !strings.Contains(text, "3") {
		t.Errorf("gate fail text wrong: %s", text)
	}
	pass := event.OutputEvent{Type: event.EventGateDone, GateDone: &event.GateDonePayload{Passed: true}}
	if text, _ := renderOutbound(pass, 1, "任务"); !strings.Contains(text, "通过") {
		t.Errorf("gate pass text wrong: %s", text)
	}
}

func (f *imbotFixture) seedPermissionRequest(t *testing.T, wsID int64) int64 {
	return f.seedPermissionRequestWithTool(t, wsID, "Bash", "{}")
}

func (f *imbotFixture) seedPermissionRequestWithTool(t *testing.T, wsID int64, tool, toolInput string) int64 {
	t.Helper()
	var id int64
	err := f.db.QueryRowContext(context.Background(),
		`INSERT INTO agent_permission_requests (workspace_id, owner_type, owner_id, session_id, tool_name, tool_input, status, requested_at, expires_at)
		 VALUES (?, 'user', 1, 's', ?, ?, 'pending', CURRENT_TIMESTAMP, ?) RETURNING id`,
		wsID, tool, toolInput, time.Now().Add(time.Hour),
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed permission request: %v", err)
	}
	return id
}

func (f *imbotFixture) seedAskUserRequest(t *testing.T, wsID int64, questionsJSON string) int64 {
	t.Helper()
	var id int64
	err := f.db.QueryRowContext(context.Background(),
		`INSERT INTO agent_ask_user_requests (workspace_id, owner_type, owner_id, session_id, questions_json, status, requested_at, expires_at)
		 VALUES (?, 'user', 1, 's', ?, 'pending', CURRENT_TIMESTAMP, ?) RETURNING id`,
		wsID, questionsJSON, time.Now().Add(time.Hour),
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed ask-user request: %v", err)
	}
	return id
}
