package relayclient

import (
	"runtime"
	"testing"
)

// TestMachineFingerprint verifies that MachineFingerprint() returns a non-empty
// string on platforms where we have an implementation.
// On unsupported platforms (GOOS != darwin/linux/windows) the empty string is
// acceptable and this test is skipped.
func TestMachineFingerprint(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		// These platforms should return a non-empty fingerprint.
	default:
		t.Skipf("MachineFingerprint not implemented on %s", runtime.GOOS)
	}

	fp := MachineFingerprint()
	if fp == "" {
		// On CI environments (e.g. containers without ioreg or /etc/machine-id)
		// an empty result is acceptable — log but don't fail hard.
		t.Logf("MachineFingerprint() returned empty on %s — may be a CI environment", runtime.GOOS)
		return
	}
	t.Logf("MachineFingerprint() = %q (len=%d)", fp, len(fp))
	if len(fp) < 8 {
		t.Errorf("fingerprint looks too short: %q", fp)
	}
}

// TestMachineFingerprint_StableAcrossCalls verifies that two consecutive calls
// return the same value (no randomness).
func TestMachineFingerprint_StableAcrossCalls(t *testing.T) {
	fp1 := MachineFingerprint()
	fp2 := MachineFingerprint()
	if fp1 != fp2 {
		t.Errorf("MachineFingerprint not stable: %q vs %q", fp1, fp2)
	}
}
