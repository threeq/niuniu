// White-box tests for egress data governance (Epic #726 / #747): the value-free
// data summary, the sensitive-pattern detector, and the personal-edition
// warn-only policy.
//
// The load-bearing invariant is the same one the audit fingerprint carries:
// the summary must describe the SHAPE of what is leaving (size + field names +
// sensitive CATEGORIES) while carrying none of its content. A confirmation card
// showing the summary, or an alert carrying it, must never become a second copy
// of the payload.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func mustJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSummarizeEgressBody_NoPlaintextLeak(t *testing.T) {
	body := map[string]any{
		"title":  "quarterly revenue projection",
		"secret": "hunter2-do-not-log",
		"user": map[string]any{
			"contact": "alice@example.com",
		},
	}
	raw := mustJSONBytes(t, body)
	s := SummarizeEgressBody(raw)

	if s.Bytes != int64(len(raw)) {
		t.Errorf("bytes = %d, want %d", s.Bytes, len(raw))
	}
	// Top-level field NAMES only, sorted.
	if got := strings.Join(s.Fields, ","); got != "secret,title,user" {
		t.Errorf("fields = %v, want [secret title user]", s.Fields)
	}
	// credential (key "secret") and email (value) must both be reported.
	if !hasHit(s, "credential") {
		t.Errorf("expected a credential hit, got %+v", s.SensitiveHits)
	}
	if !hasHit(s, "email") {
		t.Errorf("expected an email hit, got %+v", s.SensitiveHits)
	}

	// The acid test: no value from the body may survive anywhere in the summary
	// (including the dotted Where locators the hits carry).
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"hunter2-do-not-log",
		"quarterly revenue projection",
		"alice@example.com",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("summary leaked value %q: %s", secret, encoded)
		}
	}
	// The Where locators are field PATHS, never values.
	for _, h := range s.SensitiveHits {
		for _, w := range h.Where {
			if strings.Contains(w, "hunter2") || strings.Contains(w, "alice@") {
				t.Errorf("hit Where leaked a value: %q", w)
			}
		}
	}
}

func TestSummarizeEgressBody_EmptyAndNonObject(t *testing.T) {
	for _, raw := range [][]byte{nil, {}} {
		s := SummarizeEgressBody(raw)
		if s.Bytes != 0 || len(s.Fields) != 0 || s.HasSensitive() {
			t.Errorf("empty body: got %+v", s)
		}
	}
	// A non-object body records its size but no field names (same tolerance the
	// audit fingerprint applies).
	s := SummarizeEgressBody([]byte(`["a","b"]`))
	if s.Bytes != int64(len(`["a","b"]`)) || len(s.Fields) != 0 {
		t.Errorf("non-object body: got %+v", s)
	}
}

func TestSummarizeEgressBody_SensitiveCategories(t *testing.T) {
	raw := mustJSONBytes(t, map[string]any{
		"api_key":    "abcd",
		"card":       "4111 1111 1111 1111",
		"id_card":    "11010519491231002X",
		"contact":    "bob@example.org",
		"privatekey": "-----BEGIN RSA PRIVATE KEY-----MIIE",
	})
	s := SummarizeEgressBody(raw)
	for _, want := range []string{"credential", "payment_card", "national_id", "email"} {
		if !hasHit(s, want) {
			t.Errorf("missing %q hit; got %+v", want, s.SensitiveHits)
		}
	}
	// Severity ordering: high-severity categories sort first.
	if len(s.SensitiveHits) > 1 && s.SensitiveHits[0].Severity != "high" {
		t.Errorf("expected high severity first, got %+v", s.SensitiveHits)
	}
}

func TestSummarizeEgressBody_CountsRepeatedMatches(t *testing.T) {
	raw := mustJSONBytes(t, map[string]any{
		"one":   "a@x.com",
		"two":   "b@x.com",
		"three": "c@x.com",
	})
	s := SummarizeEgressBody(raw)
	h := findHit(s, "email")
	if h == nil {
		t.Fatalf("no email hit: %+v", s.SensitiveHits)
	}
	if h.Count != 3 {
		t.Errorf("email count = %d, want 3", h.Count)
	}
	if len(h.Where) != 3 {
		t.Errorf("where = %v, want 3 locators", h.Where)
	}
}

// A sensitive value must be caught wherever it sits — a scanner that truncates
// values, stops after a node budget, or only looks at JSON would miss these.
// They are the bypasses a "shortcut" tripwire would ship with.
func TestSummarizeEgressBody_DetectsBeyondPrefixAndNodeCaps(t *testing.T) {
	// 1) Secret appended well past a benign prefix inside one string value.
	raw := mustJSONBytes(t, map[string]any{
		"note": strings.Repeat("a", 8192) + " alice@example.com",
	})
	if !hasHit(SummarizeEgressBody(raw), "email") {
		t.Error("value pattern past a long prefix was not detected")
	}

	// 2) Secret-shaped key buried after many sibling nodes.
	big := map[string]any{}
	for i := 0; i < 30000; i++ {
		big[fmt.Sprintf("k%05d", i)] = "x"
	}
	big["token"] = "abcd"
	if !hasHit(SummarizeEgressBody(mustJSONBytes(t, big)), "credential") {
		t.Error("key pattern past many nodes was not detected")
	}

	// 3) A body that is not JSON is still content-scanned (as raw text).
	if !hasHit(SummarizeEgressBody([]byte("email=bob@example.com&x=1")), "email") {
		t.Error("non-JSON body was not content-scanned")
	}
}

func TestSummarizeEgressPayload_NilAndMap(t *testing.T) {
	if got := SummarizeEgressPayload(nil); got.Bytes != 0 {
		t.Errorf("nil payload bytes = %d, want 0", got.Bytes)
	}
	got := SummarizeEgressPayload(map[string]any{"body": map[string]any{"x": 1}})
	if len(got.Fields) != 1 || got.Fields[0] != "body" {
		t.Errorf("fields = %v, want [body]", got.Fields)
	}
}

// DefaultEgressPolicy must flag an over-threshold body AND a sensitive hit, and
// must NEVER block — the personal edition cannot break a workflow on a heuristic.
func TestDefaultEgressPolicy_WarnsButNeverBlocks(t *testing.T) {
	p := DefaultEgressPolicy{MaxBytes: 16}
	ctx := context.Background()

	small := SummarizeEgressBody([]byte(`{"a":"b"}`))
	if v := p.Evaluate(ctx, EgressRequest{Summary: small}); v.Decision != EgressAllow {
		t.Errorf("clean body: decision = %q, want allow (%v)", v.Decision, v.Reasons)
	}

	big := SummarizeEgressBody([]byte(`{"a":"` + strings.Repeat("x", 64) + `"}`))
	v := p.Evaluate(ctx, EgressRequest{Summary: big})
	if v.Decision != EgressWarn {
		t.Errorf("oversize body: decision = %q, want warn", v.Decision)
	}
	if len(v.Reasons) == 0 {
		t.Error("oversize body: expected a reason")
	}

	creds := SummarizeEgressBody([]byte(`{"token":"abcd"}`))
	v = p.Evaluate(ctx, EgressRequest{Summary: creds})
	if v.Decision != EgressWarn {
		t.Errorf("sensitive body: decision = %q, want warn", v.Decision)
	}

	// Whatever the input, a non-allow verdict from the default policy is never
	// `block`.
	huge := SummarizeEgressBody([]byte(`{"a":"` + strings.Repeat("x", 1<<20) + `"}`))
	if v := p.Evaluate(ctx, EgressRequest{Summary: huge}); v.Decision == EgressBlock {
		t.Error("default policy must never block")
	}
}

func TestEgressWarnBytes_DefaultAndOverride(t *testing.T) {
	if got := egressWarnBytes(nil); got != DefaultEgressWarnBytes {
		t.Errorf("nil policy: %d, want %d", got, DefaultEgressWarnBytes)
	}
	if got := egressWarnBytes(DefaultEgressPolicy{}); got != DefaultEgressWarnBytes {
		t.Errorf("zero policy: %d, want %d", got, DefaultEgressWarnBytes)
	}
	if got := egressWarnBytes(DefaultEgressPolicy{MaxBytes: 42}); got != 42 {
		t.Errorf("override: %d, want 42", got)
	}
}

func hasHit(s EgressDataSummary, pattern string) bool { return findHit(s, pattern) != nil }

func findHit(s EgressDataSummary, pattern string) *EgressSensitiveHit {
	for i := range s.SensitiveHits {
		if s.SensitiveHits[i].Pattern == pattern {
			return &s.SensitiveHits[i]
		}
	}
	return nil
}
