// Package wework implements the imbot.ChannelAdapter for 企业微信 (WeCom / WeWork)
// (Epic #555, W4). It backs TWO distinct WeCom bot products, selected by the
// credential and connection_mode:
//
//   - 智能机器人 (AI-Bot) STREAM mode (connection_mode=stream; bot_id+secret):
//     a WebSocket long connection the bot dials OUT (see stream.go). LAN/NAT
//     friendly, no public URL — the core imbot constraint. Connect runs the ws
//     loop; Push replies over the same socket (aibot_respond_msg streaming);
//     inbound arrives as aibot_msg_callback frames. Added in Issue #616.
//   - 自建应用 (self-built app) WEBHOOK mode (connection_mode=webhook;
//     corp_id+agent_id+secret): no outbound stream (design §11) — inbound comes
//     only via the encrypted HTTP callback (VerifyWebhook + AnswerURLVerification
//     GET challenge). Connect is a no-op stub; Push is a cgi-bin/message/send
//     HTTPS call (works from a LAN with no public IP).
//
// The two paths share nothing but the package: Connect/Push/VerifyCredential
// branch on streamCredsPresent (a bot_id). Like the other adapters it is fully
// self-contained — it talks HTTP/WebSocket directly to the WeCom API
// (https://qyapi.weixin.qq.com, wss://openws.work.weixin.qq.com) and does NOT
// import internal/integration/.
package wework

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

const defaultBaseURL = "https://qyapi.weixin.qq.com"

// Adapter is the WeCom channel adapter. It is stateless per channel (all secrets
// arrive via imbot.Credential); the only state is a process-wide access_token
// cache keyed by corp_id+secret (mirroring lark's token cache).
type Adapter struct {
	httpClient *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken // corp_id+"\x00"+secret -> token

	// 智能机器人 (AI-Bot) stream-mode state — the WebSocket long-connection path
	// (see stream.go). Empty/unused for self-built-app webhook channels.
	streamMu    sync.Mutex
	streamConns map[string]*aibotConn  // bot_id -> live connection
	streamReply map[string]*aibotReply // bot_id "\x1f" chatExtID -> pending reply target
}

type cachedToken struct {
	token   string
	expires time.Time
}

// New builds a WeCom adapter with sane HTTP timeouts.
func New() *Adapter {
	return &Adapter{
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		tokens:      make(map[string]cachedToken),
		streamConns: make(map[string]*aibotConn),
		streamReply: make(map[string]*aibotReply),
	}
}

// Type implements imbot.ChannelAdapter.
func (a *Adapter) Type() imbot.ChannelType { return imbot.ChannelWework }

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

// --- access_token ---

// accessToken returns a cached-or-freshly-minted access_token for the
// credential's app (keyed by corp_id+secret). Tokens are cached until ~5 minutes
// before expiry, mirroring lark's tenant_access_token cache.
func (a *Adapter) accessToken(ctx context.Context, cred imbot.Credential) (string, error) {
	corpID := credStr(cred, "corp_id")
	secret := credStr(cred, "secret")
	if corpID == "" || secret == "" {
		return "", fmt.Errorf("wework: missing corp_id/secret")
	}
	cacheKey := corpID + "\x00" + secret

	a.mu.Lock()
	if t, ok := a.tokens[cacheKey]; ok && time.Now().Before(t.expires) {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()

	u := baseURL(cred) + "/cgi-bin/gettoken?corpid=" + url.QueryEscape(corpID) + "&corpsecret=" + url.QueryEscape(secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"` // seconds
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("wework: decode token response: %w", err)
	}
	if out.ErrCode != 0 || out.AccessToken == "" {
		return "", fmt.Errorf("wework: token error errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	a.mu.Lock()
	a.tokens[cacheKey] = cachedToken{token: out.AccessToken, expires: time.Now().Add(ttl - 5*time.Minute)}
	a.mu.Unlock()
	return out.AccessToken, nil
}

// --- Push (outbound HTTPS — works from LAN) ---

// Push sends msg to the target user via cgi-bin/message/send. WeCom self-built
// app interactive cards (textcard/template_card) are limited and awkward for the
// permission approve/deny闭环, so when msg.Buttons are present we append them as
// readable text lines to the message content — an honest fallback (the buttons'
// callback payload is the same permission:approve:<reqID> string a user could
// reply with) rather than a half-working card.
func (a *Adapter) Push(ctx context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	// AI-Bot stream channels reply over the live WebSocket, not cgi-bin (stream.go).
	if streamCredsPresent(cred) {
		return a.pushStream(ctx, cred, msg)
	}
	if strings.TrimSpace(msg.ChatExtID) == "" {
		return fmt.Errorf("wework: empty chat id")
	}
	agentID, err := agentIDInt(cred)
	if err != nil {
		return err
	}
	token, err := a.accessToken(ctx, cred)
	if err != nil {
		return err
	}

	content := msg.Text
	if len(msg.Buttons) > 0 {
		content = appendButtonLines(content, msg.Buttons)
	}

	payload, _ := json.Marshal(map[string]any{
		"touser":  msg.ChatExtID,
		"msgtype": "text",
		"agentid": agentID,
		"text":    map[string]string{"content": content},
	})

	u := baseURL(cred) + "/cgi-bin/message/send?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.ErrCode != 0 {
		return fmt.Errorf("wework: send failed status=%d errcode=%d errmsg=%s", resp.StatusCode, out.ErrCode, out.ErrMsg)
	}
	return nil
}

// appendButtonLines renders each button as a "标签: 回复内容" line appended to the
// message text, so a WeCom user without an interactive card can still act on a
// permission request by replying with the payload.
func appendButtonLines(text string, buttons []imbot.Button) string {
	var b strings.Builder
	b.WriteString(text)
	if text != "" {
		b.WriteString("\n")
	}
	for _, btn := range buttons {
		b.WriteString("\n")
		b.WriteString(btn.Label)
		b.WriteString(": ")
		b.WriteString(btn.Value)
	}
	return b.String()
}

// agentIDInt parses the agent_id credential into the integer WeCom's send API
// expects.
func agentIDInt(cred imbot.Credential) (int, error) {
	raw := credStr(cred, "agent_id")
	if raw == "" {
		return 0, fmt.Errorf("wework: missing agent_id")
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("wework: agent_id %q is not an integer: %w", raw, err)
	}
	return n, nil
}

// VerifyCredential implements imbot.CredentialVerifier: it proves corp_id+secret
// are valid by fetching an access_token.
func (a *Adapter) VerifyCredential(ctx context.Context, cred imbot.Credential) error {
	// AI-Bot stream mode: a real subscribe test would kick the live long
	// connection (a bot allows only one), so validate field presence instead.
	if streamCredsPresent(cred) {
		if credStr(cred, "secret") == "" {
			return fmt.Errorf("wework: missing secret for stream mode")
		}
		return nil
	}
	_, err := a.accessToken(ctx, cred)
	return err
}

// --- Connect ---

// Connect runs the AI-Bot WebSocket long connection for stream-mode channels
// (bot_id present; see connectStream in stream.go). For self-built-app channels
// (no bot_id) it is a deliberate no-op stub: that product has no outbound stream
// mode (design §11) — inbound arrives only via the optional public webhook — so
// it just waits on ctx.Done() and returns nil to satisfy the ConnectorManager's
// "block until ctx is cancelled" contract without busy-looping.
func (a *Adapter) Connect(ctx context.Context, cred imbot.Credential, handler imbot.InboundHandler) error {
	// 智能机器人 (AI-Bot) channels DO have an outbound long connection — the
	// WebSocket stream mode (see stream.go). Self-built-app channels (no bot_id)
	// keep the no-op behavior: they receive only via the optional public webhook.
	if streamCredsPresent(cred) {
		return a.connectStream(ctx, cred, handler)
	}
	<-ctx.Done()
	return nil
}

// truncate caps a string for log lines (mirrors the wechat adapter's helper).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --- optional public webhook mode (the ONLY inbound path for WeCom) ---

// weworkEncrypted is the outer envelope of a WeCom callback POST body:
// <xml><ToUserName/><Encrypt><![CDATA[...]]></Encrypt><AgentID/></xml>.
type weworkEncrypted struct {
	ToUserName string `xml:"ToUserName"`
	Encrypt    string `xml:"Encrypt"`
	AgentID    string `xml:"AgentID"`
}

// VerifyWebhook parses an inbound WeCom callback POST (the only inbound path for
// WeCom). It reads the encrypted XML envelope, verifies the msg_signature from
// the query (msg_signature/timestamp/nonce) over the Encrypt ciphertext,
// AES-decrypts the plaintext XML, optionally checks receiveid==corp_id, and
// normalizes it into an InboundEvent. An error is returned when the signature is
// invalid, decryption fails, or the payload is not an actionable text message.
func (a *Adapter) VerifyWebhook(r *http.Request, cred imbot.Credential) (imbot.InboundEvent, error) {
	if r == nil || r.Body == nil {
		return imbot.InboundEvent{}, fmt.Errorf("wework: empty webhook body")
	}
	token := credStr(cred, "token")
	aesKey, err := aesKeyFromEncoding(credStr(cred, "aes_key"))
	if err != nil {
		return imbot.InboundEvent{}, err
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return imbot.InboundEvent{}, err
	}
	var env weworkEncrypted
	if err := xml.Unmarshal(body, &env); err != nil {
		return imbot.InboundEvent{}, fmt.Errorf("wework: decode callback envelope: %w", err)
	}
	if strings.TrimSpace(env.Encrypt) == "" {
		return imbot.InboundEvent{}, fmt.Errorf("wework: callback missing Encrypt")
	}

	q := r.URL.Query()
	if !verifySignature(token, q.Get("timestamp"), q.Get("nonce"), env.Encrypt, q.Get("msg_signature")) {
		return imbot.InboundEvent{}, fmt.Errorf("wework: msg_signature mismatch: %w", imbot.ErrWebhookUnauthorized)
	}

	plain, receiveID, err := decryptMsg(aesKey, env.Encrypt)
	if err != nil {
		return imbot.InboundEvent{}, err
	}
	// WeCom's receiveid is the corp_id; a mismatch means the payload decrypted to
	// the wrong tenant — treat it as unauthorized (401), not a benign body.
	if corpID := credStr(cred, "corp_id"); corpID != "" && string(receiveID) != corpID {
		return imbot.InboundEvent{}, fmt.Errorf("wework: receiveid mismatch: %w", imbot.ErrWebhookUnauthorized)
	}

	ev, ok := parseWeworkXML(plain)
	if !ok {
		return imbot.InboundEvent{}, fmt.Errorf("wework: non-actionable webhook event")
	}
	return ev, nil
}

// Challenge always returns (nil,false) for WeCom. WeCom's GET URL-verification
// handshake requires the token+aes_key (to verify msg_signature and decrypt the
// echostr), but the ChannelAdapter.Challenge signature has no cred parameter, so
// it cannot be answered here. The URL verification is instead answered by the
// exported AnswerURLVerification method, which the service/wiring layer can call
// once it detects a GET-with-echostr and has the decrypted credential. See the
// package doc and AnswerURLVerification.
func (a *Adapter) Challenge(*http.Request) (bodyEcho []byte, isChallenge bool) {
	return nil, false
}

// ChallengeWithCred implements imbot.CredChallenger: it answers the WeCom GET
// URL-verification handshake, which the plain Challenge cannot (it lacks the
// credential needed to decrypt echostr). The service calls this in preference to
// Challenge when the adapter implements CredChallenger. A request that is not a
// GET-with-echostr returns (nil,false) so it falls through to VerifyWebhook (the
// POST event path). A GET whose echostr fails verification also returns
// (nil,false); the platform simply will not complete URL setup.
func (a *Adapter) ChallengeWithCred(r *http.Request, cred imbot.Credential) (bodyEcho []byte, isChallenge bool) {
	if r == nil || r.Method != http.MethodGet {
		return nil, false
	}
	q := r.URL.Query()
	echostr := q.Get("echostr")
	if echostr == "" {
		return nil, false
	}
	plain, err := a.AnswerURLVerification(cred, q.Get("msg_signature"), q.Get("timestamp"), q.Get("nonce"), echostr)
	if err != nil {
		return nil, false
	}
	return plain, true
}

// AnswerURLVerification answers the WeCom GET URL-verification handshake. WeCom
// GETs the callback URL with query params msg_signature, timestamp, nonce and an
// encrypted echostr; the URL is verified by returning the DECRYPTED echostr
// plaintext verbatim (HTTP 200, plain text). This method verifies the
// msg_signature over (token,timestamp,nonce,echostr) and, on success, decrypts
// echostr and returns the plaintext to echo back.
//
// It is exported (not part of ChannelAdapter, which lacks a cred parameter on
// Challenge) so the wiring/service layer can special-case the WeCom GET echostr:
// on a GET carrying echostr, call this with the decrypted credential and write
// the returned bytes back as the response.
func (a *Adapter) AnswerURLVerification(cred imbot.Credential, msgSig, timestamp, nonce, echostr string) ([]byte, error) {
	token := credStr(cred, "token")
	aesKey, err := aesKeyFromEncoding(credStr(cred, "aes_key"))
	if err != nil {
		return nil, err
	}
	if !verifySignature(token, timestamp, nonce, echostr, msgSig) {
		return nil, fmt.Errorf("wework: url verification msg_signature mismatch")
	}
	plain, receiveID, err := decryptMsg(aesKey, echostr)
	if err != nil {
		return nil, err
	}
	if corpID := credStr(cred, "corp_id"); corpID != "" && string(receiveID) != corpID {
		return nil, fmt.Errorf("wework: url verification receiveid mismatch")
	}
	return plain, nil
}
