package localrunner

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeApprover returns a canned decision and records the requests it saw.
type fakeApprover struct {
	result ApprovalResult
	mu     sync.Mutex
	seen   []ApprovalRequest
}

func (f *fakeApprover) Approve(req ApprovalRequest) ApprovalResult {
	f.mu.Lock()
	f.seen = append(f.seen, req)
	f.mu.Unlock()
	return f.result
}

// memAuditor captures decisions in memory.
type memAuditor struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (m *memAuditor) Record(e AuditEntry) {
	m.mu.Lock()
	m.entries = append(m.entries, e)
	m.mu.Unlock()
}

func (m *memAuditor) last() AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return AuditEntry{}
	}
	return m.entries[len(m.entries)-1]
}

func fixedNow() int64 { return 1234 }

func TestAuthorizeCommand_WhitelistProgram(t *testing.T) {
	aud := &memAuditor{}
	g := NewGateway(GatewayConfig{Dir: t.TempDir(), Allowed: []string{"go", "npm"}, Audit: aud})

	ok, reason := g.AuthorizeCommand("go test ./...", fixedNow)
	if !ok {
		t.Fatalf("expected whitelisted program to be allowed, got deny: %s", reason)
	}
	if e := aud.last(); !e.Allowed || e.TS != 1234 {
		t.Fatalf("audit not recorded correctly: %+v", e)
	}
}

func TestAuthorizeCommand_DefaultDeny(t *testing.T) {
	// No approver ⇒ anything unlisted is denied (fail-safe).
	aud := &memAuditor{}
	g := NewGateway(GatewayConfig{Dir: t.TempDir(), Allowed: []string{"go"}, Audit: aud})

	ok, _ := g.AuthorizeCommand("rm -rf /", fixedNow)
	if ok {
		t.Fatal("expected unlisted command to be denied by default")
	}
	if aud.last().Allowed {
		t.Fatal("deny should be audited as not allowed")
	}
}

func TestAuthorizeCommand_ShellControlBlocksProgramShortcut(t *testing.T) {
	// "git" is whitelisted, but a chained command must NOT ride the whitelist —
	// it has to be confirmed. With no approver it is denied.
	g := NewGateway(GatewayConfig{Dir: t.TempDir(), Allowed: []string{"git"}, Audit: &memAuditor{}})

	if ok, _ := g.AuthorizeCommand("git status && rm -rf /", fixedNow); ok {
		t.Fatal("chained command with whitelisted head must not be auto-allowed")
	}
	if ok, _ := g.AuthorizeCommand("git status | sh", fixedNow); ok {
		t.Fatal("piped command must not be auto-allowed")
	}
	if ok, _ := g.AuthorizeCommand("git $(whoami)", fixedNow); ok {
		t.Fatal("command substitution must not be auto-allowed")
	}
}

func TestAuthorizeCommand_ApproverAllowOnce(t *testing.T) {
	ap := &fakeApprover{result: ApprovalResult{Allow: true, Always: false}}
	g := NewGateway(GatewayConfig{Dir: t.TempDir(), Allowed: nil, Approver: ap, Audit: &memAuditor{}})

	ok, _ := g.AuthorizeCommand("curl example.com", fixedNow)
	if !ok {
		t.Fatal("approver allowed once, expected allow")
	}
	if len(ap.seen) != 1 || ap.seen[0].Command != "curl example.com" {
		t.Fatalf("approver should see the full command + dir: %+v", ap.seen)
	}
	// Allow-once does NOT persist: a second call prompts again.
	_, _ = g.AuthorizeCommand("curl example.com", fixedNow)
	if len(ap.seen) != 2 {
		t.Fatal("allow-once must re-prompt on the next invocation")
	}
}

func TestAuthorizeCommand_ApproverAllowAlwaysPersists(t *testing.T) {
	ap := &fakeApprover{result: ApprovalResult{Allow: true, Always: true}}
	var persisted []string
	g := NewGateway(GatewayConfig{
		Dir:                t.TempDir(),
		AlwaysAllowPersist: true,
		Approver:           ap,
		Persist:            func(entry string) error { persisted = append(persisted, entry); return nil },
		Audit:              &memAuditor{},
	})

	if ok, _ := g.AuthorizeCommand("deno run x", fixedNow); !ok {
		t.Fatal("expected allow-always to allow")
	}
	if len(persisted) != 1 || persisted[0] != "deno" {
		t.Fatalf("expected program name persisted, got %v", persisted)
	}
	// Now it's whitelisted — no further prompt.
	if ok, _ := g.AuthorizeCommand("deno run y", fixedNow); !ok {
		t.Fatal("persisted program should now be whitelisted")
	}
	if len(ap.seen) != 1 {
		t.Fatalf("should not re-prompt after persist; prompts=%d", len(ap.seen))
	}
}

func TestAuthorizeCommand_AlwaysNotPersistedWhenDisabled(t *testing.T) {
	ap := &fakeApprover{result: ApprovalResult{Allow: true, Always: true}}
	var persisted []string
	g := NewGateway(GatewayConfig{
		Dir:                t.TempDir(),
		AlwaysAllowPersist: false, // user hasn't enabled persistence
		Approver:           ap,
		Persist:            func(entry string) error { persisted = append(persisted, entry); return nil },
		Audit:              &memAuditor{},
	})
	_, _ = g.AuthorizeCommand("deno run x", fixedNow)
	if len(persisted) != 0 {
		t.Fatalf("must not persist when AlwaysAllowPersist is off, got %v", persisted)
	}
}

func TestAuthorizeCommand_EmptyDenied(t *testing.T) {
	g := NewGateway(GatewayConfig{Dir: t.TempDir(), Audit: &memAuditor{}})
	if ok, _ := g.AuthorizeCommand("   ", fixedNow); ok {
		t.Fatal("empty command must be denied")
	}
}

func TestResolvePath_WithinBoundary(t *testing.T) {
	dir := t.TempDir()
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})

	abs, err := g.ResolvePath("src/main.go", fixedNow)
	if err != nil {
		t.Fatalf("relative path within dir should resolve: %v", err)
	}
	want := filepath.Join(dir, "src", "main.go")
	if abs != want {
		t.Fatalf("resolved %q, want %q", abs, want)
	}
}

func TestResolvePath_RejectsEscape(t *testing.T) {
	dir := t.TempDir()
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})

	for _, bad := range []string{"../secret", "a/../../etc/passwd", "..\\..\\win"} {
		if _, err := g.ResolvePath(bad, fixedNow); err == nil {
			t.Fatalf("path %q should be rejected as an escape", bad)
		}
	}
}

func TestResolvePath_RejectsAbsolute(t *testing.T) {
	dir := t.TempDir()
	g := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})

	abspath := filepath.Join(os.TempDir(), "outside.txt")
	if _, err := g.ResolvePath(abspath, fixedNow); err == nil {
		t.Fatalf("absolute path %q should be rejected", abspath)
	}
}
