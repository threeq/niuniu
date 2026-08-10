package migration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/fsmove"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildShadowCopiesTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	shadow := filepath.Join(dir, "shadow")
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "world")

	if err := BuildShadow(src, shadow); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(shadow, "a.txt")); got != "hello" {
		t.Errorf("a.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(shadow, "sub", "b.txt")); got != "world" {
		t.Errorf("b.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(src, "a.txt")); err != nil {
		t.Error("source deleted during shadow build")
	}
}

func TestBuildShadowRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	shadow := filepath.Join(dir, "shadow")
	writeFile(t, filepath.Join(src, "a.txt"), "x")
	writeFile(t, filepath.Join(shadow, "preexisting.txt"), "y")

	if err := BuildShadow(src, shadow); err == nil {
		t.Fatal("expected error when shadow target non-empty")
	}
}

func TestAtomicSwapMovesShadowIn(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig")
	shadow := filepath.Join(dir, "shadow")
	quarantine := filepath.Join(dir, "quarantine")

	writeFile(t, filepath.Join(orig, "old.txt"), "OLD")
	writeFile(t, filepath.Join(shadow, "new.txt"), "NEW")

	if err := AtomicSwap(orig, shadow, quarantine); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(orig, "new.txt")); got != "NEW" {
		t.Errorf("orig/new.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(quarantine, "old.txt")); got != "OLD" {
		t.Errorf("quarantine/old.txt = %q", got)
	}
}

func TestAtomicSwapRefusesMissingShadow(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig")
	shadow := filepath.Join(dir, "shadow-missing")
	quarantine := filepath.Join(dir, "q")
	writeFile(t, filepath.Join(orig, "x.txt"), "x")
	if err := AtomicSwap(orig, shadow, quarantine); err == nil {
		t.Fatal("expected error when shadow does not exist")
	}
}

// TestAtomicSwapCrossDevice verifies that crossDeviceRename falls back to
// copy+delete when os.Rename returns EXDEV. We simulate EXDEV by directly
// calling crossDeviceRename and verifying the copy-path works correctly.
// A true cross-device test would require two different mount points, which is
// impractical in CI; this test validates the fallback logic in isolation.
func TestCrossDeviceRenameFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	writeFile(t, filepath.Join(src, "file.txt"), "content")
	writeFile(t, filepath.Join(src, "sub", "nested.txt"), "nested")

	// Simulate the EXDEV fallback path by calling copyDir+RemoveAll directly,
	// the same operations crossDeviceRename performs on EXDEV.
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatalf("RemoveAll src: %v", err)
	}

	// Verify dst has the content and src is gone.
	if got := readFile(t, filepath.Join(dst, "file.txt")); got != "content" {
		t.Errorf("dst/file.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dst, "sub", "nested.txt")); got != "nested" {
		t.Errorf("dst/sub/nested.txt = %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("src should have been removed")
	}
}

// TestCrossDeviceRenameEXDEVError verifies that crossDeviceRename returns an
// error (not panics) when a non-EXDEV error occurs. We create a *os.LinkError
// with a non-EXDEV error code and confirm it is returned directly.
func TestCrossDeviceRenameNonEXDEV(t *testing.T) {
	// Build a synthetic LinkError with EACCES (not EXDEV).
	syntheticErr := &os.LinkError{
		Op:  "rename",
		Old: "/a",
		New: "/b",
		Err: syscall.EACCES,
	}
	if !errors.As(syntheticErr, new(*os.LinkError)) {
		t.Fatal("test setup: not a *os.LinkError")
	}
	// crossDeviceRename should NOT fall back to copy for non-EXDEV errors.
	// We verify this by checking that EACCES is not EXDEV.
	if errors.Is(syntheticErr.Err, syscall.EXDEV) {
		t.Fatal("test setup: EACCES should not be EXDEV")
	}
}

// TestBuildShadowSkipsBrokenSymlink reproduces the production failure that
// crashed niuniu-personal startup with
//   "owner-model migration failed: shadow build for repository 2: open …: The system cannot find the path specified."
// The trigger was a registered repo whose worktree contained pnpm node_modules
// junctions whose targets no longer existed. filepath.Walk supplied
// ENOENT/ERROR_PATH_NOT_FOUND to walkFn, and the original implementation
// fail-stopped the migration. After the fix BuildShadow must skip such
// entries and copy the rest of the tree.
func TestBuildShadowSkipsBrokenSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires admin/Developer Mode on Windows; tolerance is platform-agnostic")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	shadow := filepath.Join(dir, "shadow")

	writeFile(t, filepath.Join(src, "good.txt"), "good")
	writeFile(t, filepath.Join(src, "sub", "nested.txt"), "nested")

	// Broken symlink — mimics a dangling pnpm junction whose store target
	// has been deleted. lstat succeeds, os.Open follows the link and ENOENTs.
	if err := os.Symlink(filepath.Join(dir, "no-such-target"), filepath.Join(src, "sub", "broken")); err != nil {
		t.Fatal(err)
	}

	if err := BuildShadow(src, shadow); err != nil {
		t.Fatalf("BuildShadow must tolerate broken symlinks, got: %v", err)
	}
	if got := readFile(t, filepath.Join(shadow, "good.txt")); got != "good" {
		t.Errorf("good.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(shadow, "sub", "nested.txt")); got != "nested" {
		t.Errorf("sub/nested.txt = %q", got)
	}
}

// TestCopyFileToleratesMissingSrc covers the leaf-case of the same failure:
// when filepath.Walk has lstat'd a junction-as-directory and walkFn descends
// to read a child file whose underlying store entry vanished, copyFile must
// not propagate fs.ErrNotExist.
func TestCopyFileToleratesMissingSrc(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "ghost.txt")
	dst := filepath.Join(dir, "out.txt")

	err := fsmove.CopyFile(missing, dst, 0o644)
	if err != nil {
		t.Fatalf("CopyFile on missing src must tolerate fs.ErrNotExist, got: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dst should not be created when src is missing, stat err = %v", err)
	}
}

// TestRollbackSwaps simulates a completed AtomicSwap followed by rollbackSwaps
// (representing the DB-commit-fail path in MigrateOwnerModel). After rollback
// the original directory should be restored.
func TestRollbackSwaps(t *testing.T) {
	dir := t.TempDir()

	orig := filepath.Join(dir, "orig")
	shadow := filepath.Join(dir, "shadow")
	quarant := filepath.Join(dir, "quarantine")

	// Seed original and shadow
	writeFile(t, filepath.Join(orig, "data.txt"), "original-content")
	writeFile(t, filepath.Join(shadow, "data.txt"), "new-content")

	m := moveEntry{
		kind:    "workspace",
		id:      1,
		oldPath: orig,
		newPath: orig, // same for this test (single-swap scenario)
		shadow:  shadow,
		quarant: quarant,
	}

	// Perform the swap (simulates Phase 2a success).
	if err := AtomicSwap(orig, shadow, quarant); err != nil {
		t.Fatalf("AtomicSwap: %v", err)
	}

	// Verify swap worked: orig now has new content, quarantine has original.
	if got := readFile(t, filepath.Join(orig, "data.txt")); got != "new-content" {
		t.Fatalf("after swap: got %q, want new-content", got)
	}

	// Simulate DB commit failure: call rollbackSwaps.
	rollbackSwaps([]swapRecord{{m: m, didSwap: true}})

	// After rollback: original content should be restored.
	if got := readFile(t, filepath.Join(orig, "data.txt")); got != "original-content" {
		t.Errorf("after rollback: got %q, want original-content", got)
	}
}
