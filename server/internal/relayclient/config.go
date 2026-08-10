// Package relayclient contains the desktop side of the central relay tunnel.
package relayclient

// Config holds the relay URL, account credentials, and the desktop's issued
// long-lived identity (desktop_id + desktop_token) once registered.
//
// RefreshToken replaces the legacy Password field: the desktop login flow is
// passwordless (email + 6-digit code), and the relay rotates refresh tokens
// on every /api/accounts/refresh call. Persist the rotated refresh token
// back to credstore on each successful refresh; the access token lives in
// memory on the Client (see Client.access).
type Config struct {
	RelayURL     string // e.g. https://relay.example.com
	Email        string
	RefreshToken string // empty until VerifyEmailCode succeeds
	DesktopToken string // empty until RegisterDesktop succeeds
	DesktopID    string // empty until RegisterDesktop succeeds
}
