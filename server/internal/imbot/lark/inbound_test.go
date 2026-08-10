package lark

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

const sampleMessageEvent = `{
  "schema": "2.0",
  "header": {"event_id": "evt-1", "event_type": "im.message.receive_v1", "token": "vtok"},
  "event": {
    "sender": {"sender_id": {"open_id": "ou_actor"}},
    "message": {
      "message_id": "om_1", "chat_id": "oc_chat", "thread_id": "omt_thread",
      "message_type": "text", "content": "{\"text\":\"帮我做个表\"}"
    }
  }
}`

const sampleCardEvent = `{
  "schema": "2.0",
  "header": {"event_id": "evt-2", "event_type": "card.action.trigger"},
  "event": {
    "operator": {"open_id": "ou_actor"},
    "action": {"value": {"cb": "permission:approve:42"}},
    "context": {"open_chat_id": "oc_chat", "open_message_id": "om_card"}
  }
}`

func TestParseLarkEventJSON_Message(t *testing.T) {
	ev, ok, err := parseLarkEventJSON([]byte(sampleMessageEvent))
	if err != nil || !ok {
		t.Fatalf("parse message: ok=%v err=%v", ok, err)
	}
	if ev.Kind != "message" || ev.ChatExtID != "oc_chat" || ev.ThreadExtID != "omt_thread" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.Text != "帮我做个表" {
		t.Fatalf("text = %q", ev.Text)
	}
	if ev.EventID != "evt-1" {
		t.Fatalf("event id = %q", ev.EventID)
	}
	if ev.ActorExtID != "ou_actor" {
		t.Fatalf("actor = %q", ev.ActorExtID)
	}
	if ev.MessageExtID != "om_1" {
		t.Fatalf("message ext id = %q, want om_1", ev.MessageExtID)
	}
}

func TestParseLarkEventJSON_StripsMentions(t *testing.T) {
	// In a group the bot only receives @-mention messages; Feishu inlines the
	// mention as "@_user_N" in the text. It must be stripped so a leading slash
	// command / #id is still recognized (regression: "@_user_1 /issues" was routed
	// as a new task instead of the /issues command).
	cases := []struct{ text, want string }{
		{`@_user_1 /issues`, `/issues`},
		{`@_user_1 #574 继续`, `#574 继续`},
		{`帮我 @_user_2 看看 @_user_1 这个`, `帮我 看看 这个`},
	}
	for _, c := range cases {
		inner, _ := json.Marshal(map[string]string{"text": c.text})
		content, _ := json.Marshal(string(inner))
		body := `{"schema":"2.0","header":{"event_id":"evt-m","event_type":"im.message.receive_v1"},
		  "event":{"sender":{"sender_id":{"open_id":"ou_a"}},
		  "message":{"message_id":"om_m","chat_id":"oc_c","message_type":"text",
		    "content":` + string(content) + `,
		    "mentions":[{"key":"@_user_1","name":"bot"},{"key":"@_user_2","name":"someone"}]}}}`
		ev, ok, err := parseLarkEventJSON([]byte(body))
		if err != nil || !ok {
			t.Fatalf("parse %q: ok=%v err=%v", c.text, ok, err)
		}
		if ev.Text != c.want {
			t.Fatalf("mention strip: %q -> %q, want %q", c.text, ev.Text, c.want)
		}
	}
}

func larkEventWithMessage(msgType, contentJSON string) string {
	b, _ := json.Marshal(contentJSON)
	return `{"schema":"2.0","header":{"event_id":"evt-x","event_type":"im.message.receive_v1"},
	  "event":{"sender":{"sender_id":{"open_id":"ou_a"}},
	  "message":{"message_id":"om_x","chat_id":"oc_c","message_type":"` + msgType + `","content":` + string(b) + `}}}`
}

func TestParseLarkEventJSON_Image(t *testing.T) {
	ev, ok, err := parseLarkEventJSON([]byte(larkEventWithMessage("image", `{"image_key":"img_v2_abc"}`)))
	if err != nil || !ok {
		t.Fatalf("parse image: ok=%v err=%v", ok, err)
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "image" || ev.Attachments[0].ResourceID != "img_v2_abc" {
		t.Fatalf("image attachment = %+v", ev.Attachments)
	}
	if ev.MessageExtID != "om_x" {
		t.Fatalf("message id = %q", ev.MessageExtID)
	}
}

func TestParseLarkEventJSON_File(t *testing.T) {
	ev, ok, err := parseLarkEventJSON([]byte(larkEventWithMessage("file", `{"file_key":"file_v2_x","file_name":"report.pdf"}`)))
	if err != nil || !ok {
		t.Fatalf("parse file: ok=%v err=%v", ok, err)
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "file" ||
		ev.Attachments[0].ResourceID != "file_v2_x" || ev.Attachments[0].Name != "report.pdf" {
		t.Fatalf("file attachment = %+v", ev.Attachments)
	}
}

func TestParseLarkEventJSON_Post_TextAndImages(t *testing.T) {
	content := `{"title":"周报","content":[[{"tag":"text","text":"第一段"},{"tag":"img","image_key":"img_1"}],[{"tag":"a","text":"链接","href":"https://x"},{"tag":"img","image_key":"img_2"}]]}`
	ev, ok, err := parseLarkEventJSON([]byte(larkEventWithMessage("post", content)))
	if err != nil || !ok {
		t.Fatalf("parse post: ok=%v err=%v", ok, err)
	}
	for _, want := range []string{"周报", "第一段", "链接"} {
		if !strings.Contains(ev.Text, want) {
			t.Errorf("post text missing %q: %q", want, ev.Text)
		}
	}
	if len(ev.Attachments) != 2 || ev.Attachments[0].ResourceID != "img_1" || ev.Attachments[1].ResourceID != "img_2" {
		t.Fatalf("post images = %+v", ev.Attachments)
	}
}

func TestParseLarkEventJSON_UnsupportedTypeSkipped(t *testing.T) {
	// A sticker carries neither text nor a downloadable resource we handle.
	if _, ok, _ := parseLarkEventJSON([]byte(larkEventWithMessage("sticker", `{"file_key":"stk"}`))); ok {
		t.Fatalf("sticker should be skipped")
	}
}

func TestParseLarkEventJSON_CardAction(t *testing.T) {
	ev, ok, err := parseLarkEventJSON([]byte(sampleCardEvent))
	if err != nil || !ok {
		t.Fatalf("parse card: ok=%v err=%v", ok, err)
	}
	if ev.Kind != "action_callback" || ev.CallbackData != "permission:approve:42" {
		t.Fatalf("unexpected callback: %+v", ev)
	}
	if ev.ChatExtID != "oc_chat" {
		t.Fatalf("chat = %q", ev.ChatExtID)
	}
}

func TestParseLarkEventJSON_NonActionable(t *testing.T) {
	// URL verification and unknown types must be skipped, not errored.
	for _, body := range []string{
		`{"type":"url_verification","challenge":"c"}`,
		`{"schema":"2.0","header":{"event_type":"im.chat.updated_v1"},"event":{}}`,
		``,
		`not-json`,
	} {
		if _, ok, err := parseLarkEventJSON([]byte(body)); ok || err != nil {
			t.Fatalf("body %q: expected skip, got ok=%v err=%v", body, ok, err)
		}
	}
}

func TestChallenge(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{"type":"url_verification","challenge":"abc123"}`))
	echo, isChallenge := a.Challenge(r)
	if !isChallenge {
		t.Fatalf("expected challenge")
	}
	var out struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(echo, &out); err != nil || out.Challenge != "abc123" {
		t.Fatalf("echo = %s err=%v", echo, err)
	}

	// An ordinary event body is not a challenge, and the body stays readable.
	r2 := httptest.NewRequest("POST", "/webhook", strings.NewReader(sampleMessageEvent))
	if _, isChallenge := a.Challenge(r2); isChallenge {
		t.Fatalf("message body should not be a challenge")
	}
	cred := imbot.Credential{Config: map[string]any{"webhook_secret": "vtok"}}
	ev, err := a.VerifyWebhook(r2, cred)
	if err != nil || ev.ChatExtID != "oc_chat" {
		t.Fatalf("verify after challenge: ev=%+v err=%v", ev, err)
	}
}

// larkBodyWithToken builds a v2 message event whose header.token is the given
// Lark "Verification Token" — the value the platform stamps on every webhook so
// the receiver can prove the request came from Lark and not a forger.
func larkBodyWithToken(token string) string {
	return `{
  "schema": "2.0",
  "header": {"event_id": "evt-tok", "event_type": "im.message.receive_v1", "token": "` + token + `"},
  "event": {
    "sender": {"sender_id": {"open_id": "ou_actor"}},
    "message": {"message_id": "om_1", "chat_id": "oc_chat", "message_type": "text", "content": "{\"text\":\"hi\"}"}
  }
}`
}

// A forged event carrying the wrong verification token must be rejected as
// unauthorized — otherwise anyone who can reach the public webhook endpoint and
// guess a channelId could inject messages or permission-approval callbacks.
func TestVerifyWebhook_RejectsForgedToken(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(larkBodyWithToken("attacker-guess")))
	cred := imbot.Credential{Channel: imbot.ChannelLark, Config: map[string]any{"webhook_secret": "real-verification-token"}}
	if _, err := a.VerifyWebhook(r, cred); !errors.Is(err, imbot.ErrWebhookUnauthorized) {
		t.Fatalf("forged token: want ErrWebhookUnauthorized, got %v", err)
	}
}

// A webhook-mode channel with no configured secret must fail closed: absent a
// secret we cannot authenticate the caller, so we must reject rather than trust.
func TestVerifyWebhook_FailsClosedWhenSecretUnset(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(larkBodyWithToken("anything")))
	cred := imbot.Credential{Channel: imbot.ChannelLark, Config: map[string]any{"webhook_secret": ""}}
	if _, err := a.VerifyWebhook(r, cred); !errors.Is(err, imbot.ErrWebhookUnauthorized) {
		t.Fatalf("unset secret: want ErrWebhookUnauthorized (fail closed), got %v", err)
	}
}

func TestVerifyWebhook_AcceptsMatchingToken(t *testing.T) {
	a := New()
	r := httptest.NewRequest("POST", "/webhook", strings.NewReader(larkBodyWithToken("real-verification-token")))
	cred := imbot.Credential{Channel: imbot.ChannelLark, Config: map[string]any{"webhook_secret": "real-verification-token"}}
	ev, err := a.VerifyWebhook(r, cred)
	if err != nil || ev.ChatExtID != "oc_chat" {
		t.Fatalf("matching token: ev=%+v err=%v", ev, err)
	}
}

func TestBuildInteractiveCard_EmbedsCallback(t *testing.T) {
	card := buildInteractiveCard("允许吗？", []imbot.Button{
		{Label: "允许", Value: "permission:approve:7"},
		{Label: "拒绝", Value: "permission:deny:7"},
	})
	if !strings.Contains(card, "permission:approve:7") || !strings.Contains(card, "permission:deny:7") {
		t.Fatalf("card missing callback values: %s", card)
	}
	if !strings.Contains(card, `"cb"`) || !strings.Contains(card, "允许吗？") {
		t.Fatalf("card malformed: %s", card)
	}
	// Must be valid JSON.
	var probe map[string]any
	if err := json.Unmarshal([]byte(card), &probe); err != nil {
		t.Fatalf("card not valid json: %v", err)
	}
}

func TestBuildMarkdownCard_UsesMarkdownElement(t *testing.T) {
	card := buildMarkdownCard("**加粗** 和 [链接](https://x)\n- 一\n- 二")
	// Must be a valid card whose sole element is a markdown component carrying
	// the raw markdown (so Feishu renders it rather than showing it verbatim).
	var probe struct {
		Elements []struct {
			Tag     string `json:"tag"`
			Content string `json:"content"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(card), &probe); err != nil {
		t.Fatalf("card not valid json: %v", err)
	}
	if len(probe.Elements) != 1 || probe.Elements[0].Tag != "markdown" {
		t.Fatalf("expected a single markdown element, got %+v", probe.Elements)
	}
	if !strings.Contains(probe.Elements[0].Content, "**加粗**") {
		t.Fatalf("markdown content lost: %q", probe.Elements[0].Content)
	}
}

// buildFrame assembles a minimal pbbp2.Frame carrying one header and a payload,
// mirroring what the Lark WS stream delivers.
func buildFrame(headers map[string]string, payload []byte) []byte {
	var b []byte
	// field 4: method (varint)
	b = protowire.AppendTag(b, 4, protowire.VarintType)
	b = protowire.AppendVarint(b, 2) // data method
	// field 5: headers (repeated nested message)
	for k, v := range headers {
		var h []byte
		h = protowire.AppendTag(h, 1, protowire.BytesType)
		h = protowire.AppendBytes(h, []byte(k))
		h = protowire.AppendTag(h, 2, protowire.BytesType)
		h = protowire.AppendBytes(h, []byte(v))
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		b = protowire.AppendBytes(b, h)
	}
	// field 8: payload (bytes)
	b = protowire.AppendTag(b, 8, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

func TestDecodeFrame_RoundTrip(t *testing.T) {
	frame := buildFrame(map[string]string{"type": "event", "message_id": "m1"}, []byte(sampleMessageEvent))
	f, err := decodeFrame(frame)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if f.Headers["type"] != "event" || f.Headers["message_id"] != "m1" {
		t.Fatalf("headers = %+v", f.Headers)
	}
	if string(f.Payload) != sampleMessageEvent {
		t.Fatalf("payload mismatch")
	}
}

func TestDispatchFrame_InvokesHandler(t *testing.T) {
	a := New()
	frame := buildFrame(map[string]string{"type": "event"}, []byte(sampleMessageEvent))
	var got imbot.InboundEvent
	var called int
	a.dispatchFrame(context.Background(), frame, func(_ context.Context, ev imbot.InboundEvent) {
		called++
		got = ev
	})
	if called != 1 || got.ChatExtID != "oc_chat" || got.Text != "帮我做个表" {
		t.Fatalf("handler not invoked correctly: called=%d ev=%+v", called, got)
	}

	// A control/ping frame (no actionable payload) must not call the handler.
	a.dispatchFrame(context.Background(), buildFrame(map[string]string{"type": "ping"}, []byte("{}")), func(context.Context, imbot.InboundEvent) {
		t.Fatalf("handler should not fire for non-actionable frame")
	})
}
