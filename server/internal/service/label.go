package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Label-related sentinel errors.
var (
	ErrInvalidLabelColor = errors.New("invalid label color")
	ErrInvalidLabelName  = errors.New("invalid label name")
	ErrLabelNotFound     = errors.New("label not found")
	ErrLabelForbidden    = errors.New("not authorized to mutate label")
)

// LabelNameConflictError signals that a label with the same (project_id, name)
// already exists. The Existing field carries the conflicting row so callers
// can surface its id/color/etc.
type LabelNameConflictError struct {
	Existing store.Label
}

func (e *LabelNameConflictError) Error() string {
	return fmt.Sprintf("label name %q already exists", e.Existing.Name)
}

// presetColors is the GitHub-flavoured palette used when a caller creates a
// label without specifying a color. Keep these lower-case so they round-trip
// through validateAndNormalizeColor without mutation.
var presetColors = []string{
	"#d73a4a", "#a2eeef", "#7057ff", "#008672", "#e4e669",
	"#d876e3", "#fbca04", "#0075ca", "#cfd3d7", "#bfd4f2",
}

// IsPresetColor reports whether c is one of the bundled preset colors.
// Used by tests; lower-cased input is required (validateAndNormalizeColor
// guarantees this for any color that round-trips through Create).
func IsPresetColor(c string) bool {
	for _, p := range presetColors {
		if p == c {
			return true
		}
	}
	return false
}

// RandomPresetColor returns a deterministically-pseudo-random preset color.
// We use UnixNano() rather than crypto/rand because labels are aesthetic;
// a tight loop creating two labels in the same nanosecond would yield the
// same color, but that's acceptable for the UX (labels are user-renamed).
func RandomPresetColor() string {
	return presetColors[int(time.Now().UnixNano())%len(presetColors)]
}

// LabelService implements CRUD for project-scoped labels with an admin gate
// on Update/Delete.
type LabelService struct {
	db    *store.DB
	q     *store.Queries
	authz *Authz
}

// NewLabelService wraps db once and constructs the service. The authz pointer
// is required for Update/Delete admin checks.
func NewLabelService(db *sql.DB, authz *Authz) *LabelService {
	w := store.Wrap(db)
	return &LabelService{db: w, q: store.New(w), authz: authz}
}

// LabelCreateInput carries the user-supplied fields for label creation.
// Color is optional — empty string causes a random preset to be picked.
type LabelCreateInput struct {
	Name        string
	Color       string
	Description string
}

// LabelUpdateInput is a partial update; nil pointers leave the field unchanged.
type LabelUpdateInput struct {
	Name        *string
	Color       *string
	Description *string
}

// LabelWithUsage attaches a usage_count (derived from issue_labels) to a
// stored label.
type LabelWithUsage struct {
	store.Label
	UsageCount int64 `json:"usage_count"`
}

// colorRE matches lowercase six-digit hex colors. Three-digit shorthand
// (#fff) and uppercase forms are normalized/rejected by
// validateAndNormalizeColor before this regex sees them.
var colorRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func validateAndNormalizeColor(c string) (string, error) {
	if c == "" {
		return RandomPresetColor(), nil
	}
	low := strings.ToLower(c)
	if !colorRE.MatchString(low) {
		return "", ErrInvalidLabelColor
	}
	return low, nil
}

func validateName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || len([]rune(n)) > 50 {
		return "", ErrInvalidLabelName
	}
	return n, nil
}

// Create inserts a new label under projectID. On a unique-constraint violation
// (same name within the project) the existing row is fetched and returned in
// a *LabelNameConflictError so callers can surface details.
func (s *LabelService) Create(ctx context.Context, userID, projectID int64, in LabelCreateInput) (store.Label, error) {
	name, err := validateName(in.Name)
	if err != nil {
		return store.Label{}, err
	}
	color, err := validateAndNormalizeColor(in.Color)
	if err != nil {
		return store.Label{}, err
	}

	lbl, err := s.q.CreateLabel(ctx, store.CreateLabelParams{
		ProjectID:   projectID,
		Name:        name,
		Color:       color,
		Description: in.Description,
		CreatedBy:   userID,
	})
	if err != nil {
		// Only treat the error as a conflict when it actually is a UNIQUE
		// violation; otherwise a transient pgx/SQLite error would get
		// mis-reported as "name taken" via the blind GetLabelByProjectAndName
		// fallback. isUniqueViolation lives in service/project.go and matches
		// both SQLite and pgx forms.
		if isUniqueViolation(err) {
			existing, lookupErr := s.q.GetLabelByProjectAndName(ctx,
				store.GetLabelByProjectAndNameParams{ProjectID: projectID, Name: name})
			if lookupErr == nil {
				return store.Label{}, &LabelNameConflictError{Existing: existing}
			}
		}
		return store.Label{}, err
	}
	// Audit on org-owned projects only (appendResourceAudit no-ops for "user").
	// Failure to look up the owner is a logged warning — labels still committed.
	s.auditProjectMutation(ctx, userID, projectID, "label.created", lbl.ID,
		resourceAuditPayload(name, "color", color, "description", in.Description))
	return lbl, nil
}

// List returns labels for projectID. If query is non-empty it is matched
// (case-insensitive substring) against the label name. The query parameter
// is intentionally applied in-process rather than via SQL so callers can rely
// on stable Unicode case-folding across SQLite/Postgres.
func (s *LabelService) List(ctx context.Context, userID, projectID int64, query string) ([]store.Label, error) {
	all, err := s.q.ListLabelsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return all, nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]store.Label, 0, len(all))
	for _, l := range all {
		if strings.Contains(strings.ToLower(l.Name), q) {
			out = append(out, l)
		}
	}
	return out, nil
}

// ListWithUsage returns labels for projectID with their issue_labels count.
// Used by the admin UI to show "n issues" alongside each label.
func (s *LabelService) ListWithUsage(ctx context.Context, projectID int64) ([]LabelWithUsage, error) {
	rows, err := s.q.ListLabelsByProjectWithUsage(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]LabelWithUsage, 0, len(rows))
	for _, r := range rows {
		out = append(out, LabelWithUsage{
			Label: store.Label{
				ID:          r.ID,
				ProjectID:   r.ProjectID,
				Name:        r.Name,
				Color:       r.Color,
				Description: r.Description,
				CreatedAt:   r.CreatedAt,
				CreatedBy:   r.CreatedBy,
			},
			UsageCount: r.UsageCount,
		})
	}
	return out, nil
}

// Update mutates a label after verifying that userID is a project admin.
// Nil fields in `in` leave the corresponding column unchanged. A unique-name
// collision returns a *LabelNameConflictError just like Create.
func (s *LabelService) Update(ctx context.Context, userID, labelID int64, in LabelUpdateInput) (store.Label, error) {
	cur, err := s.q.GetLabel(ctx, labelID)
	if err == sql.ErrNoRows {
		return store.Label{}, ErrLabelNotFound
	}
	if err != nil {
		return store.Label{}, err
	}

	ok, err := s.authz.IsProjectAdmin(ctx, userID, cur.ProjectID)
	if err != nil {
		return store.Label{}, err
	}
	if !ok {
		return store.Label{}, ErrLabelForbidden
	}

	name := cur.Name
	if in.Name != nil {
		n, err := validateName(*in.Name)
		if err != nil {
			return store.Label{}, err
		}
		name = n
	}
	color := cur.Color
	if in.Color != nil {
		c, err := validateAndNormalizeColor(*in.Color)
		if err != nil {
			return store.Label{}, err
		}
		color = c
	}
	desc := cur.Description
	if in.Description != nil {
		desc = *in.Description
	}

	updated, err := s.q.UpdateLabel(ctx, store.UpdateLabelParams{
		ID:          labelID,
		Name:        name,
		Color:       color,
		Description: desc,
	})
	if err != nil {
		// Same conflict-surfacing pattern as Create, but skip the case where
		// the "conflict" is the row we're updating itself (id-collision is
		// not a conflict). Gate on isUniqueViolation so a transient driver
		// error doesn't get silently relabeled as "name taken".
		if isUniqueViolation(err) {
			existing, lookupErr := s.q.GetLabelByProjectAndName(ctx,
				store.GetLabelByProjectAndNameParams{ProjectID: cur.ProjectID, Name: name})
			if lookupErr == nil && existing.ID != labelID {
				return store.Label{}, &LabelNameConflictError{Existing: existing}
			}
		}
		return store.Label{}, err
	}
	s.auditProjectMutation(ctx, userID, cur.ProjectID, "label.updated", labelID,
		resourceAuditPayload(name, "color", color, "description", desc, "prev_name", cur.Name))
	return updated, nil
}

// Delete removes a label after verifying admin rights. Cascading deletion of
// issue_labels rows is handled by the FK constraint in the schema.
func (s *LabelService) Delete(ctx context.Context, userID, labelID int64) error {
	cur, err := s.q.GetLabel(ctx, labelID)
	if err == sql.ErrNoRows {
		return ErrLabelNotFound
	}
	if err != nil {
		return err
	}

	ok, err := s.authz.IsProjectAdmin(ctx, userID, cur.ProjectID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLabelForbidden
	}

	if err := s.q.DeleteLabel(ctx, labelID); err != nil {
		return err
	}
	s.auditProjectMutation(ctx, userID, cur.ProjectID, "label.deleted", labelID,
		resourceAuditPayload(cur.Name, "project_id", fmt.Sprintf("%d", cur.ProjectID)))
	return nil
}

// auditProjectMutation looks up the project's owner and writes an audit row
// when the project is org-owned. Audit failure (DB error, missing project)
// is logged but does not propagate, matching the policy in
// KanbanService.CreateProjectWithDefaults — labels mutate even if the audit
// hook can't write. Authz is unrelated; permission gating happens upstream.
func (s *LabelService) auditProjectMutation(ctx context.Context, actorUserID, projectID int64, action string, targetID int64, payload string) {
	db := s.authz.DB()
	if db == nil {
		return
	}
	var ownerType string
	var ownerID int64
	if err := db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM projects WHERE id = ?`, projectID,
	).Scan(&ownerType, &ownerID); err != nil {
		// Don't fail the mutation on audit-side lookup failures; just record
		// the miss. appendResourceAudit also logs on its own write failures.
		return
	}
	appendResourceAudit(ctx, db, nil, ownerType, ownerID, actorUserID, action, "label", targetID, payload)
}

// ApplyIssueLabelDeltas attaches addLabelIDs and detaches removeLabelIDs across
// every issue in issueIDs inside ONE transaction. Doing both directions in a
// single tx means a mid-way failure rolls the whole delta back, instead of
// leaving the adds committed while the removes fail (which would surface to the
// client as a total failure despite a partial write). Both directions are
// idempotent: adds use INSERT OR IGNORE (ON CONFLICT DO NOTHING on Postgres via
// the *store.DB wrapper), removes are no-ops on a non-existent pair.
//
// SECURITY (IDOR contract): authorization is the caller's responsibility.
// issueIDs MUST be pre-filtered to ids the caller may modify (the label handler
// does this via Authz.CanAccessIssue before calling). Cross-project label-issue
// pairs are additionally blocked at the FK level — the issue_labels table
// requires both ids to exist, and labelIDs belong to a specific project that
// must match. Direct callers MUST replicate the per-id authz check.
func (s *LabelService) ApplyIssueLabelDeltas(ctx context.Context, issueIDs, addLabelIDs, removeLabelIDs []int64) error {
	if len(issueIDs) == 0 || (len(addLabelIDs) == 0 && len(removeLabelIDs) == 0) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := tx.Queries()
	for _, iid := range issueIDs {
		for _, lid := range addLabelIDs {
			if err := q.AddIssueLabel(ctx, store.AddIssueLabelParams{IssueID: iid, LabelID: lid}); err != nil {
				return err
			}
		}
		for _, lid := range removeLabelIDs {
			if err := q.RemoveIssueLabel(ctx, store.RemoveIssueLabelParams{IssueID: iid, LabelID: lid}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
