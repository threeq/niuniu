package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// LockoutConfig controls account- and IP-level lockout thresholds.
// Zero AccountThreshold disables account lockout; same for IPThreshold.
type LockoutConfig struct {
	AccountThreshold int64
	AccountWindow    time.Duration
	AccountCooldown  time.Duration
	IPThreshold      int64
	IPWindow         time.Duration
	IPCooldown       time.Duration
}

// DefaultLockoutConfig returns Phase A defaults per the auth-hardening spec
// (2026-05-15-team-edition-auth-hardening-design.md §2.7):
//   - Account: 5 failures within 15 min → 30 min cooldown
//   - IP:      20 failures within 15 min → 60 min cooldown
//
// Phase A is intentionally flat (no escalation tier); the spec's escalation
// to 2h/24h ships with Phase B.
func DefaultLockoutConfig() LockoutConfig {
	return LockoutConfig{
		AccountThreshold: 5,
		AccountWindow:    15 * time.Minute,
		AccountCooldown:  30 * time.Minute,
		IPThreshold:      20,
		IPWindow:         15 * time.Minute,
		IPCooldown:       1 * time.Hour,
	}
}

// LockoutCheck describes whether a login attempt should be short-circuited
// before bcrypt evaluation.
type LockoutCheck struct {
	Locked     bool
	RetryAfter time.Duration
	Reason     string // account_locked | ip_locked | ""
}

// LoginAudit records login attempts and applies lockout decisions.
// The clock is injectable so unit tests can step time deterministically.
type LoginAudit struct {
	q   *store.Queries
	cfg LockoutConfig
	now func() time.Time
}

func NewLoginAudit(q *store.Queries, cfg LockoutConfig) *LoginAudit {
	return &LoginAudit{q: q, cfg: cfg, now: time.Now}
}

// CheckPreLogin runs the pre-bcrypt locks. user may be nil if the username
// is unknown (only IP lockout is then evaluated).
func (a *LoginAudit) CheckPreLogin(ctx context.Context, user *store.User, ip string) (LockoutCheck, error) {
	now := a.now()
	if user != nil && user.LockedUntil.Valid && user.LockedUntil.Time.After(now) {
		return LockoutCheck{
			Locked:     true,
			RetryAfter: user.LockedUntil.Time.Sub(now),
			Reason:     "account_locked",
		}, nil
	}
	if ip == "" || a.cfg.IPThreshold <= 0 {
		return LockoutCheck{}, nil
	}
	lock, err := a.q.GetIPLockout(ctx, ip)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LockoutCheck{}, nil
		}
		return LockoutCheck{}, err
	}
	if lock.LockedUntil.Valid && lock.LockedUntil.Time.After(now) {
		return LockoutCheck{
			Locked:     true,
			RetryAfter: lock.LockedUntil.Time.Sub(now),
			Reason:     "ip_locked",
		}, nil
	}
	return LockoutCheck{}, nil
}

// RecordAttempt writes a login_attempts row. Best-effort: a write failure is
// logged but does not propagate, because failing the login flow on an audit
// hiccup would harm availability more than it would hurt forensics.
func (a *LoginAudit) RecordAttempt(ctx context.Context, userID sql.NullInt64, username, ip, ua, reason string, success bool) {
	var sv int64
	if success {
		sv = 1
	}
	if err := a.q.RecordLoginAttempt(ctx, store.RecordLoginAttemptParams{
		UserID:    userID,
		Username:  username,
		Ip:        ip,
		UserAgent: ua,
		Success:   sv,
		Reason:    reason,
	}); err != nil {
		slog.Warn("record login attempt failed", "username", username, "reason", reason, "error", err)
	}
}

// OnFailedAttempt counts recent failures over the configured window for both
// account and IP, and applies a lockout if the threshold is crossed.
// Caller must call RecordAttempt(...success=false...) BEFORE this so the
// failure is counted.
func (a *LoginAudit) OnFailedAttempt(ctx context.Context, user *store.User, ip string) error {
	now := a.now()

	if user != nil && a.cfg.AccountThreshold > 0 {
		since := now.Add(-a.cfg.AccountWindow)
		n, err := a.q.CountFailedLoginsByUser(ctx, store.CountFailedLoginsByUserParams{
			UserID:    sql.NullInt64{Int64: user.ID, Valid: true},
			CreatedAt: since,
		})
		if err != nil {
			return err
		}
		if n >= a.cfg.AccountThreshold {
			until := now.Add(a.cfg.AccountCooldown)
			if err := a.q.LockUserAccount(ctx, store.LockUserAccountParams{
				LockedUntil: sql.NullTime{Time: until, Valid: true},
				ID:          user.ID,
			}); err != nil {
				return err
			}
		}
	}

	if ip == "" || a.cfg.IPThreshold <= 0 {
		return nil
	}
	since := now.Add(-a.cfg.IPWindow)
	n, err := a.q.CountFailedLoginsByIP(ctx, store.CountFailedLoginsByIPParams{
		Ip:        ip,
		CreatedAt: since,
	})
	if err != nil {
		return err
	}
	if n >= a.cfg.IPThreshold {
		until := now.Add(a.cfg.IPCooldown)
		return a.q.UpsertIPLockout(ctx, store.UpsertIPLockoutParams{
			Ip:          ip,
			FailCount:   n,
			LockedUntil: sql.NullTime{Time: until, Valid: true},
		})
	}
	return nil
}

// OnSuccessfulLogin clears account lock and resets the IP fail-count.
// IP unlocking on success is a deliberate choice to favor legitimate users on
// shared exits (office NAT, university) over absolute lockout — IP lockout
// is a brake, not a wall.
func (a *LoginAudit) OnSuccessfulLogin(ctx context.Context, userID int64, ip string) error {
	if err := a.q.UnlockUserAccount(ctx, userID); err != nil {
		return err
	}
	if ip == "" {
		return nil
	}
	if err := a.q.ResetIPLockoutFailCount(ctx, ip); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// ListRecentForUser returns the most recent N login attempts for a user.
func (a *LoginAudit) ListRecentForUser(ctx context.Context, userID int64, limit int64) ([]store.LoginAttempt, error) {
	return a.q.ListLoginAttemptsForUser(ctx, store.ListLoginAttemptsForUserParams{
		UserID: sql.NullInt64{Int64: userID, Valid: true},
		Limit:  limit,
	})
}

// StartGC launches a background goroutine that periodically deletes
// login_attempts rows older than `retention`. Per spec §10 (security &
// privacy) IP/UA values are kept at most 90d by default. The ticker runs
// once on startup (so a long-down server still trims promptly) and then
// every `interval`. Stop by cancelling ctx.
func (a *LoginAudit) StartGC(ctx context.Context, retention, interval time.Duration) {
	if retention <= 0 || interval <= 0 {
		return
	}
	go func() {
		// Run once immediately, then on each tick.
		a.gcOnce(ctx, retention)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.gcOnce(ctx, retention)
			}
		}
	}()
}

func (a *LoginAudit) gcOnce(ctx context.Context, retention time.Duration) {
	cutoff := a.now().Add(-retention)
	if err := a.q.DeleteOldLoginAttempts(ctx, cutoff); err != nil {
		// GC failures are not user-visible; logging is sufficient.
		slog.Warn("login_attempts GC failed", "cutoff", cutoff, "error", err)
	}
}
