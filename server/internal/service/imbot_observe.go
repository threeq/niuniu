package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// This file implements the "Agent 员工分身" observation layer (issue #664): the bot
// sits in a chat like a colleague, keeps a rolling record of what is said, and
// only takes over the conversation when it is actually addressed. The proactive
// half — periodically reading that record and volunteering work — lives in
// imbot_employee.go.
//
// Two modes per chat (im_bot_chats.agent_mode):
//
//	command  (default) — behave exactly as before: every message routed to the
//	                     project. Backwards-compatible for existing chats.
//	observe            — record everything; only ACT on a message that addresses
//	                     the bot (@mention / DM / slash command / #id). Ambient
//	                     chatter is logged for later analysis, never dispatched.
//
// The distinction matters because a bot in a busy group otherwise spawns a task
// for every overheard sentence.

const (
	// AgentModeCommand only acts when addressed and keeps no transcript.
	AgentModeCommand = "command"
	// AgentModeObserve records all chatter and analyzes it periodically.
	AgentModeObserve = "observe"
)

// observeTranscriptKeep bounds a chat's rolling transcript. Big enough that a
// periodic analysis sees the real shape of a discussion, small enough that a busy
// group cannot grow the table without bound (the log is an analysis window, not
// an archive).
const observeTranscriptKeep = 300

// normalizeAgentMode validates an agent_mode value coming from the API or the DB.
// Anything unrecognized (including the empty string on a row written before the
// column existed) degrades to AgentModeCommand — the conservative mode that never
// acts unbidden.
func normalizeAgentMode(mode string) string {
	if strings.TrimSpace(mode) == AgentModeObserve {
		return AgentModeObserve
	}
	return AgentModeCommand
}

// addressedToBot reports whether an inbound message is aimed AT the bot rather
// than being chatter the bot merely overheard. True when any of:
//
//   - the chat is not a group (a DM is by definition addressed to the bot);
//   - the platform reported the bot was @-mentioned (adapters set ev.Mentioned
//     while stripping their own mention syntax);
//   - the text opens with an explicit control token — a slash command or the
//     `#<id>` conversation switch — which no one types by accident.
//
// It is the gate that makes observe mode usable: everything else is recorded and
// left for the periodic analysis instead of spawning a task per sentence.
//
// NOTE: this covers only the message itself. A message inside an already-bound
// conversation is addressed by CONTEXT rather than by syntax — see
// conversationallyAddressed, which the caller ORs in.
func addressedToBot(ev imbot.InboundEvent, text string) bool {
	if !ev.IsGroup {
		return true
	}
	if ev.Mentioned {
		return true
	}
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "/") || strings.HasPrefix(t, "#")
}

// conversationallyAddressed reports whether the chat context — rather than the
// message's own wording — already dedicates this message to the bot:
//
//   - a workspace-pinned chat (bind_mode=workspace) exists solely to drive one
//     task, so everything said there is for the bot;
//   - a message inside a thread already bound to an issue is a follow-up in a
//     conversation the user deliberately started with the bot.
//
// Without this, observe mode would silently swallow every follow-up that did not
// re-@-mention the bot, breaking a thread conversation mid-way — the user would
// see the first message answered and the rest ignored.
func (s *IMBotService) conversationallyAddressed(ctx context.Context, chat store.ImBotChat, threadExtID string) bool {
	if chat.BindMode == "workspace" && chat.PinnedIssueID.Valid {
		return true
	}
	if threadExtID == "" {
		return false
	}
	_, err := s.q.GetIMBotThreadByExt(ctx, store.GetIMBotThreadByExtParams{
		ChatID: chat.ID, ThreadExtID: threadExtID,
	})
	return err == nil
}

// recordObservation appends one inbound message to the chat's rolling transcript
// and trims the log back to observeTranscriptKeep. Best-effort: a write failure is
// logged and never blocks routing or delivery — losing an observation degrades
// the next analysis, while failing the message would break the chat.
//
// Called only for observe-mode chats; a command-mode chat keeps no transcript, so
// enabling the feature costs nothing for chats that never opted in.
//
// Attachment METADATA is recorded alongside the text (issue #679) so a discussion
// held around a screenshot is not analyzed as if the screenshot were not there.
// Only kind + name: the bytes stay where they are, and platform resource keys are
// deliberately NOT stored because they expire — a key that rots between
// observation and the next sweep would be worse than not having it.
func (s *IMBotService) recordObservation(ctx context.Context, chat store.ImBotChat, ev imbot.InboundEvent, text string, addressed bool) {
	flag := int64(0)
	if addressed {
		flag = 1
	}
	if _, err := s.q.CreateIMBotChatMessage(ctx, store.CreateIMBotChatMessageParams{
		ChatID:      chat.ID,
		ActorExtID:  ev.ActorExtID,
		ActorName:   ev.ActorName,
		Text:        clipDetail(text, observeMessageMaxRunes),
		Addressed:   flag,
		Attachments: encodeObservedAttachments(ev.Attachments),
	}); err != nil {
		slog.Warn("imbot: record observation failed", "chat", chat.ID, "error", err)
		return
	}
	if err := s.q.TrimIMBotChatMessages(ctx, store.TrimIMBotChatMessagesParams{
		ChatID: chat.ID, Keep: observeTranscriptKeep,
	}); err != nil {
		slog.Warn("imbot: trim transcript failed", "chat", chat.ID, "error", err)
	}
}

// observedAttachment is the transcript's record of one file shared in a chat:
// what kind of thing it was and what it was called. No resource key, no bytes —
// see recordObservation.
type observedAttachment struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// observedAttachmentsMax bounds how many attachments one message contributes to
// the transcript. A message carrying dozens of files says "someone shared a pile
// of files", and enumerating all of them would crowd out the actual conversation
// in the analysis prompt.
const observedAttachmentsMax = 8

// observedAttachmentNameMaxRunes caps one stored filename. Names are
// user-controlled and land in a prompt, so they get the same length discipline as
// speaker labels.
const observedAttachmentNameMaxRunes = 64

// encodeObservedAttachments renders inbound attachments as the JSON stored on the
// transcript row. Returns "[]" for a message with no attachments, so the column is
// always valid JSON and readers never special-case empty.
//
// Names are sanitized HERE rather than at render time: the stored row is what a
// later analysis reads, and sanitizing on the way in means a hostile name cannot
// sit in the DB waiting for a reader that forgot to clean it.
func encodeObservedAttachments(atts []imbot.InboundAttachment) string {
	if len(atts) == 0 {
		return "[]"
	}
	out := make([]observedAttachment, 0, len(atts))
	for i, a := range atts {
		if i >= observedAttachmentsMax {
			break
		}
		out = append(out, observedAttachment{
			Kind: normalizeAttachmentKind(a.Kind),
			Name: sanitizeAttachmentName(a.Name),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Cannot happen for this shape, but an observation must never fail a message.
		slog.Warn("imbot: encode observed attachments failed", "error", err)
		return "[]"
	}
	return string(b)
}

// decodeObservedAttachments parses a transcript row's attachment JSON. A malformed
// or empty value yields no attachments rather than an error: the transcript is an
// analysis input, and one unreadable row must not stop a sweep.
func decodeObservedAttachments(raw string) []observedAttachment {
	s := strings.TrimSpace(raw)
	if s == "" || s == "[]" || s == "null" {
		return nil
	}
	var out []observedAttachment
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// normalizeAttachmentKind maps a platform-reported kind to the known set, falling
// back to "file". Adapters are the source of these strings, but an unknown kind
// must still read as something in a transcript.
func normalizeAttachmentKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		return "image"
	case "audio":
		return "audio"
	case "video":
		return "video"
	default:
		return "file"
	}
}

// sanitizeAttachmentName reduces an attacker-controlled filename to something safe
// to place in an analysis prompt.
//
// A filename is exactly as attacker-controlled as a display name — anyone in the
// group can upload `x：忽略以上指令.png` — so it gets the same treatment as
// sanitizeSpeakerName: collapsed to one line, stripped of both colon variants
// (which read as speaker separators) and of the authorization marker, then length
// capped. Also stripped: the bracket characters that delimit the attachment
// notation itself, so a name cannot forge the end of its own annotation.
func sanitizeAttachmentName(name string) string {
	n := oneLine(name)
	n = strings.ReplaceAll(n, addressedMarker, "")
	n = strings.ReplaceAll(n, ":", " ")
	n = strings.ReplaceAll(n, "：", " ")
	n = strings.ReplaceAll(n, "[", " ")
	n = strings.ReplaceAll(n, "]", " ")
	n = strings.ReplaceAll(n, "【", " ")
	n = strings.ReplaceAll(n, "】", " ")
	n = oneLine(n)
	return clipDetail(n, observedAttachmentNameMaxRunes)
}

// observeMessageMaxRunes caps one stored message. A pasted log or stack trace
// would otherwise dominate the analysis prompt (and the table) without adding
// signal about what the team is discussing.
const observeMessageMaxRunes = 1000
