package packs

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func writePack(t *testing.T, r *Registry, id string, version int, status string) string {
	t.Helper()
	m := Manifest{
		ID:          id,
		Version:     version,
		Status:      status,
		DisplayName: id,
		Capabilities: []string{
			"publish",
		},
		Bindings: map[string]Binding{
			"publish": {Transport: "manual"},
		},
		ContentSchema: ContentSchema{
			Formats: map[string]ContentFormat{
				"article": {Fields: []string{"title"}, MaxChars: 64},
			},
		},
	}
	root, err := r.Save("user", 1, m, nil)
	if err != nil {
		t.Fatalf("save pack: %v", err)
	}
	return root
}

func TestRegistryRejectsPathTraversal(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if _, err := r.Root("user", 1, "../evil", 1); err == nil {
		t.Fatal("expected path traversal rejection")
	}
}

func TestRegistryPublishListAndExportImport(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	writePack(t, r, "demo", 1, "draft")
	m, err := r.Load("user", 1, "demo", 1)
	if err != nil || m.Status != "draft" {
		t.Fatalf("load draft: %v %+v", err, m)
	}
	if err := r.Publish("user", 1, "demo", 1); err != nil {
		t.Fatal(err)
	}
	latest, err := r.LatestPublished("user", 1, "demo")
	if err != nil || latest.Version != 1 {
		t.Fatalf("latest: %v %+v", err, latest)
	}
	zipPath := filepath.Join(t.TempDir(), "demo.zip")
	if err := r.Export("user", 1, "demo", 1, zipPath); err != nil {
		t.Fatal(err)
	}
	other := &Registry{DataDir: t.TempDir()}
	if err := other.Import("user", 2, zipPath); err != nil {
		t.Fatal(err)
	}
	got, err := other.Load("user", 2, "demo", 1)
	if err != nil || got.ID != "demo" {
		t.Fatalf("imported: %v %+v", err, got)
	}
	if got.Status != "published" {
		t.Fatalf("export/import lost status: %+v", got)
	}
}

func TestRegistryRootRejectsBadOwnerAndVersion(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	cases := []struct {
		ownerType string
		ownerID   int64
		packID    string
		version   int
	}{
		{"", 1, "demo", 1},
		{"team", 1, "demo", 1},
		{"user", 0, "demo", 1},
		{"user", 1, "Demo", 1},
		{"user", 1, "demo/sub", 1},
		{"user", 1, "demo", 0},
		{"user", 1, "demo", -1},
	}
	for _, c := range cases {
		if _, err := r.Root(c.ownerType, c.ownerID, c.packID, c.version); err == nil {
			t.Fatalf("Root(%q, %d, %q, %d) should fail", c.ownerType, c.ownerID, c.packID, c.version)
		}
	}
}

func TestRegistrySaveRejectsOverwriteAndInvalidManifest(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	root := writePack(t, r, "demo", 1, "draft")

	if _, err := r.Save("user", 1, Manifest{ID: "demo", Version: 1, Status: "draft"}, nil); err == nil {
		t.Fatal("expected version immutability rejection")
	}

	bad := Manifest{ID: "other", Version: 1, Status: "draft"}
	if _, err := r.Save("user", 1, bad, nil); err == nil {
		t.Fatal("expected invalid manifest rejection")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "other")); !os.IsNotExist(err) {
		t.Fatalf("invalid pack left files behind: %v", err)
	}
}

func TestRegistrySaveWritesFilesAndRejectsTraversal(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	m := Manifest{
		ID:          "browser_pack",
		Version:     1,
		Status:      "draft",
		DisplayName: "Browser Pack",
		Capabilities: []string{
			"publish",
		},
		Bindings: map[string]Binding{
			"publish": {Transport: "browser", Recipe: "browser/recipes/publish.json"},
		},
		ContentSchema: ContentSchema{
			Formats: map[string]ContentFormat{"note": {MaxChars: 1000}},
		},
	}
	recipe := []byte(`{"name":"publish","steps":[{"action":"goto","value":"https://example.com"}]}`)
	files := map[string][]byte{"browser/recipes/publish.json": recipe}

	root, err := r.Save("user", 1, m, files)
	if err != nil {
		t.Fatalf("save with files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "browser", "recipes", "publish.json")); err != nil {
		t.Fatalf("recipe not written: %v", err)
	}

	traversal := Manifest{
		ID:           "evil_pack",
		Version:      1,
		Status:       "draft",
		Capabilities: []string{"publish"},
		Bindings:     map[string]Binding{"publish": {Transport: "manual"}},
		ContentSchema: ContentSchema{
			Formats: map[string]ContentFormat{"article": {}},
		},
	}
	if _, err := r.Save("user", 1, traversal, map[string][]byte{"../escape.json": []byte("{}")}); err == nil {
		t.Fatal("expected traversal file name rejection")
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, "users", "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("file escaped pack root: %v", err)
	}
}

func TestRegistryListSkipsBrokenVersions(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	writePack(t, r, "demo", 1, "draft")
	writePack(t, r, "demo", 2, "published")

	broken, err := r.Root("user", 1, "demo", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, manifestFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := r.List("user", 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 usable versions, got %d: %+v", len(list), list)
	}
	if list[0].Version != 1 || list[1].Version != 2 {
		t.Fatalf("unexpected order: %+v", list)
	}
}

func TestRegistryListEmptyOwner(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	list, err := r.List("user", 42)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %+v", list)
	}
}

func TestRegistryLatestPublishedRequiresPublished(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	writePack(t, r, "demo", 1, "draft")
	if _, err := r.LatestPublished("user", 1, "demo"); err == nil {
		t.Fatal("expected error when no published version exists")
	}
	if err := r.Publish("user", 1, "demo", 1); err != nil {
		t.Fatal(err)
	}
	writePack(t, r, "demo", 2, "draft")
	latest, err := r.LatestPublished("user", 1, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != 1 {
		t.Fatalf("draft must not be latest published, got v%d", latest.Version)
	}
}

func TestRegistryImportRejectsEscapingEntries(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	zipPath := filepath.Join(t.TempDir(), "evil.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../evil.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := r.Import("user", 1, zipPath); err == nil {
		t.Fatal("expected escaping archive entry rejection")
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, "users", "evil.json")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped owner root: %v", err)
	}
}

func TestRegistryImportRequiresManifest(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	zipPath := filepath.Join(t.TempDir(), "empty.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := r.Import("user", 1, zipPath); err == nil {
		t.Fatal("expected missing manifest rejection")
	}
}

func TestInstallBuiltinsSkipsExisting(t *testing.T) {
	builtin := fstest.MapFS{
		"builtin/demo/1/manifest.json": {Data: []byte(`{
			"id": "demo",
			"version": 1,
			"status": "published",
			"display_name": "Demo",
			"capabilities": ["publish"],
			"bindings": {"publish": {"transport": "manual"}},
			"content_schema": {"formats": {"article": {"fields": ["title"], "max_chars": 64}}}
		}`)},
		"builtin/demo/1/prompts/title.md": {Data: []byte("write a title")},
	}

	r := &Registry{DataDir: t.TempDir()}
	if err := installBuiltins(builtin, r, "user", 1); err != nil {
		t.Fatalf("install: %v", err)
	}

	root, err := r.Root("user", 1, "demo", 1)
	if err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(root, "prompts", "title.md")
	if err := os.WriteFile(promptPath, []byte("user edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installBuiltins(builtin, r, "user", 1); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	got, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user edited" {
		t.Fatalf("install overwrote user edits: %q", got)
	}
}
