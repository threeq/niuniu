//go:build windows

package sleepassertion

import "golang.org/x/sys/windows"

// SetThreadExecutionState flags.
const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
)

var (
	kernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

type winAssertion struct {
	prev uint32
}

func acquire(_ string) (Assertion, error) {
	prev, _, _ := procSetThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired))
	return &winAssertion{prev: uint32(prev)}, nil
}

// Close resets the execution state back to ES_CONTINUOUS only, which allows
// the system to sleep again.
func (w *winAssertion) Close() error {
	procSetThreadExecutionState.Call(uintptr(esContinuous))
	return nil
}
