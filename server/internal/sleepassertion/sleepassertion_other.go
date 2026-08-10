//go:build !darwin && !linux && !windows

package sleepassertion

// acquire is a no-op on unsupported platforms. Returns (nil, nil) so callers
// receive a nil Assertion without an error and can proceed normally.
func acquire(_ string) (Assertion, error) {
	return nil, nil
}
