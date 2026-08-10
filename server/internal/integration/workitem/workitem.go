// Package workitem defines cross-provider helpers and status constants
// used by L4 intent tools. The provider boundary types (SearchFilters,
// WorkItemSummary, WorkItemDetail, ExternalUserRef) live in the parent
// integration package; this package holds only what providers and tests
// need without dragging the full Provider interface along.
//
// See docs/superpowers/plans/2026-05-15-l4-mcp-intent-tools.md.
package workitem

import "fmt"

// Status is the niuniu-normalized status. Each provider maps its native
// statuses to one of these. The original provider value travels as
// WorkItemSummary.Status; the normalized value as
// WorkItemSummary.StatusNormalized.
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusInReview   Status = "in_review"
	StatusBlocked    Status = "blocked"
	StatusDone       Status = "done"
	StatusCancelled  Status = "cancelled"
)

// FormatRef produces the canonical cross-provider reference string.
// Format: "<source>:<external_id>". The external_id is provider-defined:
//   - GitHub: "owner/repo#123"
//   - Jira:   "PROJ-456"
//   - TAPD:   "<workspace>/<id>"
func FormatRef(source, externalID string) string {
	return fmt.Sprintf("%s:%s", source, externalID)
}

// ParseRef splits a ref back into (source, externalID). Returns
// ("", "", false) if the format is invalid (missing ':').
func ParseRef(ref string) (source, externalID string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == ':' {
			return ref[:i], ref[i+1:], true
		}
	}
	return "", "", false
}

// InjectTypeLabel prepends "type:<typeName>" to labels. Idempotent —
// if the first label is already exactly "type:<typeName>", returns
// labels unchanged. Returns the (possibly new) slice; callers must
// reassign.
func InjectTypeLabel(typeName string, labels []string) []string {
	if typeName == "" {
		return labels
	}
	want := "type:" + typeName
	if len(labels) > 0 && labels[0] == want {
		return labels
	}
	out := make([]string, 0, len(labels)+1)
	out = append(out, want)
	out = append(out, labels...)
	return out
}
