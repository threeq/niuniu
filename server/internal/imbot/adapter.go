// Package imbot is the self-contained IM remote-channel capability (Epic #555).
//
// It defines a single ChannelAdapter abstraction plus per-channel adapters
// (lark/, later dingtalk/telegram/wework) that each implement an OUTBOUND long
// connection (Connect) + message Push. The outbound connection model — the bot
// dials out to the platform (Lark WS / TG long-poll / DingTalk Stream) rather
// than exposing a public webhook — is what makes niuniu usable behind a
// LAN/NAT with no public IP (the core constraint from the design spec).
//
// This package is deliberately NOT coupled to internal/integration/ (that is
// the GitHub/Jira/TAPD issue-tracker stack, unrelated to IM). Credentials
// arrive already decrypted (see Credential); decryption happens only in the
// service layer, never here.
package imbot

import (
	"context"
	"errors"
	"net/http"
)

// ErrWebhookUnauthorized is returned by an adapter's VerifyWebhook when the
// platform-native signature check fails (bad msg_signature / secret_token /
// sign). The service maps it to an HTTP 401 so a forged webhook is rejected,
// distinct from a merely malformed/non-actionable body (acknowledged 200 so the
// platform stops retrying). This is the webhook-mode hardening from design §8.
var ErrWebhookUnauthorized = errors.New("imbot: webhook unauthorized")

// ChannelType identifies an IM platform.
type ChannelType string

const (
	ChannelLark     ChannelType = "lark"
	ChannelDingTalk ChannelType = "dingtalk"
	ChannelTelegram ChannelType = "telegram"
	ChannelWework   ChannelType = "wework"
	// ChannelWechat is the WeChat 微信ClawBot (Tencent openclaw-weixin / iLink)
	// personal-account bot. Unlike the app-credential channels above, its
	// bot_token is minted by a QR-scan login flow; the adapter carries the full
	// iLink protocol (getupdates long-poll, sendmessage, media CDN + AES-128-ECB,
	// sendtyping, getconfig).
	ChannelWechat ChannelType = "wechat"
)

// InboundEvent is the normalized shape every adapter produces for an inbound
// message or interaction, regardless of platform. ChannelID is stamped by the
// ConnectorManager (adapters do not know their DB row id).
type InboundEvent struct {
	ChannelID    int64       // filled by ConnectorManager, not the adapter
	Channel      ChannelType // platform type
	ChatExtID    string      // external id of the group/DM
	ThreadExtID  string      // topic/reply-thread id (empty if none)
	ActorExtID   string      // sender external id
	MessageExtID string      // external id of THIS message (for reactions/replies; empty for callbacks)
	Text         string      // plain text (post title+text extracted for rich-text messages)
	Attachments  []InboundAttachment // images/files/audio/video carried by the message
	Kind         string      // "message" | "action_callback"
	CallbackData string      // interaction-button payload (e.g. permission:approve:<reqID>)
	EventID      string      // platform event id, for idempotent dedupe
	Raw          map[string]any
}

// InboundAttachment references a media/file resource carried by an inbound
// message. The bytes are NOT inlined — platforms hand back an opaque resource
// key that must be downloaded separately (MessageResourceFetcher), which the
// service does lazily when it materializes the message for the agent.
type InboundAttachment struct {
	Kind       string // "image" | "file" | "audio" | "video"
	ResourceID string // platform resource key (Lark image_key / file_key)
	Name       string // original filename when the platform provides one
}

// Button is an interactive action rendered on an outbound card.
type Button struct {
	Label string // user-visible label
	Value string // callback payload echoed back as InboundEvent.CallbackData
}

// OutboundMessage is the normalized shape the dispatcher hands to an adapter to
// Push. ThreadExtID, when set, targets the corresponding thread/topic.
type OutboundMessage struct {
	ChatExtID   string
	ThreadExtID string
	Text        string
	Buttons     []Button
}

// InboundHandler receives every normalized inbound event.
type InboundHandler func(ctx context.Context, ev InboundEvent)

// Credential is the decrypted credential handed to an adapter. It must never
// escape the service layer's decryption boundary into a DTO or a log line.
type Credential struct {
	Channel ChannelType
	// Config holds channel-specific secrets:
	//   lark:     {app_id, app_secret, [base_url]}
	//   telegram: {bot_token}
	Config map[string]any
}

// String returns a redacted string so an accidental %v/%s never leaks secrets.
func (c Credential) String() string { return "imbot.Credential{channel=" + string(c.Channel) + ",<redacted>}" }

// ChannelAdapter is the per-platform capability. Connect + Push are the
// LAN-usable outbound path; VerifyWebhook + Challenge back the optional public
// webhook mode.
type ChannelAdapter interface {
	Type() ChannelType

	// Connect establishes the outbound long connection (Lark WS / TG long-poll
	// / DingTalk Stream) and invokes handler for every inbound event. It blocks
	// until ctx is cancelled or the connection fails unrecoverably; the
	// ConnectorManager owns backoff+reconnect. This is the key to working
	// behind a LAN/NAT with no public URL.
	Connect(ctx context.Context, cred Credential, handler InboundHandler) error

	// Push sends a message over the outbound connection (no public reachability
	// required).
	Push(ctx context.Context, cred Credential, msg OutboundMessage) error

	// --- optional public webhook mode only ---
	VerifyWebhook(r *http.Request, cred Credential) (InboundEvent, error)
	Challenge(r *http.Request) (bodyEcho []byte, isChallenge bool)
}

// CredentialVerifier is an optional capability an adapter may implement so the
// service's "test connectivity" action can validate credentials without
// sending a real message (e.g. Lark mints a tenant_access_token).
type CredentialVerifier interface {
	VerifyCredential(ctx context.Context, cred Credential) error
}

// Reaction is a platform-neutral, semantic marker the service asks an adapter to
// attach to an inbound message. Each adapter maps it to its own platform emoji
// (e.g. Lark's fixed emoji_type enum). Keeping it semantic — not a raw emoji —
// lets the service stay channel-agnostic while each platform picks the right key.
type Reaction string

// ReactionProcessing marks the inbound message that 牛牛 has picked up and is now
// working on (the "正在执行中" marker). Lark renders it as the 牛 emoji (AWESOMEN).
const ReactionProcessing Reaction = "processing"

// MessageReactor is an optional capability: an adapter that can attach an emoji
// reaction to a specific inbound message. The service uses it to mark a message
// as being processed. Adapters whose platform has no reaction API simply do not
// implement it, and the service silently skips the marker.
type MessageReactor interface {
	// React attaches reaction to the message identified by messageExtID and
	// returns the platform reaction id (needed to remove it later; may be empty
	// when the platform doesn't return one). It is best-effort: a failure is
	// logged by the caller but never blocks routing.
	React(ctx context.Context, cred Credential, messageExtID string, reaction Reaction) (reactionID string, err error)

	// RemoveReaction deletes a previously-added reaction (by the id React
	// returned) — used to clear the "正在处理" marker once the message is done.
	RemoveReaction(ctx context.Context, cred Credential, messageExtID, reactionID string) error
}

// MessageReplier is an optional capability: an adapter that can post a text
// reply anchored to a specific inbound message. The service uses it to mark a
// message with the task that picked it up (`#<id> <标题>`), since platforms like
// Feishu allow no arbitrary text label on another user's message — a quoted
// reply is the way to attach visible text to it.
type MessageReplier interface {
	Reply(ctx context.Context, cred Credential, messageExtID, text string) error
}

// MessageResourceFetcher is an optional capability: an adapter that can download
// the bytes of a media/file resource carried by an inbound message (an
// InboundAttachment). The service calls it to materialize attachments into the
// workspace so the agent can read the files. att.Kind selects the platform
// resource type (image vs file). Adapters whose platform inlines media, or has
// no download API, simply do not implement it.
type MessageResourceFetcher interface {
	FetchResource(ctx context.Context, cred Credential, messageExtID string, att InboundAttachment) (data []byte, err error)
}

// CredChallenger is an optional capability for webhook-mode URL-verification
// challenges that need the decrypted credential — WeCom's GET echostr must be
// AES-decrypted with the app's aes_key, which the plain ChannelAdapter.Challenge
// (request-only) cannot do. When an adapter implements this, the service calls it
// with the decrypted credential in preference to Challenge. Adapters that answer
// the challenge from the request alone (Lark) leave this unimplemented.
type CredChallenger interface {
	ChallengeWithCred(r *http.Request, cred Credential) (bodyEcho []byte, isChallenge bool)
}
