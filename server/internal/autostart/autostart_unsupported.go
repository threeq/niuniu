//go:build !windows && !darwin && !linux

package autostart

import "errors"

// errUnsupported is returned on platforms without a login-item implementation.
var errUnsupported = errors.New("autostart not supported on this platform")

func (m *Manager) Status() (enabled, minimized bool, err error) { return false, false, nil }
func (m *Manager) Enable(minimized bool) error                  { return errUnsupported }
func (m *Manager) Disable() error                               { return errUnsupported }
