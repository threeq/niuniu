//go:build linux

package sandbox

import (
	"os"
	"strconv"
	"strings"
)

// platformRSS reads the box's resident set from /proc. Linux has no unprivileged
// per-process memory cap that a Go binary tolerates (RLIMIT_AS breaks the
// runtime's address-space reservation), so the supervisor enforces the ceiling
// by sampling and killing.
func platformRSS(pid int) int64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb << 10
	}
	return 0
}
