package service

import (
	"context"
	"path/filepath"
	"testing"
)

// TestResolveSourceRoot_LocalHostedGate verifies the hosted-edition (local
// sources disabled) gate: an arbitrary server path is refused, while a path
// inside the owner's datasets dir (where uploads land) is accepted.
func TestResolveSourceRoot_LocalHostedGate(t *testing.T) {
	svc, owner := newKBTest(t)
	ctx := context.Background()

	// kbAllowLocalSources defaults false (hosted). Do NOT call allowLocal here.

	// 1) An arbitrary absolute path outside datasets is refused up front.
	outside := t.TempDir() // some unrelated server dir
	writeKBFile(t, outside, "secret.txt", "top secret")
	kbBad, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "evil", SourceKind: KBSourceLocal, SourceAddr: outside,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if _, err := svc.resolveSourceRoot(owner, kbBad); err == nil {
		t.Fatalf("resolveSourceRoot allowed an out-of-scope local path %q on hosted edition", outside)
	}

	// 2) A path inside the owner's datasets dir (the upload target) is allowed.
	inside := owner.DatasetsPath(svc.dataDir, kbBad.ID)
	writeKBFile(t, inside, "doc.md", "# ok")
	kbGood, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "uploaded", SourceKind: KBSourceLocal, SourceAddr: inside,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	root, err := svc.resolveSourceRoot(owner, kbGood)
	if err != nil {
		t.Fatalf("resolveSourceRoot refused an in-datasets local path %q: %v", inside, err)
	}
	if filepath.Clean(root) != filepath.Clean(inside) {
		t.Errorf("root=%q, want %q", root, inside)
	}
}

// TestResolveSourceRoot_LocalPersonalAllowed verifies that on the personal
// edition (local sources enabled) an arbitrary path is read as designed.
func TestResolveSourceRoot_LocalPersonalAllowed(t *testing.T) {
	allowLocal(t) // personal edition
	svc, owner := newKBTest(t)
	ctx := context.Background()

	dir := t.TempDir()
	writeKBFile(t, dir, "notes.md", "# notes")
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "local", SourceKind: KBSourceLocal, SourceAddr: dir,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	root, err := svc.resolveSourceRoot(owner, kb)
	if err != nil {
		t.Fatalf("resolveSourceRoot on personal edition refused %q: %v", dir, err)
	}
	if filepath.Clean(root) != filepath.Clean(dir) {
		t.Errorf("root=%q, want %q", root, dir)
	}
}

// TestEnsureLocalSourceAllowed_Traversal guards against a "../" escape out of
// the datasets dir on the hosted edition.
func TestEnsureLocalSourceAllowed_Traversal(t *testing.T) {
	svc, owner := newKBTest(t)
	escape := filepath.Join(owner.DatasetsDir(svc.dataDir), "..", "..", "etc")
	if err := svc.EnsureLocalSourceAllowed(owner, escape); err == nil {
		t.Fatalf("EnsureLocalSourceAllowed allowed a traversal escape %q", escape)
	}
}
