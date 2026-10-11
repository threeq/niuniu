package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKeyStore is an in-memory KeyStore. err (when set) simulates an unusable
// backend such as a Linux host with no Secret Service.
type fakeKeyStore struct {
	key   []byte
	err   error
	sets  int
	dropW bool // silently drop writes (a backend that lies about success)
}

func (f *fakeKeyStore) Get() ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.key == nil {
		return nil, ErrKeyNotFound
	}
	return append([]byte(nil), f.key...), nil
}

func (f *fakeKeyStore) Set(key []byte) error {
	if f.err != nil {
		return f.err
	}
	f.sets++
	if !f.dropW {
		f.key = append([]byte(nil), key...)
	}
	return nil
}

func (f *fakeKeyStore) Delete() error {
	if f.err != nil {
		return f.err
	}
	f.key = nil
	return nil
}

func writeLegacyFile(t *testing.T, path string, key []byte) {
	t.Helper()
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
}

func mustKey(t *testing.T, seed byte) []byte {
	t.Helper()
	k := make([]byte, keyLen)
	for i := range k {
		k[i] = seed + byte(i)
	}
	return k
}

// TestFreshInstallUsesKeychain: nothing on disk, empty keychain → generate and
// persist in the keychain, never touching the plaintext file.
func TestFreshInstallUsesKeychain(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	fake := &fakeKeyStore{}

	out, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatalf("LoadOrMigrate: %v", err)
	}
	if out.Backend != BackendKeychain {
		t.Fatalf("backend = %q, want keychain", out.Backend)
	}
	if out.Degraded || out.Migrated {
		t.Fatalf("unexpected flags: %+v", out)
	}
	if len(fake.key) != keyLen {
		t.Fatalf("keychain key length = %d, want %d", len(fake.key), keyLen)
	}
	if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext file must not be created, stat err = %v", err)
	}
}

// TestMigrationFromLegacyFile: an existing plaintext key is moved into the
// keychain, the file is deleted, and the key VALUE is preserved so existing
// ciphertext keeps decrypting (no re-encryption).
func TestMigrationFromLegacyFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	oldKey := mustKey(t, 7)
	writeLegacyFile(t, secretPath, oldKey)

	// Ciphertext produced before the migration, by the legacy file key.
	legacy, err := LoadOrCreate(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := legacy.Encrypt([]byte("provider-token"))
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeKeyStore{}
	out, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatalf("LoadOrMigrate: %v", err)
	}
	if out.Backend != BackendKeychain {
		t.Fatalf("backend = %q, want keychain", out.Backend)
	}
	if !out.Migrated {
		t.Fatal("Migrated = false, want true")
	}
	if !bytes.Equal(fake.key, oldKey) {
		t.Fatal("keychain key differs from the legacy file key — existing data would be lost")
	}
	if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy file must be deleted, stat err = %v", err)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("migration should report a warning/notice")
	}

	// Existing ciphertext still decrypts under the migrated keyring.
	pt, err := out.Keyring.Decrypt(ct)
	if err != nil || string(pt) != "provider-token" {
		t.Fatalf("pre-migration ciphertext no longer decrypts: pt=%q err=%v", pt, err)
	}
}

// TestMigrationIdempotent: a second run reuses the keychain key and does not
// regenerate, re-migrate or recreate the file.
func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	writeLegacyFile(t, secretPath, mustKey(t, 3))

	fake := &fakeKeyStore{}
	first, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	setsAfterFirst := fake.sets

	second, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.Migrated {
		t.Fatal("second run must not re-migrate")
	}
	if fake.sets != setsAfterFirst {
		t.Fatalf("second run wrote to the keychain again (%d → %d)", setsAfterFirst, fake.sets)
	}

	ct, err := first.Keyring.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := second.Keyring.Decrypt(ct); err != nil || string(pt) != "x" {
		t.Fatalf("second run produced a different key: pt=%q err=%v", pt, err)
	}
	if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file must stay deleted, stat err = %v", err)
	}
}

// TestDegradedFallbackWhenKeychainUnavailable: no Secret Service → write and
// use the plaintext file, flagged and warned (never silent).
func TestDegradedFallbackWhenKeychainUnavailable(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	fake := &fakeKeyStore{err: errors.New("The name org.freedesktop.secrets was not provided by any .service files")}

	out, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatalf("LoadOrMigrate: %v", err)
	}
	if out.Backend != BackendFile || !out.Degraded {
		t.Fatalf("want degraded file backend, got backend=%q degraded=%v", out.Backend, out.Degraded)
	}
	if len(out.Warnings) == 0 || !strings.Contains(strings.ToLower(out.Warnings[0]), "plaintext") {
		t.Fatalf("degradation must warn about plaintext storage, warnings=%v", out.Warnings)
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("fallback file missing: %v", err)
	}
	if info.Size() != keyLen {
		t.Fatalf("fallback file size = %d, want %d", info.Size(), keyLen)
	}

	// The degraded key is stable across restarts.
	again, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := out.Keyring.Encrypt([]byte("y"))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := again.Keyring.Decrypt(ct); err != nil || string(pt) != "y" {
		t.Fatalf("degraded key not stable: pt=%q err=%v", pt, err)
	}
}

// TestDegradeOnDroppedKeychainWrite: a backend that accepts Set but drops it
// must not cause the plaintext file to be deleted.
func TestDegradeOnDroppedKeychainWrite(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	oldKey := mustKey(t, 11)
	writeLegacyFile(t, secretPath, oldKey)
	fake := &fakeKeyStore{dropW: true}

	out, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Degraded || out.Backend != BackendFile {
		t.Fatalf("dropped write must degrade to file, got %+v", out)
	}
	if !bytes.Equal(out.Keyring.current, oldKey) {
		t.Fatal("degraded path must keep the legacy key value")
	}
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatalf("legacy file must survive a failed migration: %v", err)
	}
}

// TestExplicitFileBackend: kc == nil uses the file and never consults the
// OS store.
func TestExplicitFileBackend(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")

	out, err := LoadOrMigrate(nil, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.Backend != BackendFile || out.Degraded {
		t.Fatalf("explicit file backend must not be flagged degraded: %+v", out)
	}
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestConflictingLegacyFileKept: keychain is authoritative; a file holding a
// different key is preserved for manual recovery and warned about.
func TestConflictingLegacyFileKept(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	fake := &fakeKeyStore{key: mustKey(t, 100)}
	writeLegacyFile(t, secretPath, mustKey(t, 200))

	out, err := LoadOrMigrate(fake, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.Backend != BackendKeychain {
		t.Fatalf("backend = %q, want keychain", out.Backend)
	}
	if !bytes.Equal(out.Keyring.current, fake.key) {
		t.Fatal("keychain key must be authoritative on conflict")
	}
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatalf("conflicting file must be kept for recovery: %v", err)
	}
	if len(out.Warnings) == 0 || !strings.Contains(out.Warnings[0], "DIFFERENT") {
		t.Fatalf("conflict must be warned about, warnings=%v", out.Warnings)
	}
}

// TestRollbackToFileBackend: exporting the keychain key and switching to the
// file backend reproduces the same key, so existing ciphertext survives.
func TestRollbackToFileBackend(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")

	keychainOut, err := LoadOrMigrate(&fakeKeyStore{}, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := keychainOut.Keyring.Encrypt([]byte("rollback"))
	if err != nil {
		t.Fatal(err)
	}
	if err := keychainOut.Keyring.ExportToFile(secretPath); err != nil {
		t.Fatal(err)
	}

	fileOut, err := LoadOrMigrate(nil, secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if fileOut.Backend != BackendFile {
		t.Fatalf("backend = %q, want file", fileOut.Backend)
	}
	if pt, err := fileOut.Keyring.Decrypt(ct); err != nil || string(pt) != "rollback" {
		t.Fatalf("rollback changed the key: pt=%q err=%v", pt, err)
	}
}

func TestExportToFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "exported")
	kr := &Keyring{current: mustKey(t, 42)}

	if err := kr.ExportToFile(outPath); err != nil {
		t.Fatalf("ExportToFile: %v", err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, kr.current) {
		t.Fatal("exported key does not match")
	}
}

// TestLoadIntegrationKeyringExplicitFileBackend exercises the production entry
// point's env override without touching the OS keychain.
func TestLoadIntegrationKeyringExplicitFileBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvBackend, "file")

	out, err := LoadIntegrationKeyring(dir)
	if err != nil {
		t.Fatalf("LoadIntegrationKeyring: %v", err)
	}
	if out.Backend != BackendFile {
		t.Fatalf("backend = %q, want file", out.Backend)
	}
	if _, err := os.Stat(filepath.Join(dir, "integration_secret")); err != nil {
		t.Fatalf("key file not created: %v", err)
	}
}

func TestLoadIntegrationKeyringRejectsUnknownBackend(t *testing.T) {
	t.Setenv(EnvBackend, "smoke-signals")
	if _, err := LoadIntegrationKeyring(t.TempDir()); err == nil {
		t.Fatal("unknown backend must be rejected")
	}
}

// TestOSKeychainRoundTrip runs against the real OS secure store. It uses a
// throwaway service/account so it never touches the production entry, and
// skips on hosts without a usable keychain (headless Linux / CI).
func TestOSKeychainRoundTrip(t *testing.T) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	store := KeychainStore{Service: "niuniu-desktop-test", Account: "roundtrip-" + hex.EncodeToString(buf)}

	key := mustKey(t, 55)
	if err := store.Set(key); err != nil {
		t.Skipf("OS keychain unavailable, skipping round-trip: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete() })

	got, err := store.Get()
	if err != nil {
		t.Fatalf("keychain Get: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("keychain round-trip mismatch")
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("keychain Delete: %v", err)
	}
	if _, err := store.Get(); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("after delete want ErrKeyNotFound, got %v", err)
	}
}
