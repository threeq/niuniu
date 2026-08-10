package service

import (
	"regexp"
	"strconv"
	"time"
)

// Relative-time tokens for LIVE saved queries / dashboard panels.
//
// A pinned "live" panel re-runs its saved operation verbatim on every dashboard
// open. If the operation carries an ABSOLUTE time bound captured at pin time
// (e.g. an ES range {"gte": 1782305160000}), the window never advances, so the
// panel keeps returning the same frozen slice — "live" in name only. Engine-side
// date math is not always available either: a numeric epoch-millis field (common
// on Tencent CLS / log pipelines) rejects ES `now-10m` because the field is a
// long, not a date.
//
// So the server resolves a small set of `{{now}}` tokens into a concrete epoch
// MILLISECOND value at query time, across the operation's structured fields. The
// agent authors a sliding window once (e.g. {"gte": "{{now-10m}}"}) and every
// re-query gets a fresh bound. Tokens are resolved on EVERY data-proxy query
// (interactive run_data_query and panel re-run alike), so the value the agent
// sees and the value the panel re-runs are computed the same way.
//
// Grammar: {{now}}, {{now-<N><unit>}}, {{now+<N><unit>}} with unit ∈
// {ms,s,m,h,d} (m = minute, d = day). Whitespace inside the braces is tolerated.
var nowTokenRe = regexp.MustCompile(`\{\{\s*now\s*(?:([+-])\s*(\d+)\s*(ms|s|m|h|d))?\s*\}\}`)

// unitToMillis maps a token unit to milliseconds. An unknown unit yields 0,
// which the regex prevents from ever happening.
func unitToMillis(u string) int64 {
	switch u {
	case "ms":
		return 1
	case "s":
		return 1000
	case "m":
		return 60 * 1000
	case "h":
		return 60 * 60 * 1000
	case "d":
		return 24 * 60 * 60 * 1000
	default:
		return 0
	}
}

// evalNowToken computes the epoch-millisecond value for one matched token given
// its sign/number/unit submatches (empty for a bare {{now}}).
func evalNowToken(now time.Time, sign, num, unit string) int64 {
	base := now.UnixMilli()
	if num == "" {
		return base
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return base
	}
	delta := n * unitToMillis(unit)
	if sign == "-" {
		return base - delta
	}
	return base + delta
}

// resolveNowTokensValue deep-walks a JSON-shaped value (string / map / slice)
// replacing {{now...}} tokens. A string that is EXACTLY one token becomes an
// int64 epoch-ms so a numeric field (e.g. an ES long timestamp) receives a JSON
// number; a token embedded in a larger string is replaced in place by its
// decimal digits so SQL / text contexts keep working. Maps and slices are
// mutated in place (they are freshly parsed per request) and returned.
func resolveNowTokensValue(v any, now time.Time) any {
	switch t := v.(type) {
	case string:
		return resolveNowTokensInScalar(t, now)
	case map[string]any:
		for k, e := range t {
			t[k] = resolveNowTokensValue(e, now)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = resolveNowTokensValue(e, now)
		}
		return t
	default:
		return v
	}
}

// resolveNowTokensInScalar resolves tokens in a single string value. When the
// whole string is exactly one token it returns an int64 (numeric substitution);
// otherwise it returns a string with every token expanded to its decimal digits.
func resolveNowTokensInScalar(s string, now time.Time) any {
	if s == "" {
		return s
	}
	if m := nowTokenRe.FindStringSubmatch(s); m != nil && nowTokenRe.FindString(s) == s {
		// Exact single-token string -> numeric epoch ms.
		return evalNowToken(now, m[1], m[2], m[3])
	}
	return resolveNowTokensInText(s, now)
}

// resolveNowTokensInText replaces every token inside a free-text string (e.g. a
// SQL statement) with its decimal-digit epoch-ms value, leaving the rest intact.
func resolveNowTokensInText(s string, now time.Time) string {
	if s == "" {
		return s
	}
	return nowTokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		m := nowTokenRe.FindStringSubmatch(tok)
		return strconv.FormatInt(evalNowToken(now, m[1], m[2], m[3]), 10)
	})
}

// resolveNowTokens returns a copy of in with {{now...}} tokens resolved across
// every field that can carry a user-supplied time bound. Scalar string fields
// (Statement, ES/HTTP path) keep their type; structured maps/slices may turn an
// exact-token string into a JSON number. Called once at the top of Query so the
// resolved values flow through classify, scope, summary, audit and execution.
func resolveNowTokens(in DataQueryInput, now time.Time) DataQueryInput {
	in.Statement = resolveNowTokensInText(in.Statement, now)
	for i := range in.Params {
		in.Params[i] = resolveNowTokensValue(in.Params[i], now)
	}
	for i := range in.Args {
		in.Args[i] = resolveNowTokensInText(in.Args[i], now)
	}
	in.Filter = resolveNowTokensMap(in.Filter, now)
	in.Document = resolveNowTokensMap(in.Document, now)
	in.Query = resolveNowTokensMap(in.Query, now)
	in.HTTPQuery = resolveNowTokensMap(in.HTTPQuery, now)
	in.HTTPBody = resolveNowTokensMap(in.HTTPBody, now)
	for i := range in.Pipeline {
		in.Pipeline[i] = resolveNowTokensMap(in.Pipeline[i], now)
	}
	return in
}

// resolveNowTokensMap resolves tokens within a map (nil-safe), returning the
// same map for chaining.
func resolveNowTokensMap(m map[string]any, now time.Time) map[string]any {
	if m == nil {
		return nil
	}
	resolveNowTokensValue(m, now)
	return m
}
