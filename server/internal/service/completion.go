package service

// Completion (自动完成) configuration — project default settings, named profiles,
// and the per-issue override. Contract surface for design §6.5 / §6.9.
//
// Resolution rule (single source of truth, §6.5): issue profile > project
// default, no per-field merge. Resolution itself lives in internal/completion so
// autohost and this HTTP/MCP surface share one implementation.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/completion"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrCompletionProfileInUse is returned when deleting a profile still assigned
// to at least one issue. The assignment is never silently cleared.
var ErrCompletionProfileInUse = errors.New("completion profile is in use by one or more issues")

// CompletionPolicy is the effective completion configuration. It is the same
// type the resolver returns, so the API, MCP surface and autohost cannot drift.
type CompletionPolicy = completion.Policy

// CompletionSettingsView is the project default returned by GET
// /completion/settings. A project with no stored row reports the schema
// defaults rather than a 404 — "no row" means "defaults", not "unconfigured".
type CompletionSettingsView struct {
	ProjectID int64 `json:"project_id"`
	CompletionPolicy
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// CompletionProfileDTO is one named project-level profile.
type CompletionProfileDTO struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	CompletionPolicy
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CompletionProfileInput is the create/update body. Pointer fields make PATCH
// genuinely partial: omitted means "keep current value".
type CompletionProfileInput struct {
	Name            *string `json:"name"`
	AutohostEnabled *bool   `json:"autohost_enabled"`
	MaxRounds       *int64  `json:"max_rounds"`
	TokenBudget     *int64  `json:"token_budget"`
	StopOnDecision  *bool   `json:"stop_on_decision"`
	RequireReview   *bool   `json:"require_review"`
}

// CompletionSettingsInput is the PUT /completion/settings body.
type CompletionSettingsInput struct {
	AutohostEnabled *bool  `json:"autohost_enabled"`
	MaxRounds       *int64 `json:"max_rounds"`
	TokenBudget     *int64 `json:"token_budget"`
	StopOnDecision  *bool  `json:"stop_on_decision"`
	RequireReview   *bool  `json:"require_review"`
}

// CompletionService backs the completion settings, profile CRUD and the
// per-issue completion_profile_id override.
type CompletionService struct {
	db       *store.DB
	q        *store.Queries
	resolver *completion.Resolver
}

func NewCompletionService(db *sql.DB, q *store.Queries) *CompletionService {
	return &CompletionService{db: store.Wrap(db), q: q, resolver: completion.NewResolver(q)}
}

// ResolvePolicy returns the effective policy for an issue (issue profile >
// project default). It reads current rows on every call, so a change applies on
// the next turn without restarting the agent.
func (s *CompletionService) ResolvePolicy(ctx context.Context, issue store.Issue) (completion.EffectivePolicy, error) {
	return s.resolver.Resolve(ctx, issue)
}

// ResolveIssuePolicy is the issue-id form of ResolvePolicy.
func (s *CompletionService) ResolveIssuePolicy(ctx context.Context, issueID int64) (completion.EffectivePolicy, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return completion.EffectivePolicy{}, err
	}
	return s.resolver.Resolve(ctx, issue)
}

// defaultCompletionPolicy mirrors the creation defaults in §6.8.
func defaultCompletionPolicy() CompletionPolicy {
	return completion.DefaultPolicy()
}

// GetSettings returns the project default, falling back to schema defaults when
// no row exists yet.
func (s *CompletionService) GetSettings(ctx context.Context, projectID int64) (CompletionSettingsView, error) {
	row, err := s.q.GetCompletionSettings(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionSettingsView{ProjectID: projectID, CompletionPolicy: defaultCompletionPolicy()}, nil
	}
	if err != nil {
		return CompletionSettingsView{}, err
	}
	updated := row.UpdatedAt
	return CompletionSettingsView{
		ProjectID: row.ProjectID,
		CompletionPolicy: CompletionPolicy{
			AutohostEnabled: row.AutohostEnabled != 0,
			MaxRounds:       row.MaxRounds,
			TokenBudget:     row.TokenBudget,
			StopOnDecision:  row.StopOnDecision != 0,
			RequireReview:   row.RequireReview != 0,
		},
		UpdatedAt: &updated,
	}, nil
}

// UpdateSettings upserts the project default from the supplied fields; omitted
// fields keep the current value (or the default when there is no row yet).
func (s *CompletionService) UpdateSettings(ctx context.Context, projectID, actorUserID int64, in CompletionSettingsInput) (CompletionSettingsView, error) {
	cur, err := s.GetSettings(ctx, projectID)
	if err != nil {
		return CompletionSettingsView{}, err
	}
	p := cur.CompletionPolicy
	applyCompletionInput(&p, CompletionProfileInput{
		AutohostEnabled: in.AutohostEnabled,
		MaxRounds:       in.MaxRounds,
		TokenBudget:     in.TokenBudget,
		StopOnDecision:  in.StopOnDecision,
		RequireReview:   in.RequireReview,
	})
	if err := validateCompletionPolicy(p); err != nil {
		return CompletionSettingsView{}, err
	}
	if _, err := s.q.UpsertCompletionSettings(ctx, store.UpsertCompletionSettingsParams{
		ProjectID:       projectID,
		AutohostEnabled: completionBoolToInt64(p.AutohostEnabled),
		MaxRounds:       p.MaxRounds,
		TokenBudget:     p.TokenBudget,
		StopOnDecision:  completionBoolToInt64(p.StopOnDecision),
		RequireReview:   completionBoolToInt64(p.RequireReview),
	}); err != nil {
		return CompletionSettingsView{}, err
	}
	s.auditProject(ctx, projectID, actorUserID, "completion.settings_updated", "project", projectID, policyAuditPayload(p))
	return s.GetSettings(ctx, projectID)
}

// ListProfiles returns the project's named profiles, alphabetically by name.
func (s *CompletionService) ListProfiles(ctx context.Context, projectID int64) ([]CompletionProfileDTO, error) {
	rows, err := s.q.ListCompletionProfiles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]CompletionProfileDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, completionProfileDTO(r))
	}
	return out, nil
}

// CreateProfile adds a named profile. Names are unique per project.
func (s *CompletionService) CreateProfile(ctx context.Context, projectID, actorUserID int64, in CompletionProfileInput) (CompletionProfileDTO, error) {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return CompletionProfileDTO{}, fmt.Errorf("name is required")
	}
	name := strings.TrimSpace(*in.Name)
	if err := s.ensureNameFree(ctx, projectID, name, 0); err != nil {
		return CompletionProfileDTO{}, err
	}
	p := defaultCompletionPolicy()
	applyCompletionInput(&p, in)
	if err := validateCompletionPolicy(p); err != nil {
		return CompletionProfileDTO{}, err
	}
	row, err := s.q.CreateCompletionProfile(ctx, store.CreateCompletionProfileParams{
		ProjectID:       projectID,
		Name:            name,
		AutohostEnabled: completionBoolToInt64(p.AutohostEnabled),
		MaxRounds:       p.MaxRounds,
		TokenBudget:     p.TokenBudget,
		StopOnDecision:  completionBoolToInt64(p.StopOnDecision),
		RequireReview:   completionBoolToInt64(p.RequireReview),
	})
	if err != nil {
		return CompletionProfileDTO{}, err
	}
	s.auditProject(ctx, projectID, actorUserID, "completion.profile_created", "completion_profile", row.ID,
		profileAuditPayload(row.Name, p))
	return completionProfileDTO(row), nil
}

// UpdateProfile applies a partial update. The profile must belong to projectID.
func (s *CompletionService) UpdateProfile(ctx context.Context, projectID, profileID, actorUserID int64, in CompletionProfileInput) (CompletionProfileDTO, error) {
	cur, err := s.q.GetCompletionProfile(ctx, profileID)
	if err != nil {
		return CompletionProfileDTO{}, err
	}
	if cur.ProjectID != projectID {
		return CompletionProfileDTO{}, ErrNotFound
	}
	name := cur.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return CompletionProfileDTO{}, fmt.Errorf("name must not be empty")
		}
		if name != cur.Name {
			if err := s.ensureNameFree(ctx, projectID, name, profileID); err != nil {
				return CompletionProfileDTO{}, err
			}
		}
	}
	p := completionPolicyOfProfile(cur)
	applyCompletionInput(&p, in)
	if err := validateCompletionPolicy(p); err != nil {
		return CompletionProfileDTO{}, err
	}
	row, err := s.q.UpdateCompletionProfile(ctx, store.UpdateCompletionProfileParams{
		Name:            name,
		AutohostEnabled: completionBoolToInt64(p.AutohostEnabled),
		MaxRounds:       p.MaxRounds,
		TokenBudget:     p.TokenBudget,
		StopOnDecision:  completionBoolToInt64(p.StopOnDecision),
		RequireReview:   completionBoolToInt64(p.RequireReview),
		ID:              profileID,
	})
	if err != nil {
		return CompletionProfileDTO{}, err
	}
	s.auditProject(ctx, projectID, actorUserID, "completion.profile_updated", "completion_profile", row.ID,
		profileAuditPayload(row.Name, p))
	return completionProfileDTO(row), nil
}

// DeleteProfile removes a profile unless an issue still references it.
func (s *CompletionService) DeleteProfile(ctx context.Context, projectID, profileID, actorUserID int64) error {
	cur, err := s.q.GetCompletionProfile(ctx, profileID)
	if err != nil {
		return err
	}
	if cur.ProjectID != projectID {
		return ErrNotFound
	}
	n, err := s.q.CountIssuesByCompletionProfile(ctx, sql.NullInt64{Int64: profileID, Valid: true})
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrCompletionProfileInUse
	}
	if err := s.q.DeleteCompletionProfile(ctx, profileID); err != nil {
		return err
	}
	s.auditProject(ctx, projectID, actorUserID, "completion.profile_deleted", "completion_profile", profileID,
		profileAuditPayload(cur.Name, completionPolicyOfProfile(cur)))
	return nil
}

// SetIssueProfile sets or clears (nil) an issue's completion profile override.
// A non-nil profile must belong to the same project as the issue.
func (s *CompletionService) SetIssueProfile(ctx context.Context, issueID, actorUserID int64, profileID *int64) (store.Issue, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return store.Issue{}, err
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return store.Issue{}, err
	}
	if profileID != nil {
		profile, err := s.q.GetCompletionProfile(ctx, *profileID)
		if err != nil {
			return store.Issue{}, err
		}
		if profile.ProjectID != col.ProjectID {
			return store.Issue{}, fmt.Errorf("completion profile does not belong to this issue's project")
		}
	}
	updated, err := s.q.SetIssueCompletionProfile(ctx, store.SetIssueCompletionProfileParams{
		CompletionProfileID: sql.NullInt64{Int64: int64OrZero(profileID), Valid: profileID != nil},
		ID:                  issueID,
	})
	if err != nil {
		return store.Issue{}, err
	}
	payload := `{"completion_profile_id":null}`
	if profileID != nil {
		payload = fmt.Sprintf(`{"completion_profile_id":%d}`, *profileID)
	}
	s.auditProject(ctx, col.ProjectID, actorUserID, "completion.issue_profile_changed", "issue", issueID, payload)
	return updated, nil
}

// auditProject records a completion-policy mutation against the project owner.
// appendResourceAudit is a no-op for personal ('user') owners — the personal
// edition is single-user, so updated_at is the whole provenance there — and
// writes actor + time to org_audit_log for org-owned projects.
func (s *CompletionService) auditProject(ctx context.Context, projectID, actorUserID int64, action, targetType string, targetID int64, payload string) {
	if s.db == nil {
		return
	}
	project, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return
	}
	appendResourceAudit(ctx, s.db, nil, project.OwnerType, project.OwnerID, actorUserID, action, targetType, targetID, payload)
}

func policyAuditPayload(p CompletionPolicy) string {
	b, err := json.Marshal(p)
	if err != nil {
		return `{}`
	}
	return string(b)
}

func profileAuditPayload(name string, p CompletionPolicy) string {
	b, err := json.Marshal(struct {
		Name string `json:"name"`
		CompletionPolicy
	}{Name: name, CompletionPolicy: p})
	if err != nil {
		return `{}`
	}
	return string(b)
}

func completionPolicyOfProfile(p store.CompletionProfile) CompletionPolicy {
	return CompletionPolicy{
		AutohostEnabled: p.AutohostEnabled != 0,
		MaxRounds:       p.MaxRounds,
		TokenBudget:     p.TokenBudget,
		StopOnDecision:  p.StopOnDecision != 0,
		RequireReview:   p.RequireReview != 0,
	}
}

func (s *CompletionService) ensureNameFree(ctx context.Context, projectID int64, name string, exceptID int64) error {
	rows, err := s.q.ListCompletionProfiles(ctx, projectID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.ID != exceptID && strings.EqualFold(r.Name, name) {
			return fmt.Errorf("a profile named %q already exists", name)
		}
	}
	return nil
}

func completionProfileDTO(r store.CompletionProfile) CompletionProfileDTO {
	return CompletionProfileDTO{
		ID:               r.ID,
		ProjectID:        r.ProjectID,
		Name:             r.Name,
		CompletionPolicy: completionPolicyOfProfile(r),
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

func applyCompletionInput(p *CompletionPolicy, in CompletionProfileInput) {
	if in.AutohostEnabled != nil {
		p.AutohostEnabled = *in.AutohostEnabled
	}
	if in.MaxRounds != nil {
		p.MaxRounds = *in.MaxRounds
	}
	if in.TokenBudget != nil {
		p.TokenBudget = *in.TokenBudget
	}
	if in.StopOnDecision != nil {
		p.StopOnDecision = *in.StopOnDecision
	}
	if in.RequireReview != nil {
		p.RequireReview = *in.RequireReview
	}
}

func validateCompletionPolicy(p CompletionPolicy) error {
	if p.MaxRounds < 0 {
		return fmt.Errorf("max_rounds must be >= 0")
	}
	if p.TokenBudget < 0 {
		return fmt.Errorf("token_budget must be >= 0")
	}
	return nil
}

func completionBoolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func int64OrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
