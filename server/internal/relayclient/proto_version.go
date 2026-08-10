package relayclient

import (
	"errors"
	"strconv"

	"github.com/zalando/go-keyring"
)

const keyringProtoAccount = "last_proto"

// LoadLastProtocolVersion returns the highest protocol version this machine has
// previously negotiated with the relay, or 0 if unknown. If keychain access
// fails the error is suppressed and 0 is returned — callers must not hard-fail
// tunnel open on a missing or inaccessible keychain entry.
func LoadLastProtocolVersion() (uint16, error) {
	raw, err := keyring.Get(keyringService, keyringProtoAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return 0, nil
		}
		// Keychain permission error or unavailable — non-fatal.
		return 0, nil
	}
	v, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return 0, nil
	}
	return uint16(v), nil
}

// SaveLastProtocolVersion writes back max(stored, v) as the new
// last_negotiated_version. If keychain access fails the error is suppressed —
// callers must not hard-fail tunnel open on a keychain write error.
func SaveLastProtocolVersion(v uint16) error {
	stored, _ := LoadLastProtocolVersion()
	if v <= stored {
		return nil
	}
	return keyring.Set(keyringService, keyringProtoAccount, strconv.FormatUint(uint64(v), 10))
}
