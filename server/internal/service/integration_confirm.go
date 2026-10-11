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
	// Advisory is untrusted free text (doc prose, model narrative). It is shown
	// in a separate region and never enters a decision field.
	Advisory []QuarantinedChunk

	// Previous is the published state, or nil when creating.
	Previous *PublishedState
}

// ConfirmOperation is one operation in the decision area. It carries only the
// fields the proxy enforces; the document's prose about the operation stays in
// the advisory region.
type ConfirmOperation struct {
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
type ConfirmCardModel struct {
	Kind       string             `json:"kind"`
	Title      string             `json:"title"`
	Subtitle   string             `json:"subtitle,omitempty"`
	Origin     UntrustedOrigin    `json:"origin"`
	Operations []ConfirmOperation `json:"operations"`
	Scopes     []ConfirmScope     `json:"scopes"`
	Diff       []ConfirmDiff      `json:"diff"`
	Risks      []ConfirmRisk      `json:"risks"`
	Advisory   []QuarantinedChunk `json:"advisory,omitempty"`
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
			Target:   confirmTitle(in),
			AckKey:   RiskWriteEnabled,
		})
	}
	m.Risks = dedupeRisks(m.Risks)

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
	return m
}

// BehaviorPreview is the egress behaviour a publish will actually produce: the
// complete list of method+path templates that leave the machine. The sandbox
// function runtime (#739) derives its preview from this same value, so "what
// the user approved" and "what runs" cannot diverge. It is a pure function of
// the decision fields — untrusted text cannot influence it.
type BehaviorPreview struct {
	Egress []EgressEntry `json:"egress"`
}

// EgressEntry is one outbound call shape the proxy may perform.
type EgressEntry struct {
	Method       string `json:"method"`
	PathTemplate string `json:"path_template"`
	ReadOnly     bool   `json:"read_only"`
}

// BuildBehaviorPreview derives the egress preview from the decision-area
// operations alone.
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

// BehaviorPreview returns this card's egress preview.
func (m ConfirmCardModel) BehaviorPreview() BehaviorPreview {
	return BuildBehaviorPreview(m.Operations)
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
	}
	sort.Slice(diff, func(i, j int) bool {
		if diff[i].Field != diff[j].Field {
			return diff[i].Field < diff[j].Field
		}
		return diff[i].Before+diff[i].After < diff[j].Before+diff[j].After
	})
	return diff
}

func scopeDiff(next, prev map[string]any) []ConfirmDiff {
	diff := []ConfirmDiff{}
	for _, key := range sortedScopeKeys(next) {
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

// scopeValues reads a scope key as a sorted, de-duplicated string list.
func scopeValues(m map[string]any, key string) []string {
	raw, ok := m[key]
	if !ok {
		return []string{}
	}
	var vals []string
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
