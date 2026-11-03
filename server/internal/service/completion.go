package service

// Completion (自动完成) configuration — project default settings, named profiles,
// and the per-issue override. Contract surface for design §6.5 / §6.9.
//
// Resolution rule (single source of truth, §6.5): issue profile > project
// default, no per-field merge. EncodeCompletionPolicy flattens the policy into
// the JSON shape the settings page and MCP tools consume.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrCompletionProfileInUse is returned when deleting a profile still assigned
// to at least one issue. The assignment is never silently cleared.
var ErrCompletionProfileInUse = errors.New("completion profile is in use by one or more issues")

// CompletionPolicy is the effective completion configuration.
type CompletionPolicy struct {
	AutohostEnabled bool  `json:"autohost_enabled"`
	MaxRounds       int64 `json:"max_rounds"`
	TokenBudget     int64 `json:"token_budget"`
	StopOnDecision  bool  `json:"stop_on_decision"`
	RequireReview   bool  `json:"require_review"`
}

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
	db *store.DB
	q  *store.Queries
}

func NewCompletionService(db *sql.DB, q *store.Queries) *CompletionService {
	return &CompletionService{db: store.Wrap(db), q: q}
}

// defaultCompletionPolicy mirrors the creation defaults in §6.8.
func defaultCompletionPolicy() CompletionPolicy {
	return CompletionPolicy{
		AutohostEnabled: true,
		MaxRounds:       20,
		TokenBudget:     0,
		StopOnDecision:  true,
		RequireReview:   false,
	}
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
func (s *CompletionService) UpdateSettings(ctx context.Context, projectID int64, in CompletionSettingsInput) (CompletionSettingsView, error) {
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
func (s *CompletionService) CreateProfile(ctx context.Context, projectID int64, in CompletionProfileInput) (CompletionProfileDTO, error) {
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
	return completionProfileDTO(row), nil
}

// UpdateProfile applies a partial update. The profile must belong to projectID.
func (s *CompletionService) UpdateProfile(ctx context.Context, projectID, profileID int64, in CompletionProfileInput) (CompletionProfileDTO, error) {
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
	p := CompletionPolicy{
		AutohostEnabled: cur.AutohostEnabled != 0,
		MaxRounds:       cur.MaxRounds,
		TokenBudget:     cur.TokenBudget,
		StopOnDecision:  cur.StopOnDecision != 0,
		RequireReview:   cur.RequireReview != 0,
	}
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
	return completionProfileDTO(row), nil
}

// DeleteProfile removes a profile unless an issue still references it.
func (s *CompletionService) DeleteProfile(ctx context.Context, projectID, profileID int64) error {
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
	return s.q.DeleteCompletionProfile(ctx, profileID)
}

// SetIssueProfile sets or clears (nil) an issue's completion profile override.
// A non-nil profile must belong to the same project as the issue.
func (s *CompletionService) SetIssueProfile(ctx context.Context, issueID int64, profileID *int64) (store.Issue, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return store.Issue{}, err
	}
	if profileID != nil {
		col, err := s.q.GetColumn(ctx, issue.ColumnID)
		if err != nil {
			return store.Issue{}, err
		}
		profile, err := s.q.GetCompletionProfile(ctx, *profileID)
		if err != nil {
			return store.Issue{}, err
		}
		if profile.ProjectID != col.ProjectID {
			return store.Issue{}, fmt.Errorf("completion profile does not belong to this issue's project")
		}
	}
	return s.q.SetIssueCompletionProfile(ctx, store.SetIssueCompletionProfileParams{
		CompletionProfileID: sql.NullInt64{Int64: int64OrZero(profileID), Valid: profileID != nil},
		ID:                  issueID,
	})
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
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Name:      r.Name,
		CompletionPolicy: CompletionPolicy{
			AutohostEnabled: r.AutohostEnabled != 0,
			MaxRounds:       r.MaxRounds,
			TokenBudget:     r.TokenBudget,
			StopOnDecision:  r.StopOnDecision != 0,
			RequireReview:   r.RequireReview != 0,
		},
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
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
