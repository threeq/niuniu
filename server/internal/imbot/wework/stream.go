package wework

// This file implements the 企业微信 智能机器人 (WeCom AI-Bot) WebSocket
// long-connection protocol — the LAN-usable stream mode for the wework channel
// (Issue #616). Unlike the self-built-app path (corp_id/agent_id + cgi-bin HTTP
// callback, see wework.go), the AI-Bot dials OUT a wss long connection, so it
// works behind a LAN/NAT with no public callback URL — the core imbot design
// constraint.
//
// Protocol (https://developer.work.weixin.qq.com/document/path/101463):
//   - Dial wss://openws.work.weixin.qq.com; each frame is JSON
//     {"cmd":..,"headers":{"req_id":..},"body":{..}}. Ack frames carry no cmd:
//     {"headers":{"req_id":..},"errcode":0,"errmsg":"ok"}.
//   - Auth: send aibot_subscribe {bot_id, secret}; errcode==0 means accepted.
//   - Inbound: server pushes aibot_msg_callback (msgid/chatid/chattype/from/
//     msgtype/text); we normalize text messages into imbot.InboundEvent.
//   - Reply: send aibot_respond_msg {msgtype:"stream", stream:{id,finish,content}}
//     echoing the callback's req_id; finish=true ends the stream (≤10 min window).
//   - Heartbeat: ping every 30s.
//
// A bot allows only ONE live connection at a time; the ConnectorManager owns
// backoff+reconnect (Connect returns on error / ctx cancel).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// defaultAibotWSURL is the WeCom AI-Bot long-connection endpoint. Overridable per
// channel via the credential's ws_url (tests point it at an httptest ws server).
const defaultAibotWSURL = "wss://openws.work.weixin.qq.com"

// processingPlaceholder is the interim stream content shown while an agent turn
// runs. Under the stream's replace/refresh semantics it is replaced in place by
// the final answer on the finish=true frame (mirrors wechat's typing marker).
const processingPlaceholder = "🐂 牛牛正在处理…"

// Tunables (package vars so tests can shrink them for fast assertions).
var (
	// streamPingInterval keeps the socket alive (protocol recommends ~30s).
	streamPingInterval = 30 * time.Second
	// streamRefreshInterval re-sends the interim stream frame to hold the reply
	// window open during long agent turns (well under the 10-min stream cap).
	streamRefreshInterval = 30 * time.Second
	// streamReplyWindow caps how long a pending reply's refresh heartbeat runs so
	// a lost Push can never leak a goroutine forever (< the 10-min protocol cap).
	streamReplyWindow = 9 * time.Minute
)

// aibotConn wraps a live AI-Bot socket with a write mutex — gorilla/websocket
// forbids concurrent writers, and the ping heartbeat, per-reply refresh
// heartbeat and Push all write the same connection.
type aibotConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *aibotConn) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

// aibotReply is the pending reply target for one inbound message: the live
// connection to answer over, the callback's req_id (must be echoed), the stream
// id (stable across refreshes), and the cancel for its refresh heartbeat.
type aibotReply struct {
	conn     *aibotConn
	reqID    string
	streamID string
	cancel   context.CancelFunc
}

// --- wire frames ---

type aibotHeaders struct {
	ReqID string `json:"req_id,omitempty"`
}

// aibotOutFrame is a client→server frame (subscribe / ping / respond).
type aibotOutFrame struct {
	Cmd     string       `json:"cmd"`
	Headers aibotHeaders `json:"headers"`
	Body    any          `json:"body,omitempty"`
}

type aibotRespondBody struct {
	Msgtype string      `json:"msgtype"`
	Stream  aibotStream `json:"stream"`
}

type aibotStream struct {
	ID      string `json:"id"`
	Finish  bool   `json:"finish"`
	Content string `json:"content"`
}

// aibotInFrame is a server→client frame: either a push (cmd set, body present)
// or an ack (no cmd, errcode/errmsg set).
type aibotInFrame struct {
	Cmd     string          `json:"cmd"`
	Headers aibotHeaders    `json:"headers"`
	Errcode int             `json:"errcode"`
	Errmsg  string          `json:"errmsg"`
	Body    json.RawMessage `json:"body"`
}

// aibotMsgBody is the subset of an aibot_msg_callback we act on.
type aibotMsgBody struct {
	Msgid    string `json:"msgid"`
	Aibotid  string `json:"aibotid"`
	Chatid   string `json:"chatid"`
	Chattype string `json:"chattype"` // "single" | "group"
	From     struct {
		Userid string `json:"userid"`
	} `json:"from"`
	Msgtype string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

// --- credential helpers ---

// streamCredsPresent reports whether this credential selects the AI-Bot stream
// path (a bot_id is the智能机器人 discriminator; the self-built app has none).
func streamCredsPresent(cred imbot.Credential) bool {
	return credStr(cred, "bot_id") != ""
}

func aibotWSURL(cred imbot.Credential) string {
	if u := credStr(cred, "ws_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultAibotWSURL
}

// randID mints a short random id for req_id / stream.id.
func randID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// --- connection & reply registries ---

func replyKey(botID, chatExtID string) string { return botID + "\x1f" + chatExtID }

func (a *Adapter) putConn(botID string, ac *aibotConn) {
	a.streamMu.Lock()
	a.streamConns[botID] = ac
	a.streamMu.Unlock()
}

// dropConn removes a bot's connection (only if it is still the current one) and
// cancels every pending reply hanging off it.
func (a *Adapter) dropConn(botID string, ac *aibotConn) {
	a.streamMu.Lock()
	if a.streamConns[botID] == ac {
		delete(a.streamConns, botID)
	}
	prefix := botID + "\x1f"
	for k, r := range a.streamReply {
		if strings.HasPrefix(k, prefix) {
			r.cancel()
			delete(a.streamReply, k)
		}
	}
	a.streamMu.Unlock()
}

func (a *Adapter) putReply(botID, chatExtID string, r *aibotReply) {
	a.streamMu.Lock()
	k := replyKey(botID, chatExtID)
	if old := a.streamReply[k]; old != nil {
		old.cancel()
	}
	a.streamReply[k] = r
	a.streamMu.Unlock()
}

// takeReply removes and returns the pending reply for a chat (nil if none / the
// window elapsed).
func (a *Adapter) takeReply(botID, chatExtID string) *aibotReply {
	a.streamMu.Lock()
	k := replyKey(botID, chatExtID)
	r := a.streamReply[k]
	delete(a.streamReply, k)
	a.streamMu.Unlock()
	return r
}

func (a *Adapter) getConn(botID string) *aibotConn {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	return a.streamConns[botID]
}

// --- Connect (AI-Bot stream long connection) ---

// connectStream dials the AI-Bot socket, subscribes, and runs the read loop
// until ctx is cancelled or the socket errors. It blocks; the ConnectorManager
// reconnects with backoff. Because the bot dials OUT, this works behind a
// LAN/NAT with no public URL.
func (a *Adapter) connectStream(ctx context.Context, cred imbot.Credential, handler imbot.InboundHandler) error {
	botID := credStr(cred, "bot_id")
	secret := credStr(cred, "secret")
	if botID == "" || secret == "" {
		return fmt.Errorf("wework: missing bot_id/secret for stream mode")
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, aibotWSURL(cred), nil)
	if err != nil {
		return fmt.Errorf("wework: dial aibot ws: %w", err)
	}
	defer conn.Close()
	ac := &aibotConn{conn: conn}
	a.putConn(botID, ac)
	defer a.dropConn(botID, ac)

	// Close the socket promptly on ctx cancel so the blocking reads below return.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// Authenticate. The next frame is the subscribe ack; a non-zero errcode means
	// the bot_id/secret was rejected — surface it so the manager backs off.
	if err := ac.writeJSON(aibotOutFrame{
		Cmd:     "aibot_subscribe",
		Headers: aibotHeaders{ReqID: randID("sub-")},
		Body:    map[string]string{"bot_id": botID, "secret": secret},
	}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("wework: send aibot_subscribe: %w", err)
	}
	_, ackRaw, err := conn.ReadMessage()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("wework: read subscribe ack: %w", err)
	}
	var ack aibotInFrame
	if json.Unmarshal(ackRaw, &ack) == nil && ack.Errcode != 0 {
		return fmt.Errorf("wework: aibot_subscribe rejected errcode=%d errmsg=%s", ack.Errcode, ack.Errmsg)
	}
	slog.Info("wework: aibot stream connected", "bot", truncate(botID, 12))

	go a.pingLoop(ctx, ac)

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wework: aibot ws read: %w", err)
		}
		a.handleStreamFrame(ctx, botID, ac, data, handler)
	}
}

// pingLoop sends a ping every streamPingInterval to keep the socket alive; it
// returns when ctx is cancelled. Write errors are ignored — a dead socket is
// detected by the read loop, which drives reconnect.
func (a *Adapter) pingLoop(ctx context.Context, ac *aibotConn) {
	ticker := time.NewTicker(streamPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = ac.writeJSON(aibotOutFrame{Cmd: "ping", Headers: aibotHeaders{ReqID: randID("ping-")}})
		}
	}
}

// handleStreamFrame decodes one server frame. aibot_msg_callback text messages
// are normalized + handled (opening the reply stream first); acks and other
// pushes (event callbacks, ping acks) are logged/ignored.
func (a *Adapter) handleStreamFrame(ctx context.Context, botID string, ac *aibotConn, data []byte, handler imbot.InboundHandler) {
	var f aibotInFrame
	if json.Unmarshal(data, &f) != nil {
		return
	}
	switch f.Cmd {
	case "aibot_msg_callback":
		var b aibotMsgBody
		if json.Unmarshal(f.Body, &b) != nil {
			return
		}
		ev, ok := normalizeAibotMsg(b)
		if !ok {
			return
		}
		// Open the reply stream now (holds the reply window) and remember where to
		// send the final answer, keyed by chat, before routing to the agent.
		a.beginReply(ctx, botID, ac, ev.ChatExtID, f.Headers.ReqID)
		slog.Debug("wework: aibot inbound", "chat", truncate(ev.ChatExtID, 24), "req_id", f.Headers.ReqID != "")
		if handler != nil {
			handler(ctx, ev)
		}
	case "":
		// Ack frame (subscribe/ping/respond). A non-zero errcode past subscribe is
		// a benign transient (e.g. a rate-limited refresh); log at debug.
		if f.Errcode != 0 {
			slog.Debug("wework: aibot ack errcode", "errcode", f.Errcode, "errmsg", f.Errmsg)
		}
	default:
		// aibot_event_callback (enter_chat, etc.) and any future cmd: ignored.
	}
}

// normalizeAibotMsg turns an aibot_msg_callback body into a normalized inbound
// event. ok=false skips non-text messages (media/events) — text-only first cut,
// matching the self-built-app webhook path. ChatExtID is the group id for group
// chats, else the sender's userid (so 1:1 admission is per-user).
func normalizeAibotMsg(b aibotMsgBody) (imbot.InboundEvent, bool) {
	if !strings.EqualFold(strings.TrimSpace(b.Msgtype), "text") {
		return imbot.InboundEvent{}, false
	}
	from := strings.TrimSpace(b.From.Userid)
	if from == "" {
		return imbot.InboundEvent{}, false
	}
	isGroup := strings.EqualFold(b.Chattype, "group")
	text := strings.TrimSpace(b.Text.Content)
	if isGroup {
		// A group @-mention inlines "@<botname>" at the start of Content; strip the
		// single leading mention so the real text / slash command is recognized.
		text = stripLeadingAtMention(text)
	}
	if text == "" {
		return imbot.InboundEvent{}, false
	}
	chatExtID := strings.TrimSpace(b.Chatid)
	if chatExtID == "" || !isGroup {
		chatExtID = from
	}
	return imbot.InboundEvent{
		Channel:      imbot.ChannelWework,
		ChatExtID:    chatExtID,
		ActorExtID:   from,
		MessageExtID: strings.TrimSpace(b.Msgid),
		Text:         text,
		Kind:         "message",
		EventID:      strings.TrimSpace(b.Msgid),
	}, true
}

// beginReply opens the reply stream for an inbound message and starts a refresh
// heartbeat that holds the reply window open until Push finishes it or the
// window elapses. Keyed by chat so Push (which only knows the chat id) can find
// the right req_id + stream id.
func (a *Adapter) beginReply(ctx context.Context, botID string, ac *aibotConn, chatExtID, reqID string) {
	if chatExtID == "" {
		return
	}
	streamID := randID("st-")
	hbCtx, cancel := context.WithTimeout(context.Background(), streamReplyWindow)
	a.putReply(botID, chatExtID, &aibotReply{conn: ac, reqID: reqID, streamID: streamID, cancel: cancel})
	go func() {
		defer cancel()
		// Open the stream immediately so the reply window starts now.
		_ = ac.writeJSON(respondFrame(reqID, streamID, processingPlaceholder, false))
		ticker := time.NewTicker(streamRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ctx.Done(): // connection torn down
				return
			case <-ticker.C:
				_ = ac.writeJSON(respondFrame(reqID, streamID, processingPlaceholder, false))
			}
		}
	}()
}

// respondFrame builds an aibot_respond_msg stream frame.
func respondFrame(reqID, streamID, content string, finish bool) aibotOutFrame {
	return aibotOutFrame{
		Cmd:     "aibot_respond_msg",
		Headers: aibotHeaders{ReqID: reqID},
		Body:    aibotRespondBody{Msgtype: "stream", Stream: aibotStream{ID: streamID, Finish: finish, Content: content}},
	}
}

// --- Push (stream reply) ---

// pushStream sends the final answer over the live AI-Bot connection: it looks up
// the pending reply for the chat, stops its refresh heartbeat, and writes the
// finish=true stream frame (echoing the callback's req_id + stream id). An empty
// text still finishes the stream. Buttons fall back to readable text lines (the
// AI-Bot stream reply carries no interactive card).
func (a *Adapter) pushStream(_ context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	botID := credStr(cred, "bot_id")
	to := strings.TrimSpace(msg.ChatExtID)
	if to == "" {
		return fmt.Errorf("wework: empty chat id")
	}
	r := a.takeReply(botID, to)
	if r == nil {
		return fmt.Errorf("wework: no live aibot reply stream for chat %s (reply window elapsed?)", truncate(to, 24))
	}
	r.cancel() // stop the interim refresh heartbeat
	content := msg.Text
	if len(msg.Buttons) > 0 {
		content = appendButtonLines(content, msg.Buttons)
	}
	if err := r.conn.writeJSON(respondFrame(r.reqID, r.streamID, content, true)); err != nil {
		return fmt.Errorf("wework: aibot respond finish: %w", err)
	}
	return nil
}
