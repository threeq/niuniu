// Package relayclient contains the desktop side of the central relay tunnel.
package relayclient

import (
	"encoding/base64"
	"errors"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
	"github.com/zalando/go-keyring"
)

const (
	keyringService          = "niuniu-desktop"
	keyringIdentityPrefix   = "relay-identity:"
	keyringFingerprintPref  = "machine-fp:"
)

// DefaultIdentityScope is used when auth is disabled (single-user / personal edition).
const DefaultIdentityScope = "default"

// ErrIdentityNotFound is returned by LoadIdentity when no entry exists.
var ErrIdentityNotFound = errors.New("relayclient: no identity in keychain")

// ErrMachineFingerprintMismatch is returned by LoadIdentity when the stored
// machine fingerprint does not match the current machine's fingerprint.
// This may indicate the keychain entry was copied from another machine.
var ErrMachineFingerprintMismatch = errors.New("relayclient: machine fingerprint mismatch")

func identityKey(scope string) string {
	if scope == "" {
		scope = DefaultIdentityScope
	}
	return keyringIdentityPrefix + scope
}
func fingerprintKey(scope string) string {
	if scope == "" {
		scope = DefaultIdentityScope
	}
	return keyringFingerprintPref + scope
}

// LoadIdentity reads the persisted identity for the scope from the OS keychain.
// Returns ErrIdentityNotFound if the scope has no entry.  The stored machine
// fingerprint (also per-scope) is compared against the current machine's; a
// non-empty mismatch yields ErrMachineFingerprintMismatch.
func LoadIdentity(scope string) (*pairingcrypto.Identity, error) {
	raw, err := keyring.Get(keyringService, identityKey(scope))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrIdentityNotFound
		}
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	id, err := pairingcrypto.UnmarshalIdentity(blob)
	if err != nil {
		return nil, err
	}
	storedFP, fpErr := keyring.Get(keyringService, fingerprintKey(scope))
	if fpErr == nil && storedFP != "" {
		currentFP := MachineFingerprint()
		if currentFP != "" && currentFP != storedFP {
			return nil, ErrMachineFingerprintMismatch
		}
	}
	return id, nil
}

// SaveIdentity writes the identity + machine fingerprint for the scope.
func SaveIdentity(scope string, id *pairingcrypto.Identity) error {
	raw := base64.StdEncoding.EncodeToString(id.MarshalBinary())
	if err := keyring.Set(keyringService, identityKey(scope), raw); err != nil {
		return err
	}
	if fp := MachineFingerprint(); fp != "" {
		_ = keyring.Set(keyringService, fingerprintKey(scope), fp)
	}
	return nil
}

// DeleteIdentity removes the identity + fingerprint for the scope.  Idempotent.
func DeleteIdentity(scope string) error {
	err := keyring.Delete(keyringService, identityKey(scope))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	_ = keyring.Delete(keyringService, fingerprintKey(scope))
	return nil
}

// LoadOrCreateIdentity reads the keychain entry for the scope or generates
// + saves a new one.  The second return value is true when a fresh identity
// was generated.
func LoadOrCreateIdentity(scope string) (*pairingcrypto.Identity, bool, error) {
	id, err := LoadIdentity(scope)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, ErrIdentityNotFound) {
		return nil, false, err
	}
	fresh, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		return nil, false, err
	}
	if err := SaveIdentity(scope, fresh); err != nil {
		return nil, false, err
	}
	return fresh, true, nil
}
