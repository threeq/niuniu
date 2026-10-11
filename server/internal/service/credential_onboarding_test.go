package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// buildCredOnboardingSvc wires the generic credential-onboarding service over
// the isolation env's DB, reusing the same keyring the production wiring uses.
func buildCredOnboardingSvc(t *testing.T, env *niutest.IsolationEnv) *service.CredentialOnboardingService {
	t.Helper()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	ext := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	ds := service.NewDataSourceService(env.Queries(), env.DB, kr, dataconn.NewRegistry())
	return service.NewCredentialOnboardingService(env.Queries(), ext, ds)
}

// TestCredentialOnboarding_TokenStoresOnlyHash: the raw token is never
// persisted — only its sha256 hex — and the raw value is not recoverable from
// the row.
func TestCredentialOnboarding_TokenStoresOnlyHash(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P-hash")
	svc := buildCredOnboardingSvc(t, env)
	ctx := context.Background()

	raw, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindProviderCredential, "GitHub", []string{"token"},
		map[string]any{"provider": "github", "alias": "work"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var storedHash string
	if err := env.DB.QueryRow(`SELECT token_hash FROM credential_onboarding_tokens`).Scan(&storedHash); err != nil {
		t.Fatalf("read stored token_hash: %v", err)
	}
	sum := sha256.Sum256([]byte(raw))
	want := hex.EncodeToString(sum[:])
	if storedHash != want {
		t.Errorf("stored hash = %q, want sha256(raw) = %q", storedHash, want)
	}
	if storedHash == raw {
		t.Error("raw token was persisted verbatim")
	}
	// The raw token must not be a valid lookup key.
	if _, err := env.Queries().GetCredentialOnboardingTokenByHash(ctx, raw); err == nil {
		t.Error("raw token resolved as a hash key — it should only be found via its sha256")
	}
}

// TestCredentialOnboarding_ProviderCredential_HappyPath: issue → info → submit
// creates an encrypted credential whose plaintext round-trips through the
// keyring, and the token is single-use.
func TestCredentialOnboarding_ProviderCredential_HappyPath(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P-provider")
	svc := buildCredOnboardingSvc(t, env)
	ctx := context.Background()

	raw, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindProviderCredential, "GitHub PAT",
		[]string{"token"}, map[string]any{"provider": "github", "alias": "Work GitHub", "auth_mode": "bearer"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	info, err := svc.Info(ctx, raw)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Kind != service.CredentialKindProviderCredential || info.Title != "GitHub PAT" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if len(info.Fields) != 1 || info.Fields[0].Name != "token" || !info.Fields[0].Secret {
		t.Fatalf("unexpected fields: %+v", info.Fields)
	}

	res, err := svc.Submit(ctx, raw, map[string]any{"token": "ghp_supersecret"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.CredentialID <= 0 || res.Alias != "Work GitHub" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// The stored blob is ciphertext (no plaintext leak)...
	var cipher string
	if err := env.DB.QueryRow(`SELECT config FROM external_provider_credentials WHERE id = ?`, res.CredentialID).Scan(&cipher); err != nil {
		t.Fatalf("read stored config: %v", err)
	}
	if strings.Contains(cipher, "ghp_supersecret") {
		t.Fatal("plaintext secret found in stored credential config")
	}
	// ...and decrypts back to the submitted value.
	ext := service.NewExternalCredentialService(env.Queries(), env.DB, mustKeyring(t, env), integration.NewRegistry())
	dec, err := ext.GetByID(ctx, res.CredentialID, "user", env.UserA, env.UserA)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if dec.Config["token"] != "ghp_supersecret" {
		t.Errorf("decrypted token = %v, want ghp_supersecret", dec.Config["token"])
	}

	// Single use: a second submit with the same token is rejected.
	if _, err := svc.Submit(ctx, raw, map[string]any{"token": "again"}); !errors.Is(err, service.ErrCredentialTokenInvalid) {
		t.Fatalf("second submit err = %v, want ErrCredentialTokenInvalid", err)
	}

	// has_credential is true; no secret is returned.
	has, err := svc.HasCredential(ctx, proj.ID, service.CredentialKindProviderCredential,
		map[string]any{"provider": "github", "alias": "Work GitHub"})
	if err != nil {
		t.Fatalf("HasCredential: %v", err)
	}
	if !has {
		t.Error("HasCredential = false, want true")
	}
}

// TestCredentialOnboarding_DataSource_HappyPath: a data-source link creates an
// encrypted, project-bound source; a second link targeting source_id updates it
// and a blank field keeps the stored secret.
func TestCredentialOnboarding_DataSource_HappyPath(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P-ds")
	svc := buildCredOnboardingSvc(t, env)
	ctx := context.Background()

	raw, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindDataSource, "MySQL 生产库",
		[]string{"host", "port", "user", "password", "database"},
		map[string]any{"name": "prod-mysql", "kind": "mysql", "scope_config": map[string]any{"tables_allow": []any{"orders"}}})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	res, err := svc.Submit(ctx, raw, map[string]any{
		"host": "db.internal", "port": "3306", "user": "app", "password": "s3cr3t", "database": "shop",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.SourceID <= 0 {
		t.Fatalf("expected a created source id, got %+v", res)
	}

	row, err := env.Queries().GetDataSource(ctx, res.SourceID)
	if err != nil {
		t.Fatalf("GetDataSource: %v", err)
	}
	if strings.Contains(row.Config, "s3cr3t") {
		t.Fatal("plaintext password found in stored data-source config")
	}
	if row.Config == "" {
		t.Fatal("stored config is empty")
	}

	has, err := svc.HasCredential(ctx, proj.ID, service.CredentialKindDataSource, map[string]any{"source_id": res.SourceID})
	if err != nil {
		t.Fatalf("HasCredential: %v", err)
	}
	if !has {
		t.Error("HasCredential = false, want true")
	}
	// The agent never learns the new source id — it must be able to confirm via
	// the name it declared.
	has, err = svc.HasCredential(ctx, proj.ID, service.CredentialKindDataSource, map[string]any{"name": "prod-mysql"})
	if err != nil {
		t.Fatalf("HasCredential(by name): %v", err)
	}
	if !has {
		t.Error("HasCredential(by name) = false, want true")
	}

	// The created source is bound to the project so the agent can query it.
	bindings, err := env.Queries().ListBindingsForSource(ctx, res.SourceID)
	if err != nil {
		t.Fatalf("ListDataSourceBindings: %v", err)
	}
	found := false
	for _, b := range bindings {
		if b.TargetType == "project" && b.TargetID == proj.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("source not bound to project %d: %+v", proj.ID, bindings)
	}

	// Update link: blank password keeps the stored one.
	raw2, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindDataSource, "MySQL 生产库（改）",
		[]string{"host", "port", "user", "password", "database"},
		map[string]any{"name": "prod-mysql", "kind": "mysql", "source_id": res.SourceID})
	if err != nil {
		t.Fatalf("Issue(update): %v", err)
	}
	if _, err := svc.Submit(ctx, raw2, map[string]any{"host": "db2.internal", "port": "3306", "user": "app", "database": "shop"}); err != nil {
		t.Fatalf("Submit(update): %v", err)
	}
	row2, err := env.Queries().GetDataSource(ctx, res.SourceID)
	if err != nil {
		t.Fatalf("GetDataSource(after update): %v", err)
	}
	ds := service.NewDataSourceService(env.Queries(), env.DB, mustKeyring(t, env), dataconn.NewRegistry())
	if _, err := ds.LoadConnConfig(ctx, res.SourceID, env.UserA, nil); err != nil {
		t.Fatalf("LoadConnConfig after update: %v", err)
	}
	if row2.Config == row.Config {
		t.Error("update did not rewrite the config (host should have changed)")
	}
}

// TestCredentialOnboarding_ExpiredTokenRejected: a token whose expiry has
// passed is rejected by both Info and Submit.
func TestCredentialOnboarding_ExpiredTokenRejected(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P-exp")
	svc := buildCredOnboardingSvc(t, env)
	ctx := context.Background()

	raw := "expired-raw-token-value"
	sum := sha256.Sum256([]byte(raw))
	if _, err := env.Queries().CreateCredentialOnboardingToken(ctx, store.CreateCredentialOnboardingTokenParams{
		TokenHash:  hex.EncodeToString(sum[:]),
		ProjectID:  proj.ID,
		IssuedBy:   env.UserA,
		Kind:       service.CredentialKindProviderCredential,
		Title:      "expired",
		FieldsJson: `[{"name":"token","label":"Token","secret":true,"required":true}]`,
		TargetJson: `{"provider":"github","alias":"expired"}`,
		ExpiresAt:  time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreateCredentialOnboardingToken: %v", err)
	}

	if _, err := svc.Info(ctx, raw); !errors.Is(err, service.ErrCredentialTokenInvalid) {
		t.Errorf("Info(expired) err = %v, want ErrCredentialTokenInvalid", err)
	}
	if _, err := svc.Submit(ctx, raw, map[string]any{"token": "x"}); !errors.Is(err, service.ErrCredentialTokenInvalid) {
		t.Errorf("Submit(expired) err = %v, want ErrCredentialTokenInvalid", err)
	}
}

// TestCredentialOnboarding_UnknownTokenRejected: an unknown token is
// indistinguishable from an expired one.
func TestCredentialOnboarding_UnknownTokenRejected(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildCredOnboardingSvc(t, env)
	if _, err := svc.Info(context.Background(), "no-such-token"); !errors.Is(err, service.ErrCredentialTokenInvalid) {
		t.Errorf("Info(unknown) err = %v, want ErrCredentialTokenInvalid", err)
	}
}

// TestCredentialOnboarding_FieldValidation covers the issue-time and
// submit-time field contract.
func TestCredentialOnboarding_FieldValidation(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P-validate")
	svc := buildCredOnboardingSvc(t, env)
	ctx := context.Background()

	// No fields at all.
	if _, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindDataSource, "x", nil,
		map[string]any{"name": "s", "kind": "mysql"}); !errors.Is(err, service.ErrCredentialLinkInvalid) {
		t.Errorf("Issue(no fields) err = %v, want ErrCredentialLinkInvalid", err)
	}
	// Illegal field name.
	if _, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindDataSource, "x", []string{"Bad Name"},
		map[string]any{"name": "s", "kind": "mysql"}); !errors.Is(err, service.ErrCredentialLinkInvalid) {
		t.Errorf("Issue(bad name) err = %v, want ErrCredentialLinkInvalid", err)
	}
	// Unknown kind.
	if _, err := svc.Issue(ctx, proj.ID, env.UserA, "nope", "x", []string{"token"}, nil); !errors.Is(err, service.ErrCredentialLinkInvalid) {
		t.Errorf("Issue(bad kind) err = %v, want ErrCredentialLinkInvalid", err)
	}
	// Missing target identity.
	if _, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindProviderCredential, "x", []string{"token"},
		map[string]any{"alias": "a"}); !errors.Is(err, service.ErrCredentialLinkInvalid) {
		t.Errorf("Issue(no provider) err = %v, want ErrCredentialLinkInvalid", err)
	}
	// A target that smuggles a secret-shaped key is rejected (unknown field).
	if _, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindProviderCredential, "x", []string{"token"},
		map[string]any{"provider": "github", "alias": "a", "password": "oops"}); !errors.Is(err, service.ErrCredentialLinkInvalid) {
		t.Errorf("Issue(secret in target) err = %v, want ErrCredentialLinkInvalid", err)
	}

	// Submit-time: undeclared field + missing required field.
	raw, err := svc.Issue(ctx, proj.ID, env.UserA, service.CredentialKindProviderCredential, "x", []string{"token"},
		map[string]any{"provider": "github", "alias": "a"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Submit(ctx, raw, map[string]any{"token": "t", "extra": "nope"}); !errors.Is(err, service.ErrCredentialSubmissionInvalid) {
		t.Errorf("Submit(undeclared) err = %v, want ErrCredentialSubmissionInvalid", err)
	}
	// The failed submit must NOT consume the token.
	if _, err := svc.Submit(ctx, raw, map[string]any{}); !errors.Is(err, service.ErrCredentialSubmissionInvalid) {
		t.Errorf("Submit(missing required) err = %v, want ErrCredentialSubmissionInvalid", err)
	}
	if _, err := svc.Submit(ctx, raw, map[string]any{"token": "t"}); err != nil {
		t.Errorf("Submit(valid after failures) err = %v, want nil", err)
	}
}

func mustKeyring(t *testing.T, env *niutest.IsolationEnv) *crypto.Keyring {
	t.Helper()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	return kr
}
