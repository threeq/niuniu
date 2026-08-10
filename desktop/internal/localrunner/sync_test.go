package localrunner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recGit is a fake gitRunner recording invocations and optionally failing when
// the joined args contain a substring.
type recGit struct {
	mu      sync.Mutex
	calls   []string // "<args>|<stdin>"
	failOn  string
	failErr error
}

func (r *recGit) run(_ context.Context, _ string, stdin string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	r.mu.Lock()
	r.calls = append(r.calls, joined+"|"+stdin)
	r.mu.Unlock()
	if r.failOn != "" && strings.Contains(joined, r.failOn) {
		return "", r.failErr
	}
	return "", nil
}

type fakeProvider struct {
	states []RepoState
	err    error
}

func (f *fakeProvider) Fetch(context.Context) ([]RepoState, error) { return f.states, f.err }

func newTestSyncer(dir string, p RemoteStateProvider, g gitRunner) *GitSyncer {
	return &GitSyncer{dir: dir, provider: p, git: g}
}

func TestGitSyncer_NotARepo(t *testing.T) {
	rg := &recGit{failOn: "rev-parse", failErr: errors.New("not a repo")}
	s := newTestSyncer(t.TempDir(), &fakeProvider{}, rg.run)
	summary, err := s.Sync(context.Background())
	if err == nil {
		t.Fatal("expected error when not a git repo")
	}
	if !strings.Contains(summary, "not a git repository") {
		t.Fatalf("summary %q should explain the skip", summary)
	}
}

func TestGitSyncer_CheckoutAndApply(t *testing.T) {
	rg := &recGit{}
	p := &fakeProvider{states: []RepoState{{
		CurrentBranch: "ws-1/main",
		Patch:         "diff --git a/x b/x\n",
	}}}
	s := newTestSyncer(t.TempDir(), p, rg.run)

	summary, err := s.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if !strings.Contains(summary, "checked out ws-1/main") || !strings.Contains(summary, "applied remote") {
		t.Fatalf("summary missing steps: %q", summary)
	}
	var sawCheckout, sawApply bool
	for _, c := range rg.calls {
		if strings.Contains(c, "checkout ws-1/main") {
			sawCheckout = true
		}
		if strings.HasPrefix(c, "apply ") && strings.Contains(c, "diff --git") {
			sawApply = true // patch went in via stdin
		}
	}
	if !sawCheckout || !sawApply {
		t.Fatalf("expected checkout + apply(stdin), calls=%v", rg.calls)
	}
}

func TestGitSyncer_NoProvider(t *testing.T) {
	s := &GitSyncer{dir: t.TempDir(), provider: nil, git: (&recGit{}).run}
	summary, err := s.Sync(context.Background())
	if err != nil {
		t.Fatalf("nil provider should be a no-op, got %v", err)
	}
	if !strings.Contains(summary, "no remote state provider") {
		t.Fatalf("unexpected summary %q", summary)
	}
}

func TestGitSyncer_ProviderError(t *testing.T) {
	rg := &recGit{}
	p := &fakeProvider{err: errors.New("boom")}
	s := newTestSyncer(t.TempDir(), p, rg.run)
	if _, err := s.Sync(context.Background()); err == nil {
		t.Fatal("provider error should surface")
	}
}

func TestHTTPRemoteState_Fetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/42/diff" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`[{"name":"repo","base_branch":"main","current_branch":"ws-1/main","files":[{"raw_patch":"diff a"},{"raw_patch":"diff b\n"}]}]`))
	}))
	defer srv.Close()

	p := NewHTTPRemoteState(srv.Client(), srv.URL, "42", "tok")
	states, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("expected 1 repo state, got %d", len(states))
	}
	if states[0].CurrentBranch != "ws-1/main" {
		t.Fatalf("branch = %q", states[0].CurrentBranch)
	}
	if states[0].Patch != "diff a\ndiff b\n" {
		t.Fatalf("concatenated patch = %q", states[0].Patch)
	}
}
