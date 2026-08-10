// Package consent records and enforces per-user acceptance of the privacy &
// disclaimer agreement. A user must accept the CurrentVersion before they may
// enter the system; write actions, run-class actions, and AI execution
// interfaces (agent / MCP / terminal / schedule) are gated on it.
//
// State lives server-side (not just a browser flag) so the AI execution path
// can enforce it. The agreement TEXT lives in the frontend i18n bundle; the
// server only tracks version strings. CurrentVersion is the coordination
// point: bump it AND update the three-locale i18n text in the same change to
// force re-consent.
//
// The service mirrors the license package: it owns one table and talks to it via
// raw *store.DB (SQLite-flavored `?` placeholders rewritten for Postgres by the
// wrapper), keeping the feature self-contained and off the sqlc users path.
package consent

import (
	"context"
	"database/sql"
	"errors"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// CurrentVersion is the version of the privacy & disclaimer agreement currently
// in force. Bump it whenever the agreement text changes; every user is then
// required to re-consent. Keep it in lockstep with the `consent.version.*` key
// in server/web/src/i18n/locales/{zh-CN,zh-TW,en}/common.json.
const CurrentVersion = "2026-06-01"

// ErrVersionMismatch is returned by Accept when the submitted version is not the
// CurrentVersion (e.g. a stale client posting an old agreement version).
var ErrVersionMismatch = errors.New("consent: version mismatch")

// Status is the UI-safe consent state for a single user.
type Status struct {
	CurrentVersion string `json:"current_version"`
	AgreedVersion  string `json:"agreed_version"`
	NeedsConsent   bool   `json:"needs_consent"`
}

// Service is the single source of truth for per-user consent state.
type Service struct {
	db *store.DB
}

// NewService constructs the service over a wrapped store.DB.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// AgreedVersion returns the version the user last accepted, or "" if none.
func (s *Service) AgreedVersion(ctx context.Context, userID int64) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT version FROM user_consents WHERE user_id = ?`, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// HasConsented reports whether the user has accepted the CurrentVersion. On any
// read error it returns false (fail-closed): a DB hiccup must not silently open
// the gate. The caller (middleware) treats false as "blocked".
func (s *Service) HasConsented(ctx context.Context, userID int64) bool {
	v, err := s.AgreedVersion(ctx, userID)
	if err != nil {
		return false
	}
	return v == CurrentVersion
}

// Status reports the user's consent state for the UI.
func (s *Service) Status(ctx context.Context, userID int64) (Status, error) {
	v, err := s.AgreedVersion(ctx, userID)
	if err != nil {
		return Status{}, err
	}
	return Status{
		CurrentVersion: CurrentVersion,
		AgreedVersion:  v,
		NeedsConsent:   v != CurrentVersion,
	}, nil
}

// Accept records the user's acceptance of the given version. The version must
// equal CurrentVersion (rejects stale/forged submissions). Idempotent: a second
// accept just refreshes agreed_at.
func (s *Service) Accept(ctx context.Context, userID int64, version string) error {
	if version != CurrentVersion {
		return ErrVersionMismatch
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_consents (user_id, version, agreed_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(user_id) DO UPDATE SET version = excluded.version, agreed_at = CURRENT_TIMESTAMP`,
		userID, version)
	return err
}
