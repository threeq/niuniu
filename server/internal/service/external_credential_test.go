// External-tests for ExternalCredentialService — exercise the encrypt-on-
// create / decrypt-on-GetByID round-trip and the ErrNoCredential sentinel.
package service_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func TestExternalCredential_CreateAndGet(t *testing.T) {
	env := niutest.NewIsolationEnv(t)

	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := integration.NewRegistry()
	svc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)

	userID := env.UserA

	got, err := svc.Create(context.Background(), service.ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   userID,
		UserID:    userID,
		Provider:  integration.ProviderGitHub,
		Alias:     "test-cred",
		RawConfig: map[string]any{"token": "ghp_x"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Provider != integration.ProviderGitHub {
		t.Fatalf("provider mismatch: got %q want %q", got.Provider, integration.ProviderGitHub)
	}

	loaded, err := svc.GetByID(context.Background(), got.ID, "user", userID, userID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	tok, _ := loaded.Config["token"].(string)
	if tok != "ghp_x" {
		t.Fatalf("decrypted token mismatch: got %q want %q", tok, "ghp_x")
	}

	// List should redact the token — only metadata escapes the API.
	listed, err := svc.List(context.Background(), "user", userID, userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(listed))
	}
	if v, ok := listed[0].Config["token"]; ok {
		t.Fatalf("List leaked token: %v", v)
	}
}

func TestExternalCredential_CreatePreservesVerifiedAt(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := integration.NewRegistry()
	svc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	uid := env.UserA

	got, err := svc.Create(context.Background(), service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: uid, UserID: uid,
		Provider:  integration.ProviderGitHub,
		Alias:     "test-preserve",
		RawConfig: map[string]any{"token": "ghp_first"},
	})
	if err != nil {
		t.Fatalf("Create#1: %v", err)
	}

	// Touch verified-at through the store (Verify requires a live call).
	if err := env.Queries().TouchCredentialVerifiedAtByID(context.Background(), store.TouchCredentialVerifiedAtByIDParams{
		ID:        got.ID,
		OwnerType: "user", OwnerID: uid, UserID: uid,
	}); err != nil {
		t.Fatalf("TouchCredentialVerifiedAtByID: %v", err)
	}

	// UpdateConfig should bump the token; the query sets last_verified_at = NULL
	// because any config change invalidates prior verification.
	updated, err := svc.UpdateConfig(context.Background(), got.ID, "user", uid, uid, map[string]any{"token": "ghp_second"})
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if updated.LastVerifiedAt.Valid {
		t.Fatalf("UpdateConfig preserved last_verified_at; expected NULL because config change invalidates verification")
	}
	if tok, _ := updated.Config["token"].(string); tok != "ghp_second" {
		t.Fatalf("UpdateConfig did not update token: got %q", tok)
	}
}

func TestExternalCredential_GetDecryptedConfigByAlias(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	svc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	userID := env.UserA

	if _, err := svc.Create(context.Background(), service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: userID, UserID: userID,
		Provider: integration.ProviderName("imap"), Alias: "mailbox",
		RawConfig: map[string]any{"username": "alice@example.com", "password": "s3cret"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	owner := service.OwnerRef{Type: "user", ID: userID}
	// Hit: correct (owner, user, provider, alias).
	cfg, err := svc.GetDecryptedConfigByAlias(context.Background(), owner, userID, integration.ProviderName("imap"), "mailbox")
	if err != nil {
		t.Fatalf("GetDecryptedConfigByAlias: %v", err)
	}
	if cfg["password"] != "s3cret" {
		t.Fatalf("decrypted password mismatch: got %v", cfg["password"])
	}

	// Cross-user miss: same owner/provider/alias but a different user_id must
	// not resolve (org-shared mailbox isolation, spec §4.3/§5).
	// 规约3 回归：物化按 user_id 收窄，他人 user_id 必须落空，不得拿到他人密钥。
	if _, err := svc.GetDecryptedConfigByAlias(context.Background(), owner, userID+1, integration.ProviderName("imap"), "mailbox"); err != service.ErrNoCredential {
		t.Fatalf("expected ErrNoCredential for other user, got %v", err)
	}

	// Wrong alias miss.
	if _, err := svc.GetDecryptedConfigByAlias(context.Background(), owner, userID, integration.ProviderName("imap"), "other"); err != service.ErrNoCredential {
		t.Fatalf("expected ErrNoCredential for unknown alias, got %v", err)
	}
}

// TestListForOwner proves the org (team) listing is one-to-many: two different
// members each create an org-owned credential, and ListForOwner(org, orgID, "")
// returns BOTH — user_id is intentionally not a filter. The provider filter,
// when supplied, narrows correctly.
func TestListForOwner(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	svc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	ctx := context.Background()

	// userB joins OrgA so we have two distinct creators inside the same org.
	if _, err := env.DB.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`,
		env.OrgA, env.UserB); err != nil {
		t.Fatalf("add userB to OrgA: %v", err)
	}

	// Two org-owned github creds, created by different members.
	if _, err := svc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: env.OrgA, UserID: env.UserA,
		Provider: integration.ProviderGitHub, Alias: "team-a",
		RawConfig: map[string]any{"token": "ghp_a"},
	}); err != nil {
		t.Fatalf("Create cred by userA: %v", err)
	}
	if _, err := svc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: env.OrgA, UserID: env.UserB,
		Provider: integration.ProviderGitHub, Alias: "team-b",
		RawConfig: map[string]any{"token": "ghp_b"},
	}); err != nil {
		t.Fatalf("Create cred by userB: %v", err)
	}
	// A different-provider org cred to prove the provider filter narrows.
	if _, err := svc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: env.OrgA, UserID: env.UserA,
		Provider: integration.ProviderName("tapd"), Alias: "team-tapd",
		RawConfig: map[string]any{"token": "tok"},
	}); err != nil {
		t.Fatalf("Create tapd cred: %v", err)
	}

	// No provider filter: returns all 3 org creds across both creators.
	all, err := svc.ListForOwner(ctx, "org", env.OrgA, "")
	if err != nil {
		t.Fatalf("ListForOwner(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListForOwner(all) returned %d rows, want 3 (not user_id-narrowed)", len(all))
	}
	// Confirm both creators' user_ids show up (proves no user_id narrowing).
	sawA, sawB := false, false
	for _, c := range all {
		if c.UserID == env.UserA {
			sawA = true
		}
		if c.UserID == env.UserB {
			sawB = true
		}
		if v, ok := c.Config["token"]; ok {
			t.Fatalf("ListForOwner leaked token: %v", v)
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected creds from both userA and userB; sawA=%v sawB=%v", sawA, sawB)
	}

	// Provider filter: only the two github creds.
	gh, err := svc.ListForOwner(ctx, "org", env.OrgA, integration.ProviderGitHub)
	if err != nil {
		t.Fatalf("ListForOwner(github): %v", err)
	}
	if len(gh) != 2 {
		t.Fatalf("ListForOwner(github) returned %d rows, want 2", len(gh))
	}
	for _, c := range gh {
		if c.Provider != integration.ProviderGitHub {
			t.Fatalf("provider filter leaked %q", c.Provider)
		}
	}
}

func TestExternalCredential_GetMissing(t *testing.T) {
	env := niutest.NewIsolationEnv(t)

	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := integration.NewRegistry()
	svc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)

	_, err = svc.GetByID(context.Background(), 9999, "user", 9999, 9999)
	if err != service.ErrNoCredential {
		t.Fatalf("expected ErrNoCredential, got %v", err)
	}
}
