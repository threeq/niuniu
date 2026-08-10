package relayclient

// machineFingerprintFn is the function used to obtain the machine fingerprint.
// It can be overridden in tests to inject a mock value.
var machineFingerprintFn = defaultMachineFingerprint

// defaultMachineFingerprint calls the platform-specific implementation.
// Each platform build tag file defines platformFingerprint().
func defaultMachineFingerprint() string {
	return platformFingerprint()
}

// MachineFingerprint returns a short stable machine identifier derived from
// OS-level hardware identifiers. Returns "" on failure — callers should treat
// an empty string as "don't enforce" (i.e. skip the mismatch check).
//
// The returned value is:
//   - macOS:   IOPlatformUUID from ioreg
//   - Linux:   /etc/machine-id (trimmed)
//   - Windows: HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid
//   - Other:   "" (not enforced)
func MachineFingerprint() string {
	return machineFingerprintFn()
}
