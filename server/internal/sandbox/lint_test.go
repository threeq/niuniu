package sandbox

import (
	"bufio"
	"strings"
	"testing"
)

// The lint is a review aid, not the security boundary, but a draft it flags as
// blocking must never reach publication — so the blocking verdicts are the part
// worth pinning down.
//
// Every eval / new Function / require below is a STRING FIXTURE handed to the
// linter. None of it is ever executed; the assertions are about the verdicts,
// not behaviour.
func TestLintBlocksTheSampleConstructs(t *testing.T) {
	cases := []struct {
		name string
		code string
		rule string
	}{
		{"eval", "function main() { return eval('1+1'); }", "no-eval"},
		{"dynamic function", "function main() { return new Function('return 1')(); }", "no-eval"},
		{"require", "function main() { const fs = require('fs'); return 1; }", "no-require"},
		{"host global", "function main() { return process.env.SECRET; }", "no-require"},
		{"fetch", "function main() { return fetch('x'); }", "no-host-network"},
		{"async", "async function main() { await 1; }", "no-async"},
		{"promise", "function main() { return Promise.resolve(1); }", "no-async"},
		{"timer", "function main() { setTimeout(function(){}, 1); }", "no-async"},
		{"infinite loop", "function main() { while (true) {} }", "no-infinite-loop"},
		{"for empty", "function main() { for (;;) {} }", "no-infinite-loop"},
		{"proto", "function main() { return ({}).__proto__; }", "no-proto-access"},
		{"constructor", "function main() { return [].constructor; }", "no-proto-access"},
		{"absolute url", `function main() { return http_request({operation: "x", url: "https://evil.example.com/"}); }`, "no-absolute-url"},
		{"import", "import x from 'y';\nfunction main() {}", "no-import"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Lint(tc.code)
			found := false
			for _, f := range findings {
				if f.Rule == tc.rule {
					found = true
					if f.Severity != SeverityError {
						t.Errorf("rule %s should block, got severity %s", tc.rule, f.Severity)
					}
					if f.Line < 1 {
						t.Errorf("rule %s reported line %d", tc.rule, f.Line)
					}
				}
			}
			if !found {
				t.Fatalf("expected rule %s to fire on %q; got %#v", tc.rule, tc.code, findings)
			}
			if !LintBlocks(findings) {
				t.Fatalf("rule %s produced no blocking verdict", tc.rule)
			}
		})
	}
}

// A well-formed operation function must pass cleanly, or the lint would be
// noise the reviewer learns to click through.
func TestLintAcceptsAWellFormedFunction(t *testing.T) {
	code := `function main(input) {
		const token = http_request({operation: "exchangeToken", body: {grant: input.grant}});
		if (!token.ok) { return {ok: false, error: token.error}; }
		const orders = http_request({operation: "listOrders", query: {page: 1}});
		console.log("fetched " + orders.status);
		return {ok: true, count: orders.body.length};
	}`
	findings := Lint(code)
	if LintBlocks(findings) {
		t.Fatalf("a valid function was blocked: %s", LintSummary(findings))
	}
	if len(findings) != 0 {
		t.Logf("advisory findings: %s", LintSummary(findings))
	}
}

// Banned words inside a string literal are data, not code: a function that
// returns a message mentioning "process" must not be blocked, while a real
// absolute URL is still caught.
func TestLintIgnoresBannedWordsInsideStringLiterals(t *testing.T) {
	findings := Lint(`function main() { return {note: "the import of this process is denied"}; }`)
	if LintBlocks(findings) {
		t.Fatalf("string content was treated as code: %s", LintSummary(findings))
	}

	urlFindings := Lint(`function main() { return http_request({operation: "x", note: "https://evil.example.com"}); }`)
	found := false
	for _, f := range urlFindings {
		if f.Rule == "no-absolute-url" {
			found = true
		}
	}
	if !found {
		t.Fatal("an absolute URL inside a literal slipped through")
	}
}

func TestSpecNormalizeRejectsBadInput(t *testing.T) {
	base := Spec{
		Name:   "ok",
		Code:   "function main() { return 1; }",
		Egress: []EgressOp{{Operation: "op"}},
	}
	if _, err := base.Normalize(); err != nil {
		t.Fatalf("a valid spec was rejected: %v", err)
	}

	bad := []Spec{
		{Name: "", Code: base.Code, Egress: base.Egress},
		{Name: "1nope", Code: base.Code, Egress: base.Egress},
		{Name: "x", Code: "", Egress: base.Egress},
		{Name: "x", Code: base.Code},
		{Name: "x", Code: base.Code, Runtime: RuntimeNode, Egress: base.Egress},
		{Name: "x", Code: base.Code, Egress: []EgressOp{{Operation: "op"}, {Operation: "op"}}},
	}
	for i, s := range bad {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("spec %d was accepted but should not have been: %#v", i, s)
		}
	}
}

// A spec must never authorize an operation it did not declare, whatever the
// request says.
func TestSpecAllowsOnlyDeclaredOperations(t *testing.T) {
	spec := Spec{Egress: []EgressOp{{Operation: "a"}, {Operation: "b"}}}
	if !spec.Allows(&HTTPRequestMessage{Operation: "a"}) {
		t.Fatal("declared operation a was refused")
	}
	if spec.Allows(&HTTPRequestMessage{Operation: "c"}) {
		t.Fatal("undeclared operation c was allowed")
	}
	if spec.Allows(&HTTPRequestMessage{}) {
		t.Fatal("an operation-less request was allowed")
	}
}

func TestLimitsAreClampedToTheCeiling(t *testing.T) {
	l := Limits{TimeoutMS: 10_000_000, MemoryMB: 100_000, MaxEgress: 100_000, MaxOutputBytes: 1 << 40}.normalized()
	if l.TimeoutMS != maxTimeoutMS {
		t.Errorf("timeout not clamped: %d", l.TimeoutMS)
	}
	if l.MemoryMB != maxMemoryMB {
		t.Errorf("memory not clamped: %d", l.MemoryMB)
	}
	if l.MaxEgress != maxEgress {
		t.Errorf("egress not clamped: %d", l.MaxEgress)
	}
	if l.MaxOutputBytes != maxOutputB {
		t.Errorf("output not clamped: %d", l.MaxOutputBytes)
	}
	def := Limits{}.normalized()
	if def.TimeoutMS != DefaultLimits().TimeoutMS {
		t.Errorf("zero limits did not fall back to the default: %#v", def)
	}
}

func TestToolNameIsStableAndSanitised(t *testing.T) {
	if got := ToolName("TAPD Personal", "Two Step"); got != "ext__tapd_personal__two_step" {
		t.Fatalf("unexpected tool name %q", got)
	}
	if got := ToolName("", ""); !strings.HasPrefix(got, "ext__") {
		t.Fatalf("empty segments must still produce a usable name, got %q", got)
	}
}

func TestCodeHashPinsTheSource(t *testing.T) {
	a := Spec{Code: "function main(){}"}
	b := Spec{Code: "function main(){} "}
	if a.CodeHash() == "" || !strings.HasPrefix(a.CodeHash(), "sha256:") {
		t.Fatalf("unexpected hash %q", a.CodeHash())
	}
	if a.CodeHash() == b.CodeHash() {
		t.Fatal("a one-character change must change the hash")
	}
	if a.CodeHash() != (Spec{Code: a.Code}).CodeHash() {
		t.Fatal("the hash is not deterministic")
	}
}

// Frame framing must survive a round trip and refuse an oversized frame rather
// than allocating for it.
func TestFrameRoundTrip(t *testing.T) {
	var buf strings.Builder
	msg := ResultMessage{Kind: KindResult, OK: true, Output: map[string]any{"n": 1}, EgressCalls: 2}
	if err := WriteFrame(&buf, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	kind, body, err := ReadFrame(bufio.NewReader(strings.NewReader(buf.String())))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != KindResult {
		t.Fatalf("kind = %q", kind)
	}
	if !strings.Contains(string(body), `"egress_calls":2`) {
		t.Fatalf("body did not round-trip: %s", body)
	}
}

// A build that ships without the helper must report the fence UNAVAILABLE. The
// platform probes only run their spawn check when a helper is configured, so
// without this the host would report a healthy fence and then fail every call
// with "helper not configured" — the silent downgrade the design forbids.
func TestSelfCheckFailsClosedWithoutTheHelper(t *testing.T) {
	prev := HelperPath()
	t.Cleanup(func() { SetHelperPath(prev) })

	SetHelperPath("")
	av := SelfCheck(true)
	if av.Available {
		t.Fatal("a host with no sandbox helper reported a usable fence")
	}
	missing := false
	for _, c := range av.Components {
		if c.Name == "sandbox-helper" && c.Required && !c.OK {
			missing = true
		}
	}
	if !missing {
		t.Fatalf("the missing helper was not reported as a required failure: %+v", av.Components)
	}
	// The refusal has to name the cause, or the UI cannot tell the user what to fix.
	if !strings.Contains(av.Reason, "sandbox-helper") {
		t.Fatalf("the verdict does not name the failing component: %q", av.Reason)
	}

	// And a configured helper restores the verdict.
	SetHelperPath(prev)
	if prev != "" {
		if got := SelfCheck(true); !got.Available {
			t.Fatalf("the fence did not recover once the helper was back: %s", got.Reason)
		}
	}
}
