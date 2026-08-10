package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// expiryOf reads the stored expires_at for a raw token (re-deriving the hash
// the same way the service does).
func expiryOf(t *testing.T, q *store.Queries, raw string) time.Time {
	t.Helper()
	h := sha256.Sum256([]byte(raw))
	row, err := q.GetMCPSessionByHash(context.Background(), base64.RawURLEncoding.EncodeToString(h[:]))
	if err != nil {
		t.Fatalf("GetMCPSessionByHash: %v", err)
	}
	return row.ExpiresAt
}

func openMCPTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	store.Migrate(db)
	_, _ = db.Exec(`INSERT INTO workspaces (id, path, status) VALUES (1, '/tmp/ws', 'created')`)
	return db
}

func TestMCPSessionCreateValidate(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	svc := NewMCPSessionService(store.New(db))
	raw, err := svc.Create(context.Background(), 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		t.Fatal("empty raw token")
	}
	wsID, err := svc.Validate(context.Background(), raw)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if wsID != 1 {
		t.Errorf("wsID = %d; want 1", wsID)
	}
	if _, err := svc.Validate(context.Background(), "not-a-real-token"); err == nil {
		t.Error("expected validate to fail on unknown token")
	}
}

func TestMCPSessionExpired(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	svc := NewMCPSessionService(store.New(db))
	raw, err := svc.Create(context.Background(), 1, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Validate(context.Background(), raw); err == nil {
		t.Error("expected validate to fail on expired token")
	}
}

// TestMCPSessionSlidingRenewal verifies that validating a token whose remaining
// lifetime has dropped within the renewal window pushes its expiry forward, so a
// long-running (autohost) session never trips over the fixed TTL.
func TestMCPSessionSlidingRenewal(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	q := store.New(db)
	svc := NewMCPSessionService(q)

	// Issued with 1h remaining — well inside the renewal window (< TTL/2).
	raw, err := svc.Create(context.Background(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	before := expiryOf(t, q, raw)

	if _, err := svc.Validate(context.Background(), raw); err != nil {
		t.Fatalf("validate: %v", err)
	}
	after := expiryOf(t, q, raw)

	// Expiry must have been pushed out to ~now+MCPSessionTTL, far beyond the
	// original 1h.
	if !after.After(before) {
		t.Fatalf("expiry not renewed: before=%v after=%v", before, after)
	}
	if want := time.Now().Add(MCPSessionTTL - time.Hour); !after.After(want) {
		t.Errorf("renewed expiry %v not pushed near now+TTL (want > %v)", after, want)
	}
}

// TestMCPSessionNoRenewalWhenFresh verifies that a token with plenty of life
// left is NOT renewed on validation (bounding DB writes to ~one per window).
func TestMCPSessionNoRenewalWhenFresh(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	q := store.New(db)
	svc := NewMCPSessionService(q)

	// Full TTL remaining (> renewal window), so validation must leave it alone.
	raw, err := svc.Create(context.Background(), 1, MCPSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	before := expiryOf(t, q, raw)
	if _, err := svc.Validate(context.Background(), raw); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if after := expiryOf(t, q, raw); !after.Equal(before) {
		t.Errorf("fresh token expiry changed: before=%v after=%v", before, after)
	}
}

// TestMCPSessionRenewForWorkspace verifies the heartbeat path: a workspace token
// about to expire is pushed out to ~now+TTL, keeping a weeks-long workspace's
// token alive through idle gaps for as long as its agent is live.
func TestMCPSessionRenewForWorkspace(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	q := store.New(db)
	svc := NewMCPSessionService(q)

	raw, err := svc.Create(context.Background(), 1, time.Minute) // nearly expired
	if err != nil {
		t.Fatal(err)
	}
	before := expiryOf(t, q, raw)

	if err := svc.RenewForWorkspace(context.Background(), 1); err != nil {
		t.Fatalf("RenewForWorkspace: %v", err)
	}
	after := expiryOf(t, q, raw)
	if !after.After(before) {
		t.Fatalf("expiry not extended: before=%v after=%v", before, after)
	}
	if want := time.Now().Add(MCPSessionTTL - time.Hour); !after.After(want) {
		t.Errorf("renewed expiry %v not near now+TTL (want > %v)", after, want)
	}
	// And the token validates cleanly after renewal.
	if _, err := svc.Validate(context.Background(), raw); err != nil {
		t.Errorf("validate after renew: %v", err)
	}
}

// TestMCPSessionRenewForWorkspaceNoToken confirms renewing a workspace with no
// token is a harmless no-op (does not error).
func TestMCPSessionRenewForWorkspaceNoToken(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	svc := NewMCPSessionService(store.New(db))
	if err := svc.RenewForWorkspace(context.Background(), 1); err != nil {
		t.Errorf("RenewForWorkspace with no token should be a no-op, got: %v", err)
	}
}

// TestMCPSessionExpiresAtBoundary verifies that a token with ttl=0 is rejected
// immediately: ExpiresAt == time.Now() must be treated as expired.
func TestMCPSessionExpiresAtBoundary(t *testing.T) {
	db := openMCPTestDB(t)
	defer db.Close()
	svc := NewMCPSessionService(store.New(db))
	// ttl=0 means ExpiresAt is set to approximately time.Now() at creation.
	raw, err := svc.Create(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The token's ExpiresAt is <= time.Now() by the time Validate runs, so it
	// must be rejected. Using Before semantics: !time.Now().Before(expiresAt).
	if _, err := svc.Validate(context.Background(), raw); err == nil {
		t.Error("expected validate to fail for token with ttl=0 (ExpiresAt boundary)")
	}
}
