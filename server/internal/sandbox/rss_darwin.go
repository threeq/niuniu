//go:build darwin

package sandbox

import (
	"os/exec"
	"strconv"
	"strings"
)

// platformRSS reads the box's resident set size on macOS.
//
// Seatbelt enforces no memory ceiling and RLIMIT_AS cannot be used for a Go
// binary (the runtime reserves a large virtual arena and would abort on
// startup), so — exactly as on Linux — the supervisor enforces the ceiling by
// sampling RSS and KILLING the box when it exceeds it (see runner.go). Without
// this reader that watchdog is dead on macOS: sampleMemory would always see 0
// ("unknown"), the `used > 0 && used > memLimit` guard would never fire, and a
// memory-bomb function would be bounded only by the wall clock.
//
// macOS exposes no unprivileged sysctl that reliably returns live RSS —
// kinfo_proc's rssize is stale/zero on modern releases — and proc_pidinfo needs
// cgo/private headers, so this reads what `ps` itself reads: /bin/ps, by
// absolute path so a doctored PATH cannot substitute a binary. Resident size is
// reported in 1024-byte units. A zero means "unknown", never "zero bytes used".
func platformRSS(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || kb <= 0 {
		return 0
	}
	return kb << 10
}
