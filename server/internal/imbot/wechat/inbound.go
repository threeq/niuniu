package wechat

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// This file normalizes an inbound iLink weixinMessage into imbot.InboundEvent.
// The protocol is DM-oriented: from_user_id identifies the human and doubles as
// the chat id (a reply's to_user_id is that same id). Media items are not
// inlined — each carries a CDN reference that FetchResource downloads and
// AES-128-ECB decrypts lazily, so the reference (query param + key + url) is
// packed into InboundAttachment.ResourceID as an opaque JSON token.

// mediaRef is the self-contained descriptor packed into an
// InboundAttachment.ResourceID. It holds everything FetchResource needs to pull
// and decrypt one CDN object without re-reading the original message.
type mediaRef struct {
	EncryptQueryParam string `json:"q,omitempty"`
	FullURL           string `json:"u,omitempty"`
	// AESKeyB64 is the key already normalized to the base64 encoding parseAESKey
	// expects (image_item.aeskey hex is converted at parse time). Empty means the
	// object is unencrypted (plain CDN download).
	AESKeyB64 string `json:"k,omitempty"`
	FileName  string `json:"n,omitempty"`
}

func encodeMediaRef(r mediaRef) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func decodeMediaRef(s string) (mediaRef, bool) {
	var r mediaRef
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return mediaRef{}, false
	}
	return r, true
}

// parseInbound converts one getupdates message into an imbot.InboundEvent.
// ok=false skips non-actionable messages (our own echoes, empties). The
// returned contextToken is stored by the caller so replies can echo it.
func parseInbound(m *weixinMessage) (ev imbot.InboundEvent, contextToken string, ok bool) {
	if m == nil || m.FromUserID == "" {
		return imbot.InboundEvent{}, "", false
	}
	// Ignore anything the bot itself authored (message_type BOT) — getupdates can
	// replay our own sends. Only human (USER) messages drive the agent.
	if m.MessageType == msgTypeBot {
		return imbot.InboundEvent{}, "", false
	}
	text := bodyFromItems(m.ItemList)
	atts := mediaAttachments(m.ItemList)
	if text == "" && len(atts) == 0 {
		return imbot.InboundEvent{}, "", false
	}
	ev = imbot.InboundEvent{
		Channel:   imbot.ChannelWechat,
		ChatExtID: m.FromUserID,
		// ThreadExtID: iLink personal bots are 1:1 DMs with no topic threads.
		ActorExtID: m.FromUserID,
		// MessageExtID is the user id: React/typing and Reply both target the
		// conversation with this user (the protocol has no per-message reply
		// anchor for personal bots).
		MessageExtID: m.FromUserID,
		Text:         text,
		Attachments:  atts,
		Kind:         "message",
		EventID:      eventID(m),
	}
	return ev, m.ContextToken, true
}

// eventID is the stable idempotency key for a message (im_bot_inbox dedupe).
// message_id is preferred; seq is the fallback when a message carries none.
func eventID(m *weixinMessage) string {
	if m.MessageID != 0 {
		return "m" + strconv.FormatInt(m.MessageID, 10)
	}
	if m.Seq != 0 {
		return "s" + strconv.FormatInt(m.Seq, 10)
	}
	return ""
}

// bodyFromItems extracts the plain-text body. It mirrors the SDK: the first
// TEXT item wins; a quoted (ref_msg) message is prefixed as "[引用: ...]"; a
// voice item with a server transcript contributes that transcript.
func bodyFromItems(items []*messageItem) string {
	for _, it := range items {
		if it == nil {
			continue
		}
		if it.Type == itemTypeText && it.TextItem != nil && it.TextItem.Text != "" {
			text := it.TextItem.Text
			ref := it.RefMsg
			if ref == nil {
				return text
			}
			// Quoted media is delivered as an attachment; keep only the new text.
			if ref.MessageItem != nil && isMediaItem(ref.MessageItem) {
				return text
			}
			var parts []string
			if ref.Title != "" {
				parts = append(parts, ref.Title)
			}
			if ref.MessageItem != nil {
				if rb := bodyFromItems([]*messageItem{ref.MessageItem}); rb != "" {
					parts = append(parts, rb)
				}
			}
			if len(parts) == 0 {
				return text
			}
			return "[引用: " + strings.Join(parts, " | ") + "]\n" + text
		}
		if it.Type == itemTypeVoice && it.VoiceItem != nil && it.VoiceItem.Text != "" {
			return it.VoiceItem.Text
		}
	}
	return ""
}

func isMediaItem(it *messageItem) bool {
	switch it.Type {
	case itemTypeImage, itemTypeVoice, itemTypeFile, itemTypeVideo:
		return true
	}
	return false
}

// mediaAttachments lifts each media item into an imbot.InboundAttachment whose
// ResourceID is the packed mediaRef. Types map to the framework's four Kinds.
func mediaAttachments(items []*messageItem) []imbot.InboundAttachment {
	var atts []imbot.InboundAttachment
	for _, it := range items {
		if it == nil {
			continue
		}
		switch it.Type {
		case itemTypeImage:
			if it.ImageItem == nil {
				continue
			}
			ref, ok := imageRef(it.ImageItem)
			if !ok {
				continue
			}
			atts = append(atts, imbot.InboundAttachment{Kind: "image", ResourceID: encodeMediaRef(ref)})
		case itemTypeVoice:
			if it.VoiceItem == nil || it.VoiceItem.Media == nil {
				continue
			}
			ref, ok := mediaRefFrom(it.VoiceItem.Media, "")
			if !ok {
				continue
			}
			atts = append(atts, imbot.InboundAttachment{Kind: "audio", ResourceID: encodeMediaRef(ref)})
		case itemTypeFile:
			if it.FileItem == nil || it.FileItem.Media == nil {
				continue
			}
			ref, ok := mediaRefFrom(it.FileItem.Media, it.FileItem.FileName)
			if !ok {
				continue
			}
			atts = append(atts, imbot.InboundAttachment{Kind: "file", ResourceID: encodeMediaRef(ref), Name: it.FileItem.FileName})
		case itemTypeVideo:
			if it.VideoItem == nil || it.VideoItem.Media == nil {
				continue
			}
			ref, ok := mediaRefFrom(it.VideoItem.Media, "")
			if !ok {
				continue
			}
			atts = append(atts, imbot.InboundAttachment{Kind: "video", ResourceID: encodeMediaRef(ref)})
		}
	}
	return atts
}

// imageRef prefers image_item.aeskey (hex) over media.aes_key, converting the
// hex form to the base64 encoding parseAESKey consumes.
func imageRef(img *imageItem) (mediaRef, bool) {
	if img.Media == nil {
		return mediaRef{}, false
	}
	keyB64 := img.Media.AESKey
	if img.AESKey != "" {
		if raw, err := hex.DecodeString(img.AESKey); err == nil {
			keyB64 = base64.StdEncoding.EncodeToString(raw)
		}
	}
	return mediaRefWithKey(img.Media, keyB64, "")
}

func mediaRefFrom(m *cdnMedia, fileName string) (mediaRef, bool) {
	return mediaRefWithKey(m, m.AESKey, fileName)
}

func mediaRefWithKey(m *cdnMedia, keyB64, fileName string) (mediaRef, bool) {
	if m.EncryptQueryParam == "" && m.FullURL == "" {
		return mediaRef{}, false
	}
	return mediaRef{
		EncryptQueryParam: m.EncryptQueryParam,
		FullURL:           m.FullURL,
		AESKeyB64:         keyB64,
		FileName:          fileName,
	}, true
}
