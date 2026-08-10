package service

import (
	"strconv"
	"testing"
	"time"
)

// fixedNow is an arbitrary fixed instant so token math is deterministic.
// 2026-06-30T01:00:00Z == 1782781200000 ms.
var fixedNow = time.Date(2026, 6, 30, 1, 0, 0, 0, time.UTC)

const fixedNowMS = int64(1782781200000)

func TestEvalNowToken(t *testing.T) {
	cases := []struct {
		name             string
		sign, num, unit  string
		want             int64
	}{
		{"bare now", "", "", "", fixedNowMS},
		{"minus 10m", "-", "10", "m", fixedNowMS - 10*60*1000},
		{"plus 5s", "+", "5", "s", fixedNowMS + 5*1000},
		{"minus 2h", "-", "2", "h", fixedNowMS - 2*60*60*1000},
		{"minus 1d", "-", "1", "d", fixedNowMS - 24*60*60*1000},
		{"minus 500ms", "-", "500", "ms", fixedNowMS - 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := evalNowToken(fixedNow, c.sign, c.num, c.unit); got != c.want {
				t.Fatalf("evalNowToken(%q,%q,%q) = %d, want %d", c.sign, c.num, c.unit, got, c.want)
			}
		})
	}
}

func TestResolveNowTokensInScalar_ExactTokenBecomesNumber(t *testing.T) {
	got := resolveNowTokensInScalar("{{now-10m}}", fixedNow)
	want := fixedNowMS - 10*60*1000
	n, ok := got.(int64)
	if !ok {
		t.Fatalf("exact token should resolve to int64, got %T (%v)", got, got)
	}
	if n != want {
		t.Fatalf("got %d, want %d", n, want)
	}
}

func TestResolveNowTokensInScalar_WhitespaceTolerant(t *testing.T) {
	got := resolveNowTokensInScalar("{{ now - 10 m }}", fixedNow)
	if n, ok := got.(int64); !ok || n != fixedNowMS-10*60*1000 {
		t.Fatalf("whitespace token mis-resolved: %T %v", got, got)
	}
}

func TestResolveNowTokensInScalar_NoToken(t *testing.T) {
	if got := resolveNowTokensInScalar("plain string", fixedNow); got != "plain string" {
		t.Fatalf("non-token string changed: %v", got)
	}
}

func TestResolveNowTokensInText_EmbeddedBecomesDigits(t *testing.T) {
	got := resolveNowTokensInText("ts >= {{now-1h}} AND ts < {{now}}", fixedNow)
	want := "ts >= " +
		itoa(fixedNowMS-60*60*1000) + " AND ts < " + itoa(fixedNowMS)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestResolveNowTokens_ESBodyRangeSlides is the regression for the pinned live
// log panel: an ES range gte authored as a token must resolve to a fresh numeric
// epoch on each query (the bug shipped a frozen absolute gte that never moved).
func TestResolveNowTokens_ESBodyRangeSlides(t *testing.T) {
	in := DataQueryInput{
		HTTPMethod: "POST",
		HTTPPath:   "/_search",
		HTTPBody: map[string]any{
			"size": 0,
			"query": map[string]any{
				"range": map[string]any{
					"__TIMESTAMP__": map[string]any{"gte": "{{now-10m}}"},
				},
			},
		},
	}
	out := resolveNowTokens(in, fixedNow)
	gte := out.HTTPBody["query"].(map[string]any)["range"].(map[string]any)["__TIMESTAMP__"].(map[string]any)["gte"]
	n, ok := gte.(int64)
	if !ok {
		t.Fatalf("gte should be numeric epoch ms after resolution, got %T (%v)", gte, gte)
	}
	if n != fixedNowMS-10*60*1000 {
		t.Fatalf("gte = %d, want %d", n, fixedNowMS-10*60*1000)
	}
}

func TestResolveNowTokens_StatementEmbedded(t *testing.T) {
	in := DataQueryInput{Statement: "SELECT 1 WHERE ts >= {{now-1d}}"}
	out := resolveNowTokens(in, fixedNow)
	want := "SELECT 1 WHERE ts >= " + itoa(fixedNowMS-24*60*60*1000)
	if out.Statement != want {
		t.Fatalf("got %q, want %q", out.Statement, want)
	}
}

func TestResolveNowTokens_NilMapsSafe(t *testing.T) {
	// No panic on a fully-empty input.
	_ = resolveNowTokens(DataQueryInput{}, fixedNow)
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
