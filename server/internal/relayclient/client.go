package relayclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// Client talks to the relay's account + desktop management API.
type Client struct {
	cfg    *Config
	access string
	http   *http.Client
}

// NewClient returns a Client that will use cfg for its relay URL and credentials.
// It does not perform network I/O; call Login first.
func NewClient(cfg *Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{}}
}

// Cfg returns the Config the Client was constructed with (including any
// DesktopID/DesktopToken that RegisterDesktop has filled in).
func (c *Client) Cfg() *Config { return c.cfg }

// AccessToken returns the bearer token Login populated. Empty until Login (or
// SetAccessToken) succeeds. Callers that cache a valid token across Client
// instances read this to snapshot; others shouldn't rely on it.
func (c *Client) AccessToken() string { return c.access }

// SetAccessToken injects an access token obtained from a prior Login on the
// same account. Lets a caller reuse a cached token instead of re-hitting the
// relay's rate-limited /api/accounts/login route for every service call.
// Empty string clears the token.
func (c *Client) SetAccessToken(tok string) { c.access = tok }

// AccountInfo mirrors the relay's GET /api/accounts/me response.
type AccountInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	PlanID    string `json:"plan_id"`
	CreatedAt string `json:"created_at"`
}

// ErrInvalidCredentials is returned by token refresh on a 401 from the relay.
// Kept under the old name so existing call-sites that switch on it (e.g. Me)
// continue to compile.
var ErrInvalidCredentials = errors.New("relayclient: invalid credentials")

// ErrEmailVerificationRequired is returned by RegisterDesktop when the relay
// refuses a new desktop registration because the account is unverified and
// already has ≥1 desktop (§8.2). Caller can prompt the user to verify and call
// RequestEmailVerification.
var ErrEmailVerificationRequired = errors.New("relayclient: email verification required")

// ErrQuotaExceeded is returned by CreatePairingSession when the relay refuses
// a new mobile pairing because the account has hit its plan quota (max_mobiles
// or the daily pairing cap). Caller can prompt the user to revoke a stale
// mobile or upgrade the plan.
var ErrQuotaExceeded = errors.New("relayclient: quota exceeded")

// Email-code login error sentinels. The relay returns these as
// `{"error": "<code>"}` 400-level responses; we surface them as typed errors
// so the service layer can map them to user-facing strings.
var (
	ErrInvalidEmail      = errors.New("relayclient: invalid email")
	ErrInvalidLoginCode  = errors.New("relayclient: invalid login code")
	ErrExpiredLoginCode  = errors.New("relayclient: login code expired")
	ErrTooManyAttempts   = errors.New("relayclient: too many code attempts")
	ErrInvalidRefresh    = errors.New("relayclient: refresh token invalid or revoked")
)

// EmailCodeLoginResult mirrors the relay's POST /api/accounts/email-code/verify
// 200 response. The desktop persists RefreshToken so subsequent reconnects can
// re-issue access tokens without prompting the user again.
type EmailCodeLoginResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Email        string `json:"email"`
	IsNewUser    bool   `json:"is_new_user"`
}

// RefreshResult mirrors POST /api/accounts/refresh: the relay rotates both
// tokens, so callers must persist the new refresh token back to credstore.
type RefreshResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// RequestEmailCode triggers a 6-digit login code email for `email` via
// POST /api/accounts/email-code/request. The relay returns 200 even for unknown
// addresses (anti-enumeration), so a successful call only proves the relay
// accepted the request — the user still needs the code from their inbox.
func (c *Client) RequestEmailCode(email string) error {
	body, _ := json.Marshal(map[string]string{"email": email})
	resp, err := c.http.Post(c.cfg.RelayURL+"/api/accounts/email-code/request", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	code, _ := readErrorEnvelopeCode(resp)
	if resp.StatusCode == http.StatusBadRequest && code == "invalid_email" {
		return ErrInvalidEmail
	}
	return fmt.Errorf("request_email_code: status %d", resp.StatusCode)
}

// VerifyEmailCode exchanges a (email, code) pair for an access+refresh token
// pair via POST /api/accounts/email-code/verify. Stores the access token in
// the client; caller persists the refresh token externally.
func (c *Client) VerifyEmailCode(email, code string) (*EmailCodeLoginResult, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "code": code})
	resp, err := c.http.Post(c.cfg.RelayURL+"/api/accounts/email-code/verify", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errCode, _ := readErrorEnvelopeCode(resp)
		switch errCode {
		case "invalid_code":
			return nil, ErrInvalidLoginCode
		case "expired_code":
			return nil, ErrExpiredLoginCode
		case "too_many_attempts":
			return nil, ErrTooManyAttempts
		case "invalid_email":
			return nil, ErrInvalidEmail
		}
		return nil, fmt.Errorf("verify_email_code: status %d", resp.StatusCode)
	}
	var out EmailCodeLoginResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		return nil, errors.New("verify_email_code: empty tokens")
	}
	c.access = out.AccessToken
	return &out, nil
}

// RefreshAccessToken exchanges a refresh token for a new access+refresh pair
// via POST /api/accounts/refresh. Stores the new access token in the client;
// caller must persist the new refresh token (the relay rotates them, so the
// old one is revoked once this returns).
//
// Returns ErrInvalidRefresh on 401 — the caller should treat this as a
// terminal "log the user out" signal: the refresh token has been revoked
// (logged out elsewhere, password reset, etc.) and re-asking for the same
// token won't recover.
func (c *Client) RefreshAccessToken(refreshToken string) (*RefreshResult, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	resp, err := c.http.Post(c.cfg.RelayURL+"/api/accounts/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrInvalidRefresh
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh: status %d", resp.StatusCode)
	}
	var out RefreshResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		return nil, errors.New("refresh: empty tokens")
	}
	c.access = out.AccessToken
	return &out, nil
}

// readErrorEnvelopeCode extracts the `error` field from a relay error body.
// Tolerates both `{"error":"<code>"}` (string form, used by email-code endpoints)
// and `{"error":{"code":"<code>",...}}` (object form, used elsewhere).
func readErrorEnvelopeCode(resp *http.Response) (string, string) {
	env, detail := readErrorEnvelope(resp)
	if env.Code != "" {
		return env.Code, detail
	}
	return env.Error, detail
}

// Me calls GET /api/accounts/me; caller must have called Login() first.
func (c *Client) Me() (*AccountInfo, error) {
	if c.access == "" {
		return nil, errors.New("me: not logged in")
	}
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/accounts/me", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("me: status %d", resp.StatusCode)
	}
	var out AccountInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RegisterDesktop creates a new desktop row on the relay, including the
// identity's public keys, and stores the issued desktop_id + desktop_token
// into the Config.
func (c *Client) RegisterDesktop(id *pairingcrypto.Identity, name, osName, arch, version string) error {
	body, _ := json.Marshal(map[string]string{
		"name":         name,
		"os":           osName,
		"arch":         arch,
		"version":      version,
		"xpub_b64":     base64.StdEncoding.EncodeToString(id.XPub),
		"sign_pub_b64": base64.StdEncoding.EncodeToString(id.EdPub),
	})
	req, _ := http.NewRequest("POST", c.cfg.RelayURL+"/api/desktops", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		env, detail := readErrorEnvelope(resp)
		if resp.StatusCode == http.StatusForbidden &&
			(env.Code == "email_verification_required" || env.Error == "email_verification_required") {
			return ErrEmailVerificationRequired
		}
		if detail != "" {
			return fmt.Errorf("register: status %d (%s)", resp.StatusCode, detail)
		}
		return fmt.Errorf("register: status %d", resp.StatusCode)
	}
	var out struct {
		DesktopID    string `json:"desktop_id"`
		DesktopToken string `json:"desktop_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	c.cfg.DesktopID = out.DesktopID
	c.cfg.DesktopToken = out.DesktopToken
	return nil
}

// RequestEmailVerification calls POST /api/accounts/verify-email/request, which
// tells the relay to (re)send a verification link to the logged-in account's
// email. Caller must have called Login() first.
func (c *Client) RequestEmailVerification() error {
	if c.access == "" {
		return errors.New("request_email_verification: not logged in")
	}
	req, _ := http.NewRequest("POST", c.cfg.RelayURL+"/api/accounts/verify-email/request", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return fmt.Errorf("request_email_verification: status %d (%s)", resp.StatusCode, detail)
		}
		return fmt.Errorf("request_email_verification: status %d", resp.StatusCode)
	}
	return nil
}
