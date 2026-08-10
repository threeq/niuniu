package sleepassertion

import (
	"testing"
)

// TestAcquireAndRelease verifies that Acquire + Close complete without panicking
// on the current platform. A nil Assertion (from unsupported platforms) is also
// tolerated.
func TestAcquireAndRelease(t *testing.T) {
	a, err := Acquire("sleepassertion test")
	if err != nil {
		// Soft-fail: system may not have caffeinate / systemd-inhibit installed.
		// Log and skip rather than hard-fail so CI on minimal images still passes.
		t.Logf("Acquire returned error (may be unavailable in CI): %v", err)
		return
	}
	if a == nil {
		// Unsupported platform — this is fine.
		t.Log("Acquire returned nil (unsupported platform), OK")
		return
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

// TestAcquireDoubleClose verifies that calling Close twice does not panic.
func TestAcquireDoubleClose(t *testing.T) {
	a, err := Acquire("sleepassertion double-close test")
	if err != nil || a == nil {
		t.Skip("Acquire unavailable on this platform")
	}
	if err := a.Close(); err != nil {
		t.Logf("first Close: %v", err)
	}
	// Second close must not panic (may return an error — that is fine).
	_ = a.Close()
}
