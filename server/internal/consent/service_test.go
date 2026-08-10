package consent

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

func newTestSvc(t *testing.T) *Service {
	t.Helper()
	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	if _, err := raw.Exec(`CREATE TABLE user_consents (
		user_id INTEGER PRIMARY KEY,
		version TEXT NOT NULL,
		agreed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	return NewService(store.Wrap(raw))
}

func TestStatusNeedsConsentWhenNoRow(t *testing.T) {
	svc := newTestSvc(t)
	st, err := svc.Status(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsConsent || st.AgreedVersion != "" || st.CurrentVersion != CurrentVersion {
		t.Fatalf("expected needs-consent fresh user, got %+v", st)
	}
	if svc.HasConsented(context.Background(), 7) {
		t.Fatal("HasConsented should be false before accept")
	}
}

func TestAcceptThenConsented(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	if err := svc.Accept(ctx, 7, CurrentVersion); err != nil {
		t.Fatal(err)
	}
	if !svc.HasConsented(ctx, 7) {
		t.Fatal("HasConsented should be true after accept")
	}
	st, _ := svc.Status(ctx, 7)
	if st.NeedsConsent || st.AgreedVersion != CurrentVersion {
		t.Fatalf("expected satisfied status, got %+v", st)
	}
	// A different user is unaffected.
	if svc.HasConsented(ctx, 8) {
		t.Fatal("user 8 should still need consent")
	}
}

func TestAcceptIsIdempotent(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	if err := svc.Accept(ctx, 7, CurrentVersion); err != nil {
		t.Fatal(err)
	}
	if err := svc.Accept(ctx, 7, CurrentVersion); err != nil {
		t.Fatalf("second accept should upsert cleanly, got %v", err)
	}
	if v, _ := svc.AgreedVersion(ctx, 7); v != CurrentVersion {
		t.Fatalf("expected %q, got %q", CurrentVersion, v)
	}
}

func TestAcceptRejectsWrongVersion(t *testing.T) {
	svc := newTestSvc(t)
	if err := svc.Accept(context.Background(), 7, "1999-old"); err != ErrVersionMismatch {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
	if svc.HasConsented(context.Background(), 7) {
		t.Fatal("rejected accept must not record consent")
	}
}

func TestReConsentAfterVersionBump(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	// Simulate a user who accepted an older version: insert directly.
	if _, err := svc.db.ExecContext(ctx,
		`INSERT INTO user_consents (user_id, version) VALUES (?, ?)`, 7, "2020-01-01"); err != nil {
		t.Fatal(err)
	}
	if svc.HasConsented(ctx, 7) {
		t.Fatal("stale-version user must need re-consent")
	}
	st, _ := svc.Status(ctx, 7)
	if !st.NeedsConsent || st.AgreedVersion != "2020-01-01" {
		t.Fatalf("expected needs-consent with old version, got %+v", st)
	}
}
