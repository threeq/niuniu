package relayclient

import (
	"errors"
	"os"
	"testing"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
	"github.com/zalando/go-keyring"
)

const testScope = "default"

func TestLoadOrCreateIdentity_RoundTrip(t *testing.T) {
	if os.Getenv("RELAYCLIENT_KEYCHAIN_TESTS") != "1" {
		t.Skip("set RELAYCLIENT_KEYCHAIN_TESTS=1 to run (modifies OS keychain)")
	}
	_ = DeleteIdentity(testScope)
	t.Cleanup(func() { _ = DeleteIdentity(testScope) })

	id1, created, err := LoadOrCreateIdentity(testScope)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on empty keychain")
	}
	id2, created2, err := LoadOrCreateIdentity(testScope)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity second: %v", err)
	}
	if created2 {
		t.Fatal("expected created=false on second call")
	}
	if string(id1.XPriv) != string(id2.XPriv) {
		t.Fatal("identity should persist across calls")
	}
}

func TestLoadIdentityNotFound(t *testing.T) {
	if os.Getenv("RELAYCLIENT_KEYCHAIN_TESTS") != "1" {
		t.Skip("set RELAYCLIENT_KEYCHAIN_TESTS=1 to run (modifies OS keychain)")
	}
	_ = DeleteIdentity(testScope)
	_, err := LoadIdentity(testScope)
	if err == nil {
		t.Fatal("expected error on empty keychain")
	}
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("expected ErrIdentityNotFound, got %v", err)
	}
}

// seedMockIdentity saves a fresh identity into a mock keyring under `scope`
// with the given fingerprint.
func seedMockIdentity(t *testing.T, scope, fp string) {
	t.Helper()
	machineFingerprintFn = func() string { return fp }
	_ = keyring.Delete(keyringService, identityKey(scope))
	_ = keyring.Delete(keyringService, fingerprintKey(scope))
	fresh, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := SaveIdentity(scope, fresh); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
}

func TestLoadIdentity_MachineFingerprintMismatch(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { machineFingerprintFn = defaultMachineFingerprint })

	seedMockIdentity(t, testScope, "machine-A")

	machineFingerprintFn = func() string { return "machine-B" }

	_, err := LoadIdentity(testScope)
	if !errors.Is(err, ErrMachineFingerprintMismatch) {
		t.Fatalf("expected ErrMachineFingerprintMismatch, got %v", err)
	}
}

func TestLoadIdentity_FingerprintMatch(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { machineFingerprintFn = defaultMachineFingerprint })

	seedMockIdentity(t, testScope, "machine-A")
	if _, err := LoadIdentity(testScope); err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
}

func TestLoadIdentity_EmptyFingerprintSkipCheck(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { machineFingerprintFn = defaultMachineFingerprint })

	machineFingerprintFn = func() string { return "" }
	_ = keyring.Delete(keyringService, identityKey(testScope))
	_ = keyring.Delete(keyringService, fingerprintKey(testScope))
	fresh, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := SaveIdentity(testScope, fresh); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	machineFingerprintFn = func() string { return "machine-X" }
	if _, err := LoadIdentity(testScope); err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
}

func TestIdentity_ScopeIsolation(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { machineFingerprintFn = defaultMachineFingerprint })
	machineFingerprintFn = func() string { return "machine" }

	id1, created1, err := LoadOrCreateIdentity("alice")
	if err != nil || !created1 {
		t.Fatalf("alice create: %v created=%v", err, created1)
	}
	id2, created2, err := LoadOrCreateIdentity("bob")
	if err != nil || !created2 {
		t.Fatalf("bob create: %v created=%v", err, created2)
	}
	if string(id1.XPriv) == string(id2.XPriv) {
		t.Fatal("alice and bob must have different identities")
	}

	// Reload must return the scope-matching identity.
	aliceReload, _, err := LoadOrCreateIdentity("alice")
	if err != nil {
		t.Fatalf("alice reload: %v", err)
	}
	if string(aliceReload.XPriv) != string(id1.XPriv) {
		t.Fatal("alice reload returned different identity")
	}

	// Deleting one scope must not touch the other.
	if err := DeleteIdentity("alice"); err != nil {
		t.Fatalf("delete alice: %v", err)
	}
	if _, err := LoadIdentity("alice"); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("alice after delete: %v", err)
	}
	if _, err := LoadIdentity("bob"); err != nil {
		t.Fatalf("bob after alice-delete still loadable: %v", err)
	}
}
