//go:build !linux && !darwin

package sandbox

// On Windows the box's hardening is entirely parent-applied (AppContainer
// security capabilities + job object limits), so there is nothing for the box
// to do to itself. On a platform with no fence at all this is never reached:
// platformFence() refuses to spawn, and the host reports the fence as
// unavailable.
func ApplySelfFence(_ int) error { return nil }
