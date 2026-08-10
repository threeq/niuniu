package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactManifestPath is the workspace-root-relative path of the deliverable
// manifest. The agent maintains it (deciding which files are user-facing
// products) and the 产物预览面板 reads it; this lets a user promote a file to a
// deliverable from the file detail dialog too.
const ArtifactManifestPath = ".niuniu/artifacts.json"

// ArtifactEntry is one registered deliverable in the manifest.
type ArtifactEntry struct {
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`
}

// artifactManifest is the canonical on-disk shape: {"artifacts":[...]}. Reads
// also accept a bare array (some manifests were authored that way); writes
// always emit this object form.
type artifactManifest struct {
	Artifacts []ArtifactEntry `json:"artifacts"`
}

// AddArtifactToManifest registers relPath as a user-facing deliverable in the
// workspace's .niuniu/artifacts.json, creating the file (and .niuniu dir) when
// absent. It is idempotent: if relPath is already registered its title is
// updated to a non-empty title and no duplicate is appended. Returns the full
// manifest after the change.
//
// Both the {"artifacts":[...]} object form and a bare [...] array form are
// accepted on read; the manifest is always written back in the canonical object
// form. A manifest that exists but is unparseable is treated as empty rather
// than failing, so a hand-corrupted file never blocks the user action.
func AddArtifactToManifest(workspaceDir, relPath, title string) ([]ArtifactEntry, error) {
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if relPath == "" {
		return nil, errors.New("artifact path is empty")
	}
	title = strings.TrimSpace(title)

	manifestPath := filepath.Join(workspaceDir, filepath.FromSlash(ArtifactManifestPath))
	entries := readArtifactManifest(manifestPath)

	found := false
	for i := range entries {
		if entries[i].Path == relPath {
			if title != "" {
				entries[i].Title = title
			}
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, ArtifactEntry{Path: relPath, Title: title})
	}

	if err := writeArtifactManifest(manifestPath, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// RemoveArtifactFromManifest unregisters relPath from the workspace's
// deliverable manifest. It is idempotent: removing a path that isn't registered
// (or when no manifest exists) is a no-op that neither errors nor creates the
// file. Returns the manifest after the change. The underlying file on disk is
// left untouched — this only affects deliverable membership, so a file can be
// removed from the 产物预览面板 even after it has been deleted.
func RemoveArtifactFromManifest(workspaceDir, relPath string) ([]ArtifactEntry, error) {
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if relPath == "" {
		return nil, errors.New("artifact path is empty")
	}

	manifestPath := filepath.Join(workspaceDir, filepath.FromSlash(ArtifactManifestPath))
	entries := readArtifactManifest(manifestPath)

	kept := make([]ArtifactEntry, 0, len(entries))
	for _, e := range entries {
		if e.Path != relPath {
			kept = append(kept, e)
		}
	}
	// No matching entry (or no manifest at all) — don't rewrite, and never
	// create a manifest just to record a removal.
	if len(kept) == len(entries) {
		return kept, nil
	}
	if err := writeArtifactManifest(manifestPath, kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// readArtifactManifest loads the manifest entries, tolerating a missing,
// empty, or unparseable file (all yield an empty list) so a corrupt manifest
// never blocks the operation — the caller rewrites it in canonical form.
func readArtifactManifest(manifestPath string) []ArtifactEntry {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	// Object form: {"artifacts":[...]}.
	var obj artifactManifest
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil && obj.Artifacts != nil {
		return sanitizeEntries(obj.Artifacts)
	}
	// Bare array form: [...].
	var arr []ArtifactEntry
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		return sanitizeEntries(arr)
	}
	return nil
}

// sanitizeEntries drops entries without a path and normalizes separators, so a
// later dedup compares like against like.
func sanitizeEntries(in []ArtifactEntry) []ArtifactEntry {
	out := make([]ArtifactEntry, 0, len(in))
	for _, e := range in {
		p := filepath.ToSlash(strings.TrimSpace(e.Path))
		if p == "" {
			continue
		}
		out = append(out, ArtifactEntry{Path: p, Title: strings.TrimSpace(e.Title)})
	}
	return out
}

// writeArtifactManifest writes the entries in canonical indented object form,
// creating the .niuniu directory if needed.
func writeArtifactManifest(manifestPath string, entries []ArtifactEntry) error {
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(artifactManifest{Artifacts: entries}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(manifestPath, data, 0o644)
}
