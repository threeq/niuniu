//go:build darwin

package autostart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// plistPath returns ~/Library/LaunchAgents/com.niuniu.personal.plist.
func (m *Manager) plistPath() (string, error) {
	home, err := m.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist"), nil
}

// Status reports whether the LaunchAgent plist exists and matches our exe, and
// whether the minimized flag is set.
func (m *Manager) Status() (enabled, minimized bool, err error) {
	p, err := m.plistPath()
	if err != nil {
		return false, false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	en, min := classify(string(b), func(mm bool) string { return darwinPlist(m.exePath, mm) }, exactEq)
	return en, min, nil
}

// Enable writes (or overwrites) the LaunchAgent plist.
func (m *Manager) Enable(minimized bool) error {
	p, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(darwinPlist(m.exePath, minimized)), 0o644)
}

// Disable removes the LaunchAgent plist. Missing file is treated as success.
func (m *Manager) Disable() error {
	p, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
