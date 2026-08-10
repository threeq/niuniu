// Package sleepassertion prevents the host machine from sleeping while held.
// The package provides a cross-platform Acquire/Close interface backed by
// OS-native mechanisms on each supported platform.
package sleepassertion

// Assertion holds a sleep-prevention assertion. Call Close to release it.
// Callers must tolerate a nil Assertion (returned on unsupported platforms).
type Assertion interface {
	// Close releases the sleep-prevention assertion. It is idempotent.
	Close() error
}

// Acquire returns an Assertion that prevents the system from sleeping while it
// is held. The reason string is displayed in system sleep-inhibitor lists where
// the OS supports it. Returns (nil, nil) on platforms where the feature is not
// implemented; callers should tolerate a nil return without error.
func Acquire(reason string) (Assertion, error) {
	return acquire(reason)
}
