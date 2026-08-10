package pairingcrypto

import (
	"bytes"
	"testing"
)

func TestBackupWrapUnwrap(t *testing.T) {
	id, _ := GenerateIdentity()
	blob, meta, err := WrapIdentity("correct-horse-battery-staple", "account-1", id)
	if err != nil {
		t.Fatalf("WrapIdentity: %v", err)
	}
	if len(blob) < 160 {
		t.Fatalf("blob too short: %d", len(blob))
	}
	if meta.KDF != "argon2id" {
		t.Fatalf("KDF label: %q", meta.KDF)
	}
	got, err := UnwrapIdentity("correct-horse-battery-staple", "account-1", blob, meta)
	if err != nil {
		t.Fatalf("UnwrapIdentity: %v", err)
	}
	if !bytes.Equal(got.XPriv, id.XPriv) {
		t.Fatal("XPriv mismatch")
	}
	if !bytes.Equal(got.EdPriv, id.EdPriv) {
		t.Fatal("EdPriv mismatch")
	}
}

func TestBackupWrongPassphraseFails(t *testing.T) {
	id, _ := GenerateIdentity()
	blob, meta, _ := WrapIdentity("right-pw", "account-1", id)
	if _, err := UnwrapIdentity("wrong-pw", "account-1", blob, meta); err == nil {
		t.Fatal("expected decryption error on wrong passphrase")
	}
}

func TestBackupWrongAccountFails(t *testing.T) {
	id, _ := GenerateIdentity()
	blob, meta, _ := WrapIdentity("pw", "account-A", id)
	if _, err := UnwrapIdentity("pw", "account-B", blob, meta); err == nil {
		t.Fatal("wrong account ID should fail (used in AAD)")
	}
}
