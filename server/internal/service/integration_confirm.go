// Package service — system-rendered confirmation card for integration
// authorization decisions.
//
// Principle: AI 生成 → 系统渲染（结构化事实）→ 人工确认. A confirmation card
// renders only the structured facts the system will actually enforce, so what
// the user sees equals what will be executed. Free text — a document's own
// summary, a model's prose — is confined to the advisory region and can never
// populate a decision field.
//
// BuildConfirmCard is the single construction point for the card: every
// decision field is derived from a structured input (contract operations,
// scope_config, host, diff against the published state), never from a string
// that a document or a model authored. DecisionPayload is the exact bytes a
// reviewer approves; it drops the advisory region by construction.
//
// Scope note: this governs the integration authorization surface only. It does
// not touch coding work in a workspace — see integration_untrusted.go.
package service

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
)

// ConfirmDecisionKind discriminates the two onboarding lines.
type ConfirmDecisionKind string

const (
	ConfirmKindProvider   ConfirmDecisionKind = "provider"
	ConfirmKindDataSource ConfirmDecisionKind = "data_source"
)

// Risk severity. Every risk code below is "high" today: each one either widens
// authority or grants authority the source document did not ask for, so each
// must be acknowledged item by item (default unchecked).
const RiskSeverityHigh = "high"

// Risk codes. Structured, localised by the UI — never free text.
const (
	// RiskCrossOriginHost — the proposed API host differs from the host already
	// published, or from the host the source document itself declares.
	RiskCrossOriginHost = "cross_origin_host"
	// RiskNewWriteOperation — a non-read-only operation that is not in the
	// already-published set.
	RiskNewWriteOperation = "new_write_operation"
	// RiskOperationNotInDocument — an operation absent from the structured
	// operation list of the source document.
	RiskOperationNotInDocument = "operation_not_in_document"
	// RiskScopeWidened — a scope_config key granting values outside the
	// baseline (the published config, or the minimal default on first publish).
	RiskScopeWidened = "scope_widened"
	// RiskWriteEnabled — the provider write gate is being turned on.
	RiskWriteEnabled = "write_enabled"
	// RiskFunctionUnrehearsed — an operation function (#739) is being published
	// without a behaviour rehearsal, so its egress was never previewed.
	RiskFunctionUnrehearsed = "function_unrehearsed"
	// RiskFunctionUndeclaredOp — a function's rehearsal showed it calling an
	// operation outside the set it declares.
	RiskFunctionUndeclaredOp = "function_undeclared_operation"
)

// Change values for scope and diff entries.
const (
	ChangeAdded     = "added"
	ChangeRemoved   = "removed"
	ChangeWidened   = "widened"
	ChangeNarrowed  = "narrowed"
	ChangeChanged   = "changed"
	ChangeUnchanged = "unchanged"
)

// OperationKey identifies an operation by the two fields the proxy enforces.
// Nothing else about an operation is machine-enforceable, so nothing else
// belongs in the decision area.
type OperationKey struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// PublishedState is the currently live configuration a draft is diffed
// against. A nil *PublishedState means nothing has been published yet.
type PublishedState struct {
	BaseURL     string         `json:"base_url,omitempty"`
	Operations  []OperationKey `json:"operations,omitempty"`
	ScopeConfig map[string]any `json:"scope_config,omitempty"`
	// FunctionScope is the authority the live operation functions currently
	// hold (provider line only). A rollback that would change it is a
	// re-authorization, so it has to be part of what the diff compares.
	FunctionScope []FunctionScopeEntry `json:"function_scope,omitempty"`
}

// ConfirmDraftInput is the structured input to the card builder — exactly the
// set of fields that will be persisted and enforced when the draft publishes.
// Advisory carries untrusted free text and is never read for a decision.
type ConfirmDraftInput struct {
	Kind    ConfirmDecisionKind
	Name    string
	Label   string
	BaseURL string
	// Contract is the provider-line contract (#729). Nil for a data source.
	Contract *ProviderContract
	// ScopeConfig is the data-source-line scope policy. Nil for a provider.
	ScopeConfig map[string]any
	// SourceKind is the data-source kind ("http", "mysql", …) used to pick the
	// minimal scope baseline. Ignored for a provider.
	SourceKind string
	// WriteEnabled is the provider-line write gate the draft asks for.
	WriteEnabled bool

	// DocDeclaredOperations is the operation list taken from the STRUCTURED
	// sections of the source document (an OpenAPI document's paths/operations),
	// never from its prose. Empty means the document declared no list, so the
	// "operation not in document" risk cannot be evaluated.
	DocDeclaredOperations []OperationKey
	// DocDeclaredHost is the host the source document declares (OpenAPI
	// servers[].url). Empty when the document declares none.
	DocDeclaredHost string

	// Origin is the provenance of the document this draft came from.
	Origin UntrustedOrigin
	// Advisory carries the QUARANTINED chunks — untrusted text that reads as an
	// instruction, held back by IngestUntrustedDoc. It is shown in a separate
	// region and never enters a decision field.
	//
	// Plain descriptive prose (operation summaries, model narrative) has no
	// place here either: descriptive untrusted text is deliberately not
	// rendered on the authorization card at all. It belongs to the
	// capability-description surface (#738), not to a grant decision.
	Advisory []QuarantinedChunk

	// Previous is the published state, or nil when creating.
	Previous *PublishedState

	// Functions are the sandbox operation functions this draft would publish
	// (Epic #726 / #739). They belong to the DECISION, not to decoration: the
	// approval covers each function's code hash, its declared egress set and
	// the behaviour rehearsal, because for AI-authored code the behaviour is
	// what a human is actually approving.
	Functions []ConfirmFunction
}

// ConfirmFunction is one operation function on the confirmation card.
type ConfirmFunction struct {
	Name       string                `json:"name"`
	ToolName   string                `json:"tool_name"`
	Runtime    string                `json:"runtime"`
	Code       string                `json:"code"`
	CodeHash   string                `json:"code_hash"`
	Idempotent bool                  `json:"idempotent"`
	Enabled    bool                  `json:"enabled"`
	Egress     []string              `json:"egress"`
	Lint       []sandbox.LintFinding `json:"lint,omitempty"`
	// Rehearsal is the system-run behaviour preview. Nil means the preview
	// could not be produced, which is itself a fact the reviewer must see.
	Rehearsal *RehearsalReport `json:"rehearsal,omitempty"`
	// IsNew distinguishes a first-time grant from a re-publish, so the reviewer
	// knows whether this authority already existed.
	IsNew        bool   `json:"is_new"`
	PreviousHash string `json:"previous_hash,omitempty"`
}

// ConfirmOperation is one operation in the decision area. Method and Path are
// the identity the proxy enforces; Name is a display label (typically the
// document's operationId) with no authority of its own. The document's prose
// about the operation never appears here.
type ConfirmOperation struct {
	// Name is a display label. It may originate from the document, so it must
	// never be read as a security claim — the enforced identity is
	// Method + Path.
	Name     string `json:"name"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
	// HighRisk mirrors whether any risk targets this operation. The
	// authoritative acknowledgement list is ConfirmCardModel.Risks.
	HighRisk bool `json:"high_risk"`
}

// ConfirmScope is one scope_config key in the decision area.
type ConfirmScope struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
	Change string   `json:"change"`
}

// ConfirmDiff is one field-level change against the published state.
type ConfirmDiff struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Change string `json:"change"`
}

// ConfirmRisk is one high-risk item the user must acknowledge individually.
// AckKey is the stable identifier the UI echoes back when the item is ticked.
type ConfirmRisk struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Target is the structured subject: a host, or a "METHOD path" serial, or a
	// scope key. Detail carries the structured counterpart when there is one.
	Target string `json:"target"`
	Detail string `json:"detail,omitempty"`
	AckKey string `json:"ack_key"`
}

// ConfirmCardModel is the only thing the confirmation card renders. Every field
// is derived from a structured source. Advisory is the sole free-text field and
// is displayed outside the decision area.
//
// Display labels vs authority: Title, ConfirmOperation.Name and ConfirmRisk
// targets exist so a human can recognise what they are approving — they carry
// no authority. The fields the proxy actually enforces are the operation
// method/path and the scope values; those are what an approval grants.
type ConfirmCardModel struct {
	Kind string `json:"kind"`
	// Title is a display label for the integration. Callers must derive it from
	// the user or the provider identifier — never from document prose or model
	// narrative, which can mislabel what the operations actually do.
	Title      string             `json:"title"`
	Subtitle   string             `json:"subtitle,omitempty"`
	Origin     UntrustedOrigin    `json:"origin"`
	Operations []ConfirmOperation `json:"operations"`
	// VerifyProbe is the read-only probe the system will issue after publish to
	// verify auth and connectivity. It is egress too, so it belongs in the
	// decision area and the behaviour preview. Nil when the contract names none.
	VerifyProbe *ConfirmOperation  `json:"verify_probe,omitempty"`
	Scopes      []ConfirmScope     `json:"scopes"`
	Diff        []ConfirmDiff      `json:"diff"`
	Risks       []ConfirmRisk      `json:"risks"`
	Advisory    []QuarantinedChunk `json:"advisory,omitempty"`
	// Functions are the sandbox operation functions this version would publish.
	// RequiresFunctionAck is set when at least one of them is new or changed, so
	// the UI can present the group as a unit the reviewer must look at rather
	// than another line of boilerplate.
	Functions           []ConfirmFunction `json:"functions,omitempty"`
	RequiresFunctionAck bool              `json:"requires_function_ack,omitempty"`
}

// BuildConfirmCard renders the confirmation card for a draft. It is
// deterministic: identical inputs produce identical output, so the card can be
// snapshotted and audited.
func BuildConfirmCard(in ConfirmDraftInput) ConfirmCardModel {
	m := ConfirmCardModel{
		Kind:       string(in.Kind),
		Title:      confirmTitle(in),
		Subtitle:   strings.TrimSpace(in.BaseURL),
		Origin:     in.Origin,
		Operations: proposedOperations(in),
		Scopes:     []ConfirmScope{},
		Diff:       []ConfirmDiff{},
		Risks:      []ConfirmRisk{},
		Advisory:   in.Advisory,
	}
	if m.Advisory == nil {
		m.Advisory = []QuarantinedChunk{}
	}

	prevOps := operationSet(publishedOperations(in))
	declared := operationSet(in.DocDeclaredOperations)

	m.Risks = append(m.Risks, operationRisks(m.Operations, prevOps, declared)...)
	m.Risks = append(m.Risks, hostRisks(in)...)
	m.Risks = append(m.Risks, scopeRisks(in)...)
	if in.WriteEnabled {
		m.Risks = append(m.Risks, ConfirmRisk{
			Code:     RiskWriteEnabled,
			Severity: RiskSeverityHigh,
			Target:   structuredName(in),
			AckKey:   RiskWriteEnabled,
		})
	}
	m.Risks = dedupeRisks(m.Risks)
	m.VerifyProbe = resolveVerifyProbe(in, m.Operations)

	// Flag operations that carry a risk so the UI can badge the row. The
	// authoritative ack list remains m.Risks.
	riskyOps := map[string]bool{}
	for _, r := range m.Risks {
		riskyOps[r.Target] = true
	}
	for i := range m.Operations {
		if riskyOps[opSerial(m.Operations[i].Method, m.Operations[i].Path)] {
			m.Operations[i].HighRisk = true
		}
	}

	m.Scopes = buildScopes(in)
	m.Diff = buildDiff(in, m.Operations)

	// Operation functions (Epic #726 / #739). They are rendered in full — code,
	// declared reach and the behaviour rehearsal — because for AI-authored code
	// the behaviour is the thing being approved. A missing rehearsal is
	// surfaced as a risk rather than hidden: "we could not preview it" is
	// exactly what a reviewer needs to know.
	m.Functions = in.Functions
	for i := range m.Functions {
		f := &m.Functions[i]
		if f.IsNew || f.PreviousHash != f.CodeHash {
			m.RequiresFunctionAck = true
		}
		if f.Rehearsal == nil {
			m.RequiresFunctionAck = true
			m.Risks = append(m.Risks, ConfirmRisk{
				Code:     RiskFunctionUnrehearsed,
				Severity: RiskSeverityHigh,
				Target:   f.Name,
				Detail:   "no behaviour rehearsal was produced for this function",
				AckKey:   RiskFunctionUnrehearsed + ":" + f.Name,
			})
			continue
		}
		if len(f.Rehearsal.UndeclaredOps) > 0 {
			m.Risks = append(m.Risks, ConfirmRisk{
				Code:     RiskFunctionUndeclaredOp,
				Severity: RiskSeverityHigh,
				Target:   f.Name,
				Detail:   "during rehearsal it called " + strings.Join(f.Rehearsal.UndeclaredOps, ", ") + " without declaring it",
				AckKey:   RiskFunctionUndeclaredOp + ":" + f.Name,
			})
		}
	}
	m.Risks = dedupeRisks(m.Risks)
	return m
}

// BehaviorPreview is the egress behaviour a publish will actually produce: the
// complete list of method+path templates that leave the machine, including the
// post-publish verification probe. The sandbox function runtime (#739) derives
// its preview from this same value, so "what the user approved" and "what runs"
// cannot diverge. It is a pure function of the decision fields — untrusted text
// cannot influence it.
type BehaviorPreview struct {
	Egress []EgressEntry `json:"egress"`
}

// EgressEntry is one outbound call shape the proxy may perform.
type EgressEntry struct {
	Method       string `json:"method"`
	PathTemplate string `json:"path_template"`
	ReadOnly     bool   `json:"read_only"`
}

// BuildBehaviorPreview derives the egress preview from the operations it is
// given. The slice must contain every operation the integration will execute —
// the granted operations plus the verify probe — so the preview is complete.
func BuildBehaviorPreview(ops []ConfirmOperation) BehaviorPreview {
	p := BehaviorPreview{Egress: []EgressEntry{}}
	for _, op := range ops {
		p.Egress = append(p.Egress, EgressEntry{
			Method:       op.Method,
			PathTemplate: op.Path,
			ReadOnly:     op.ReadOnly,
		})
	}
	return p
}

// BehaviorPreview returns this card's egress preview: every granted operation
// plus the post-publish verification probe.
func (m ConfirmCardModel) BehaviorPreview() BehaviorPreview {
	ops := m.Operations
	if m.VerifyProbe != nil {
		ops = append(append([]ConfirmOperation{}, m.Operations...), *m.VerifyProbe)
	}
	return BuildBehaviorPreview(ops)
}

// resolveVerifyProbe finds the operation the contract names in verify_operation.
// It is resolved from the decision-area operations, so it inherits their
// structured identity and can never be invented by free text. An unknown name
// yields no probe (LintContract already rejects that case).
func resolveVerifyProbe(in ConfirmDraftInput, ops []ConfirmOperation) *ConfirmOperation {
	if in.Contract == nil {
		return nil
	}
	name := strings.TrimSpace(in.Contract.VerifyOperation)
	if name == "" {
		return nil
	}
	for i := range ops {
		if ops[i].Name == name {
			probe := ops[i]
			return &probe
		}
	}
	return nil
}

// AckKeys returns every acknowledgement key the user must tick before the card
// may be approved, in card order.
func (m ConfirmCardModel) AckKeys() []string {
	keys := make([]string, 0, len(m.Risks))
	for _, r := range m.Risks {
		keys = append(keys, r.AckKey)
	}
	return keys
}

// DecisionPayload marshals the decision area alone, dropping the advisory
// region. A reviewer approves exactly these bytes. A regression test asserts
// that untrusted text can never appear here.
func DecisionPayload(m ConfirmCardModel) ([]byte, error) {
	m.Advisory = nil
	return json.Marshal(m)
}

// --- construction helpers -------------------------------------------------

func confirmTitle(in ConfirmDraftInput) string {
	for _, s := range []string{in.Label, in.Name} {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return string(in.Kind)
}

// structuredName is the integration's identifier — the name credentials and the
// API reference. Unlike the display label it is never model-authored, so it is
// the right subject for a risk that must name its target unambiguously.
func structuredName(in ConfirmDraftInput) string {
	if name := strings.TrimSpace(in.Name); name != "" {
		return name
	}
	return string(in.Kind)
}

// proposedOperations lists the provider-line operations, sorted by method then
// path so the card (and its digest) is order-stable.
func proposedOperations(in ConfirmDraftInput) []ConfirmOperation {
	ops := []ConfirmOperation{}
	if in.Contract == nil {
		return ops
	}
	for _, op := range in.Contract.Operations {
		ops = append(ops, ConfirmOperation{
			Name:     op.Name,
			Method:   strings.ToUpper(strings.TrimSpace(op.Method)),
			Path:     op.Path,
			ReadOnly: op.ReadOnly,
		})
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Method != ops[j].Method {
			return ops[i].Method < ops[j].Method
		}
		return ops[i].Path < ops[j].Path
	})
	return ops
}

func publishedOperations(in ConfirmDraftInput) []OperationKey {
	if in.Previous == nil {
		return nil
	}
	return in.Previous.Operations
}

func operationRisks(ops []ConfirmOperation, prev, declared map[string]bool) []ConfirmRisk {
	var risks []ConfirmRisk
	for _, op := range ops {
		serial := opSerial(op.Method, op.Path)
		if !op.ReadOnly && !prev[serial] {
			risks = append(risks, ConfirmRisk{
				Code: RiskNewWriteOperation, Severity: RiskSeverityHigh,
				Target: serial, AckKey: RiskNewWriteOperation + ":" + serial,
			})
		}
		if len(declared) > 0 && !declared[serial] {
			risks = append(risks, ConfirmRisk{
				Code: RiskOperationNotInDocument, Severity: RiskSeverityHigh,
				Target: serial, AckKey: RiskOperationNotInDocument + ":" + serial,
			})
		}
	}
	return risks
}

// hostRisks flags a proposed host that differs from either the published host
// or the host the source document itself declares.
func hostRisks(in ConfirmDraftInput) []ConfirmRisk {
	proposed := hostOf(in.BaseURL)
	if proposed == "" {
		return nil
	}
	var detail string
	switch prev := hostOf(publishedBaseURL(in)); {
	case prev != "" && prev != proposed:
		detail = prev
	default:
		if docHost := hostOf(in.DocDeclaredHost); docHost != "" && docHost != proposed {
			detail = docHost
		}
	}
	if detail == "" {
		return nil
	}
	return []ConfirmRisk{{
		Code: RiskCrossOriginHost, Severity: RiskSeverityHigh,
		Target: proposed, Detail: detail,
		AckKey: RiskCrossOriginHost + ":" + proposed,
	}}
}

func publishedBaseURL(in ConfirmDraftInput) string {
	if in.Previous == nil {
		return ""
	}
	return in.Previous.BaseURL
}

// scopeRisks flags scope keys granting values outside the baseline. On first
// publish the baseline is the minimal default; afterwards it is the published
// config. Narrowing is never a risk.
func scopeRisks(in ConfirmDraftInput) []ConfirmRisk {
	if in.ScopeConfig == nil {
		return nil
	}
	baseline := MinimalScopeDefaults(in.SourceKind)
	if in.Previous != nil && in.Previous.ScopeConfig != nil {
		baseline = in.Previous.ScopeConfig
	}
	var risks []ConfirmRisk
	for _, key := range sortedScopeKeys(in.ScopeConfig) {
		added := valuesOutside(scopeValues(in.ScopeConfig, key), scopeValues(baseline, key))
		if len(added) == 0 {
			continue
		}
		risks = append(risks, ConfirmRisk{
			Code: RiskScopeWidened, Severity: RiskSeverityHigh,
			Target: key, Detail: strings.Join(added, ", "),
			AckKey: RiskScopeWidened + ":" + key,
		})
	}
	return risks
}

func buildScopes(in ConfirmDraftInput) []ConfirmScope {
	scopes := []ConfirmScope{}
	if in.ScopeConfig == nil {
		return scopes
	}
	baseline := MinimalScopeDefaults(in.SourceKind)
	if in.Previous != nil && in.Previous.ScopeConfig != nil {
		baseline = in.Previous.ScopeConfig
	}
	for _, key := range sortedScopeKeys(in.ScopeConfig) {
		next := scopeValues(in.ScopeConfig, key)
		prior := scopeValues(baseline, key)
		scopes = append(scopes, ConfirmScope{
			Key:    key,
			Values: next,
			Change: scopeChange(next, prior),
		})
	}
	return scopes
}

func scopeChange(next, prior []string) string {
	added := valuesOutside(next, prior)
	removed := valuesOutside(prior, next)
	switch {
	case len(added) > 0 && len(prior) == 0:
		return ChangeAdded
	case len(added) > 0:
		return ChangeWidened
	case len(removed) > 0:
		return ChangeNarrowed
	default:
		return ChangeUnchanged
	}
}

// functionScopeDiff compares the authority the proposed functions hold with the
// authority the live ones hold. Field names use the "scope." prefix so the
// existing rule — any scope-prefixed diff means the change must be
// re-confirmed — covers function authority without a second mechanism.
func functionScopeDiff(next []ConfirmFunction, prev []FunctionScopeEntry) []ConfirmDiff {
	prevBy := map[string]FunctionScopeEntry{}
	for _, f := range prev {
		prevBy[f.Name] = f
	}
	nextBy := map[string]bool{}
	var diff []ConfirmDiff
	for _, f := range next {
		nextBy[f.Name] = true
		field := "scope.function." + f.Name
		before, existed := prevBy[f.Name]
		entry := FunctionScopeEntry{Name: f.Name, Enabled: f.Enabled, Idempotent: f.Idempotent, Egress: f.Egress, CodeHash: f.CodeHash}
		if !existed {
			diff = append(diff, ConfirmDiff{Field: field, After: scopeEntryString(entry), Change: ChangeAdded})
			continue
		}
		// Only AUTHORITY changes are diffed: which operations it may reach, and
		// whether it may be replayed. A pure code edit with the same reach is
		// reviewed on the card but does not by itself re-grant anything.
		if scopeEntryString(entry) != scopeEntryString(before) {
			diff = append(diff, ConfirmDiff{
				Field: field, Before: scopeEntryString(before), After: scopeEntryString(entry),
				Change: ChangeChanged,
			})
		}
	}
	for _, f := range prev {
		if !nextBy[f.Name] {
			diff = append(diff, ConfirmDiff{
				Field: "scope.function." + f.Name, Before: scopeEntryString(f), Change: ChangeRemoved,
			})
		}
	}
	return diff
}

func scopeEntryString(e FunctionScopeEntry) string {
	ops := append([]string(nil), e.Egress...)
	sort.Strings(ops)
	s := strings.Join(ops, ",")
	if e.Idempotent {
		s += " (idempotent)"
	}
	if !e.Enabled {
		s += " (disabled)"
	}
	return s
}

func buildDiff(in ConfirmDraftInput, ops []ConfirmOperation) []ConfirmDiff {
	diff := []ConfirmDiff{}
	prev := in.Previous

	prevURL := publishedBaseURL(in)
	if prevURL != in.BaseURL {
		change := ChangeChanged
		if prevURL == "" {
			change = ChangeAdded
		}
		diff = append(diff, ConfirmDiff{
			Field: "base_url", Before: prevURL, After: in.BaseURL, Change: change,
		})
	}

	prevSet := operationSet(publishedOperations(in))
	nextSet := map[string]bool{}
	for _, op := range ops {
		serial := opSerial(op.Method, op.Path)
		nextSet[serial] = true
		if !prevSet[serial] {
			diff = append(diff, ConfirmDiff{Field: "operation", After: serial, Change: ChangeAdded})
		}
	}
	for _, op := range publishedOperations(in) {
		serial := opSerial(op.Method, op.Path)
		if !nextSet[serial] {
			diff = append(diff, ConfirmDiff{Field: "operation", Before: serial, Change: ChangeRemoved})
		}
	}
	if prev != nil {
		diff = append(diff, scopeDiff(in.ScopeConfig, prev.ScopeConfig)...)
		diff = append(diff, functionScopeDiff(in.Functions, prev.FunctionScope)...)
	}
	sort.Slice(diff, func(i, j int) bool {
		if diff[i].Field != diff[j].Field {
			return diff[i].Field < diff[j].Field
		}
		return diff[i].Before+diff[i].After < diff[j].Before+diff[j].After
	})
	return diff
}

// scopeDiff reports per-key scope changes against the published config. It
// walks the union of both key sets so a scope key revoked outright (present
// before, gone now) still shows up as a removal instead of vanishing.
func scopeDiff(next, prev map[string]any) []ConfirmDiff {
	diff := []ConfirmDiff{}
	for _, key := range unionScopeKeys(next, prev) {
		added := valuesOutside(scopeValues(next, key), scopeValues(prev, key))
		removed := valuesOutside(scopeValues(prev, key), scopeValues(next, key))
		if len(added) > 0 {
			diff = append(diff, ConfirmDiff{
				Field: "scope." + key, After: strings.Join(added, ", "), Change: ChangeAdded,
			})
		}
		if len(removed) > 0 {
			diff = append(diff, ConfirmDiff{
				Field: "scope." + key, Before: strings.Join(removed, ", "), Change: ChangeRemoved,
			})
		}
	}
	return diff
}

// --- small helpers --------------------------------------------------------

func opSerial(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + path
}

func operationSet(ops []OperationKey) map[string]bool {
	set := make(map[string]bool, len(ops))
	for _, op := range ops {
		set[opSerial(op.Method, op.Path)] = true
	}
	return set
}

func dedupeRisks(risks []ConfirmRisk) []ConfirmRisk {
	seen := map[string]bool{}
	out := make([]ConfirmRisk, 0, len(risks))
	for _, r := range risks {
		if seen[r.AckKey] {
			continue
		}
		seen[r.AckKey] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// hostOf returns the lower-cased host of a raw URL, or "" when unparseable.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

func sortedScopeKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unionScopeKeys returns the sorted union of both maps' keys.
func unionScopeKeys(a, b map[string]any) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scopeValues reads a scope key as a sorted, de-duplicated string list. The
// result is always non-nil: a scope key that grants nothing must serialize as
// `[]`, never `null` — the card model's wire shape is part of what consumers
// render, and a null list is not a list.
func scopeValues(m map[string]any, key string) []string {
	vals := []string{}
	raw, ok := m[key]
	if !ok {
		return vals
	}
	switch v := raw.(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				vals = append(vals, s)
			}
		}
	case []string:
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				vals = append(vals, s)
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			vals = append(vals, v)
		}
	}
	sort.Strings(vals)
	return vals
}

// valuesOutside returns next's values that are absent from baseline.
func valuesOutside(next, baseline []string) []string {
	have := make(map[string]bool, len(baseline))
	for _, b := range baseline {
		have[b] = true
	}
	out := []string{}
	for _, n := range next {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}
