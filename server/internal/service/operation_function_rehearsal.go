package service

// Behaviour rehearsal (Epic #726 / #739).
//
// The publish safety rule for AI-authored code is that a human approves
// BEHAVIOUR, not source. Rehearsal is what makes that literal: the function is
// executed against a MOCK upstream — nothing leaves the machine — and the
// result is a manifest of exactly which calls it would make:
//
//	1. POST /orders/42/cancel   (operation: cancelOrder)
//	   sends:  id, reason
//	2. GET  /orders/42          (operation: getOrder)
//
// Two properties make the artefact trustworthy:
//
//   - The mock resolves method and path from the CONFIRMED contract, so the
//     manifest describes the same request the proxy would build at call time.
//   - The egress allowlist the agent proposes is the authorization object. A
//     rehearsal step that is not covered by the declaration is reported as
//     undeclared and blocks publication, so "what it will do" and "what it is
//     allowed to do" cannot drift apart between review and runtime.
//
// The report is deterministic (no timings, no logs) because the same bytes are
// what the user's approval covers: a nondeterministic report would make every
// approval look stale the moment it was checked.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
)

// RehearsalStep is one call the function would make.
type RehearsalStep struct {
	Seq           int      `json:"seq"`
	Operation     string   `json:"operation"`
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	Resolved      bool     `json:"resolved"`
	QueryFields   []string `json:"query_fields,omitempty"`
	BodyFields    []string `json:"body_fields,omitempty"`
	MockStatus    int      `json:"mock_status"`
	ResponseShape any      `json:"response_shape,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

// RehearsalReport is the artefact a human reviews. DurationMS and Logs are
// stripped before it is embedded in a confirmation card.
type RehearsalReport struct {
	OK            bool            `json:"ok"`
	Error         string          `json:"error,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	Steps         []RehearsalStep `json:"steps"`
	DeclaredOps   []string        `json:"declared_ops"`
	ObservedOps   []string        `json:"observed_ops"`
	UndeclaredOps []string        `json:"undeclared_ops"`
	Output        any             `json:"output,omitempty"`
	Logs          []string        `json:"logs,omitempty"`
	DurationMS    int64           `json:"duration_ms,omitempty"`
	// MockOnly is always true and is stated explicitly so a reader is never in
	// doubt that nothing was sent.
	MockOnly bool `json:"mock_only"`
	// Consistent is the property that matters: every call it made is a call it
	// is allowed to make, and every one resolved against the live contract.
	Consistent bool   `json:"consistent"`
	Manifest   string `json:"manifest"`
	Detail     string `json:"detail,omitempty"`
}

// Deterministic returns the copy that goes on a confirmation card.
func (r RehearsalReport) Deterministic() RehearsalReport {
	out := r
	out.Logs = nil
	out.DurationMS = 0
	return out
}

// RehearseRequest runs one behaviour rehearsal.
type RehearseRequest struct {
	// ProviderName is used only when Contract is nil: it resolves the LIVE
	// provider's contract, for rehearsing a function against an integration that
	// already exists.
	ProviderName string
	// Contract is the contract to rehearse against. A draft MUST pass its own
	// candidate contract: resolvable against the live provider only works when
	// the provider is already published, and the most natural onboarding — one
	// draft that introduces a source AND its fallback function — has no live
	// provider at all. Rehearsing against the candidate is also simply more
	// correct: the function ships with the contract being published, not with
	// whatever is live today.
	Contract *ProviderContract
	Spec     sandbox.Spec
	// Input is the typical invocation the rehearsal uses. A draft supplies it
	// from the operation samples; without one the function is run with an empty
	// input, which still reveals the call sequence whenever the code does not
	// branch on it.
	Input map[string]any
}

// Rehearse executes the function against a mock upstream. It never touches the
// network: the mock resolves operations from the contract and answers with a
// synthesised body.
func (s *OperationFunctionService) Rehearse(ctx context.Context, req RehearseRequest) *RehearsalReport {
	report := &RehearsalReport{MockOnly: true, DeclaredOps: declaredOpKeys(req.Spec)}
	contract := req.Contract
	if contract == nil {
		prov, err := s.providers.GetByName(ctx, req.ProviderName)
		if err != nil {
			report.Error = fmt.Sprintf("provider %q not found: %v", req.ProviderName, err)
			report.ErrorCode = "provider_not_found"
			report.Manifest = "The rehearsal could not run: the provider is not configured."
			return report
		}
		contract = prov.Contract()
	}
	if contract == nil {
		report.Error = "the draft carries no contract to rehearse against"
		report.ErrorCode = "contract_missing"
		report.Manifest = "The rehearsal could not run: no contract was supplied."
		return report
	}

	var steps []RehearsalStep
	var observed []string
	mock := func(_ context.Context, m *sandbox.HTTPRequestMessage) *sandbox.HTTPResponseMessage {
		// Record the observation in the SAME key space as DeclaredOps
		// (EgressOp.Key). Comparing a bare operation name against a
		// "operation:<name>" key made every function look like it had reached
		// something undeclared, which blocked every publish.
		observed = append(observed, sandbox.EgressOp{Operation: m.Operation}.Key())
		step := RehearsalStep{
			Seq:         len(steps) + 1,
			Operation:   m.Operation,
			QueryFields: sortedFieldNames(m.Query),
			BodyFields:  sortedFieldNames(m.Body),
		}
		op, found := contract.OperationByName(m.Operation)
		if !found {
			step.Notes = append(step.Notes,
				"the provider contract declares no operation with this name; a real call would be refused")
			steps = append(steps, step)
			return &sandbox.HTTPResponseMessage{OK: false,
				Error: fmt.Sprintf("unknown operation %q", m.Operation)}
		}
		step.Method = strings.ToUpper(strings.TrimSpace(op.Method))
		path, _, perr := op.ResolvePath(copyAnyMap(m.Query), copyAnyMap(m.Body))
		if perr != nil {
			step.Notes = append(step.Notes, perr.Error())
		} else {
			step.Path = path
			step.Resolved = true
		}
		step.MockStatus = 200
		step.ResponseShape = synthesizeFromShape(op.ResponseShape)
		steps = append(steps, step)
		return &sandbox.HTTPResponseMessage{
			OK:     true,
			Status: 200,
			Body:   jsonOrNull(step.ResponseShape),
		}
	}

	input := req.Input
	if input == nil {
		input = map[string]any{}
	}
	res := sandbox.Run(ctx, sandbox.RunRequest{Spec: req.Spec, Input: input, Egress: mock})

	report.Steps = steps
	report.ObservedOps = dedupe(observed)
	report.Output = res.Output
	report.Logs = res.Logs
	report.DurationMS = res.DurationMS
	report.OK = res.OK
	if res.Error != nil {
		report.Error = res.Error.Message
		report.ErrorCode = res.Error.Code
	}
	report.UndeclaredOps = difference(report.ObservedOps, report.DeclaredOps)
	consistent := len(report.UndeclaredOps) == 0 && report.OK
	for _, st := range report.Steps {
		if !st.Resolved {
			consistent = false
		}
	}
	report.Consistent = consistent
	report.Manifest = renderManifest(report)
	switch {
	case !report.OK:
		report.Detail = "the function did not complete during rehearsal; publishing is blocked until it does"
	case len(report.UndeclaredOps) > 0:
		report.Detail = "the function tried to call an operation it does not declare; publishing is blocked"
	case !consistent:
		report.Detail = "at least one call could not be resolved against the provider contract"
	default:
		report.Detail = "every call the function makes is one it declares, and each resolves against the live contract"
	}
	return report
}

// renderManifest is the human-readable "will send" text. It is written for a
// person deciding whether to approve, so it names the method, the path and the
// request fields — never a secret, which the box never sees anyway.
func renderManifest(r *RehearsalReport) string {
	var b strings.Builder
	b.WriteString("Behaviour rehearsal against a mock upstream — nothing was sent.\n")
	if len(r.Steps) == 0 {
		b.WriteString("The function made no outbound call.\n")
	}
	for _, st := range r.Steps {
		line := fmt.Sprintf("%d. %s %s", st.Seq, st.Method, st.Path)
		if st.Method == "" {
			line = fmt.Sprintf("%d. %s", st.Seq, st.Operation)
		}
		if st.Operation != "" {
			line += fmt.Sprintf("   (operation: %s)", st.Operation)
		}
		b.WriteString(line + "\n")
		var sends []string
		for _, f := range st.QueryFields {
			sends = append(sends, f+" (query)")
		}
		sends = append(sends, st.BodyFields...)
		if len(sends) > 0 {
			b.WriteString("   sends: " + strings.Join(sends, ", ") + "\n")
		}
		for _, n := range st.Notes {
			b.WriteString("   note: " + n + "\n")
		}
	}
	if len(r.DeclaredOps) > 0 {
		b.WriteString("Allowed operations: " + strings.Join(r.DeclaredOps, ", ") + "\n")
	}
	if len(r.UndeclaredOps) > 0 {
		b.WriteString("NOT ALLOWED but attempted: " + strings.Join(r.UndeclaredOps, ", ") + "\n")
	}
	if r.Error != "" {
		b.WriteString("Failed: " + r.Error + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// synthesizeFromShape produces a plausible response body from a contract's
// declared response shape, so the function's data handling is exercised rather
// than short-circuited by a null.
//
// A contract's response_shape is a compact HINT, usually written as JSON (e.g.
// `{"orders":[{"id":"1"}]}`). Parsing it is what makes the rehearsal useful: a
// function that reads res.body.orders must be handed an object, not the hint
// text, or the rehearsal fails for a reason that has nothing to do with the
// function's behaviour in production.
func synthesizeFromShape(shape any) any {
	switch v := shape.(type) {
	case nil:
		return map[string]any{"ok": true}
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = synthesizeFromShape(val)
		}
		return out
	case []any:
		if len(v) == 0 {
			return []any{}
		}
		return []any{synthesizeFromShape(v[0])}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return map[string]any{"ok": true}
		}
		var parsed any
		if json.Unmarshal([]byte(trimmed), &parsed) == nil {
			return parsed
		}
		// A prose hint cannot be indexed into, but the call still needs a body
		// the function can survive.
		return map[string]any{"ok": true}
	default:
		return v
	}
}

func declaredOpKeys(spec sandbox.Spec) []string {
	out := make([]string, 0, len(spec.Egress))
	for _, e := range spec.Egress {
		out = append(out, e.Key())
	}
	sort.Strings(out)
	return out
}

func sortedFieldNames(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func difference(all, declared []string) []string {
	set := map[string]bool{}
	for _, d := range declared {
		set[d] = true
	}
	var out []string
	for _, a := range all {
		if !set[a] {
			out = append(out, a)
		}
	}
	return out
}

func jsonOrNull(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}
