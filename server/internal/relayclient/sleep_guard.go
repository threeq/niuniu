package relayclient

import (
	"sync"

	"github.com/niuniu-dev/niuniu/internal/sleepassertion"
)

var (
	sleepMu        sync.Mutex
	activeSessions int
	sleepHold      sleepassertion.Assertion
)

// NotifySessionOpen increments the active session count and acquires a
// sleep-prevention assertion on the first session. Safe for concurrent use.
func NotifySessionOpen() {
	sleepMu.Lock()
	defer sleepMu.Unlock()
	activeSessions++
	if activeSessions == 1 && sleepHold == nil {
		sleepHold, _ = sleepassertion.Acquire("niuniu mobile session")
	}
}

// NotifySessionClose decrements the active session count and releases the
// sleep-prevention assertion when the last session closes. Safe for concurrent use.
func NotifySessionClose() {
	sleepMu.Lock()
	defer sleepMu.Unlock()
	activeSessions--
	if activeSessions <= 0 && sleepHold != nil {
		_ = sleepHold.Close()
		sleepHold = nil
		activeSessions = 0
	}
}
