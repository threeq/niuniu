package relayclient

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestProtoVersionDowngradeDefense simulates the downgrade-attack check that
// Dial performs after receiving a HelloAck.
//
// The logic under test (inlined here to avoid touching the WS layer) is:
//   if lastProto > 0 && ack.NegotiatedVersion < lastProto → abort
//   else persist max(stored, negotiated)
func TestProtoVersionDowngradeDefense(t *testing.T) {
	// Use the mock keyring provided by the test package so we don't touch the real OS keychain.
	keyring.MockInit()

	// Start clean.
	if err := keyring.Delete(keyringService, keyringProtoAccount); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("cleanup: %v", err)
	}

	// Scenario 1: last_proto=2, ack=1 → should detect downgrade.
	if err := keyring.Set(keyringService, keyringProtoAccount, "2"); err != nil {
		t.Fatalf("set keyring: %v", err)
	}
	lastProto, _ := LoadLastProtocolVersion()
	ackVersion := uint16(1)
	if !(lastProto > 0 && ackVersion < lastProto) {
		t.Fatal("scenario 1: expected downgrade to be detected (last=2, ack=1)")
	}

	// Scenario 2: last_proto=1, ack=2 → should accept and save 2.
	if err := keyring.Set(keyringService, keyringProtoAccount, "1"); err != nil {
		t.Fatalf("set keyring: %v", err)
	}
	lastProto2, _ := LoadLastProtocolVersion()
	ackVersion2 := uint16(2)
	if lastProto2 > 0 && ackVersion2 < lastProto2 {
		t.Fatal("scenario 2: false positive downgrade (last=1, ack=2)")
	}
	if err := SaveLastProtocolVersion(ackVersion2); err != nil {
		t.Fatalf("SaveLastProtocolVersion: %v", err)
	}
	saved, _ := LoadLastProtocolVersion()
	if saved != 2 {
		t.Fatalf("scenario 2: expected saved=2, got %d", saved)
	}

	// Scenario 3: last_proto=0 (no stored value), ack=1 → should accept.
	if err := keyring.Delete(keyringService, keyringProtoAccount); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("delete keyring: %v", err)
	}
	lastProto3, _ := LoadLastProtocolVersion()
	ackVersion3 := uint16(1)
	if lastProto3 > 0 && ackVersion3 < lastProto3 {
		t.Fatal("scenario 3: should accept when no stored version exists")
	}
}

// TestProtoVersionSaveIsMonotone verifies that SaveLastProtocolVersion never
// decreases the stored value.
func TestProtoVersionSaveIsMonotone(t *testing.T) {
	keyring.MockInit()

	if err := keyring.Delete(keyringService, keyringProtoAccount); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("cleanup: %v", err)
	}

	if err := SaveLastProtocolVersion(3); err != nil {
		t.Fatalf("save 3: %v", err)
	}
	v, _ := LoadLastProtocolVersion()
	if v != 3 {
		t.Fatalf("expected 3, got %d", v)
	}

	// Attempt to "save" a lower value — should be a no-op.
	if err := SaveLastProtocolVersion(1); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	v2, _ := LoadLastProtocolVersion()
	if v2 != 3 {
		t.Fatalf("expected 3 after attempted downgrade save, got %d", v2)
	}

	// Save a higher value — should update.
	if err := SaveLastProtocolVersion(5); err != nil {
		t.Fatalf("save 5: %v", err)
	}
	v3, _ := LoadLastProtocolVersion()
	if v3 != 5 {
		t.Fatalf("expected 5, got %d", v3)
	}
}
