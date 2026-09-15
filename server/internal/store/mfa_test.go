package store

import (
	"context"
	"testing"
)

// TestCreateMFAIsUpsert pins the enrollment re-entry contract: calling Setup
// again for a user who already has a (possibly stale, unconfirmed) user_mfa
// row must overwrite that row instead of failing on the user_id primary key.
// Regressed in production as SQLSTATE 23505 on user_mfa_pkey when the team
// mandatory-MFA enroll gate re-ran Setup for users with a leftover row.
func TestCreateMFAIsUpsert(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	q := NewQueries(db)
	ctx := context.Background()

	u, err := q.CreateUser(ctx, CreateUserParams{
		Username: "mfa-user", PasswordHash: "x", DisplayName: "MFA User", Role: "member",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := q.CreateMFA(ctx, CreateMFAParams{
		UserID: u.ID, Method: "totp", SecretCiphertext: []byte("first-secret"),
	}); err != nil {
		t.Fatalf("first CreateMFA: %v", err)
	}

	// Second Setup (fresh QR after a refresh / abandoned enrollment) must
	// overwrite the stored secret, not violate user_mfa_pkey.
	if err := q.CreateMFA(ctx, CreateMFAParams{
		UserID: u.ID, Method: "totp", SecretCiphertext: []byte("second-secret"),
	}); err != nil {
		t.Fatalf("second CreateMFA must upsert, got: %v", err)
	}

	mfa, err := q.GetMFAByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetMFAByUserID: %v", err)
	}
	if string(mfa.SecretCiphertext) != "second-secret" {
		t.Errorf("secret not overwritten: %q", mfa.SecretCiphertext)
	}
	if mfa.EnabledAt.Valid || mfa.ConfirmedAt.Valid {
		t.Errorf("re-setup must reset to unconfirmed, got enabled_at=%v confirmed_at=%v",
			mfa.EnabledAt, mfa.ConfirmedAt)
	}
}
