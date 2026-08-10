package telegram

import (
	"encoding/json"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

func mustUpdate(t *testing.T, raw string) tgUpdate {
	t.Helper()
	var u tgUpdate
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	return u
}

func TestParseTelegramUpdate_Message(t *testing.T) {
	u := mustUpdate(t, `{"update_id":100,"message":{"message_id":5,"from":{"id":77},"chat":{"id":4242,"type":"private"},"text":"帮我做张表"}}`)
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		t.Fatal("expected actionable message")
	}
	if ev.Kind != "message" || ev.Channel != imbot.ChannelTelegram {
		t.Fatalf("kind/channel = %q/%q", ev.Kind, ev.Channel)
	}
	if ev.ChatExtID != "4242" || ev.ActorExtID != "77" || ev.Text != "帮我做张表" {
		t.Fatalf("fields = %+v", ev)
	}
	if ev.ThreadExtID != "" {
		t.Errorf("plain DM should have no thread, got %q", ev.ThreadExtID)
	}
	if ev.EventID != "u100" {
		t.Errorf("EventID=%q, want u100", ev.EventID)
	}
}

func TestParseTelegramUpdate_TopicThread(t *testing.T) {
	u := mustUpdate(t, `{"update_id":101,"message":{"message_id":6,"from":{"id":77},"chat":{"id":4242,"type":"supergroup"},"is_topic_message":true,"message_thread_id":88,"text":"继续"}}`)
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		t.Fatal("expected actionable message")
	}
	if ev.ThreadExtID != "88" {
		t.Errorf("ThreadExtID=%q, want 88", ev.ThreadExtID)
	}
}

func TestParseTelegramUpdate_Callback(t *testing.T) {
	u := mustUpdate(t, `{"update_id":102,"callback_query":{"id":"cb","from":{"id":77},"data":"permission:approve:9","message":{"message_id":7,"chat":{"id":4242,"type":"private"}}}}`)
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		t.Fatal("expected actionable callback")
	}
	if ev.Kind != "action_callback" || ev.CallbackData != "permission:approve:9" {
		t.Fatalf("callback fields = %+v", ev)
	}
	if ev.ChatExtID != "4242" || ev.ActorExtID != "77" {
		t.Fatalf("callback chat/actor = %+v", ev)
	}
	if ev.EventID != "u102" {
		t.Errorf("EventID=%q, want u102", ev.EventID)
	}
}

// MessageExtID encodes chat+message id so Reply/React can target the message;
// decodeMsgRef recovers both parts.
func TestParseTelegramUpdate_MessageExtIDCarriesChatAndMessage(t *testing.T) {
	u := mustUpdate(t, `{"update_id":100,"message":{"message_id":5,"from":{"id":77},"chat":{"id":4242,"type":"private"},"text":"hi"}}`)
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		t.Fatal("expected actionable message")
	}
	chatID, msgID := decodeMsgRef(ev.MessageExtID)
	if chatID != "4242" || msgID != "5" {
		t.Errorf("decodeMsgRef(%q) = (%q,%q), want (4242,5)", ev.MessageExtID, chatID, msgID)
	}
}

func TestParseTelegramUpdate_PhotoWithCaption(t *testing.T) {
	// Photo arrives as an ascending size array; the largest (last) file_id wins.
	u := mustUpdate(t, `{"update_id":100,"message":{"message_id":5,"chat":{"id":4242,"type":"private"},"caption":"这个图片里面是什么","photo":[{"file_id":"small"},{"file_id":"big"}]}}`)
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		t.Fatal("expected actionable photo message")
	}
	if ev.Text != "这个图片里面是什么" {
		t.Errorf("Text=%q, want the caption", ev.Text)
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "image" || ev.Attachments[0].ResourceID != "big" {
		t.Fatalf("attachments=%+v, want one image with the largest file_id", ev.Attachments)
	}
}

func TestParseTelegramUpdate_Document(t *testing.T) {
	u := mustUpdate(t, `{"update_id":100,"message":{"message_id":5,"chat":{"id":4242,"type":"private"},"document":{"file_id":"doc1","file_name":"report.pdf"}}}`)
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		t.Fatal("expected actionable document message")
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "file" || ev.Attachments[0].Name != "report.pdf" || ev.Attachments[0].ResourceID != "doc1" {
		t.Fatalf("attachments=%+v, want one file report.pdf/doc1", ev.Attachments)
	}
}

func TestParseTelegramUpdate_VoiceAndVideo(t *testing.T) {
	voice := mustUpdate(t, `{"update_id":100,"message":{"message_id":5,"chat":{"id":4242,"type":"private"},"voice":{"file_id":"v1"}}}`)
	if ev, ok := parseTelegramUpdate(voice); !ok || len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "audio" {
		t.Fatalf("voice: ok=%v attachments=%+v, want one audio", ok, ev.Attachments)
	}
	video := mustUpdate(t, `{"update_id":101,"message":{"message_id":6,"chat":{"id":4242,"type":"private"},"video":{"file_id":"vid1"}}}`)
	if ev, ok := parseTelegramUpdate(video); !ok || len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "video" {
		t.Fatalf("video: ok=%v attachments=%+v, want one video", ok, ev.Attachments)
	}
}

func TestParseTelegramUpdate_NonActionable(t *testing.T) {
	cases := map[string]string{
		"empty update":        `{"update_id":1}`,
		"non-text message":    `{"update_id":2,"message":{"message_id":1,"chat":{"id":4242,"type":"private"}}}`,
		"blank text":          `{"update_id":3,"message":{"message_id":1,"chat":{"id":4242,"type":"private"},"text":"   "}}`,
		"missing chat":        `{"update_id":4,"message":{"message_id":1,"text":"hi"}}`,
		"callback no data":    `{"update_id":5,"callback_query":{"id":"c","message":{"chat":{"id":4242}}}}`,
		"callback no message": `{"update_id":6,"callback_query":{"id":"c","data":"x"}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseTelegramUpdate(mustUpdate(t, raw)); ok {
				t.Errorf("expected non-actionable for %s", name)
			}
		})
	}
}
