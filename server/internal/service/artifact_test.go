package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readEntries loads the on-disk manifest for assertions.
func readEntries(t *testing.T, dir string) []ArtifactEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ArtifactManifestPath)))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m artifactManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v (raw=%s)", err, raw)
	}
	return m.Artifacts
}

func TestAddArtifactToManifest_CreatesManifest(t *testing.T) {
	dir := t.TempDir()

	entries, err := AddArtifactToManifest(dir, "report.docx", "季度报告")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "report.docx" || entries[0].Title != "季度报告" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	// The .niuniu dir + canonical object form must exist on disk.
	got := readEntries(t, dir)
	if len(got) != 1 || got[0].Path != "report.docx" {
		t.Fatalf("on-disk mismatch: %+v", got)
	}
}

func TestAddArtifactToManifest_Idempotent(t *testing.T) {
	dir := t.TempDir()

	if _, err := AddArtifactToManifest(dir, "a/b.pptx", "Deck"); err != nil {
		t.Fatalf("add 1: %v", err)
	}
	// Re-submitting the same path updates the title, not appends a duplicate.
	entries, err := AddArtifactToManifest(dir, "a/b.pptx", "Deck v2")
	if err != nil {
		t.Fatalf("add 2: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected dedup to 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Title != "Deck v2" {
		t.Fatalf("expected title updated, got %q", entries[0].Title)
	}
}

func TestAddArtifactToManifest_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	if _, err := AddArtifactToManifest(dir, "one.md", "One"); err != nil {
		t.Fatalf("add one: %v", err)
	}
	entries, err := AddArtifactToManifest(dir, "two.md", "Two")
	if err != nil {
		t.Fatalf("add two: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
}

func TestAddArtifactToManifest_ReadsBareArrayForm(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, filepath.FromSlash(ArtifactManifestPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A manifest authored as a bare array must be honored on read.
	if err := os.WriteFile(manifestPath, []byte(`[{"path":"legacy.pdf","title":"Legacy"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := AddArtifactToManifest(dir, "new.pdf", "New")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected legacy + new = 2 entries, got %d: %+v", len(entries), entries)
	}
	// And it's rewritten in canonical object form.
	if got := readEntries(t, dir); len(got) != 2 {
		t.Fatalf("on-disk canonical form mismatch: %+v", got)
	}
}

func TestAddArtifactToManifest_CorruptManifestTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, filepath.FromSlash(ArtifactManifestPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := AddArtifactToManifest(dir, "fresh.docx", "Fresh")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "fresh.docx" {
		t.Fatalf("expected corrupt manifest reset to single entry, got %+v", entries)
	}
}

func TestAddArtifactToManifest_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := AddArtifactToManifest(dir, "   ", "x"); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestRemoveArtifactFromManifest_RemovesEntry(t *testing.T) {
	dir := t.TempDir()
	if _, err := AddArtifactToManifest(dir, "a.md", "A"); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if _, err := AddArtifactToManifest(dir, "b.md", "B"); err != nil {
		t.Fatalf("add b: %v", err)
	}

	entries, err := RemoveArtifactFromManifest(dir, "a.md")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "b.md" {
		t.Fatalf("expected only b.md left, got %+v", entries)
	}
	if got := readEntries(t, dir); len(got) != 1 || got[0].Path != "b.md" {
		t.Fatalf("on-disk mismatch: %+v", got)
	}
}

func TestRemoveArtifactFromManifest_MissingPathIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if _, err := AddArtifactToManifest(dir, "keep.md", "Keep"); err != nil {
		t.Fatalf("add: %v", err)
	}
	entries, err := RemoveArtifactFromManifest(dir, "nope.md")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "keep.md" {
		t.Fatalf("expected keep.md untouched, got %+v", entries)
	}
}

func TestRemoveArtifactFromManifest_NoManifestDoesNotCreateFile(t *testing.T) {
	dir := t.TempDir()
	entries, err := RemoveArtifactFromManifest(dir, "ghost.md")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty result, got %+v", entries)
	}
	// A removal against a nonexistent manifest must not create one.
	if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(ArtifactManifestPath))); !os.IsNotExist(statErr) {
		t.Fatalf("expected no manifest file to be created, stat err = %v", statErr)
	}
}

func TestRemoveArtifactFromManifest_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := RemoveArtifactFromManifest(dir, "  "); err == nil {
		t.Fatal("expected error for empty path")
	}
}
