//go:build linux

package autostart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// desktopPath returns $XDG_CONFIG_HOME/autostart/niuniu-desktop.desktop,
// falling back to ~/.config/autostart when XDG_CONFIG_HOME is unset.
func (m *Manager) desktopPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := m.home()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart", linuxDesktopName), nil
}

// Status reports whether the autostart .desktop entry exists and matches our
// exe, and whether the minimized flag is set.
func (m *Manager) Status() (enabled, minimized bool, err error) {
	p, err := m.desktopPath()
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
	en, min := classify(string(b), func(mm bool) string { return linuxDesktopEntry(m.exePath, mm) }, exactEq)
	return en, min, nil
}

// Enable writes (or overwrites) the autostart .desktop entry.
func (m *Manager) Enable(minimized bool) error {
	p, err := m.desktopPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(linuxDesktopEntry(m.exePath, minimized)), 0o644)
}

// Disable removes the autostart .desktop entry. Missing file is treated as success.
func (m *Manager) Disable() error {
	p, err := m.desktopPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
