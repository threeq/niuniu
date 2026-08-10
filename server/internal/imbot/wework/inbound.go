package wework

import (
	"encoding/xml"
	"strings"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// This file decodes a decrypted WeCom callback plaintext XML into the normalized
// imbot.InboundEvent. A self-built app's text message looks like:
//
//	<xml>
//	  <ToUserName>corpid</ToUserName>
//	  <FromUserName>userid</FromUserName>
//	  <CreateTime>1700000000</CreateTime>
//	  <MsgType>text</MsgType>
//	  <Content>hi</Content>
//	  <MsgId>1234567890</MsgId>
//	  <AgentID>1000002</AgentID>
//	</xml>
//
// WeCom self-built app callbacks are per-user (FromUserName is the sender's
// userid); there is no group/chat id for app messages, so ChatExtID is the user
// id too. A group chat callback (rare, "群机器人回调") carries a ChatId element,
// which we prefer as the chat id when present.

// weworkXML is the subset of a decrypted callback we act on.
type weworkXML struct {
	ToUserName   string `xml:"ToUserName"`
	FromUserName string `xml:"FromUserName"`
	CreateTime   int64  `xml:"CreateTime"`
	MsgType      string `xml:"MsgType"`
	Content      string `xml:"Content"`
	MsgID        string `xml:"MsgId"`
	AgentID      string `xml:"AgentID"`
	ChatID       string `xml:"ChatId"` // present only for group-chat callbacks
}

// parseWeworkXML normalizes a decrypted plaintext XML into an imbot.InboundEvent.
// ok=false means the payload is not an actionable text message (an event push
// such as subscribe/unsubscribe, a non-text media message, or malformed XML) and
// should be skipped.
func parseWeworkXML(plain []byte) (imbot.InboundEvent, bool) {
	var m weworkXML
	if err := xml.Unmarshal(plain, &m); err != nil {
		return imbot.InboundEvent{}, false
	}
	// Only plain text messages route into the task pipeline; media and event
	// pushes (subscribe, click, etc.) are ignored for the first WeCom cut.
	if !strings.EqualFold(strings.TrimSpace(m.MsgType), "text") {
		return imbot.InboundEvent{}, false
	}
	text := strings.TrimSpace(m.Content)
	chatID := strings.TrimSpace(m.ChatID)
	// In a group callback (ChatId present), WeCom inlines "@<appname>" at the
	// start of Content when the app is @-mentioned, just like DingTalk; strip
	// that single leading mention so the real text — and any leading slash
	// command — is recognized. 1:1 callbacks (no ChatId) never carry the prefix.
	if chatID != "" {
		text = stripLeadingAtMention(text)
	}
	if text == "" {
		return imbot.InboundEvent{}, false
	}
	if strings.TrimSpace(m.FromUserName) == "" {
		return imbot.InboundEvent{}, false
	}

	// ChatExtID is the group id when the callback is a group chat, else the
	// sender's userid (self-built app callbacks are per-user, no group id).
	chatExtID := strings.TrimSpace(m.FromUserName)
	if chatID != "" {
		chatExtID = chatID
	}

	ev := imbot.InboundEvent{
		Channel:    imbot.ChannelWework,
		ChatExtID:  chatExtID,
		ActorExtID: strings.TrimSpace(m.FromUserName),
		Text:       text,
		Kind:       "message",
		EventID:    strings.TrimSpace(m.MsgID),
	}
	return ev, true
}

// stripLeadingAtMention removes the single leading "@<name>" mention WeCom
// prepends to a group callback's Content when the app is @-mentioned (e.g.
// "@牛牛 /issues 提交" -> "/issues 提交"). WeCom inlines the app's display name
// literally and offers no structured name to match precisely, so we drop the one
// leading "@<run-of-non-space>" token — the app's own mention (how a user
// addresses it). Any later "@human" in the user's actual text is preserved.
// Group-only (caller gates on ChatId): 1:1 callbacks never carry the prefix.
func stripLeadingAtMention(text string) string {
	if !strings.HasPrefix(text, "@") {
		return text
	}
	rest := text[1:]
	if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
		rest = rest[i:]
	} else {
		rest = "" // entire text is "@<name>" with nothing after
	}
	return strings.TrimLeftFunc(rest, unicode.IsSpace)
}
