package wechat

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// --- crypto ---

func encryptAESECB(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	padded := pkcs7Pad(plaintext, block.BlockSize())
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += block.BlockSize() {
		block.Encrypt(out[i:i+block.BlockSize()], padded[i:i+block.BlockSize()])
	}
	return out
}

func TestAESECBRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes
	plain := []byte("hello wechat clawbot media payload that spans blocks")
	ct := encryptAESECB(t, plain, key)
	got, err := decryptAESECB(ct, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestDecryptAESECBRejectsBadInput(t *testing.T) {
	key := []byte("0123456789abcdef")
	if _, err := decryptAESECB([]byte("not-block-aligned"), key); err == nil {
		t.Fatal("expected error on non-block-aligned ciphertext")
	}
	// Wrong key -> invalid padding.
	ct := encryptAESECB(t, []byte("secret"), key)
	if _, err := decryptAESECB(ct, []byte("ffffffffffffffff")); err == nil {
		t.Fatal("expected padding error on wrong key")
	}
}

func TestParseAESKey(t *testing.T) {
	raw := []byte("0123456789abcdef")
	// raw 16 bytes -> base64
	if got, err := parseAESKey(base64.StdEncoding.EncodeToString(raw)); err != nil || string(got) != string(raw) {
		t.Fatalf("raw16: got %q err %v", got, err)
	}
	// base64 of 32-char ascii hex
	hexStr := hex.EncodeToString(raw) // 32 chars
	if got, err := parseAESKey(base64.StdEncoding.EncodeToString([]byte(hexStr))); err != nil || string(got) != string(raw) {
		t.Fatalf("hex32: got %q err %v", got, err)
	}
	if _, err := parseAESKey("!!!notbase64"); err == nil {
		t.Fatal("expected error on non-base64")
	}
}

// --- inbound parsing ---

func TestParseInboundText(t *testing.T) {
	m := &weixinMessage{
		FromUserID:   "user-1",
		MessageID:    42,
		MessageType:  msgTypeUser,
		ContextToken: "ctx-abc",
		ItemList:     []*messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "hi 牛牛"}}},
	}
	ev, tok, ok := parseInbound(m)
	if !ok {
		t.Fatal("expected ok")
	}
	if ev.Channel != imbot.ChannelWechat || ev.ChatExtID != "user-1" || ev.ActorExtID != "user-1" {
		t.Fatalf("unexpected ev: %+v", ev)
	}
	if ev.Text != "hi 牛牛" || ev.Kind != "message" || ev.EventID != "m42" {
		t.Fatalf("unexpected ev fields: %+v", ev)
	}
	if tok != "ctx-abc" {
		t.Fatalf("context token = %q", tok)
	}
}

func TestParseInboundSkipsOwnAndEmpty(t *testing.T) {
	if _, _, ok := parseInbound(&weixinMessage{FromUserID: "u", MessageType: msgTypeBot, ItemList: []*messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "echo"}}}}); ok {
		t.Fatal("should skip bot-authored message")
	}
	if _, _, ok := parseInbound(&weixinMessage{FromUserID: "u", MessageType: msgTypeUser}); ok {
		t.Fatal("should skip empty message")
	}
	if _, _, ok := parseInbound(&weixinMessage{MessageType: msgTypeUser, ItemList: []*messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "x"}}}}); ok {
		t.Fatal("should skip message with no from_user_id")
	}
}

func TestParseInboundVoiceTranscript(t *testing.T) {
	m := &weixinMessage{
		FromUserID:  "u",
		Seq:         7,
		MessageType: msgTypeUser,
		ItemList:    []*messageItem{{Type: itemTypeVoice, VoiceItem: &voiceItem{Text: "转文字内容", Media: &cdnMedia{FullURL: "https://cdn/x", AESKey: "k"}}}},
	}
	ev, _, ok := parseInbound(m)
	if !ok || ev.Text != "转文字内容" {
		t.Fatalf("voice transcript: ok=%v text=%q", ok, ev.Text)
	}
	if ev.EventID != "s7" {
		t.Fatalf("event id fallback to seq: %q", ev.EventID)
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "audio" {
		t.Fatalf("expected 1 audio attachment: %+v", ev.Attachments)
	}
}

func TestParseInboundQuotedRef(t *testing.T) {
	m := &weixinMessage{
		FromUserID:  "u",
		MessageID:   1,
		MessageType: msgTypeUser,
		ItemList: []*messageItem{{
			Type:     itemTypeText,
			TextItem: &textItem{Text: "回复"},
			RefMsg:   &refMessage{Title: "原标题"},
		}},
	}
	ev, _, ok := parseInbound(m)
	if !ok || !strings.Contains(ev.Text, "原标题") || !strings.Contains(ev.Text, "回复") {
		t.Fatalf("quoted ref: %q", ev.Text)
	}
}

func TestMediaAttachmentImageHexKey(t *testing.T) {
	rawKey := []byte("0123456789abcdef")
	m := &weixinMessage{
		FromUserID:  "u",
		MessageID:   1,
		MessageType: msgTypeUser,
		ItemList: []*messageItem{{
			Type: itemTypeImage,
			ImageItem: &imageItem{
				AESKey: hex.EncodeToString(rawKey), // hex form preferred
				Media:  &cdnMedia{EncryptQueryParam: "qp", AESKey: "ignored"},
			},
		}},
	}
	ev, _, ok := parseInbound(m)
	if !ok || len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "image" {
		t.Fatalf("image attachment: %+v", ev.Attachments)
	}
	ref, ok := decodeMediaRef(ev.Attachments[0].ResourceID)
	if !ok {
		t.Fatal("decode media ref")
	}
	// image_item.aeskey (hex) should have been normalized to base64 of raw key.
	if ref.AESKeyB64 != base64.StdEncoding.EncodeToString(rawKey) {
		t.Fatalf("image key normalization: %q", ref.AESKeyB64)
	}
}

// --- adapter HTTP paths ---

func testCred(baseURL string) imbot.Credential {
	return imbot.Credential{Channel: imbot.ChannelWechat, Config: map[string]any{
		"token":      "tok-123",
		"base_url":   baseURL,
		"account_id": "bot-1",
		"user_id":    "owner-1",
	}}
}

func TestPushEchoesContextToken(t *testing.T) {
	var gotBody sendMessageReq
	var gotAuth, gotUin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUin = r.Header.Get("X-WECHAT-UIN")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"ret":0}`)
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	// Seed a context token as if an inbound message had arrived.
	a.rememberContextToken("bot-1", "owner-1", "ctx-777")

	if err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "owner-1", Text: "hello"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotUin == "" {
		t.Fatal("missing X-WECHAT-UIN")
	}
	if gotBody.Msg == nil || gotBody.Msg.ToUserID != "owner-1" || gotBody.Msg.ContextToken != "ctx-777" {
		t.Fatalf("send body = %+v", gotBody.Msg)
	}
	if gotBody.Msg.MessageType != msgTypeBot || len(gotBody.Msg.ItemList) != 1 || gotBody.Msg.ItemList[0].TextItem.Text != "hello" {
		t.Fatalf("send item = %+v", gotBody.Msg)
	}
}

func TestPushEmptyTextIsNoop(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"ret":0}`)
	}))
	defer srv.Close()
	if err := New().Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "u", Text: "  "}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if called {
		t.Fatal("empty text should not hit the server")
	}
}

func TestConnectDeliversAndRemembersToken(t *testing.T) {
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "getupdates") {
			http.Error(w, "unexpected", 400)
			return
		}
		resp := getUpdatesResp{GetUpdatesBuf: "buf-2"}
		once.Do(func() {
			resp.Msgs = []*weixinMessage{{
				FromUserID:   "owner-1",
				MessageID:    9,
				MessageType:  msgTypeUser,
				ContextToken: "ctx-conn",
				ItemList:     []*messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "drive"}}},
			}}
		})
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan imbot.InboundEvent, 1)
	go func() {
		_ = a.Connect(ctx, cred, func(_ context.Context, ev imbot.InboundEvent) {
			select {
			case got <- ev:
			default:
			}
		})
	}()

	select {
	case ev := <-got:
		if ev.Text != "drive" || ev.ChatExtID != "owner-1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbound event")
	}
	cancel()
	if tok := a.contextToken("bot-1", "owner-1"); tok != "ctx-conn" {
		t.Fatalf("context token not remembered: %q", tok)
	}
}

func TestConnectSessionExpiredReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(getUpdatesResp{Errcode: -14, Errmsg: "session timeout"})
	}))
	defer srv.Close()
	err := New().Connect(context.Background(), testCred(srv.URL), nil)
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("expected session-expired error, got %v", err)
	}
}

func TestFetchResourceDecrypts(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("decrypted image bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAESECB(t, plain, key))
	}))
	defer srv.Close()

	ref := mediaRef{FullURL: srv.URL + "/download", AESKeyB64: base64.StdEncoding.EncodeToString(key)}
	att := imbot.InboundAttachment{Kind: "image", ResourceID: encodeMediaRef(ref)}
	got, err := New().FetchResource(context.Background(), testCred(srv.URL), "", att)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted = %q", got)
	}
}

func TestReactStartsTypingHeartbeat(t *testing.T) {
	var mu sync.Mutex
	var gotConfig, gotTyping, sawTypingOn, sentMessage bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch {
		case strings.HasSuffix(r.URL.Path, "sendmessage"):
			sentMessage = true // React must NOT send a message now — only a typing heartbeat
		case strings.HasSuffix(r.URL.Path, "getconfig"):
			gotConfig = true
		case strings.HasSuffix(r.URL.Path, "sendtyping"):
			gotTyping = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if s, _ := body["status"].(float64); int(s) == typingOn {
				sawTypingOn = true // a heartbeat pulse (vs the later cancel on RemoveReaction)
			}
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ret":0,"typing_ticket":"tick-1"}`)
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	id, err := a.React(context.Background(), cred, "owner-1", imbot.ReactionProcessing)
	if err != nil {
		t.Fatalf("react: %v", err)
	}
	if id != string(imbot.ReactionProcessing) {
		t.Fatalf("react id=%q", id)
	}
	// The heartbeat runs in a goroutine; wait for its first typing send.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := gotTyping
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stop the heartbeat so its goroutine does not outlive the test server.
	_ = a.RemoveReaction(context.Background(), cred, "owner-1", "")

	mu.Lock()
	defer mu.Unlock()
	if !gotConfig || !gotTyping {
		t.Fatalf("typing heartbeat not sent: config=%v typing=%v", gotConfig, gotTyping)
	}
	if !sawTypingOn {
		t.Fatal("heartbeat never sent typing=on")
	}
	if sentMessage {
		t.Fatal("React must not send a message (typing heartbeat only)")
	}
}

// React starts a typing heartbeat; Push must stop it (so no "对方正在输入" lingers
// after the reply) and send the reply as a FINISH message.
func TestReactHeartbeatStoppedByPush(t *testing.T) {
	var mu sync.Mutex
	var finishState float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "sendmessage") {
			var body sendMessageReq
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Msg != nil {
				mu.Lock()
				finishState = float64(body.Msg.MessageState)
				mu.Unlock()
			}
		}
		_, _ = io.WriteString(w, `{"ret":0,"typing_ticket":"t"}`)
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	if _, err := a.React(context.Background(), cred, "owner-1", imbot.ReactionProcessing); err != nil {
		t.Fatalf("react: %v", err)
	}
	// Heartbeat should be registered right after React.
	a.mu.Lock()
	_, running := a.typingHB[ctxKey("bot-1", "owner-1")]
	a.mu.Unlock()
	if !running {
		t.Fatal("React should have registered a typing heartbeat")
	}

	if err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "owner-1", Text: "完整回复内容"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	mu.Lock()
	gotFinish := int(finishState) == msgStateFinish
	mu.Unlock()
	if !gotFinish {
		t.Fatalf("reply not sent as FINISH: state=%v", finishState)
	}
	// Push must have stopped the heartbeat.
	a.mu.Lock()
	_, stillRunning := a.typingHB[ctxKey("bot-1", "owner-1")]
	a.mu.Unlock()
	if stillRunning {
		t.Fatal("Push should have stopped the typing heartbeat")
	}
}

// --- login ---

func TestCredentialFromStatus(t *testing.T) {
	c := CredentialFromStatus(QRStatus{Status: "confirmed", BotToken: "bt", IlinkBotID: "bid", IlinkUserID: "uid"})
	if c["token"] != "bt" || c["account_id"] != "bid" || c["user_id"] != "uid" || c["base_url"] != loginBaseURL {
		t.Fatalf("credential = %+v", c)
	}
	c2 := CredentialFromStatus(QRStatus{BaseURL: "https://idc.example"})
	if c2["base_url"] != "https://idc.example" {
		t.Fatalf("base_url override = %v", c2["base_url"])
	}
}

// verify the adapter satisfies the optional capability interfaces it advertises.
var (
	_ imbot.ChannelAdapter         = (*Adapter)(nil)
	_ imbot.CredentialVerifier     = (*Adapter)(nil)
	_ imbot.MessageReactor         = (*Adapter)(nil)
	_ imbot.MessageResourceFetcher = (*Adapter)(nil)
)
