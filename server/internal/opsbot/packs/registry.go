package packs

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// manifestFile is the one file that defines a pack version. A version directory
// is otherwise opaque: whatever the generator wrote (schemas, prompts, recipes,
// fixtures) is copied verbatim.
const manifestFile = "manifest.json"

// Registry stores packs on the filesystem. The filesystem is the only source of
// truth: there is no database index, and a version is immutable once written.
// Layout: <DataDir>/<users|orgs>/<ownerID>/ops/packs/<packID>/<version>/.
type Registry struct {
	DataDir string
}

var (
	ErrInvalidOwner   = errors.New("pack registry: invalid owner")
	ErrInvalidPackID  = errors.New("pack registry: invalid pack id")
	ErrInvalidVersion = errors.New("pack registry: invalid version")
	ErrVersionExists  = errors.New("pack registry: version already exists")
	ErrPackNotFound   = errors.New("pack registry: pack not found")
)

// Root resolves the directory of one pack version and rejects any input that
// could escape the owner's packs directory.
func (r *Registry) Root(ownerType string, ownerID int64, packID string, version int) (string, error) {
	ownerRoot, err := r.ownerRoot(ownerType, ownerID)
	if err != nil {
		return "", err
	}
	if !idPattern.MatchString(packID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPackID, packID)
	}
	if version <= 0 {
		return "", fmt.Errorf("%w: %d", ErrInvalidVersion, version)
	}

	root := filepath.Clean(filepath.Join(ownerRoot, "ops", "packs", packID, strconv.Itoa(version)))
	if err := ensureInside(ownerRoot, root); err != nil {
		return "", err
	}
	return root, nil
}

func (r *Registry) ownerRoot(ownerType string, ownerID int64) (string, error) {
	dir := ""
	switch ownerType {
	case "user":
		dir = "users"
	case "org":
		dir = "orgs"
	default:
		return "", fmt.Errorf("%w: type %q", ErrInvalidOwner, ownerType)
	}
	if ownerID <= 0 {
		return "", fmt.Errorf("%w: id %d", ErrInvalidOwner, ownerID)
	}
	return filepath.Join(r.DataDir, dir, strconv.FormatInt(ownerID, 10)), nil
}

func (r *Registry) packsDir(ownerType string, ownerID int64) (string, error) {
	ownerRoot, err := r.ownerRoot(ownerType, ownerID)
	if err != nil {
		return "", err
	}
	return filepath.Join(ownerRoot, "ops", "packs"), nil
}

// Save writes a new immutable pack version. files maps pack-relative paths to
// their contents (manifest.json is generated from m and may not appear there).
// The version is staged in a temporary directory, validated as a whole, and
// then renamed into place, so a failed Save leaves nothing behind.
func (r *Registry) Save(ownerType string, ownerID int64, m Manifest, files map[string][]byte) (string, error) {
	root, err := r.Root(ownerType, ownerID, m.ID, m.Version)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(root); err == nil {
		return "", fmt.Errorf("%w: %s v%d", ErrVersionExists, m.ID, m.Version)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, "."+m.ID+"-staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	if err := writePackDir(tmp, m, files); err != nil {
		return "", err
	}
	if err := Validate(tmp, m); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, root); err != nil {
		return "", err
	}
	return root, nil
}

func writePackDir(dir string, m Manifest, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if strings.TrimSpace(name) == "" || filepath.Clean(name) == "." {
			return fmt.Errorf("pack %s has an empty file name", m.ID)
		}
		if name == manifestFile {
			return fmt.Errorf("pack %s files may not include %s", m.ID, manifestFile)
		}
		target, err := resolveInside(dir, name)
		if err != nil {
			return fmt.Errorf("pack %s file %q: %w", m.ID, name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, files[name], 0o644); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestFile), data, 0o644)
}

// Load reads and validates one pack version.
func (r *Registry) Load(ownerType string, ownerID int64, packID string, version int) (Manifest, error) {
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return Manifest{}, err
	}
	m, err := loadManifest(root)
	if err != nil {
		return Manifest{}, err
	}
	if m.ID != packID || m.Version != version {
		return Manifest{}, fmt.Errorf("%w: %s declares %s v%d", ErrPackNotFound, root, m.ID, m.Version)
	}
	if err := Validate(root, m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func loadManifest(root string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(root, manifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, fmt.Errorf("%w: %s", ErrPackNotFound, root)
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("pack manifest %s: %w", root, err)
	}
	return m, nil
}

// List returns every usable version under the owner, sorted by id then version.
// Damaged or invalid versions are skipped rather than failing the whole list.
func (r *Registry) List(ownerType string, ownerID int64) ([]Manifest, error) {
	packsDir, err := r.packsDir(ownerType, ownerID)
	if err != nil {
		return nil, err
	}

	packEntries, err := os.ReadDir(packsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Manifest
	for _, packEntry := range packEntries {
		if !packEntry.IsDir() {
			continue
		}
		packDir := filepath.Join(packsDir, packEntry.Name())
		versionEntries, err := os.ReadDir(packDir)
		if err != nil {
			continue
		}
		for _, versionEntry := range versionEntries {
			if !versionEntry.IsDir() {
				continue
			}
			root := filepath.Join(packDir, versionEntry.Name())
			m, ok := usableManifest(root, packEntry.Name(), versionEntry.Name())
			if !ok {
				continue
			}
			out = append(out, m)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

func usableManifest(root, packID, versionName string) (Manifest, bool) {
	m, err := loadManifest(root)
	if err != nil {
		return Manifest{}, false
	}
	if m.ID != packID || strconv.Itoa(m.Version) != versionName {
		return Manifest{}, false
	}
	if err := Validate(root, m); err != nil {
		return Manifest{}, false
	}
	return m, true
}

// LatestPublished returns the highest published version of one pack.
func (r *Registry) LatestPublished(ownerType string, ownerID int64, packID string) (Manifest, error) {
	list, err := r.List(ownerType, ownerID)
	if err != nil {
		return Manifest{}, err
	}

	var latest Manifest
	found := false
	for _, m := range list {
		if m.ID != packID || m.Status != "published" {
			continue
		}
		if !found || m.Version > latest.Version {
			latest = m
			found = true
		}
	}
	if !found {
		return Manifest{}, fmt.Errorf("%w: %s has no published version", ErrPackNotFound, packID)
	}
	return latest, nil
}

// Publish flips a version to published with an atomic manifest rewrite. The
// rest of the version directory is never touched, so published content cannot
// drift. Publishing an already-published version is a no-op.
func (r *Registry) Publish(ownerType string, ownerID int64, packID string, version int) error {
	m, err := r.Load(ownerType, ownerID, packID, version)
	if err != nil {
		return err
	}

	switch m.Status {
	case "published":
		return nil
	case "deprecated":
		return fmt.Errorf("pack %s v%d is deprecated and cannot be published", packID, version)
	}

	m.Status = "published"
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(root, manifestFile), data)
}

func atomicWriteFile(name string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, name)
}

// Export writes one pack version to a zip archive; entries are relative to the
// version directory.
func (r *Registry) Export(ownerType string, ownerID int64, packID string, version int, zipPath string) error {
	if _, err := r.Load(ownerType, ownerID, packID, version); err != nil {
		return err
	}
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return err
	}

	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)

	walkErr := filepath.WalkDir(root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		zw.Close()
		f.Close()
		return walkErr
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Import restores a pack version exported from another owner. The archive root
// may be the version directory itself or contain it; the manifest decides.
// Every entry is checked so a malicious archive cannot write outside the pack.
func (r *Registry) Import(ownerType string, ownerID int64, zipPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open pack archive: %w", err)
	}
	defer zr.Close()

	raw := make(map[string][]byte, len(zr.File))
	for _, zf := range zr.File {
		name, ok := cleanArchiveName(zf.Name)
		if !ok {
			return fmt.Errorf("pack archive entry %q escapes the pack directory", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		if !zf.Mode().IsRegular() {
			return fmt.Errorf("pack archive entry %q is not a regular file", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		raw[name] = data
	}

	// The manifest at the shallowest depth is the pack root; deeper ones are
	// ordinary payload files.
	manifestPath := ""
	manifestDepth := 0
	for name := range raw {
		if path.Base(name) != manifestFile {
			continue
		}
		depth := strings.Count(name, "/")
		if manifestPath == "" || depth < manifestDepth {
			manifestPath = name
			manifestDepth = depth
			continue
		}
		if depth == manifestDepth {
			return fmt.Errorf("pack archive contains more than one %s", manifestFile)
		}
	}
	if manifestPath == "" {
		return fmt.Errorf("pack archive has no %s", manifestFile)
	}

	var m Manifest
	if err := json.Unmarshal(raw[manifestPath], &m); err != nil {
		return fmt.Errorf("pack archive manifest: %w", err)
	}

	prefix := path.Dir(manifestPath)
	files := make(map[string][]byte, len(raw))
	for name, data := range raw {
		if name == manifestPath {
			continue
		}
		rel := name
		if prefix != "." {
			if !strings.HasPrefix(name, prefix+"/") {
				return fmt.Errorf("pack archive entry %q is outside %q", name, prefix)
			}
			rel = strings.TrimPrefix(name, prefix+"/")
		}
		files[filepath.FromSlash(rel)] = data
	}

	_, err = r.Save(ownerType, ownerID, m, files)
	return err
}

// cleanArchiveName accepts only relative, forward-slash archive names that
// normalise inside the archive root.
func cleanArchiveName(name string) (string, bool) {
	if name == "" || strings.Contains(name, `\`) || strings.Contains(name, ":") {
		return "", false
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", false
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// ensureInside reports an error unless path is root or nested under it.
func ensureInside(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path %q escapes %q", path, root)
	}
	return nil
}
