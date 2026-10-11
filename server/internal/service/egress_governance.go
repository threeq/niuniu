// Package service — egress data governance (Epic #726 / #747).
//
// The whitelist and the data-source scope answer WHICH call may be made. They
// say nothing about WHAT data the call carries: an agent (or a sandbox
// function) can POST the contents of a local file to a perfectly authorized
// endpoint. This file is the "what" half of egress governance:
//
//   - EgressDataSummary is a VALUE-FREE description of an outbound body — byte
//     size, top-level field NAMES, and which sensitive patterns matched (by
//     category and field path, never the value). It feeds the write-preview /
//     confirmation card ("将发送的数据摘要") and the threshold alert.
//   - EgressPolicy is the seam a team edition implements to block egress
//     (allowed write sources, a size ceiling, sensitive-pattern blocking —
//     issue #740). The personal edition ships DefaultEgressPolicy, which only
//     ever WARNS: it flags an over-threshold or sensitive-looking body and
//     raises a notification, and never blocks a call. Warning is the default
//     disposition; blocking is opt-in at the organization level.
//
// Everything here is derived from the caller's own request; nothing is
// persisted except what the audit row already records (hash + bytes + field
// names), so a summary can be shown to the human who is about to approve a
// write without the approval surface becoming a second copy of the payload.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DefaultEgressWarnBytes is the personal-edition size ceiling for a single
// egress body. A body above it is flagged (audit + notification) but still
// sent: this is a "批量外泄" tripwire, not a gate. Team editions override it
// through EgressPolicy (issue #740).
const DefaultEgressWarnBytes int64 = 1 << 20 // 1 MiB

// maxSensitiveWhere caps how many field paths a single hit records, so the
// summary stays a summary. This bounds REPORTING only — never detection: the
// walk always visits every node and every value, so a sensitive value can
// never hide behind a count/path cap.
const maxSensitiveWhere = 5

// EgressSensitiveHit is one sensitive-pattern category that matched in an
// outbound body. It records the category, how many times it matched, and up to
// maxSensitiveWhere field PATHS (dotted keys) — never the matched value.
type EgressSensitiveHit struct {
	Pattern  string   `json:"pattern"`
	Severity string   `json:"severity"`
	Count    int      `json:"count"`
	Where    []string `json:"where,omitempty"`
}

// EgressDataSummary is the value-free "data summary" of one outbound body.
// Fields are top-level JSON key names; SensitiveHits describe pattern matches
// by category. No field value and no matched substring is ever carried here.
type EgressDataSummary struct {
	Bytes           int64                `json:"bytes"`
	Fields          []string             `json:"fields"`
	FieldsTruncated bool                 `json:"fields_truncated,omitempty"`
	SensitiveHits   []EgressSensitiveHit `json:"sensitive_hits,omitempty"`
}

// HasSensitive reports whether any sensitive pattern matched.
func (s EgressDataSummary) HasSensitive() bool { return len(s.SensitiveHits) > 0 }

// sensitiveRule is one value-free detector. A rule fires either on a field
// NAME/KEY (keyRe — e.g. a key literally named "password") or on a string
// VALUE's shape (valRe — e.g. an email-looking value). Only the category is
// reported, so a match can never smuggle a value into the summary.
type sensitiveRule struct {
	name     string
	severity string
	keyRe    *regexp.Regexp
	valRe    *regexp.Regexp
}

// mustRe compiles a rule pattern; the table is package-level and validated by
// its own test, so a compile failure is a programming error, not a runtime one.
func mustRe(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// The rule table. Patterns are deliberately few and high-signal: false
// positives cost a warning line, false negatives cost a missed tripwire, so
// the value patterns are conservative (a category is only reported when the
// shape is unmistakable).
var sensitiveRules = []sensitiveRule{
	{
		name:     "credential",
		severity: "high",
		keyRe:    mustRe(`(?i)(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|credential|cookie|session[_-]?id|authorization)`),
		valRe:    mustRe(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	},
	{
		name:     "payment_card",
		severity: "high",
		valRe:    mustRe(`\b(?:\d[ -]?){13,19}\b`),
	},
	{
		name:     "national_id",
		severity: "high",
		valRe:    mustRe(`\b\d{17}[\dXx]\b`),
	},
	{
		name:     "email",
		severity: "medium",
		valRe:    mustRe(`[\w.+-]+@[\w-]+\.[\w.-]+`),
	},
	{
		name:     "phone",
		severity: "medium",
		valRe:    mustRe(`(?:^|\D)(?:\+?86)?1[3-9]\d{9}(?:\D|$)`),
	},
}

// SummarizeEgressBody builds a value-free summary of raw (the marshaled request
// body). A nil/empty body yields a zero summary with an empty field list.
//
// Detection is COMPLETE — every node and every string value is scanned, and a
// non-JSON body is scanned as raw text — because a tripwire that skips part of
// the payload is not a tripwire. Only the reported locator list is capped
// (maxSensitiveWhere). The patterns are RE2 (linear time, no backtracking), so
// a full scan is O(body size), never a CPU sink.
func SummarizeEgressBody(raw []byte) EgressDataSummary {
	s := EgressDataSummary{Bytes: int64(len(raw)), Fields: []string{}}
	if len(raw) == 0 {
		return s
	}
	s.Fields, s.FieldsTruncated = topLevelFieldNames(raw)

	hits := map[string]*EgressSensitiveHit{}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not JSON (a form-encoded or plain-text payload, or a body too deeply
		// nested for the decoder). Content detection still runs over the raw
		// bytes — there are no key names to match, so only the value patterns
		// apply — so a secret cannot slip through merely by not being JSON.
		scanSensitiveString(string(raw), "", hits)
	} else {
		scanSensitiveValue(v, "", hits)
	}
	s.SensitiveHits = sortedSensitiveHits(hits)
	return s
}

// SummarizeEgressPayload marshals an arbitrary payload (a body map, a redis
// arg list, a query document) and summarizes it. A nil payload yields a zero
// summary.
func SummarizeEgressPayload(payload any) EgressDataSummary {
	if payload == nil {
		return EgressDataSummary{Fields: []string{}}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return EgressDataSummary{Fields: []string{}}
	}
	return SummarizeEgressBody(raw)
}

// topLevelFieldNames returns the sorted top-level JSON object keys of raw, and
// whether the list was truncated to the audit bound. A non-object body yields
// no names (the same tolerance fingerprintBody applies to the audit column).
func topLevelFieldNames(raw []byte) (fields []string, truncated bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return []string{}, false
	}
	fields = make([]string, 0, len(obj))
	for k := range obj {
		fields = append(fields, k)
	}
	sortStrings(fields)
	if len(fields) > maxAuditBodyFields {
		fields = fields[:maxAuditBodyFields]
		truncated = true
	}
	return fields, truncated
}

// scanSensitiveValue walks a decoded JSON value, accumulating per-category
// hits. `path` is the dotted key path used only as a human-readable locator —
// no value is recorded. Every node is visited (no node budget), so detection
// cannot be skipped by burying the payload.
func scanSensitiveValue(node any, path string, hits map[string]*EgressSensitiveHit) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			for i := range sensitiveRules {
				r := &sensitiveRules[i]
				if r.keyRe != nil && r.keyRe.MatchString(k) {
					recordSensitiveHit(hits, r, childPath)
				}
			}
			scanSensitiveValue(child, childPath, hits)
		}
	case []any:
		for i, child := range v {
			// Index is a locator only; it is not a key name, so no keyRe runs here.
			scanSensitiveValue(child, fmt.Sprintf("%s[%d]", path, i), hits)
		}
	case string:
		scanSensitiveString(v, path, hits)
	}
}

// scanSensitiveString runs every value rule over s in full — no prefix
// truncation, so a secret appended past any fixed offset is still caught.
func scanSensitiveString(s, path string, hits map[string]*EgressSensitiveHit) {
	for i := range sensitiveRules {
		r := &sensitiveRules[i]
		if r.valRe != nil && r.valRe.MatchString(s) {
			recordSensitiveHit(hits, r, path)
		}
	}
}

// recordSensitiveHit accumulates one match into hits[pattern], de-duplicating
// the Where list and capping it at maxSensitiveWhere. Count is the number of
// matches, so a body with 50 token-shaped keys reports count 50.
func recordSensitiveHit(hits map[string]*EgressSensitiveHit, r *sensitiveRule, where string) {
	h := hits[r.name]
	if h == nil {
		h = &EgressSensitiveHit{Pattern: r.name, Severity: r.severity}
		hits[r.name] = h
	}
	h.Count++
	if where == "" {
		return
	}
	for _, w := range h.Where {
		if w == where {
			return
		}
	}
	if len(h.Where) < maxSensitiveWhere {
		h.Where = append(h.Where, where)
	}
}

// sortedSensitiveHits flattens the hit map into a deterministic order (high
// severity first, then category name) so a summary — and any snapshot of it —
// is stable.
func sortedSensitiveHits(hits map[string]*EgressSensitiveHit) []EgressSensitiveHit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]EgressSensitiveHit, 0, len(hits))
	for _, h := range hits {
		sort.Strings(h.Where)
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) < severityRank(out[j].Severity)
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}

func severityRank(s string) int {
	if strings.EqualFold(s, "high") {
		return 0
	}
	return 1
}

// EgressDecision is a policy verdict. `warn` means "send it, but flag it";
// `block` means "refuse" (team edition only).
type EgressDecision string

const (
	EgressAllow EgressDecision = "allow"
	EgressWarn  EgressDecision = "warn"
	EgressBlock EgressDecision = "block"
)

// EgressVerdict is the policy's answer for one request. Reasons are
// human-readable, value-free explanations surfaced to the user.
type EgressVerdict struct {
	Decision EgressDecision `json:"decision"`
	Reasons  []string       `json:"reasons,omitempty"`
}

// EgressRequest is the value-free description of one outbound call the policy
// is asked to judge. It carries the summary (never the payload), so a policy —
// including a remote one — can decide without seeing the data.
type EgressRequest struct {
	UserID     int64
	Provider   string
	ProviderID int64
	Method     string
	Path       string
	Summary    EgressDataSummary
}

// EgressPolicy is the ORGANIZATION-LEVEL egress-policy seam (Epic #726 / #747,
// strategy implemented in the team edition #740). Implementations decide
// whether an outbound write may go, and at what disposition:
//
//   - allow — send, no flag.
//   - warn  — send, flag the audit row and notify the owner.
//   - block — refuse the call (returned to the caller as a refusal).
//
// The personal edition ships DefaultEgressPolicy (warn-only). A team edition
// supplies its own policy (allowed write sources, size ceiling, sensitive
// blocking) and wires it with ExternalProxyService.SetEgressPolicy — no call
// path changes, because the decision is applied at the single egress point.
type EgressPolicy interface {
	Evaluate(ctx context.Context, req EgressRequest) EgressVerdict
}

// egressThresholder is an optional extension: a policy that exposes its numeric
// warn threshold so the audit READ side can flag over-threshold rows with the
// same limit the write side warned at (the audit row itself does not persist a
// flag — the byte count is persisted, and the flag is derived).
type egressThresholder interface {
	WarnBytes() int64
}

// egressWarnBytes returns the effective warn threshold for a policy, falling
// back to the personal-edition default.
func egressWarnBytes(p EgressPolicy) int64 {
	if t, ok := p.(egressThresholder); ok {
		if n := t.WarnBytes(); n > 0 {
			return n
		}
	}
	return DefaultEgressWarnBytes
}

// DefaultEgressPolicy is the personal-edition policy: it WARNS (over-threshold
// size, sensitive-pattern match) and never blocks. MaxBytes <= 0 selects
// DefaultEgressWarnBytes.
type DefaultEgressPolicy struct {
	MaxBytes int64
}

// WarnBytes reports the effective size ceiling.
func (p DefaultEgressPolicy) WarnBytes() int64 {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return DefaultEgressWarnBytes
}

// Evaluate flags an over-threshold body and any sensitive-pattern match. It
// never returns block — the personal edition must not break a workflow on a
// heuristic.
func (p DefaultEgressPolicy) Evaluate(_ context.Context, req EgressRequest) EgressVerdict {
	limit := p.WarnBytes()
	var reasons []string
	if req.Summary.Bytes > limit {
		reasons = append(reasons, fmt.Sprintf(
			"outbound body is %d bytes, over the %d-byte warning threshold", req.Summary.Bytes, limit))
	}
	for _, h := range req.Summary.SensitiveHits {
		reasons = append(reasons, fmt.Sprintf(
			"sensitive pattern %q matched %d time(s)", h.Pattern, h.Count))
	}
	if len(reasons) == 0 {
		return EgressVerdict{Decision: EgressAllow}
	}
	return EgressVerdict{Decision: EgressWarn, Reasons: reasons}
}

// EgressAlert is what an EgressAlertNotifier receives when a policy returns a
// non-allow verdict. It is value-free (it embeds the summary, never the body).
type EgressAlert struct {
	OwnerType  string
	OwnerID    int64
	UserID     int64
	Provider   string
	ProviderID int64
	Method     string
	Path       string
	Summary    EgressDataSummary
	Verdict    EgressVerdict
}

// EgressAlertNotifier delivers egress alerts (station-in hub + best-effort IM
// push). Wired in server.go so this package stays free of the notify hub. A nil
// notifier means alerts are dropped (tests that do not exercise alerting).
type EgressAlertNotifier interface {
	NotifyEgressAlert(ctx context.Context, alert EgressAlert)
}
