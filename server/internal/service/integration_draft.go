// Package service — the integration draft pipeline (Epic #726 / #734).
//
//	draft -> lint -> impact confirmation -> publish -> verify / rollback
//
// Both onboarding lines share it: a Provider draft (contract + whitelist +
// credential field list) and a data-source draft (config + scope_config +
// access mode + credential field list).
//
// Three properties carry the design:
//
//  1. A draft is NOT live. Creating one changes nothing about the running
//     integration — it writes a row. That is also why an interrupted agent is
//     harmless: the candidate lives in the database, not in a conversation.
//  2. The decision surface is system-rendered. The confirmation card is built
//     by BuildConfirmCard (integration_confirm.go) from the draft's STRUCTURED
//     fields only; the draft's own prose has no path into a decision field.
//  3. Authorization is human-only. An agent may draft and request; it may never
//     publish, widen scope, or turn on writes. Enforced here (agent_authored +
//     the caller's origin), not by hiding a button.
//
// Publish applies the candidate with the entity DISABLED, runs the read-only
// verification probe the card displayed, and only then enables it. A failed
// probe restores the previously published version and keeps the draft, so the
// user can fix the cause and retry.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Draft kinds — the two onboarding lines.
const (
	DraftKindProvider   = "provider"
	DraftKindDataSource = "data_source"
)

// Draft lifecycle. Only `confirmed` may publish.
const (
	DraftStatusDraft      = "draft"
	DraftStatusLintFailed = "lint_failed"
	DraftStatusConfirmed  = "confirmed"
	DraftStatusPublished  = "published"
	DraftStatusRejected   = "rejected"
)

// ErrDraftNotFound covers both "no such draft" and "not yours", so a caller
// cannot probe for drafts it cannot see.
var ErrDraftNotFound = errors.New("integration draft not found")

// ErrDraftInvalid is the base for structural problems with a draft request.
var ErrDraftInvalid = errors.New("integration draft is invalid")

// ErrAgentCannotAuthorize is returned whenever an agent-originated request tries
// to perform an integration authorization action — publish, widen scope, enable
// writes, or roll back. The rule behind it: a passwordless local UI is not an
// authorization for an agent to change integration configuration. An agent may
// draft and ask; only a human may grant.
var ErrAgentCannotAuthorize = errors.New(
	"integration authorization refused: an agent may draft and request, but may not publish, widen scope, enable writes, or roll back")

// ErrDraftNotConfirmed is returned by publish when the draft has not passed the
// impact confirmation step.
var ErrDraftNotConfirmed = errors.New("integration draft has not passed impact confirmation")

// ErrDraftStale is returned when the confirmed bytes no longer match the draft's
// current structured fields — i.e. the draft was edited after approval. The
// approval is void; re-confirm.
var ErrDraftStale = errors.New("integration draft changed after confirmation; re-confirm before publishing")

// ErrDraftAckIncomplete is returned when a confirmation omits a high-risk item.
var ErrDraftAckIncomplete = errors.New("every high-risk item must be acknowledged")

// ProviderDraftPayload is the candidate configuration of the Provider line.
// Every field is structured; none is prose. It carries NO secret: the credential
// VALUES arrive through the one-time credential link (#727), and
// CredentialFields records only which fields the draft needs.
type ProviderDraftPayload struct {
	// Label is a display name. It is deliberately NOT rendered on the
	// confirmation card (see cardInput) — a display name grants nothing and a
	// poisoned one could mislabel a dangerous operation set.
	Label      string            `json:"label,omitempty"`
	BaseURL    string            `json:"base_url"`
	Contract   *ProviderContract `json:"contract"`
	Whitelist  string            `json:"whitelist,omitempty"`
	OpenAPIURL string            `json:"openapi_url,omitempty"`
	// WriteEnabled is the provider-line write gate the draft asks for. Turning
	// it on is an authorization act and lands on the card as a high-risk item.
	WriteEnabled bool `json:"write_enabled"`
	// CredentialFields are the FIELD names the provider needs (never values).
	CredentialFields []DraftCredential `json:"credential_fields,omitempty"`
	// Functions are the sandbox operation functions this draft would publish
	// (Epic #726 / #739). Their lint verdicts and behaviour rehearsals are
	// regenerated server-side on ingest, so a draft cannot present a
	// flattering preview of code that behaves differently.
	Functions []FunctionDraftPayload `json:"functions,omitempty"`
}

// DataSourceDraftPayload is the candidate configuration of the data-source line.
type DataSourceDraftPayload struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Config      map[string]any `json:"config"`
	ScopeConfig map[string]any `json:"scope_config"`
	// DefaultAccessMode is "read" | "readwrite". "readwrite" turns on the write
	// gate and therefore appears on the card as write_enabled.
	DefaultAccessMode string `json:"default_access_mode"`
	RequireConfirm    string `json:"require_confirm"`
	// CredentialFields are the FIELD names the source needs (never values).
	CredentialFields []DraftCredential `json:"credential_fields,omitempty"`
}

// DraftIssue is one lint finding. "error" blocks publish; "warning" is advisory.
type DraftIssue struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

// IntegrationDraftView is the API/service representation of a draft: the
// candidate, its lint verdict, and the system-rendered confirmation card.
type IntegrationDraftView struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	TargetName    string `json:"target_name"`
	TargetID      int64  `json:"target_id"`
	Status        string `json:"status"`
	AgentAuthored bool   `json:"agent_authored"`

	Provider   *ProviderDraftPayload   `json:"provider,omitempty"`
	DataSource *DataSourceDraftPayload `json:"data_source,omitempty"`

	Origin   UntrustedOrigin    `json:"origin"`
	Advisory []QuarantinedChunk `json:"advisory"`
	DocHost  string             `json:"doc_host,omitempty"`

	Issues []DraftIssue `json:"issues"`
	// Card is the system-rendered impact-confirmation card. It is produced by
	// BuildConfirmCard from structured fields; the advisory region is the only
	// free-text part and sits outside the decision area.
	Card *ConfirmCardModel `json:"card,omitempty"`
	// ConfirmPayload is the exact decision bytes a reviewer approved (advisory
	// region dropped by construction). Empty until confirmed.
	ConfirmPayload string `json:"confirm_payload,omitempty"`
	// RequiresSecondConfirmation is true when the current candidate widens
	// authority (scope widened / writes enabled) and the explicit second
	// confirmation has not been given yet.
	RequiresSecondConfirmation bool     `json:"requires_second_confirmation,omitempty"`
	PendingRiskCodes           []string `json:"pending_risk_codes,omitempty"`

	CreatedAt time.Time    `json:"created_at"`
	Published sql.NullTime `json:"published_at,omitempty"`
}

// CreateDraftInput is a draft-creation request. OwnerType/OwnerID are the
// integration owner (a personal user, or an org); CreatedBy is the requesting
// user. AgentAuthored must be set by the caller based on the request's origin:
// true when the request arrived through an agent-facing (MCP) path.
type CreateDraftInput struct {
	OwnerType string
	OwnerID   int64
	CreatedBy int64
	Kind      string
	// TargetName is the provider name / data-source name the draft publishes
	// into. It is an identifier the system enforces, never model prose.
	TargetName string

	AgentAuthored bool

	Provider   *ProviderDraftPayload
	DataSource *DataSourceDraftPayload

	// Provenance of the source document, when the draft was derived from one.
	Origin   UntrustedOrigin
	Advisory []QuarantinedChunk
	// DocOps / DocHost come from the STRUCTURED sections of the source document
	// (OpenAPI paths / servers). Empty means the document declared none.
	DocOps  []OperationKey
	DocHost string
}

// ConfirmImpactInput is an impact-confirmation decision.
type ConfirmImpactInput struct {
	DraftID   int64
	OwnerType string
	OwnerID   int64
	// AckKeys are the risk ack_keys the reviewer ticked. Every key the card
	// lists must be present.
	AckKeys []string
	// SecondConfirmation is the SEPARATE, explicit confirmation a widening
	// change (scope widened / writes enabled) demands. Without it such a draft
	// stays unconfirmed, so a single click can never both widen authority and
	// approve it.
	SecondConfirmation bool
	// Reject=true records a rejection instead of an approval.
	Reject bool
}

// DraftVerifier runs the post-publish verification probe. It is an interface so
// the pipeline's own tests are hermetic; the default implementation delegates to
// the live proxy (providers) and the connector registry (data sources).
type DraftVerifier interface {
	VerifyProvider(ctx context.Context, providerName string, userID int64) (*DraftVerifyResult, error)
	VerifyDataSource(ctx context.Context, sourceID, userID int64, orgIDs []int64) (*DraftVerifyResult, error)
}

// DraftVerifyResult is the verdict of the post-publish probe.
type DraftVerifyResult struct {
	OK     bool           `json:"ok"`
	Status int            `json:"status,omitempty"`
	Probe  *ProbeManifest `json:"probe,omitempty"`
	Detail string         `json:"detail,omitempty"`
	// RequiresConfirmation is set when the selected probe is not a provably
	// read-only method (GET/HEAD): the probe was refused because it cannot be
	// auto-executed, even though the confirmation card was approved.
	RequiresConfirmation bool `json:"requires_confirmation,omitempty"`
}

// IntegrationDraftService owns the draft pipeline.
type IntegrationDraftService struct {
	q         *store.Queries
	providers *ExternalProviderService
	sources   *DataSourceService
	health    *IntegrationHealthService
	verifier  DraftVerifier
	// functions is the sandbox operation-function runtime (#739). It is wired
	// after construction (SetFunctionService) so the draft pipeline keeps
	// working in builds and tests that do not exercise functions at all.
	functions *OperationFunctionService
}

// SetFunctionService wires the operation-function runtime into the draft
// pipeline. Nil is a supported state: a draft carrying functions is then
// refused with a clear reason rather than silently dropping them.
func (s *IntegrationDraftService) SetFunctionService(f *OperationFunctionService) { s.functions = f }

// NewIntegrationDraftService wires the pipeline to the live services. The
// verifier defaults to the real probe path (liveVerifier).
func NewIntegrationDraftService(
	q *store.Queries,
	providers *ExternalProviderService,
	sources *DataSourceService,
	proxy *ExternalProxyService,
	health *IntegrationHealthService,
) *IntegrationDraftService {
	s := &IntegrationDraftService{q: q, providers: providers, sources: sources, health: health}
	s.verifier = &liveVerifier{proxy: proxy, sources: sources, providers: providers}
	return s
}

// SetVerifier overrides the verification probe (tests inject a fake so the
// pipeline can be exercised without a network).
func (s *IntegrationDraftService) SetVerifier(v DraftVerifier) { s.verifier = v }

// --- create / read --------------------------------------------------------

// CreateDraft lints a candidate and persists it. Nothing about the running
// integration changes: the row is the whole effect, which is what makes an
// interrupted agent harmless.
func (s *IntegrationDraftService) CreateDraft(ctx context.Context, in CreateDraftInput) (*IntegrationDraftView, error) {
	if err := validateOwnerType(in.OwnerType); err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != DraftKindProvider && kind != DraftKindDataSource {
		return nil, fmt.Errorf("%w: kind must be %s|%s", ErrDraftInvalid, DraftKindProvider, DraftKindDataSource)
	}
	name := strings.TrimSpace(in.TargetName)
	if name == "" {
		return nil, fmt.Errorf("%w: target name is required", ErrDraftInvalid)
	}
	if len(name) > 128 {
		return nil, fmt.Errorf("%w: target name too long", ErrDraftInvalid)
	}
	if err := validateDraftTargetName(name); err != nil {
		return nil, err
	}

	var payload any
	var issues []DraftIssue
	switch kind {
	case DraftKindProvider:
		if in.Provider == nil {
			return nil, fmt.Errorf("%w: provider payload is required", ErrDraftInvalid)
		}
		if err := normalizeProviderDraft(in.Provider); err != nil {
			return nil, err
		}
		// Operation functions (#739) are linted and REHEARSED here, on the
		// server, before the draft is stored. Whatever the caller sent in the
		// lint/rehearsal fields is discarded: the preview a human approves must
		// be the system's own, or a draft could show benign behaviour and ship
		// something else.
		if len(in.Provider.Functions) > 0 && s.functions != nil {
			prepared, flints, err := s.functions.PrepareFunctionDrafts(ctx, name, in.Provider.Contract, in.Provider.Functions, in.CreatedBy)
			if err != nil {
				return nil, err
			}
			in.Provider.Functions = prepared
			for _, fl := range flints {
				path := "function:" + fl.Function
				if fl.Blocking && len(fl.Findings) == 0 {
					// A blocking verdict with no finding means the rehearsal
					// itself failed; that is still a reason not to publish.
					issues = append(issues, DraftIssue{
						Severity: "error", Path: path,
						Message: "the behaviour rehearsal did not complete, so this function's egress was never previewed",
					})
				}
				for _, f := range fl.Findings {
					issues = append(issues, DraftIssue{
						Severity: string(f.Severity),
						Path:     path,
						Message:  fmt.Sprintf("line %d: %s", f.Line, f.Message),
					})
				}
				if fl.Blocking && !sandbox.LintBlocks(fl.Findings) && len(fl.Findings) > 0 {
					issues = append(issues, DraftIssue{
						Severity: "error", Path: path,
						Message: "the behaviour rehearsal failed or reached an undeclared operation",
					})
				}
			}
		} else if len(in.Provider.Functions) > 0 && s.functions == nil {
			return nil, fmt.Errorf("%w: the sandbox function runtime is not available in this build", ErrDraftInvalid)
		}
		payload = in.Provider
		issues = append(issues, s.lintProvider(in.Provider)...)
	case DraftKindDataSource:
		if in.DataSource == nil {
			return nil, fmt.Errorf("%w: data_source payload is required", ErrDraftInvalid)
		}
		if err := s.normalizeDataSourceDraft(in.DataSource); err != nil {
			return nil, err
		}
		if in.DataSource.Name != name {
			return nil, fmt.Errorf("%w: data source name %q does not match target name %q", ErrDraftInvalid, in.DataSource.Name, name)
		}
		payload = in.DataSource
		issues = s.lintDataSource(in.DataSource)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal payload: %v", ErrDraftInvalid, err)
	}
	status := DraftStatusDraft
	if hasBlockingIssue(issues) {
		status = DraftStatusLintFailed
	}
	issuesJSON, _ := json.Marshal(issues)
	originJSON, _ := json.Marshal(in.Origin)
	advisoryJSON, _ := json.Marshal(nonNilChunks(in.Advisory))
	docOpsJSON, _ := json.Marshal(nonNilOps(in.DocOps))

	agentAuthored := int64(0)
	if in.AgentAuthored {
		agentAuthored = 1
	}
	row, err := s.q.CreateIntegrationDraft(ctx, store.CreateIntegrationDraftParams{
		OwnerType:     in.OwnerType,
		OwnerID:       in.OwnerID,
		Kind:          kind,
		TargetName:    name,
		TargetID:      0,
		Status:        status,
		Payload:       string(payloadJSON),
		OriginJson:    string(originJSON),
		AdvisoryJson:  string(advisoryJSON),
		DocOpsJson:    string(docOpsJSON),
		DocHost:       strings.TrimSpace(in.DocHost),
		LintJson:      string(issuesJSON),
		AgentAuthored: agentAuthored,
		CreatedBy:     in.CreatedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("create integration draft: %w", err)
	}
	return s.view(ctx, row)
}

// GetDraft returns a draft within the caller's owner scope.
func (s *IntegrationDraftService) GetDraft(ctx context.Context, id int64, ownerType string, ownerID int64) (*IntegrationDraftView, error) {
	row, err := s.getOwnedDraft(ctx, id, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, row)
}

// ListDrafts returns the owner's drafts, newest first.
func (s *IntegrationDraftService) ListDrafts(ctx context.Context, ownerType string, ownerID int64, limit int64) ([]IntegrationDraftView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.ListIntegrationDrafts(ctx, store.ListIntegrationDraftsParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]IntegrationDraftView, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// DeleteDraft removes a draft the caller owns. It never touches the live
// integration.
func (s *IntegrationDraftService) DeleteDraft(ctx context.Context, id int64, ownerType string, ownerID int64) error {
	if _, err := s.getOwnedDraft(ctx, id, ownerType, ownerID); err != nil {
		return err
	}
	return s.q.DeleteIntegrationDraft(ctx, id)
}

func (s *IntegrationDraftService) getOwnedDraft(ctx context.Context, id int64, ownerType string, ownerID int64) (store.IntegrationDraft, error) {
	row, err := s.q.GetIntegrationDraft(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.IntegrationDraft{}, ErrDraftNotFound
	}
	if err != nil {
		return store.IntegrationDraft{}, err
	}
	if row.OwnerType != ownerType || row.OwnerID != ownerID {
		return store.IntegrationDraft{}, ErrDraftNotFound
	}
	return row, nil
}

// --- view + card ----------------------------------------------------------

// view renders a draft row into its API shape, including the system-rendered
// confirmation card and the "does this need a second confirmation?" verdict.
func (s *IntegrationDraftService) view(ctx context.Context, row store.IntegrationDraft) (*IntegrationDraftView, error) {
	v := &IntegrationDraftView{
		ID:             row.ID,
		Kind:           row.Kind,
		TargetName:     row.TargetName,
		TargetID:       row.TargetID,
		Status:         row.Status,
		AgentAuthored:  row.AgentAuthored != 0,
		Origin:         UntrustedOrigin{},
		Advisory:       []QuarantinedChunk{},
		Issues:         []DraftIssue{},
		ConfirmPayload: row.ConfirmJson,
		CreatedAt:      row.CreatedAt,
		Published:      row.PublishedAt,
	}
	_ = json.Unmarshal([]byte(row.OriginJson), &v.Origin)
	_ = json.Unmarshal([]byte(row.AdvisoryJson), &v.Advisory)
	if v.Advisory == nil {
		v.Advisory = []QuarantinedChunk{}
	}
	_ = json.Unmarshal([]byte(row.LintJson), &v.Issues)
	if v.Issues == nil {
		v.Issues = []DraftIssue{}
	}

	var docOps []OperationKey
	_ = json.Unmarshal([]byte(row.DocOpsJson), &docOps)

	switch row.Kind {
	case DraftKindProvider:
		var p ProviderDraftPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return nil, fmt.Errorf("integration draft %d: corrupt payload: %w", row.ID, err)
		}
		v.Provider = &p
	case DraftKindDataSource:
		var d DataSourceDraftPayload
		if err := json.Unmarshal([]byte(row.Payload), &d); err != nil {
			return nil, fmt.Errorf("integration draft %d: corrupt payload: %w", row.ID, err)
		}
		v.DataSource = &d
	}

	card, err := s.cardFor(ctx, row, docOps)
	if err != nil {
		return nil, err
	}
	v.Card = &card
	if requiresSecondConfirmation(card) && row.SecondConfirmed == 0 {
		v.RequiresSecondConfirmation = true
		v.PendingRiskCodes = wideningRiskCodes(card)
	}
	return v, nil
}

// cardFor builds the system-rendered confirmation card for a draft row. This is
// the ONLY construction point: every decision field comes from a structured
// draft field or from the published state being diffed against, never from text.
// confirmFunctions projects the draft's function entries onto the card.
func confirmFunctions(fns []FunctionDraftPayload, providerName string) []ConfirmFunction {
	if len(fns) == 0 {
		return nil
	}
	out := make([]ConfirmFunction, 0, len(fns))
	for _, f := range fns {
		cf := ConfirmFunction{
			Name:         f.Name,
			ToolName:     sandbox.ToolName(providerName, f.Name),
			Runtime:      f.Runtime,
			Code:         f.Code,
			CodeHash:     f.CodeHash,
			Idempotent:   f.Idempotent,
			Enabled:      f.Enabled,
			Lint:         f.Lint,
			IsNew:        f.ExistingID == 0,
			PreviousHash: f.PreviousHash,
		}
		for _, e := range f.Egress {
			cf.Egress = append(cf.Egress, e.Key())
		}
		if f.Rehearsal != nil {
			det := f.Rehearsal.Deterministic()
			cf.Rehearsal = &det
		}
		out = append(out, cf)
	}
	return out
}

func (s *IntegrationDraftService) cardFor(ctx context.Context, row store.IntegrationDraft, docOps []OperationKey) (ConfirmCardModel, error) {
	var origin UntrustedOrigin
	_ = json.Unmarshal([]byte(row.OriginJson), &origin)
	var advisory []QuarantinedChunk
	_ = json.Unmarshal([]byte(row.AdvisoryJson), &advisory)

	prev, err := s.publishedState(ctx, row.OwnerType, row.OwnerID, row.Kind, row.TargetName)
	if err != nil {
		return ConfirmCardModel{}, err
	}
	return buildCardFromPayload(row.Kind, row.TargetName, row.Payload, docOps, row.DocHost, origin, advisory, prev)
}

// buildCardFromPayload is the single place a draft/version payload becomes a
// confirmation card. It is shared by the draft card, the publish re-check and
// the rollback card so all three can never disagree about what will run.
func buildCardFromPayload(kind, targetName, payloadJSON string, docOps []OperationKey, docHost string,
	origin UntrustedOrigin, advisory []QuarantinedChunk, prev *PublishedState) (ConfirmCardModel, error) {

	in := ConfirmDraftInput{
		Kind:                  ConfirmKindProvider,
		Name:                  targetName,
		DocDeclaredOperations: docOps,
		DocDeclaredHost:       docHost,
		Origin:                origin,
		Advisory:              advisory,
		Previous:              prev,
	}

	switch kind {
	case DraftKindProvider:
		var p ProviderDraftPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return ConfirmCardModel{}, fmt.Errorf("integration draft: corrupt provider payload: %w", err)
		}
		in.Kind = ConfirmKindProvider
		in.BaseURL = p.BaseURL
		in.Contract = p.Contract
		in.WriteEnabled = p.WriteEnabled
		in.Functions = confirmFunctions(p.Functions, targetName)
	case DraftKindDataSource:
		var d DataSourceDraftPayload
		if err := json.Unmarshal([]byte(payloadJSON), &d); err != nil {
			return ConfirmCardModel{}, fmt.Errorf("integration draft: corrupt data-source payload: %w", err)
		}
		in.Kind = ConfirmKindDataSource
		in.BaseURL = dataSourceBaseURL(d.Config)
		in.ScopeConfig = d.ScopeConfig
		in.SourceKind = d.Kind
		// A readwrite access mode IS the write gate for the data-source line, so
		// it must surface as the same high-risk item a provider's write toggle
		// does.
		in.WriteEnabled = strings.EqualFold(strings.TrimSpace(d.DefaultAccessMode), "readwrite")
	default:
		return ConfirmCardModel{}, fmt.Errorf("%w: unknown draft kind %q", ErrDraftInvalid, kind)
	}
	// Note: in.Label is deliberately left empty. confirmTitle falls back to
	// in.Name, the enforced identifier — a display label grants nothing and a
	// poisoned one could mislabel a dangerous operation set (see
	// docs/integration-authorization-security.md §4.1.1).
	return BuildConfirmCard(in), nil
}

// publishedState loads the live configuration a draft is diffed against, or nil
// when the target has never been published.
func (s *IntegrationDraftService) publishedState(ctx context.Context, ownerType string, ownerID int64, kind, targetName string) (*PublishedState, error) {
	switch kind {
	case DraftKindProvider:
		prov, err := s.providers.GetByName(ctx, targetName)
		if err != nil {
			return nil, nil // not published yet
		}
		c := prov.Contract()
		ops := make([]OperationKey, 0, len(c.Operations))
		for _, op := range c.Operations {
			ops = append(ops, OperationKey{Method: op.Method, Path: op.Path})
		}
		state := &PublishedState{BaseURL: c.BaseURL, Operations: ops}
		if s.functions != nil {
			// The live functions are part of what a rollback would replace, so
			// their authority belongs in the state the diff compares against.
			state.FunctionScope = s.functions.FunctionScopeForProvider(ctx, prov.ID).Functions
		}
		return state, nil
	case DraftKindDataSource:
		id, err := s.sources.ResolveSourceID(ctx, targetName, dsOwnershipUserID(ownerType, ownerID), orgIDsForOwner(ownerType, ownerID))
		if err != nil || id <= 0 {
			return nil, nil // not published yet
		}
		dto, err := s.sources.Get(ctx, id, dsOwnershipUserID(ownerType, ownerID), orgIDsForOwner(ownerType, ownerID))
		if err != nil {
			return nil, nil
		}
		return &PublishedState{
			BaseURL:     dataSourceBaseURL(dto.Config),
			ScopeConfig: dto.ScopeConfig,
		}, nil
	}
	return nil, nil
}

// --- confirmation ---------------------------------------------------------

// ConfirmDraft records an impact-confirmation decision. It refuses to confirm
// anything the card flags as high-risk until every item is acknowledged, and
// refuses a widening change (scope widened / writes enabled) until the caller
// gives the explicit second confirmation.
func (s *IntegrationDraftService) ConfirmDraft(ctx context.Context, in ConfirmImpactInput) (*IntegrationDraftView, error) {
	row, err := s.getOwnedDraft(ctx, in.DraftID, in.OwnerType, in.OwnerID)
	if err != nil {
		return nil, err
	}
	if row.Status == DraftStatusPublished {
		return nil, fmt.Errorf("%w: draft %d is already published", ErrDraftInvalid, row.ID)
	}

	// Rejection is available for ANY unpublished draft, including one that failed
	// lint: "no thanks" is a terminal answer that needs no validation.
	if in.Reject {
		if err := s.q.UpdateIntegrationDraftLint(ctx, store.UpdateIntegrationDraftLintParams{
			Status: DraftStatusRejected, LintJson: row.LintJson, ID: row.ID,
		}); err != nil {
			return nil, err
		}
		row.Status = DraftStatusRejected
		return s.view(ctx, row)
	}

	// A draft with blocking lint errors is not confirmable: the card is for a
	// human decision, not a validator, and approving an unpublishable candidate
	// would only produce a confusing failure later.
	var lintIssues []DraftIssue
	_ = json.Unmarshal([]byte(row.LintJson), &lintIssues)
	if hasBlockingIssue(lintIssues) {
		return nil, fmt.Errorf("%w: draft %d has blocking lint errors; fix them and re-lint first", ErrDraftInvalid, row.ID)
	}
	var docOps []OperationKey
	_ = json.Unmarshal([]byte(row.DocOpsJson), &docOps)
	card, err := s.cardFor(ctx, row, docOps)
	if err != nil {
		return nil, err
	}

	// Every high-risk item the card lists must be acknowledged individually.
	acked := make(map[string]bool, len(in.AckKeys))
	for _, k := range in.AckKeys {
		acked[k] = true
	}
	var missing []string
	for _, key := range card.AckKeys() {
		if !acked[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrDraftAckIncomplete, strings.Join(missing, ", "))
	}

	// A widening change demands a separate, explicit second confirmation. Until
	// it is given the draft stays unconfirmed, so one click can never both widen
	// authority and approve it.
	if requiresSecondConfirmation(card) && !in.SecondConfirmation {
		v, err := s.view(ctx, row)
		if err != nil {
			return nil, err
		}
		v.RequiresSecondConfirmation = true
		v.PendingRiskCodes = wideningRiskCodes(card)
		return v, nil
	}

	payload, err := DecisionPayload(card)
	if err != nil {
		return nil, fmt.Errorf("integration draft %d: build decision payload: %w", row.ID, err)
	}
	ackJSON, _ := json.Marshal(card.AckKeys())
	second := int64(0)
	if requiresSecondConfirmation(card) {
		second = 1
	}
	if err := s.q.UpdateIntegrationDraftConfirm(ctx, store.UpdateIntegrationDraftConfirmParams{
		ConfirmJson:     string(payload),
		AckJson:         string(ackJSON),
		SecondConfirmed: second,
		ID:              row.ID,
	}); err != nil {
		return nil, err
	}
	return s.GetDraft(ctx, row.ID, in.OwnerType, in.OwnerID)
}

// requiresSecondConfirmation reports whether the card's risks include one that
// widens authority rather than merely adding capability.
func requiresSecondConfirmation(card ConfirmCardModel) bool {
	return len(wideningRiskCodes(card)) > 0
}

// wideningRiskCodes lists the risk codes that constitute "widening authority":
// a scope grant outside the baseline, or the write gate being switched on.
func wideningRiskCodes(card ConfirmCardModel) []string {
	var codes []string
	for _, r := range card.Risks {
		switch r.Code {
		case RiskScopeWidened, RiskWriteEnabled:
			codes = append(codes, r.Code)
		}
	}
	sort.Strings(codes)
	return codes
}

// --- lint -----------------------------------------------------------------

// lintProvider validates a provider draft. Contract validation is delegated to
// LintContract (the #729 entry point), so the draft pipeline and the publish
// path cannot disagree about what "valid" means.
func (s *IntegrationDraftService) lintProvider(p *ProviderDraftPayload) []DraftIssue {
	var issues []DraftIssue

	if p.Contract == nil {
		return []DraftIssue{{Severity: "error", Path: "contract", Message: "a provider contract is required"}}
	}
	// normalizeProviderDraft has already filled BaseURL from the contract.
	if strings.TrimSpace(p.BaseURL) == "" {
		issues = append(issues, DraftIssue{"error", "base_url", "base_url is required"})
	}
	for _, iss := range LintContract(p.Contract) {
		issues = append(issues, DraftIssue{Severity: iss.Severity, Path: iss.Path, Message: iss.Message})
	}
	// The confirmation card must display the verification probe, and "what the
	// user sees" must equal "what runs" — so the contract has to name it. A
	// probe auto-picked at publish time would egress without ever being shown.
	if strings.TrimSpace(p.Contract.VerifyOperation) == "" {
		issues = append(issues, DraftIssue{"error", "verify_operation",
			"the contract must name a read-only verify_operation so the post-publish probe is visible on the confirmation card"})
	}

	// Auth field completeness. Values are never checked — the credential link
	// owns them, and a draft carries none.
	auth := p.Contract.Auth
	switch {
	case auth == nil || strings.TrimSpace(auth.Strategy) == "":
		issues = append(issues, DraftIssue{"error", "auth.strategy", "an auth strategy is required"})
	default:
		strategy := strings.ToLower(strings.TrimSpace(auth.Strategy))
		switch strategy {
		case "bearer", "basic", "custom_header", "api_key", "header":
			if strings.TrimSpace(auth.Header) == "" {
				issues = append(issues, DraftIssue{"error", "auth.header", "auth strategy " + strategy + " requires a header name"})
			}
		case "query_param":
			if strings.TrimSpace(auth.Param) == "" {
				issues = append(issues, DraftIssue{"error", "auth.param", "auth strategy query_param requires a parameter name"})
			}
		}
		if len(p.CredentialFields) == 0 {
			issues = append(issues, DraftIssue{"error", "credential_fields",
				"declare the credential fields the provider needs (field names only; values come from the one-time link)"})
		}
	}

	// Whitelist legality: a JSON array of {method, path} rules.
	if strings.TrimSpace(p.Whitelist) != "" {
		var rules []WhitelistRule
		if err := json.Unmarshal([]byte(p.Whitelist), &rules); err != nil {
			issues = append(issues, DraftIssue{"error", "whitelist", "whitelist is not a JSON array of {method,path}"})
		} else {
			for i, r := range rules {
				loc := fmt.Sprintf("whitelist[%d]", i)
				if strings.TrimSpace(r.Method) == "" {
					issues = append(issues, DraftIssue{"error", loc + ".method", "whitelist rule needs a method"})
				}
				if strings.TrimSpace(r.Path) == "" {
					issues = append(issues, DraftIssue{"error", loc + ".path", "whitelist rule needs a path"})
				}
			}
			if p.WriteEnabled && !whitelistHasWrite(rules) {
				issues = append(issues, DraftIssue{"warning", "whitelist",
					"the write gate is being enabled but no non-GET rule is whitelisted; every method/path will pass once writes are on"})
			}
		}
	}
	return issues
}

// lintDataSource validates a data-source draft: kind supported, non-secret
// config complete, no secrets in the payload, scope keys legal for the kind.
func (s *IntegrationDraftService) lintDataSource(d *DataSourceDraftPayload) []DraftIssue {
	var issues []DraftIssue

	if !s.sources.isSupportedKind(d.Kind) {
		issues = append(issues, DraftIssue{"error", "kind", "unsupported data source kind " + d.Kind})
	}
	if strings.TrimSpace(d.Name) == "" {
		issues = append(issues, DraftIssue{"error", "name", "name is required"})
	}

	// No secret may travel in a draft. The one-time credential link exists
	// precisely so a value never enters the model context; a draft carrying one
	// would defeat it.
	for _, k := range secretDraftConfigKeys(d.Config) {
		issues = append(issues, DraftIssue{"error", "config." + k,
			"a secret must not appear in a draft; submit it through the one-time credential link instead"})
	}

	// Non-secret connection completeness. Every non-http kind is a network
	// service addressed by host; http is too (the connector builds its base URL
	// from scheme/host).
	if strings.TrimSpace(stringField(d.Config, "host")) == "" {
		issues = append(issues, DraftIssue{"error", "config.host", "config.host is required"})
	}

	// Scope legality: the proxy reads exactly these four keys, so anything else
	// is either a typo or an attempt to smuggle policy past the gate.
	for _, k := range sortedScopeKeys(d.ScopeConfig) {
		if !validScopeKeys[k] {
			issues = append(issues, DraftIssue{"error", "scope_config." + k,
				"unknown scope key; the proxy reads only databases, tables_allow, tables_deny, methods"})
			continue
		}
		if _, ok := scopeValueList(d.ScopeConfig, k); !ok {
			issues = append(issues, DraftIssue{"error", "scope_config." + k, "scope values must be a list of strings"})
		}
	}
	if len(scopeValueListOrNil(d.ScopeConfig, "methods")) > 0 && !strings.EqualFold(d.Kind, "http") {
		issues = append(issues, DraftIssue{"warning", "scope_config.methods",
			"the methods allow-list only applies to http sources"})
	}

	// Access mode / confirmation policy must be one of the recognized values.
	switch strings.ToLower(strings.TrimSpace(d.DefaultAccessMode)) {
	case "", "read", "readwrite":
	default:
		issues = append(issues, DraftIssue{"error", "default_access_mode", "default_access_mode must be read or readwrite"})
	}
	switch strings.ToLower(strings.TrimSpace(d.RequireConfirm)) {
	case "", "always", "writes_only", "never":
	default:
		issues = append(issues, DraftIssue{"error", "require_confirm", "require_confirm must be always or writes_only"})
	}
	if len(d.CredentialFields) == 0 && configNeedsCredential(d.Kind) {
		issues = append(issues, DraftIssue{"warning", "credential_fields",
			"no credential fields declared; the source will be published without a stored credential until one is submitted"})
	}
	return issues
}

// validScopeKeys are the only scope_config keys the data proxy reads (see
// data_proxy.go parseScopeConfig). Anything else is dead policy at best.
var validScopeKeys = map[string]bool{
	"databases":    true,
	"tables_allow": true,
	"tables_deny":  true,
	"methods":      true,
}

// secretDraftConfigKeys lists secret-shaped keys present in a draft config.
// Presence alone is rejected (regardless of value) so the rule is unambiguous.
func secretDraftConfigKeys(cfg map[string]any) []string {
	if cfg == nil {
		return nil
	}
	var found []string
	for _, k := range []string{
		"password", "private_key", "ssh_password", "api_key", "token", "secret",
		"headers", "app_secret", "client_secret", "aes_key", "bot_token", "webhook_secret",
	} {
		if _, ok := cfg[k]; ok {
			found = append(found, k)
		}
	}
	if opts, ok := cfg["options"].(map[string]any); ok {
		for _, k := range []string{"api_key", "token", "headers"} {
			if _, ok := opts[k]; ok {
				found = append(found, "options."+k)
			}
		}
	}
	if ssh, ok := cfg["ssh"].(map[string]any); ok {
		for _, k := range []string{"password", "private_key"} {
			if _, ok := ssh[k]; ok {
				found = append(found, "ssh."+k)
			}
		}
	}
	sort.Strings(found)
	return found
}

// configNeedsCredential reports whether a kind's connectivity normally requires
// a credential (used only to raise an advisory warning for the http kind, whose
// base URL may be public).
func configNeedsCredential(kind string) bool {
	return !strings.EqualFold(strings.TrimSpace(kind), "http")
}

func whitelistHasWrite(rules []WhitelistRule) bool {
	for _, r := range rules {
		m := strings.ToUpper(strings.TrimSpace(r.Method))
		if m != "GET" && m != "HEAD" {
			return true
		}
	}
	return false
}

func hasBlockingIssue(issues []DraftIssue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}

// --- normalization --------------------------------------------------------

// validateDraftTargetName keeps a target name to the shape the live stores
// accept, so a draft cannot name something unpublishable.
func validateDraftTargetName(name string) error {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == ' ':
		default:
			return fmt.Errorf("%w: target name may only contain letters, digits, space, '-', '_' and '.'", ErrDraftInvalid)
		}
	}
	return nil
}

// normalizeProviderDraft fills the fields that have exactly one correct value,
// so lint and publish always read the same wiring.
func normalizeProviderDraft(p *ProviderDraftPayload) error {
	if p.Contract == nil {
		return nil
	}
	p.Contract.normalized()
	if strings.TrimSpace(p.BaseURL) == "" {
		p.BaseURL = strings.TrimSpace(p.Contract.BaseURL)
	}
	if strings.TrimSpace(p.Contract.BaseURL) == "" {
		p.Contract.BaseURL = strings.TrimSpace(p.BaseURL)
	}
	return nil
}

// normalizeDataSourceDraft canonicalizes kind/name/access-mode so the payload
// stored is the payload published.
func (s *IntegrationDraftService) normalizeDataSourceDraft(d *DataSourceDraftPayload) error {
	d.Kind = strings.TrimSpace(d.Kind)
	d.Name = strings.TrimSpace(d.Name)
	if d.Config == nil {
		d.Config = map[string]any{}
	}
	if d.ScopeConfig == nil {
		// A draft that grants nothing is the correct starting point: the scope
		// baseline is the minimal default, and widening is a deliberate user
		// action on the card.
		d.ScopeConfig = MinimalScopeDefaults(d.Kind)
	}
	switch strings.ToLower(strings.TrimSpace(d.DefaultAccessMode)) {
	case "readwrite":
		d.DefaultAccessMode = "readwrite"
	default:
		d.DefaultAccessMode = "read"
	}
	if strings.EqualFold(strings.TrimSpace(d.RequireConfirm), "always") {
		d.RequireConfirm = "always"
	} else {
		d.RequireConfirm = "writes_only"
	}
	return nil
}

// --- small helpers --------------------------------------------------------

func nonNilChunks(in []QuarantinedChunk) []QuarantinedChunk {
	if in == nil {
		return []QuarantinedChunk{}
	}
	return in
}

func nonNilOps(in []OperationKey) []OperationKey {
	if in == nil {
		return []OperationKey{}
	}
	return in
}

// scopeValueList coerces a scope key's value to a string list, reporting
// ok=false when the value is present but not a list of strings.
func scopeValueList(m map[string]any, key string) ([]string, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, true
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []string:
		return append([]string{}, v...), true
	default:
		return nil, false
	}
}

func scopeValueListOrNil(m map[string]any, key string) []string {
	v, _ := scopeValueList(m, key)
	return v
}

// dataSourceBaseURL renders the non-secret address of a data-source config for
// display on the card. It is derived from connection facts only, and it never
// invents a scheme the config does not carry — showing "https://" for a MySQL
// source would misdescribe what is being approved.
func dataSourceBaseURL(cfg map[string]any) string {
	if cfg == nil {
		return ""
	}
	host := strings.TrimSpace(stringField(cfg, "host"))
	if host == "" {
		return ""
	}
	if p := intField(cfg, "port"); p > 0 {
		host = fmt.Sprintf("%s:%d", host, p)
	}
	addr := host + strings.TrimSpace(stringField(cfg, "base_path"))
	if scheme := strings.TrimSpace(stringField(cfg, "scheme")); scheme != "" {
		return scheme + "://" + addr
	}
	return addr
}
