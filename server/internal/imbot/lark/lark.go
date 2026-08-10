// Package lark implements the imbot.ChannelAdapter for Feishu / Lark.
//
// It is fully self-contained: it talks HTTP/WebSocket directly to the Lark
// open platform and does NOT import internal/integration/. The W1 deliverable
// is the OUTBOUND path — tenant_access_token acquisition + im/v1/messages send
// (Push) — which works over plain outbound HTTPS and therefore needs no public
// IP (LAN/desktop friendly). Connect establishes the Lark event-subscription
// WebSocket long connection and keeps it alive; decoding inbound business
// frames into imbot.InboundEvent is the W2 deliverable (see Connect).
package lark

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/imbot"
)

const defaultBaseURL = "https://open.feishu.cn"

// Adapter is the Lark channel adapter. It is stateless per channel (all
// per-channel secrets arrive via imbot.Credential); the only state is a
// process-wide tenant_access_token cache keyed by app_id.
type Adapter struct {
	httpClient *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken // app_id -> token
}

type cachedToken struct {
	token   string
	expires time.Time
}

// New builds a Lark adapter with sane HTTP timeouts.
func New() *Adapter {
	return &Adapter{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		tokens:     make(map[string]cachedToken),
	}
}

// Type implements imbot.ChannelAdapter.
func (a *Adapter) Type() imbot.ChannelType { return imbot.ChannelLark }

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

// --- tenant_access_token ---

// tenantToken returns a cached-or-freshly-minted tenant_access_token for the
// credential's app. Tokens are cached until ~5 minutes before expiry.
func (a *Adapter) tenantToken(ctx context.Context, cred imbot.Credential) (string, error) {
	appID := credStr(cred, "app_id")
	appSecret := credStr(cred, "app_secret")
	if appID == "" || appSecret == "" {
		return "", fmt.Errorf("lark: missing app_id/app_secret")
	}

	a.mu.Lock()
	if t, ok := a.tokens[appID]; ok && time.Now().Before(t.expires) {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"app_id": appID, "app_secret": appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL(cred)+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
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
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"` // seconds
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("lark: decode token response: %w", err)
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("lark: token error code=%d msg=%s", out.Code, out.Msg)
	}
	ttl := time.Duration(out.Expire) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	a.mu.Lock()
	a.tokens[appID] = cachedToken{token: out.TenantAccessToken, expires: time.Now().Add(ttl - 5*time.Minute)}
	a.mu.Unlock()
	return out.TenantAccessToken, nil
}

// --- Push (outbound) ---

// Push sends msg to the target chat via im/v1/messages. Plain notifications go
// as text; when msg.Buttons are present it sends an interactive card whose
// buttons carry the callback payload (msg.Button.Value) in each button's
// `value.cb`, so a click comes back as a card.action.trigger the inbound path
// decodes into an action_callback InboundEvent (the W2 permission闭环).
func (a *Adapter) Push(ctx context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	if strings.TrimSpace(msg.ChatExtID) == "" {
		return fmt.Errorf("lark: empty chat id")
	}
	token, err := a.tenantToken(ctx, cred)
	if err != nil {
		return err
	}

	// Always send as an interactive card so markdown renders: Feishu's `text`
	// message type shows markdown source verbatim, while a card's `markdown`
	// element renders it as rich text. Button messages add an action row.
	msgType := "interactive"
	var content string
	if len(msg.Buttons) > 0 {
		content = buildInteractiveCard(msg.Text, msg.Buttons)
	} else {
		content = buildMarkdownCard(msg.Text)
	}

	payload, _ := json.Marshal(map[string]any{
		"receive_id": msg.ChatExtID,
		"msg_type":   msgType,
		"content":    content,
	})

	u := baseURL(cred) + "/open-apis/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.Code != 0 {
		return fmt.Errorf("lark: send failed status=%d code=%d msg=%s", resp.StatusCode, out.Code, out.Msg)
	}
	return nil
}

// VerifyCredential implements imbot.CredentialVerifier: it proves the app
// credentials are valid by minting a tenant_access_token.
func (a *Adapter) VerifyCredential(ctx context.Context, cred imbot.Credential) error {
	_, err := a.tenantToken(ctx, cred)
	return err
}

// --- Reply (anchored text reply) ---

// Reply implements imbot.MessageReplier: it posts a plain-text reply anchored to
// messageExtID via im/v1/messages/:message_id/reply, so the text (e.g. the task
// marker `#<id> <标题>`) is shown quoting the original message. reply_in_thread
// is false so it stays a normal anchored reply rather than opening a topic.
func (a *Adapter) Reply(ctx context.Context, cred imbot.Credential, messageExtID, text string) error {
	if strings.TrimSpace(messageExtID) == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("lark: empty message id / reply text")
	}
	token, err := a.tenantToken(ctx, cred)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	payload, _ := json.Marshal(map[string]any{
		"content":         string(content),
		"msg_type":        "text",
		"reply_in_thread": false,
	})
	u := baseURL(cred) + "/open-apis/im/v1/messages/" + url.PathEscape(messageExtID) + "/reply"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.Code != 0 {
		return fmt.Errorf("lark: reply failed status=%d code=%d msg=%s", resp.StatusCode, out.Code, out.Msg)
	}
	return nil
}

// --- React (message reaction) ---

// reactionEmojiType maps a platform-neutral imbot.Reaction to Feishu's fixed
// emoji_type enum. Unknown reactions map to "" so React can no-op rather than
// send an invalid emoji_type the API would reject.
func reactionEmojiType(r imbot.Reaction) string {
	switch r {
	case imbot.ReactionProcessing:
		return "AWESOMEN" // 牛 (emoji_socool) — marks "牛牛 正在执行中"
	default:
		return ""
	}
}

// React implements imbot.MessageReactor: it attaches an emoji reaction to a
// message via im/v1/messages/:message_id/reactions, marking it (e.g. as being
// processed), and returns the created reaction_id (used to remove it later). It
// is best-effort; the "reaction already exists" case is treated as success (with
// an empty id) so a redelivered inbound event does not surface a spurious error.
func (a *Adapter) React(ctx context.Context, cred imbot.Credential, messageExtID string, reaction imbot.Reaction) (string, error) {
	if strings.TrimSpace(messageExtID) == "" {
		return "", fmt.Errorf("lark: empty message id")
	}
	emoji := reactionEmojiType(reaction)
	if emoji == "" {
		return "", nil // unsupported reaction — nothing to do
	}
	token, err := a.tenantToken(ctx, cred)
	if err != nil {
		return "", err
	}

	payload, _ := json.Marshal(map[string]any{
		"reaction_type": map[string]string{"emoji_type": emoji},
	})
	u := baseURL(cred) + "/open-apis/im/v1/messages/" + url.PathEscape(messageExtID) + "/reactions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ReactionID string `json:"reaction_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &out)
	// Feishu returns 230026 when the reaction already exists — harmless under
	// event redelivery, so treat it as success (no new id to return).
	if out.Code == larkReactionExistsCode {
		return "", nil
	}
	if resp.StatusCode/100 != 2 || out.Code != 0 {
		return "", fmt.Errorf("lark: react failed status=%d code=%d msg=%s", resp.StatusCode, out.Code, out.Msg)
	}
	return out.Data.ReactionID, nil
}

// RemoveReaction implements imbot.MessageReactor: it deletes a reaction by id via
// DELETE im/v1/messages/:message_id/reactions/:reaction_id, clearing the "正在
// 处理" marker once the message is done. A missing reaction id is a no-op.
func (a *Adapter) RemoveReaction(ctx context.Context, cred imbot.Credential, messageExtID, reactionID string) error {
	if strings.TrimSpace(messageExtID) == "" || strings.TrimSpace(reactionID) == "" {
		return nil
	}
	token, err := a.tenantToken(ctx, cred)
	if err != nil {
		return err
	}
	u := baseURL(cred) + "/open-apis/im/v1/messages/" + url.PathEscape(messageExtID) +
		"/reactions/" + url.PathEscape(reactionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.Code != 0 {
		return fmt.Errorf("lark: remove reaction failed status=%d code=%d msg=%s", resp.StatusCode, out.Code, out.Msg)
	}
	return nil
}

// larkReactionExistsCode is Feishu's error code for "the reaction already exists
// on this message" (idempotent no-op under inbound-event redelivery).
const larkReactionExistsCode = 230026

// --- FetchResource (download inbound media/file) ---

// FetchResource implements imbot.MessageResourceFetcher: it downloads the bytes
// of an image/file/audio/video carried by an inbound message via
// im/v1/messages/:message_id/resources/:file_key?type=image|file. Images use
// type=image; every other kind (file/audio/video) uses type=file, per the Lark
// resource API.
func (a *Adapter) FetchResource(ctx context.Context, cred imbot.Credential, messageExtID string, att imbot.InboundAttachment) ([]byte, error) {
	if strings.TrimSpace(messageExtID) == "" || strings.TrimSpace(att.ResourceID) == "" {
		return nil, fmt.Errorf("lark: missing message id / resource key")
	}
	token, err := a.tenantToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	resType := "file"
	if att.Kind == "image" {
		resType = "image"
	}
	u := baseURL(cred) + "/open-apis/im/v1/messages/" + url.PathEscape(messageExtID) +
		"/resources/" + url.PathEscape(att.ResourceID) + "?type=" + resType
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Success is a raw binary body; an error is a JSON envelope with a non-zero
	// code. Cap the read so a hostile/oversized resource can't exhaust memory.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResourceBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(data, &out)
		return nil, fmt.Errorf("lark: fetch resource failed status=%d code=%d msg=%s", resp.StatusCode, out.Code, out.Msg)
	}
	return data, nil
}

// maxResourceBytes caps a single downloaded inbound resource (Lark's own upload
// limit is 30MB for files); the ceiling guards the server against a hostile or
// corrupt Content-Length.
const maxResourceBytes = 32 << 20

// --- Connect (outbound long connection) ---

// wsEndpointResp is the shape of POST /callback/ws/endpoint (the Lark WS
// handshake that hands back the wss URL to dial).
type wsEndpointResp struct {
	Code int `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		URL string `json:"URL"`
	} `json:"data"`
}

// Connect negotiates the Lark event WebSocket endpoint, dials it, and blocks
// reading frames (keeping the connection alive via gorilla's automatic
// ping/pong) until ctx is cancelled or the socket errors — at which point it
// returns and the ConnectorManager reconnects with backoff.
//
// W1 scope: this establishes and maintains a genuine outbound long connection
// so the manager has something real to supervise (reconnect/hot-reload/restart
// recovery) and so the LAN-no-public-URL property holds. Decoding inbound
// business frames (protobuf) into imbot.InboundEvent and invoking handler is
// the W2 deliverable; handler is threaded through now so W2 is a drop-in.
func (a *Adapter) Connect(ctx context.Context, cred imbot.Credential, handler imbot.InboundHandler) error {
	appID := credStr(cred, "app_id")
	appSecret := credStr(cred, "app_secret")
	if appID == "" || appSecret == "" {
		return fmt.Errorf("lark: missing app_id/app_secret")
	}

	wsURL, err := a.negotiateWSEndpoint(ctx, cred, appID, appSecret)
	if err != nil {
		return fmt.Errorf("lark: negotiate ws endpoint: %w", err)
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("lark: dial ws: %w", err)
	}
	defer conn.Close()

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
			return fmt.Errorf("lark: ws read: %w", err)
		}
		a.dispatchFrame(ctx, data, handler)
	}
}

// dispatchFrame decodes one WS binary frame (a pbbp2.Frame) and, when it carries
// an actionable message/card event, invokes handler with the normalized event.
// Control frames (ping/pong, acks) and non-text events decode to ok=false and
// are ignored. Redeliveries are made harmless by the service-layer idempotency
// (im_bot_inbox), so we do not implement the response-ack frame in W2.
func (a *Adapter) dispatchFrame(ctx context.Context, data []byte, handler imbot.InboundHandler) {
	if handler == nil || len(data) == 0 {
		return
	}
	// The event JSON may arrive either as a bare WS text frame or wrapped in a
	// protobuf Frame. Try the frame first; fall back to treating data as JSON.
	payload := data
	if f, err := decodeFrame(data); err == nil && len(f.Payload) > 0 {
		payload = f.Payload
	}
	ev, ok, err := parseLarkEventJSON(payload)
	if err != nil || !ok {
		return
	}
	ev.Channel = imbot.ChannelLark
	handler(ctx, ev)
}

func (a *Adapter) negotiateWSEndpoint(ctx context.Context, cred imbot.Credential, appID, appSecret string) (string, error) {
	body, _ := json.Marshal(map[string]string{"AppID": appID, "AppSecret": appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(cred)+"/callback/ws/endpoint", bytes.NewReader(body))
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
	var out wsEndpointResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode endpoint: %w", err)
	}
	if out.Code != 0 || out.Data.URL == "" {
		return "", fmt.Errorf("endpoint error code=%d msg=%s", out.Code, out.Msg)
	}
	if _, err := url.Parse(out.Data.URL); err != nil {
		return "", fmt.Errorf("bad ws url: %w", err)
	}
	return out.Data.URL, nil
}

// --- optional public webhook mode (W2+) ---

// VerifyWebhook parses an inbound HTTP event body (the optional public-webhook
// path) into a normalized InboundEvent, reusing the exact same JSON parser as
// the stream transport. Encrypted-payload webhooks (Encrypt Key) are deferred to
// a later wave; W2 supports the plaintext event body. An error is returned when
// the body is malformed or not an actionable event.
func (a *Adapter) VerifyWebhook(r *http.Request, cred imbot.Credential) (imbot.InboundEvent, error) {
	if r == nil || r.Body == nil {
		return imbot.InboundEvent{}, fmt.Errorf("lark: empty webhook body")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return imbot.InboundEvent{}, err
	}
	// Authenticate the caller against the channel's Lark Verification Token
	// (design §8), injected by the service as webhook_secret. Without this a
	// forger who reaches the public endpoint and guesses a channelId could
	// inject messages or permission-approval callbacks. Fail closed: a
	// webhook-mode channel with no secret cannot be authenticated, so reject
	// rather than trust the body.
	expected := credStr(cred, "webhook_secret")
	if expected == "" {
		return imbot.InboundEvent{}, fmt.Errorf("lark: webhook secret not configured: %w", imbot.ErrWebhookUnauthorized)
	}
	var env struct {
		Header struct {
			Token string `json:"token"`
		} `json:"header"`
	}
	_ = json.Unmarshal(body, &env)
	if subtle.ConstantTimeCompare([]byte(env.Header.Token), []byte(expected)) != 1 {
		return imbot.InboundEvent{}, fmt.Errorf("lark: verification token mismatch: %w", imbot.ErrWebhookUnauthorized)
	}
	ev, ok, err := parseLarkEventJSON(body)
	if err != nil {
		return imbot.InboundEvent{}, err
	}
	if !ok {
		return imbot.InboundEvent{}, fmt.Errorf("lark: non-actionable webhook event")
	}
	ev.Channel = imbot.ChannelLark
	return ev, nil
}

// Challenge answers the Feishu URL-verification handshake: a POST body of
// {"type":"url_verification","challenge":"..."} must be echoed back as
// {"challenge":"..."}. isChallenge=false for ordinary event bodies. The request
// body is left consumed; callers use Challenge before VerifyWebhook and only
// forward to VerifyWebhook when isChallenge is false.
func (a *Adapter) Challenge(r *http.Request) (bodyEcho []byte, isChallenge bool) {
	if r == nil || r.Body == nil {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		return nil, false
	}
	// Restore the body so a non-challenge request can still be read downstream.
	r.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Type != "url_verification" {
		return nil, false
	}
	echo, _ := json.Marshal(map[string]string{"challenge": probe.Challenge})
	return echo, true
}
