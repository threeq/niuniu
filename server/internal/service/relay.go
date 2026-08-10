// Package service: RelayService owns the full relay-account + mobile-pairing
// lifecycle on the server side.  Safe for multi-user niuniu-server
// deployments: every piece of state is scoped per niuniu user.
//
// Per-scope isolation
//
// Every externally-visible method takes a `scope string` that identifies the
// caller.  A scope is an opaque ID chosen by the HTTP layer (typically the
// niuniu auth user_id formatted as "%d"; "default" when auth is disabled).
//
// For each scope we keep an independent:
//   • credstore entry in the OS keychain  (relay-account:<scope>)
//   • long-term identity keypair          (relay-identity:<scope>)
//   • trusted-mobiles SQLite row subset   (scope column)
//   • running relayclient.Tunnel goroutine
//   • single-ceremony pairing slot
//
// One user's Login/Logout/pair action does not touch another user's state.
//
// Active-scope persistence
//
// We don't enumerate keychain entries; instead we keep a tiny JSON file at
// ~/.niuniu/relay/scopes.json listing scopes that currently have credentials.
// On Start() we iterate the list and boot one tunnel per scope.  Login adds
// a scope to the list; Logout removes it.
//
// Lifecycle:  srv := NewRelayService(); srv.SetLocalBaseURL(url); srv.Start(ctx); ... srv.Stop()
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
	"github.com/niuniu-dev/niuniu/internal/credstore"
	"github.com/niuniu-dev/niuniu/internal/lanhost"
	"github.com/niuniu-dev/niuniu/internal/relayclient"
	"rsc.io/qr"
)

const (
	defaultRelayURL = "https://niuniu-relay.niu6ai.com"

	// DefaultScope is the scope used when auth is disabled (personal edition).
	DefaultScope = "default"
)

// RelayStatus is what GetStatus returns to the frontend.
type RelayStatus struct {
	LoggedIn  bool   `json:"logged_in"`
	Email     string `json:"email,omitempty"`
	URL       string `json:"url"`
	Connected bool   `json:"connected"` // tunnel goroutine alive
}

// PairingState is polled by the frontend while a ceremony is running.
type PairingState struct {
	Phase      string `json:"phase"` // idle | starting | waiting_claim | ready_to_confirm | confirmed | failed
	QRPNG      string `json:"qr_png,omitempty"`
	QRURL      string `json:"qr_url,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	SAS        string `json:"sas,omitempty"`
	MobileName string `json:"mobile_name,omitempty"`
	Error      string `json:"error,omitempty"`
	// ErrorCode is a stable machine-readable tag when Phase == "failed" so the
	// UI can branch on it (e.g. show a "resend verification email" button for
	// "email_verification_required"). Human-readable text stays in Error.
	ErrorCode string `json:"error_code,omitempty"`
}

// TrustedMobileInfo is the JSON shape returned to the frontend.
type TrustedMobileInfo struct {
	XpubHex    string `json:"xpub_hex"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	PairedAt   string `json:"paired_at"`
	LastSeenAt string `json:"last_seen_at"`
}

// normalizeScope collapses empty scopes to DefaultScope so every code path
// deals with a non-empty ID.
func normalizeScope(s string) string {
	if s == "" {
		return DefaultScope
	}
	return s
}

// --- userState -------------------------------------------------------------

// userState owns one scope's tunnel + pairing slot + globals.  Every field
// is guarded either by the struct's own mutex or is set once at construction.
type userState struct {
	scope string

	authMu sync.Mutex
	// Access-token cache. Without this, each billing/mobile call triggered a
	// fresh POST /api/accounts/login on the relay; a single BillingPanel open
	// fires 3-4 parallel calls, which combined with the relay's 10/minute
	// login rate-limit quickly produced "status 429" on otherwise-successful
	// logins. Guarded by authMu.
	cachedAccess    string
	cachedAccessExp time.Time
	cachedAccessURL string
	// Short-lived login-error cache. If Login() has just failed, concurrent
	// authedClient callers return the same error rather than each hammering
	// the relay's login endpoint — which would self-perpetuate a 429 storm
	// (every fresh call triggers the same rate-limit, keeping the window
	// pegged). TTL is intentionally small: just long enough to absorb a burst
	// of parallel calls triggered by one page render, short enough that
	// transient errors clear without needing a user retry.
	cachedLoginErr    error
	cachedLoginErrExp time.Time

	opMu         sync.Mutex
	lifeMu       sync.Mutex
	tunnelCancel context.CancelFunc
	tunnelDone   chan struct{}

	globalsMu sync.RWMutex
	proxy     *relayclient.Proxy
	lanServer *lanhost.Server

	pairMu        sync.Mutex
	pairGen       uint64
	pairState     PairingState
	pairCancelled bool
	pairNonce     []byte
	pairMobileX   []byte
	pairMobileE   []byte
	pairSessionID string
}

// accessCacheTTL is how long a freshly-issued relay access token is considered
// fresh enough to reuse without re-logging-in. Conservative — the relay's JWT
// access token lives longer than this, so we refresh well before expiry; and
// short enough that rotated credentials surface within the window.
const accessCacheTTL = 10 * time.Minute

// loginErrorCacheTTL is how long a failed Login() result is remembered so
// concurrent authedClient callers don't all hammer /api/accounts/login when
// one caller has already discovered the credentials are bad or the relay is
// rate-limiting us. Short enough that transient failures recover on the next
// user action, long enough to absorb the 3-4 parallel fetches that a single
// BillingPanel open produces.
const loginErrorCacheTTL = 3 * time.Second

func newUserState(scope string) *userState {
	return &userState{
		scope:     scope,
		pairState: PairingState{Phase: "idle"},
	}
}

// --- Manager (RelayService) ------------------------------------------------

// RelayService is the per-process manager.  HTTP handlers resolve the caller's
// scope and dispatch here; the manager keeps one userState per scope.
type RelayService struct {
	ctx            context.Context
	localBaseURL   string
	lanHostEnabled bool

	mu    sync.Mutex
	users map[string]*userState
}

// NewRelayService returns an idle manager with no scopes loaded.  Call
// SetLocalBaseURL then Start to boot the persisted scopes' tunnels.
func NewRelayService() *RelayService {
	return &RelayService{users: map[string]*userState{}}
}

// SetLocalBaseURL records the URL the relay tunnel should forward inbound
// mobile requests to (i.e. where the local HTTP server is listening).
func (s *RelayService) SetLocalBaseURL(u string) {
	s.localBaseURL = strings.TrimRight(u, "/")
}

// EnableLanHost toggles the optional LAN direct-connect listener.  Applies
// to all scopes.
func (s *RelayService) EnableLanHost(on bool) { s.lanHostEnabled = on }

// getOrCreateUser returns (and registers) the state for a scope.
func (s *RelayService) getOrCreateUser(scope string) *userState {
	scope = normalizeScope(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[scope]
	if u == nil {
		u = newUserState(scope)
		s.users[scope] = u
	}
	return u
}

// Start boots tunnels for every persisted scope that has complete creds.
// Safe to call before SetLocalBaseURL; tunnel startups are skipped and logged
// if the base URL is missing.
func (s *RelayService) Start(ctx context.Context) {
	s.ctx = ctx
	scopes, err := loadPersistedScopes()
	if err != nil {
		slog.Warn("relay: load scopes list failed", "error", err)
		return
	}
	if len(scopes) == 0 {
		slog.Info("relay: no persisted scopes; waiting for first login")
		return
	}
	if s.localBaseURL == "" {
		slog.Warn("relay: local base URL not set; deferring tunnel start for scopes", "n", len(scopes))
		return
	}
	for _, scope := range scopes {
		creds, err := credstore.Load(scope)
		if err != nil {
			slog.Warn("relay: skipping scope with missing creds", "scope", scope, "error", err)
			_ = removePersistedScope(scope)
			continue
		}
		if creds == nil || creds.Email == "" || creds.RefreshToken == "" {
			// Treat legacy password-only entries (RefreshToken empty) as
			// logged-out; the user must reauthenticate via email-code.
			slog.Warn("relay: skipping scope with incomplete creds", "scope", scope)
			continue
		}
		u := s.getOrCreateUser(scope)
		slog.Info("relay: starting tunnel", "scope", scope, "email", creds.Email, "url", creds.URL)
		s.startTunnel(u, creds)
	}
}

// Stop terminates all running tunnel goroutines.  Safe to call many times.
func (s *RelayService) Stop() {
	s.mu.Lock()
	users := make([]*userState, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}
	s.mu.Unlock()
	for _, u := range users {
		s.stopTunnel(u)
	}
}

// --- tunnel lifecycle ------------------------------------------------------

func (s *RelayService) startTunnel(u *userState, creds *credstore.Creds) {
	u.opMu.Lock()
	defer u.opMu.Unlock()
	u.lifeMu.Lock()
	if u.tunnelCancel != nil {
		u.lifeMu.Unlock()
		return
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	tunnelCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	u.tunnelCancel = cancel
	u.tunnelDone = done
	u.lifeMu.Unlock()

	credsCopy := *creds
	localBase := s.localBaseURL
	lanHost := s.lanHostEnabled
	scope := u.scope

	go func() {
		defer close(done)
		defer func() {
			u.lifeMu.Lock()
			if u.tunnelDone == done {
				u.tunnelCancel = nil
				u.tunnelDone = nil
			}
			u.lifeMu.Unlock()
		}()
		s.runTunnel(tunnelCtx, u, scope, &credsCopy, localBase, lanHost)
	}()
}

func (s *RelayService) stopTunnel(u *userState) {
	u.opMu.Lock()
	defer u.opMu.Unlock()
	u.lifeMu.Lock()
	cancel := u.tunnelCancel
	done := u.tunnelDone
	u.lifeMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func (u *userState) tunnelRunning() bool {
	u.lifeMu.Lock()
	defer u.lifeMu.Unlock()
	return u.tunnelCancel != nil
}

func (s *RelayService) restartTunnel(u *userState, creds *credstore.Creds) {
	s.stopTunnel(u)
	if s.localBaseURL == "" {
		slog.Warn("relay: local base URL not set; cannot (re)start tunnel", "scope", u.scope)
		return
	}
	s.startTunnel(u, creds)
}

func (s *RelayService) runTunnel(
	ctx context.Context, u *userState, scope string,
	creds *credstore.Creds, localBaseURL string, lanHostEnabled bool,
) {
	id, created, err := relayclient.LoadOrCreateIdentity(scope)
	if err != nil {
		if errors.Is(err, relayclient.ErrMachineFingerprintMismatch) {
			slog.Error("relay: machine fingerprint mismatch — tunnel disabled", "scope", scope)
			return
		}
		slog.Warn("relay: keychain identity unavailable; using ephemeral", "scope", scope, "error", err)
		id, err = pairingcrypto.GenerateIdentity()
		if err != nil {
			slog.Error("relay: ephemeral identity generation failed", "scope", scope, "error", err)
			return
		}
		created = true
	}
	if created {
		slog.Info("relay: new identity generated", "scope", scope)
	}

	rc := relayclient.NewClient(&relayclient.Config{
		RelayURL:     creds.URL,
		Email:        creds.Email,
		RefreshToken: creds.RefreshToken,
		DesktopID:    creds.DesktopID,
		DesktopToken: creds.DesktopToken,
	})
	// Refresh + RegisterDesktop retry forever with exponential backoff up to 30s.
	// Original code returned on first failure and stranded the tunnel until
	// the next niuniu-server restart — observed when HTTPS_PROXY pointed at
	// a local proxy (Clash/Xray) that was down at startup: relay login
	// `proxyconnect tcp: dial tcp 127.0.0.1:7897: connectex: ...`, goroutine
	// exited, and bringing the proxy back up later did nothing because the
	// inner `tun.RunForeverCtx` (which DOES retry) was never reached.
	//
	// On ErrInvalidRefresh the relay has revoked the token (logout elsewhere,
	// expired, etc.) — retrying won't recover, so we clear the scope and let
	// the user reauthenticate via the email-code flow.
	if err := retryWithBackoff(ctx, func() error {
		_, err := s.refreshAndPersist(u, scope, rc)
		if errors.Is(err, relayclient.ErrInvalidRefresh) {
			slog.Warn("relay: refresh token revoked; clearing scope", "scope", scope)
			_ = credstore.Clear(scope)
			_ = removePersistedScope(scope)
			return nil // stop retrying; runTunnel will exit below
		}
		if err != nil {
			slog.Error("relay: refresh failed; will retry", "scope", scope, "error", err)
			return err
		}
		return nil
	}); err != nil {
		return // ctx cancelled
	}
	if rc.AccessToken() == "" {
		// refresh token was revoked above; nothing more to do.
		return
	}
	if rc.Cfg().DesktopToken == "" {
		name, _ := os.Hostname()
		if err := retryWithBackoff(ctx, func() error {
			if err := rc.RegisterDesktop(id, name, runtime.GOOS, runtime.GOARCH, "p1"); err != nil {
				slog.Error("relay: RegisterDesktop failed; will retry", "scope", scope, "error", err)
				return err
			}
			return nil
		}); err != nil {
			return // ctx cancelled
		}
		persistDesktopToken(scope, creds.Email, rc.Cfg().DesktopID, rc.Cfg().DesktopToken)
	}

	tun := relayclient.NewTunnel(rc.Cfg(), id)
	proxy := relayclient.NewProxyWithIdentity(localBaseURL, id)

	u.globalsMu.Lock()
	u.proxy = proxy
	u.globalsMu.Unlock()
	defer func() {
		u.globalsMu.Lock()
		if u.proxy == proxy {
			u.proxy = nil
		}
		u.globalsMu.Unlock()
	}()

	if lanHostEnabled {
		lanSrv := &lanhost.Server{
			DesktopID: rc.Cfg().DesktopID,
			Scope:     scope,
			Identity:  id,
			Proxy:     proxy,
		}
		for _, xpub := range relayclient.LoadTrustedMobileBytes(scope) {
			lanSrv.AddTrustedMobile(xpub)
		}
		u.globalsMu.Lock()
		u.lanServer = lanSrv
		u.globalsMu.Unlock()
		defer func() {
			u.globalsMu.Lock()
			if u.lanServer == lanSrv {
				u.lanServer = nil
			}
			u.globalsMu.Unlock()
		}()
		if addr, err := lanSrv.Start("0.0.0.0:0"); err != nil {
			log.Printf("lanhost start failed: %v", err)
		} else {
			log.Printf("LAN host listening on %s (scope=%s)", addr, scope)
			if tcpAddr, ok := addr.(*net.TCPAddr); ok {
				_, _ = lanhost.Publish(rc.Cfg().DesktopID, id.XPub, tcpAddr.Port)
			}
		}
	}

	slog.Info("relay tunnel connecting", "scope", scope, "desktop_id", rc.Cfg().DesktopID, "relay", creds.URL)
	tun.RunForeverCtx(ctx, proxy)
	slog.Info("relay tunnel exited", "scope", scope, "desktop_id", rc.Cfg().DesktopID)
}

// --- account methods -------------------------------------------------------

// GetStatus reports login + tunnel state for the scope.
func (s *RelayService) GetStatus(scope string) RelayStatus {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	st := RelayStatus{URL: defaultRelayURL}
	creds, err := credstore.Load(scope)
	switch {
	case err == nil && creds != nil:
		st.LoggedIn = creds.Email != ""
		st.Email = creds.Email
		if creds.URL != "" {
			st.URL = creds.URL
		}
	case errors.Is(err, credstore.ErrNotFound):
		// normal logged-out
	case err != nil:
		slog.Warn("GetStatus: credstore.Load failed", "scope", scope, "error", err)
	}
	st.Connected = u.tunnelRunning()
	return st
}

// RequestEmailCode asks the relay to send a 6-digit login code to `email`.
// `relayURL` lets the caller point at a self-hosted relay; empty falls back
// to the public default. The relay returns 200 even for unknown addresses
// (anti-enumeration), so a successful return only proves the relay accepted
// the request.
func (s *RelayService) RequestEmailCode(scope, email, relayURL string) error {
	scope = normalizeScope(scope)
	_ = s.getOrCreateUser(scope) // ensure userState exists for later VerifyEmailCode

	email = strings.TrimSpace(email)
	relayURL = strings.TrimRight(strings.TrimSpace(relayURL), "/")
	if email == "" {
		return errors.New("邮箱为必填")
	}
	if relayURL == "" {
		relayURL = defaultRelayURL
	}
	cli := relayclient.NewClient(&relayclient.Config{RelayURL: relayURL, Email: email})
	if err := cli.RequestEmailCode(email); err != nil {
		if errors.Is(err, relayclient.ErrInvalidEmail) {
			return errors.New("邮箱格式不正确")
		}
		return fmt.Errorf("发送验证码失败：%v", err)
	}
	return nil
}

// VerifyEmailCode exchanges (email, code) for refresh tokens, persists them
// for the scope, and (re)starts the tunnel.
func (s *RelayService) VerifyEmailCode(scope, email, code, relayURL string) (RelayStatus, error) {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	u.authMu.Lock()
	defer u.authMu.Unlock()

	// Drop any stale cached token from a prior account on this scope; we'll
	// repopulate on successful verify.
	u.invalidateAccessCacheLocked()

	email = strings.TrimSpace(email)
	code = strings.TrimSpace(code)
	relayURL = strings.TrimRight(strings.TrimSpace(relayURL), "/")
	if email == "" || code == "" {
		return RelayStatus{URL: defaultRelayURL}, errors.New("邮箱与验证码均为必填")
	}
	if relayURL == "" {
		relayURL = defaultRelayURL
	}

	cli := relayclient.NewClient(&relayclient.Config{RelayURL: relayURL, Email: email})
	result, err := cli.VerifyEmailCode(email, code)
	if err != nil {
		switch {
		case errors.Is(err, relayclient.ErrInvalidLoginCode):
			return RelayStatus{URL: relayURL}, errors.New("验证码不正确")
		case errors.Is(err, relayclient.ErrExpiredLoginCode):
			return RelayStatus{URL: relayURL}, errors.New("验证码已过期，请重新获取")
		case errors.Is(err, relayclient.ErrTooManyAttempts):
			return RelayStatus{URL: relayURL}, errors.New("尝试次数过多，请重新获取验证码")
		case errors.Is(err, relayclient.ErrInvalidEmail):
			return RelayStatus{URL: relayURL}, errors.New("邮箱格式不正确")
		}
		return RelayStatus{URL: relayURL}, fmt.Errorf("登录失败：%v", err)
	}

	canonicalEmail := result.Email
	if canonicalEmail == "" {
		canonicalEmail = strings.ToLower(email)
	}
	newCreds := &credstore.Creds{
		URL:          relayURL,
		Email:        canonicalEmail,
		RefreshToken: result.RefreshToken,
	}
	if err := credstore.Save(scope, newCreds); err != nil {
		return RelayStatus{URL: relayURL}, fmt.Errorf("保存凭证失败：%v", err)
	}
	if err := addPersistedScope(scope); err != nil {
		slog.Warn("relay: scopes.json update failed (verify-email); tunnel will still start but won't auto-resume after restart", "scope", scope, "error", err)
	}
	// Cache the access token just obtained so immediate follow-up calls (the
	// UI typically fetches plan+usage+mobiles right after login) don't trigger
	// a redundant /api/accounts/refresh round-trip.
	u.cachedAccess = result.AccessToken
	u.cachedAccessURL = relayURL
	u.cachedAccessExp = time.Now().Add(accessCacheTTL)
	s.restartTunnel(u, newCreds)
	return RelayStatus{
		LoggedIn:  true,
		Email:     canonicalEmail,
		URL:       relayURL,
		Connected: u.tunnelRunning(),
	}, nil
}

// Logout clears the scope's creds and stops its tunnel.  Does NOT delete the
// server-side account on the relay.
func (s *RelayService) Logout(scope string) error {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	u.authMu.Lock()
	defer u.authMu.Unlock()
	u.invalidateAccessCacheLocked()
	s.stopTunnel(u)
	if err := credstore.Clear(scope); err != nil {
		return fmt.Errorf("清除凭证失败：%v", err)
	}
	if err := removePersistedScope(scope); err != nil {
		slog.Warn("relay: scopes.json update failed (logout)", "scope", scope, "error", err)
	}
	return nil
}

// --- pairing methods -------------------------------------------------------

// StartPairing kicks off a new pairing ceremony for the scope.
func (s *RelayService) StartPairing(scope string) (PairingState, error) {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	u.pairMu.Lock()
	phase := u.pairState.Phase
	if phase != "idle" && phase != "confirmed" && phase != "failed" {
		st := u.pairState
		u.pairMu.Unlock()
		return st, fmt.Errorf("pairing already in progress")
	}
	u.pairGen++
	gen := u.pairGen
	u.pairState = PairingState{Phase: "starting"}
	u.pairCancelled = false
	u.pairSessionID = ""
	u.pairNonce = nil
	u.pairMobileX = nil
	u.pairMobileE = nil
	u.pairMu.Unlock()

	go s.runPairingBackground(u, scope, gen)

	u.pairMu.Lock()
	st := u.pairState
	u.pairMu.Unlock()
	return st, nil
}

// PairingStatus returns the scope's current pairing-slot snapshot.
func (s *RelayService) PairingStatus(scope string) PairingState {
	u := s.getOrCreateUser(scope)
	u.pairMu.Lock()
	defer u.pairMu.Unlock()
	return u.pairState
}

// ConfirmPairing completes a pairing after SAS verification.
func (s *RelayService) ConfirmPairing(scope string) error {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	u.pairMu.Lock()
	sessionID := u.pairSessionID
	mobileXpub := append([]byte(nil), u.pairMobileX...)
	mobileName := u.pairState.MobileName
	u.pairMu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("no active pairing session")
	}
	cli, err := s.buildClient(scope)
	if err != nil {
		return err
	}
	if _, err := s.refreshAndPersist(u, scope, cli); err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	if _, err := cli.ConfirmPairingSession(sessionID); err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	_ = relayclient.AppendTrustedMobile(scope, mobileXpub, mobileName, "")

	u.pairMu.Lock()
	u.pairState.Phase = "confirmed"
	u.pairMu.Unlock()
	return nil
}

// RejectPairing cancels an in-progress pairing for the scope.
func (s *RelayService) RejectPairing(scope string) error {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	u.pairMu.Lock()
	sessionID := u.pairSessionID
	u.pairCancelled = true
	u.pairGen++
	u.pairState.Phase = "failed"
	u.pairState.Error = "rejected by user"
	u.pairMu.Unlock()
	if sessionID == "" {
		return nil
	}
	cli, err := s.buildClient(scope)
	if err != nil {
		return err
	}
	_, _ = s.refreshAndPersist(u, scope, cli)
	_ = cli.RejectPairingSession(sessionID)
	return nil
}

// ListTrustedMobiles returns the scope's trusted mobile list.
func (s *RelayService) ListTrustedMobiles(scope string) []TrustedMobileInfo {
	scope = normalizeScope(scope)
	mobs, err := relayclient.LoadTrustedMobiles(scope)
	if err != nil {
		return nil
	}
	out := make([]TrustedMobileInfo, 0, len(mobs))
	for _, m := range mobs {
		lastSeen := ""
		if !m.LastSeenAt.IsZero() {
			lastSeen = m.LastSeenAt.Format("2006-01-02 15:04:05")
		}
		out = append(out, TrustedMobileInfo{
			XpubHex:    m.XpubHex,
			Name:       m.Name,
			Platform:   m.Platform,
			PairedAt:   m.PairedAt.Format("2006-01-02 15:04:05"),
			LastSeenAt: lastSeen,
		})
	}
	return out
}

// RemoveTrustedMobile revokes trust for the scope + cascades to live sessions.
func (s *RelayService) RemoveTrustedMobile(scope, xpubHex string) error {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	if err := relayclient.RemoveTrustedMobile(scope, xpubHex); err != nil {
		return err
	}
	u.globalsMu.RLock()
	proxy := u.proxy
	lanSrv := u.lanServer
	u.globalsMu.RUnlock()
	if proxy != nil {
		proxy.RemoveCipher("lan:" + xpubHex)
	}
	if lanSrv != nil {
		lanSrv.RemoveTrustedMobile(xpubHex)
	}
	return nil
}

// --- billing + remote mobile management ------------------------------------

// authedClient returns a relayclient for the scope, holding a fresh access
// token. Reuses a cached access token (TTL accessCacheTTL) across parallel
// callers so a single page open doesn't fan out into N refresh calls —
// which would otherwise trip the relay's per-account refresh rate-limit
// and manifest as "status 429" on screens that issue 3-4 authenticated
// calls in parallel (billing panel, mobile-devices list, etc.).
//
// On ErrInvalidRefresh the scope's creds are cleared (the refresh token has
// been revoked / expired); the wrapped error tells the caller "not
// configured" so the UI can prompt for re-login.
func (s *RelayService) authedClient(scope string) (*relayclient.Client, error) {
	scope = normalizeScope(scope)
	u := s.getOrCreateUser(scope)
	cli, err := s.buildClient(scope)
	if err != nil {
		return nil, err
	}

	u.authMu.Lock()
	defer u.authMu.Unlock()

	// Fast path: cached token still valid for this relay URL.
	if u.cachedAccess != "" &&
		u.cachedAccessURL == cli.Cfg().RelayURL &&
		time.Now().Before(u.cachedAccessExp) {
		cli.SetAccessToken(u.cachedAccess)
		return cli, nil
	}

	// Short-lived failure cache: if a prior refresh just failed, return the
	// same error rather than firing another request. Stops parallel callers
	// from self-perpetuating a 429 by piling on the same refresh endpoint.
	if u.cachedLoginErr != nil && time.Now().Before(u.cachedLoginErrExp) {
		return nil, u.cachedLoginErr
	}

	// Slow path: refresh and cache the result.
	if _, err := s.refreshAndPersist(u, scope, cli); err != nil {
		wrapped := fmt.Errorf("refresh: %w", err)
		u.cachedLoginErr = wrapped
		u.cachedLoginErrExp = time.Now().Add(loginErrorCacheTTL)
		return nil, wrapped
	}
	u.cachedAccess = cli.AccessToken()
	u.cachedAccessURL = cli.Cfg().RelayURL
	u.cachedAccessExp = time.Now().Add(accessCacheTTL)
	u.cachedLoginErr = nil
	u.cachedLoginErrExp = time.Time{}
	return cli, nil
}

// refreshAndPersist exchanges the scope's stored refresh token for a fresh
// access token, persists the rotated refresh token back to credstore, and
// updates the relayclient's in-memory access token.
//
// On ErrInvalidRefresh (the relay rejected the refresh token — revoked,
// expired, or unknown) the scope's creds are cleared and the scope is
// removed from scopes.json so the tunnel goroutine sees a clean
// logged-out state on its next iteration. The error is still returned
// so the immediate caller can short-circuit.
//
// Returns the new RefreshResult on success.
func (s *RelayService) refreshAndPersist(u *userState, scope string, cli *relayclient.Client) (*relayclient.RefreshResult, error) {
	cur, err := credstore.Load(scope)
	if err != nil || cur == nil || cur.RefreshToken == "" {
		return nil, fmt.Errorf("请先登录 Relay 账号")
	}
	result, err := cli.RefreshAccessToken(cur.RefreshToken)
	if err != nil {
		if errors.Is(err, relayclient.ErrInvalidRefresh) {
			_ = credstore.Clear(scope)
			_ = removePersistedScope(scope)
			u.invalidateAccessCacheLocked()
		}
		return nil, err
	}
	cur.RefreshToken = result.RefreshToken
	if err := credstore.Save(scope, cur); err != nil {
		slog.Warn("relay: persist rotated refresh token failed", "scope", scope, "error", err)
	}
	// Keep the Client's Config in sync so subsequent refreshes use the new token.
	cli.Cfg().RefreshToken = result.RefreshToken
	return result, nil
}

// invalidateAccessCacheLocked clears the cached access token for a scope.
// Caller must hold u.authMu. Used on auth transitions (login, register,
// logout) so the next authedClient call does a fresh Login under the new
// credentials.
func (u *userState) invalidateAccessCacheLocked() {
	u.cachedAccess = ""
	u.cachedAccessURL = ""
	u.cachedAccessExp = time.Time{}
	u.cachedLoginErr = nil
	u.cachedLoginErrExp = time.Time{}
}

// GetBillingUsage returns the account's current usage vs. limits. Used by the
// desktop settings UI to render progress bars and explain pair-session 402s.
func (s *RelayService) GetBillingUsage(scope string) (*relayclient.UsageSummary, error) {
	cli, err := s.authedClient(scope)
	if err != nil {
		return nil, err
	}
	return cli.MyUsage()
}

// GetBillingPlan returns the account's current plan.
func (s *RelayService) GetBillingPlan(scope string) (*relayclient.Plan, error) {
	cli, err := s.authedClient(scope)
	if err != nil {
		return nil, err
	}
	return cli.MyPlan()
}

// ListBillingPlans returns all active plans published by the relay. Public
// endpoint but we still route through authedClient for consistent TLS/error
// surfacing; the Bearer header is ignored server-side for this path.
func (s *RelayService) ListBillingPlans(scope string) ([]relayclient.Plan, error) {
	cli, err := s.buildClient(scope)
	if err != nil {
		return nil, err
	}
	return cli.ListPlans()
}

// ChangeBillingPlan initiates a plan change. Free plans apply immediately
// (Applied=true); paid plans return a Stripe checkout URL that the desktop
// must open in the system browser (via the Wails OpenExternalURL binding).
// The Stripe webhook on the relay is what actually upgrades the account —
// the UI should poll GetBillingPlan/GetBillingUsage after the checkout
// completes to detect the upgrade.
func (s *RelayService) ChangeBillingPlan(scope, targetPlanID string) (*relayclient.ChangePlanResult, error) {
	cli, err := s.authedClient(scope)
	if err != nil {
		return nil, err
	}
	return cli.ChangePlan(targetPlanID)
}

// ListRemoteMobileDevices returns the account's non-revoked mobile_devices
// rows on the relay. The desktop UI surfaces this so the user can manually
// revoke stale devices (each revoke frees one MaxMobiles slot and unblocks
// further pair sessions when the 5-mobile free-plan cap has been hit via
// debug churn).
func (s *RelayService) ListRemoteMobileDevices(scope string) ([]relayclient.MobileDeviceSummary, error) {
	cli, err := s.authedClient(scope)
	if err != nil {
		return nil, err
	}
	return cli.ListMobileDevices()
}

// RevokeRemoteMobileDevice revokes a mobile_devices row on the relay. Unlike
// RemoveTrustedMobile (which only touches the local desktop's trusted_mobiles
// allowlist), this affects the relay's per-account mobile cap — so it's the
// correct fix for a 402 `mobile limit N reached` on pair-session creation.
func (s *RelayService) RevokeRemoteMobileDevice(scope, mobileID string) error {
	cli, err := s.authedClient(scope)
	if err != nil {
		return err
	}
	return cli.RevokeMobileDevice(mobileID)
}

// ListRemoteAccountDesktops returns the account's non-revoked desktops on
// the relay. Surfacing these in the device list lets a user spot stale
// desktop registrations from earlier installs / dev builds — each one
// counts against the per-account desktop cap, and a mobile that's still
// pointing at a revoked desktop_id will fail every RPC with 503.
func (s *RelayService) ListRemoteAccountDesktops(scope string) ([]relayclient.DesktopSummary, error) {
	cli, err := s.authedClient(scope)
	if err != nil {
		return nil, err
	}
	return cli.ListAccountDesktops()
}

// RevokeRemoteAccountDesktop revokes a desktop on the relay (DELETE
// /api/desktops/:id). The relay tunnel router will refuse subsequent
// reconnects from that desktop_token, and any mobile still pointing at
// the desktop_id gets a 503 DesktopOffline (which apiClient demotes
// gracefully on the pairkey 409 fallback). Use this to clean up
// orphaned desktops that show up in the list but no longer correspond
// to a running niuniu-server process.
func (s *RelayService) RevokeRemoteAccountDesktop(scope, desktopID string) error {
	cli, err := s.authedClient(scope)
	if err != nil {
		return err
	}
	return cli.RevokeAccountDesktop(desktopID)
}

// RequestEmailVerification asks the relay to (re)send the verification email
// for the scope's logged-in account. Used by the pair dialog to unblock the
// "unverified account, 1 desktop already used" 403 path.
func (s *RelayService) RequestEmailVerification(scope string) error {
	cli, err := s.authedClient(scope)
	if err != nil {
		return err
	}
	return cli.RequestEmailVerification()
}

// --- pairing internals -----------------------------------------------------

// pairingErrorCode maps a relayclient sentinel to the stable PairingState.ErrorCode
// tag the UI branches on. Returns "" for generic errors so the UI falls back
// to the plain-text message.
func pairingErrorCode(err error) string {
	switch {
	case errors.Is(err, relayclient.ErrEmailVerificationRequired):
		return "email_verification_required"
	case errors.Is(err, relayclient.ErrQuotaExceeded):
		return "quota_exceeded"
	default:
		return ""
	}
}

func (s *RelayService) buildClient(scope string) (*relayclient.Client, error) {
	scope = normalizeScope(scope)
	creds, err := credstore.Load(scope)
	if err != nil || creds == nil || creds.Email == "" || creds.RefreshToken == "" {
		return nil, fmt.Errorf("请先登录 Relay 账号")
	}
	// Pass the persisted DesktopID/DesktopToken through so the client can
	// skip RegisterDesktop on every boot/pair — avoiding a redundant round-
	// trip even though the relay now dedups server-side by xpub.
	return relayclient.NewClient(&relayclient.Config{
		RelayURL:     creds.URL,
		Email:        creds.Email,
		RefreshToken: creds.RefreshToken,
		DesktopID:    creds.DesktopID,
		DesktopToken: creds.DesktopToken,
	}), nil
}

// persistDesktopToken writes the newly-issued desktop id+token back to the
// scope's credstore entry — but only if the entry still belongs to the same
// email we just registered against (a concurrent Login to a different
// account could have replaced it).  Errors are logged, not propagated —
// losing the persistence means we re-register next time, which is recover­-
// able (at worst hits §8.2 on unverified accounts with ≥1 desktop).
func persistDesktopToken(scope, expectedEmail, desktopID, desktopToken string) {
	if desktopID == "" || desktopToken == "" {
		return
	}
	cur, err := credstore.Load(scope)
	if err != nil || cur == nil {
		slog.Warn("relay: persist desktop token — creds missing", "scope", scope, "error", err)
		return
	}
	if cur.Email != expectedEmail {
		slog.Info("relay: persist desktop token skipped — creds changed under us", "scope", scope)
		return
	}
	cur.DesktopID = desktopID
	cur.DesktopToken = desktopToken
	if err := credstore.Save(scope, cur); err != nil {
		slog.Warn("relay: persist desktop token failed", "scope", scope, "error", err)
	}
}

func (s *RelayService) runPairingBackground(u *userState, scope string, myGen uint64) {
	writeIfCurrent := func(fn func()) bool {
		u.pairMu.Lock()
		defer u.pairMu.Unlock()
		if u.pairGen != myGen {
			return false
		}
		fn()
		return true
	}
	setErr := func(err error) {
		writeIfCurrent(func() {
			u.pairState = PairingState{Phase: "failed", Error: err.Error(), ErrorCode: pairingErrorCode(err)}
		})
	}
	id, _, err := relayclient.LoadOrCreateIdentity(scope)
	if err != nil {
		setErr(fmt.Errorf("load identity: %w", err))
		return
	}
	cli, err := s.buildClient(scope)
	if err != nil {
		setErr(err)
		return
	}
	if _, err := s.refreshAndPersist(u, scope, cli); err != nil {
		setErr(fmt.Errorf("refresh: %w", err))
		return
	}
	if cli.Cfg().DesktopToken == "" {
		name, _ := os.Hostname()
		if err := cli.RegisterDesktop(id, name, runtime.GOOS, runtime.GOARCH, "p2"); err != nil {
			setErr(fmt.Errorf("register desktop: %w", err))
			return
		}
		// Re-read the email the client was built against so we don't
		// stomp creds if the user has logged into a different account
		// in the interim.
		if cur, _ := credstore.Load(scope); cur != nil {
			persistDesktopToken(scope, cur.Email, cli.Cfg().DesktopID, cli.Cfg().DesktopToken)
		}
	}
	nonce, err := pairingcrypto.RandomBytes(32)
	if err != nil {
		setErr(fmt.Errorf("generate nonce: %w", err))
		return
	}
	sess, err := cli.CreatePairingSession(cli.Cfg().DesktopID)
	if err != nil {
		setErr(fmt.Errorf("create pairing session: %w", err))
		return
	}
	qrURL := buildPairingURL(cli.Cfg().RelayURL, cli.Cfg().DesktopID, sess.SessionID, id.XPub, id.EdPub, nonce, sess.ExpiresAt)
	var qrPNG string
	if code, err := qr.Encode(qrURL, qr.Q); err == nil {
		qrPNG = "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
	}

	if !writeIfCurrent(func() {
		u.pairNonce = nonce
		u.pairSessionID = sess.SessionID
		u.pairState = PairingState{
			Phase: "waiting_claim", QRPNG: qrPNG, QRURL: qrURL, SessionID: sess.SessionID,
		}
	}) {
		return
	}

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		u.pairMu.Lock()
		cancelled := u.pairCancelled || u.pairGen != myGen
		u.pairMu.Unlock()
		if cancelled {
			return
		}
		snap, err := cli.PollPairingSession(sess.SessionID)
		if err == nil && snap.Status == "claimed" {
			mobileXpub, err := base64.StdEncoding.DecodeString(snap.ClaimedMobileXpubB64)
			if err != nil {
				setErr(fmt.Errorf("decode mobile xpub: %w", err))
				return
			}
			mobileSignPub, err := base64.StdEncoding.DecodeString(snap.ClaimedMobileSignPubB64)
			if err != nil {
				setErr(fmt.Errorf("decode mobile sign pub: %w", err))
				return
			}
			sas := pairingcrypto.DeriveSAS(nonce, id.XPub, mobileXpub, id.EdPub, mobileSignPub)
			writeIfCurrent(func() {
				u.pairMobileX = mobileXpub
				u.pairMobileE = mobileSignPub
				u.pairState = PairingState{
					Phase: "ready_to_confirm", QRPNG: qrPNG, QRURL: qrURL,
					SessionID: sess.SessionID, SAS: sas, MobileName: snap.ClaimedMobileName,
				}
			})
			return
		}
		if err == nil && (snap.Status == "expired" || snap.Status == "failed") {
			setErr(fmt.Errorf("pairing session %s", snap.Status))
			return
		}
		time.Sleep(1 * time.Second)
	}
	setErr(fmt.Errorf("pairing timed out after 5 minutes"))
}

func buildPairingURL(relayURL, desktopID, sessionID string, xpub, signPub, nonce []byte, expiresAt time.Time) string {
	q := url.Values{}
	q.Set("r", relayURL)
	q.Set("d", desktopID)
	q.Set("s", sessionID)
	q.Set("xk", base64.StdEncoding.EncodeToString(xpub))
	q.Set("sk", base64.StdEncoding.EncodeToString(signPub))
	q.Set("n", base64.StdEncoding.EncodeToString(nonce))
	q.Set("t", strconv.FormatInt(expiresAt.Unix(), 10))
	return "niuniu-pair://v1?" + q.Encode()
}

// --- scopes.json (active-scopes list) --------------------------------------

// scopesStorePath returns ~/.niuniu/relay/scopes.json, which lists scopes
// that currently have credstore entries.  Used at startup to fan out tunnels.
// Overridable by tests.
var scopesStorePath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".niuniu", "relay", "scopes.json"), nil
}

// scopesMu guards reads/writes of scopes.json across goroutines.
var scopesMu sync.Mutex

type scopesFile struct {
	Scopes []string `json:"scopes"`
}

func loadPersistedScopes() ([]string, error) {
	path, err := scopesStorePath()
	if err != nil {
		return nil, err
	}
	scopesMu.Lock()
	defer scopesMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f scopesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Scopes, nil
}

func addPersistedScope(scope string) error {
	scope = normalizeScope(scope)
	path, err := scopesStorePath()
	if err != nil {
		return err
	}
	scopesMu.Lock()
	defer scopesMu.Unlock()
	scopes, err := readScopesLocked(path)
	if err != nil {
		return err
	}
	for _, existing := range scopes {
		if existing == scope {
			return nil // already in list
		}
	}
	scopes = append(scopes, scope)
	sort.Strings(scopes)
	return writeScopesLocked(path, scopes)
}

func removePersistedScope(scope string) error {
	scope = normalizeScope(scope)
	path, err := scopesStorePath()
	if err != nil {
		return err
	}
	scopesMu.Lock()
	defer scopesMu.Unlock()
	scopes, err := readScopesLocked(path)
	if err != nil {
		return err
	}
	out := scopes[:0]
	for _, existing := range scopes {
		if existing != scope {
			out = append(out, existing)
		}
	}
	return writeScopesLocked(path, out)
}

func readScopesLocked(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f scopesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Scopes, nil
}

func writeScopesLocked(path string, scopes []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(scopesFile{Scopes: scopes}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// retryWithBackoffInitial / retryWithBackoffCap are exposed as vars so tests
// can shrink them — the production loop sleeps 1s..30s, which is fine in
// real life but unworkable in unit tests.
var (
	retryWithBackoffInitial = time.Second
	retryWithBackoffCap     = 30 * time.Second
)

// retryWithBackoff calls fn repeatedly with exponential backoff (capped at
// retryWithBackoffCap) until fn returns nil or ctx is cancelled. Returns
// ctx.Err() on cancel; never returns fn's error (the loop is infinite by
// design — `runTunnel` callers want "keep trying until logout").
func retryWithBackoff(ctx context.Context, fn func() error) error {
	backoff := retryWithBackoffInitial
	for {
		if err := fn(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < retryWithBackoffCap {
			backoff *= 2
			if backoff > retryWithBackoffCap {
				backoff = retryWithBackoffCap
			}
		}
	}
}
