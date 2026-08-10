// Package integration defines the Provider abstraction used to talk to
// external issue trackers (GitHub Issues today, TAPD next). Concrete
// adapters live in subpackages (integration/github, integration/tapd)
// and are registered into a Registry constructed at server boot.
//
// Credential carries the decrypted token / config. It is intentionally
// confined to the service layer — handlers receive only the caller's
// user id and let service code load + decrypt the right credential
// before passing it to a Provider call.
package integration

import (
	"context"
	"fmt"
)

// ProviderName identifies a provider implementation.
type ProviderName string

const (
	ProviderGitHub ProviderName = "github"
	ProviderJira   ProviderName = "jira"
	ProviderTAPD   ProviderName = "tapd"
)

// Credential is the decrypted form passed to provider adapters.
// It MUST NOT escape the service layer; handlers never see it.
type Credential struct {
	Provider ProviderName
	// Config holds provider-specific fields:
	//   GitHub: { token, scopes? }
	//   TAPD:   { workspace_id, api_user, api_password }
	Config map[string]any
}

// ListOpts narrows a list-issues call.
type ListOpts struct {
	Query   string
	State   string // "open" | "closed" | "all"
	Labels  []string
	Page    int
	PerPage int
}

// ExternalIssue is the normalized external issue shape.
//
// JSON tags are mandatory — the Browse handler emits these objects directly
// to the SPA, whose `types/integration.ts` ExternalIssueSummary expects
// snake_case fields (`labels`, `external_id`, `updated_at` …). Without tags
// Go marshals as PascalCase (`Labels`), and the frontend reads `item.labels`
// as `undefined`, crashing with `Cannot read properties of undefined
// (reading 'length')` on the labels-pill render path.
type ExternalIssue struct {
	ExternalID  string   `json:"external_id"` // canonical id: "owner/repo#123" for GitHub
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"` // "open" | "closed"
	Labels      []string `json:"labels"`
	Author      string   `json:"author"`
	UpdatedAt   string   `json:"updated_at"` // RFC3339
}

// IssuePage is the paginated list response. Frontend hook
// `useExternalIssues` expects `items` not `Issues`, hence the json tag.
type IssuePage struct {
	Issues   []ExternalIssue `json:"items"`
	NextPage int             `json:"next_page"` // 0 if no next
	Total    int             `json:"total"`     // -1 if unknown
}

// ExternalComment is the normalized shape of a comment fetched from an
// external system. Used by ListComments and serialized into the
// `external_comments_snapshot` JSON column on issues for read-only
// display in the SPA (v1.1 — never persisted into niuniu's
// issue_comments table; not editable; not part of the writeback path).
type ExternalComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"` // as-provided by source (RFC3339 for GitHub, "YYYY-MM-DD HH:MM:SS" for TAPD)
	URL       string `json:"url"`        // direct link to the comment in the external UI
}

// ListCommentsOpts narrows the comment query. Limit defaults to 50.
type ListCommentsOpts struct {
	Limit int
}

// === L4 cross-provider types (2026-05) ===

// SearchFilters narrows a cross-provider work item search. Empty fields
// mean "no filter". Status values are normalized (workitem.Status); the
// adapter must accept these and translate to the provider's native
// filter shape.
type SearchFilters struct {
	Query   string   // free-text
	Sources []string // ["github","jira","tapd"]; empty = all configured for the caller
	Status  []string // normalized statuses, e.g. ["open","in_progress"]
	Labels  []string // OR-match across labels (including injected "type:*")
	// Assignee is a per-provider external user id, or the "me" sentinel.
	// "me" means "the calling niuniu user's mapped external_user on this
	// provider" — adapters may honor it natively (GitHub's @me, Jira's
	// currentUser()) or leave it for the service layer to resolve. Leave
	// empty for "no assignee filter".
	Assignee string
	// StatusNormalized is the single-value form of Status used by the
	// MCP list_my_assignments / get_recent_activity tools. Values are
	// the workitem.Status constants:
	// open|in_progress|in_review|blocked|done|cancelled. When set, the
	// adapter should honor it as if Status had a single entry; adapters
	// that already use Status can OR the two together.
	StatusNormalized string
	// Since is an RFC3339 timestamp. When set, adapters should restrict
	// results to items updated at or after this moment. Format is
	// validated upstream (REST handler / MCP tool); adapters can pass
	// the raw string through to provider-native filters.
	Since string
	// Sort is a per-result-set ordering hint. Values: "recent" (most
	// recently updated first), "updated" (alias for recent), or empty
	// (adapter default). Adapters that don't natively sort may ignore.
	Sort  string
	Limit int // 0 = adapter default (typically 30)
}

// ExternalUserRef is the raw per-provider user reference. The service
// layer maps these to a normalized identity via external_user_identities
// before surfacing to AI.
type ExternalUserRef struct {
	Provider     string `json:"provider"`
	ExternalUser string `json:"external_user"`
	DisplayName  string `json:"display_name"`
}

// WorkItemSummary is the cross-provider work item shape returned by
// SearchWorkItems. Adapters MUST populate StatusNormalized (matching
// workitem.Status constants) and include the injected "type:<Type>"
// label as the first labels element.
type WorkItemSummary struct {
	Ref              string            `json:"ref"`    // "github:owner/repo#123"
	Source           string            `json:"source"` // "github" | "jira" | "tapd"
	Type             string            `json:"type"`   // provider-original
	Title            string            `json:"title"`
	Status           string            `json:"status"`            // provider-original
	StatusNormalized string            `json:"status_normalized"` // matches workitem.Status
	Priority         string            `json:"priority"`          // "p0"|"p1"|"p2"|"p3"|""
	Assignees        []ExternalUserRef `json:"assignees"`
	Labels           []string          `json:"labels"`     // includes "type:<Type>" at index 0
	UpdatedAt        string            `json:"updated_at"` // RFC3339
	URL              string            `json:"url"`
}

// WorkItemDetail is the full shape returned by GetWorkItemDetail.
type WorkItemDetail struct {
	WorkItemSummary
	Body     string            `json:"body"`
	Reporter ExternalUserRef   `json:"reporter"`
	Comments []ExternalComment `json:"comments"`
	Raw      map[string]any    `json:"raw,omitempty"` // provider-specific escape hatch
}

// ErrorKind classifies provider errors. Adapters must map every
// non-2xx response into one of these kinds so callers can decide
// whether to retry, surface to the user, or back off.
type ErrorKind string

const (
	ErrInvalid     ErrorKind = "invalid"      // 401/403/revoked
	ErrRateLimited ErrorKind = "rate_limited" // 403 + remaining=0
	ErrNotFound    ErrorKind = "not_found"
	ErrTransient   ErrorKind = "transient" // 5xx / network
	ErrUnknown     ErrorKind = "unknown"
)

// ProviderError is the normalized error type all adapters must return.
// Message is safe to surface to end users (already redacted); Detail
// is for logs only and may carry sensitive request/response fragments.
type ProviderError struct {
	Kind    ErrorKind
	Status  int
	Message string // user-facing, redacted
	Detail  string // log-only, may contain sensitive fields
	ResetAt string // RFC3339 when Kind == ErrRateLimited
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error [%s] status=%d: %s", e.Kind, e.Status, e.Message)
}

// Provider is implemented by each external system adapter (github,
// tapd, …). All methods take ctx and a decrypted Credential; adapters
// must not log the token. AddComment / CloseIssue are stubbed in M0
// and filled in M3.
type Provider interface {
	Name() ProviderName

	VerifyCredential(ctx context.Context, cred Credential) error

	ListIssues(ctx context.Context, cred Credential, sourceKey string, opts ListOpts) (*IssuePage, error)
	GetIssue(ctx context.Context, cred Credential, sourceKey, externalID string) (*ExternalIssue, error)

	// AddComment: caller MUST include the niuniu-writeback-key marker
	// in body for idempotency (see spec §7.2).
	// Returns external comment id.
	AddComment(ctx context.Context, cred Credential, sourceKey, externalID, body string) (string, error)
	CloseIssue(ctx context.Context, cred Credential, sourceKey, externalID, finalCommentBody string) error

	// ListComments fetches recent comments on an external issue (v1.1).
	// Caller-side: invoked from RefreshFromExternal as a best-effort
	// secondary fetch — failures should not abort the title/description
	// refresh. Most-recent-first ordering preferred when the source supports
	// it; default limit 50 when opts.Limit <= 0.
	ListComments(ctx context.Context, cred Credential, sourceKey, externalID string, opts ListCommentsOpts) ([]ExternalComment, error)

	// === L4 intent-tool methods (2026-05) ===

	// SearchWorkItems returns work items matching the filters, normalized
	// into WorkItemSummary shape. Adapters MUST set StatusNormalized
	// (one of workitem.Status constants) and include "type:<Type>" as the
	// first labels element. sourceKey may be empty to mean "all configured
	// keys for this provider".
	SearchWorkItems(ctx context.Context, cred Credential, sourceKey string, filters SearchFilters) ([]WorkItemSummary, error)

	// GetWorkItemDetail returns full detail including comments.
	GetWorkItemDetail(ctx context.Context, cred Credential, sourceKey, externalID string) (*WorkItemDetail, error)

	// UpdateStatus changes the work item state. target uses normalized
	// values (open|in_progress|in_review|blocked|done|cancelled); the
	// adapter is responsible for mapping to the provider's native state.
	UpdateStatus(ctx context.Context, cred Credential, sourceKey, externalID, target string) error

	// UpdateAssignees adds and/or removes assignees. Identifiers are
	// provider-native (GitHub login, Jira accountId, TAPD user id).
	UpdateAssignees(ctx context.Context, cred Credential, sourceKey, externalID string, add, remove []string) error

	// UpdateLabels adds and/or removes labels. The injected "type:*"
	// labels are server-side only — adapters must filter them out before
	// sending to the provider.
	UpdateLabels(ctx context.Context, cred Credential, sourceKey, externalID string, add, remove []string) error
}
