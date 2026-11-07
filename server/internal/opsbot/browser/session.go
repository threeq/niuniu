// Package browser implements the browser worker runtime for Ops Bot accounts:
// per-account browser profiles, single-operation leases, and the task runner
// that executes browser recipes serially with evidence capture.
package browser

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Browser backends (design §8.1).
const (
	BackendLocal = "local"
	BackendCloud = "cloud"
)

// Session login states (ops_browser_sessions.login_status).
const (
	LoginUnknown = "unknown"
	LoginValid   = "valid"
	LoginExpired = "expired"
)

// Session health states (ops_browser_sessions.health).
const (
	HealthUnknown  = "unknown"
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthDown     = "down"
)

// Session risk states (ops_browser_sessions.risk_state).
const (
	RiskNormal    = "normal"
	RiskThrottled = "throttled"
	RiskBlocked   = "blocked"
)

var (
	// ErrLeaseBusy means another worker holds the account's operation lease.
	ErrLeaseBusy = errors.New("opsbot/browser: session lease is busy")
	// ErrLeaseLost means Release did not match the lease owner (expired or
	// reclaimed by another worker).
	ErrLeaseLost = errors.New("opsbot/browser: session lease not held by owner")
	// ErrSessionNotFound means no browser session exists for (bot, backend).
	ErrSessionNotFound = errors.New("opsbot/browser: session not found")
	// ErrInvalidSession identifies malformed session input.
	ErrInvalidSession = errors.New("opsbot/browser: invalid session")
)

const defaultLeaseTTL = 5 * time.Minute

// SessionManager owns browser session rows and the per-account operation lease.
// A lease is a CAS update: only one owner can hold an unexpired lease at a
// time, and an expired lease is reclaimable.
type SessionManager struct {
	Q        *store.Queries
	DataDir  string
	LeaseTTL time.Duration
}

// NewSessionManager wires a session manager. dataDir is the server data root
// (normally ~/.niuniu); profiles live under ops/browser/<botID>/<backend>/.
func NewSessionManager(q *store.Queries, dataDir string) *SessionManager {
	return &SessionManager{Q: q, DataDir: dataDir, LeaseTTL: defaultLeaseTTL}
}

// Ensure creates the account's profile directory (0700) and registers the
// session row. It is idempotent.
func (m *SessionManager) Ensure(ctx context.Context, botID int64, backend string) (store.OpsBrowserSession, error) {
	if botID <= 0 {
		return store.OpsBrowserSession{}, fmt.Errorf("%w: bot id %d", ErrInvalidSession, botID)
	}
	if err := validateBackend(backend); err != nil {
		return store.OpsBrowserSession{}, err
	}
	if m.DataDir == "" {
		return store.OpsBrowserSession{}, fmt.Errorf("%w: data dir is empty", ErrInvalidSession)
	}
	dir := m.ProfileDir(botID, backend)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.OpsBrowserSession{}, fmt.Errorf("opsbot/browser: create profile dir: %w", err)
	}
	sess, err := m.Q.UpsertOpsBrowserSession(ctx, store.UpsertOpsBrowserSessionParams{
		BotID:      botID,
		Backend:    backend,
		ProfileDir: dir,
	})
	if err != nil {
		return store.OpsBrowserSession{}, fmt.Errorf("opsbot/browser: upsert session: %w", err)
	}
	return sess, nil
}

// ProfileDir returns the per-account profile directory. Each browser backend
// gets its own directory; cookies/localStorage/fingerprint are never shared.
func (m *SessionManager) ProfileDir(botID int64, backend string) string {
	return filepath.Join(m.DataDir, "ops", "browser", strconv.FormatInt(botID, 10), backend)
}

// Acquire takes the account lease for owner. On success the caller must
// Release with the same owner. An expired lease is reclaimed automatically.
func (m *SessionManager) Acquire(ctx context.Context, botID int64, backend, owner string, ttl time.Duration) error {
	if botID <= 0 || owner == "" {
		return fmt.Errorf("%w: bot id %d, owner %q", ErrInvalidSession, botID, owner)
	}
	if err := validateBackend(backend); err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = m.LeaseTTL
	}
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	now := time.Now()
	expires := sql.NullTime{Time: now.Add(ttl), Valid: true}
	n, err := m.Q.AcquireOpsBrowserLease(ctx, store.AcquireOpsBrowserLeaseParams{
		BotID:          botID,
		Backend:        backend,
		LeaseOwner:     owner,
		LeaseExpiresAt: expires,
		Now:            sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("opsbot/browser: acquire lease: %w", err)
	}
	if n == 0 {
		// Distinguish "busy" from "no session row": both yield 0 updated rows.
		if _, err := m.State(ctx, botID, backend); err != nil {
			return err
		}
		return fmt.Errorf("%w: bot %d backend %s", ErrLeaseBusy, botID, backend)
	}
	return nil
}

// Release drops the lease, but only when owner still holds it.
func (m *SessionManager) Release(ctx context.Context, botID int64, backend, owner string) error {
	if botID <= 0 || owner == "" {
		return fmt.Errorf("%w: bot id %d, owner %q", ErrInvalidSession, botID, owner)
	}
	n, err := m.Q.ReleaseOpsBrowserLease(ctx, store.ReleaseOpsBrowserLeaseParams{
		BotID:      botID,
		Backend:    backend,
		LeaseOwner: owner,
	})
	if err != nil {
		return fmt.Errorf("opsbot/browser: release lease: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: bot %d backend %s owner %s", ErrLeaseLost, botID, backend, owner)
	}
	return nil
}

// State returns the current session row.
func (m *SessionManager) State(ctx context.Context, botID int64, backend string) (store.OpsBrowserSession, error) {
	if err := validateBackend(backend); err != nil {
		return store.OpsBrowserSession{}, err
	}
	sess, err := m.Q.GetOpsBrowserSession(ctx, store.GetOpsBrowserSessionParams{
		BotID:   botID,
		Backend: backend,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return store.OpsBrowserSession{}, fmt.Errorf("%w: bot %d backend %s", ErrSessionNotFound, botID, backend)
	}
	if err != nil {
		return store.OpsBrowserSession{}, fmt.Errorf("opsbot/browser: get session: %w", err)
	}
	return sess, nil
}

// StatePatch updates the mutable session state fields. Nil fields keep their
// current value.
type StatePatch struct {
	LoginStatus *string
	LastLoginAt *time.Time
	Health      *string
	RiskState   *string
	LastError   *string
}

// Update applies a state patch and returns the updated row.
func (m *SessionManager) Update(ctx context.Context, botID int64, backend string, patch StatePatch) (store.OpsBrowserSession, error) {
	cur, err := m.State(ctx, botID, backend)
	if err != nil {
		return store.OpsBrowserSession{}, err
	}
	loginStatus := cur.LoginStatus
	if patch.LoginStatus != nil {
		loginStatus = *patch.LoginStatus
	}
	lastLogin := cur.LastLoginAt
	if patch.LastLoginAt != nil {
		lastLogin = sql.NullTime{Time: *patch.LastLoginAt, Valid: true}
	}
	health := cur.Health
	if patch.Health != nil {
		health = *patch.Health
	}
	risk := cur.RiskState
	if patch.RiskState != nil {
		risk = *patch.RiskState
	}
	lastError := cur.LastError
	if patch.LastError != nil {
		lastError = *patch.LastError
	}
	sess, err := m.Q.UpdateOpsBrowserSessionState(ctx, store.UpdateOpsBrowserSessionStateParams{
		BotID:       botID,
		Backend:     backend,
		LoginStatus: loginStatus,
		LastLoginAt: lastLogin,
		Health:      health,
		RiskState:   risk,
		LastError:   lastError,
	})
	if err != nil {
		return store.OpsBrowserSession{}, fmt.Errorf("opsbot/browser: update session: %w", err)
	}
	return sess, nil
}

func validateBackend(backend string) error {
	switch backend {
	case BackendLocal, BackendCloud:
		return nil
	default:
		return fmt.Errorf("%w: backend %q", ErrInvalidSession, backend)
	}
}
