// Package service — untrusted-input discipline for the integration line.
//
// Every byte that originates outside niuniu — an OpenAPI document, a README,
// an HTML page, a pasted spec, or a model's own prose — is untrusted input.
// Untrusted text is data, never instruction: it inherits no permission, it can
// never widen a scope, and it never reaches the decision area of a
// confirmation card. The document→draft parser (#731) and the draft pipeline
// (#734) both apply the rules in this file.
//
// Scope note (deliberately narrow): this discipline governs the integration
// authorization surface — integration drafts, confirmation cards, credential
// submission, team approval, and sandbox-function publish previews — judged by
// "is this output used as the basis for a permission/credential/security
// decision?". It does NOT govern coding work inside a workspace: agent-written
// code files land on disk and go through the existing engineering flow (git
// diff, review column, harness gates, pre-commit checks). Free-text agent
// output (chat answers, analysis docs, proposals) is likewise unaffected.
//
// The guarantee is structural, not heuristic. The imperative-text classifier
// below is defence in depth; the load-bearing rule lives in
// integration_confirm.go — a draft's instruction surface (operations, scope,
// host, auth) is populated only from structured fields, so free text has no
// path into it even when the classifier fails to recognise an injection.
package service

import (
	"strings"
	"time"
)

// UntrustedOrigin records where a piece of doc-derived content came from, so
// provenance travels with the content all the way to the confirmation card.
type UntrustedOrigin struct {
	Kind string `json:"kind"`
	// Ref is the fetch location (URL or file name), empty when unknown.
	Ref string `json:"ref,omitempty"`
	// FetchedAt is RFC3339 UTC, empty when the content was not fetched live.
	FetchedAt string `json:"fetched_at,omitempty"`
}

// Provenance kinds.
const (
	UntrustedKindOpenAPI  = "openapi"
	UntrustedKindMarkdown = "markdown"
	UntrustedKindHTML     = "html"
	UntrustedKindNote     = "user_note"
	UntrustedKindModel    = "model"
)

// NewUntrustedOrigin stamps provenance at fetch time. An unknown or empty kind
// degrades to UntrustedKindNote — content without a recognised origin is still
// untrusted, never trusted by omission.
func NewUntrustedOrigin(kind, ref string) UntrustedOrigin {
	if !knownUntrustedKind(kind) {
		kind = UntrustedKindNote
	}
	return UntrustedOrigin{
		Kind:      kind,
		Ref:       ref,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func knownUntrustedKind(k string) bool {
	switch k {
	case UntrustedKindOpenAPI, UntrustedKindMarkdown, UntrustedKindHTML,
		UntrustedKindNote, UntrustedKindModel:
		return true
	}
	return false
}

// DocChunk is one labelled slice of untrusted text from a source document.
// Where is a structural locator inside the source (e.g.
// "paths./pets.get.description") so a reviewer can be pointed at the origin.
type DocChunk struct {
	Where string `json:"where"`
	Text  string `json:"text"`
}

// DocTextClass is the classifier verdict for a chunk of untrusted text.
type DocTextClass string

const (
	// DocTextDeclarative is descriptive prose about the integration.
	DocTextDeclarative DocTextClass = "declarative"
	// DocTextImperative is text addressed at an agent — an instruction. It is
	// quarantined: shown to a human, never executed, never parsed into a field.
	DocTextImperative DocTextClass = "imperative"
)

// Quarantine reasons. Structured codes, localised by the UI; they say why the
// text was held back, not what it says.
const (
	QuarantineInstructionOverride = "instruction_override"
	QuarantineAgentAddressed      = "agent_addressed"
	QuarantinePrivilegeEscalation = "privilege_escalation"
	QuarantineExfiltration        = "exfiltration"
)

// imperativePatterns is a coarse, deliberately over-inclusive matcher for text
// addressed at an agent rather than describing an API. Substrings are matched
// case-insensitively; CJK entries are unaffected by lower-casing.
//
// Over-inclusion is the correct failure direction: a false positive costs a
// reviewer one glance at the quarantine region, a false negative is caught by
// the structural rule that free text can never populate a decision field.
var imperativePatterns = []struct {
	reason string
	subs   []string
}{
	{QuarantineInstructionOverride, []string{
		"ignore previous", "ignore all previous", "ignore the above", "ignore prior",
		"disregard previous", "disregard all previous", "disregard the above",
		"forget previous", "forget all previous", "override previous",
		"忽略之前", "忽略以上", "忽略上述", "忽略前面", "无视以上", "无视之前", "覆盖之前",
	}},
	{QuarantineAgentAddressed, []string{
		"you must", "you should now", "you are required to", "as an ai",
		"assistant:", "system:", "<system>", "### instruction", "new instructions",
		"你必须", "请务必", "请立即", "你现在必须", "注意：必须",
	}},
	{QuarantinePrivilegeEscalation, []string{
		"grant ", "granted scope", "whitelist", "enable write", "allow write",
		"add a new operation", "add an operation", "escalate", "expand scope",
		"扩权", "提权", "授予", "放开写", "启用写", "添加操作", "新增操作", "扩大范围",
	}},
	{QuarantineExfiltration, []string{
		"do not tell", "without telling", "do not mention", "don't mention",
		"secretly", "silently",
		"不要告诉", "不要提及", "静默", "悄悄",
	}},
}

// ClassifyDocText decides whether a chunk of untrusted text describes an
// integration (declarative) or instructs an agent (imperative). It returns the
// class and, for imperative text, the quarantine reason.
func ClassifyDocText(text string) (DocTextClass, string) {
	lower := strings.ToLower(text)
	for _, p := range imperativePatterns {
		for _, sub := range p.subs {
			if strings.Contains(lower, sub) {
				return DocTextImperative, p.reason
			}
		}
	}
	return DocTextDeclarative, ""
}

// QuarantinedChunk is untrusted text that reads as an instruction. It may be
// displayed to a human; it is never executed and never parsed into a field.
type QuarantinedChunk struct {
	Where  string `json:"where"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// UntrustedDoc is the partitioned result of ingesting one untrusted source:
// descriptive chunks stay available as context, imperative chunks are
// quarantined. The partition is a presentation aid — both halves remain
// untrusted, and neither may populate a decision field.
type UntrustedDoc struct {
	Origin      UntrustedOrigin    `json:"origin"`
	Facts       []DocChunk         `json:"facts"`
	Quarantined []QuarantinedChunk `json:"quarantined"`
}

// IngestUntrustedDoc partitions raw doc chunks under a single source origin.
// The returned slices are always non-nil so the wire shape is [] rather than
// null.
func IngestUntrustedDoc(origin UntrustedOrigin, chunks []DocChunk) UntrustedDoc {
	doc := UntrustedDoc{
		Origin:      origin,
		Facts:       []DocChunk{},
		Quarantined: []QuarantinedChunk{},
	}
	for _, c := range chunks {
		if class, reason := ClassifyDocText(c.Text); class == DocTextImperative {
			doc.Quarantined = append(doc.Quarantined, QuarantinedChunk{
				Where: c.Where, Text: c.Text, Reason: reason,
			})
			continue
		}
		doc.Facts = append(doc.Facts, c)
	}
	return doc
}

// MinimalScopeDefaults returns the smallest scope_config a freshly onboarded
// data source starts from, per source kind. Nothing is granted until the user
// grants it — a document's own "required scopes" section never widens this.
//
// For http sources the method allow-list starts at GET, which is the read
// floor rather than a grant; every other kind starts with no database and no
// object allowed.
func MinimalScopeDefaults(sourceKind string) map[string]any {
	base := map[string]any{
		"databases":    []any{},
		"tables_allow": []any{},
		"tables_deny":  []any{},
	}
	if strings.EqualFold(strings.TrimSpace(sourceKind), "http") {
		base["methods"] = []any{"GET"}
	}
	return base
}
