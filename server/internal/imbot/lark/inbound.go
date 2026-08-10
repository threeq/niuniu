package lark

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// This file decodes Feishu/Lark inbound events into the normalized
// imbot.InboundEvent. Two transports feed the same JSON envelope:
//
//   - the stream long connection (default, LAN-friendly): a protobuf pbbp2.Frame
//     wraps the event JSON in its payload (decodeFrame below);
//   - the optional public webhook: the HTTP body IS the event JSON.
//
// Both funnel through parseLarkEventJSON, so the normalization logic is
// transport-independent and unit-tested at the JSON layer (which is exactly what
// the design's replay smoke test exercises).

// larkEventEnvelope is the v2 (schema 2.0) event envelope shared by the webhook
// body and the stream frame payload.
type larkEventEnvelope struct {
	Schema string `json:"schema"`
	Header struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Token     string `json:"token"`
	} `json:"header"`
	Event json.RawMessage `json:"event"`
	// URL-verification handshake fields (present only on the challenge request).
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
}

// parseLarkEventJSON normalizes a Feishu event JSON envelope into an
// imbot.InboundEvent. ok=false means the payload is not an actionable
// message/card event (URL challenge, unsupported type, control frame, etc.) and
// should be skipped. Errors are only for malformed JSON.
func parseLarkEventJSON(raw []byte) (imbot.InboundEvent, bool, error) {
	raw = trimSpaceBytes(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return imbot.InboundEvent{}, false, nil // control frame / non-JSON
	}
	var env larkEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return imbot.InboundEvent{}, false, err
	}
	// URL verification is handled by Challenge(), not here.
	if env.Type == "url_verification" {
		return imbot.InboundEvent{}, false, nil
	}

	switch env.Header.EventType {
	case "im.message.receive_v1":
		return parseMessageEvent(env)
	case "card.action.trigger":
		return parseCardActionEvent(env)
	default:
		return imbot.InboundEvent{}, false, nil
	}
}

// parseMessageEvent extracts a text message into an InboundEvent.
func parseMessageEvent(env larkEventEnvelope) (imbot.InboundEvent, bool, error) {
	var e struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			ChatID      string `json:"chat_id"`
			ThreadID    string `json:"thread_id"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
			// Mentions carries the @-mention placeholders that appear inline in the
			// text as keys like "@_user_1" / "@_all". In a group the bot only gets
			// messages that @-mention it, so the text always leads with its own
			// placeholder; we strip these keys so the agent (and slash-command /
			// #id parsing) sees the real message, not "@_user_1 /issues".
			Mentions []struct {
				Key string `json:"key"`
			} `json:"mentions"`
		} `json:"message"`
	}
	if err := json.Unmarshal(env.Event, &e); err != nil {
		return imbot.InboundEvent{}, false, err
	}
	if e.Message.ChatID == "" {
		return imbot.InboundEvent{}, false, nil
	}
	// Normalize the common message types into text + attachment references.
	// Unsupported types (sticker, share_chat, ...) decode to no text and no
	// attachments and are skipped (the caller's dedupe still acks them).
	text, atts := extractMessageContent(e.Message.MessageType, e.Message.Content)
	if len(e.Message.Mentions) > 0 {
		keys := make([]string, 0, len(e.Message.Mentions))
		for _, m := range e.Message.Mentions {
			keys = append(keys, m.Key)
		}
		text = stripMentionKeys(text, keys)
	}
	if strings.TrimSpace(text) == "" && len(atts) == 0 {
		return imbot.InboundEvent{}, false, nil
	}
	ev := imbot.InboundEvent{
		Channel:      imbot.ChannelLark,
		ChatExtID:    e.Message.ChatID,
		ThreadExtID:  e.Message.ThreadID,
		ActorExtID:   e.Sender.SenderID.OpenID,
		MessageExtID: e.Message.MessageID,
		Text:         text,
		Attachments:  atts,
		Kind:         "message",
		EventID:      eventID(env, e.Message.MessageID),
	}
	return ev, true, nil
}

// extractMessageContent normalizes a Feishu message's (type, content-JSON) into
// plain text plus attachment references. Supported types: text, post (rich
// text/图文 — title+text extracted, inline images collected), image, file,
// audio, media (video). Unknown types yield ("", nil) so the caller skips them.
func extractMessageContent(msgType, content string) (string, []imbot.InboundAttachment) {
	switch msgType {
	case "", "text":
		return extractTextContent(content), nil
	case "post":
		return extractPostContent(content)
	case "image":
		var c struct {
			ImageKey string `json:"image_key"`
		}
		_ = json.Unmarshal([]byte(content), &c)
		if c.ImageKey == "" {
			return "", nil
		}
		return "", []imbot.InboundAttachment{{Kind: "image", ResourceID: c.ImageKey}}
	case "file", "audio", "media":
		var c struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		_ = json.Unmarshal([]byte(content), &c)
		if c.FileKey == "" {
			return "", nil
		}
		kind := "file"
		if msgType == "audio" {
			kind = "audio"
		} else if msgType == "media" {
			kind = "video"
		}
		return "", []imbot.InboundAttachment{{Kind: kind, ResourceID: c.FileKey, Name: c.FileName}}
	default:
		return "", nil
	}
}

// extractPostContent flattens a Feishu post (rich text) message into plain text
// (title + the text/link segments, joined line-per-paragraph) and collects any
// inline images as attachments. The post content shape is
// {"title":"..","content":[[{"tag":"text","text":".."},{"tag":"img","image_key":".."}], ...]}.
func extractPostContent(content string) (string, []imbot.InboundAttachment) {
	var post struct {
		Title   string `json:"title"`
		Content [][]struct {
			Tag      string `json:"tag"`
			Text     string `json:"text"`
			ImageKey string `json:"image_key"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &post); err != nil {
		return "", nil
	}
	var b strings.Builder
	var atts []imbot.InboundAttachment
	if s := strings.TrimSpace(post.Title); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	for _, para := range post.Content {
		for _, seg := range para {
			switch seg.Tag {
			case "text", "a", "md":
				b.WriteString(seg.Text)
			case "img":
				if seg.ImageKey != "" {
					atts = append(atts, imbot.InboundAttachment{Kind: "image", ResourceID: seg.ImageKey})
				}
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), atts
}

// parseCardActionEvent extracts an interactive-card button click (the permission
// approve/deny callback) into an action_callback InboundEvent.
func parseCardActionEvent(env larkEventEnvelope) (imbot.InboundEvent, bool, error) {
	var e struct {
		Operator struct {
			OpenID string `json:"open_id"`
		} `json:"operator"`
		Action struct {
			Value map[string]any `json:"value"`
		} `json:"action"`
		Context struct {
			OpenChatID    string `json:"open_chat_id"`
			OpenMessageID string `json:"open_message_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(env.Event, &e); err != nil {
		return imbot.InboundEvent{}, false, err
	}
	cb, _ := e.Action.Value["cb"].(string)
	if strings.TrimSpace(cb) == "" {
		return imbot.InboundEvent{}, false, nil
	}
	ev := imbot.InboundEvent{
		Channel:      imbot.ChannelLark,
		ChatExtID:    e.Context.OpenChatID,
		ActorExtID:   e.Operator.OpenID,
		Kind:         "action_callback",
		CallbackData: cb,
		EventID:      eventID(env, e.Context.OpenMessageID+":"+cb),
	}
	return ev, true, nil
}

// eventID prefers the platform header event_id (stable for dedupe across
// redeliveries) and falls back to a per-message id when absent.
func eventID(env larkEventEnvelope, fallback string) string {
	if env.Header.EventID != "" {
		return env.Header.EventID
	}
	return fallback
}

// extractTextContent pulls the plain text out of a Lark text message's content
// ({"text":"..."} JSON). Returns the raw string if it is not JSON-wrapped.
func extractTextContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	var wrap struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &wrap); err == nil && wrap.Text != "" {
		return strings.TrimSpace(wrap.Text)
	}
	return content
}

// stripMentionKeys removes the Feishu @-mention placeholder tokens (e.g.
// "@_user_1", "@_all") from a message's text and tidies the whitespace those
// tokens leave behind, preserving line breaks. Feishu inlines these keys in the
// text string of a text message while the real display names live in the
// mentions array; leaving them in pollutes the message and, worse, pushes a
// leading slash command / #id off the front so it is no longer recognized.
func stripMentionKeys(text string, keys []string) string {
	for _, k := range keys {
		if k != "" {
			text = strings.ReplaceAll(text, k, "")
		}
	}
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		// Fields collapses the runs of spaces/tabs left where a token was removed.
		lines[i] = strings.Join(strings.Fields(ln), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\n' || b[0] == '\r' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 {
		last := b[len(b)-1]
		if last == ' ' || last == '\n' || last == '\r' || last == '\t' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

// --- stream frame decoding (pbbp2.Frame) ---

// larkFrame is the decoded subset of the Feishu long-connection pbbp2.Frame we
// need: the header key/values and the payload (the event JSON). Field numbers
// match the Lark WS protocol (SeqID=1, LogID=2, service=3, method=4, headers=5,
// payloadEncoding=6, payloadType=7, payload=8).
type larkFrame struct {
	Method  int32
	Headers map[string]string
	Payload []byte
}

// decodeFrame parses a raw pbbp2.Frame protobuf message. It is tolerant: unknown
// fields are skipped so protocol additions don't break decoding.
func decodeFrame(b []byte) (larkFrame, error) {
	f := larkFrame{Headers: map[string]string{}}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return f, fmt.Errorf("lark: bad frame tag")
		}
		b = b[n:]
		switch {
		case num == 4 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return f, fmt.Errorf("lark: bad method")
			}
			f.Method = int32(v)
			b = b[m:]
		case num == 5 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return f, fmt.Errorf("lark: bad header")
			}
			if k, val, ok := decodeHeader(v); ok {
				f.Headers[k] = val
			}
			b = b[m:]
		case num == 8 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return f, fmt.Errorf("lark: bad payload")
			}
			f.Payload = append([]byte(nil), v...)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return f, fmt.Errorf("lark: bad field %d", num)
			}
			b = b[m:]
		}
	}
	return f, nil
}

// decodeHeader parses a pbbp2.Header{key=1, value=2} nested message.
func decodeHeader(b []byte) (key, value string, ok bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", "", false
		}
		b = b[n:]
		if typ != protowire.BytesType {
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return "", "", false
			}
			b = b[m:]
			continue
		}
		v, m := protowire.ConsumeBytes(b)
		if m < 0 {
			return "", "", false
		}
		switch num {
		case 1:
			key = string(v)
		case 2:
			value = string(v)
		}
		b = b[m:]
	}
	return key, value, key != ""
}
