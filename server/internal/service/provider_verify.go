// Package service — provider verification probes (Epic #726 / #732).
//
// After a provider contract is published, niuniu runs a cheap, idempotent,
// read-only PROBE against the provider to prove the credential + connectivity
// actually work before the integration goes live. This file owns:
//
//   - SelectVerifyOperation — picks the probe operation. It prefers the
//     contract's declared verify_operation; when that is unset it defaults to
//     the LEAST-HARMFUL read-only operation (GET over HEAD over a read-via-POST,
//     no path placeholders, fewest required params) so an AI-authored contract
//     never probes with something that could mutate state.
//   - ProbeManifest — the human-visible description of exactly which request
//     the probe will send. It is returned in every verify/publish response and
//     embedded in the confirmation card, so a probe can never be silently
//     authored into something dangerous.
//   - buildProbeRequest — turns a probe operation + its request_example into a
//     concrete HTTP request.
//
// On publish: probe OK -> the provider is enabled; probe fails -> the candidate
// contract is NOT persisted (the row keeps its previous, working state — the
// rollback) and the caller gets next_action / alternatives.
package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// pathPlaceholderRe matches contract path placeholders, e.g. "{siteUrl}".
var pathPlaceholderRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// ProbeManifest is the human-visible description of a verification probe. It is
// surfaced on the confirmation card and in every verify/publish response so a
// person (and the agent) can see exactly which method+path the probe will hit
// before it runs. CoarseReadOnly restates the LINT-level judgement (GET/HEAD);
// it is deliberately NOT a safety guarantee — a GET can still have side effects
// (e.g. GET /admin/delete?confirm=1), which is why a non-GET probe is gated on
// explicit human confirmation.
type ProbeManifest struct {
	Operation      string          `json:"operation"`
	Method         string          `json:"method"`
	Path           string          `json:"path"`
	ReadOnly       bool            `json:"read_only"`
	CoarseReadOnly bool            `json:"coarse_read_only"`
	Source         string          `json:"source"` // "verify_operation" | "default_least_harmful"
	Parameters     []ContractParam `json:"parameters,omitempty"`
	Warnings       []string        `json:"warnings,omitempty"`
}

// coarseReadOnly is the lint-level read-only test: the method is GET or HEAD.
// It is a coarse filter only (some APIs read via POST; some GETs mutate).
func coarseReadOnly(method string) bool {
	return readOnlyMethods[strings.ToUpper(strings.TrimSpace(method))]
}

// isWriteMethod reports whether method can change upstream state. Used to mark
// an ambiguous transport failure on a write as outcome_unknown (never blindly
// replayable) while a failed read stays a plain error.
func isWriteMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// probeScore orders candidate probe operations; lower is less harmful. The
// fields are compared in order: method rank (GET < HEAD < other), then path
// placeholders (fewer is better — they need substitute values), then required
// parameters, then total parameters, then name (for determinism).
type probeScore struct {
	methodRank     int
	pathPlaceholds int
	requiredParams int
	totalParams    int
	name           string
}

func (a probeScore) less(b probeScore) bool {
	if a.methodRank != b.methodRank {
		return a.methodRank < b.methodRank
	}
	if a.pathPlaceholds != b.pathPlaceholds {
		return a.pathPlaceholds < b.pathPlaceholds
	}
	if a.requiredParams != b.requiredParams {
		return a.requiredParams < b.requiredParams
	}
	if a.totalParams != b.totalParams {
		return a.totalParams < b.totalParams
	}
	return a.name < b.name
}

// scoreProbe builds the ordering key for one operation.
func scoreProbe(op *ContractOperation) probeScore {
	method := strings.ToUpper(strings.TrimSpace(op.Method))
	rank := 2
	switch method {
	case "GET":
		rank = 0
	case "HEAD":
		rank = 1
	}
	required := 0
	for _, p := range op.Parameters {
		if p.Required {
			required++
		}
	}
	return probeScore{
		methodRank:     rank,
		pathPlaceholds: len(pathPlaceholderRe.FindAllString(op.Path, -1)),
		requiredParams: required,
		totalParams:    len(op.Parameters),
		name:           op.Name,
	}
}

// SelectVerifyOperation chooses the probe operation for a contract. It returns
// the operation and its provenance ("verify_operation" when the contract
// declared one, "default_least_harmful" when the least-harmful read-only
// operation was auto-selected), or (nil, "") when the contract has no usable
// read-only operation.
//
// The contract's explicit verify_operation wins even if a less harmful
// operation exists — the author may know the cheapest authenticated endpoint
// (e.g. a dedicated /ping). Lint already requires that declared operation to be
// read-only.
func SelectVerifyOperation(c *ProviderContract) (*ContractOperation, string) {
	if c == nil {
		return nil, ""
	}
	if c.VerifyOperation != "" {
		for i := range c.Operations {
			if c.Operations[i].Name == c.VerifyOperation {
				return &c.Operations[i], "verify_operation"
			}
		}
		return nil, ""
	}
	best := -1
	var bestScore probeScore
	for i := range c.Operations {
		op := &c.Operations[i]
		if !op.ReadOnly {
			continue
		}
		sc := scoreProbe(op)
		if best < 0 || sc.less(bestScore) {
			best, bestScore = i, sc
		}
	}
	if best < 0 {
		return nil, ""
	}
	return &c.Operations[best], "default_least_harmful"
}

// BuildProbeManifest describes a selected probe for display + confirmation.
func BuildProbeManifest(op *ContractOperation, source string) *ProbeManifest {
	m := &ProbeManifest{
		Operation:      op.Name,
		Method:         strings.ToUpper(strings.TrimSpace(op.Method)),
		Path:           op.Path,
		ReadOnly:       op.ReadOnly,
		CoarseReadOnly: coarseReadOnly(op.Method),
		Source:         source,
		Parameters:     op.Parameters,
	}
	if !m.CoarseReadOnly {
		m.Warnings = append(m.Warnings,
			fmt.Sprintf("probe uses %s — the read-only check is coarse (a non-GET call can have side effects); confirm it is safe before publishing", m.Method))
	}
	if n := len(pathPlaceholderRe.FindAllString(op.Path, -1)); n > 0 {
		m.Warnings = append(m.Warnings,
			fmt.Sprintf("probe path has %d placeholder(s); request_example must supply values for them", n))
	}
	return m
}

// buildProbeRequest turns a probe operation + the contract's request_example
// into a concrete HTTP request. Path placeholders are substituted from the
// example; for coarse-read-only methods the example keys become query params,
// otherwise they become the JSON body. An error is returned when a path
// placeholder has no value in the example (the probe cannot be built safely).
func buildProbeRequest(op *ContractOperation) (method, path string, query, body map[string]any, err error) {
	method = strings.ToUpper(strings.TrimSpace(op.Method))
	var example map[string]any
	if len(op.RequestExample) > 0 {
		// Best-effort: a non-object example is simply ignored (no params).
		_ = json.Unmarshal(op.RequestExample, &example)
	}
	path = op.Path
	for _, m := range pathPlaceholderRe.FindAllStringSubmatch(op.Path, -1) {
		name := m[1]
		v, ok := example[name]
		if !ok {
			return "", "", nil, nil, fmt.Errorf(
				"verify probe %q needs a value for path parameter {%s}: add it to the operation's request_example",
				op.Name, name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(fmt.Sprintf("%v", v)))
	}
	if len(example) > 0 {
		if coarseReadOnly(method) {
			query = example
		} else {
			body = example
		}
	}
	return method, path, query, body, nil
}
