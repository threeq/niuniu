package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

const sampleTelegramUpdate = `{"update_id":100,"message":{"message_id":5,"from":{"id":77},"chat":{"id":4242,"type":"private"},"text":"hi"}}`

// Telegram's setWebhook lets the bot register a secret_token; Telegram then
// echoes it in the X-Telegram-Bot-Api-Secret-Token header on every webhook POST.
// A request without the exact per-channel token must be rejected — otherwise a
// forger who reaches the public endpoint can drive the agent or forge approvals.
func TestVerifyWebhook_RejectsForgedSecretHeader(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(sampleTelegramUpdate))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "attacker-guess")
	cred := imbot.Credential{Channel: imbot.ChannelTelegram, Config: map[string]any{"webhook_secret": "real-secret"}}
	if _, err := a.VerifyWebhook(r, cred); !errors.Is(err, imbot.ErrWebhookUnauthorized) {
		t.Fatalf("forged header: want ErrWebhookUnauthorized, got %v", err)
	}
}

// Missing header is just the empty-string case of a forged/absent token.
func TestVerifyWebhook_RejectsMissingSecretHeader(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(sampleTelegramUpdate))
	cred := imbot.Credential{Channel: imbot.ChannelTelegram, Config: map[string]any{"webhook_secret": "real-secret"}}
	if _, err := a.VerifyWebhook(r, cred); !errors.Is(err, imbot.ErrWebhookUnauthorized) {
		t.Fatalf("absent header: want ErrWebhookUnauthorized, got %v", err)
	}
}

// A webhook-mode channel with no configured secret must fail closed.
func TestVerifyWebhook_FailsClosedWhenSecretUnset(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(sampleTelegramUpdate))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "whatever")
	cred := imbot.Credential{Channel: imbot.ChannelTelegram, Config: map[string]any{"webhook_secret": ""}}
	if _, err := a.VerifyWebhook(r, cred); !errors.Is(err, imbot.ErrWebhookUnauthorized) {
		t.Fatalf("unset secret: want ErrWebhookUnauthorized (fail closed), got %v", err)
	}
}

func TestVerifyWebhook_AcceptsMatchingSecretHeader(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(sampleTelegramUpdate))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "real-secret")
	cred := imbot.Credential{Channel: imbot.ChannelTelegram, Config: map[string]any{"webhook_secret": "real-secret"}}
	ev, err := a.VerifyWebhook(r, cred)
	if err != nil || ev.ChatExtID != "4242" {
		t.Fatalf("matching header: ev=%+v err=%v", ev, err)
	}
}

func testCred(baseURL string) imbot.Credential {
	return imbot.Credential{Channel: imbot.ChannelTelegram, Config: map[string]any{
		"bot_token": "123:ABC",
		"base_url":  baseURL,
	}}
}

func TestPush_SendsTextToChat(t *testing.T) {
	var gotMethod, gotToken string
	var gotChatID any
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path is /bot<token>/<method>.
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		gotToken = strings.TrimPrefix(parts[0], "bot")
		gotMethod = parts[1]
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ChatID any    `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(body, &msg)
		gotChatID = msg.ChatID
		gotText = msg.Text
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	a := New()
	err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "4242", Text: "done"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotToken != "123:ABC" {
		t.Errorf("token=%q, want 123:ABC", gotToken)
	}
	if gotMethod != "sendMessage" {
		t.Errorf("method=%q, want sendMessage", gotMethod)
	}
	// A numeric chat id must be sent as a JSON number, not a string.
	if f, ok := gotChatID.(float64); !ok || int64(f) != 4242 {
		t.Errorf("chat_id=%#v, want numeric 4242", gotChatID)
	}
	if gotText != "done" {
		t.Errorf("text=%q, want done", gotText)
	}
}

// Push renders markdown to Telegram HTML (parse_mode=HTML).
func TestPush_RendersHTML(t *testing.T) {
	var gotMode, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		_ = json.Unmarshal(body, &msg)
		gotText, gotMode = msg.Text, msg.ParseMode
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	a := New()
	if err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "4242", Text: "**做完了** 见 `log`"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotMode != "HTML" {
		t.Errorf("parse_mode=%q, want HTML", gotMode)
	}
	if gotText != "<b>做完了</b> 见 <code>log</code>" {
		t.Errorf("text=%q, want rendered HTML", gotText)
	}
}

// A parse rejection on the HTML attempt retries once as plain text so the message
// is never lost to a formatting error.
func TestPush_FallsBackToPlainText(t *testing.T) {
	var calls int
	var lastMode, lastText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		_ = json.Unmarshal(body, &msg)
		calls++
		lastMode, lastText = msg.ParseMode, msg.Text
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"can't parse entities"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	a := New()
	if err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "4242", Text: "raw **x**"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2 (HTML then plain)", calls)
	}
	if lastMode != "" {
		t.Errorf("fallback parse_mode=%q, want empty (plain text)", lastMode)
	}
	if lastText != "raw **x**" {
		t.Errorf("fallback text=%q, want the raw unrendered text", lastText)
	}
}

// Reply posts a quoted reply (reply_parameters.message_id) to the decoded chat.
func TestReply_QuotesMessage(t *testing.T) {
	var gotChatID, gotReplyTo any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ChatID          any `json:"chat_id"`
			ReplyParameters struct {
				MessageID any `json:"message_id"`
			} `json:"reply_parameters"`
		}
		_ = json.Unmarshal(body, &msg)
		gotChatID, gotReplyTo = msg.ChatID, msg.ReplyParameters.MessageID
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	a := New()
	ref := encodeMsgRef(4242, 5)
	if err := a.Reply(context.Background(), testCred(srv.URL), ref, "#12 标题"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if f, ok := gotChatID.(float64); !ok || int64(f) != 4242 {
		t.Errorf("chat_id=%#v, want numeric 4242", gotChatID)
	}
	if f, ok := gotReplyTo.(float64); !ok || int64(f) != 5 {
		t.Errorf("reply_parameters.message_id=%#v, want 5", gotReplyTo)
	}
}

func TestReply_MissingMessageIDIsError(t *testing.T) {
	a := New()
	// A bare (separator-less) ref decodes to an empty message id → cannot target.
	if err := a.Reply(context.Background(), testCred("http://unused"), "4242", "x"); err == nil {
		t.Fatal("expected error when messageExtID carries no message id")
	}
}

// React sets the 👀 reaction and returns it as the id; RemoveReaction clears it.
func TestReactAndRemove_SetsThenClearsReaction(t *testing.T) {
	var setEmoji string
	var cleared bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			Reaction []struct {
				Emoji string `json:"emoji"`
			} `json:"reaction"`
		}
		_ = json.Unmarshal(body, &msg)
		if len(msg.Reaction) == 0 {
			cleared = true
		} else {
			setEmoji = msg.Reaction[0].Emoji
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer srv.Close()

	a := New()
	cred := testCred(srv.URL)
	ref := encodeMsgRef(4242, 5)

	id, err := a.React(context.Background(), cred, ref, imbot.ReactionProcessing)
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if id != processingEmoji || setEmoji != processingEmoji {
		t.Errorf("React id=%q setEmoji=%q, want %q", id, setEmoji, processingEmoji)
	}
	if err := a.RemoveReaction(context.Background(), cred, ref, id); err != nil {
		t.Fatalf("RemoveReaction: %v", err)
	}
	if !cleared {
		t.Error("RemoveReaction did not send an empty reaction list")
	}
}

func TestReact_UnsupportedReactionNoOps(t *testing.T) {
	a := New()
	if id, err := a.React(context.Background(), testCred("http://unused"), encodeMsgRef(1, 2), imbot.Reaction("bogus")); err != nil || id != "" {
		t.Fatalf("React(bogus) = (%q,%v), want (\"\",nil)", id, err)
	}
}

// FetchResource resolves the file_id via getFile then downloads from the file URL.
func TestFetchResource_GetFileThenDownload(t *testing.T) {
	var gotFileID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				FileID string `json:"file_id"`
			}
			_ = json.Unmarshal(body, &msg)
			gotFileID = msg.FileID
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"photos/f.jpg"}}`)
		case strings.Contains(r.URL.Path, "/file/bot123:ABC/photos/f.jpg"):
			_, _ = w.Write([]byte("JPEGDATA"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := New()
	data, err := a.FetchResource(context.Background(), testCred(srv.URL), encodeMsgRef(1, 2),
		imbot.InboundAttachment{Kind: "image", ResourceID: "file_xyz"})
	if err != nil {
		t.Fatalf("FetchResource: %v", err)
	}
	if string(data) != "JPEGDATA" {
		t.Errorf("data=%q, want JPEGDATA", data)
	}
	if gotFileID != "file_xyz" {
		t.Errorf("getFile file_id=%q, want file_xyz", gotFileID)
	}
}

func TestPush_ButtonsSendInlineKeyboard(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	a := New()
	err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{
		ChatExtID: "5",
		Text:      "Delete temp files?",
		Buttons:   []imbot.Button{{Label: "允许", Value: "permission:approve:9"}, {Label: "拒绝", Value: "permission:deny:9"}},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !strings.Contains(gotBody, "inline_keyboard") {
		t.Fatalf("expected inline_keyboard, got %s", gotBody)
	}
	if !strings.Contains(gotBody, "permission:approve:9") || !strings.Contains(gotBody, "permission:deny:9") {
		t.Errorf("callback_data missing: %s", gotBody)
	}
}

func TestPush_ThreadTargetsTopic(t *testing.T) {
	var gotThread any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			MessageThreadID any `json:"message_thread_id"`
		}
		_ = json.Unmarshal(body, &msg)
		gotThread = msg.MessageThreadID
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()

	a := New()
	if err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "5", ThreadExtID: "77", Text: "hi"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if f, ok := gotThread.(float64); !ok || int64(f) != 77 {
		t.Errorf("message_thread_id=%#v, want 77", gotThread)
	}
}

func TestPush_ErrorOnMissingToken(t *testing.T) {
	a := New()
	err := a.Push(context.Background(), imbot.Credential{Config: map[string]any{}}, imbot.OutboundMessage{ChatExtID: "1"})
	if err == nil {
		t.Fatal("expected error on missing bot_token")
	}
}

func TestPush_ErrorOnAPINotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.Push(context.Background(), testCred(srv.URL), imbot.OutboundMessage{ChatExtID: "1", Text: "x"}); err == nil {
		t.Fatal("expected error when API returns ok=false")
	}
}

func TestVerifyCredential_CallsGetMe(t *testing.T) {
	var hitGetMe atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			hitGetMe.Store(true)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true}}`)
	}))
	defer srv.Close()
	a := New()
	if err := a.VerifyCredential(context.Background(), testCred(srv.URL)); err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
	if !hitGetMe.Load() {
		t.Fatal("VerifyCredential did not call getMe")
	}
}

// TestConnect_LongPollDeliversAndAdvancesOffset verifies the getUpdates loop
// delivers each update to the handler and acknowledges it by advancing the
// offset on the next poll (the long-poll idempotency mechanism).
func TestConnect_LongPollDeliversAndAdvancesOffset(t *testing.T) {
	var mu sync.Mutex
	var offsets []int64 // offset seen on each getUpdates call
	firstServed := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Offset int64 `json:"offset"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		offsets = append(offsets, req.Offset)
		callNo := len(offsets)
		mu.Unlock()

		if callNo == 1 {
			// First poll returns one message update (update_id 100).
			_, _ = io.WriteString(w, `{"ok":true,"result":[
				{"update_id":100,"message":{"message_id":1,"from":{"id":7},"chat":{"id":4242,"type":"private"},"text":"帮我做张表"}}
			]}`)
			once.Do(func() { close(firstServed) })
			return
		}
		// Later polls return nothing (block-then-empty in real Telegram).
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
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

	<-firstServed
	// Give the loop a moment to issue the second (offset-advanced) poll.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(offsets)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second poll never issued")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Connect returned error on cancel: %v", err)
	}

	gmu.Lock()
	defer gmu.Unlock()
	if len(got) != 1 || got[0].Text != "帮我做张表" || got[0].ChatExtID != "4242" {
		t.Fatalf("handler events = %+v, want one message from chat 4242", got)
	}
	if got[0].EventID != "u100" {
		t.Errorf("EventID=%q, want u100 (update_id based, for idempotency)", got[0].EventID)
	}
	mu.Lock()
	defer mu.Unlock()
	if offsets[0] != 0 {
		t.Errorf("first poll offset=%d, want 0", offsets[0])
	}
	if offsets[1] != 101 {
		t.Errorf("second poll offset=%d, want 101 (update_id+1)", offsets[1])
	}
}

// TestConnect_ReturnsErrorForReconnect asserts that a failed poll surfaces as an
// error so the ConnectorManager reconnects with backoff, while a cancelled ctx
// returns nil (a clean shutdown, not a failure).
func TestConnect_ReturnsErrorForReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"ok":false,"description":"boom"}`)
	}))
	defer srv.Close()

	a := New()
	err := a.Connect(context.Background(), testCred(srv.URL), func(context.Context, imbot.InboundEvent) {})
	if err == nil {
		t.Fatal("expected Connect to return an error so the manager reconnects")
	}

	// Missing token fails fast (also an error the manager will surface/log).
	if err := a.Connect(context.Background(), imbot.Credential{Config: map[string]any{}}, nil); err == nil {
		t.Fatal("expected error on missing bot_token")
	}
}

func TestConnect_NilOnCancel(t *testing.T) {
	// A server that always returns empty keeps the loop polling until cancel.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
	defer srv.Close()

	a := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testCred(srv.URL), nil) }()
	time.Sleep(20 * time.Millisecond)
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
