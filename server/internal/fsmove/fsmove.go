// Package fsmove implements directory moves that survive cross-filesystem
// boundaries. The standard library's os.Rename returns EXDEV when src and dst
// live on different filesystems (typical for Docker bind-mount volumes layered
// on the container's overlay). Rename here transparently falls back to a
// recursive copy + remove in that case.
//
// This used to live in internal/migration/safe_move.go but service code needs
// it too (RepositoryService.finishCreate moves auto-init'd repos from the
// container overlay onto the persistent /data/.niuniu volume). Migration
// already imports service, so service cannot import migration — hence this
// dedicated leaf package.
package fsmove

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// Rename moves src to dst. Tries os.Rename first; on EXDEV falls back to a
// recursive copy of src into dst followed by os.RemoveAll(src).
func Rename(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !errors.Is(linkErr.Err, syscall.EXDEV) {
		return err
	}
	if err2 := CopyDir(src, dst); err2 != nil {
		// Best-effort cleanup of partial dst so the caller can retry from a
		// clean slate.
		_ = os.RemoveAll(dst)
		return fmt.Errorf("fsmove.Rename copy %s → %s: %w", src, dst, err2)
	}
	if err2 := os.RemoveAll(src); err2 != nil {
		return fmt.Errorf("fsmove.Rename remove src %s after copy: %w", src, err2)
	}
	return nil
}

// CopyDir recursively copies the directory tree rooted at src into dst.
// dst must not already exist or must be an empty directory.
func CopyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				slog.Warn("fsmove.CopyDir: skipping unreadable path", "path", path, "error", err)
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		return CopyFile(path, target, info.Mode().Perm())
	})
}

// CopyFile copies a single file from src to dst with the given mode. Creates
// any missing parent directories. Returns nil if src vanishes between caller
// stat and our open (typical for dangling Windows junctions).
func CopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		// File vanished between filepath.Walk's lstat and our open
		// (typical for dangling pnpm junctions on Windows). Drop it.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
