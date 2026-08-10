package wechat

// Wire types for the iLink (openclaw-weixin) JSON protocol. These mirror the
// upstream proto (WeixinMessage / GetUpdates / SendMessage / GetConfig /
// SendTyping) as consumed over HTTP+JSON; bytes fields are base64 strings.
// Only the fields niuniu acts on are modeled — unknown fields are ignored.

// Message item type enum (proto MessageItemType).
const (
	itemTypeText  = 1
	itemTypeImage = 2
	itemTypeVoice = 3
	itemTypeFile  = 4
	itemTypeVideo = 5
)

// Message type enum (proto MessageType): who authored a message.
const (
	msgTypeUser = 1 // inbound, from the human
	msgTypeBot  = 2 // outbound, from us
)

// Message state enum (proto MessageState), per the upstream openclaw-weixin docs:
// 0 = NEW, 1 = GENERATING (reply in progress), 2 = FINISH (complete). niuniu sends
// a complete reply as FINISH. (A true streaming reply would emit GENERATING chunks
// then FINISH, but the upstream README does not document how the outbound updates
// correlate — no client_id/run_id semantics — so we do not rely on it; the reply
// window is instead held open by a live typing indicator, see startTypingHeartbeat.)
const (
	msgStateNew        = 0
	msgStateGenerating = 1
	msgStateFinish     = 2
)

// Typing status enum (proto TypingStatus).
const (
	typingOn     = 1
	typingCancel = 2
)

// cdnMedia is a CDN object reference. aesKey is base64 in JSON; fullURL, when
// present, is the ready-to-GET download URL (no client-side assembly needed).
type cdnMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type textItem struct {
	Text string `json:"text,omitempty"`
}

type imageItem struct {
	Media *cdnMedia `json:"media,omitempty"`
	// AESKey is the raw AES-128 key as a hex string (16 bytes); when present it
	// is preferred over Media.AESKey for inbound image decryption.
	AESKey string `json:"aeskey,omitempty"`
}

type voiceItem struct {
	Media *cdnMedia `json:"media,omitempty"`
	// Text is the server-side speech-to-text transcript, when available.
	Text string `json:"text,omitempty"`
}

type fileItem struct {
	Media    *cdnMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
}

type videoItem struct {
	Media *cdnMedia `json:"media,omitempty"`
}

type refMessage struct {
	MessageItem *messageItem `json:"message_item,omitempty"`
	Title       string       `json:"title,omitempty"`
}

type messageItem struct {
	Type      int         `json:"type,omitempty"`
	RefMsg    *refMessage `json:"ref_msg,omitempty"`
	TextItem  *textItem   `json:"text_item,omitempty"`
	ImageItem *imageItem  `json:"image_item,omitempty"`
	VoiceItem *voiceItem  `json:"voice_item,omitempty"`
	FileItem  *fileItem   `json:"file_item,omitempty"`
	VideoItem *videoItem  `json:"video_item,omitempty"`
}

// weixinMessage is the unified message shape (proto WeixinMessage).
type weixinMessage struct {
	Seq          int64          `json:"seq,omitempty"`
	MessageID    int64          `json:"message_id,omitempty"`
	FromUserID   string         `json:"from_user_id,omitempty"`
	ToUserID     string         `json:"to_user_id,omitempty"`
	ClientID     string         `json:"client_id,omitempty"`
	CreateTimeMS int64          `json:"create_time_ms,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	GroupID      string         `json:"group_id,omitempty"`
	MessageType  int            `json:"message_type,omitempty"`
	MessageState int            `json:"message_state,omitempty"`
	ItemList     []*messageItem `json:"item_list,omitempty"`
	ContextToken string         `json:"context_token,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
}

// getUpdatesResp is the getupdates long-poll response envelope.
type getUpdatesResp struct {
	Ret                int              `json:"ret,omitempty"`
	Errcode            int              `json:"errcode,omitempty"`
	Errmsg             string           `json:"errmsg,omitempty"`
	Msgs               []*weixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf      string           `json:"get_updates_buf,omitempty"`
	LongpollingTimeout int64            `json:"longpolling_timeout_ms,omitempty"`
}

// sendMessageReq / sendMessageResp wrap one outbound message.
type sendMessageReq struct {
	Msg      *weixinMessage `json:"msg"`
	BaseInfo baseInfo       `json:"base_info"`
}

type sendMessageResp struct {
	Ret    int    `json:"ret,omitempty"`
	Errmsg string `json:"errmsg,omitempty"`
}

// getConfigResp carries the typing_ticket needed by sendtyping.
type getConfigResp struct {
	Ret          int    `json:"ret,omitempty"`
	Errmsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// baseInfo is attached to every CGI request (observability only).
type baseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}
