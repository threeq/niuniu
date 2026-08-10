// Package telegram implements the imbot.ChannelAdapter for Telegram (Epic #555,
// W3).
//
// Like lark/, it is fully self-contained: it talks HTTP directly to the
// Telegram Bot API (https://core.telegram.org/bots/api) and does NOT import
// internal/integration/. The outbound long connection is Telegram's native
// long-polling — getUpdates with a server-side timeout — which needs only plain
// outbound HTTPS and therefore works behind a LAN/NAT with no public IP or
// webhook (the core constraint from the design spec). Push maps to sendMessage;
// interaction buttons map to an inline keyboard whose callback_data round-trips
// back through getUpdates as a callback_query (the permission闭环).
package telegram

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	// longPollSeconds is the server-side wait Telegram holds getUpdates open for
	// when there are no new updates; the poll HTTP client timeout is kept safely
	// above it. This is what makes the connection a genuine long connection
	// (few requests, low latency) rather than a busy poll.
	longPollSeconds = 50
)

// Adapter is the Telegram channel adapter. It is stateless per channel (all
// per-channel secrets — just bot_token — arrive via imbot.Credential); the
// getUpdates offset is local to a single Connect call.
type Adapter struct {
	httpClient *http.Client // short-timeout client for sendMessage/getMe
	pollClient *http.Client // long-timeout client for getUpdates long-poll
}

// New builds a Telegram adapter with sane HTTP timeouts.
func New() *Adapter {
	return &Adapter{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		pollClient: &http.Client{Timeout: (longPollSeconds + 15) * time.Second},
	}
}

// Type implements imbot.ChannelAdapter.
func (a *Adapter) Type() imbot.ChannelType { return imbot.ChannelTelegram }

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

// apiURL builds the method endpoint: <base>/bot<token>/<method>.
func apiURL(cred imbot.Credential, token, method string) string {
	return baseURL(cred) + "/bot" + token + "/" + method
}

// tgResponse is the envelope every Bot API method returns.
type tgResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// call POSTs a JSON payload to a Bot API method using the given client and
// decodes the envelope. It returns an error when the transport fails or the API
// reports ok=false.
func (a *Adapter) call(ctx context.Context, client *http.Client, cred imbot.Credential, token, method string, payload any) (json.RawMessage, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(cred, token, method), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env tgResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("telegram: decode %s response: %w", method, err)
	}
	if !env.OK {
		return nil, fmt.Errorf("telegram: %s failed status=%d desc=%s", method, resp.StatusCode, env.Description)
	}
	return env.Result, nil
}

// --- Push (outbound) ---

// Push sends msg to the target chat via sendMessage. The text is rendered to
// Telegram HTML (parse_mode=HTML) so **bold**/`code`/[links] display as rich text
// instead of their markdown source (Telegram renders neither CommonMark nor,
// safely, MarkdownV2). When msg.Buttons are present it attaches an inline
// keyboard whose buttons carry the callback payload (Button.Value) in each
// button's callback_data, so a click comes back through getUpdates as a
// callback_query the inbound path decodes into an action_callback InboundEvent.
func (a *Adapter) Push(ctx context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	token := credStr(cred, "bot_token")
	if token == "" {
		return fmt.Errorf("telegram: missing bot_token")
	}
	if strings.TrimSpace(msg.ChatExtID) == "" {
		return fmt.Errorf("telegram: empty chat id")
	}

	payload := map[string]any{"chat_id": chatIDValue(msg.ChatExtID)}
	// Forum-topic / thread targeting: Telegram uses message_thread_id (an int).
	if id, err := strconv.Atoi(strings.TrimSpace(msg.ThreadExtID)); err == nil && id != 0 {
		payload["message_thread_id"] = id
	}
	if len(msg.Buttons) > 0 {
		payload["reply_markup"] = inlineKeyboard(msg.Buttons)
	}
	return a.sendRich(ctx, cred, token, payload, msg.Text)
}

// sendRich POSTs sendMessage with the text rendered as Telegram HTML, and on
// failure retries once as plain text. The HTML renderer only ever emits balanced,
// self-generated tags over escaped prose, so a parse rejection is not expected —
// but a message must never be lost to a formatting error, so the plain-text retry
// is the guarantee. payload carries the non-text fields (chat_id, thread, keyboard).
func (a *Adapter) sendRich(ctx context.Context, cred imbot.Credential, token string, payload map[string]any, text string) error {
	payload["text"] = renderTelegramHTML(text)
	payload["parse_mode"] = "HTML"
	if _, err := a.call(ctx, a.httpClient, cred, token, "sendMessage", payload); err != nil {
		delete(payload, "parse_mode")
		payload["text"] = text
		if _, ferr := a.call(ctx, a.httpClient, cred, token, "sendMessage", payload); ferr != nil {
			return ferr
		}
	}
	return nil
}

// chatIDValue passes a numeric chat id as an integer (Telegram treats a numeric
// string as a @channel username lookup and rejects it), falling back to the raw
// string for @channel targets.
func chatIDValue(chatExtID string) any {
	if n, err := strconv.ParseInt(strings.TrimSpace(chatExtID), 10, 64); err == nil {
		return n
	}
	return chatExtID
}

// inlineKeyboard renders buttons as a single-row inline keyboard carrying each
// button's callback payload in callback_data (max 64 bytes, which the
// permission:approve:<reqID> payloads fit).
func inlineKeyboard(buttons []imbot.Button) map[string]any {
	row := make([]map[string]string, 0, len(buttons))
	for _, b := range buttons {
		row = append(row, map[string]string{"text": b.Label, "callback_data": b.Value})
	}
	return map[string]any{"inline_keyboard": [][]map[string]string{row}}
}

// VerifyCredential implements imbot.CredentialVerifier: getMe proves the bot
// token is valid without sending a message.
func (a *Adapter) VerifyCredential(ctx context.Context, cred imbot.Credential) error {
	token := credStr(cred, "bot_token")
	if token == "" {
		return fmt.Errorf("telegram: missing bot_token")
	}
	_, err := a.call(ctx, a.httpClient, cred, token, "getMe", map[string]any{})
	return err
}

// --- Reply / React / FetchResource (parity capabilities) ---

// processingEmoji is the reaction Telegram shows while an inbound message is being
// worked on (the "正在执行中" marker). 👀 (eyes) is in Telegram's fixed allowed
// reaction set and reads as "seen / on it"; it is cleared on agent_done.
const processingEmoji = "👀"

// maxResourceBytes caps a single downloaded inbound resource; the ceiling guards
// the server against a hostile or corrupt Content-Length.
const maxResourceBytes = 32 << 20

// Reply implements imbot.MessageReplier: it posts the `#<id> <标题>` task marker
// as a quoted reply to the inbound message (reply_parameters.message_id) when a
// new workspace is created, decoding the chat+message id from messageExtID. The
// marker renders as Telegram HTML like any other outbound text.
func (a *Adapter) Reply(ctx context.Context, cred imbot.Credential, messageExtID, text string) error {
	token := credStr(cred, "bot_token")
	if token == "" {
		return fmt.Errorf("telegram: missing bot_token")
	}
	chatID, msgID := decodeMsgRef(messageExtID)
	mid, err := strconv.Atoi(msgID)
	if chatID == "" || err != nil {
		return fmt.Errorf("telegram: reply missing chat/message id")
	}
	payload := map[string]any{
		"chat_id":          chatIDValue(chatID),
		"reply_parameters": map[string]any{"message_id": mid},
	}
	return a.sendRich(ctx, cred, token, payload, text)
}

// React implements imbot.MessageReactor: for ReactionProcessing it attaches the
// 👀 reaction to the message via setMessageReaction and returns the emoji as the
// "reaction id" (non-empty so the service records it and later clears it).
// Unknown reactions and an un-targetable message are silent no-ops.
func (a *Adapter) React(ctx context.Context, cred imbot.Credential, messageExtID string, reaction imbot.Reaction) (string, error) {
	if reaction != imbot.ReactionProcessing {
		return "", nil
	}
	token := credStr(cred, "bot_token")
	if token == "" {
		return "", fmt.Errorf("telegram: missing bot_token")
	}
	chatID, msgID := decodeMsgRef(messageExtID)
	mid, err := strconv.Atoi(msgID)
	if chatID == "" || err != nil {
		return "", nil
	}
	payload := map[string]any{
		"chat_id":    chatIDValue(chatID),
		"message_id": mid,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": processingEmoji}},
	}
	if _, err := a.call(ctx, a.httpClient, cred, token, "setMessageReaction", payload); err != nil {
		return "", err
	}
	return processingEmoji, nil
}

// RemoveReaction implements imbot.MessageReactor: it clears the reaction by
// setting an empty reaction list on the message, removing the 👀 marker once the
// agent finishes. A missing chat/message id is a no-op.
func (a *Adapter) RemoveReaction(ctx context.Context, cred imbot.Credential, messageExtID, _ string) error {
	token := credStr(cred, "bot_token")
	if token == "" {
		return nil
	}
	chatID, msgID := decodeMsgRef(messageExtID)
	mid, err := strconv.Atoi(msgID)
	if chatID == "" || err != nil {
		return nil
	}
	payload := map[string]any{
		"chat_id":    chatIDValue(chatID),
		"message_id": mid,
		"reaction":   []map[string]string{}, // empty list clears reactions
	}
	_, err = a.call(ctx, a.httpClient, cred, token, "setMessageReaction", payload)
	return err
}

// FetchResource implements imbot.MessageResourceFetcher: it materializes the
// bytes of an inbound photo/document/voice/audio/video. Telegram is a two-step
// download — getFile resolves the file_id (att.ResourceID) to a file_path, then
// the bytes are fetched from <base>/file/bot<token>/<file_path>. messageExtID is
// unused (the file_id fully identifies the resource).
func (a *Adapter) FetchResource(ctx context.Context, cred imbot.Credential, _ string, att imbot.InboundAttachment) ([]byte, error) {
	token := credStr(cred, "bot_token")
	if token == "" {
		return nil, fmt.Errorf("telegram: missing bot_token")
	}
	if strings.TrimSpace(att.ResourceID) == "" {
		return nil, fmt.Errorf("telegram: missing file id")
	}
	result, err := a.call(ctx, a.httpClient, cred, token, "getFile", map[string]any{"file_id": att.ResourceID})
	if err != nil {
		return nil, err
	}
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(result, &f); err != nil {
		return nil, fmt.Errorf("telegram: decode getFile result: %w", err)
	}
	if f.FilePath == "" {
		return nil, fmt.Errorf("telegram: getFile returned no file_path")
	}
	u := baseURL(cred) + "/file/bot" + token + "/" + f.FilePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return nil, fmt.Errorf("telegram: fetch resource failed status=%d", resp.StatusCode)
	}
	return data, nil
}

// --- Connect (outbound long connection) ---

// Connect runs the getUpdates long-poll loop: it asks Telegram for updates with
// a server-side timeout, invokes handler for each actionable update, advances
// the offset to acknowledge them, and repeats until ctx is cancelled (returns
// nil) or a request fails unrecoverably (returns an error so the
// ConnectorManager reconnects with backoff). Confirmed updates are never
// redelivered by the offset mechanism; the service-layer idempotency
// (im_bot_inbox keyed on update_id) covers any webhook-mode redelivery.
func (a *Adapter) Connect(ctx context.Context, cred imbot.Credential, handler imbot.InboundHandler) error {
	token := credStr(cred, "bot_token")
	if token == "" {
		return fmt.Errorf("telegram: missing bot_token")
	}

	var offset int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := a.getUpdates(ctx, cred, token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancellation aborts the in-flight request; not a failure
			}
			return err // real failure -> manager backoff + reconnect
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1 // ack past this update on the next poll
			}
			if handler == nil {
				continue
			}
			if ev, ok := parseTelegramUpdate(u); ok {
				handler(ctx, ev)
			}
		}
	}
}

// getUpdates performs one long-poll request. offset<=0 requests all unconfirmed
// updates; offset>0 confirms everything below it.
func (a *Adapter) getUpdates(ctx context.Context, cred imbot.Credential, token string, offset int64) ([]tgUpdate, error) {
	payload := map[string]any{
		"timeout":         longPollSeconds,
		"allowed_updates": []string{"message", "callback_query"},
	}
	if offset > 0 {
		payload["offset"] = offset
	}
	result, err := a.call(ctx, a.pollClient, cred, token, "getUpdates", payload)
	if err != nil {
		return nil, err
	}
	var updates []tgUpdate
	if len(result) > 0 {
		if err := json.Unmarshal(result, &updates); err != nil {
			return nil, fmt.Errorf("telegram: decode updates: %w", err)
		}
	}
	return updates, nil
}

// --- optional public webhook mode (W4+) ---

// VerifyWebhook parses an inbound HTTP update body (the optional public-webhook
// path) into a normalized InboundEvent, reusing the exact same parser as the
// long-poll transport. An error is returned when the body is malformed or is
// not an actionable update.
func (a *Adapter) VerifyWebhook(r *http.Request, cred imbot.Credential) (imbot.InboundEvent, error) {
	if r == nil || r.Body == nil {
		return imbot.InboundEvent{}, fmt.Errorf("telegram: empty webhook body")
	}
	// Authenticate against the per-channel secret_token registered via
	// setWebhook, which Telegram echoes in this header on every POST (injected
	// by the service as webhook_secret). Without it a forger reaching the public
	// endpoint could drive the agent or forge permission approvals. Fail closed
	// when the secret is unset.
	expected := credStr(cred, "webhook_secret")
	if expected == "" {
		return imbot.InboundEvent{}, fmt.Errorf("telegram: webhook secret not configured: %w", imbot.ErrWebhookUnauthorized)
	}
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		return imbot.InboundEvent{}, fmt.Errorf("telegram: secret token mismatch: %w", imbot.ErrWebhookUnauthorized)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return imbot.InboundEvent{}, err
	}
	var u tgUpdate
	if err := json.Unmarshal(body, &u); err != nil {
		return imbot.InboundEvent{}, err
	}
	ev, ok := parseTelegramUpdate(u)
	if !ok {
		return imbot.InboundEvent{}, fmt.Errorf("telegram: non-actionable webhook update")
	}
	return ev, nil
}

// Challenge is a no-op for Telegram: it has no URL-verification handshake (the
// webhook is authenticated by the secret_token header, checked in
// VerifyWebhook), so every request falls through to VerifyWebhook.
func (a *Adapter) Challenge(*http.Request) (bodyEcho []byte, isChallenge bool) {
	return nil, false
}
