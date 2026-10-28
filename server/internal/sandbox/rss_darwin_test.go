//go:build darwin

package sandbox

import (
	"os"
	"testing"
)

// The supervisor kills an over-limit box only if it can READ that box's memory.
// On macOS that read is what this asserts: this process has a resident set, and
// platformRSS must report it rather than 0 — a 0 would leave the memory
// watchdog permanently disarmed (`used > 0 && used > memLimit` never fires) and
// bound a memory-bomb function only by the wall clock.
func TestPlatformRSSReadsDarwinProcess(t *testing.T) {
	pid := os.Getpid()
	got := platformRSS(pid)
	if got <= 0 {
		t.Fatalf("platformRSS(%d) = %d; the macOS memory watchdog would never fire", pid, got)
	}
	// A sanity ceiling: no plausible test process holds many GiB resident.
	if got > 64<<30 {
		t.Fatalf("platformRSS returned an implausible %d bytes for pid %d", got, pid)
	}
}

// An invalid/absent pid must read as UNKNOWN (0), never as a bogus number — a
// wrong positive could kill a healthy box, and "0" is the documented sentinel.
func TestPlatformRSSUnknownForInvalidPid(t *testing.T) {
	if v := platformRSS(-1); v != 0 {
		t.Fatalf("platformRSS(-1) = %d, want 0 (unknown)", v)
	}
	if v := platformRSS(0); v != 0 {
		t.Fatalf("platformRSS(0) = %d, want 0 (unknown)", v)
	}
}
