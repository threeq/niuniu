package dingtalk

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

func TestParseDingTalkBotMessage_Text(t *testing.T) {
	raw := []byte(`{
		"msgtype":"text",
		"text":{"content":"  帮我做张表  "},
		"conversationId":"cid_group_1",
		"conversationType":"2",
		"senderStaffId":"staff_42",
		"senderId":"sender_99",
		"msgId":"m_100",
		"sessionWebhook":"https://oapi.dingtalk.com/robot/sendBySession?session=abc",
		"robotCode":"robot_x"
	}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true for a text message")
	}
	if ev.ChatExtID != "cid_group_1" {
		t.Errorf("ChatExtID=%q, want cid_group_1", ev.ChatExtID)
	}
	if ev.ThreadExtID != "" {
		t.Errorf("ThreadExtID=%q, want empty", ev.ThreadExtID)
	}
	if ev.ActorExtID != "staff_42" {
		t.Errorf("ActorExtID=%q, want staff_42 (senderStaffId preferred)", ev.ActorExtID)
	}
	if ev.Text != "帮我做张表" {
		t.Errorf("Text=%q, want trimmed 帮我做张表", ev.Text)
	}
	if ev.Kind != "message" {
		t.Errorf("Kind=%q, want message", ev.Kind)
	}
	if ev.EventID != "m_100" {
		t.Errorf("EventID=%q, want m_100 (msgId for dedupe)", ev.EventID)
	}
	if ev.Raw["sessionWebhook"] != "https://oapi.dingtalk.com/robot/sendBySession?session=abc" {
		t.Errorf("Raw.sessionWebhook not stashed: %v", ev.Raw["sessionWebhook"])
	}
	if ev.Raw["conversationType"] != "2" || ev.Raw["robotCode"] != "robot_x" {
		t.Errorf("Raw missing conversationType/robotCode: %v", ev.Raw)
	}
}

func TestParseDingTalkBotMessage_FallsBackToSenderID(t *testing.T) {
	raw := []byte(`{"msgtype":"text","text":{"content":"hi"},"conversationId":"c","senderId":"sender_only","msgId":"m1"}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.ActorExtID != "sender_only" {
		t.Errorf("ActorExtID=%q, want sender_only (fallback)", ev.ActorExtID)
	}
}

func TestParseDingTalkBotMessage_SkipsEmptyAndNonText(t *testing.T) {
	cases := map[string]string{
		"empty text":      `{"msgtype":"text","text":{"content":"   "},"conversationId":"c","msgId":"m"}`,
		"picture no code": `{"msgtype":"picture","content":{},"conversationId":"c","msgId":"m"}`,
		"unknown no text": `{"msgtype":"weird","conversationId":"c","msgId":"m"}`,
		"missing conv":    `{"msgtype":"text","text":{"content":"x"},"msgId":"m"}`,
		"malformed json":  `not json`,
		"ack frame shape": `{"response":{},"status":"SUCCESS","message":"OK"}`,
	}
	for name, raw := range cases {
		if _, ok := parseDingTalkBotMessage([]byte(raw)); ok {
			t.Errorf("%s: expected ok=false", name)
		}
	}
}

func TestParseCardActionCallback_BestEffort(t *testing.T) {
	// Bare permission payload under "value".
	ev, ok := parseCardActionCallback([]byte(`{"value":"permission:approve:9","conversationId":"c","senderStaffId":"s","msgId":"mm"}`))
	if !ok {
		t.Fatal("expected ok=true for a permission callback")
	}
	if ev.Kind != "action_callback" {
		t.Errorf("Kind=%q, want action_callback", ev.Kind)
	}
	if ev.CallbackData != "permission:approve:9" {
		t.Errorf("CallbackData=%q, want permission:approve:9", ev.CallbackData)
	}
	if ev.ActorExtID != "s" {
		t.Errorf("ActorExtID=%q, want s", ev.ActorExtID)
	}

	// JSON-wrapped payload under cardPrivateData.
	ev2, ok2 := parseCardActionCallback([]byte(`{"cardPrivateData":"{\"cb\":\"permission:deny:5\"}","conversationId":"c"}`))
	if !ok2 || ev2.CallbackData != "permission:deny:5" {
		t.Fatalf("wrapped payload: ok=%v cb=%q", ok2, ev2.CallbackData)
	}

	// No permission marker -> not our callback.
	if _, ok3 := parseCardActionCallback([]byte(`{"value":"something-else"}`)); ok3 {
		t.Error("expected ok=false for a non-permission callback")
	}
}

// sanity: the normalizer stamps the channel via the caller, but a direct parse
// leaves Channel set by the parser itself for defense in depth.
func TestParseDingTalkBotMessage_ChannelStamped(t *testing.T) {
	ev, _ := parseDingTalkBotMessage([]byte(`{"msgtype":"text","text":{"content":"x"},"conversationId":"c","msgId":"m"}`))
	if ev.Channel != imbot.ChannelDingTalk {
		t.Errorf("Channel=%q, want dingtalk", ev.Channel)
	}
}

// MessageExtID encodes the conversation id so Reply/React can target it; a text
// message still populates it (msgId + conv), and decodeMsgRef recovers the conv.
func TestParseDingTalkBotMessage_MessageExtIDCarriesConversation(t *testing.T) {
	ev, ok := parseDingTalkBotMessage([]byte(`{"msgtype":"text","text":{"content":"hi"},"conversationId":"cid_9","msgId":"m_7"}`))
	if !ok {
		t.Fatal("expected ok=true")
	}
	msgID, conv := decodeMsgRef(ev.MessageExtID)
	if msgID != "m_7" || conv != "cid_9" {
		t.Errorf("decodeMsgRef(%q) = (%q,%q), want (m_7,cid_9)", ev.MessageExtID, msgID, conv)
	}
}

// In a group, DingTalk prepends the literal "@<botname>" the user mentioned to
// text.content. That leading mention must be stripped so the real text — and any
// leading slash command — is what the agent sees.
func TestParseDingTalkBotMessage_StripsLeadingAtMentionInGroup(t *testing.T) {
	raw := []byte(`{
		"msgtype":"text",
		"text":{"content":"@牛牛 /issues 提交"},
		"conversationId":"cid_grp",
		"conversationType":"2",
		"senderStaffId":"staff_1",
		"msgId":"m_at"
	}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Text != "/issues 提交" {
		t.Errorf("Text=%q, want %q (leading @bot mention stripped)", ev.Text, "/issues 提交")
	}
}

// A group @mention actually arrives as a richText message, NOT a text message:
// DingTalk turns the "@bot" into a rich-text run, so the literal "@牛牛" lands in
// content.richText[], never in text.content. The leading bot mention must still
// be stripped from the assembled richText text (this is the real production
// payload — msgtype=richText, text.content="" — that the text-only strip missed).
func TestParseDingTalkBotMessage_StripsLeadingAtMentionInRichTextGroup(t *testing.T) {
	raw := []byte(`{
		"msgtype":"richText",
		"conversationId":"cid_grp",
		"conversationType":"2",
		"senderStaffId":"staff_1",
		"msgId":"m_rt",
		"content":{"richText":[{"text":"@牛牛 "},{"text":"/issues 提交"}]}
	}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Text != "/issues 提交" {
		t.Errorf("Text=%q, want %q (leading @bot mention stripped from richText)", ev.Text, "/issues 提交")
	}
}

// A bare "@bot" richText mention with no other content is not actionable: after
// stripping the only run there is no text and no attachment, so it is skipped.
func TestParseDingTalkBotMessage_BareAtMentionRichTextSkipped(t *testing.T) {
	raw := []byte(`{"msgtype":"richText","conversationId":"c","conversationType":"2","senderStaffId":"s","msgId":"m","content":{"richText":[{"text":"@牛牛"}]}}`)
	if _, ok := parseDingTalkBotMessage(raw); ok {
		t.Error("expected ok=false for a bare @mention richText with no content")
	}
}

// Only the leading bot mention is stripped; a later "@human" that is part of the
// user's actual message is preserved.
func TestParseDingTalkBotMessage_StripsOnlyLeadingBotMention(t *testing.T) {
	raw := []byte(`{"msgtype":"text","text":{"content":"@牛牛 @张三 你好"},"conversationId":"c","conversationType":"2","senderStaffId":"s","msgId":"m"}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Text != "@张三 你好" {
		t.Errorf("Text=%q, want @张三 你好 (subsequent @human preserved)", ev.Text)
	}
}

// A bare "@bot" with no content is not actionable and is skipped.
func TestParseDingTalkBotMessage_BareAtMentionSkipped(t *testing.T) {
	raw := []byte(`{"msgtype":"text","text":{"content":"@牛牛"},"conversationId":"c","conversationType":"2","senderStaffId":"s","msgId":"m"}`)
	if _, ok := parseDingTalkBotMessage(raw); ok {
		t.Error("expected ok=false for a bare @mention with no content")
	}
}

// 1:1 DMs never carry the @bot prefix, so a literal leading "@" is the user's
// own text and must be preserved (the strip is group-only).
func TestParseDingTalkBotMessage_PreservesLeadingAtInDM(t *testing.T) {
	raw := []byte(`{"msgtype":"text","text":{"content":"@someone 你好"},"conversationId":"cid_dm","conversationType":"1","senderStaffId":"s","msgId":"m"}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Text != "@someone 你好" {
		t.Errorf("Text=%q, want @someone 你好 (DM leading @ preserved)", ev.Text)
	}
}

func TestParseDingTalkBotMessage_Picture(t *testing.T) {
	ev, ok := parseDingTalkBotMessage([]byte(`{"msgtype":"picture","content":{"downloadCode":"dc_img"},"conversationId":"c","msgId":"m"}`))
	if !ok {
		t.Fatal("expected ok=true for a picture message")
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "image" || ev.Attachments[0].ResourceID != "dc_img" {
		t.Fatalf("attachments=%+v, want one image with ResourceID dc_img", ev.Attachments)
	}
}

func TestParseDingTalkBotMessage_RichTextTextPlusPicture(t *testing.T) {
	raw := []byte(`{"msgtype":"richText","conversationId":"c","msgId":"m","content":{"richText":[{"text":"这个图片里面是什么"},{"type":"picture","downloadCode":"dc_1"}]}}`)
	ev, ok := parseDingTalkBotMessage(raw)
	if !ok {
		t.Fatal("expected ok=true for a richText message")
	}
	if ev.Text != "这个图片里面是什么" {
		t.Errorf("Text=%q, want 这个图片里面是什么", ev.Text)
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "image" || ev.Attachments[0].ResourceID != "dc_1" {
		t.Fatalf("attachments=%+v, want one image dc_1", ev.Attachments)
	}
}

func TestParseDingTalkBotMessage_File(t *testing.T) {
	ev, ok := parseDingTalkBotMessage([]byte(`{"msgtype":"file","content":{"downloadCode":"dc_f","fileName":"report.pdf"},"conversationId":"c","msgId":"m"}`))
	if !ok {
		t.Fatal("expected ok=true for a file message")
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "file" || ev.Attachments[0].Name != "report.pdf" {
		t.Fatalf("attachments=%+v, want one file report.pdf", ev.Attachments)
	}
}

func TestParseDingTalkBotMessage_AudioUsesRecognition(t *testing.T) {
	ev, ok := parseDingTalkBotMessage([]byte(`{"msgtype":"audio","content":{"downloadCode":"dc_a","recognition":"帮我建个任务"},"conversationId":"c","msgId":"m"}`))
	if !ok {
		t.Fatal("expected ok=true for an audio message")
	}
	if ev.Text != "帮我建个任务" {
		t.Errorf("Text=%q, want the ASR transcript 帮我建个任务", ev.Text)
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "audio" {
		t.Fatalf("attachments=%+v, want one audio", ev.Attachments)
	}
}

func TestParseDingTalkBotMessage_Video(t *testing.T) {
	ev, ok := parseDingTalkBotMessage([]byte(`{"msgtype":"video","content":{"downloadCode":"dc_v"},"conversationId":"c","msgId":"m"}`))
	if !ok {
		t.Fatal("expected ok=true for a video message")
	}
	if len(ev.Attachments) != 1 || ev.Attachments[0].Kind != "video" || ev.Attachments[0].ResourceID != "dc_v" {
		t.Fatalf("attachments=%+v, want one video dc_v", ev.Attachments)
	}
}
