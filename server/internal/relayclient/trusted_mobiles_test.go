package relayclient

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const tScope = "default"

// overrideHome temporarily replaces the user home directory so that the
// trusted_mobiles DB is created in a per-test temp directory.
func overrideHome(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	type envVar struct{ key, orig string }
	vars := []envVar{
		{"HOME", os.Getenv("HOME")},
		{"USERPROFILE", os.Getenv("USERPROFILE")},
	}
	for _, v := range vars {
		if err := os.Setenv(v.key, dir); err != nil {
			t.Fatalf("setenv %s: %v", v.key, err)
		}
	}
	return dir, func() {
		for _, v := range vars {
			if v.orig == "" {
				_ = os.Unsetenv(v.key)
			} else {
				_ = os.Setenv(v.key, v.orig)
			}
		}
	}
}

func TestTrustedMobilesRoundTrip(t *testing.T) {
	_, cleanup := overrideHome(t)
	defer cleanup()

	xpub1 := []byte{0x01, 0x02, 0x03, 0x04}
	xpub2 := []byte{0x05, 0x06, 0x07, 0x08}

	if err := AppendTrustedMobile(tScope, xpub1, "Phone A", "android"); err != nil {
		t.Fatalf("AppendTrustedMobile xpub1: %v", err)
	}
	if err := AppendTrustedMobile(tScope, xpub2, "Phone B", "ios"); err != nil {
		t.Fatalf("AppendTrustedMobile xpub2: %v", err)
	}

	mobs, err := LoadTrustedMobiles(tScope)
	if err != nil {
		t.Fatalf("LoadTrustedMobiles: %v", err)
	}
	if len(mobs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(mobs))
	}

	hexMap := map[string]TrustedMobile{}
	for _, m := range mobs {
		hexMap[m.XpubHex] = m
	}
	hex1 := hex.EncodeToString(xpub1)
	if m, ok := hexMap[hex1]; !ok {
		t.Errorf("xpub1 not found in list")
	} else {
		if m.Name != "Phone A" {
			t.Errorf("name=%q; want 'Phone A'", m.Name)
		}
		if m.Platform != "android" {
			t.Errorf("platform=%q; want 'android'", m.Platform)
		}
		if m.PairedAt.IsZero() {
			t.Error("PairedAt should not be zero")
		}
	}

	if err := RemoveTrustedMobile(tScope, hex1); err != nil {
		t.Fatalf("RemoveTrustedMobile: %v", err)
	}
	mobs, err = LoadTrustedMobiles(tScope)
	if err != nil {
		t.Fatalf("LoadTrustedMobiles after remove: %v", err)
	}
	if len(mobs) != 1 {
		t.Fatalf("expected 1 entry after remove, got %d", len(mobs))
	}
	if mobs[0].XpubHex == hex1 {
		t.Error("xpub1 still present after removal")
	}
}

func TestTrustedMobilesMarkSeen(t *testing.T) {
	_, cleanup := overrideHome(t)
	defer cleanup()

	xpub := []byte{0xaa, 0xbb, 0xcc}
	if err := AppendTrustedMobile(tScope, xpub, "Device", ""); err != nil {
		t.Fatalf("AppendTrustedMobile: %v", err)
	}

	hexStr := hex.EncodeToString(xpub)
	if err := MarkSeen(tScope, hexStr); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	mobs, _ := LoadTrustedMobiles(tScope)
	if len(mobs) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(mobs))
	}
	if mobs[0].LastSeenAt.IsZero() {
		t.Error("LastSeenAt should be non-zero after MarkSeen")
	}
}

func TestTrustedMobilesMigration(t *testing.T) {
	homeDir, cleanup := overrideHome(t)
	defer cleanup()

	relayDir := filepath.Join(homeDir, ".niuniu", "relay")
	if err := os.MkdirAll(relayDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacyPath := filepath.Join(relayDir, "trusted_mobiles.txt")
	legacyContent := "# comment\n0102030405060708\naabbccdd\n\n"
	if err := os.WriteFile(legacyPath, []byte(legacyContent), 0600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// LoadTrustedMobiles on the DefaultScope should trigger migration; all
	// legacy entries land in DefaultScope.
	mobs, err := LoadTrustedMobiles(DefaultTrustedMobilesScope)
	if err != nil {
		t.Fatalf("LoadTrustedMobiles: %v", err)
	}
	if len(mobs) != 2 {
		t.Fatalf("expected 2 migrated entries, got %d", len(mobs))
	}

	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("legacy .txt file should have been renamed to .txt.migrated")
	}
	if _, err := os.Stat(legacyPath + ".migrated"); err != nil {
		t.Errorf("expected .txt.migrated to exist: %v", err)
	}

	mobs2, err := LoadTrustedMobiles(DefaultTrustedMobilesScope)
	if err != nil {
		t.Fatalf("second LoadTrustedMobiles: %v", err)
	}
	if len(mobs2) != 2 {
		t.Fatalf("expected 2 entries on second load, got %d", len(mobs2))
	}
}

func TestLoadTrustedMobileBytes(t *testing.T) {
	_, cleanup := overrideHome(t)
	defer cleanup()

	xpub := []byte{0xde, 0xad, 0xbe, 0xef}
	if err := AppendTrustedMobile(tScope, xpub, "", ""); err != nil {
		t.Fatalf("AppendTrustedMobile: %v", err)
	}

	bytes := LoadTrustedMobileBytes(tScope)
	if len(bytes) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(bytes))
	}
	if !equalBytes(bytes[0], xpub) {
		t.Errorf("got %x; want %x", bytes[0], xpub)
	}
}

// TestTrustedMobiles_ScopeIsolation verifies that one scope cannot see
// another scope's rows.
func TestTrustedMobiles_ScopeIsolation(t *testing.T) {
	_, cleanup := overrideHome(t)
	defer cleanup()

	xA := []byte{0xa0, 0xa1}
	xB := []byte{0xb0, 0xb1}
	if err := AppendTrustedMobile("alice", xA, "A", "x"); err != nil {
		t.Fatalf("append alice: %v", err)
	}
	if err := AppendTrustedMobile("bob", xB, "B", "y"); err != nil {
		t.Fatalf("append bob: %v", err)
	}

	alice, _ := LoadTrustedMobiles("alice")
	if len(alice) != 1 || alice[0].Name != "A" {
		t.Fatalf("alice sees %+v", alice)
	}
	bob, _ := LoadTrustedMobiles("bob")
	if len(bob) != 1 || bob[0].Name != "B" {
		t.Fatalf("bob sees %+v", bob)
	}

	// Removing from one scope must not affect the other.
	if err := RemoveTrustedMobile("alice", hex.EncodeToString(xA)); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	aliceAfter, _ := LoadTrustedMobiles("alice")
	if len(aliceAfter) != 0 {
		t.Fatalf("alice after remove: %+v", aliceAfter)
	}
	bobAfter, _ := LoadTrustedMobiles("bob")
	if len(bobAfter) != 1 {
		t.Fatalf("bob after alice-remove: %+v", bobAfter)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// compile-time check: ensure we're using the right sql package.
var _ *sql.DB
