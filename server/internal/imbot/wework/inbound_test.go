package wework

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

func TestParseWeworkXML_TextMessage(t *testing.T) {
	plain := []byte(`<xml>
		<ToUserName>ww_corp</ToUserName>
		<FromUserName>lucy</FromUserName>
		<CreateTime>1700000000</CreateTime>
		<MsgType>text</MsgType>
		<Content>  帮我做张表  </Content>
		<MsgId>1234567890</MsgId>
		<AgentID>1000002</AgentID>
	</xml>`)

	ev, ok := parseWeworkXML(plain)
	if !ok {
		t.Fatal("expected an actionable text message")
	}
	if ev.Channel != imbot.ChannelWework {
		t.Errorf("Channel = %q, want wework", ev.Channel)
	}
	if ev.ChatExtID != "lucy" {
		t.Errorf("ChatExtID = %q, want lucy (per-user app callback)", ev.ChatExtID)
	}
	if ev.ActorExtID != "lucy" {
		t.Errorf("ActorExtID = %q, want lucy", ev.ActorExtID)
	}
	if ev.Text != "帮我做张表" {
		t.Errorf("Text = %q, want trimmed 帮我做张表", ev.Text)
	}
	if ev.Kind != "message" {
		t.Errorf("Kind = %q, want message", ev.Kind)
	}
	if ev.EventID != "1234567890" {
		t.Errorf("EventID = %q, want 1234567890", ev.EventID)
	}
}

func TestParseWeworkXML_GroupChatPrefersChatId(t *testing.T) {
	plain := []byte(`<xml>
		<FromUserName>lucy</FromUserName>
		<MsgType>text</MsgType>
		<Content>hi</Content>
		<MsgId>9</MsgId>
		<ChatId>group-42</ChatId>
	</xml>`)
	ev, ok := parseWeworkXML(plain)
	if !ok {
		t.Fatal("expected an actionable text message")
	}
	if ev.ChatExtID != "group-42" {
		t.Errorf("ChatExtID = %q, want group-42 (ChatId preferred)", ev.ChatExtID)
	}
	if ev.ActorExtID != "lucy" {
		t.Errorf("ActorExtID = %q, want lucy", ev.ActorExtID)
	}
}

// In a group callback (ChatId present), WeCom prepends the literal "@<appname>"
// the user mentioned to Content; that leading mention must be stripped so the
// real text — and any leading slash command — is what the agent sees.
func TestParseWeworkXML_GroupStripsLeadingAtMention(t *testing.T) {
	plain := []byte(`<xml>
		<FromUserName>lucy</FromUserName>
		<MsgType>text</MsgType>
		<Content>@牛牛 /issues 提交</Content>
		<MsgId>101</MsgId>
		<ChatId>group-42</ChatId>
	</xml>`)
	ev, ok := parseWeworkXML(plain)
	if !ok {
		t.Fatal("expected an actionable text message")
	}
	if ev.Text != "/issues 提交" {
		t.Errorf("Text = %q, want /issues 提交 (leading @app mention stripped)", ev.Text)
	}
	if ev.ChatExtID != "group-42" {
		t.Errorf("ChatExtID = %q, want group-42", ev.ChatExtID)
	}
}

// Only the leading app mention is stripped; a later "@human" is preserved.
func TestParseWeworkXML_GroupStripsOnlyLeadingAppMention(t *testing.T) {
	plain := []byte(`<xml><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>@牛牛 @张三 你好</Content><MsgId>102</MsgId><ChatId>g</ChatId></xml>`)
	ev, ok := parseWeworkXML(plain)
	if !ok {
		t.Fatal("expected an actionable text message")
	}
	if ev.Text != "@张三 你好" {
		t.Errorf("Text = %q, want @张三 你好 (subsequent @human preserved)", ev.Text)
	}
}

// A bare "@app" with no content in a group is not actionable and is skipped.
func TestParseWeworkXML_GroupBareAtMentionSkipped(t *testing.T) {
	plain := []byte(`<xml><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>@牛牛</Content><MsgId>103</MsgId><ChatId>g</ChatId></xml>`)
	if _, ok := parseWeworkXML(plain); ok {
		t.Error("expected ok=false for a bare @mention with no content")
	}
}

// 1:1 callbacks (no ChatId) never carry the @bot prefix, so a literal leading
// "@" is the user's own text and must be preserved.
func TestParseWeworkXML_DMPreservesLeadingAt(t *testing.T) {
	plain := []byte(`<xml><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>@someone 你好</Content><MsgId>104</MsgId></xml>`)
	ev, ok := parseWeworkXML(plain)
	if !ok {
		t.Fatal("expected an actionable text message")
	}
	if ev.Text != "@someone 你好" {
		t.Errorf("Text = %q, want @someone 你好 (1:1 leading @ preserved)", ev.Text)
	}
}

func TestParseWeworkXML_SkipsNonText(t *testing.T) {
	cases := map[string][]byte{
		"image event": []byte(`<xml><FromUserName>lucy</FromUserName><MsgType>image</MsgType><PicUrl>http://x</PicUrl><MsgId>1</MsgId></xml>`),
		"empty text":  []byte(`<xml><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>   </Content><MsgId>1</MsgId></xml>`),
		"no sender":   []byte(`<xml><MsgType>text</MsgType><Content>hi</Content><MsgId>1</MsgId></xml>`),
		"malformed":   []byte(`not xml at all`),
	}
	for name, plain := range cases {
		if _, ok := parseWeworkXML(plain); ok {
			t.Errorf("%s: expected ok=false", name)
		}
	}
}
