package wework

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/imbot"
)

var streamUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func streamCred(wsURL string) imbot.Credential {
	return imbot.Credential{Channel: imbot.ChannelWework, Config: map[string]any{
		"bot_id": "bot_x",
		"secret": "sec_y",
		"ws_url": wsURL,
	}}
}

// wsURLOf turns an httptest http:// base into its ws:// form + path.
func wsURLOf(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// aibotOutParsed decodes a client→server frame in a test.
type aibotOutParsed struct {
	Cmd     string       `json:"cmd"`
	Headers aibotHeaders `json:"headers"`
	Body    struct {
		BotID   string `json:"bot_id"`
		Secret  string `json:"secret"`
		Msgtype string `json:"msgtype"`
		Stream  struct {
			ID      string `json:"id"`
			Finish  bool   `json:"finish"`
			Content string `json:"content"`
		} `json:"stream"`
	} `json:"body"`
}

func TestStream_SubscribeInboundAndReply(t *testing.T) {
	subFrames := make(chan aibotOutParsed, 1)
	outFrames := make(chan aibotOutParsed, 8)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := streamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 1. read the subscribe frame.
		_, sub, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var subF aibotOutParsed
		_ = json.Unmarshal(sub, &subF)
		subFrames <- subF
		// 2. ack the subscribe, then push a text callback.
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"headers":{"req_id":"sub"},"errcode":0,"errmsg":"ok"}`))
		cb := `{"cmd":"aibot_msg_callback","headers":{"req_id":"REQ-1"},"body":{"msgid":"m_9","aibotid":"ab","chatid":"c_1","chattype":"group","from":{"userid":"u_7"},"msgtype":"text","text":{"content":"@牛牛 帮我做张表"}}}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(cb))
		// 3. forward every further client frame (placeholder + finish reply).
		for {
			_, m, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var f aibotOutParsed
			if json.Unmarshal(m, &f) == nil {
				outFrames <- f
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New()
	events := make(chan imbot.InboundEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Connect(ctx, streamCred(wsURLOf(srv)), func(_ context.Context, ev imbot.InboundEvent) { events <- ev }) }()

	// Subscribe frame carries the bot credentials.
	select {
	case sf := <-subFrames:
		if sf.Cmd != "aibot_subscribe" || sf.Body.BotID != "bot_x" || sf.Body.Secret != "sec_y" {
			t.Fatalf("bad subscribe frame: %+v", sf)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no subscribe frame")
	}

	// Inbound normalized: group chat id, @-mention stripped, msg id carried.
	select {
	case ev := <-events:
		if ev.ChatExtID != "c_1" || ev.Text != "帮我做张表" || ev.EventID != "m_9" ||
			ev.ActorExtID != "u_7" || ev.Channel != imbot.ChannelWework {
			t.Fatalf("bad inbound event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound event delivered")
	}

	// The adapter opens the reply stream immediately (finish=false placeholder).
	var streamID string
	select {
	case f := <-outFrames:
		if f.Cmd != "aibot_respond_msg" || f.Headers.ReqID != "REQ-1" || f.Body.Stream.Finish {
			t.Fatalf("bad placeholder respond frame: %+v", f)
		}
		streamID = f.Body.Stream.ID
	case <-time.After(2 * time.Second):
		t.Fatal("adapter never opened the reply stream")
	}

	// Push the final answer → finish=true frame over the same stream + req_id.
	if err := a.Push(ctx, streamCred(wsURLOf(srv)), imbot.OutboundMessage{ChatExtID: "c_1", Text: "已完成"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	select {
	case f := <-outFrames:
		if !f.Body.Stream.Finish || f.Body.Stream.Content != "已完成" ||
			f.Headers.ReqID != "REQ-1" || f.Body.Stream.ID != streamID {
			t.Fatalf("bad finish respond frame: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Push never sent the finish frame")
	}
}

func TestStream_SubscribeRejectedReturnsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := streamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"headers":{"req_id":"sub"},"errcode":40001,"errmsg":"bad secret"}`))
		// Hold open so the adapter's error comes from the rejected ack, not EOF.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New()
	err := a.Connect(context.Background(), streamCred(wsURLOf(srv)), nil)
	if err == nil || !strings.Contains(err.Error(), "aibot_subscribe rejected") {
		t.Fatalf("Connect err = %v, want subscribe-rejected error", err)
	}
}

func TestStream_MissingCredsAndNoLiveStream(t *testing.T) {
	a := New()
	// bot_id present but secret missing → connect fails fast.
	err := a.Connect(context.Background(), imbot.Credential{Config: map[string]any{"bot_id": "b"}}, nil)
	if err == nil {
		t.Fatal("expected error for bot_id without secret")
	}
	// Push with no live reply stream for the chat is an error (window elapsed).
	perr := a.Push(context.Background(), streamCred("ws://unused"), imbot.OutboundMessage{ChatExtID: "c_1", Text: "x"})
	if perr == nil {
		t.Fatal("expected error pushing with no live reply stream")
	}
}

func TestNormalizeAibotMsg(t *testing.T) {
	// Single chat: chatExtID is the sender's userid (per-user admission).
	ev, ok := normalizeAibotMsg(aibotMsgBody{
		Msgid: "m1", Chattype: "single", Msgtype: "text",
		From:  struct{ Userid string `json:"userid"` }{Userid: "u_1"},
		Text:  struct{ Content string `json:"content"` }{Content: "hi"},
	})
	if !ok || ev.ChatExtID != "u_1" || ev.Text != "hi" {
		t.Fatalf("single-chat normalize wrong: ok=%v ev=%+v", ok, ev)
	}
	// Non-text is skipped.
	if _, ok := normalizeAibotMsg(aibotMsgBody{Msgid: "m2", Msgtype: "image", From: struct{ Userid string `json:"userid"` }{Userid: "u"}}); ok {
		t.Fatal("image message should be skipped")
	}
	// Empty sender is skipped.
	if _, ok := normalizeAibotMsg(aibotMsgBody{Msgid: "m3", Msgtype: "text", Text: struct{ Content string `json:"content"` }{Content: "x"}}); ok {
		t.Fatal("message with empty sender should be skipped")
	}
}

func TestStreamCredsPresent(t *testing.T) {
	if !streamCredsPresent(streamCred("")) {
		t.Error("bot_id credential should select stream mode")
	}
	app := imbot.Credential{Config: map[string]any{"corp_id": "c", "agent_id": "1", "secret": "s"}}
	if streamCredsPresent(app) {
		t.Error("self-built-app credential should NOT select stream mode")
	}
}
