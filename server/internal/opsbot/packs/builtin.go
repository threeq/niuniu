package packs

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
)

// builtinFS holds the seed packs shipped with the server. They are copied into
// an owner's directory on first use only; a pack the user already has (same id
// and version) is never overwritten.
//
//go:embed all:builtin
var builtinFS embed.FS

// InstallBuiltins copies every embedded seed pack into the owner's pack
// directory, skipping any id/version that already exists.
func InstallBuiltins(r *Registry, ownerType string, ownerID int64) error {
	return installBuiltins(builtinFS, r, ownerType, ownerID)
}

func installBuiltins(fsys fs.FS, r *Registry, ownerType string, ownerID int64) error {
	packEntries, err := fs.ReadDir(fsys, "builtin")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, packEntry := range packEntries {
		if !packEntry.IsDir() {
			continue
		}
		packID := packEntry.Name()
		versionEntries, err := fs.ReadDir(fsys, path.Join("builtin", packID))
		if err != nil {
			return err
		}
		for _, versionEntry := range versionEntries {
			if !versionEntry.IsDir() {
				continue
			}
			version, err := parseVersionDir(versionEntry.Name())
			if err != nil || version <= 0 {
				continue
			}
			if err := installBuiltinPack(fsys, r, ownerType, ownerID, packID, versionEntry.Name(), version); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseVersionDir accepts both the numeric layout used by the registry ("1")
// and the documented seed layout ("v1").
func parseVersionDir(name string) (int, error) {
	return strconv.Atoi(strings.TrimPrefix(name, "v"))
}

// BuiltinPack returns the highest-versioned embedded seed pack for packID,
// together with a read-only filesystem rooted at that version directory so
// callers can read prompts and sidecar files without installing the pack.
func BuiltinPack(packID string) (Manifest, fs.FS, error) {
	base := path.Join("builtin", packID)
	entries, err := fs.ReadDir(builtinFS, base)
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, nil, fmt.Errorf("%w: builtin %s", ErrPackNotFound, packID)
	}
	if err != nil {
		return Manifest{}, nil, err
	}

	best, bestDir := 0, ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version, err := parseVersionDir(entry.Name())
		if err != nil || version <= 0 {
			continue
		}
		if version > best {
			best, bestDir = version, entry.Name()
		}
	}
	if best == 0 {
		return Manifest{}, nil, fmt.Errorf("%w: builtin %s", ErrPackNotFound, packID)
	}

	dir := path.Join(base, bestDir)
	data, err := fs.ReadFile(builtinFS, path.Join(dir, manifestFile))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("builtin pack %s v%d: %w", packID, best, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, nil, fmt.Errorf("builtin pack %s v%d manifest: %w", packID, best, err)
	}
	if m.ID != packID || m.Version != best {
		return Manifest{}, nil, fmt.Errorf("%w: builtin %s declares %s v%d", ErrPackNotFound, dir, m.ID, m.Version)
	}

	if len(m.ContentSchema.Formats) == 0 {
		if err := readSidecarFS(builtinFS, path.Join(dir, contentSchemaFile), &m.ContentSchema); err != nil {
			return Manifest{}, nil, err
		}
	}
	if len(m.Compliance.ForbiddenTerms) == 0 && len(m.Compliance.RequiredChecks) == 0 {
		if err := readSidecarFS(builtinFS, path.Join(dir, complianceFile), &m.Compliance); err != nil {
			return Manifest{}, nil, err
		}
	}
	if len(m.MetricsMap.Fields) == 0 {
		if err := readSidecarFS(builtinFS, path.Join(dir, metricsMapFile), &m.MetricsMap); err != nil {
			return Manifest{}, nil, err
		}
	}

	sub, err := fs.Sub(builtinFS, dir)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("builtin pack %s v%d: %w", packID, best, err)
	}
	return m, sub, nil
}

func readSidecarFS(fsys fs.FS, name string, target any) error {
	data, err := fs.ReadFile(fsys, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("pack sidecar %s: %w", name, err)
	}
	return nil
}

func installBuiltinPack(fsys fs.FS, r *Registry, ownerType string, ownerID int64, packID, versionDir string, version int) error {
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return err
	}
	if _, err := os.Stat(root); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	packDir := path.Join("builtin", packID, versionDir)
	data, err := fs.ReadFile(fsys, path.Join(packDir, manifestFile))
	if err != nil {
		return fmt.Errorf("builtin pack %s v%d: %w", packID, version, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("builtin pack %s v%d manifest: %w", packID, version, err)
	}
	if m.ID != packID || m.Version != version {
		return fmt.Errorf("builtin pack %s v%d manifest declares %s v%d", packID, version, m.ID, m.Version)
	}

	files := make(map[string][]byte)
	err = fs.WalkDir(fsys, packDir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(name, packDir+"/")
		if rel == manifestFile {
			return nil
		}
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		files[rel] = content
		return nil
	})
	if err != nil {
		return err
	}

	_, err = r.Save(ownerType, ownerID, m, files)
	return err
}
