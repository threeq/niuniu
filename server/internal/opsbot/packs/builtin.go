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
			version, err := strconv.Atoi(versionEntry.Name())
			if err != nil || version <= 0 {
				continue
			}
			if err := installBuiltinPack(fsys, r, ownerType, ownerID, packID, version); err != nil {
				return err
			}
		}
	}
	return nil
}

func installBuiltinPack(fsys fs.FS, r *Registry, ownerType string, ownerID int64, packID string, version int) error {
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return err
	}
	if _, err := os.Stat(root); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	packDir := path.Join("builtin", packID, strconv.Itoa(version))
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
