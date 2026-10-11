package service

// Publish / apply for operation functions.
//
// Functions ride the SAME draft pipeline as the rest of an integration (#734):
// they are carried inside a provider draft's payload, linted and rehearsed when
// the draft is created, shown on the confirmation card, and written into the
// registry only when a human publishes. There is deliberately no separate
// "publish a function" path an agent could take — the authorization boundary is
// the provider draft's, and it already refuses agent-originated publishes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// FunctionDraftPayload is one function as it appears inside a provider draft.
//
// The first block is what a caller may supply. Everything after the marker is
// SERVER-GENERATED and is overwritten on ingest: the rehearsal and the lint
// findings must come from the system so a draft cannot present a flattering
// preview of code that behaves differently (the three-stage rule from #746).
type FunctionDraftPayload struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Runtime     string             `json:"runtime,omitempty"`
	Code        string             `json:"code"`
	Entry       string             `json:"entry,omitempty"`
	Limits      sandbox.Limits     `json:"limits,omitempty"`
	Egress      []sandbox.EgressOp `json:"egress"`
	Idempotent  bool               `json:"idempotent"`
	Enabled     bool               `json:"enabled"`
	// SampleInput is the typical invocation the rehearsal uses. It shapes the
	// rehearsal only: the constraint that actually binds the function at
	// runtime is the declared egress set, not this sample.
	SampleInput map[string]any `json:"sample_input,omitempty"`

	// --- system-generated, never trusted from input ---
	CodeHash     string                `json:"code_hash,omitempty"`
	Lint         []sandbox.LintFinding `json:"lint,omitempty"`
	Rehearsal    *RehearsalReport      `json:"rehearsal,omitempty"`
	ExistingID   int64                 `json:"existing_id,omitempty"`
	PreviousHash string                `json:"previous_hash,omitempty"`
}

// Spec builds the executable spec for a draft entry.
func (f FunctionDraftPayload) Spec() sandbox.Spec {
	return sandbox.Spec{
		Name:        f.Name,
		Description: f.Description,
		Runtime:     f.Runtime,
		Code:        f.Code,
		Entry:       f.Entry,
		Limits:      f.Limits,
		Egress:      f.Egress,
		Idempotent:  f.Idempotent,
	}
}

// FunctionDraftLint is the lint verdict for one draft entry, in the shape the
// draft row's lint_json uses.
type FunctionDraftLint struct {
	Function string                `json:"function"`
	Blocking bool                  `json:"blocking"`
	Findings []sandbox.LintFinding `json:"findings"`
}

// maxFunctionsPerDraft bounds one draft. Each entry costs a real sandboxed
// rehearsal (up to the function's whole time budget), so an unbounded list is a
// cheap way for a caller to make the server run hundreds of boxes.
const maxFunctionsPerDraft = 16

// PrepareFunctionDrafts is the server-side pass that turns caller-supplied
// function bodies into reviewable entries: validate, lint, and rehearse against
// a mock upstream. The rehearsal and lint fields on the way out are the
// system's, whatever the caller sent in.
func (s *OperationFunctionService) PrepareFunctionDrafts(
	ctx context.Context, providerName string, contract *ProviderContract,
	in []FunctionDraftPayload, byUser int64,
) ([]FunctionDraftPayload, []FunctionDraftLint, error) {
	if len(in) > maxFunctionsPerDraft {
		return nil, nil, fmt.Errorf("%w: a draft may carry at most %d functions (got %d)",
			ErrDraftInvalid, maxFunctionsPerDraft, len(in))
	}
	out := make([]FunctionDraftPayload, 0, len(in))
	lints := make([]FunctionDraftLint, 0, len(in))
	seen := map[string]bool{}
	for _, f := range in {
		prepared := FunctionDraftPayload{
			Name:        strings.TrimSpace(f.Name),
			Description: f.Description,
			Runtime:     f.Runtime,
			Code:        f.Code,
			Entry:       f.Entry,
			Limits:      f.Limits,
			Egress:      f.Egress,
			Idempotent:  f.Idempotent,
			Enabled:     f.Enabled,
			SampleInput: f.SampleInput,
		}
		if prepared.Name == "" {
			return nil, nil, fmt.Errorf("%w: every function needs a name", ErrDraftInvalid)
		}
		if seen[prepared.Name] {
			return nil, nil, fmt.Errorf("%w: duplicate function name %q", ErrDraftInvalid, prepared.Name)
		}
		seen[prepared.Name] = true

		spec := prepared.Spec()
		normalized, err := spec.Normalize()
		if err != nil {
			// A body that cannot even be normalized is reported as a blocking
			// lint finding rather than an error, so the review UI can show the
			// reason next to the code instead of failing the whole draft.
			lints = append(lints, FunctionDraftLint{
				Function: prepared.Name,
				Blocking: true,
				Findings: []sandbox.LintFinding{{
					Rule: "invalid_spec", Severity: sandbox.SeverityError, Message: err.Error(),
				}},
			})
			prepared.CodeHash = spec.CodeHash()
			out = append(out, prepared)
			continue
		}
		prepared.Runtime = normalized.Runtime
		prepared.Entry = normalized.Entry
		prepared.CodeHash = normalized.CodeHash()

		findings := sandbox.Lint(normalized.Code)
		if !RuntimeImplementedOK(normalized.Runtime) {
			findings = append(findings, sandbox.LintFinding{
				Rule: "runtime_unavailable", Severity: sandbox.SeverityError,
				Message: fmt.Sprintf("the %q runtime is not available in this build", normalized.Runtime),
			})
		}
		prepared.Lint = findings
		lints = append(lints, FunctionDraftLint{
			Function: prepared.Name,
			Blocking: sandbox.LintBlocks(findings),
			Findings: findings,
		})

		report := s.Rehearse(ctx, RehearseRequest{
			ProviderName: providerName,
			Contract:     contract,
			Spec:         normalized,
			Input:        prepared.SampleInput,
		})
		det := report.Deterministic()
		prepared.Rehearsal = &det
		if !report.OK || len(report.UndeclaredOps) > 0 {
			lints[len(lints)-1].Blocking = true
		}

		// Attach the live counterpart so the card can show "what changed".
		if prov, err := s.providers.GetByName(ctx, providerName); err == nil {
			if existing, err := s.q.GetIntegrationFunctionByName(ctx, store.GetIntegrationFunctionByNameParams{
				ProviderID: prov.ID, Name: prepared.Name,
			}); err == nil {
				prepared.ExistingID = existing.ID
				prepared.PreviousHash = existing.CodeHash
			}
		}
		out = append(out, prepared)
	}
	return out, lints, nil
}

// RuntimeImplementedOK is a small indirection so the publish path and the
// runtime check cannot drift apart.
func RuntimeImplementedOK(name string) bool { return sandbox.RuntimeImplemented(name) }

// FunctionLintBlocks reports whether any per-function lint verdict blocks the
// publish.
func FunctionLintBlocks(lints []FunctionDraftLint) bool {
	for _, l := range lints {
		if l.Blocking {
			return true
		}
	}
	return false
}

// SyncProviderFunctions writes a published draft's functions into the registry
// and appends them to the version chain.
//
// A function that is ABSENT from the payload is disabled rather than deleted:
// the code stays available for rollback and review, but it stops being
// callable and stops projecting an MCP tool. That is the "函数随源启用/停用"
// requirement — the source remains the authority over its functions.
func (s *OperationFunctionService) SyncProviderFunctions(ctx context.Context, ownerType string, ownerID, providerID int64,
	providerName string, byUser, draftID int64, fns []FunctionDraftPayload) error {

	existing, err := s.q.ListIntegrationFunctionsByProvider(ctx, providerID)
	if err != nil {
		return err
	}
	byName := map[string]store.IntegrationFunction{}
	for _, row := range existing {
		byName[row.Name] = row
	}

	for _, f := range fns {
		spec, err := f.Spec().Normalize()
		if err != nil {
			return fmt.Errorf("publish function %q: %w", f.Name, err)
		}
		limitsJSON, _ := json.Marshal(spec.Limits)
		egressJSON, _ := json.Marshal(spec.Egress)
		enabled := int64(0)
		if f.Enabled {
			enabled = 1
		}
		idempotent := int64(0)
		if spec.Idempotent {
			idempotent = 1
		}

		row, exists := byName[f.Name]
		var functionID int64
		if !exists {
			created, err := s.q.CreateIntegrationFunction(ctx, store.CreateIntegrationFunctionParams{
				OwnerType:    ownerType,
				OwnerID:      ownerID,
				ProviderID:   providerID,
				ProviderName: providerName,
				Name:         f.Name,
				Description:  f.Description,
				Runtime:      spec.Runtime,
				Code:         spec.Code,
				CodeHash:     spec.CodeHash(),
				Entry:        spec.Entry,
				LimitsJson:   string(limitsJSON),
				EgressJson:   string(egressJSON),
				Idempotent:   idempotent,
				Enabled:      enabled,
				Version:      1,
				// Every new function starts under observation: its first calls
				// are audited in full before it is treated as routine.
				Status:           FuncObserving,
				ObservationLimit: observationDefaultCalls,
				CreatedBy:        byUser,
			})
			if err != nil {
				return err
			}
			functionID = created.ID
		} else {
			functionID = row.ID
			if err := s.q.ReplaceIntegrationFunctionBody(ctx, store.ReplaceIntegrationFunctionBodyParams{
				Description:      f.Description,
				Runtime:          spec.Runtime,
				Code:             spec.Code,
				CodeHash:         spec.CodeHash(),
				Entry:            spec.Entry,
				LimitsJson:       string(limitsJSON),
				EgressJson:       string(egressJSON),
				Idempotent:       idempotent,
				Version:          row.Version + 1,
				Status:           FuncObserving,
				ObservationCalls: 0,
				ObservationLimit: observationDefaultCalls,
				Enabled:          enabled,
				ID:               row.ID,
			}); err != nil {
				return err
			}
		}

		// Append to the rollback chain and make the new row active.
		latest, err := s.q.LatestIntegrationFunctionVersion(ctx, functionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		scopeJSON, _ := json.Marshal(map[string]any{
			"egress":     spec.Egress,
			"idempotent": spec.Idempotent,
		})
		ver, err := s.q.CreateIntegrationFunctionVersion(ctx, store.CreateIntegrationFunctionVersionParams{
			FunctionID:    functionID,
			OwnerType:     ownerType,
			OwnerID:       ownerID,
			ProviderID:    providerID,
			Name:          f.Name,
			Version:       latest + 1,
			Code:          spec.Code,
			CodeHash:      spec.CodeHash(),
			EgressJson:    string(egressJSON),
			LimitsJson:    string(limitsJSON),
			Idempotent:    idempotent,
			ScopeJson:     string(scopeJSON),
			Active:        1,
			PublishedBy:   byUser,
			SourceDraftID: draftID,
		})
		if err != nil {
			return err
		}
		if err := s.q.DeactivateIntegrationFunctionVersions(ctx, functionID); err != nil {
			return err
		}
		if err := s.q.ActivateIntegrationFunctionVersion(ctx, ver.ID); err != nil {
			return err
		}
		delete(byName, f.Name)
	}

	// Anything left in byName is no longer part of the draft.
	for _, row := range byName {
		if err := s.q.UpdateIntegrationFunctionEnabled(ctx, store.UpdateIntegrationFunctionEnabledParams{
			Enabled: 0, Status: FuncDisabled, ID: row.ID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ProviderFunctionScope is the function-side authority a provider version
// carries. It is folded into the provider draft's scope snapshot so a rollback
// that changes which operations functions may reach is caught by the existing
// "rollback changed scope -> re-confirm" rule.
type ProviderFunctionScope struct {
	Functions []FunctionScopeEntry `json:"functions"`
}

// FunctionScopeEntry is one function's contribution to the scope snapshot.
type FunctionScopeEntry struct {
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Idempotent bool     `json:"idempotent"`
	Egress     []string `json:"egress"`
	CodeHash   string   `json:"code_hash,omitempty"`
}

// FunctionScopeForDraft renders the authority a draft's functions would grant.
func FunctionScopeForDraft(fns []FunctionDraftPayload) ProviderFunctionScope {
	out := ProviderFunctionScope{Functions: []FunctionScopeEntry{}}
	for _, f := range fns {
		entry := FunctionScopeEntry{
			Name:       f.Name,
			Enabled:    f.Enabled,
			Idempotent: f.Idempotent,
			CodeHash:   f.CodeHash,
		}
		for _, e := range f.Egress {
			entry.Egress = append(entry.Egress, e.Key())
		}
		sort.Strings(entry.Egress)
		out.Functions = append(out.Functions, entry)
	}
	sort.Slice(out.Functions, func(i, j int) bool { return out.Functions[i].Name < out.Functions[j].Name })
	return out
}

// FunctionScopeForProvider renders the authority a LIVE provider currently has
// through its functions.
func (s *OperationFunctionService) FunctionScopeForProvider(ctx context.Context, providerID int64) ProviderFunctionScope {
	out := ProviderFunctionScope{Functions: []FunctionScopeEntry{}}
	rows, err := s.q.ListIntegrationFunctionsByProvider(ctx, providerID)
	if err != nil {
		return out
	}
	for _, row := range rows {
		entry := FunctionScopeEntry{
			Name:       row.Name,
			Enabled:    row.Enabled != 0,
			Idempotent: row.Idempotent != 0,
			CodeHash:   row.CodeHash,
		}
		var ops []sandbox.EgressOp
		if json.Unmarshal([]byte(row.EgressJson), &ops) == nil {
			for _, o := range ops {
				entry.Egress = append(entry.Egress, o.Key())
			}
		}
		sort.Strings(entry.Egress)
		out.Functions = append(out.Functions, entry)
	}
	sort.Slice(out.Functions, func(i, j int) bool { return out.Functions[i].Name < out.Functions[j].Name })
	return out
}
