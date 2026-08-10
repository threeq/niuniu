package dingtalk

import (
	"encoding/json"
	"log/slog"
	"strings"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// This file normalizes DingTalk inbound payloads into imbot.InboundEvent. Two
// transports feed the same JSON documents:
//
//   - the Stream long connection (default, LAN-friendly): the CALLBACK frame's
//     `data` field is a JSON STRING carrying the bot message;
//   - the optional public webhook: the HTTP body IS that same JSON document.
//
// Both funnel through parseDingTalkBotMessage, so normalization is
// transport-independent and unit-tested at the JSON layer.

// dingtalkBotMessage is the subset of a DingTalk bot-message callback we need.
// `content` carries the media/rich payloads (picture/richText/file/audio/video);
// it is a distinct top-level field from `text` (which only text messages use).
type dingtalkBotMessage struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	Content          dingtalkContent `json:"content"`
	ConversationID   string          `json:"conversationId"`
	ConversationType string          `json:"conversationType"` // "1"=1:1, "2"=group
	SenderStaffID    string          `json:"senderStaffId"`
	SenderID         string          `json:"senderId"`
	MsgID            string          `json:"msgId"`
	SessionWebhook   string          `json:"sessionWebhook"`
	RobotCode        string          `json:"robotCode"`
}

// dingtalkContent is the `content` object DingTalk attaches to non-text bot
// messages. Each media kind carries a `downloadCode` (a short-lived key the
// robot exchanges for a signed download URL, see FetchResource); richText mixes
// text runs and embedded pictures in one array. Only the fields we consume are
// modeled — DingTalk sends more (duration, width, etc.) we ignore.
type dingtalkContent struct {
	DownloadCode string `json:"downloadCode"`
	FileName     string `json:"fileName"`
	Recognition  string `json:"recognition"` // audio ASR transcript, when present
	RichText     []struct {
		Text         string `json:"text"`
		Type         string `json:"type"`         // "picture" for embedded images
		DownloadCode string `json:"downloadCode"` // present on picture runs
	} `json:"richText"`
}

// parseDingTalkBotMessage normalizes a DingTalk bot-message JSON document into an
// imbot.InboundEvent. Beyond plain text it now handles image/richText/file/audio/
// video messages: each carried resource becomes an imbot.InboundAttachment whose
// ResourceID is the DingTalk downloadCode (materialized later via FetchResource).
// ok=false means the payload is not actionable (missing conversation, or a
// msgtype with neither text nor a downloadable resource) and should be skipped.
// It never returns an error: a malformed document simply yields ok=false
// (control/ack frames flow through here too).
func parseDingTalkBotMessage(raw []byte) (imbot.InboundEvent, bool) {
	var m dingtalkBotMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return imbot.InboundEvent{}, false
	}
	if m.ConversationID == "" {
		return imbot.InboundEvent{}, false
	}

	text := strings.TrimSpace(m.Text.Content)
	var atts []imbot.InboundAttachment
	switch m.MsgType {
	case "", "text":
		// text already extracted above
	case "richText":
		// richText interleaves text runs and embedded pictures; concatenate the
		// text and lift each picture run into an image attachment.
		var parts []string
		for _, it := range m.Content.RichText {
			if t := strings.TrimSpace(it.Text); t != "" {
				parts = append(parts, t)
			}
			if it.DownloadCode != "" && (it.Type == "picture" || it.Type == "") {
				atts = append(atts, imbot.InboundAttachment{Kind: "image", ResourceID: it.DownloadCode})
			}
		}
		if text == "" {
			text = strings.TrimSpace(strings.Join(parts, " "))
		}
	case "picture":
		if m.Content.DownloadCode != "" {
			atts = append(atts, imbot.InboundAttachment{Kind: "image", ResourceID: m.Content.DownloadCode})
		}
	case "file":
		if m.Content.DownloadCode != "" {
			atts = append(atts, imbot.InboundAttachment{Kind: "file", ResourceID: m.Content.DownloadCode, Name: strings.TrimSpace(m.Content.FileName)})
		}
	case "audio", "voice":
		if m.Content.DownloadCode != "" {
			atts = append(atts, imbot.InboundAttachment{Kind: "audio", ResourceID: m.Content.DownloadCode})
		}
		// Surface the platform's speech-to-text transcript as the message text so
		// routing/classification has a hook without waiting on the audio download.
		if text == "" {
			text = strings.TrimSpace(m.Content.Recognition)
		}
	case "video":
		if m.Content.DownloadCode != "" {
			atts = append(atts, imbot.InboundAttachment{Kind: "video", ResourceID: m.Content.DownloadCode})
		}
	default:
		// Unknown msgtype: only actionable if it happened to carry text.
	}

	// In a group, DingTalk prepends the literal "@<botname>" the user @-mentioned
	// to the message text. CRUCIAL: an @mention makes DingTalk send the message as
	// msgtype=richText (the mention becomes a rich-text run), so "@牛牛" lands in
	// content.richText[] and is assembled into `text` ABOVE — NOT in text.content.
	// The strip therefore has to run here, AFTER assembly, so it covers both the
	// plain text.content path and the (real-world) richText path. Only the single
	// leading mention is dropped; a later "@human" in the user's own text and audio
	// ASR transcripts (which never start with "@bot") are left intact. 1:1 DMs
	// (conversationType!="2") never carry the prefix, so they are untouched.
	rawText := text
	if m.ConversationType == "2" {
		text = stripLeadingAtMention(text)
	}
	// Diagnostic: raw (pre-strip, post-assembly) vs post-strip text with msgtype/
	// conv_type, so a group @mention that is (or isn't) being stripped is
	// observable. The richText payload previously logged raw="" because assembly
	// hadn't happened yet at the old log point — now it shows the real text.
	slog.Info("dingtalk: inbound text", "msgtype", m.MsgType, "conv_type", m.ConversationType, "raw", truncDing(rawText), "text", truncDing(text))

	if text == "" && len(atts) == 0 {
		return imbot.InboundEvent{}, false
	}
	actor := m.SenderStaffID
	if actor == "" {
		actor = m.SenderID
	}
	ev := imbot.InboundEvent{
		Channel:     imbot.ChannelDingTalk,
		ChatExtID:   m.ConversationID,
		ThreadExtID: "", // DingTalk has no reply-thread on bot messages
		ActorExtID:  actor,
		// MessageExtID encodes the conversation id so the capability methods
		// (Reply/React/RemoveReaction) can target it — DingTalk's robot APIs
		// address a conversation, not a message id, and those methods only receive
		// MessageExtID. FetchResource ignores it (it uses the downloadCode).
		MessageExtID: encodeMsgRef(m.MsgID, m.ConversationID),
		Text:         text,
		Attachments:  atts,
		Kind:         "message",
		EventID:      m.MsgID, // msgId is stable for idempotent dedupe
		// Stash the reply hints so Push can target the same conversation/robot.
		Raw: map[string]any{
			"sessionWebhook":   m.SessionWebhook,
			"conversationType": m.ConversationType,
			"robotCode":        m.RobotCode,
		},
	}
	return ev, true
}

// truncDing returns the first 60 runes of s (with an ellipsis if clipped) for
// diagnostic logging of an inbound message without dumping the whole payload.
func truncDing(s string) string {
	r := []rune(s)
	if len(r) <= 60 {
		return s
	}
	return string(r[:60]) + "…"
}

// stripLeadingAtMention removes the single leading "@<name>" mention DingTalk
// prepends to a group message's text.content when the bot is @-mentioned (e.g.
// "@牛牛 /issues 提交" -> "/issues 提交"). DingTalk inlines the bot's display name
// literally in the text; the atUsers array carries only opaque ids (no names),
// so the name cannot be matched precisely — instead we drop the one leading
// "@<run-of-non-space>" token, which is the bot's own mention (the way a user
// addresses the bot). Any later "@human" that is part of the user's actual
// message is preserved. Group-only: 1:1 DMs never carry the @bot prefix.
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

// msgRefSep joins the DingTalk msgId and conversationId inside InboundEvent.
// MessageExtID. It is the ASCII unit separator (0x1F), which never appears in a
// DingTalk id, so decode is unambiguous.
const msgRefSep = "\x1f"

// encodeMsgRef packs (msgId, conversationId) into the opaque MessageExtID token.
// conversationId is the part the capability methods actually need; msgId is kept
// for symmetry/debuggability. An empty conversationId degrades to the bare msgId.
func encodeMsgRef(msgID, convID string) string {
	if convID == "" {
		return msgID
	}
	return msgID + msgRefSep + convID
}

// decodeMsgRef reverses encodeMsgRef, returning the conversationId a capability
// method should target. A token with no separator (legacy/degenerate) yields an
// empty convID so the caller no-ops rather than mis-targeting.
func decodeMsgRef(ref string) (msgID, convID string) {
	if i := strings.Index(ref, msgRefSep); i >= 0 {
		return ref[:i], ref[i+len(msgRefSep):]
	}
	return ref, ""
}

// parseCardActionCallback is a BEST-EFFORT parser for DingTalk interactive-card
// action callbacks (the permission approve/deny round-trip). DingTalk card
// callbacks don't have a single stable shape testable without live credentials,
// so we scan the two fields observed to carry app payloads — `value` and
// `cardPrivateData` — for our `permission:...` marker. If found, we surface it
// as an action_callback InboundEvent. This is deliberately small and lenient;
// it does not try to model the full card-callback protocol.
func parseCardActionCallback(raw []byte) (imbot.InboundEvent, bool) {
	var m struct {
		Value           string `json:"value"`
		CardPrivateData string `json:"cardPrivateData"`
		ConversationID  string `json:"conversationId"`
		SenderStaffID   string `json:"senderStaffId"`
		SenderID        string `json:"senderId"`
		MsgID           string `json:"msgId"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return imbot.InboundEvent{}, false
	}
	cb := extractPermissionPayload(m.Value)
	if cb == "" {
		cb = extractPermissionPayload(m.CardPrivateData)
	}
	if cb == "" {
		return imbot.InboundEvent{}, false
	}
	actor := m.SenderStaffID
	if actor == "" {
		actor = m.SenderID
	}
	ev := imbot.InboundEvent{
		Channel:      imbot.ChannelDingTalk,
		ChatExtID:    m.ConversationID,
		ActorExtID:   actor,
		Kind:         "action_callback",
		CallbackData: cb,
		EventID:      m.MsgID + ":" + cb,
	}
	return ev, true
}

// extractPermissionPayload returns a `permission:...` payload if s carries one,
// either as the bare string or nested inside a small JSON object under a "cb" /
// "value" key. Empty string means "not our payload".
func extractPermissionPayload(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "permission:") {
		return s
	}
	// Tolerate a JSON-wrapped payload like {"cb":"permission:approve:9"}.
	var wrap map[string]string
	if err := json.Unmarshal([]byte(s), &wrap); err == nil {
		for _, k := range []string{"cb", "value", "callback"} {
			if v := strings.TrimSpace(wrap[k]); strings.HasPrefix(v, "permission:") {
				return v
			}
		}
	}
	return ""
}
