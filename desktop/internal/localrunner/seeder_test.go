package localrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSeeder(dir string, p RemoteStateProvider, g gitRunner) *Seeder {
	return &Seeder{dir: dir, provider: p, git: g}
}

// TestSeeder_NonEmptyDirSkips: an already-populated bound dir must never be
// clobbered by a clone — seeding only applies to an empty directory.
func TestSeeder_NonEmptyDirSkips(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rg := &recGit{}
	s := newTestSeeder(dir, &fakeProvider{states: []RepoState{{Name: "r", CloneURL: "https://h/r.git"}}}, rg.run)

	summary, err := s.Seed(context.Background())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !strings.Contains(summary, "not empty") {
		t.Fatalf("summary %q should explain the skip", summary)
	}
	if len(rg.calls) != 0 {
		t.Fatalf("no git call expected on a non-empty dir, got %v", rg.calls)
	}
}

// TestSeeder_NoCloneURLSkips: an empty dir with no clone URL configured is a
// no-op — the runner simply provides an empty operable directory.
func TestSeeder_NoCloneURLSkips(t *testing.T) {
	rg := &recGit{}
	s := newTestSeeder(t.TempDir(), &fakeProvider{states: []RepoState{{Name: "r", CurrentBranch: "ws-1/main"}}}, rg.run)

	summary, err := s.Seed(context.Background())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !strings.Contains(summary, "no clone URL") {
		t.Fatalf("summary %q should explain the no-op", summary)
	}
	if len(rg.calls) != 0 {
		t.Fatalf("no git call expected without a clone URL, got %v", rg.calls)
	}
}

// TestSeeder_SingleRepoClonesIntoDir: one seedable repo clones into the bound
// dir itself (keeps the single-repo sync model) then materializes its branch.
func TestSeeder_SingleRepoClonesIntoDir(t *testing.T) {
	dir := t.TempDir()
	rg := &recGit{}
	s := newTestSeeder(dir, &fakeProvider{states: []RepoState{{
		Name:          "niuniu",
		CurrentBranch: "ws-740/epic/526",
		CloneURL:      "https://github.com/x/niuniu.git",
	}}}, rg.run)

	summary, err := s.Seed(context.Background())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var sawClone, sawCheckout bool
	for _, c := range rg.calls {
		if strings.Contains(c, "clone https://github.com/x/niuniu.git "+dir) {
			sawClone = true
		}
		if strings.Contains(c, "checkout -B ws-740/epic/526") {
			sawCheckout = true
		}
	}
	if !sawClone {
		t.Fatalf("expected clone into bound dir, calls=%v", rg.calls)
	}
	if !sawCheckout {
		t.Fatalf("expected checkout -B of the current branch, calls=%v", rg.calls)
	}
	if !strings.Contains(summary, "niuniu") {
		t.Fatalf("summary should name the cloned repo: %q", summary)
	}
}

// TestSeeder_MultiRepoClonesIntoSubdirs: multiple seedable repos each clone into
// a per-name subdirectory of the bound dir (the AI-managed multi-repo layout).
func TestSeeder_MultiRepoClonesIntoSubdirs(t *testing.T) {
	dir := t.TempDir()
	rg := &recGit{}
	s := newTestSeeder(dir, &fakeProvider{states: []RepoState{
		{Name: "api", CloneURL: "https://h/api.git", CurrentBranch: "main"},
		{Name: "web", CloneURL: "https://h/web.git"},
	}}, rg.run)

	if _, err := s.Seed(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wantAPI := "clone https://h/api.git " + filepath.Join(dir, "api")
	wantWeb := "clone https://h/web.git " + filepath.Join(dir, "web")
	var sawAPI, sawWeb bool
	for _, c := range rg.calls {
		if strings.Contains(c, wantAPI) {
			sawAPI = true
		}
		if strings.Contains(c, wantWeb) {
			sawWeb = true
		}
	}
	if !sawAPI || !sawWeb {
		t.Fatalf("expected both repos cloned into subdirs, calls=%v", rg.calls)
	}
}

// TestSeeder_CloneFailureSurfaces: a failed clone returns an error the caller
// logs (best-effort), and does not attempt the branch checkout.
func TestSeeder_CloneFailureSurfaces(t *testing.T) {
	rg := &recGit{failOn: "clone", failErr: errors.New("auth failed")}
	s := newTestSeeder(t.TempDir(), &fakeProvider{states: []RepoState{{
		Name: "r", CloneURL: "https://h/r.git", CurrentBranch: "main",
	}}}, rg.run)

	if _, err := s.Seed(context.Background()); err == nil {
		t.Fatal("clone failure should surface as an error")
	}
	for _, c := range rg.calls {
		if strings.Contains(c, "checkout") {
			t.Fatalf("must not checkout after a failed clone, calls=%v", rg.calls)
		}
	}
}

// TestSeeder_RejectsUnsafeRepoName: a crafted multi-repo name must never escape
// the bound directory via the per-name subdir path.
func TestSeeder_RejectsUnsafeRepoName(t *testing.T) {
	rg := &recGit{}
	s := newTestSeeder(t.TempDir(), &fakeProvider{states: []RepoState{
		{Name: "../evil", CloneURL: "https://h/a.git"},
		{Name: "ok", CloneURL: "https://h/b.git"},
	}}, rg.run)

	if _, err := s.Seed(context.Background()); err == nil {
		t.Fatal("an escaping repo name must fail the seed")
	}
	for _, c := range rg.calls {
		if strings.Contains(c, "..") {
			t.Fatalf("must not clone into an escaping path, calls=%v", rg.calls)
		}
	}
}

// TestSeeder_ProviderErrorSurfaces: an inability to fetch seed state is an error.
func TestSeeder_ProviderErrorSurfaces(t *testing.T) {
	s := newTestSeeder(t.TempDir(), &fakeProvider{err: errors.New("boom")}, (&recGit{}).run)
	if _, err := s.Seed(context.Background()); err == nil {
		t.Fatal("provider error should surface")
	}
}
