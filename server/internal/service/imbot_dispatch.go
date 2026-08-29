package service

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// IMBotDispatcher subscribes to the in-process event bus and pushes outbound
// notifications to the IM chats of the affected project (Feishu/Lark WS long
// connection Push — LAN-friendly, no public URL). It is the outbound half of
// the W1 end-to-end path: an agent finishes / needs approval -> the bound
// project's active chats get a de-jargonized message.
type IMBotDispatcher struct {
	bus      *event.Bus
	q        *store.Queries
	svc      *IMBotService
	adapters map[imbot.ChannelType]imbot.ChannelAdapter

	mu   sync.Mutex
	ch   chan event.OutputEvent
	stop chan struct{}
	done chan struct{}

	// sendMu guards senders: one serial sender goroutine per chat, so a push that
	// is being retried cannot be overtaken by the next message to the same chat
	// (see sendQueued). wg tracks them so Stop drains in-flight sends.
	sendMu  sync.Mutex
	senders map[int64]*chatSender
	wg      sync.WaitGroup

	// retrySleep is the backoff sleeper, overridable in tests so a retry schedule
	// can be exercised without real waiting. It must respect the stop channel.
	retrySleep func(d time.Duration, stop <-chan struct{}) bool
}

// NewIMBotDispatcher builds a dispatcher. svc supplies credential decryption
// (decrypt only happens in the service layer).
func NewIMBotDispatcher(bus *event.Bus, q *store.Queries, svc *IMBotService, adapters map[imbot.ChannelType]imbot.ChannelAdapter) *IMBotDispatcher {
	return &IMBotDispatcher{bus: bus, q: q, svc: svc, adapters: adapters}
}

// Start subscribes to the bus and runs the dispatch loop until Stop.
func (d *IMBotDispatcher) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ch != nil {
		return
	}
	d.ch = d.bus.Subscribe()
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go d.loop(d.ch, d.stop, d.done)
	slog.Info("imbot: dispatcher started")
}

// Stop unsubscribes and waits for the loop to exit.
func (d *IMBotDispatcher) Stop() {
	d.mu.Lock()
	ch, stop, done := d.ch, d.stop, d.done
	d.ch, d.stop, d.done = nil, nil, nil
	d.mu.Unlock()
	if ch == nil {
		return
	}
	close(stop)
	d.bus.Unsubscribe(ch)
	<-done

	// Wait for the per-chat senders to notice stop and exit, so a shutdown does not
	// race an in-flight push. They observe the same closed stop channel, and
	// sendWithRetry abandons its backoff on it, so this cannot block for a full
	// retry schedule.
	d.wg.Wait()
	d.sendMu.Lock()
	d.senders = nil
	d.sendMu.Unlock()
}

func (d *IMBotDispatcher) loop(ch chan event.OutputEvent, stop, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			d.handle(ev, stop)
		}
	}
}

// maxOutboundLen caps a forwarded agent reply so a long answer never trips a
// platform message-size limit (Telegram ~4096; keep headroom for the title).
const maxOutboundLen = 3500

// failureDetailMaxRunes bounds the failure reason carried into a chat. An agent
// failure can be a multi-screen stack trace; the first few hundred runes hold the
// part someone can act on, and the rest would bury it.
const failureDetailMaxRunes = 300

// interestedIn reports whether an event type should reach IM chats. Streaming
// text/tool noise is ignored; only meaningful lifecycle/interaction events.
// EventAgentDone now carries the agent's real final reply (proxy publishes the
// turn's last assistant text) — that is the primary per-turn chat message.
// EventAgentFailed is the same turn's other outcome and must be forwarded too
// (issue #680): without it a crashed agent looks exactly like a working one,
// because the only visible change is the 🐂 marker disappearing.
// EventWorkspaceCompleted is intentionally NOT forwarded: it would double up on
// the agent_done reply and re-create the old "『…』已完成" spam.
func interestedIn(t string) bool {
	switch t {
	case event.EventAgentDone,
		event.EventAgentFailed,
		event.EventScheduleTrigger,
		event.EventGateDone,
		event.EventAskUserRequest,
		event.EventPermissionRequest:
		return true
	}
	return false
}

// outTarget is one concrete chat (optionally in-thread) that should receive an
// outbound message, resolved together with the channel credential needed to push.
type outTarget struct {
	channelID     int64
	chatID        int64
	channelType   string
	credentialEnc string
	chatExtID     string
	threadExtID   string
}

func (d *IMBotDispatcher) handle(ev event.OutputEvent, stop <-chan struct{}) {
	if !interestedIn(ev.Type) || ev.WorkspaceId == 0 {
		return
	}
	slog.Debug("imbot: dispatch event received", "type", ev.Type, "workspace", ev.WorkspaceId)
	ctx := context.Background()

	// The agent finished this workspace's turn — clear the 🐂 "正在执行中"
	// markers placed on the inbound messages that drove it (runs regardless of
	// whether the reply below has any IM target).
	//
	// A FAILED turn is just as finished as a successful one: leaving the marker on
	// would say "still working" forever. Cleared before the failure message is
	// pushed so the two never disagree about whether the turn is over.
	if ev.Type == event.EventAgentDone || ev.Type == event.EventAgentFailed {
		d.svc.clearProcessingReactions(ctx, ev.WorkspaceId)
	}
	pctx, err := d.q.GetProjectContextByWorkspace(ctx, ev.WorkspaceId)
	if err != nil {
		return // workspace not resolvable to a project (e.g. assistant scratch)
	}
	issueID := int64(0)
	if pctx.IssueID.Valid {
		issueID = pctx.IssueID.Int64
	}
	text, buttons := renderOutbound(ev, issueID, pctx.IssueTitle)
	if text == "" {
		return
	}

	// Scope the push to the chats that actually own this conversation — a thread
	// bound to the issue, or a chat whose active/pinned pointer is this issue.
	// A task with no IM binding (pure WebUI work) yields no targets, so IM stays
	// quiet instead of broadcasting every project workspace's reply to every chat.
	targets := d.resolveTargets(ctx, pctx.ProjectID, issueID)
	// Diagnostic: confirm the dispatcher reached the push stage and what it is
	// sending (runes) and to how many chats. Pairs with each adapter's send log
	// to trace an outbound message end-to-end when a reply goes missing in IM.
	slog.Info("imbot: dispatch outbound", "workspace", ev.WorkspaceId, "issue", issueID, "type", ev.Type, "runes", utf8.RuneCountInString(text), "targets", len(targets))
	for _, tg := range targets {
		adapter, ok := d.adapters[imbot.ChannelType(tg.channelType)]
		if !ok {
			continue
		}
		cred, err := d.svc.decryptCred(tg.channelType, tg.credentialEnc)
		if err != nil {
			slog.Warn("imbot: dispatch decrypt failed", "channel", tg.channelID, "error", err)
			continue
		}
		msg := imbot.OutboundMessage{
			ChatExtID:   tg.chatExtID,
			ThreadExtID: tg.threadExtID,
			Text:        text,
			Buttons:     buttons,
		}
		// Hand the send to this chat's serial sender rather than pushing inline
		// (issue #681). Inline retries would block the dispatch loop, and because
		// event.Bus drops events when a subscriber's buffer fills, a loop stalled on
		// backoff would lose OTHER workspaces' messages entirely — trading one
		// dropped reply for several.
		d.enqueueSend(tg, adapter, cred, msg, stop)
	}
}

// --- outbound send with retry (issue #681) -----------------------------------

const (
	// pushMaxAttempts bounds one message's delivery attempts, including the first.
	// Three is enough to ride out a token refresh or a brief rate-limit window
	// without holding a message so long that it arrives detached from the
	// conversation it belongs to.
	pushMaxAttempts = 3

	// pushRetryBaseDelay is the first backoff, doubled per attempt (1s, 2s). The
	// pattern mirrors the connector manager's reconnect backoff, at a much shorter
	// horizon: a stale chat reply is worth far less than a stale connection.
	pushRetryBaseDelay = time.Second

	// pushSenderQueue bounds one chat's pending sends. A chat this far behind is
	// already failing; queueing more would grow memory without making the group any
	// more informed.
	pushSenderQueue = 32
)

// pendingPush is one queued outbound message plus everything needed to send it.
type pendingPush struct {
	adapter imbot.ChannelAdapter
	cred    imbot.Credential
	msg     imbot.OutboundMessage
	// channelID/chatID are for logging only — they identify the failure in logs.
	channelID int64
	chatID    int64
}

// chatSender serializes sends for ONE chat.
//
// Per-chat rather than global: messages to the same chat must arrive in the order
// they were produced (a group reading "已完成" before the answer it refers to is
// worse than a slow answer), while two unrelated chats have no ordering
// relationship and must not be able to stall each other.
//
// A sender goroutine is created on a chat's first outbound message and lives until
// Stop — it is never reclaimed when a chat goes idle. That bounds the goroutine
// count by the number of chats that have EVER received a message in this process,
// which is a small, slowly-growing number (bound chats, not messages); reaping idle
// senders would need a timer per chat and a race-free handoff between the reaper
// and enqueueSend, which costs more complexity than the goroutines cost memory.
type chatSender struct {
	queue chan pendingPush
}

// enqueueSend hands a message to its chat's sender, starting one if needed.
//
// Non-blocking by contract: a full queue drops the message with a log rather than
// blocking the dispatch loop. Dropping here is the same outcome as before this fix,
// but only after 32 queued messages rather than on the first network blip.
// stop is threaded in from the dispatch loop rather than read off the struct: the
// loop already owns the channel for its lifetime, and reading d.stop here would
// need the OTHER mutex (mu, which Stop holds while clearing it) — a lock-ordering
// hazard for no benefit.
func (d *IMBotDispatcher) enqueueSend(tg outTarget, adapter imbot.ChannelAdapter, cred imbot.Credential, msg imbot.OutboundMessage, stop <-chan struct{}) {
	select {
	case <-stop: // shutting down: nothing will drain a new queue
		return
	default:
	}
	d.sendMu.Lock()
	if d.senders == nil {
		d.senders = map[int64]*chatSender{}
	}
	s := d.senders[tg.chatID]
	if s == nil {
		s = &chatSender{queue: make(chan pendingPush, pushSenderQueue)}
		d.senders[tg.chatID] = s
		d.wg.Add(1)
		go d.runSender(s, stop)
	}
	d.sendMu.Unlock()

	p := pendingPush{
		adapter: adapter, cred: cred, msg: msg,
		channelID: tg.channelID, chatID: tg.chatID,
	}
	select {
	case s.queue <- p:
	default:
		slog.Warn("imbot: outbound queue full; message dropped",
			"channel", tg.channelID, "chat", tg.chatID, "queue", pushSenderQueue)
	}
}

// runSender drains one chat's queue until stop closes.
func (d *IMBotDispatcher) runSender(s *chatSender, stop <-chan struct{}) {
	defer d.wg.Done()
	for {
		select {
		case <-stop:
			return
		case p := <-s.queue:
			d.sendWithRetry(p, stop)
		}
	}
}

// sendWithRetry attempts one message until it succeeds, is judged permanent, or
// runs out of attempts.
//
// Before this, a failed push was logged and forgotten — so a platform rate limit,
// a briefly-expired token or a network blip PERMANENTLY lost an agent's reply, and
// the user saw a task that simply never answered.
func (d *IMBotDispatcher) sendWithRetry(p pendingPush, stop <-chan struct{}) {
	for attempt := 1; ; attempt++ {
		// A fresh context per attempt: the dispatch loop's context may be long gone,
		// and a retry should not inherit an already-consumed deadline.
		//
		// Cancelled by stop as well as by the timeout, so shutdown does not wait out a
		// hung request. Without this, Stop blocks in wg.Wait() for up to
		// pushAttemptTimeout while an unresponsive platform holds the connection open
		// — the in-flight Push is exactly the case Stop needs to interrupt.
		ctx, cancel := contextWithStop(stop, pushAttemptTimeout)
		err := p.adapter.Push(ctx, p.cred, p.msg)
		cancel()
		if err == nil {
			if attempt > 1 {
				slog.Info("imbot: outbound push succeeded on retry",
					"channel", p.channelID, "chat", p.chatID, "attempt", attempt)
			}
			return
		}
		// A permanent failure (bad credentials, chat gone, malformed request) will
		// fail identically forever. Retrying wastes quota and, for auth errors, can
		// read as credential brute-forcing to platform risk controls.
		if !imbot.IsRetryablePush(err) {
			slog.Warn("imbot: outbound push failed permanently; not retrying",
				"channel", p.channelID, "chat", p.chatID, "attempt", attempt, "error", err)
			return
		}
		if attempt >= pushMaxAttempts {
			// Logged distinctly from a single failure: this line means a message the
			// user was waiting for is now definitively lost.
			slog.Error("imbot: outbound push gave up after retries; message lost",
				"channel", p.channelID, "chat", p.chatID, "attempts", attempt, "error", err)
			return
		}
		delay := pushRetryBaseDelay << (attempt - 1)
		slog.Warn("imbot: outbound push failed; will retry",
			"channel", p.channelID, "chat", p.chatID, "attempt", attempt,
			"retry_in", delay.String(), "error", err)
		if !d.sleep(delay, stop) {
			return // shutting down
		}
	}
}

// pushAttemptTimeout bounds one delivery attempt so a hung platform request cannot
// occupy a chat's sender indefinitely.
const pushAttemptTimeout = 30 * time.Second

// contextWithStop returns a context that expires after timeout OR when stop
// closes, whichever comes first. The returned cancel must always be called; it
// also releases the watchdog goroutine. Calling it more than once is safe, as
// context.CancelFunc requires.
func contextWithStop(stop <-chan struct{}, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	done := make(chan struct{})
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done(): // finished or timed out normally
		case <-done: // cancel() already called by the caller
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(done) })
		cancel()
	}
}

// sleep waits for d, returning false if stop closed first. Overridable via
// retrySleep so tests need not wait out real backoff.
func (d *IMBotDispatcher) sleep(dur time.Duration, stop <-chan struct{}) bool {
	if d.retrySleep != nil {
		return d.retrySleep(dur, stop)
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

// resolveTargets returns the active chats of a project that are bound to issueID,
// each paired with the thread (if any) that owns the conversation. A chat is a
// target when it has a thread bound to the issue (design §4.4 thread routing) OR
// its active/pinned pointer is the issue (the no-thread DM case). issueID==0 (an
// unlinked scratch workspace) has no owner and yields nothing.
func (d *IMBotDispatcher) resolveTargets(ctx context.Context, projectID, issueID int64) []outTarget {
	if issueID == 0 {
		return nil
	}
	threadByChat := map[int64]string{}
	if threads, err := d.q.ListIMBotThreadsByIssue(ctx, issueID); err == nil {
		for _, th := range threads {
			// A synthetic proactive binding (see proactiveThreadExtID) marks "this
			// chat should hear about this issue" without naming a real platform
			// thread. Keep the chat as a target but blank the thread so the reply
			// goes to the chat itself — passing the sentinel to an adapter would ask
			// the platform to post into a thread that does not exist.
			if isProactiveThreadExtID(th.ThreadExtID) {
				if _, exists := threadByChat[th.ChatID]; !exists {
					threadByChat[th.ChatID] = ""
				}
				continue
			}
			threadByChat[th.ChatID] = th.ThreadExtID
		}
	}
	// Shared-bot routing (design §5): the chats that own this conversation are
	// those ROUTED to this project (chat.project_id), not those under a
	// project-owned channel. Query them directly, then look up each chat's
	// channel for the credential to push with.
	chats, err := d.q.ListActiveIMBotChatsByProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil || len(chats) == 0 {
		return nil
	}
	channelByID := map[int64]store.ImBotChannel{}
	var out []outTarget
	for _, chat := range chats {
		thread, bound := threadByChat[chat.ID]
		isActive := chat.ActiveIssueID.Valid && chat.ActiveIssueID.Int64 == issueID
		isPinned := chat.PinnedIssueID.Valid && chat.PinnedIssueID.Int64 == issueID
		if !bound && !isActive && !isPinned {
			continue
		}
		ch, ok := channelByID[chat.ChannelID]
		if !ok {
			loaded, cerr := d.q.GetIMBotChannel(ctx, chat.ChannelID)
			if cerr != nil || loaded.Status != "active" {
				continue // channel gone or disabled — skip this chat
			}
			ch = loaded
			channelByID[chat.ChannelID] = ch
		}
		out = append(out, outTarget{
			channelID: ch.ID, chatID: chat.ID,
			channelType: ch.ChannelType, credentialEnc: ch.CredentialEnc,
			chatExtID: chat.ChatExtID, threadExtID: thread,
		})
	}
	return out
}

// outboundTitle is the `#<id> <名字>` line prefixed on every outbound message so
// the user can tell which conversation (workspace/task) it came from — and can
// reply `#<id>` to switch back to it (see parseHashSwitch). The full title is
// shown (no length cap); the `#<id>` prefix has no space so it matches the reply
// syntax verbatim.
func outboundTitle(issueID int64, issueTitle string) string {
	return formatTaskRef(issueID, issueTitle)
}

// formatTaskRef renders the `#<id> <标题>` reference shared by the outbound header
// and the new-conversation task marker. The full title is shown (no length cap);
// a blank title falls back to "任务". The `#<id>` prefix has no space so it
// matches the `#<id>` reply-to-switch syntax verbatim.
func formatTaskRef(issueID int64, issueTitle string) string {
	title := strings.TrimSpace(issueTitle)
	if title == "" {
		title = "任务"
	}
	return "#" + strconv.FormatInt(issueID, 10) + " " + title
}

// truncateOutbound bounds a forwarded reply to maxOutboundLen runes, appending a
// marker so a clipped answer is obviously incomplete rather than silently cut.
func truncateOutbound(s string) string {
	r := []rune(s)
	if len(r) <= maxOutboundLen {
		return s
	}
	return string(r[:maxOutboundLen]) + "\n…（内容较长已截断，完整结果请到牛牛里查看）"
}

// renderOutbound turns a bus event into the message pushed to IM. Every message
// is titled with `#<id> <名字>` (outboundTitle). EventAgentDone forwards the
// agent's real reply verbatim (below the title); the rest are de-jargonized
// interaction/lifecycle lines. For a permission request it also returns the
// approve/deny buttons whose callback payloads the inbound handler decodes back
// into a PermissionService.Decide (the W2 permission闭环).
func renderOutbound(ev event.OutputEvent, issueID int64, issueTitle string) (string, []imbot.Button) {
	header := outboundTitle(issueID, issueTitle)
	switch ev.Type {
	case event.EventAgentDone:
		body := strings.TrimSpace(ev.Content)
		if body == "" || body == "completed" {
			body = "✅ 已处理完成。"
		}
		return header + "\n\n" + truncateOutbound(body), nil
	case event.EventAgentFailed:
		// The failure REASON is the actionable part — a CLI/auth/provider message
		// tells someone what to fix, while a generic "出错了" only tells them to go
		// look somewhere else. Same reasoning as the employee's
		// reportAnalysisTrouble, which carries its cause for exactly this reason.
		//
		// Clipped rather than truncated to the full outbound budget: an agent failure
		// can carry a very long stack trace, and a wall of it in a group chat buries
		// the one line that matters.
		body := clipDetail(ev.Content, failureDetailMaxRunes)
		if body == "" {
			return header + "\n\n❌ 执行失败了，请到牛牛里查看详情。", nil
		}
		return header + "\n\n❌ 执行失败了。原因：" + body + "\n\n详情请到牛牛里查看。", nil
	case event.EventScheduleTrigger:
		return header + "\n\n⏰ 定时任务已触发。", nil
	case event.EventGateDone:
		// Informative result instead of a bare "检查已完成": pass/fail + failure count
		// so the user knows whether to look, without opening 牛牛.
		if ev.GateDone != nil && !ev.GateDone.Passed {
			n := ev.GateDone.FailureCount
			if n <= 0 {
				n = 1
			}
			return header + "\n\n🔎 检查未通过，发现 " + strconv.Itoa(n) + " 处问题，请到牛牛里查看详情。", nil
		}
		return header + "\n\n✅ 检查通过。", nil
	case event.EventAskUserRequest:
		return renderAskUser(header, ev)
	case event.EventPermissionRequest:
		if ev.PermissionRequest == nil || ev.PermissionRequest.RequestID <= 0 {
			// No decidable request id — fall back to a "go check in 牛牛" nudge.
			return header + "\n\n🔐 请求执行一项操作，请到牛牛里批准或拒绝。", nil
		}
		id := strconv.FormatInt(ev.PermissionRequest.RequestID, 10)
		text := header + "\n\n🔐 想执行一项操作"
		if tool := strings.TrimSpace(ev.PermissionRequest.ToolName); tool != "" {
			text += "（" + tool + "）"
		}
		text += "，允许吗？"
		buttons := []imbot.Button{
			{Label: "允许", Value: "permission:approve:" + id},
			{Label: "始终允许", Value: "permission:always:" + id},
			{Label: "拒绝", Value: "permission:deny:" + id},
		}
		return text, buttons
	}
	return "", nil
}

// maxAskUserButtons caps how many option buttons a pushed ask-user card carries;
// beyond it (or for multi-question / multi-select requests) the card degrades to a
// "go answer in 牛牛" nudge rather than rendering an unwieldy button wall.
const maxAskUserButtons = 10

// renderAskUser turns an ask_user request into a question card with one tappable
// button per option — the in-IM answer path. It only handles the common shape
// (exactly one single-select question with a manageable option count); anything
// richer (several questions, multi-select) still routes the user to 牛牛, since a
// flat button row cannot express it faithfully.
func renderAskUser(header string, ev event.OutputEvent) (string, []imbot.Button) {
	nudge := header + "\n\n❓ 需要你的确认，请到牛牛里查看。"
	d := ev.AskUserRequest
	if d == nil || d.RequestID <= 0 || len(d.Questions) != 1 {
		return nudge, nil
	}
	q := d.Questions[0]
	if q.MultiSelect || len(q.Options) == 0 || len(q.Options) > maxAskUserButtons {
		return nudge, nil
	}
	id := strconv.FormatInt(d.RequestID, 10)
	text := header + "\n\n❓ "
	if h := strings.TrimSpace(q.Header); h != "" {
		text += "[" + h + "] "
	}
	text += strings.TrimSpace(q.Question)
	buttons := make([]imbot.Button, 0, len(q.Options))
	for i, opt := range q.Options {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			continue
		}
		buttons = append(buttons, imbot.Button{Label: label, Value: "askuser:" + id + ":" + strconv.Itoa(i)})
	}
	if len(buttons) == 0 {
		return nudge, nil
	}
	return text, buttons
}
