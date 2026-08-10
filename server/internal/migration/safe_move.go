package migration

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/niuniu-dev/niuniu/internal/fsmove"
)

// BuildShadow recursively copies src into shadow. shadow must not already
// exist or must be empty. This is Phase 1 of the two-phase migration
// (spec §7.4): originals are left untouched so any error can simply discard
// the shadow and abort.
func BuildShadow(src, shadow string) error {
	if info, err := os.Stat(shadow); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("shadow target %s exists and is not a directory", shadow)
		}
		entries, err := os.ReadDir(shadow)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("shadow target %s is not empty", shadow)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Tolerate dangling NTFS junctions / pnpm store entries whose
			// targets vanished. The shadow copy is best-effort: a path that
			// can't even be stat'd has no data we could move anyway.
			if errors.Is(err, fs.ErrNotExist) {
				slog.Warn("BuildShadow: skipping unreadable path", "path", path, "error", err)
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(shadow, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			return os.Symlink(target, dst)
		}
		return fsmove.CopyFile(path, dst, info.Mode().Perm())
	})
}

// AtomicSwap is Phase 2 of the two-phase migration:
//  1. Rename orig → quarantine (if orig exists)
//  2. Rename shadow → orig
//
// On step-2 failure, step 1 is reversed.
// Both renames use crossDeviceRename, which falls back to recursive copy+delete
// when the two paths are on different devices (EXDEV).
// The caller is responsible for asynchronously deleting the quarantine after
// the surrounding DB transaction commits.
func AtomicSwap(orig, shadow, quarantine string) error {
	if _, err := os.Stat(shadow); err != nil {
		return fmt.Errorf("shadow missing: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(orig); err == nil {
		if err := crossDeviceRename(orig, quarantine); err != nil {
			return fmt.Errorf("move orig→quarantine: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
		_ = crossDeviceRename(quarantine, orig)
		return err
	}
	if err := crossDeviceRename(shadow, orig); err != nil {
		_ = crossDeviceRename(quarantine, orig)
		return fmt.Errorf("move shadow→orig: %w", err)
	}
	return nil
}

// crossDeviceRename is a thin wrapper kept for migration call-site stability.
// The implementation lives in internal/fsmove so service code can use it too.
func crossDeviceRename(src, dst string) error { return fsmove.Rename(src, dst) }

// copyDir is a thin wrapper kept for migration call-site stability.
func copyDir(src, dst string) error { return fsmove.CopyDir(src, dst) }
