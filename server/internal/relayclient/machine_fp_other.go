//go:build !darwin && !linux && !windows

package relayclient

func platformFingerprint() string { return "" }
