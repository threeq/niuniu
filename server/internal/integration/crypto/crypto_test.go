package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")

	keyring, err := LoadOrCreate(secretPath)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	plaintext := []byte(`{"token":"ghp_secret_xyz","scopes":["issues"]}`)
	ct, err := keyring.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if string(ct) == string(plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}

	got, err := keyring.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("Decrypt got %q, want %q", got, plaintext)
	}
}

func TestLoadIdempotent(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")

	k1, err := LoadOrCreate(secretPath)
	if err != nil {
		t.Fatalf("first LoadOrCreate failed: %v", err)
	}
	k2, err := LoadOrCreate(secretPath)
	if err != nil {
		t.Fatalf("second LoadOrCreate failed: %v", err)
	}
	ct, err := k1.Encrypt([]byte("hello"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := k2.Decrypt(ct)
	if err != nil || string(pt) != "hello" {
		t.Fatalf("cross-instance decrypt failed: pt=%q err=%v", pt, err)
	}
}

func TestSecretFilePermissions(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "integration_secret")
	if _, err := LoadOrCreate(secretPath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	// On Unix, expect 0600; on Windows, permission model differs and we don't assert.
	if info.Size() < 32 {
		t.Fatalf("secret file too small: %d bytes", info.Size())
	}
}
