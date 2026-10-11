package service

import (
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
)

// The limiter is what stops a runaway function turning the sandbox into a jump
// box for the upstream, and the accounting is PER FUNCTION. Both halves matter.
func TestRateLimiterAccountsPerFunction(t *testing.T) {
	lim := newFunctionRateLimiter()
	now := time.Unix(1_700_000_000, 0)
	lim.now = func() time.Time { return now }

	for i := 0; i < functionCallsPerMinute; i++ {
		if wait, ok := lim.reserve(1); !ok {
			t.Fatalf("call %d of function 1 was refused (wait=%s)", i+1, wait)
		}
	}
	wait, ok := lim.reserve(1)
	if ok {
		t.Fatal("function 1 exceeded its call budget without being refused")
	}
	if wait <= 0 || wait > time.Minute {
		t.Fatalf("retry hint out of range: %s", wait)
	}
	// A different function has its own budget: one noisy function must not
	// starve another.
	if _, ok := lim.reserve(2); !ok {
		t.Fatal("function 2 was starved by function 1's budget")
	}
	// The window rolls.
	now = now.Add(time.Minute)
	if _, ok := lim.reserve(1); !ok {
		t.Fatal("the window did not roll over")
	}
}

func TestRateLimiterEgressBudgetIsSeparate(t *testing.T) {
	lim := newFunctionRateLimiter()
	for i := 0; i < functionEgressPerMinute; i++ {
		if !lim.allowEgress(7) {
			t.Fatalf("egress call %d was refused", i+1)
		}
	}
	if lim.allowEgress(7) {
		t.Fatal("the egress budget was not enforced")
	}
	// Spending the egress budget must not consume the invocation budget.
	if _, ok := lim.reserve(7); !ok {
		t.Fatal("egress accounting consumed the call budget")
	}
}

func TestFunctionScopeKeysIncludeIdempotency(t *testing.T) {
	egress := `[{"operation":"cancelOrder"},{"method":"get","path":"/x"}]`
	keys := functionScopeKeys(egress, true)
	for _, want := range []string{"operation:cancelOrder", "GET /x", "idempotent"} {
		if !keys[want] {
			t.Fatalf("scope key %q missing from %#v", want, keys)
		}
	}
	without := functionScopeKeys(egress, false)
	if without["idempotent"] {
		t.Fatal("idempotency leaked into the scope of a non-idempotent function")
	}
}

// A rollback that would re-grant authority must say so, and must lead with the
// grants — that is the direction a reviewer has to think hardest about.
func TestDiffScopeKeysReportsGrantsBeforeRevocations(t *testing.T) {
	live := map[string]bool{"operation:a": true, "operation:b": true}
	candidate := map[string]bool{"operation:b": true, "operation:c": true}
	diffs := diffScopeKeys(live, candidate)
	if len(diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %#v", diffs)
	}
	if diffs[0].Key != "operation:c" || diffs[0].After != "allowed" {
		t.Fatalf("the re-grant must come first, got %#v", diffs[0])
	}
	if diffs[1].Key != "operation:a" || diffs[1].After != "not allowed" {
		t.Fatalf("the revocation must come second, got %#v", diffs[1])
	}
	if d := diffScopeKeys(live, live); len(d) != 0 {
		t.Fatalf("identical scopes produced a diff: %#v", d)
	}
}

func TestFunctionScopeDiffDetectsAddedChangedAndRemoved(t *testing.T) {
	prev := []FunctionScopeEntry{
		{Name: "keep", Enabled: true, Egress: []string{"operation:a"}},
		{Name: "gone", Enabled: true, Egress: []string{"operation:z"}},
		{Name: "edit", Enabled: true, Egress: []string{"operation:a"}},
	}
	next := []ConfirmFunction{
		{Name: "keep", Enabled: true, Egress: []string{"operation:a"}},
		{Name: "edit", Enabled: true, Egress: []string{"operation:a", "operation:b"}},
		{Name: "fresh", Enabled: true, Egress: []string{"operation:c"}},
	}
	diffs := functionScopeDiff(next, prev)
	byField := map[string]ConfirmDiff{}
	for _, d := range diffs {
		byField[d.Field] = d
	}
	if d, ok := byField["scope.function.fresh"]; !ok || d.Change != ChangeAdded {
		t.Fatalf("a new function was not reported: %#v", diffs)
	}
	if d, ok := byField["scope.function.edit"]; !ok || d.Change != ChangeChanged {
		t.Fatalf("a widened function was not reported: %#v", diffs)
	}
	if d, ok := byField["scope.function.gone"]; !ok || d.Change != ChangeRemoved {
		t.Fatalf("a dropped function was not reported: %#v", diffs)
	}
	if _, ok := byField["scope.function.keep"]; ok {
		t.Fatalf("an unchanged function produced a diff: %#v", byField["scope.function.keep"])
	}
	for field := range byField {
		if len(field) < len("scope.") || field[:len("scope.")] != "scope." {
			t.Fatalf("diff field %q does not carry the scope. prefix the re-confirmation rule keys on", field)
		}
	}
}

// A pure code edit with identical reach is reviewed on the card but does not by
// itself re-grant anything.
func TestFunctionScopeDiffIgnoresCodeOnlyChanges(t *testing.T) {
	prev := []FunctionScopeEntry{{Name: "f", Enabled: true, Egress: []string{"operation:a"}, CodeHash: "sha256:old"}}
	next := []ConfirmFunction{{Name: "f", Enabled: true, Egress: []string{"operation:a"}, CodeHash: "sha256:new"}}
	if diffs := functionScopeDiff(next, prev); len(diffs) != 0 {
		t.Fatalf("a same-reach code change produced a scope diff: %#v", diffs)
	}
}

func TestRuntimeImplementedOK(t *testing.T) {
	if !RuntimeImplementedOK(sandbox.RuntimeGoja) {
		t.Fatal("the default runtime must be available")
	}
	if !RuntimeImplementedOK("") {
		t.Fatal("an empty runtime means the default and must be available")
	}
	if RuntimeImplementedOK(sandbox.RuntimeNode) {
		t.Fatal("the Node tier is declared but not implemented; it must not be reported as available")
	}
}

func TestFunctionLintBlocks(t *testing.T) {
	if FunctionLintBlocks([]FunctionDraftLint{{Function: "a"}}) {
		t.Fatal("a clean verdict blocked the publish")
	}
	if !FunctionLintBlocks([]FunctionDraftLint{{Function: "a", Blocking: true}}) {
		t.Fatal("a blocking verdict did not block the publish")
	}
}

// The draft-carried function scope must be stable and complete: it is folded
// into the provider version's scope snapshot, which the rollback rule diffs.
func TestFunctionScopeForDraftIsSortedAndComplete(t *testing.T) {
	scope := FunctionScopeForDraft([]FunctionDraftPayload{
		{Name: "zeta", Enabled: true, Idempotent: true,
			Egress: []sandbox.EgressOp{{Operation: "b"}, {Operation: "a"}}},
		{Name: "alpha", Enabled: false, Egress: []sandbox.EgressOp{{Method: "get", Path: "/x"}}},
	})
	if len(scope.Functions) != 2 || scope.Functions[0].Name != "alpha" {
		t.Fatalf("functions are not sorted by name: %#v", scope.Functions)
	}
	if got := scope.Functions[1].Egress; len(got) != 2 || got[0] != "operation:a" || got[1] != "operation:b" {
		t.Fatalf("egress keys are not sorted: %#v", got)
	}
	if !scope.Functions[1].Idempotent {
		t.Fatal("the idempotency declaration was dropped from the scope")
	}
	if scope.Functions[0].Enabled {
		t.Fatal("the enabled flag was dropped from the scope")
	}
}

func TestTruncateMsg(t *testing.T) {
	if got := truncateMsg("abcdef", 3); got != "abc…" {
		t.Fatalf("got %q", got)
	}
	if got := truncateMsg("ab", 3); got != "ab" {
		t.Fatalf("got %q", got)
	}
}
