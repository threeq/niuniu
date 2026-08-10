// Package dingtalk implements the imbot.ChannelAdapter for DingTalk (钉钉) in
// Stream mode.
//
// Like the lark adapter, it is fully self-contained: it talks HTTP/WebSocket
// directly to the DingTalk open platform and does NOT import
// internal/integration/. The core deliverable is the OUTBOUND path — DingTalk
// "Stream" mode, where the bot dials OUT to DingTalk over a WebSocket long
// connection (POST /gateway/connections/open → wss endpoint) and receives
// callbacks over that socket instead of exposing a public webhook. That is what
// makes niuniu usable behind a LAN/NAT with no public IP (the core design
// constraint). Push (outbound message send) rides plain outbound HTTPS with an
// access token, so it too needs no public reachability.
//
// The optional public-webhook mode (VerifyWebhook/Challenge) exists only for
// deployments that DO have a public URL; the LAN default never calls it.
package dingtalk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// defaultBaseURL is the DingTalk open-platform API host. Tests override it via
// cred.Config["base_url"] so the httptest mock server stands in for it.
const defaultBaseURL = "https://api.dingtalk.com"

// Stream subscription topics: SYSTEM/* keeps the socket alive (ping) and
// CALLBACK bot-messages delivers inbound bot messages.
const botMessageTopic = "/v1.0/im/bot/messages/get"

// Adapter is the DingTalk channel adapter. It is stateless per channel (all
// per-channel secrets arrive via imbot.Credential); the only state is a
// process-wide access-token cache keyed by client_id (AppKey).
type Adapter struct {
	httpClient *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken // client_id -> token
}

type cachedToken struct {
	token   string
	expires time.Time
}

// New builds a DingTalk adapter with sane HTTP timeouts.
func New() *Adapter {
	return &Adapter{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		tokens:     make(map[string]cachedToken),
	}
}

// Type implements imbot.ChannelAdapter.
func (a *Adapter) Type() imbot.ChannelType { return imbot.ChannelDingTalk }

// --- credential helpers ---

func credStr(cred imbot.Credential, key string) string {
	if cred.Config == nil {
		return ""
	}
	if v, ok := cred.Config[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func baseURL(cred imbot.Credential) string {
	if b := credStr(cred, "base_url"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return defaultBaseURL
}

// --- access token ---

// accessToken returns a cached-or-freshly-minted access token for the
// credential's app. Tokens are cached until ~5 minutes before expiry.
func (a *Adapter) accessToken(ctx context.Context, cred imbot.Credential) (string, error) {
	clientID := credStr(cred, "client_id")
	clientSecret := credStr(cred, "client_secret")
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("dingtalk: missing client_id/client_secret")
	}

	a.mu.Lock()
	if t, ok := a.tokens[clientID]; ok && time.Now().Before(t.expires) {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"appKey": clientID, "appSecret": clientSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL(cred)+"/v1.0/oauth2/accessToken", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"` // seconds
		Code        string `json:"code"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("dingtalk: decode token response: %w", err)
	}
	if resp.StatusCode/100 != 2 || out.AccessToken == "" {
		return "", fmt.Errorf("dingtalk: token error status=%d code=%s message=%s", resp.StatusCode, out.Code, out.Message)
	}
	ttl := time.Duration(out.ExpireIn) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	a.mu.Lock()
	a.tokens[clientID] = cachedToken{token: out.AccessToken, expires: time.Now().Add(ttl - 5*time.Minute)}
	a.mu.Unlock()
	return out.AccessToken, nil
}

// VerifyCredential implements imbot.CredentialVerifier: it proves the app
// credentials are valid by minting an access token.
func (a *Adapter) VerifyCredential(ctx context.Context, cred imbot.Credential) error {
	_, err := a.accessToken(ctx, cred)
	return err
}

// --- Push (outbound) ---

// Push sends msg to the target conversation via the robot group-messages API.
// Plain notifications go as sampleMarkdown so **bold**/lists/links render as rich
// text instead of showing their markdown source (DingTalk's sampleText renders
// verbatim). When msg.Buttons are present we render a sampleActionCard. DingTalk
// action-card buttons carry only URLs (no free-form callback payload the way
// Lark/Telegram do), so the interactive approve/deny round-trip over a card is
// best-effort; to keep the permission prompt honestly visible we ALSO append the
// button labels/values as readable text lines. A clean, visible fallback beats a
// broken card.
func (a *Adapter) Push(ctx context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	if strings.TrimSpace(msg.ChatExtID) == "" {
		return fmt.Errorf("dingtalk: empty conversation id")
	}
	msgKey, msgParam := renderOutbound(msg)
	_, err := a.sendMessage(ctx, cred, msg.ChatExtID, msgKey, msgParam)
	return err
}

// renderOutbound turns an OutboundMessage into the (msgKey, msgParam) pair the
// robot send API expects: a sampleActionCard when there are buttons, otherwise a
// sampleMarkdown so the text renders as rich markdown.
func renderOutbound(msg imbot.OutboundMessage) (msgKey, msgParam string) {
	if len(msg.Buttons) > 0 {
		// Append the callback payloads as readable lines so the operator can see
		// (and, if needed, act on) the permission prompt even though card buttons
		// can't carry our permission:* payload back over Stream.
		var sb strings.Builder
		sb.WriteString(msg.Text)
		btns := make([]map[string]string, 0, len(msg.Buttons))
		for _, b := range msg.Buttons {
			sb.WriteString("\n- " + b.Label + " (" + b.Value + ")")
			btns = append(btns, map[string]string{"title": b.Label, "actionURL": ""})
		}
		param, _ := json.Marshal(map[string]any{
			"title":             "niuniu",
			"text":              sb.String(),
			"buttonList":        btns,
			"buttonOrientation": "0",
		})
		return "sampleActionCard", string(param)
	}
	param, _ := json.Marshal(map[string]string{"title": markdownTitle(msg.Text), "text": msg.Text})
	return "sampleMarkdown", string(param)
}

// markdownTitle derives the sampleMarkdown `title` (DingTalk requires one; it is
// used as the push-notification summary, not shown in the bubble). It is the
// first non-empty line, rune-capped, falling back to the bot name.
func markdownTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r := []rune(line)
			if len(r) > 20 {
				return string(r[:20])
			}
			return line
		}
	}
	return "牛牛"
}

// sendMessage posts one message to a conversation via the robot group-messages
// API and returns its processQueryKey (the handle used to recall it later — see
// RemoveReaction). A non-2xx status or non-empty error code is a hard error.
func (a *Adapter) sendMessage(ctx context.Context, cred imbot.Credential, conv, msgKey, msgParam string) (string, error) {
	token, err := a.accessToken(ctx, cred)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"robotCode":          credStr(cred, "robot_code"),
		"openConversationId": conv,
		"msgKey":             msgKey,
		"msgParam":           msgParam,
	})
	u := baseURL(cred) + "/v1.0/robot/groupMessages/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		ProcessQueryKey string `json:"processQueryKey"`
		Code            string `json:"code"`
		Message         string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.Code != "" {
		return "", fmt.Errorf("dingtalk: send failed status=%d code=%s message=%s", resp.StatusCode, out.Code, out.Message)
	}
	return out.ProcessQueryKey, nil
}

// --- Reply (new-conversation task marker) ---

// Reply implements imbot.MessageReplier: it posts the `#<id> <标题>` task marker
// into the message's conversation when a new workspace is created. DingTalk has
// no anchored/quoted-reply API for robot messages, so this is a normal message
// sent to the same conversation (decoded from messageExtID) — the visible marker
// is what matters, mirroring the Feishu reply. It renders as markdown.
func (a *Adapter) Reply(ctx context.Context, cred imbot.Credential, messageExtID, text string) error {
	_, conv := decodeMsgRef(messageExtID)
	if conv == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("dingtalk: reply missing conversation / text")
	}
	param, _ := json.Marshal(map[string]string{"title": markdownTitle(text), "text": text})
	_, err := a.sendMessage(ctx, cred, conv, "sampleMarkdown", string(param))
	return err
}

// --- React (status receipt) ---

// processingReceiptText is the transient "牛牛 正在处理" receipt DingTalk shows
// while an inbound message is being worked on. DingTalk has no emoji-reaction API
// (unlike Feishu), so the platform-appropriate parity is a lightweight message
// that is recalled once the agent finishes (see RemoveReaction).
const processingReceiptText = "🐂 牛牛正在处理…"

// React implements imbot.MessageReactor. For ReactionProcessing it sends the
// transient receipt into the conversation and returns its processQueryKey as the
// "reaction id", which RemoveReaction later recalls. Unknown reactions and an
// un-targetable message (no conversation encoded) are silent no-ops so a caller
// never surfaces a spurious error.
func (a *Adapter) React(ctx context.Context, cred imbot.Credential, messageExtID string, reaction imbot.Reaction) (string, error) {
	if reaction != imbot.ReactionProcessing {
		return "", nil
	}
	_, conv := decodeMsgRef(messageExtID)
	if conv == "" {
		return "", nil
	}
	param, _ := json.Marshal(map[string]string{"title": "牛牛", "text": processingReceiptText})
	return a.sendMessage(ctx, cred, conv, "sampleMarkdown", string(param))
}

// RemoveReaction implements imbot.MessageReactor: it recalls the transient
// receipt message (by the processQueryKey React returned) once the agent
// finishes, so the "正在处理" marker disappears. A missing id / conversation is a
// no-op.
func (a *Adapter) RemoveReaction(ctx context.Context, cred imbot.Credential, messageExtID, reactionID string) error {
	if strings.TrimSpace(reactionID) == "" {
		return nil
	}
	_, conv := decodeMsgRef(messageExtID)
	if conv == "" {
		return nil
	}
	token, err := a.accessToken(ctx, cred)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"robotCode":          credStr(cred, "robot_code"),
		"openConversationId": conv,
		"processQueryKeys":   []string{reactionID},
	})
	u := baseURL(cred) + "/v1.0/robot/groupMessages/recall"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.Code != "" {
		return fmt.Errorf("dingtalk: recall failed status=%d code=%s message=%s", resp.StatusCode, out.Code, out.Message)
	}
	return nil
}

// --- FetchResource (download inbound media/file) ---

// maxResourceBytes caps a single downloaded inbound resource (DingTalk's own bot
// media limit is well under this); the ceiling guards the server against a
// hostile or corrupt Content-Length.
const maxResourceBytes = 32 << 20

// FetchResource implements imbot.MessageResourceFetcher: it materializes the
// bytes of an inbound image/file/audio/video. DingTalk hands the robot a
// short-lived downloadCode (att.ResourceID); we exchange it for a signed
// downloadUrl via /v1.0/robot/messageFiles/download, then GET the bytes. The
// messageExtID argument is unused (the downloadCode fully identifies the
// resource).
func (a *Adapter) FetchResource(ctx context.Context, cred imbot.Credential, _ string, att imbot.InboundAttachment) ([]byte, error) {
	if strings.TrimSpace(att.ResourceID) == "" {
		return nil, fmt.Errorf("dingtalk: missing download code")
	}
	token, err := a.accessToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{
		"downloadCode": att.ResourceID,
		"robotCode":    credStr(cred, "robot_code"),
	})
	u := baseURL(cred) + "/v1.0/robot/messageFiles/download"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		DownloadURL string `json:"downloadUrl"`
		Code        string `json:"code"`
		Message     string `json:"message"`
	}
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		return nil, fmt.Errorf("dingtalk: decode download response: %w", jerr)
	}
	if resp.StatusCode/100 != 2 || out.DownloadURL == "" {
		return nil, fmt.Errorf("dingtalk: download-url failed status=%d code=%s message=%s", resp.StatusCode, out.Code, out.Message)
	}

	greq, err := http.NewRequestWithContext(ctx, http.MethodGet, out.DownloadURL, nil)
	if err != nil {
		return nil, err
	}
	gresp, err := a.httpClient.Do(greq)
	if err != nil {
		return nil, err
	}
	defer gresp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(gresp.Body, maxResourceBytes))
	if err != nil {
		return nil, err
	}
	if gresp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("dingtalk: fetch resource failed status=%d", gresp.StatusCode)
	}
	return data, nil
}

// --- Connect (Stream mode outbound long connection) ---

// streamFrame is a DingTalk Stream protocol frame. Both directions (inbound
// callbacks and our acks) use this shape. data is a JSON STRING (a nested JSON
// document), not a nested object, matching the wire protocol.
type streamFrame struct {
	SpecVersion string            `json:"specVersion"`
	Type        string            `json:"type"`    // SYSTEM | CALLBACK | EVENT
	Headers     map[string]string `json:"headers"` // topic, messageId, contentType
	Data        string            `json:"data"`
}

// gatewayOpenResp is the shape of POST /gateway/connections/open: the wss
// endpoint to dial plus a one-shot ticket.
type gatewayOpenResp struct {
	Endpoint string `json:"endpoint"`
	Ticket   string `json:"ticket"`
}

// Connect opens the DingTalk Stream long connection and blocks reading frames
// until ctx is cancelled or the socket errors — at which point it returns and
// the ConnectorManager reconnects with backoff. Because the bot dials OUT (no
// inbound port), this works behind a LAN/NAT with no public URL.
//
// Protocol: register subscriptions to get an endpoint+ticket, dial
// endpoint?ticket=..., then for each text frame:
//   - SYSTEM/ping  -> reply an ack echoing the same messageId+data (pong keepalive)
//   - CALLBACK bot-message -> normalize to InboundEvent, call handler, then ack
//     with {"response":{},"status":"SUCCESS","message":"OK"} so DingTalk marks
//     it processed (unacked callbacks get redelivered).
//   - everything else is ignored.
func (a *Adapter) Connect(ctx context.Context, cred imbot.Credential, handler imbot.InboundHandler) error {
	clientID := credStr(cred, "client_id")
	clientSecret := credStr(cred, "client_secret")
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("dingtalk: missing client_id/client_secret")
	}

	wsURL, err := a.openGateway(ctx, cred, clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("dingtalk: open gateway: %w", err)
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dingtalk: dial ws: %w", err)
	}
	defer conn.Close()
	// Diagnostic: confirm the Stream WS gateway came up. The read loop below is
	// otherwise silent on the success path, so a connector that connected but
	// receives no frames is indistinguishable from one that never connected.
	slog.Info("dingtalk: stream connected", "url", wsURL)

	// Close the socket promptly when ctx is cancelled so the blocking
	// ReadMessage below returns.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("dingtalk: ws read: %w", err)
		}
		a.dispatchFrame(ctx, conn, data, handler)
	}
}

// dispatchFrame decodes one Stream frame and reacts per the protocol: pong for
// SYSTEM/ping, normalize+handle+ack for a bot-message CALLBACK. Non-JSON /
// unknown frames are ignored. Frame-write (ack/pong) errors are swallowed here;
// a truly dead socket surfaces on the next ReadMessage and triggers reconnect.
func (a *Adapter) dispatchFrame(ctx context.Context, conn *websocket.Conn, data []byte, handler imbot.InboundHandler) {
	var f streamFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	topic := f.Headers["topic"]
	messageID := f.Headers["messageId"]
	// Diagnostic: log every inbound frame's type/topic so a connected socket
	// that receives no CALLBACK frames (vs only SYSTEM pings) is observable.
	slog.Debug("dingtalk: frame", "type", f.Type, "topic", topic)

	switch {
	case f.Type == "SYSTEM" && topic == "ping":
		// Echo the same messageId + data back as a pong to keep the socket alive.
		a.writeFrame(conn, streamFrame{
			SpecVersion: "1.0",
			Type:        "SYSTEM",
			Headers:     map[string]string{"topic": "pong", "messageId": messageID, "contentType": "application/json"},
			Data:        f.Data,
		})
	case f.Type == "CALLBACK" && topic == botMessageTopic:
		if handler != nil {
			if ev, ok := parseDingTalkBotMessage([]byte(f.Data)); ok {
				ev.Channel = imbot.ChannelDingTalk
				handler(ctx, ev)
			} else if ev, ok := parseCardActionCallback([]byte(f.Data)); ok {
				// Best-effort: a bot-message topic that actually carries a card
				// action callback (permission approve/deny). See inbound.go.
				ev.Channel = imbot.ChannelDingTalk
				handler(ctx, ev)
			}
		}
		// Ack is REQUIRED so DingTalk marks the callback processed; without it
		// the platform redelivers. Service-layer idempotency (im_bot_inbox) makes
		// any race-window redelivery harmless.
		a.ackCallback(conn, messageID)
	case f.Type == "CALLBACK":
		// Other CALLBACK topics (e.g. a dedicated card-instances callback) may
		// still carry our permission payload — parse best-effort, then ack.
		if handler != nil {
			if ev, ok := parseCardActionCallback([]byte(f.Data)); ok {
				ev.Channel = imbot.ChannelDingTalk
				handler(ctx, ev)
			}
		}
		a.ackCallback(conn, messageID)
	default:
		// SYSTEM (non-ping), EVENT, unknown: ignore.
	}
}

// ackCallback sends the required SUCCESS ack for a processed CALLBACK frame.
func (a *Adapter) ackCallback(conn *websocket.Conn, messageID string) {
	a.writeFrame(conn, streamFrame{
		SpecVersion: "1.0",
		Type:        "CALLBACK",
		Headers:     map[string]string{"messageId": messageID, "contentType": "application/json"},
		Data:        `{"response":{},"status":"SUCCESS","message":"OK"}`,
	})
}

// writeFrame marshals and writes a single text frame. Errors are intentionally
// ignored: a dead socket is detected by the read loop, which drives reconnect.
func (a *Adapter) writeFrame(conn *websocket.Conn, f streamFrame) {
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, b)
}

// openGateway registers the Stream subscriptions and returns the ws:// | wss://
// URL to dial (endpoint with the ticket appended as a query param).
func (a *Adapter) openGateway(ctx context.Context, cred imbot.Credential, clientID, clientSecret string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"subscriptions": []map[string]string{
			{"type": "SYSTEM", "topic": "*"},
			{"type": "CALLBACK", "topic": botMessageTopic},
		},
		"ua": "niuniu-imbot",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(cred)+"/v1.0/gateway/connections/open", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out gatewayOpenResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode gateway: %w", err)
	}
	if resp.StatusCode/100 != 2 || out.Endpoint == "" || out.Ticket == "" {
		return "", fmt.Errorf("gateway error status=%d endpoint=%q", resp.StatusCode, out.Endpoint)
	}
	sep := "?"
	if strings.Contains(out.Endpoint, "?") {
		sep = "&"
	}
	return out.Endpoint + sep + "ticket=" + out.Ticket, nil
}

// --- optional public webhook mode ---

// VerifyWebhook parses an inbound HTTP callback body (the optional public-webhook
// path) into a normalized InboundEvent, reusing the same JSON normalizer as the
// Stream transport. When cred.Config["webhook_secret"] is non-empty the DingTalk
// callback signature (HMAC-SHA256 over timestamp using the secret, base64) is
// verified; an empty secret skips verification (the LAN default never calls this
// path). An error is returned for a failed signature, malformed body, or a
// non-actionable event.
func (a *Adapter) VerifyWebhook(r *http.Request, cred imbot.Credential) (imbot.InboundEvent, error) {
	if r == nil || r.Body == nil {
		return imbot.InboundEvent{}, fmt.Errorf("dingtalk: empty webhook body")
	}
	if secret := credStr(cred, "webhook_secret"); secret != "" {
		if err := verifySignature(r, secret); err != nil {
			return imbot.InboundEvent{}, err
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return imbot.InboundEvent{}, err
	}
	if ev, ok := parseDingTalkBotMessage(body); ok {
		ev.Channel = imbot.ChannelDingTalk
		return ev, nil
	}
	if ev, ok := parseCardActionCallback(body); ok {
		ev.Channel = imbot.ChannelDingTalk
		return ev, nil
	}
	return imbot.InboundEvent{}, fmt.Errorf("dingtalk: non-actionable webhook event")
}

// verifySignature checks the DingTalk HTTP-callback signature: the platform
// signs "<timestamp>" with HMAC-SHA256 keyed by the shared secret and
// base64-encodes it. timestamp/sign arrive via headers (fallback: query). A
// mismatch is a hard error.
func verifySignature(r *http.Request, secret string) error {
	timestamp := headerOrQuery(r, "timestamp")
	sign := headerOrQuery(r, "sign")
	if timestamp == "" || sign == "" {
		return fmt.Errorf("dingtalk: missing timestamp/sign: %w", imbot.ErrWebhookUnauthorized)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sign)) {
		return fmt.Errorf("dingtalk: signature mismatch: %w", imbot.ErrWebhookUnauthorized)
	}
	return nil
}

func headerOrQuery(r *http.Request, key string) string {
	if v := r.Header.Get(key); v != "" {
		return v
	}
	return r.URL.Query().Get(key)
}

// Challenge answers the DingTalk HTTP callback-URL registration handshake. The
// encrypted-callback variant (an "encrypt" field decrypted with the AES key) is
// out of scope for the LAN-default Stream deployment; the plaintext/simple case
// has no separate challenge, so we return isChallenge=false and let VerifyWebhook
// handle ordinary bodies.
func (a *Adapter) Challenge(_ *http.Request) (bodyEcho []byte, isChallenge bool) {
	return nil, false
}
