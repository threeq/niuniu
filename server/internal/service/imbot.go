package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrIMBotNotFound is returned when a channel/chat does not exist or does not
// belong to the project the caller is scoped to (handlers map it to 404).
var ErrIMBotNotFound = errors.New("imbot: not found")

// ErrWebhookUnauthorized is returned from HandleWebhook when the adapter's
// platform-native signature verification fails; the handler maps it to HTTP 401
// (a forged webhook), distinct from a malformed/non-actionable body (200).
var ErrWebhookUnauthorized = errors.New("imbot: webhook unauthorized")

// ErrInvalidChannelConfig is returned when a channel's create/update payload is
// internally inconsistent (handlers map it to HTTP 400). Chief case: a
// webhook-mode Lark/Telegram channel with no webhook secret, which would fail
// closed on every inbound request.
var ErrInvalidChannelConfig = errors.New("imbot: invalid channel config")

// ErrDuplicateIMBotCredential is returned when a channel create would register a
// second bot for the same app identity (owner + channel_type + credential
// fingerprint). A single app = a single stream connection; a duplicate would
// fight the existing one for the connection (handlers map it to HTTP 409).
var ErrDuplicateIMBotCredential = errors.New("imbot: a bot for this app already exists under this owner")

// IMBotService owns project-level IM channel CRUD + chat pairing approval.
//
// Credentials are AES-GCM encrypted at rest (credential_enc column) using the
// same server keyring as the other server-side encrypted-credential features
// (external credentials, data sources). This is deliberately chosen over the
// OS-keychain credstore: the hosted edition runs on a headless Linux server
// where no OS keychain exists, so DB-column AES-GCM is the only deploy-safe
// option. The imbot/ adapter package itself never sees ciphertext or the
// keyring — decryption happens only here, and adapters receive a decrypted
// imbot.Credential (honoring the "creds decrypt only in the service layer"
// and "imbot/ does not import integration/" constraints from the design).
type IMBotService struct {
	q        *store.Queries
	db       *store.DB
	keyring  *crypto.Keyring
	authz    *Authz
	adapters map[imbot.ChannelType]imbot.ChannelAdapter

	mgr *imbot.ConnectorManager // set post-construction to break a wiring cycle

	// --- inbound (W2), wired via SetInbound after construction ---
	dispatch  TaskRouter        // continue-or-create issue+workspace in the bound project
	deliverer MessageDeliverer  // agentproxy.Deliver (interface to avoid an import cycle)
	perm      PermissionDecider // permission-button callback write-back
	askUser   AskUserDecider    // ask-user-question answer write-back (option buttons)

	// deleter removes an issue+workspace for the /delete command; stopper
	// terminates the workspace's running agent session first. Both are the same
	// concrete objects already passed to SetInbound (the dispatch service and the
	// agentproxy deliverer), captured here via a capability type-assertion so no
	// extra wiring/signature change is needed. Either may stay nil (tests, or a
	// deliverer/dispatch that lacks the capability) — /delete degrades to a nudge.
	deleter TaskDeleter
	stopper AgentSessionStopper

	// starter provisions a backing workspace for an EXISTING project issue that
	// has none yet — the `#<id>` control targeting a kanban issue created without a
	// workspace. Same concrete object as dispatch, captured via a capability
	// assertion; nil leaves `#<id>` on a workspace-less issue falling through to a
	// new conversation.
	starter IssueWorkspaceStarter

	// procMu guards procReactions: the 🐂 "正在执行中" markers placed on inbound
	// messages, keyed by workspace id, held until the agent finishes (agent_done)
	// so they can be removed. In-memory only — a cosmetic marker, safe to lose on
	// restart.
	procMu        sync.Mutex
	procReactions map[int64][]pendingReaction

	// wechatSessions holds in-flight WeChat QR-login handshakes keyed by the
	// sha256 of the onboarding token. Ephemeral and process-local (the zero-value
	// sync.Map is ready to use); a lost session just makes the user restart the
	// QR flow. See imbot_wechat_login.go.
	wechatSessions sync.Map
}

// TaskRouter decides continue-vs-new in a project and provisions the
// issue+workspace when new. *AssistantDispatchService satisfies it; the seam
// lets tests inject a fake without spinning up real workspace creation.
type TaskRouter interface {
	RouteInProject(ctx context.Context, owner OwnerRef, projectID int64, text string, hint RouteHint) (PlanTarget, error)
}

// MessageDeliverer forwards a routed inbound message into a workspace's agent
// session. *agentproxy.AgentProxy satisfies it; the interface keeps the service
// package free of an agentproxy import cycle.
type MessageDeliverer interface {
	Deliver(ctx context.Context, workspaceID int64, workDir, content, attachments string) (queued bool, queueID int64, err error)
}

// PermissionDecider writes back an IM permission-button decision.
// *PermissionService satisfies it.
type PermissionDecider interface {
	Decide(ctx context.Context, requestID int64, d Decision) error
}

// AskUserDecider writes back an IM ask-user-question answer (the user tapped an
// option button on the pushed question card). *AskUserService satisfies it. Wired
// via SetAskUserDecider after construction; nil leaves ask_user requests as a
// "请到牛牛里查看" nudge (no in-IM answering).
type AskUserDecider interface {
	Decide(ctx context.Context, requestID int64, d AskUserDecision) error
}

// TaskDeleter deletes an issue and its backing workspace within a project (the
// /delete command). *AssistantDispatchService satisfies it — the same object
// wired as the TaskRouter — so SetInbound captures it without extra wiring; the
// seam also lets tests inject a fake. stop, when non-nil, terminates each
// workspace's running agent session before its on-disk cleanup.
type TaskDeleter interface {
	DeleteTask(ctx context.Context, projectID, issueID int64, stop func(context.Context, int64)) error
}

// IssueWorkspaceStarter provisions a backing workspace for an existing issue in a
// project (the IM `#<id>` "start work on a kanban issue" path).
// *AssistantDispatchService satisfies it — the same object wired as the
// TaskRouter — so SetInbound captures it via a capability assertion.
type IssueWorkspaceStarter interface {
	StartWorkspaceForExistingIssue(ctx context.Context, owner OwnerRef, projectID, issueID int64) (PlanTarget, error)
}

// AgentSessionStopper terminates a workspace's running agent session so the
// workspace can be deleted without tearing the directory out from under a live
// agent. *agentproxy.AgentProxy satisfies it (the same object wired as the
// MessageDeliverer), so SetInbound captures it via a capability assertion.
type AgentSessionStopper interface {
	RemoveSession(ctx context.Context, workspaceID int64)
}

// SetInbound wires the W2 inbound collaborators (routing core, agent delivery,
// permission callback). Called from server.go after the proxy/permission
// services exist. Any may be nil in tests that exercise only a subset.
func (s *IMBotService) SetInbound(dispatch TaskRouter, deliverer MessageDeliverer, perm PermissionDecider) {
	s.dispatch = dispatch
	s.deliverer = deliverer
	s.perm = perm
	// Capture the /delete capabilities from the same objects when they provide
	// them: the dispatch service also deletes issue+workspace, and the agentproxy
	// deliverer can stop a workspace's agent session. A fake that lacks a
	// capability simply leaves it nil.
	if d, ok := dispatch.(TaskDeleter); ok {
		s.deleter = d
	}
	if st, ok := deliverer.(AgentSessionStopper); ok {
		s.stopper = st
	}
	if ws, ok := dispatch.(IssueWorkspaceStarter); ok {
		s.starter = ws
	}
}

// NewIMBotService wires the dependencies. adapters is the same registry handed
// to the ConnectorManager so test/welcome pushes reuse the exact adapters.
func NewIMBotService(q *store.Queries, rawDB *sql.DB, kr *crypto.Keyring, authz *Authz, adapters map[imbot.ChannelType]imbot.ChannelAdapter) *IMBotService {
	return &IMBotService{q: q, db: store.Wrap(rawDB), keyring: kr, authz: authz, adapters: adapters}
}

// SetAskUserDecider wires the ask-user answer write-back so a user can answer an
// agent's question by tapping an option button in the chat. Optional (nil in
// tests / when not wired) — ask_user then degrades to a "请到牛牛里查看" nudge.
func (s *IMBotService) SetAskUserDecider(a AskUserDecider) { s.askUser = a }

// SetConnectorManager attaches the manager so CRUD can hot-reload connections.
func (s *IMBotService) SetConnectorManager(m *imbot.ConnectorManager) { s.mgr = m }

// WorkspacePath returns the filesystem path of a workspace, used by callers
// that need to pass a real workDir to MessageDeliverer.Deliver. Returns an
// empty string and a non-nil error when the workspace is not found.
func (s *IMBotService) WorkspacePath(ctx context.Context, workspaceID int64) (string, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return ws.Path, nil
}

// --- DTOs (never carry plaintext credentials) ---

type IMBotChannelDTO struct {
	ID             int64     `json:"id"`
	OwnerType      string    `json:"owner_type"`
	OwnerID        int64     `json:"owner_id"`
	ChannelType    string    `json:"channel_type"`
	Name           string    `json:"name"`
	ConnectionMode string    `json:"connection_mode"`
	Status         string    `json:"status"`
	HasCredential  bool      `json:"has_credential"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type IMBotChatDTO struct {
	ID            int64     `json:"id"`
	ChannelID     int64     `json:"channel_id"`
	ProjectID     *int64    `json:"project_id"`
	ChatExtID     string    `json:"chat_ext_id"`
	ChatName      string    `json:"chat_name"`
	BindMode      string    `json:"bind_mode"`
	PinnedIssueID *int64    `json:"pinned_issue_id"`
	ActiveIssueID *int64    `json:"active_issue_id"`
	Status        string    `json:"status"`
	PairedBy      *int64    `json:"paired_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func channelDTO(r store.ImBotChannel) IMBotChannelDTO {
	return IMBotChannelDTO{
		ID:             r.ID,
		OwnerType:      r.OwnerType,
		OwnerID:        r.OwnerID,
		ChannelType:    r.ChannelType,
		Name:           r.Name,
		ConnectionMode: r.ConnectionMode,
		Status:         r.Status,
		HasCredential:  r.CredentialEnc != "",
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func chatDTO(r store.ImBotChat) IMBotChatDTO {
	return IMBotChatDTO{
		ID:            r.ID,
		ChannelID:     r.ChannelID,
		ProjectID:     nullInt64Ptr(r.ProjectID),
		ChatExtID:     r.ChatExtID,
		ChatName:      r.ChatName,
		BindMode:      r.BindMode,
		PinnedIssueID: nullInt64Ptr(r.PinnedIssueID),
		ActiveIssueID: nullInt64Ptr(r.ActiveIssueID),
		Status:        r.Status,
		PairedBy:      nullInt64Ptr(r.PairedBy),
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// credentialFingerprint derives a stable, non-reversible SHA-256 (hex) of the
// app-identity fields of a credential, per platform. It is used to forbid a
// second bot/connection for the same app under one owner. NEVER a hash of the
// full secret — only the identity (app_id / token / client_id / corp:agent), so
// two channels for the same app collide while different apps do not. Returns ""
// when the identity fields are absent (unknown type / empty cred), which the
// caller treats as "no fingerprint" (skips the dedupe check, leaves column '').
func credentialFingerprint(channelType string, cred map[string]any) string {
	get := func(k string) string {
		if cred == nil {
			return ""
		}
		v, _ := cred[k].(string)
		return strings.TrimSpace(v)
	}
	var identity string
	switch imbot.ChannelType(channelType) {
	case imbot.ChannelLark:
		identity = get("app_id")
	case imbot.ChannelTelegram:
		identity = get("token")
	case imbot.ChannelDingTalk:
		identity = get("client_id")
	case imbot.ChannelWework:
		// 智能机器人 (AI-Bot) stream channels are identified by bot_id (one live
		// long connection per bot); self-built-app channels by corp_id:agent_id.
		if bot := get("bot_id"); bot != "" {
			identity = "bot:" + bot
		} else if corp, agent := get("corp_id"), get("agent_id"); corp != "" || agent != "" {
			identity = corp + ":" + agent
		}
	case imbot.ChannelWechat:
		// The QR-login-minted bot identity (ilink_bot_id) uniquely names the
		// connected WeChat account; a second channel for the same bot would fight
		// it for the getupdates long-poll.
		identity = get("account_id")
	}
	if identity == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(channelType + ":" + identity))
	return hex.EncodeToString(sum[:])
}

func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func ptrToNullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// --- credential crypto ---

func (s *IMBotService) encryptCred(config map[string]any) (string, error) {
	if config == nil {
		config = map[string]any{}
	}
	plain, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	ct, err := s.keyring.Encrypt(plain)
	if err != nil {
		return "", err
	}
	return string(ct), nil
}

func (s *IMBotService) decryptCred(channelType, enc string) (imbot.Credential, error) {
	cred := imbot.Credential{Channel: imbot.ChannelType(channelType), Config: map[string]any{}}
	if enc == "" {
		return cred, nil
	}
	plain, err := s.keyring.Decrypt([]byte(enc))
	if err != nil {
		return cred, err
	}
	if err := json.Unmarshal(plain, &cred.Config); err != nil {
		return cred, err
	}
	return cred, nil
}

// BackfillCredentialFingerprints computes and stores credential_fingerprint for
// channels created before the fingerprint dedup existed (empty fingerprint), so
// the one-bot-per-app UNIQUE constraint becomes enforceable and a second channel
// for the same app is blocked at creation. A channel whose fingerprint would
// collide with an existing one is a leftover duplicate connection: it is left
// empty and logged (never auto-deleted) so an admin can reconcile it. Best-effort
// and idempotent — safe to run on every startup.
func (s *IMBotService) BackfillCredentialFingerprints(ctx context.Context) {
	rows, err := s.q.ListIMBotChannelsMissingFingerprint(ctx)
	if err != nil {
		slog.Warn("imbot: fingerprint backfill list failed", "error", err)
		return
	}
	for _, ch := range rows {
		cred, derr := s.decryptCred(ch.ChannelType, ch.CredentialEnc)
		if derr != nil {
			slog.Warn("imbot: fingerprint backfill decrypt failed", "channel", ch.ID, "error", derr)
			continue
		}
		fp := credentialFingerprint(ch.ChannelType, cred.Config)
		if fp == "" {
			continue // credential lacks identity fields; nothing to fingerprint
		}
		// A pre-existing channel with this fingerprint means two channels share one
		// app (the duplicate-connection bug). Don't trip the UNIQUE index; surface it.
		if dup, e := s.q.GetIMBotChannelByFingerprint(ctx, store.GetIMBotChannelByFingerprintParams{
			OwnerType: ch.OwnerType, OwnerID: ch.OwnerID, ChannelType: ch.ChannelType, CredentialFingerprint: fp,
		}); e == nil && dup.ID != ch.ID {
			slog.Warn("imbot: duplicate bot channel for the same app — remove one in settings",
				"channel", ch.ID, "duplicate_of", dup.ID, "owner_type", ch.OwnerType, "owner_id", ch.OwnerID)
			continue
		}
		if e := s.q.SetIMBotChannelFingerprint(ctx, store.SetIMBotChannelFingerprintParams{
			CredentialFingerprint: fp, ID: ch.ID,
		}); e != nil {
			slog.Warn("imbot: fingerprint backfill set failed", "channel", ch.ID, "error", e)
		}
	}
}

// --- channel CRUD ---

func (s *IMBotService) ListChannels(ctx context.Context, projectID int64) ([]IMBotChannelDTO, error) {
	rows, err := s.q.ListIMBotChannelsByProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]IMBotChannelDTO, len(rows))
	for i, r := range rows {
		out[i] = channelDTO(r)
	}
	return out, nil
}

type CreateChannelInput struct {
	ChannelType    string
	Name           string
	ConnectionMode string
	WebhookSecret  string
	Credential     map[string]any
}

// validateWebhookConfig enforces defense-in-depth at write time: a webhook-mode
// Lark/Telegram channel MUST carry a webhook secret (the Lark Verification Token
// / Telegram secret_token). Without it VerifyWebhook fails closed on every
// request, so the channel would be dead while still inviting forged-request
// probing — reject the misconfiguration up front. WeCom (webhook-only) keeps its
// token/aes_key in the credential blob rather than the WebhookSecret column, so
// it is exempt from this column check.
func validateWebhookConfig(channelType, mode, webhookSecret string) error {
	if mode != "webhook" {
		return nil
	}
	switch channelType {
	case string(imbot.ChannelLark), string(imbot.ChannelTelegram):
		if strings.TrimSpace(webhookSecret) == "" {
			return fmt.Errorf("%w: %s webhook mode requires a webhook secret", ErrInvalidChannelConfig, channelType)
		}
	}
	return nil
}

// CreateChannel creates an owner-level bot (channel). The bot belongs to owner
// and to no project; all of the owner's projects are peers and reach it through
// the chats routed to them. Callers derive owner from their context (an owner-level
// route, or the project whose panel/onboarding kicked off the create).
func (s *IMBotService) CreateChannel(ctx context.Context, owner OwnerRef, in CreateChannelInput) (IMBotChannelDTO, error) {
	mode := in.ConnectionMode
	if mode == "" {
		mode = "stream"
	}
	if err := validateWebhookConfig(in.ChannelType, mode, in.WebhookSecret); err != nil {
		return IMBotChannelDTO{}, err
	}
	if owner.Type == "" {
		return IMBotChannelDTO{}, ErrIMBotNotFound
	}
	// Fingerprint the app identity (non-plaintext) so a second bot for the same
	// app under this owner is rejected before it opens a rival connection.
	fp := credentialFingerprint(in.ChannelType, in.Credential)
	if fp != "" {
		if _, derr := s.q.GetIMBotChannelByFingerprint(ctx, store.GetIMBotChannelByFingerprintParams{
			OwnerType:             owner.Type,
			OwnerID:               owner.ID,
			ChannelType:           in.ChannelType,
			CredentialFingerprint: fp,
		}); derr == nil {
			return IMBotChannelDTO{}, ErrDuplicateIMBotCredential
		} else if !errors.Is(derr, sql.ErrNoRows) {
			return IMBotChannelDTO{}, derr
		}
	}
	enc, err := s.encryptCred(in.Credential)
	if err != nil {
		return IMBotChannelDTO{}, err
	}
	row, err := s.q.CreateIMBotChannel(ctx, store.CreateIMBotChannelParams{
		OwnerType:             owner.Type,
		OwnerID:               owner.ID,
		CredentialFingerprint: fp,
		ChannelType:           in.ChannelType,
		Name:                  in.Name,
		ConnectionMode:        mode,
		CredentialEnc:         enc,
		WebhookSecret:         in.WebhookSecret,
		Status:                "active",
	})
	if err != nil {
		return IMBotChannelDTO{}, err
	}
	s.reload(ctx, row.ID)
	return channelDTO(row), nil
}

type UpdateChannelInput struct {
	Name           string
	ConnectionMode string
	WebhookSecret  string
	Status         string
	Credential     map[string]any // nil = keep existing
}

func (s *IMBotService) UpdateChannel(ctx context.Context, owner OwnerRef, id int64, in UpdateChannelInput) (IMBotChannelDTO, error) {
	cur, err := s.getOwnedChannelByOwner(ctx, owner, id)
	if err != nil {
		return IMBotChannelDTO{}, err
	}
	name := in.Name
	if name == "" {
		name = cur.Name
	}
	mode := in.ConnectionMode
	if mode == "" {
		mode = cur.ConnectionMode
	}
	// Preserve the stored webhook secret when the caller omits it (empty), the
	// same preserve-on-empty contract as name/mode/status. Otherwise a name-only
	// edit (e.g. the rename affordance) would blank the secret and break a
	// webhook-mode bot. There is no UI path that intentionally clears it.
	webhookSecret := in.WebhookSecret
	if webhookSecret == "" {
		webhookSecret = cur.WebhookSecret
	}
	if err := validateWebhookConfig(cur.ChannelType, mode, webhookSecret); err != nil {
		return IMBotChannelDTO{}, err
	}
	status := in.Status
	if status == "" {
		status = cur.Status
	}
	row, err := s.q.UpdateIMBotChannel(ctx, store.UpdateIMBotChannelParams{
		Name:           name,
		ConnectionMode: mode,
		WebhookSecret:  webhookSecret,
		Status:         status,
		ID:             id,
	})
	if err != nil {
		return IMBotChannelDTO{}, err
	}
	if in.Credential != nil {
		// Re-derive the app-identity fingerprint from the new credential and
		// re-flush it. Without this, updating a bot's credential to a different
		// app leaves the stale fingerprint in place, so a second bot for the NEW
		// app would slip past the create-time dedupe check and open a rival
		// connection — the exact double-connection the fingerprint UNIQUE is meant
		// to forbid (design §3.1/§6). Guard against colliding with a *different*
		// existing bot for the same owner + new app identity.
		fp := credentialFingerprint(cur.ChannelType, in.Credential)
		if fp != "" && fp != cur.CredentialFingerprint {
			if other, derr := s.q.GetIMBotChannelByFingerprint(ctx, store.GetIMBotChannelByFingerprintParams{
				OwnerType:             cur.OwnerType,
				OwnerID:               cur.OwnerID,
				ChannelType:           cur.ChannelType,
				CredentialFingerprint: fp,
			}); derr == nil {
				if other.ID != id {
					return IMBotChannelDTO{}, ErrDuplicateIMBotCredential
				}
			} else if !errors.Is(derr, sql.ErrNoRows) {
				return IMBotChannelDTO{}, derr
			}
		}
		enc, err := s.encryptCred(in.Credential)
		if err != nil {
			return IMBotChannelDTO{}, err
		}
		if err := s.q.UpdateIMBotChannelCredential(ctx, store.UpdateIMBotChannelCredentialParams{CredentialEnc: enc, ID: id}); err != nil {
			return IMBotChannelDTO{}, err
		}
		if fp != cur.CredentialFingerprint {
			if err := s.q.SetIMBotChannelFingerprint(ctx, store.SetIMBotChannelFingerprintParams{CredentialFingerprint: fp, ID: id}); err != nil {
				return IMBotChannelDTO{}, err
			}
			row.CredentialFingerprint = fp
		}
		row.CredentialEnc = enc
	}
	s.reload(ctx, id)
	return channelDTO(row), nil
}

func (s *IMBotService) DeleteChannel(ctx context.Context, owner OwnerRef, id int64) error {
	if _, err := s.getOwnedChannelByOwner(ctx, owner, id); err != nil {
		return err
	}
	if err := s.q.DeleteIMBotChannel(ctx, id); err != nil {
		return err
	}
	if s.mgr != nil {
		s.mgr.StopChannel(id)
	}
	return nil
}

// TestChannel validates connectivity: it decrypts the credential and, when the
// adapter supports it, verifies without sending a message.
func (s *IMBotService) TestChannel(ctx context.Context, owner OwnerRef, id int64) error {
	ch, err := s.getOwnedChannelByOwner(ctx, owner, id)
	if err != nil {
		return err
	}
	adapter, ok := s.adapters[imbot.ChannelType(ch.ChannelType)]
	if !ok {
		return fmt.Errorf("imbot: unsupported channel type %q", ch.ChannelType)
	}
	cred, err := s.decryptCred(ch.ChannelType, ch.CredentialEnc)
	if err != nil {
		return fmt.Errorf("imbot: decrypt credential: %w", err)
	}
	if v, ok := adapter.(imbot.CredentialVerifier); ok {
		return v.VerifyCredential(ctx, cred)
	}
	return fmt.Errorf("imbot: %s does not support connectivity test", ch.ChannelType)
}

// getOwnedChannelByOwner loads a channel and enforces it belongs to owner,
// mapping a missing row or a cross-owner channel to ErrIMBotNotFound (hide
// existence). This is the owner-level authorization used by channel CRUD now
// that channels carry no project_id.
func (s *IMBotService) getOwnedChannelByOwner(ctx context.Context, owner OwnerRef, id int64) (store.ImBotChannel, error) {
	row, err := s.q.GetIMBotChannel(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ImBotChannel{}, ErrIMBotNotFound
		}
		return store.ImBotChannel{}, err
	}
	if row.OwnerType != owner.Type || row.OwnerID != owner.ID {
		return store.ImBotChannel{}, ErrIMBotNotFound
	}
	return row, nil
}

// --- chat pairing ---

func (s *IMBotService) ListChats(ctx context.Context, projectID int64) ([]IMBotChatDTO, error) {
	rows, err := s.q.ListIMBotChatsByProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]IMBotChatDTO, len(rows))
	for i, r := range rows {
		out[i] = chatDTO(r)
	}
	return out, nil
}

// AddChat registers a chat under a channel in pending state so an admin can
// approve it. In W1 (no inbound yet) this is how a pairing is seeded; in W2 the
// connector will create pending chats automatically on first inbound message.
func (s *IMBotService) AddChat(ctx context.Context, owner OwnerRef, channelID int64, chatExtID, chatName string) (IMBotChatDTO, error) {
	if _, err := s.getOwnedChannelByOwner(ctx, owner, channelID); err != nil {
		return IMBotChatDTO{}, err
	}
	row, err := s.q.CreateIMBotChat(ctx, store.CreateIMBotChatParams{
		ChannelID: channelID,
		ChatExtID: chatExtID,
		ChatName:  chatName,
		Status:    "pending",
	})
	if err != nil {
		return IMBotChatDTO{}, err
	}
	return chatDTO(row), nil
}

// ApproveChat admits a pending chat into the given project. It is the
// project-scoped entry point (back-compat): the chat's bot must belong to this
// project, and the chat is routed to this project. It delegates to
// ApproveChatToProject so both paths share the same-owner + writable check.
func (s *IMBotService) ApproveChat(ctx context.Context, projectID, chatID, userID int64) (IMBotChatDTO, error) {
	// Ownership gate through the channel<->project link (existing semantics).
	if _, err := s.getOwnedChat(ctx, projectID, chatID); err != nil {
		return IMBotChatDTO{}, err
	}
	return s.ApproveChatToProject(ctx, chatID, projectID, userID)
}

// ApproveChatToProject admits a pending chat and routes it to projectID (the
// design A1 flow: approval chooses the target project). It enforces that the
// chat's bot and the target project share the same owner, and that the caller
// may write to that owner. On success it stamps project_id + status=active +
// paired_by and best-effort pushes the welcome message.
func (s *IMBotService) ApproveChatToProject(ctx context.Context, chatID, projectID, userID int64) (IMBotChatDTO, error) {
	chat, channel, err := s.chatWithChannel(ctx, chatID)
	if err != nil {
		return IMBotChatDTO{}, err
	}
	if err := s.ensureSameOwnerWritable(ctx, channel, projectID, userID); err != nil {
		return IMBotChatDTO{}, err
	}
	row, err := s.q.ApproveIMBotChatToProject(ctx, store.ApproveIMBotChatToProjectParams{
		ProjectID: sql.NullInt64{Int64: projectID, Valid: true},
		PairedBy:  sql.NullInt64{Int64: userID, Valid: userID > 0},
		ID:        chatID,
	})
	if err != nil {
		return IMBotChatDTO{}, err
	}
	s.pushWelcome(ctx, chat.ChannelID, row.ChatExtID)
	return chatDTO(row), nil
}

// ReassignChat moves an already-paired chat to a different project (design §7
// 改派). Same-owner + target-writable check, then updates project_id only.
func (s *IMBotService) ReassignChat(ctx context.Context, chatID, newProjectID, userID int64) (IMBotChatDTO, error) {
	_, channel, err := s.chatWithChannel(ctx, chatID)
	if err != nil {
		return IMBotChatDTO{}, err
	}
	if err := s.ensureSameOwnerWritable(ctx, channel, newProjectID, userID); err != nil {
		return IMBotChatDTO{}, err
	}
	row, err := s.q.ReassignIMBotChat(ctx, store.ReassignIMBotChatParams{
		ProjectID: sql.NullInt64{Int64: newProjectID, Valid: true},
		ID:        chatID,
	})
	if err != nil {
		return IMBotChatDTO{}, err
	}
	return chatDTO(row), nil
}

// chatWithChannel loads a chat plus its owning channel, mapping missing rows to
// ErrIMBotNotFound.
func (s *IMBotService) chatWithChannel(ctx context.Context, chatID int64) (store.ImBotChat, store.ImBotChannel, error) {
	chat, err := s.q.GetIMBotChat(ctx, chatID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ImBotChat{}, store.ImBotChannel{}, ErrIMBotNotFound
		}
		return store.ImBotChat{}, store.ImBotChannel{}, err
	}
	channel, err := s.q.GetIMBotChannel(ctx, chat.ChannelID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ImBotChat{}, store.ImBotChannel{}, ErrIMBotNotFound
		}
		return store.ImBotChat{}, store.ImBotChannel{}, err
	}
	return chat, channel, nil
}

// ensureSameOwnerWritable enforces the design §9 authz for approval/reassign:
// the chat's bot (channel) and the target project must share the same owner, and
// the caller must be able to write to that owner. A cross-owner target maps to
// ErrIMBotNotFound (hide existence); a same-owner-but-not-writable caller maps to
// the authz error the handler renders as 403.
func (s *IMBotService) ensureSameOwnerWritable(ctx context.Context, channel store.ImBotChannel, projectID, userID int64) error {
	target, ok := s.projectOwner(ctx, projectID)
	if !ok {
		return ErrIMBotNotFound
	}
	if target.Type != channel.OwnerType || target.ID != channel.OwnerID {
		return ErrIMBotNotFound // cross-owner routing is out of scope (design §14)
	}
	if s.authz == nil {
		return nil // tests without an authz wired
	}
	return s.authz.EnsureOwnerWritable(ctx, userID, target)
}

// BotOwner returns a bot's (channel's) owner. Used by owner-level bot routes to
// authorize the caller by owner before the owner-keyed CRUD methods.
func (s *IMBotService) BotOwner(ctx context.Context, channelID int64) (owner OwnerRef, err error) {
	ch, err := s.q.GetIMBotChannel(ctx, channelID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerRef{}, ErrIMBotNotFound
		}
		return OwnerRef{}, err
	}
	return OwnerRef{Type: ch.OwnerType, ID: ch.OwnerID}, nil
}

// GetChannelByOwner returns a single bot (channel) as a DTO, enforcing it belongs
// to owner (cross-owner or missing -> ErrIMBotNotFound). Used for owner-scoped
// status lookups that must not depend on a project routing a chat to the bot.
func (s *IMBotService) GetChannelByOwner(ctx context.Context, owner OwnerRef, id int64) (IMBotChannelDTO, error) {
	row, err := s.getOwnedChannelByOwner(ctx, owner, id)
	if err != nil {
		return IMBotChannelDTO{}, err
	}
	return channelDTO(row), nil
}

// ListBotsByOwner lists all bots (channels) owned by owner. Owner-level bot CRUD
// entry (design §8): the caller is authorized at the handler via the owner.
func (s *IMBotService) ListBotsByOwner(ctx context.Context, owner OwnerRef) ([]IMBotChannelDTO, error) {
	rows, err := s.q.ListIMBotChannelsByOwner(ctx, store.ListIMBotChannelsByOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]IMBotChannelDTO, len(rows))
	for i, r := range rows {
		out[i] = channelDTO(r)
	}
	return out, nil
}

// ListPendingChatsByOwner lists every pending (awaiting approval) chat across all
// of the owner's bots — the owner-level 待配对列表 (design §7).
func (s *IMBotService) ListPendingChatsByOwner(ctx context.Context, owner OwnerRef) ([]IMBotChatDTO, error) {
	rows, err := s.q.ListPendingIMBotChatsByOwner(ctx, store.ListPendingIMBotChatsByOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]IMBotChatDTO, len(rows))
	for i, r := range rows {
		out[i] = chatDTO(r)
	}
	return out, nil
}

// ListActiveChatsByOwner lists the owner's active chat->project bindings (which
// group is routed to which project) for owner-level management (design §10).
func (s *IMBotService) ListActiveChatsByOwner(ctx context.Context, owner OwnerRef) ([]IMBotChatDTO, error) {
	rows, err := s.q.ListActiveIMBotChatsByOwner(ctx, store.ListActiveIMBotChatsByOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]IMBotChatDTO, len(rows))
	for i, r := range rows {
		out[i] = chatDTO(r)
	}
	return out, nil
}

// DeleteChatByOwner removes a chat->project binding, authorized by owner (the
// chat's bot must belong to owner). The group becomes unpaired and must be
// re-approved to route anywhere again. Cross-owner maps to ErrIMBotNotFound.
func (s *IMBotService) DeleteChatByOwner(ctx context.Context, chatID int64, owner OwnerRef) error {
	_, channel, err := s.chatWithChannel(ctx, chatID)
	if err != nil {
		return err
	}
	if channel.OwnerType != owner.Type || channel.OwnerID != owner.ID {
		return ErrIMBotNotFound
	}
	return s.q.DeleteIMBotChat(ctx, chatID)
}

type PatchChatInput struct {
	BindMode      string
	PinnedIssueID *int64
	ActiveIssueID *int64
	Status        string
}

func (s *IMBotService) PatchChat(ctx context.Context, projectID, chatID int64, in PatchChatInput) (IMBotChatDTO, error) {
	cur, err := s.getOwnedChat(ctx, projectID, chatID)
	if err != nil {
		return IMBotChatDTO{}, err
	}
	bindMode := in.BindMode
	if bindMode == "" {
		bindMode = cur.BindMode
	}
	status := in.Status
	if status == "" {
		status = cur.Status
	}
	pinned := cur.PinnedIssueID
	if in.PinnedIssueID != nil {
		pinned = ptrToNullInt64(in.PinnedIssueID)
	}
	active := cur.ActiveIssueID
	if in.ActiveIssueID != nil {
		active = ptrToNullInt64(in.ActiveIssueID)
	}
	row, err := s.q.UpdateIMBotChat(ctx, store.UpdateIMBotChatParams{
		BindMode:      bindMode,
		PinnedIssueID: pinned,
		ActiveIssueID: active,
		Status:        status,
		ID:            chatID,
	})
	if err != nil {
		return IMBotChatDTO{}, err
	}
	return chatDTO(row), nil
}

func (s *IMBotService) DeleteChat(ctx context.Context, projectID, chatID int64) error {
	if _, err := s.getOwnedChat(ctx, projectID, chatID); err != nil {
		return err
	}
	return s.q.DeleteIMBotChat(ctx, chatID)
}

func (s *IMBotService) getOwnedChat(ctx context.Context, projectID, chatID int64) (store.ImBotChat, error) {
	chat, err := s.q.GetIMBotChat(ctx, chatID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ImBotChat{}, ErrIMBotNotFound
		}
		return store.ImBotChat{}, err
	}
	// Shared bots have no owning project: a routed chat is managed ONLY by the
	// project it is routed to (chat.project_id) -- there is no home/default-project
	// privilege. Not-yet-routed (pending) chats have no project, so any project of
	// the bot's owner may approve/reject them: fall back to a same-owner check
	// between the channel and projectID. The handler already gated the caller on
	// write access to projectID.
	if chat.ProjectID.Valid {
		if chat.ProjectID.Int64 == projectID {
			return chat, nil
		}
		return store.ImBotChat{}, ErrIMBotNotFound
	}
	owner, ok := s.projectOwner(ctx, projectID)
	if !ok {
		return store.ImBotChat{}, ErrIMBotNotFound
	}
	if _, err := s.getOwnedChannelByOwner(ctx, owner, chat.ChannelID); err != nil {
		return store.ImBotChat{}, err
	}
	return chat, nil
}

func (s *IMBotService) pushWelcome(ctx context.Context, channelID int64, chatExtID string) {
	ch, err := s.q.GetIMBotChannel(ctx, channelID)
	if err != nil {
		return
	}
	adapter, ok := s.adapters[imbot.ChannelType(ch.ChannelType)]
	if !ok {
		return
	}
	cred, err := s.decryptCred(ch.ChannelType, ch.CredentialEnc)
	if err != nil {
		return
	}
	if err := adapter.Push(ctx, cred, imbot.OutboundMessage{
		ChatExtID: chatExtID,
		Text:      welcomeGuideText,
	}); err != nil {
		slog.Warn("imbot: welcome push failed", "channel", channelID, "error", err)
	}
}

// welcomeGuideText is the short usage guide pushed to a chat the moment it is
// paired (design: onboarding闭环的最后一步——让用户一眼看懂怎么用)。用 markdown，
// 飞书等平台走 interactive 卡片会渲染成排版文本。措辞用「您」、句末单个「。」。
const welcomeGuideText = `✅ 已完成配对，现在可以直接在这里给牛牛派活了。

**怎么用：**
- 直接发一句话 = 新建一个任务，牛牛开始为您工作。
- 切到别的任务：发 ` + "`#任务编号`" + `（例如 #123）。
- 查看 / 切换全部任务：发 ` + "`/issues`" + `。
- 查看某个任务详情：发 ` + "`/detail #编号`" + `。
- 停止当前任务的执行（不删除）：发 ` + "`/stop`" + `。
- 删除某个任务及其工作空间：发 ` + "`/delete 编号`" + `（例如 /delete 1 或 /delete #123）。
- 图片、文件、语音、视频都能发给牛牛，会一并转交处理。

试着发一句话开始吧。`

func (s *IMBotService) reload(ctx context.Context, id int64) {
	if s.mgr == nil {
		return
	}
	if err := s.mgr.ReloadChannel(ctx, id); err != nil {
		slog.Warn("imbot: reload channel failed", "channel", id, "error", err)
	}
}

// --- imbot.ChannelProvider implementation (for ConnectorManager) ---

// ActiveStreamChannels implements imbot.ChannelProvider: every active stream
// channel across all projects, credentials decrypted.
func (s *IMBotService) ActiveStreamChannels(ctx context.Context) ([]imbot.ManagedChannel, error) {
	rows, err := s.q.ListActiveStreamChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]imbot.ManagedChannel, 0, len(rows))
	for _, r := range rows {
		mc, ok := s.toManaged(r)
		if ok {
			out = append(out, mc)
		}
	}
	return out, nil
}

// ChannelByID implements imbot.ChannelProvider: returns ok=false unless the
// channel currently qualifies as an active stream channel.
func (s *IMBotService) ChannelByID(ctx context.Context, id int64) (imbot.ManagedChannel, bool, error) {
	r, err := s.q.GetIMBotChannel(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return imbot.ManagedChannel{}, false, nil
		}
		return imbot.ManagedChannel{}, false, err
	}
	if r.Status != "active" || r.ConnectionMode != "stream" {
		return imbot.ManagedChannel{}, false, nil
	}
	mc, ok := s.toManaged(r)
	return mc, ok, nil
}

func (s *IMBotService) toManaged(r store.ImBotChannel) (imbot.ManagedChannel, bool) {
	cred, err := s.decryptCred(r.ChannelType, r.CredentialEnc)
	if err != nil {
		slog.Warn("imbot: decrypt credential for connector failed", "channel", r.ID, "error", err)
		return imbot.ManagedChannel{}, false
	}
	return imbot.ManagedChannel{
		ID:   r.ID,
		Type: imbot.ChannelType(r.ChannelType),
		Cred: cred,
	}, true
}
