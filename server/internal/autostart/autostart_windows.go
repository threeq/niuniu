//go:build windows

package autostart

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// Status reports whether the Run value exists and points at our executable, and
// whether the minimized flag is set. Windows paths are case-insensitive, so the
// value is compared with strings.EqualFold.
func (m *Manager) Status() (enabled, minimized bool, err error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		// The Run key effectively always exists; treat any open failure as
		// "not enabled" rather than a hard error on a best-effort status read.
		return false, false, nil
	}
	defer k.Close()
	v, _, err := k.GetStringValue(windowsRunValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	en, min := classify(v, func(mm bool) string { return windowsRunValue(m.exePath, mm) }, strings.EqualFold)
	return en, min, nil
}

// Enable writes (or overwrites) the Run value with the current exe path.
func (m *Manager) Enable(minimized bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(windowsRunValueName, windowsRunValue(m.exePath, minimized))
}

// Disable removes the Run value. Missing value is treated as success.
func (m *Manager) Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	if err := k.DeleteValue(windowsRunValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
