package dingtalk

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/imbot"
)

func testCred(baseURL string) imbot.Credential {
	return imbot.Credential{Channel: imbot.ChannelDingTalk, Config: map[string]any{
		"client_id":     "appkey_x",
		"client_secret": "appsecret_y",
		"robot_code":    "robot_x",
		"base_url":      baseURL,
	}}
}

func TestPush_SendsTextWithToken(t *testing.T) {
	var tokenHits, sendHits atomic.Int64
	var gotToken, gotConv, gotMsgKey, gotContent, gotRobot string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/accessToken"):
			tokenHits.Add(1)
			_, _ = io.WriteString(w, `{"accessToken":"tok-abc","expireIn":7200}`)
		case strings.HasSuffix(r.URL.Path, "/robot/groupMessages/send"):
			sendHits.Add(1)
			gotToken = r.Header.Get("x-acs-dingtalk-access-token")
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				RobotCode          string `json:"robotCode"`
				OpenConversationID string `json:"openConversationId"`
				MsgKey             string `json:"msgKey"`
				MsgParam           string `json:"msgParam"`
			}
			_ = json.Unmarshal(body, &msg)
			gotConv = msg.OpenConversationID
			gotMsgKey = msg.MsgKey
			gotRobot = msg.RobotCode
			var param struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal([]byte(msg.MsgParam), &param)
			gotContent = param.Text
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	if err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "cid_1", Text: "done"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotToken != "tok-abc" {
		t.Errorf("access-token header=%q, want tok-abc", gotToken)
	}
	if gotConv != "cid_1" {
		t.Errorf("openConversationId=%q, want cid_1", gotConv)
	}
	if gotMsgKey != "sampleMarkdown" {
		t.Errorf("msgKey=%q, want sampleMarkdown (markdown rendering)", gotMsgKey)
	}
	if gotRobot != "robot_x" {
		t.Errorf("robotCode=%q, want robot_x", gotRobot)
	}
	if gotContent != "done" {
		t.Errorf("markdown text=%q, want done", gotContent)
	}

	// Second push reuses the cached token (no extra token fetch).
	if err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "cid_1", Text: "again"}); err != nil {
		t.Fatalf("Push #2: %v", err)
	}
	if tokenHits.Load() != 1 {
		t.Errorf("token fetched %d times, want 1 (cached)", tokenHits.Load())
	}
	if sendHits.Load() != 2 {
		t.Errorf("send hits=%d, want 2", sendHits.Load())
	}
}

func TestPush_ButtonsSendActionCardWithFallback(t *testing.T) {
	var gotMsgKey, gotParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			_, _ = io.WriteString(w, `{"accessToken":"t","expireIn":7200}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			MsgKey   string `json:"msgKey"`
			MsgParam string `json:"msgParam"`
		}
		_ = json.Unmarshal(body, &msg)
		gotMsgKey = msg.MsgKey
		gotParam = msg.MsgParam
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	a := New()
	err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{
		ChatExtID: "cid",
		Text:      "Delete temp files?",
		Buttons:   []imbot.Button{{Label: "允许", Value: "permission:approve:9"}, {Label: "拒绝", Value: "permission:deny:9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMsgKey != "sampleActionCard" {
		t.Errorf("msgKey=%q, want sampleActionCard", gotMsgKey)
	}
	// The permission payloads must appear as readable fallback lines so the
	// prompt is visible even though card buttons can't carry them back.
	if !strings.Contains(gotParam, "permission:approve:9") || !strings.Contains(gotParam, "permission:deny:9") {
		t.Errorf("action card missing callback values in text fallback: %s", gotParam)
	}
}

func TestPush_ErrorOnMissingCred(t *testing.T) {
	a := New()
	err := a.Push(context.Background(), imbot.Credential{Config: map[string]any{}}, imbot.OutboundMessage{ChatExtID: "cid"})
	if err == nil {
		t.Fatal("expected error on missing client_id/client_secret")
	}
}

func TestPush_ErrorOnAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			_, _ = io.WriteString(w, `{"accessToken":"t","expireIn":7200}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"Forbidden.AccessDenied","message":"no permission"}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "cid", Text: "x"}); err == nil {
		t.Fatal("expected error when API returns an error code")
	}
}

func TestVerifyCredential_MintsToken(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			hit.Store(true)
			_, _ = io.WriteString(w, `{"accessToken":"t","expireIn":7200}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	a := New()
	if err := a.VerifyCredential(context.Background(), testCred(srv.URL)); err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
	if !hit.Load() {
		t.Fatal("VerifyCredential did not mint a token")
	}
}

func TestVerifyCredential_ErrorOnBadCred(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"invalidParameter","message":"bad appKey"}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.VerifyCredential(context.Background(), testCred(srv.URL)); err == nil {
		t.Fatal("expected error for invalid credentials")
	}
}

// --- Connect (Stream WS) ---

// wsUpgrader upgrades the mock server's ws route.
var wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// newStreamMockServer stands up an httptest server that (a) serves
// /v1.0/gateway/connections/open returning an endpoint pointing back at its own
// ws:// URL plus a ticket, and (b) upgrades /ws to a WebSocket. onConn is called
// with the upgraded server-side connection so a test can drive frames.
func newStreamMockServer(t *testing.T, onConn func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/gateway/connections/open", func(w http.ResponseWriter, r *http.Request) {
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
		_, _ = io.WriteString(w, `{"endpoint":"`+wsURL+`","ticket":"tkt-123"}`)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		onConn(conn)
	})
	srv = httptest.NewServer(mux)
	return srv
}

func TestConnect_DeliversInboundAndAcks(t *testing.T) {
	acked := make(chan streamFrame, 4)
	srv := newStreamMockServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		// Send a bot-message CALLBACK frame; its `data` is a JSON string.
		data := `{"msgtype":"text","text":{"content":"帮我做张表"},"conversationId":"cid_42","conversationType":"2","senderStaffId":"staff_7","msgId":"m_500","robotCode":"robot_x"}`
		frame := streamFrame{
			SpecVersion: "1.0",
			Type:        "CALLBACK",
			Headers:     map[string]string{"topic": botMessageTopic, "messageId": "wire_1", "contentType": "application/json"},
			Data:        data,
		}
		b, _ := json.Marshal(frame)
		_ = conn.WriteMessage(websocket.TextMessage, b)
		// Read whatever ack the adapter sends back.
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var f streamFrame
			if json.Unmarshal(msg, &f) == nil {
				acked <- f
			}
		}
	})
	defer srv.Close()

	a := New()
	var got []imbot.InboundEvent
	var gmu sync.Mutex
	handler := func(_ context.Context, ev imbot.InboundEvent) {
		gmu.Lock()
		got = append(got, ev)
		gmu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testCred(srv.URL), handler) }()

	// Wait for the ack (proves the frame was processed).
	select {
	case f := <-acked:
		if f.Headers["messageId"] != "wire_1" {
			t.Errorf("ack messageId=%q, want wire_1", f.Headers["messageId"])
		}
		if !strings.Contains(f.Data, "SUCCESS") {
			t.Errorf("ack data missing SUCCESS: %s", f.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter never acked the callback")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Connect returned error on cancel: %v", err)
	}

	gmu.Lock()
	defer gmu.Unlock()
	if len(got) != 1 {
		t.Fatalf("handler events = %d, want 1: %+v", len(got), got)
	}
	if got[0].ChatExtID != "cid_42" || got[0].Text != "帮我做张表" || got[0].EventID != "m_500" {
		t.Errorf("normalized event wrong: %+v", got[0])
	}
	if got[0].Channel != imbot.ChannelDingTalk {
		t.Errorf("Channel=%q, want dingtalk", got[0].Channel)
	}
}

func TestConnect_PingIsPonged(t *testing.T) {
	pong := make(chan streamFrame, 1)
	srv := newStreamMockServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		ping := streamFrame{
			SpecVersion: "1.0",
			Type:        "SYSTEM",
			Headers:     map[string]string{"topic": "ping", "messageId": "ping_1", "contentType": "application/json"},
			Data:        `{"t":1}`,
		}
		b, _ := json.Marshal(ping)
		_ = conn.WriteMessage(websocket.TextMessage, b)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var f streamFrame
		if json.Unmarshal(msg, &f) == nil {
			pong <- f
		}
	})
	defer srv.Close()

	a := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Connect(ctx, testCred(srv.URL), func(context.Context, imbot.InboundEvent) {}) }()

	select {
	case f := <-pong:
		if f.Headers["messageId"] != "ping_1" {
			t.Errorf("pong messageId=%q, want ping_1 (echo)", f.Headers["messageId"])
		}
		if f.Data != `{"t":1}` {
			t.Errorf("pong data=%q, want the ping data echoed", f.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter never ponged the ping")
	}
}

func TestConnect_NilOnCancel(t *testing.T) {
	srv := newStreamMockServer(t, func(conn *websocket.Conn) {
		// Hold the socket open (block on read) until it is closed by cancel.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer srv.Close()

	a := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testCred(srv.URL), nil) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Connect on cancel = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connect did not return after cancel")
	}
}

func TestConnect_ErrorOnGatewayFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":"boom"}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.Connect(context.Background(), testCred(srv.URL), nil); err == nil {
		t.Fatal("expected error so the manager reconnects")
	}
	// Missing creds fail fast too.
	if err := a.Connect(context.Background(), imbot.Credential{Config: map[string]any{}}, nil); err == nil {
		t.Fatal("expected error on missing client_id/client_secret")
	}
}

// --- VerifyWebhook ---

func TestVerifyWebhook_NoSecretParsesBody(t *testing.T) {
	a := New()
	body := `{"msgtype":"text","text":{"content":"hello"},"conversationId":"c","msgId":"m1"}`
	r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(body))
	ev, err := a.VerifyWebhook(r, imbot.Credential{Config: map[string]any{}})
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.Text != "hello" || ev.ChatExtID != "c" || ev.Channel != imbot.ChannelDingTalk {
		t.Errorf("unexpected event: %+v", ev)
	}
}

func TestVerifyWebhook_SignaturePassAndFail(t *testing.T) {
	a := New()
	secret := "shh"
	body := `{"msgtype":"text","text":{"content":"hi"},"conversationId":"c","msgId":"m1"}`
	ts := "1720000000000"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	good := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	cred := imbot.Credential{Config: map[string]any{"webhook_secret": secret}}

	// Correct signature passes.
	r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(body))
	r.Header.Set("timestamp", ts)
	r.Header.Set("sign", good)
	if _, err := a.VerifyWebhook(r, cred); err != nil {
		t.Fatalf("expected valid signature to pass: %v", err)
	}

	// Wrong signature fails.
	r2 := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(body))
	r2.Header.Set("timestamp", ts)
	r2.Header.Set("sign", "wrong")
	if _, err := a.VerifyWebhook(r2, cred); err == nil {
		t.Fatal("expected signature mismatch error")
	}

	// Missing signature fails.
	r3 := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(body))
	if _, err := a.VerifyWebhook(r3, cred); err == nil {
		t.Fatal("expected missing timestamp/sign error")
	}
}

func TestVerifyWebhook_NonActionable(t *testing.T) {
	a := New()
	r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(`{"msgtype":"image","conversationId":"c"}`))
	if _, err := a.VerifyWebhook(r, imbot.Credential{Config: map[string]any{}}); err == nil {
		t.Fatal("expected error for non-actionable event")
	}
}

func TestChallenge_ReturnsFalse(t *testing.T) {
	a := New()
	r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(`{"encrypt":"x"}`))
	if _, isChallenge := a.Challenge(r); isChallenge {
		t.Error("Challenge should return false for the plaintext Stream default")
	}
}

// --- Reply / React / RemoveReaction / FetchResource ---

// Reply sends the task marker to the conversation decoded from messageExtID.
func TestReply_SendsToDecodedConversation(t *testing.T) {
	var gotConv, gotMsgKey, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			_, _ = io.WriteString(w, `{"accessToken":"t","expireIn":7200}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			OpenConversationID string `json:"openConversationId"`
			MsgKey             string `json:"msgKey"`
			MsgParam           string `json:"msgParam"`
		}
		_ = json.Unmarshal(body, &msg)
		gotConv, gotMsgKey = msg.OpenConversationID, msg.MsgKey
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(msg.MsgParam), &p)
		gotText = p.Text
		_, _ = io.WriteString(w, `{"processQueryKey":"k1"}`)
	}))
	defer srv.Close()

	a := New()
	ref := encodeMsgRef("m_1", "cid_reply")
	if err := a.Reply(context.Background(), testCred(srv.URL), ref, "#12 标题\n🐂 牛牛正在为您工作。"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if gotConv != "cid_reply" {
		t.Errorf("conversation=%q, want cid_reply (decoded from ref)", gotConv)
	}
	if gotMsgKey != "sampleMarkdown" {
		t.Errorf("msgKey=%q, want sampleMarkdown", gotMsgKey)
	}
	if !strings.Contains(gotText, "#12 标题") {
		t.Errorf("text=%q, want the task marker", gotText)
	}
}

func TestReply_NoConversationIsError(t *testing.T) {
	a := New()
	// A bare (separator-less) ref decodes to an empty conversation → cannot target.
	if err := a.Reply(context.Background(), testCred("http://unused"), "bare_msg_id", "x"); err == nil {
		t.Fatal("expected error when messageExtID carries no conversation")
	}
}

// React posts the transient receipt and returns its processQueryKey; the encoded
// conversation is the send target. RemoveReaction then recalls by that key.
func TestReactAndRemove_SendThenRecall(t *testing.T) {
	var sentConv, recallConv, recalledKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/accessToken"):
			_, _ = io.WriteString(w, `{"accessToken":"t","expireIn":7200}`)
		case strings.HasSuffix(r.URL.Path, "/robot/groupMessages/send"):
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				OpenConversationID string `json:"openConversationId"`
			}
			_ = json.Unmarshal(body, &msg)
			sentConv = msg.OpenConversationID
			_, _ = io.WriteString(w, `{"processQueryKey":"pqk_9"}`)
		case strings.HasSuffix(r.URL.Path, "/robot/groupMessages/recall"):
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				OpenConversationID string   `json:"openConversationId"`
				ProcessQueryKeys   []string `json:"processQueryKeys"`
			}
			_ = json.Unmarshal(body, &msg)
			recallConv = msg.OpenConversationID
			if len(msg.ProcessQueryKeys) == 1 {
				recalledKey = msg.ProcessQueryKeys[0]
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	ref := encodeMsgRef("m_2", "cid_react")

	key, err := a.React(context.Background(), cred, ref, imbot.ReactionProcessing)
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if key != "pqk_9" {
		t.Errorf("React returned key=%q, want pqk_9 (processQueryKey)", key)
	}
	if sentConv != "cid_react" {
		t.Errorf("receipt sent to conv=%q, want cid_react", sentConv)
	}

	if err := a.RemoveReaction(context.Background(), cred, ref, key); err != nil {
		t.Fatalf("RemoveReaction: %v", err)
	}
	if recallConv != "cid_react" || recalledKey != "pqk_9" {
		t.Errorf("recall conv=%q key=%q, want cid_react/pqk_9", recallConv, recalledKey)
	}
}

func TestReact_UnsupportedReactionNoOps(t *testing.T) {
	a := New()
	// A non-processing reaction is a silent no-op (no network call, no error).
	if id, err := a.React(context.Background(), testCred("http://unused"), encodeMsgRef("m", "c"), imbot.Reaction("bogus")); err != nil || id != "" {
		t.Fatalf("React(bogus) = (%q,%v), want (\"\",nil)", id, err)
	}
}

// FetchResource exchanges the downloadCode for a signed URL, then GETs the bytes.
func TestFetchResource_ResolvesThenDownloads(t *testing.T) {
	var gotCode, gotRobot string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/accessToken"):
			_, _ = io.WriteString(w, `{"accessToken":"t","expireIn":7200}`)
		case strings.HasSuffix(r.URL.Path, "/robot/messageFiles/download"):
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				DownloadCode string `json:"downloadCode"`
				RobotCode    string `json:"robotCode"`
			}
			_ = json.Unmarshal(body, &msg)
			gotCode, gotRobot = msg.DownloadCode, msg.RobotCode
			// Point the download URL back at this same server.
			_, _ = io.WriteString(w, `{"downloadUrl":"`+baseOf(r)+`/file/blob"}`)
		case strings.HasSuffix(r.URL.Path, "/file/blob"):
			_, _ = w.Write([]byte("PNGDATA"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := New()
	data, err := a.FetchResource(context.Background(), testCred(srv.URL), encodeMsgRef("m", "c"),
		imbot.InboundAttachment{Kind: "image", ResourceID: "dc_x"})
	if err != nil {
		t.Fatalf("FetchResource: %v", err)
	}
	if string(data) != "PNGDATA" {
		t.Errorf("data=%q, want PNGDATA", data)
	}
	if gotCode != "dc_x" || gotRobot != "robot_x" {
		t.Errorf("download req code=%q robot=%q, want dc_x/robot_x", gotCode, gotRobot)
	}
}

func TestFetchResource_MissingCodeIsError(t *testing.T) {
	a := New()
	if _, err := a.FetchResource(context.Background(), testCred("http://unused"), "", imbot.InboundAttachment{Kind: "file"}); err == nil {
		t.Fatal("expected error when the download code is empty")
	}
}

// baseOf reconstructs the mock server's base URL from an inbound request so the
// download handler can point downloadUrl back at itself.
func baseOf(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
