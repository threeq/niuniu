package mfapolicy

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// newTestQueries builds an in-memory users table holding just the columns the
// policy reads, and seeds (id, role, mfa_enabled) rows.
func newTestQueries(t *testing.T, rows [][3]any) *store.Queries {
	t.Helper()
	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	if _, err := raw.Exec(`CREATE TABLE users (
		id                       INTEGER PRIMARY KEY,
		username                 TEXT NOT NULL DEFAULT '',
		password_hash            TEXT NOT NULL DEFAULT '',
		display_name             TEXT NOT NULL DEFAULT '',
		email                    TEXT NOT NULL DEFAULT '',
		role                     TEXT NOT NULL DEFAULT 'member',
		locked_until             TIMESTAMP,
		lockout_count            INTEGER NOT NULL DEFAULT 0,
		require_password_change  INTEGER NOT NULL DEFAULT 0,
		password_changed_at      TIMESTAMP,
		mfa_enabled              INTEGER NOT NULL DEFAULT 0,
		created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := raw.Exec(
			`INSERT INTO users (id, username, role, mfa_enabled) VALUES (?, ?, ?, ?)`,
			r[0], r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	return store.New(store.Wrap(raw))
}

// Team edition, enforce on, no role narrowing: every role owes enrollment until
// they enable it.
func TestNeedsSetupEveryRoleByDefault(t *testing.T) {
	q := newTestQueries(t, [][3]any{
		{1, "admin", 0},
		{2, "member", 0},
		{3, "viewer", 0},
		{4, "member", 1},
	})
	svc := NewService(q, true, true, true, nil)
	ctx := context.Background()

	for _, id := range []int64{1, 2, 3} {
		if !svc.NeedsSetup(ctx, id) {
			t.Fatalf("user %d must owe enrollment", id)
		}
	}
	if svc.NeedsSetup(ctx, 4) {
		t.Fatal("user 4 already has MFA enabled and must be allowed through")
	}
}

// required_roles narrows enforcement, for a staged rollout.
func TestNeedsSetupRespectsRequiredRoles(t *testing.T) {
	q := newTestQueries(t, [][3]any{{1, "admin", 0}, {2, "member", 0}})
	svc := NewService(q, true, true, true, []string{"Admin"}) // case-insensitive
	ctx := context.Background()

	if !svc.NeedsSetup(ctx, 1) {
		t.Fatal("admin is in required_roles and must owe enrollment")
	}
	if svc.NeedsSetup(ctx, 2) {
		t.Fatal("member is outside required_roles and must pass")
	}
}

// Personal/embedded edition has no login and a single local user; enforcement
// must never apply there.
func TestPersonalEditionNeverEnforces(t *testing.T) {
	q := newTestQueries(t, [][3]any{{1, "admin", 0}})
	svc := NewService(q, false, true, true, nil)
	if svc.Active() || svc.NeedsSetup(context.Background(), 1) {
		t.Fatal("personal edition must not enforce enrollment")
	}
}

func TestEnforceOffNeverBlocks(t *testing.T) {
	q := newTestQueries(t, [][3]any{{1, "admin", 0}})
	svc := NewService(q, true, false, true, nil)
	if svc.Active() || svc.NeedsSetup(context.Background(), 1) {
		t.Fatal("enforce=false must not block anybody")
	}
}

// If the MFA keyring failed to load, nobody CAN enroll. Enforcing would be a
// permanent lockout, so the policy must stand down.
func TestUnavailableSubsystemNeverBlocks(t *testing.T) {
	q := newTestQueries(t, [][3]any{{1, "admin", 0}})
	svc := NewService(q, true, true, false, nil)
	if svc.Active() || svc.NeedsSetup(context.Background(), 1) {
		t.Fatal("MFA-unavailable deployment must not block anybody")
	}
}

// A DB read failure must fail CLOSED, matching consent.HasConsented.
func TestNeedsSetupFailsClosedOnUnknownUser(t *testing.T) {
	q := newTestQueries(t, nil)
	svc := NewService(q, true, true, true, nil)
	if !svc.NeedsSetup(context.Background(), 99) {
		t.Fatal("unresolvable user must fail closed (blocked)")
	}
}

func TestStatusShape(t *testing.T) {
	q := newTestQueries(t, [][3]any{{1, "member", 0}, {2, "member", 1}})
	svc := NewService(q, true, true, true, nil)
	ctx := context.Background()

	st, err := svc.Status(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enforced || st.Enabled || !st.NeedsSetup {
		t.Fatalf("un-enrolled member: %+v", st)
	}

	st, err = svc.Status(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enforced || !st.Enabled || st.NeedsSetup {
		t.Fatalf("enrolled member: %+v", st)
	}
}

// Status must surface the read error so the SPA can fail OPEN (no undismissable
// page); the server-side guard remains the safety net.
func TestStatusSurfacesReadError(t *testing.T) {
	q := newTestQueries(t, nil)
	svc := NewService(q, true, true, true, nil)
	if _, err := svc.Status(context.Background(), 99); err == nil {
		t.Fatal("expected an error for an unknown user")
	}
}
