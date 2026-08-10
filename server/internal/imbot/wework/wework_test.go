package wework

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// TestVerifyWebhook_ReceiveIDMismatchUnauthorized asserts a payload that decrypts
// to a different corp than the credential is rejected as unauthorized (so the
// service maps it to HTTP 401), not silently 200-acked.
func TestVerifyWebhook_ReceiveIDMismatchUnauthorized(t *testing.T) {
	a := New()
	cred := testCred("") // corp_id = ww_corp
	key, _ := aesKeyFromEncoding(testAESKey)
	plain := []byte(`<xml><ToUserName>other_corp</ToUserName><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>hi</Content><MsgId>9</MsgId></xml>`)
	encrypt, err := encryptMsg(key, plain, "other_corp") // != ww_corp
	if err != nil {
		t.Fatalf("encryptMsg: %v", err)
	}
	ts, nonce := "1700000000", "n"
	sig := msgSignature("verifyToken", ts, nonce, encrypt)
	body := `<xml><Encrypt><![CDATA[` + encrypt + `]]></Encrypt></xml>`
	u := "https://example.com/webhook?msg_signature=" + sig + "&timestamp=" + ts + "&nonce=" + nonce
	r := httptest.NewRequest(http.MethodPost, u, strings.NewReader(body))
	if _, err := a.VerifyWebhook(r, cred); !errors.Is(err, imbot.ErrWebhookUnauthorized) {
		t.Fatalf("receiveid mismatch err=%v, want ErrWebhookUnauthorized", err)
	}
}

func testCred(baseURL string) imbot.Credential {
	return imbot.Credential{Channel: imbot.ChannelWework, Config: map[string]any{
		"corp_id":  "ww_corp",
		"agent_id": "1000002",
		"secret":   "app-secret",
		"token":    "verifyToken",
		"aes_key":  testAESKey,
		"base_url": baseURL,
	}}
}

func TestType(t *testing.T) {
	if New().Type() != imbot.ChannelWework {
		t.Fatalf("Type = %q, want wework", New().Type())
	}
}

// TestPush_SendsTextAndUsesToken verifies Push fetches an access_token then POSTs
// a text message carrying touser/agentid/content.
func TestPush_SendsTextAndUsesToken(t *testing.T) {
	var gotToken string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/cgi-bin/gettoken"):
			_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok","access_token":"TOK","expires_in":7200}`)
		case strings.HasPrefix(r.URL.Path, "/cgi-bin/message/send"):
			gotToken = r.URL.Query().Get("access_token")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := New()
	err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "lucy", Text: "done"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotToken != "TOK" {
		t.Errorf("access_token = %q, want TOK", gotToken)
	}
	if gotBody["touser"] != "lucy" {
		t.Errorf("touser = %v, want lucy", gotBody["touser"])
	}
	// agent_id must be sent as a JSON number.
	if f, ok := gotBody["agentid"].(float64); !ok || int(f) != 1000002 {
		t.Errorf("agentid = %#v, want numeric 1000002", gotBody["agentid"])
	}
	text, _ := gotBody["text"].(map[string]any)
	if text["content"] != "done" {
		t.Errorf("content = %v, want done", text["content"])
	}
}

// TestPush_ButtonsAppendedAsText asserts the honest text fallback for buttons.
func TestPush_ButtonsAppendedAsText(t *testing.T) {
	var content string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cgi-bin/gettoken") {
			_, _ = io.WriteString(w, `{"errcode":0,"access_token":"TOK","expires_in":7200}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		_ = json.Unmarshal(body, &m)
		content = m.Text.Content
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	a := New()
	err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{
		ChatExtID: "lucy",
		Text:      "删除临时文件？",
		Buttons:   []imbot.Button{{Label: "允许", Value: "permission:approve:9"}, {Label: "拒绝", Value: "permission:deny:9"}},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !strings.Contains(content, "permission:approve:9") || !strings.Contains(content, "permission:deny:9") {
		t.Errorf("button payloads missing from content: %q", content)
	}
	if !strings.Contains(content, "删除临时文件？") {
		t.Errorf("original text missing from content: %q", content)
	}
}

func TestPush_ErrorOnMissingCreds(t *testing.T) {
	a := New()
	// Missing agent_id.
	err := a.Push(context.Background(), imbot.Credential{Config: map[string]any{"corp_id": "c", "secret": "s"}},
		imbot.OutboundMessage{ChatExtID: "lucy"})
	if err == nil {
		t.Fatal("expected error on missing agent_id")
	}
	// Empty chat id.
	if err := a.Push(context.Background(), imbot.Credential{Config: map[string]any{}}, imbot.OutboundMessage{}); err == nil {
		t.Fatal("expected error on empty chat id")
	}
}

func TestPush_ErrorOnAPINotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cgi-bin/gettoken") {
			_, _ = io.WriteString(w, `{"errcode":0,"access_token":"TOK","expires_in":7200}`)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":40001,"errmsg":"invalid credential"}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "lucy", Text: "x"}); err == nil {
		t.Fatal("expected error when API returns errcode!=0")
	}
}

func TestVerifyCredential_FetchesToken(t *testing.T) {
	var hitToken bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cgi-bin/gettoken") {
			hitToken = true
		}
		_, _ = io.WriteString(w, `{"errcode":0,"access_token":"TOK","expires_in":7200}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.VerifyCredential(context.Background(), testCred(srv.URL)); err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
	if !hitToken {
		t.Fatal("VerifyCredential did not call gettoken")
	}
}

func TestVerifyCredential_ErrorOnBadCreds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":40013,"errmsg":"invalid corpid"}`)
	}))
	defer srv.Close()
	if err := New().VerifyCredential(context.Background(), testCred(srv.URL)); err == nil {
		t.Fatal("expected error when gettoken reports errcode!=0")
	}
}

// TestTokenCache asserts the access_token is fetched once and reused.
func TestTokenCache(t *testing.T) {
	var tokenHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cgi-bin/gettoken") {
			tokenHits++
		}
		_, _ = io.WriteString(w, `{"errcode":0,"access_token":"TOK","expires_in":7200}`)
	}))
	defer srv.Close()
	a := New()
	cred := testCred(srv.URL)
	for i := 0; i < 3; i++ {
		if err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "lucy", Text: "x"}); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	if tokenHits != 1 {
		t.Errorf("gettoken called %d times, want 1 (cached)", tokenHits)
	}
}

// TestConnect_NoOpReturnsNilOnCancel asserts the stub blocks until ctx is
// cancelled and returns nil (WeCom has no outbound long connection).
func TestConnect_NoOpReturnsNilOnCancel(t *testing.T) {
	a := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testCred(""), nil) }()

	// It must still be blocking before cancel.
	select {
	case err := <-done:
		t.Fatalf("Connect returned before cancel: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

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

// TestVerifyWebhook_DecryptsAndParses builds a real encrypted callback (using the
// crypto helpers), signs it, and asserts VerifyWebhook verifies + decrypts +
// normalizes it into an InboundEvent.
func TestVerifyWebhook_DecryptsAndParses(t *testing.T) {
	a := New()
	cred := testCred("")
	key, _ := aesKeyFromEncoding(testAESKey)
	plain := []byte(`<xml><ToUserName>ww_corp</ToUserName><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>帮我做张表</Content><MsgId>77</MsgId></xml>`)
	encrypt, err := encryptMsg(key, plain, "ww_corp")
	if err != nil {
		t.Fatalf("encryptMsg: %v", err)
	}
	ts, nonce := "1700000000", "nonce-1"
	sig := msgSignature("verifyToken", ts, nonce, encrypt)

	body := `<xml><ToUserName>ww_corp</ToUserName><Encrypt><![CDATA[` + encrypt + `]]></Encrypt><AgentID>1000002</AgentID></xml>`
	u := "https://example.com/webhook?msg_signature=" + sig + "&timestamp=" + ts + "&nonce=" + nonce
	r := httptest.NewRequest(http.MethodPost, u, strings.NewReader(body))

	ev, err := a.VerifyWebhook(r, cred)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.Text != "帮我做张表" || ev.ChatExtID != "lucy" || ev.EventID != "77" {
		t.Fatalf("event = %+v, want text/chat/event lucy 77", ev)
	}
	if ev.Channel != imbot.ChannelWework {
		t.Errorf("Channel = %q, want wework", ev.Channel)
	}
}

func TestVerifyWebhook_RejectsBadSignature(t *testing.T) {
	a := New()
	cred := testCred("")
	key, _ := aesKeyFromEncoding(testAESKey)
	encrypt, _ := encryptMsg(key, []byte(`<xml><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>hi</Content><MsgId>1</MsgId></xml>`), "ww_corp")

	body := `<xml><Encrypt><![CDATA[` + encrypt + `]]></Encrypt></xml>`
	u := "https://example.com/webhook?msg_signature=deadbeef&timestamp=1&nonce=n"
	r := httptest.NewRequest(http.MethodPost, u, strings.NewReader(body))
	if _, err := a.VerifyWebhook(r, cred); err == nil {
		t.Fatal("expected error on msg_signature mismatch")
	}
}

// TestAnswerURLVerification round-trips the GET echostr handshake: encrypt a
// random echostr, sign it, and assert AnswerURLVerification returns the decrypted
// plaintext; a tampered signature must fail.
func TestAnswerURLVerification(t *testing.T) {
	a := New()
	cred := testCred("")
	key, _ := aesKeyFromEncoding(testAESKey)
	const echoPlain = "1616140317555161061"
	echostr, err := encryptMsg(key, []byte(echoPlain), "ww_corp")
	if err != nil {
		t.Fatalf("encryptMsg: %v", err)
	}
	ts, nonce := "1700000000", "nonce-1"
	sig := msgSignature("verifyToken", ts, nonce, echostr)

	got, err := a.AnswerURLVerification(cred, sig, ts, nonce, echostr)
	if err != nil {
		t.Fatalf("AnswerURLVerification: %v", err)
	}
	if string(got) != echoPlain {
		t.Errorf("echo = %q, want %q", got, echoPlain)
	}

	if _, err := a.AnswerURLVerification(cred, sig+"00", ts, nonce, echostr); err == nil {
		t.Error("expected error on tampered msg_signature")
	}
}

// TestChallenge_AlwaysNoOp documents the API-shape gap: Challenge cannot answer
// the WeCom GET verification (no cred param) and always returns (nil,false).
func TestChallenge_AlwaysNoOp(t *testing.T) {
	a := New()
	r := httptest.NewRequest(http.MethodGet, "https://example.com/webhook?echostr=abc", nil)
	if body, isCh := a.Challenge(r); isCh || body != nil {
		t.Fatalf("Challenge = (%q,%v), want (nil,false)", body, isCh)
	}
}
