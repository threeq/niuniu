// Package credstore persists the relay account credentials to the OS
// keychain so they never hit disk in cleartext.
//
// Backing store:
//   - macOS → Keychain
//   - Windows → Credential Manager
//   - Linux → Secret Service (libsecret)
//
// # Scoping
//
// Every entry is keyed by an opaque `scope` string so a multi-user
// niuniu-server deployment keeps each user's credentials isolated:
//
//	(service="niuniu-desktop", account="relay-account:"+scope)
//
// Callers are responsible for choosing a stable scope per niuniu user
// (e.g. the int64 user_id from auth, formatted as a string).  Personal
// mode (auth disabled) should pass scope="default".
package credstore

import (
	"encoding/json"
	"errors"

	"github.com/zalando/go-keyring"
)

const (
	service       = "niuniu-desktop"
	accountPrefix = "relay-account:"
)

// DefaultScope is the scope used when auth is disabled (niuniu-desktop
// embedded mode).  It keeps the single-user keychain entry tied to a
// deterministic key so the entry from older single-scope builds can be
// accessed by one-shot migration code.
const DefaultScope = "default"

// ErrNotFound is returned by Load when no entry exists for the scope.
var ErrNotFound = errors.New("credstore: no relay credentials in keychain")

// Creds is the shape stored in the keychain.
//
// The desktop login flow is passwordless email-code + rotating refresh tokens:
// VerifyEmailCode populates RefreshToken, every RefreshAccessToken call
// rotates it. AccessToken is not persisted — it lives in memory on the
// Client and is short-lived.
//
// DesktopID + DesktopToken are written after the first successful
// relayclient.RegisterDesktop call — persisting them is what lets the
// tunnel + pair flows skip re-registration on subsequent boots or on every
// pair attempt.  The relay's /api/desktops endpoint does NOT dedup, so
// re-registering would create a new desktop row and, on unverified
// accounts, hit the §8.2 one-desktop limit with HTTP 403.
//
// Backwards compat: older entries written before the email-code rollout
// also carried `password` and `access_token` JSON keys; those fields were
// removed from the struct so unmarshal silently drops them. An old entry
// that lacks RefreshToken is treated as logged-out — the user must
// reauthenticate via the email-code flow.
type Creds struct {
	URL          string `json:"url"`
	Email        string `json:"email"`
	RefreshToken string `json:"refresh_token,omitempty"`
	DesktopID    string `json:"desktop_id,omitempty"`
	DesktopToken string `json:"desktop_token,omitempty"`
}

func keyFor(scope string) string {
	if scope == "" {
		scope = DefaultScope
	}
	return accountPrefix + scope
}

// Load reads the creds for the given scope. Returns ErrNotFound if absent.
func Load(scope string) (*Creds, error) {
	raw, err := keyring.Get(service, keyFor(scope))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c Creds
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save replaces the stored entry for the scope atomically.  After a successful
// Set we immediately Get the entry back and compare — if the store silently
// dropped the write (some keyring backends do this under size or permission
// pressure) we surface it as an error instead of pretending the save succeeded.
func Save(scope string, c *Creds) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	want := string(b)
	key := keyFor(scope)
	if err := keyring.Set(service, key, want); err != nil {
		return err
	}
	got, err := keyring.Get(service, key)
	if err != nil {
		return errors.New("credstore: write appeared to succeed but read-back failed: " + err.Error())
	}
	if got != want {
		return errors.New("credstore: read-back mismatch (write was silently dropped)")
	}
	return nil
}

// Clear deletes the entry for the scope. Returns nil if it didn't exist.
func Clear(scope string) error {
	err := keyring.Delete(service, keyFor(scope))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
