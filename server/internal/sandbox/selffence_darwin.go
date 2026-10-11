//go:build darwin

package sandbox

import (
	"fmt"
	"syscall"
)

// The box's own half of the macOS fence. Seatbelt (applied by the parent
// through sandbox-exec) already denies the filesystem and the network; what is
// left for the box is the resource ceiling, which Seatbelt does not express.
//
// RLIMIT_AS is deliberately not used: the Go runtime reserves a large virtual
// arena, so an address-space cap sized for the function's real memory need
// would abort the box during startup. Memory is enforced by the supervisor's
// RSS watchdog; CPU seconds are a genuine kernel-enforced cap.
func ApplySelfFence(_ int) error {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CPU, &lim); err != nil {
		return err
	}
	lim.Cur = 30
	lim.Max = 30
	if err := syscall.Setrlimit(syscall.RLIMIT_CPU, &lim); err != nil {
		return fmt.Errorf("setrlimit(RLIMIT_CPU): %w", err)
	}
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
	return nil
}
