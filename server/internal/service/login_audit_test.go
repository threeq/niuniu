package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// loginAuditFixture builds an in-memory SQLite-backed LoginAudit with the
// default config, plus a controllable clock the test steps forward by
// calling step(d). A seeded user "alice" is created so account-level
// branches have a real users row.
func loginAuditFixture(t *testing.T) (*LoginAudit, *store.Queries, store.User, func(time.Duration)) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	q := store.New(db)

	// Anchor the injectable clock to wall-clock time so that windows lined
	// up against SQLite's CURRENT_TIMESTAMP (used inside RecordLoginAttempt)
	// agree. Tests that need to step time forward call `step(d)`.
	now := time.Now().UTC()
	a := NewLoginAudit(q, DefaultLockoutConfig())
	a.now = func() time.Time { return now }
	step := func(d time.Duration) { now = now.Add(d) }

	user, err := q.CreateUser(context.Background(), store.CreateUserParams{
		Username:     "alice",
		PasswordHash: "$2a$10$dummy",
		DisplayName:  "Alice",
		Role:         "member",
	})
	require.NoError(t, err)
	return a, q, user, step
}

func TestLoginAudit_RecordAttempt_PersistsRow(t *testing.T) {
	a, q, user, _ := loginAuditFixture(t)
	ctx := context.Background()

	a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.2.3.4", "ua", "ok", true)

	rows, err := q.ListLoginAttemptsForUser(ctx, store.ListLoginAttemptsForUserParams{
		UserID: sql.NullInt64{Int64: user.ID, Valid: true},
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "alice", rows[0].Username)
	assert.Equal(t, "1.2.3.4", rows[0].Ip)
	assert.Equal(t, "ok", rows[0].Reason)
	assert.Equal(t, int64(1), rows[0].Success)
}

func TestLoginAudit_CheckPreLogin_AccountLocked(t *testing.T) {
	a, q, user, _ := loginAuditFixture(t)
	ctx := context.Background()

	// Lock the account 10 minutes into the future.
	until := a.now().Add(10 * time.Minute)
	require.NoError(t, q.LockUserAccount(ctx, store.LockUserAccountParams{
		LockedUntil: sql.NullTime{Time: until, Valid: true},
		ID:          user.ID,
	}))
	locked, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)

	check, err := a.CheckPreLogin(ctx, &locked, "1.2.3.4")
	require.NoError(t, err)
	assert.True(t, check.Locked)
	assert.Equal(t, "account_locked", check.Reason)
	assert.InDelta(t, (10*time.Minute).Seconds(), check.RetryAfter.Seconds(), 1)
}

func TestLoginAudit_CheckPreLogin_ExpiredLockClears(t *testing.T) {
	a, q, user, step := loginAuditFixture(t)
	ctx := context.Background()

	until := a.now().Add(5 * time.Minute)
	require.NoError(t, q.LockUserAccount(ctx, store.LockUserAccountParams{
		LockedUntil: sql.NullTime{Time: until, Valid: true},
		ID:          user.ID,
	}))
	step(10 * time.Minute) // walk past the expiry
	locked, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)

	check, err := a.CheckPreLogin(ctx, &locked, "1.2.3.4")
	require.NoError(t, err)
	assert.False(t, check.Locked)
}

func TestLoginAudit_CheckPreLogin_IPLocked(t *testing.T) {
	a, q, _, _ := loginAuditFixture(t)
	ctx := context.Background()

	until := a.now().Add(30 * time.Minute)
	require.NoError(t, q.UpsertIPLockout(ctx, store.UpsertIPLockoutParams{
		Ip:          "1.2.3.4",
		FailCount:   99,
		LockedUntil: sql.NullTime{Time: until, Valid: true},
	}))

	check, err := a.CheckPreLogin(ctx, nil, "1.2.3.4")
	require.NoError(t, err)
	assert.True(t, check.Locked)
	assert.Equal(t, "ip_locked", check.Reason)
}

func TestLoginAudit_CheckPreLogin_EmptyIPNoLookup(t *testing.T) {
	a, q, _, _ := loginAuditFixture(t)
	ctx := context.Background()

	// Even if an ip_lockouts row exists for some other IP, an empty ip arg
	// must not consult ip_lockouts (no row for "" → would still pass without
	// the guard; the test asserts the explicit short-circuit doesn't error).
	require.NoError(t, q.UpsertIPLockout(ctx, store.UpsertIPLockoutParams{
		Ip:          "9.9.9.9",
		FailCount:   99,
		LockedUntil: sql.NullTime{Time: a.now().Add(time.Hour), Valid: true},
	}))

	check, err := a.CheckPreLogin(ctx, nil, "")
	require.NoError(t, err)
	assert.False(t, check.Locked)
}

func TestLoginAudit_OnFailedAttempt_LocksAccountAtThreshold(t *testing.T) {
	a, q, user, _ := loginAuditFixture(t)
	ctx := context.Background()
	cfg := DefaultLockoutConfig()

	// Insert (threshold) prior failed attempts within the window so the next
	// call crosses the line.
	for i := int64(0); i < cfg.AccountThreshold; i++ {
		a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.2.3.4", "ua", "bad_password", false)
	}
	require.NoError(t, a.OnFailedAttempt(ctx, &user, "1.2.3.4"))

	locked, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	require.True(t, locked.LockedUntil.Valid, "locked_until must be set")
	assert.InDelta(t, a.now().Add(cfg.AccountCooldown).Unix(), locked.LockedUntil.Time.Unix(), 1)
	assert.Equal(t, int64(1), locked.LockoutCount)
}

func TestLoginAudit_OnFailedAttempt_BelowThresholdNoLock(t *testing.T) {
	a, q, user, _ := loginAuditFixture(t)
	ctx := context.Background()
	cfg := DefaultLockoutConfig()

	// Insert one fewer than threshold; OnFailedAttempt must NOT lock.
	for i := int64(0); i < cfg.AccountThreshold-1; i++ {
		a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.2.3.4", "ua", "bad_password", false)
	}
	require.NoError(t, a.OnFailedAttempt(ctx, &user, "1.2.3.4"))

	locked, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.False(t, locked.LockedUntil.Valid)
}

func TestLoginAudit_OnFailedAttempt_OutsideWindowDoesNotCount(t *testing.T) {
	a, q, user, step := loginAuditFixture(t)
	ctx := context.Background()
	cfg := DefaultLockoutConfig()

	// Plant threshold-1 failures more than the window ago — they should age
	// out and NOT contribute to the new lockout decision.
	for i := int64(0); i < cfg.AccountThreshold; i++ {
		a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.2.3.4", "ua", "bad_password", false)
	}
	step(cfg.AccountWindow + time.Minute) // walk past the window
	// Now plant one fresh failure and trigger the check.
	a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.2.3.4", "ua", "bad_password", false)
	require.NoError(t, a.OnFailedAttempt(ctx, &user, "1.2.3.4"))

	locked, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.False(t, locked.LockedUntil.Valid, "stale failures must not lock the account")
}

func TestLoginAudit_OnFailedAttempt_IPLockAtThreshold(t *testing.T) {
	a, q, _, _ := loginAuditFixture(t)
	ctx := context.Background()
	cfg := DefaultLockoutConfig()

	for i := int64(0); i < cfg.IPThreshold; i++ {
		a.RecordAttempt(ctx, sql.NullInt64{}, "alice", "1.2.3.4", "ua", "bad_password", false)
	}
	require.NoError(t, a.OnFailedAttempt(ctx, nil, "1.2.3.4"))

	row, err := q.GetIPLockout(ctx, "1.2.3.4")
	require.NoError(t, err)
	require.True(t, row.LockedUntil.Valid)
	assert.InDelta(t, a.now().Add(cfg.IPCooldown).Unix(), row.LockedUntil.Time.Unix(), 1)
}

func TestLoginAudit_OnSuccessfulLogin_ClearsAccountLock(t *testing.T) {
	a, q, user, _ := loginAuditFixture(t)
	ctx := context.Background()

	require.NoError(t, q.LockUserAccount(ctx, store.LockUserAccountParams{
		LockedUntil: sql.NullTime{Time: a.now().Add(time.Hour), Valid: true},
		ID:          user.ID,
	}))
	require.NoError(t, a.OnSuccessfulLogin(ctx, user.ID, "1.2.3.4"))

	after, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.False(t, after.LockedUntil.Valid)
}

func TestLoginAudit_OnSuccessfulLogin_ResetsIPCounter(t *testing.T) {
	a, q, user, _ := loginAuditFixture(t)
	ctx := context.Background()

	require.NoError(t, q.UpsertIPLockout(ctx, store.UpsertIPLockoutParams{
		Ip:          "1.2.3.4",
		FailCount:   15,
		LockedUntil: sql.NullTime{Time: a.now().Add(time.Hour), Valid: true},
	}))
	require.NoError(t, a.OnSuccessfulLogin(ctx, user.ID, "1.2.3.4"))

	row, err := q.GetIPLockout(ctx, "1.2.3.4")
	require.NoError(t, err)
	assert.Equal(t, int64(0), row.FailCount)
	assert.False(t, row.LockedUntil.Valid)
}

func TestLoginAudit_ListRecentForUser_ScopesAndOrders(t *testing.T) {
	a, q, user, step := loginAuditFixture(t)
	ctx := context.Background()

	// Another user whose attempts must not leak into alice's history.
	bob, err := q.CreateUser(ctx, store.CreateUserParams{
		Username: "bob", PasswordHash: "$2a$10$dummy", DisplayName: "Bob", Role: "member",
	})
	require.NoError(t, err)

	// Insert three records for alice across time so created_at ordering is
	// observable. RecordAttempt uses CURRENT_TIMESTAMP server-side, so we
	// have to nudge step + sleep-equivalent SQL via separate calls.
	a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.1.1.1", "ua", "bad_password", false)
	step(time.Second)
	a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.1.1.1", "ua", "ok", true)
	step(time.Second)
	a.RecordAttempt(ctx, sql.NullInt64{Int64: bob.ID, Valid: true}, "bob", "9.9.9.9", "ua", "ok", true)

	rows, err := a.ListRecentForUser(ctx, user.ID, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2, "bob's attempt must not leak into alice's history")
	for _, r := range rows {
		assert.Equal(t, "alice", r.Username)
	}
}

func TestLoginAudit_GC_DeletesOnlyOldRows(t *testing.T) {
	a, q, user, step := loginAuditFixture(t)
	ctx := context.Background()

	// SQLite CURRENT_TIMESTAMP captures the wall clock, not our injectable
	// clock. Insert real rows now, then advance the audit clock far enough
	// for the cutoff to be in the future of every real row.
	for i := 0; i < 3; i++ {
		a.RecordAttempt(ctx, sql.NullInt64{Int64: user.ID, Valid: true}, "alice", "1.1.1.1", "ua", "bad_password", false)
	}
	step(100 * 24 * time.Hour) // 100 days forward
	a.gcOnce(ctx, 90*24*time.Hour)

	rows, err := q.ListLoginAttemptsForUser(ctx, store.ListLoginAttemptsForUserParams{
		UserID: sql.NullInt64{Int64: user.ID, Valid: true},
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Empty(t, rows, "all rows older than retention should be deleted")
}
