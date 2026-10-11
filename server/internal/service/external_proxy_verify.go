// Package service — provider verify / publish / dry-run pre-check
// (Epic #726 / #732). See provider_verify.go for probe selection.
//
// The publish flow is the "verify after publish" half of the pre-check story:
//
//	lint candidate -> select probe -> (human confirm non-GET probe) -> run probe
//	  probe OK   -> persist the candidate contract + enable the provider
//	  probe FAIL -> persist NOTHING (the row keeps its previous, working state
//	                = the rollback) and return next_action + alternatives
//
// PrecheckCall is the "dry run" half: it validates everything the proxy would
// check locally (provider visible, enabled, whitelist hit, credential present,
// params conform to the contract) WITHOUT contacting the upstream, so a paid or
// side-effecting call can be vetted before it is spent.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PrecheckCheck is one line of a dry-run pre-check verdict.
type PrecheckCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// CallPrecheck is the dry_run verdict for call_external_api. It never contacts
// the upstream: every check is resolved from the local store + the provider's
// contract.
type CallPrecheck struct {
	OK         bool            `json:"ok"`
	Provider   string          `json:"provider"`
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	Operation  string          `json:"operation,omitempty"`
	Checks     []PrecheckCheck `json:"checks"`
	Warnings   []string        `json:"warnings,omitempty"`
	NextAction string          `json:"next_action,omitempty"`
}

// ProviderVerifyResult is the outcome of a verify probe or a publish attempt.
type ProviderVerifyResult struct {
	OK       bool   `json:"ok"`
	Provider string `json:"provider"`
	// Published is set by PublishProvider: true only when the candidate contract
	// was persisted AND the provider enabled.
	Published bool `json:"published,omitempty"`
	// RolledBack is set by PublishProvider on failure: true means nothing was
	// persisted, so the provider retains its previous (working) contract/state.
	RolledBack bool `json:"rolled_back,omitempty"`
	// Status is the upstream HTTP status returned by the probe (0 when the probe
	// never reached the upstream).
	Status int             `json:"status,omitempty"`
	Probe  *ProbeManifest  `json:"probe,omitempty"`
	Detail string          `json:"detail,omitempty"`
	Issues []ContractIssue `json:"issues,omitempty"`
	// RequiresConfirmation is set when the probe is not a coarse read-only method
	// (GET/HEAD) and the caller has not confirmed it: the manifest must be shown
	// on the confirmation card and the call repeated with Confirmed=true.
	RequiresConfirmation bool     `json:"requires_confirmation,omitempty"`
	NextAction           string   `json:"next_action,omitempty"`
	Alternatives         []string `json:"alternatives,omitempty"`
}

// VerifyProviderInput identifies the provider to probe. SourceKey applies when
// an in-workspace agent resolves the credential via a project binding.
// Confirmed acknowledges a non-coarse (non-GET) probe shown on the card.
type VerifyProviderInput struct {
	Provider    string
	UserID      int64
	WorkspaceID int64
	SourceKey   string
	Confirmed   bool
}

// PublishProviderInput is a publish request. Profile, when non-nil, is the
// candidate contract JSON to validate + persist; nil re-publishes the stored
// profile. Confirmed acknowledges a non-coarse (non-GET) probe shown on the
// confirmation card.
type PublishProviderInput struct {
	ProviderID  int64
	UserID      int64
	WorkspaceID int64
	SourceKey   string
	Profile     *string
	Confirmed   bool
}

// VerifyProvider runs the provider's verification probe and returns the result.
// It does not change the provider's enabled state.
func (s *ExternalProxyService) VerifyProvider(ctx context.Context, in VerifyProviderInput) (*ProviderVerifyResult, error) {
	prov, err := s.providerSvc.GetByName(ctx, in.Provider)
	if err != nil {
		return nil, fmt.Errorf("provider %q not found: %w", in.Provider, err)
	}
	contract := prov.Contract()
	if res := lintVerifyResult(prov.Name, contract); res != nil {
		return res, nil
	}
	op, source := SelectVerifyOperation(contract)
	if op == nil {
		return noProbeResult(prov.Name), nil
	}
	manifest := BuildProbeManifest(op, source)
	if !manifest.CoarseReadOnly && !in.Confirmed {
		return &ProviderVerifyResult{
			OK:                   false,
			Provider:             prov.Name,
			Probe:                manifest,
			RequiresConfirmation: true,
			Detail: fmt.Sprintf("the probe uses %s, which the coarse read-only check cannot prove safe; show the probe on the confirmation card and approve it",
				manifest.Method),
			NextAction:   "confirm the probe operation, then re-verify with confirm=true",
			Alternatives: []string{"switch verify_operation to a GET operation in the contract"},
		}, nil
	}
	return s.runProbe(ctx, prov, op, manifest, in.UserID, in.WorkspaceID, in.SourceKey), nil
}

// PublishProvider lints + probes a candidate contract and, only on success,
// persists it and enables the provider. On any failure nothing is persisted
// (RolledBack=true) and next_action / alternatives are returned.
func (s *ExternalProxyService) PublishProvider(ctx context.Context, in PublishProviderInput) (*ProviderVerifyResult, error) {
	existing, err := s.providerSvc.GetByID(ctx, in.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("provider %d not found: %w", in.ProviderID, err)
	}
	candidate := existing.Profile
	if in.Profile != nil {
		candidate = *in.Profile
	}
	// Overlay the candidate profile on the row so the derived contract reflects
	// the live wiring (base_url / auth) exactly as the proxy will use it.
	probeDef := *existing
	probeDef.Profile = candidate
	contract := probeDef.Contract()

	if res := lintVerifyResult(existing.Name, contract); res != nil {
		res.RolledBack = true
		return res, nil
	}
	op, source := SelectVerifyOperation(contract)
	if op == nil {
		res := noProbeResult(existing.Name)
		res.RolledBack = true
		return res, nil
	}
	manifest := BuildProbeManifest(op, source)
	if !manifest.CoarseReadOnly && !in.Confirmed {
		return &ProviderVerifyResult{
			OK:                   false,
			Provider:             existing.Name,
			RolledBack:           true,
			Probe:                manifest,
			RequiresConfirmation: true,
			Detail: fmt.Sprintf("the probe uses %s, which the coarse read-only check cannot prove safe; show the probe on the confirmation card and approve it",
				manifest.Method),
			NextAction:   "confirm the probe operation, then re-publish with confirm=true",
			Alternatives: []string{"switch verify_operation to a GET operation in the contract"},
		}, nil
	}

	res := s.runProbe(ctx, &probeDef, op, manifest, in.UserID, in.WorkspaceID, in.SourceKey)
	if !res.OK {
		// Rollback: nothing persisted — the provider keeps its previous contract
		// and enabled state.
		res.RolledBack = true
		return res, nil
	}

	if _, err := s.providerSvc.Update(ctx, existing.ID,
		existing.Label, existing.APIBaseURL, existing.AuthType, existing.AuthHeader,
		existing.AuthPrefix, existing.Whitelist, candidate, existing.OpenAPIURL, true); err != nil {
		return nil, fmt.Errorf("persist published provider: %w", err)
	}
	res.Published = true
	res.Provider = existing.Name
	return res, nil
}

// lintVerifyResult returns a non-nil failure result when the contract has
// blocking lint errors, else nil.
func lintVerifyResult(providerName string, contract *ProviderContract) *ProviderVerifyResult {
	issues := LintContract(contract)
	var blocking []ContractIssue
	for _, iss := range issues {
		if iss.Severity == "error" {
			blocking = append(blocking, iss)
		}
	}
	if len(blocking) == 0 {
		return nil
	}
	return &ProviderVerifyResult{
		OK:         false,
		Provider:   providerName,
		Issues:     issues,
		Detail:     "the contract has blocking lint errors",
		NextAction: "fix the contract errors and retry",
		Alternatives: []string{
			"add a read-only operation and set verify_operation to it",
			"correct the operation method / path / parameter list",
		},
	}
}

// noProbeResult is the verdict when a contract exposes no usable read-only
// operation to probe with.
func noProbeResult(providerName string) *ProviderVerifyResult {
	return &ProviderVerifyResult{
		OK:         false,
		Provider:   providerName,
		Detail:     "the contract has no read-only operation to use as a verification probe",
		NextAction: "add a read-only operation to the contract (or point verify_operation at one)",
		Alternatives: []string{
			"declare a cheap read-only endpoint such as a list/ping operation",
			"mark an existing read-via-POST operation read_only and confirm it has no side effects",
		},
	}
}

// runProbe builds and sends the probe request through the normal proxy path
// (credential resolution, auth injection, SSRF guard, audit) with Probe=true so
// a not-yet-enabled provider is permitted and the write whitelist is skipped.
func (s *ExternalProxyService) runProbe(ctx context.Context, prov *ProviderDef, op *ContractOperation,
	manifest *ProbeManifest, userID, workspaceID int64, sourceKey string) *ProviderVerifyResult {
	res := &ProviderVerifyResult{Provider: prov.Name, Probe: manifest}
	method, path, query, body, err := buildProbeRequest(op)
	if err != nil {
		res.Detail = err.Error()
		res.NextAction = "supply the probe's path parameters in the operation's request_example"
		res.Alternatives = []string{"choose a verify_operation whose path has no placeholders"}
		return res
	}
	out, err := s.Call(ctx, ProxyInput{
		UserID:      userID,
		Provider:    prov.Name,
		Method:      method,
		Path:        path,
		Query:       query,
		Body:        body,
		WorkspaceID: workspaceID,
		SourceKey:   sourceKey,
		Probe:       true,
	})
	if err != nil {
		res.Detail = err.Error()
		res.NextAction = "fix the provider's auth or connectivity, then retry"
		res.Alternatives = []string{
			"check the credential in Settings → Integrations",
			"verify the API base URL is reachable and correct",
			"adjust the probe operation in the contract",
		}
		return res
	}
	res.Status = out.Status
	if out.Status >= 200 && out.Status < 400 {
		res.OK = true
		return res
	}
	res.Detail = probeFailureDetail(out)
	res.NextAction = fmt.Sprintf("the probe returned HTTP %d — check the credential, base URL and probe operation", out.Status)
	res.Alternatives = []string{
		"check the credential in Settings → Integrations",
		"pick a different verify_operation in the contract",
	}
	return res
}

// probeFailureDetail extracts a short human-readable message from a failed
// probe response body, falling back to a truncated raw body.
func probeFailureDetail(out *ProxyOutput) string {
	raw := strings.TrimSpace(string(out.Body))
	if raw == "" {
		return ""
	}
	var env struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
		Detail  string          `json:"detail"`
	}
	if err := json.Unmarshal(out.Body, &env); err == nil {
		if env.Message != "" {
			return env.Message
		}
		if env.Detail != "" {
			return env.Detail
		}
		if len(env.Error) > 0 {
			var s string
			if json.Unmarshal(env.Error, &s) == nil && s != "" {
				return s
			}
			var nested struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(env.Error, &nested) == nil && nested.Message != "" {
				return nested.Message
			}
		}
	}
	if len(raw) > 300 {
		raw = raw[:300] + "…"
	}
	return raw
}

// PrecheckCall is the dry_run path for call_external_api: it validates the
// request locally and never contacts the upstream. Returns a verdict even when
// a check fails (OK=false), so the agent can self-correct before spending the
// call. An error is returned only for an unexpected internal failure.
func (s *ExternalProxyService) PrecheckCall(ctx context.Context, in ProxyInput) (*CallPrecheck, error) {
	res := &CallPrecheck{
		Provider: in.Provider,
		Method:   strings.ToUpper(strings.TrimSpace(in.Method)),
		Path:     in.Path,
		OK:       true,
	}
	add := func(name string, ok bool, detail string) {
		res.Checks = append(res.Checks, PrecheckCheck{Name: name, OK: ok, Detail: detail})
		if !ok {
			res.OK = false
		}
	}

	// 1) Provider exists + is enabled.
	prov, err := s.providerSvc.GetByName(ctx, in.Provider)
	if err != nil {
		add("provider_exists", false, fmt.Sprintf("provider %q not found", in.Provider))
		res.NextAction = "call list_providers to see the providers available in this project"
		return res, nil
	}
	add("provider_exists", true, prov.Name)
	add("provider_enabled", prov.Enabled, enabledDetail(prov.Enabled))

	// 2) Resolve the target operation. A call is addressed either by a contract
	// operation name (#737) or by an explicit method+path pair. Resolving first
	// lets the whitelist + parameter checks run against the SAME method/path the
	// proxy will actually use.
	contract := prov.Contract()
	var op *ContractOperation
	if in.Operation != "" {
		o, ok := contract.OperationByName(in.Operation)
		if !ok {
			add("operation_exists", false, fmt.Sprintf("no contract operation named %q", in.Operation))
			res.NextAction = "call get_provider_schema to list the provider's operations"
			return res, nil
		}
		op = o
		add("operation_exists", true, in.Operation)
		res.Operation = op.Name
		res.Method = strings.ToUpper(strings.TrimSpace(op.Method))
		res.Path = op.Path
	} else if o, ok := contract.MatchOperation(in.Method, in.Path); ok {
		op = o
		res.Operation = op.Name
	}

	// 3) Whitelist / write gate (same predicate the proxy falls back to).
	writeEnabled := s.writeEnabledFor(ctx, in.UserID, prov.ID)
	allowed := s.gateAllows(writeEnabled, prov.Whitelist, res.Method, res.Path)
	add("whitelist", allowed, whitelistDetail(allowed, writeEnabled, res.Method, res.Path))

	// 4) Credential resolves (existence only — no upstream call).
	if _, cerr := s.resolveCredential(ctx, in); cerr != nil {
		add("credential", false, cerr.Error())
		if res.NextAction == "" {
			res.NextAction = "bind a credential for this provider before calling it"
		}
	} else {
		add("credential", true, "credential resolved")
	}

	// 5) Parameters conform to the contract (best effort — an unmatched
	// operation is a warning, not a failure, since a contract need not list
	// every path).
	if op != nil {
		missing := missingRequiredParams(op, in.Query, in.Body)
		add("contract_parameters", len(missing) == 0, missingDetail(missing))
		if len(missing) > 0 && res.NextAction == "" {
			res.NextAction = "supply the required parameters listed in the failed check, or call get_provider_schema for the operation"
		}
	} else {
		res.Warnings = append(res.Warnings,
			"no contract operation matches this method+path; parameter conformance was not checked")
	}
	return res, nil
}

// findOperationByName resolves a contract operation by its semantic name.
func findOperationByName(c *ProviderContract, name string) *ContractOperation {
	if c == nil {
		return nil
	}
	for i := range c.Operations {
		if c.Operations[i].Name == name {
			return &c.Operations[i]
		}
	}
	return nil
}

func enabledDetail(enabled bool) string {
	if enabled {
		return "provider is enabled"
	}
	return "provider is disabled"
}

func whitelistDetail(allowed, writeEnabled bool, method, path string) string {
	if allowed {
		return "allowed"
	}
	return fmt.Sprintf("%s %s is not in the default safe set and writes are disabled for this provider", strings.ToUpper(method), path)
}

func missingDetail(missing []string) string {
	if len(missing) == 0 {
		return "all required parameters present"
	}
	return "missing required parameters: " + strings.Join(missing, ", ")
}

// missingRequiredParams lists declared-required query/body parameters the call
// does not supply. Path parameters are already baked into the request path, and
// headers are injected by the proxy, so neither is checked here.
func missingRequiredParams(op *ContractOperation, query, body map[string]any) []string {
	var missing []string
	for _, p := range op.Parameters {
		if !p.Required {
			continue
		}
		switch p.In {
		case "query":
			if _, ok := query[p.Name]; !ok {
				missing = append(missing, p.Name+" (query)")
			}
		case "body":
			if _, ok := body[p.Name]; !ok {
				missing = append(missing, p.Name+" (body)")
			}
		}
	}
	return missing
}
