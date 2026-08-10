package telegram

import (
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// This file decodes Telegram Bot API updates into the normalized
// imbot.InboundEvent. Two transports feed the same JSON shape:
//
//   - the getUpdates long-poll (default, LAN-friendly): each element of the
//     result array is a tgUpdate;
//   - the optional public webhook: the HTTP body IS a single tgUpdate.
//
// Both funnel through parseTelegramUpdate, so normalization is
// transport-independent and unit-tested at the JSON layer.

// tgUpdate is the subset of a Telegram Update we act on. update_id is the
// monotonic id used both to advance the getUpdates offset and, stamped into
// EventID, for the service-layer idempotent dedupe.
type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgMessage struct {
	MessageID       int64          `json:"message_id"`
	From            *tgUser        `json:"from"`
	Chat            tgChat         `json:"chat"`
	Text            string         `json:"text"`
	Caption         string         `json:"caption"` // text accompanying a media message
	Photo           []tgPhotoSize  `json:"photo"`   // ascending sizes; last is largest
	Document        *tgFileRef     `json:"document"`
	Voice           *tgFileRef     `json:"voice"`
	Audio           *tgFileRef     `json:"audio"`
	Video           *tgFileRef     `json:"video"`
	VideoNote       *tgFileRef     `json:"video_note"`
	MessageThreadID int64          `json:"message_thread_id"`
	IsTopicMessage  bool           `json:"is_topic_message"`
}

// tgPhotoSize is one entry of a photo message's size array; file_id is the handle
// FetchResource exchanges (via getFile) for a download path.
type tgPhotoSize struct {
	FileID string `json:"file_id"`
}

// tgFileRef is the common shape of the document/voice/audio/video/video_note
// media objects — only the fields we materialize (file_id + optional file name).
type tgFileRef struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgUser struct {
	ID int64 `json:"id"`
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	From    *tgUser    `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

// parseTelegramUpdate normalizes one update into an imbot.InboundEvent. ok=false
// means the update is not an actionable text message or button callback (a
// join/leave, a non-text message, an edited message we don't subscribe to, etc.)
// and should be skipped.
func parseTelegramUpdate(u tgUpdate) (imbot.InboundEvent, bool) {
	switch {
	case u.CallbackQuery != nil:
		return parseCallback(u)
	case u.Message != nil:
		return parseMessage(u)
	default:
		return imbot.InboundEvent{}, false
	}
}

// parseMessage extracts a text or media message. Beyond plain text it now lifts
// photo/document/voice/audio/video/video_note into imbot.InboundAttachments whose
// ResourceID is the Telegram file_id (materialized later via FetchResource); a
// media message's caption becomes the text. Forum-topic messages carry a
// message_thread_id which maps to the second-layer thread routing; ordinary DMs
// and non-topic group messages have no thread (empty ThreadExtID -> active
// pointer + slash commands govern task selection).
func parseMessage(u tgUpdate) (imbot.InboundEvent, bool) {
	m := u.Message
	if m.Chat.ID == 0 {
		return imbot.InboundEvent{}, false
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	atts := mediaAttachments(m)
	if text == "" && len(atts) == 0 {
		return imbot.InboundEvent{}, false // service msg / unsupported (sticker, join)
	}
	ev := imbot.InboundEvent{
		Channel:     imbot.ChannelTelegram,
		ChatExtID:   strconv.FormatInt(m.Chat.ID, 10),
		ThreadExtID: threadExtID(m),
		ActorExtID:  actorID(m.From),
		// MessageExtID encodes chat+message id so Reply/React (reply_parameters /
		// setMessageReaction) can target this exact message. FetchResource ignores
		// it (it uses the attachment's file_id).
		MessageExtID: encodeMsgRef(m.Chat.ID, m.MessageID),
		Text:         text,
		Attachments:  atts,
		Kind:         "message",
		EventID:      "u" + strconv.FormatInt(u.UpdateID, 10),
	}
	return ev, true
}

// mediaAttachments lifts each media object carried by a message into a normalized
// imbot.InboundAttachment. A photo arrives as an ascending size array — the last
// (largest) entry is taken. Every kind is addressed by its file_id.
func mediaAttachments(m *tgMessage) []imbot.InboundAttachment {
	var atts []imbot.InboundAttachment
	if n := len(m.Photo); n > 0 && m.Photo[n-1].FileID != "" {
		atts = append(atts, imbot.InboundAttachment{Kind: "image", ResourceID: m.Photo[n-1].FileID})
	}
	if m.Document != nil && m.Document.FileID != "" {
		atts = append(atts, imbot.InboundAttachment{Kind: "file", ResourceID: m.Document.FileID, Name: strings.TrimSpace(m.Document.FileName)})
	}
	if m.Voice != nil && m.Voice.FileID != "" {
		atts = append(atts, imbot.InboundAttachment{Kind: "audio", ResourceID: m.Voice.FileID})
	}
	if m.Audio != nil && m.Audio.FileID != "" {
		atts = append(atts, imbot.InboundAttachment{Kind: "audio", ResourceID: m.Audio.FileID, Name: strings.TrimSpace(m.Audio.FileName)})
	}
	if m.Video != nil && m.Video.FileID != "" {
		atts = append(atts, imbot.InboundAttachment{Kind: "video", ResourceID: m.Video.FileID})
	}
	if m.VideoNote != nil && m.VideoNote.FileID != "" {
		atts = append(atts, imbot.InboundAttachment{Kind: "video", ResourceID: m.VideoNote.FileID})
	}
	return atts
}

// msgRefSep joins the Telegram chat id and message id inside InboundEvent.
// MessageExtID. It is the ASCII unit separator (0x1F), absent from the numeric
// ids, so decode is unambiguous.
const msgRefSep = "\x1f"

// encodeMsgRef packs (chatID, messageID) into the opaque MessageExtID token so the
// capability methods can address the message. A zero messageID degrades to a bare
// chat id (Reply/React then no-op, since both parts are required).
func encodeMsgRef(chatID, messageID int64) string {
	if messageID == 0 {
		return strconv.FormatInt(chatID, 10)
	}
	return strconv.FormatInt(chatID, 10) + msgRefSep + strconv.FormatInt(messageID, 10)
}

// decodeMsgRef reverses encodeMsgRef, returning the chat id and message id a
// capability method targets. A token with no separator yields an empty messageID
// so the caller no-ops rather than mis-targeting.
func decodeMsgRef(ref string) (chatID, messageID string) {
	if i := strings.Index(ref, msgRefSep); i >= 0 {
		return ref[:i], ref[i+len(msgRefSep):]
	}
	return ref, ""
}

// parseCallback extracts an inline-keyboard button click (the permission
// approve/deny callback) into an action_callback InboundEvent.
func parseCallback(u tgUpdate) (imbot.InboundEvent, bool) {
	cq := u.CallbackQuery
	if strings.TrimSpace(cq.Data) == "" || cq.Message == nil || cq.Message.Chat.ID == 0 {
		return imbot.InboundEvent{}, false
	}
	ev := imbot.InboundEvent{
		Channel:      imbot.ChannelTelegram,
		ChatExtID:    strconv.FormatInt(cq.Message.Chat.ID, 10),
		ThreadExtID:  threadExtID(cq.Message),
		ActorExtID:   actorID(cq.From),
		Kind:         "action_callback",
		CallbackData: cq.Data,
		EventID:      "u" + strconv.FormatInt(u.UpdateID, 10),
	}
	return ev, true
}

// threadExtID returns the forum-topic thread id as a string, or "" for messages
// that are not part of a topic (the general chat / plain DMs).
func threadExtID(m *tgMessage) string {
	if m.IsTopicMessage && m.MessageThreadID != 0 {
		return strconv.FormatInt(m.MessageThreadID, 10)
	}
	return ""
}

func actorID(u *tgUser) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}
