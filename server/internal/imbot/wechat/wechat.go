// Package wechat implements the imbot.ChannelAdapter for WeChat 微信ClawBot
// (Tencent openclaw-weixin / iLink personal-account bot protocol).
//
// Like the other imbot adapters it is fully self-contained: it speaks the iLink
// JSON/HTTP protocol directly and does NOT import internal/integration/. The
// outbound long connection is the protocol's native `getupdates` long-poll —
// plain outbound HTTPS, so it works behind a LAN/NAT with no public IP (the
// core constraint from the imbot design). Push maps to `sendmessage`; the
// per-message `context_token` (required by the server for session routing) is
// remembered per user from inbound and echoed on every reply. Inbound media
// (image/voice/file/video) is downloaded from the WeChat CDN and AES-128-ECB
// decrypted on demand via FetchResource. The "正在执行中" processing marker maps
// to the protocol's typing indicator (`sendtyping`).
//
// The bot_token is minted out-of-band by the QR-scan login flow (see login.go)
// and arrives here already decrypted inside imbot.Credential.Config.
package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

const (
	// defaultAPIBaseURL is the iLink CGI host. Overridable per channel via the
	// credential's base_url (the login flow may redirect to an IDC-specific host).
	defaultAPIBaseURL = "https://ilinkai.weixin.qq.com"
	// defaultCDNBaseURL is the c2c media CDN host used to assemble a download URL
	// when the message does not carry a ready-made full_url.
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	// longPollSeconds is the server-side hold for getupdates; the poll client
	// timeout sits safely above it so the request is a genuine long-poll.
	longPollSeconds  = 35
	maxResourceBytes = 100 << 20 // cap a single inbound media download
)

// Adapter is the WeChat iLink channel adapter. It is stateless per channel (all
// secrets arrive via imbot.Credential) except for an in-memory context-token
// cache: the iLink server issues a context_token per inbound message that must
// be echoed on replies, and Push happens asynchronously long after the inbound
// message, so the latest token per (account,user) is remembered here. The cache
// is best-effort — losing it on restart only means the first reply after a
// restart omits the token, which the server tolerates.
type Adapter struct {
	httpClient *http.Client
	pollClient *http.Client

	mu       sync.Mutex
	ctxTok   map[string]string             // key: accountID + "\x1f" + userID -> context_token
	typingHB map[string]context.CancelFunc // key: accountID + "\x1f" + userID -> cancel the live typing heartbeat
}

// New builds a WeChat adapter with sane HTTP timeouts.
func New() *Adapter {
	return &Adapter{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		pollClient: &http.Client{Timeout: (longPollSeconds + 15) * time.Second},
		ctxTok:     make(map[string]string),
		typingHB:   make(map[string]context.CancelFunc),
	}
}

// Type implements imbot.ChannelAdapter.
func (a *Adapter) Type() imbot.ChannelType { return imbot.ChannelWechat }

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

func token(cred imbot.Credential) string     { return credStr(cred, "token") }
func accountID(cred imbot.Credential) string { return credStr(cred, "account_id") }
func userID(cred imbot.Credential) string    { return credStr(cred, "user_id") }

func apiBaseURL(cred imbot.Credential) string {
	if b := credStr(cred, "base_url"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return defaultAPIBaseURL
}

func cdnBaseURL(cred imbot.Credential) string {
	if b := credStr(cred, "cdn_base_url"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return defaultCDNBaseURL
}

// --- context-token cache ---

func ctxKey(accountID, userID string) string { return accountID + "\x1f" + userID }

func (a *Adapter) rememberContextToken(accountID, userID, tok string) {
	if userID == "" || tok == "" {
		return
	}
	a.mu.Lock()
	a.ctxTok[ctxKey(accountID, userID)] = tok
	a.mu.Unlock()
}

func (a *Adapter) contextToken(accountID, userID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctxTok[ctxKey(accountID, userID)]
}

// --- typing heartbeat (holds the reply window open) ---
//
// iLink drops a bot reply as "请稍后再试" when it lands outside a short
// synchronous reply window (~1 min), and an agent turn routinely takes 60–120s.
// A single typing hint is transient (WeChat clears "对方正在输入" after a few
// seconds), so React starts a BACKGROUND heartbeat that re-sends typing=on every
// typingHeartbeatInterval — keeping the bot visibly "typing" (the user's desired
// 输入状态) and the session active until Push/RemoveReaction stops it. Capped at
// typingHeartbeatMax so a lost Push can never leak a goroutine forever.

const (
	// typingHeartbeatInterval matches upstream openclaw-weixin, whose reply
	// dispatcher refreshes the typing indicator every 5s (createTypingCallbacks
	// keepaliveIntervalMs: 5000) to hold a long reply inside iLink's window.
	typingHeartbeatInterval = 5 * time.Second
	typingHeartbeatMax      = 10 * time.Minute
)

// startTypingHeartbeat launches (or restarts) the per-user typing heartbeat. It
// returns immediately; the heartbeat runs on a background context so it outlives
// the request-scoped ctx of the inbound handler.
func (a *Adapter) startTypingHeartbeat(cred imbot.Credential, to string) {
	if to == "" || token(cred) == "" {
		return
	}
	a.stopTypingHeartbeat(accountID(cred), to) // cancel any prior run for this user
	ctx, cancel := context.WithTimeout(context.Background(), typingHeartbeatMax)
	a.mu.Lock()
	a.typingHB[ctxKey(accountID(cred), to)] = cancel
	a.mu.Unlock()
	go func() {
		defer cancel()
		ticker := time.NewTicker(typingHeartbeatInterval)
		defer ticker.Stop()
		_ = a.sendTyping(ctx, cred, to, typingOn) // show it immediately
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = a.sendTyping(ctx, cred, to, typingOn) // refresh; transient errors ignored
			}
		}
	}()
}

// stopTypingHeartbeat cancels a user's typing heartbeat if one is running.
func (a *Adapter) stopTypingHeartbeat(accountID, userID string) {
	a.mu.Lock()
	k := ctxKey(accountID, userID)
	cancel := a.typingHB[k]
	delete(a.typingHB, k)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// --- HTTP plumbing ---

// randomWechatUin builds the X-WECHAT-UIN header: a random uint32 rendered as
// its decimal string, then base64-encoded (matches the SDK exactly).
func randomWechatUin() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := binary.BigEndian.Uint32(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

// apiPost POSTs a JSON body to an iLink endpoint using client and decodes the
// JSON response into out (may be nil to ignore the body). It returns an error
// on transport failure or a non-2xx status.
func (a *Adapter) apiPost(ctx context.Context, client *http.Client, cred imbot.Credential, endpoint string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := apiBaseURL(cred) + "/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", randomWechatUin())
	req.Header.Set("iLink-App-ClientVersion", "1")
	if t := token(cred); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("wechat: %s status=%d body=%s", endpoint, resp.StatusCode, truncate(string(raw), 200))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("wechat: decode %s response: %w", endpoint, err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// baseInfoPayload is the base_info attached to every request. niuniu declares a
// stable bot_agent for observability on the server side.
func baseInfoPayload() baseInfo {
	return baseInfo{BotAgent: "niuniu"}
}

// --- Push (outbound) ---

// Push sends msg.Text to the target user via sendmessage, echoing the cached
// context_token for that user so the server routes the reply into the right
// session. The iLink personal-bot protocol has no interactive buttons, so
// msg.Buttons are ignored (permission approvals fall back to text commands / the
// WebUI). An empty text is a no-op.
func (a *Adapter) Push(ctx context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	if token(cred) == "" {
		return fmt.Errorf("wechat: missing token")
	}
	to := strings.TrimSpace(msg.ChatExtID)
	if to == "" {
		return fmt.Errorf("wechat: empty chat id")
	}
	if strings.TrimSpace(msg.Text) == "" {
		return nil
	}
	// The agent turn is done, so stop the typing heartbeat React started (no more
	// "对方正在输入" once the answer lands).
	a.stopTypingHeartbeat(accountID(cred), to)
	ctxTok := a.contextToken(accountID(cred), to)
	resp, err := a.sendReplyItem(ctx, cred, to, msg.Text, msgStateFinish)
	// Diagnostic: log every outbound send's shape and the iLink business code so a
	// reply that iLink accepts (ret=0) but WeChat never shows can be told apart
	// from a hard reject (ret=-2 rate-limited / token-expired / too-long). Logged
	// unconditionally (not just on error) because the failure mode of interest is
	// ret=0-but-undelivered, which the error path never sees.
	slog.Info("wechat: sendmessage",
		"to", truncate(to, 32),
		"runes", utf8.RuneCountInString(msg.Text),
		"has_context_token", ctxTok != "",
		"err", err,
		"ret", resp.Ret,
		"errmsg", truncate(resp.Errmsg, 120))
	if err != nil {
		return err
	}
	if resp.Ret != 0 {
		return fmt.Errorf("wechat: sendmessage ret=%d errmsg=%s", resp.Ret, resp.Errmsg)
	}
	return nil
}

// newClientID mints a per-send idempotency id for sendmessage.
func newClientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "niuniu-" + base64.RawURLEncoding.EncodeToString(b[:])
}

// --- VerifyCredential (connectivity test) ---

// VerifyCredential implements imbot.CredentialVerifier: getconfig is a
// lightweight authenticated call. A rejected token yields a non-2xx status
// (surfaced as an error); a decodable response proves the token is accepted.
func (a *Adapter) VerifyCredential(ctx context.Context, cred imbot.Credential) error {
	if token(cred) == "" {
		return fmt.Errorf("wechat: missing token")
	}
	payload := map[string]any{
		"ilink_user_id": userID(cred),
		"base_info":     baseInfoPayload(),
	}
	var resp getConfigResp
	return a.apiPost(ctx, a.httpClient, cred, "ilink/bot/getconfig", payload, &resp)
}

// --- typing indicator (mapped to the processing reaction) ---

// React implements imbot.MessageReactor. The framework's ReactionProcessing (the
// "正在执行中" marker) maps here to a LIVE typing indicator held for the whole
// agent turn.
//
// Why: the iLink personal-bot protocol drops the bot's real reply as "请稍后再试"
// when it lands outside a short synchronous reply window (~1 min), and an agent
// turn routinely takes 60–120s — proven in the field: a FINISH at +101s got ret=0
// yet never rendered. A single one-shot signal does not hold the window (a lone
// interim message and a lone typing hint both failed). So React starts a typing
// HEARTBEAT: it keeps re-sending typing=on (the protocol's documented "bot is
// composing" signal — getconfig typing_ticket + sendtyping) until Push finishes,
// which both shows the user "对方正在输入…" and keeps the session live. Returns
// "processing" so the service records the marker; RemoveReaction/Push stop it.
func (a *Adapter) React(ctx context.Context, cred imbot.Credential, messageExtID string, reaction imbot.Reaction) (string, error) {
	if reaction != imbot.ReactionProcessing {
		return "", nil
	}
	a.startTypingHeartbeat(cred, strings.TrimSpace(messageExtID))
	return string(imbot.ReactionProcessing), nil
}

// sendReplyItem sends one text item as the given message_state, echoing the cached
// context_token so the server routes into the right session. It does not chunk —
// the iLink personal-bot renders a long single message fine; the earlier
// byte-chunking was reverted (length was never the cause; the reply window was).
func (a *Adapter) sendReplyItem(ctx context.Context, cred imbot.Credential, to, text string, state int) (sendMessageResp, error) {
	req := sendMessageReq{
		Msg: &weixinMessage{
			ToUserID:     to,
			ClientID:     newClientID(),
			MessageType:  msgTypeBot,
			MessageState: state,
			ItemList:     []*messageItem{{Type: itemTypeText, TextItem: &textItem{Text: text}}},
			ContextToken: a.contextToken(accountID(cred), to),
		},
		BaseInfo: baseInfoPayload(),
	}
	var resp sendMessageResp
	err := a.apiPost(ctx, a.httpClient, cred, "ilink/bot/sendmessage", req, &resp)
	return resp, err
}

// RemoveReaction implements imbot.MessageReactor: stop the typing heartbeat React
// started and clear the "对方正在输入" indicator.
func (a *Adapter) RemoveReaction(ctx context.Context, cred imbot.Credential, messageExtID, _ string) error {
	a.stopTypingHeartbeat(accountID(cred), strings.TrimSpace(messageExtID))
	return a.sendTyping(ctx, cred, messageExtID, typingCancel)
}

// sendTyping fetches a fresh typing_ticket for the user and sends a typing
// status. The ticket is short-lived and getconfig is cheap, so it is not cached.
func (a *Adapter) sendTyping(ctx context.Context, cred imbot.Credential, targetUserID string, status int) error {
	targetUserID = strings.TrimSpace(targetUserID)
	if token(cred) == "" || targetUserID == "" {
		return nil
	}
	var cfg getConfigResp
	if err := a.apiPost(ctx, a.httpClient, cred, "ilink/bot/getconfig", map[string]any{
		"ilink_user_id": targetUserID,
		"context_token": a.contextToken(accountID(cred), targetUserID),
		"base_info":     baseInfoPayload(),
	}, &cfg); err != nil {
		return err
	}
	if cfg.TypingTicket == "" {
		return nil // server did not issue a ticket; nothing to send
	}
	return a.apiPost(ctx, a.httpClient, cred, "ilink/bot/sendtyping", map[string]any{
		"ilink_user_id": targetUserID,
		"typing_ticket": cfg.TypingTicket,
		"status":        status,
		"base_info":     baseInfoPayload(),
	}, nil)
}

// --- FetchResource (inbound media) ---

// FetchResource implements imbot.MessageResourceFetcher: it downloads a CDN
// object referenced by an inbound attachment and AES-128-ECB decrypts it. The
// packed mediaRef in att.ResourceID carries the download URL/param and key.
// Voice objects are SILK-encoded and returned as-is (the .silk bytes) since Go
// has no SILK decoder; the agent receives the raw file.
func (a *Adapter) FetchResource(ctx context.Context, cred imbot.Credential, _ string, att imbot.InboundAttachment) ([]byte, error) {
	ref, ok := decodeMediaRef(att.ResourceID)
	if !ok {
		return nil, fmt.Errorf("wechat: bad media reference")
	}
	url := ref.FullURL
	if url == "" {
		if ref.EncryptQueryParam == "" {
			return nil, fmt.Errorf("wechat: media reference has no url")
		}
		url = cdnBaseURL(cred) + "/download?encrypted_query_param=" + urlQueryEscape(ref.EncryptQueryParam)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResourceBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("wechat: cdn download status=%d", resp.StatusCode)
	}
	if ref.AESKeyB64 == "" {
		return data, nil // unencrypted object
	}
	key, err := parseAESKey(ref.AESKeyB64)
	if err != nil {
		return nil, err
	}
	return decryptAESECB(data, key)
}

// urlQueryEscape percent-encodes a query value without pulling net/url for one
// call site (the CDN param is opaque base64url-ish text).
func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteByte(r)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}

// --- Connect (outbound long connection) ---

// Connect runs the getupdates long-poll loop: it repeatedly asks the iLink
// server for updates (carrying the rolling get_updates_buf cursor), normalizes
// each human message into an imbot.InboundEvent, remembers its context_token,
// and invokes handler. It blocks until ctx is cancelled (returns nil) or a
// request fails unrecoverably (returns an error so the ConnectorManager
// reconnects with backoff). Confirmed updates are not redelivered by the buf
// cursor; the service-layer im_bot_inbox dedupe covers any overlap.
func (a *Adapter) Connect(ctx context.Context, cred imbot.Credential, handler imbot.InboundHandler) error {
	if token(cred) == "" {
		return fmt.Errorf("wechat: missing token")
	}
	acct := accountID(cred)
	var buf string
	for {
		if ctx.Err() != nil {
			return nil
		}
		resp, err := a.getUpdates(ctx, cred, buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancellation aborts the in-flight request; not a failure
			}
			return err
		}
		// -14 = session/token expired: the persisted bot_token is no longer
		// valid. Surface it so the manager backs off (reconnect won't help until
		// the user re-scans, but a tight loop is avoided and the error is logged).
		if resp.Errcode == -14 {
			return fmt.Errorf("wechat: session expired (errcode -14), re-login required")
		}
		if resp.GetUpdatesBuf != "" {
			buf = resp.GetUpdatesBuf
		}
		for _, m := range resp.Msgs {
			ev, tok, ok := parseInbound(m)
			if !ok {
				continue
			}
			a.rememberContextToken(acct, ev.ChatExtID, tok)
			// Diagnostic: confirm inbound messages carry a context_token so the
			// outbound cache actually populates. An always-empty token here would
			// mean replies go out with no token (iLink accepts but mis-routes).
			slog.Debug("wechat: inbound message", "from", truncate(ev.ChatExtID, 32), "has_context_token", tok != "")
			if handler != nil {
				handler(ctx, ev)
			}
		}
	}
}

// getUpdates performs one long-poll request. A client-side timeout (the poll
// client) simply returns an empty response so the loop retries — normal for a
// long-poll — while a genuine transport error propagates.
func (a *Adapter) getUpdates(ctx context.Context, cred imbot.Credential, buf string) (getUpdatesResp, error) {
	payload := map[string]any{
		"get_updates_buf": buf,
		"base_info":       baseInfoPayload(),
	}
	var resp getUpdatesResp
	if err := a.apiPost(ctx, a.pollClient, cred, "ilink/bot/getupdates", payload, &resp); err != nil {
		return getUpdatesResp{}, err
	}
	return resp, nil
}

// --- webhook mode (unsupported: iLink is outbound-only) ---

// VerifyWebhook is unsupported: the iLink protocol has no inbound webhook, so a
// wechat channel is always stream mode. It fails closed.
func (a *Adapter) VerifyWebhook(*http.Request, imbot.Credential) (imbot.InboundEvent, error) {
	return imbot.InboundEvent{}, fmt.Errorf("wechat: webhook mode not supported: %w", imbot.ErrWebhookUnauthorized)
}

// Challenge is a no-op: iLink has no URL-verification handshake.
func (a *Adapter) Challenge(*http.Request) (bodyEcho []byte, isChallenge bool) {
	return nil, false
}
