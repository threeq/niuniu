// Package completion owns the auto-completion policy: the project default, the
// named profiles a project can define, and the single resolution rule from
// design §6.5 — issue profile > project default, never a per-field merge.
//
// The resolver is deliberately stateless: every call reads the current rows, so
// a settings/profile change is picked up by the next autohost turn without
// restarting the agent. Autohost and the HTTP/MCP surface both resolve through
// this package instead of re-deriving the rule.
package completion

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Policy is the effective completion configuration (design §6.5).
type Policy struct {
	AutohostEnabled bool  `json:"autohost_enabled"`
	MaxRounds       int64 `json:"max_rounds"`
	TokenBudget     int64 `json:"token_budget"`
	StopOnDecision  bool  `json:"stop_on_decision"`
	RequireReview   bool  `json:"require_review"`
}

// DefaultPolicy mirrors the creation defaults in design §6.8. A project with no
// completion_settings row behaves exactly like one holding these values.
func DefaultPolicy() Policy {
	return Policy{
		AutohostEnabled: true,
		MaxRounds:       20,
		TokenBudget:     0,
		StopOnDecision:  true,
		RequireReview:   false,
	}
}

// Source says which level supplied an EffectivePolicy. The UI renders it as
// 「来源：项目默认 / <profile name>」 so an override is never hidden.
type Source string

const (
	SourceProjectDefault Source = "project_default"
	SourceIssueProfile   Source = "issue_profile"
)

// EffectivePolicy is the resolved policy plus where it came from.
type EffectivePolicy struct {
	Policy
	Source      Source `json:"source"`
	ProfileID   *int64 `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

// BuiltinProfile is one named profile seeded into a new project.
type BuiltinProfile struct {
	Name   string
	Policy Policy
}

// BuiltinProfiles returns the four profiles every new project starts with
// (design §6.7.4). They are ordinary rows afterwards: editable and deletable
// under the same reference guard as user-created profiles.
func BuiltinProfiles() []BuiltinProfile {
	return []BuiltinProfile{
		{
			Name: "标准",
			Policy: Policy{
				AutohostEnabled: true,
				MaxRounds:       20,
				TokenBudget:     0,
				StopOnDecision:  true,
				RequireReview:   false,
			},
		},
		{
			Name: "快速",
			Policy: Policy{
				AutohostEnabled: true,
				MaxRounds:       3,
				TokenBudget:     20_000,
				StopOnDecision:  true,
				RequireReview:   false,
			},
		},
		{
			Name: "深度",
			Policy: Policy{
				AutohostEnabled: true,
				MaxRounds:       60,
				TokenBudget:     200_000,
				StopOnDecision:  true,
				RequireReview:   false,
			},
		},
		{
			Name: "高风险需审查",
			Policy: Policy{
				AutohostEnabled: true,
				MaxRounds:       20,
				TokenBudget:     100_000,
				StopOnDecision:  true,
				RequireReview:   true,
			},
		},
	}
}

// Resolver resolves an issue's effective completion policy. It holds no cache:
// each Resolve call reads the current settings/profile rows, which is what makes
// a change take effect on the next turn without an agent restart.
type Resolver struct {
	q *store.Queries
}

func NewResolver(q *store.Queries) *Resolver {
	return &Resolver{q: q}
}

// ResolveCompletionPolicy is the single resolution entry point from design
// §6.5. Callers that resolve once can use this instead of holding a Resolver.
func ResolveCompletionPolicy(ctx context.Context, q *store.Queries, issue store.Issue) (EffectivePolicy, error) {
	return NewResolver(q).Resolve(ctx, issue)
}

// Resolve returns the effective policy for an issue.
//
// Rule (the only one): a non-NULL issues.completion_profile_id wins over the
// project default. NULL means inherit. A dangling reference (profile deleted
// out-of-band) degrades to the project default instead of breaking autohost.
func (r *Resolver) Resolve(ctx context.Context, issue store.Issue) (EffectivePolicy, error) {
	if r == nil || r.q == nil {
		return EffectivePolicy{Policy: DefaultPolicy(), Source: SourceProjectDefault}, nil
	}
	if issue.CompletionProfileID.Valid && issue.CompletionProfileID.Int64 > 0 {
		profile, err := r.q.GetCompletionProfile(ctx, issue.CompletionProfileID.Int64)
		switch {
		case err == nil:
			if r.profileBelongsToIssue(ctx, issue, profile.ProjectID) {
				id := profile.ID
				return EffectivePolicy{
					Policy:      policyOfProfile(profile),
					Source:      SourceIssueProfile,
					ProfileID:   &id,
					ProfileName: profile.Name,
				}, nil
			}
			slog.Warn("completion: issue references a profile from another project; inheriting project default",
				"issue_id", issue.ID, "profile_id", profile.ID, "profile_project_id", profile.ProjectID)
		case errors.Is(err, sql.ErrNoRows):
			slog.Warn("completion: issue references a missing profile; inheriting project default",
				"issue_id", issue.ID, "profile_id", issue.CompletionProfileID.Int64)
		default:
			return EffectivePolicy{}, err
		}
	}
	return r.projectDefault(ctx, issue)
}

// ResolveIssueID is the convenience form used by callers that only hold an
// issue id (autohost keeps issueID, not the row).
func (r *Resolver) ResolveIssueID(ctx context.Context, issueID int64) (EffectivePolicy, error) {
	issue, err := r.q.GetIssue(ctx, issueID)
	if err != nil {
		return EffectivePolicy{}, err
	}
	return r.Resolve(ctx, issue)
}

func (r *Resolver) projectDefault(ctx context.Context, issue store.Issue) (EffectivePolicy, error) {
	col, err := r.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return EffectivePolicy{}, err
	}
	row, err := r.q.GetCompletionSettings(ctx, col.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return EffectivePolicy{Policy: DefaultPolicy(), Source: SourceProjectDefault}, nil
	}
	if err != nil {
		return EffectivePolicy{}, err
	}
	return EffectivePolicy{
		Policy: Policy{
			AutohostEnabled: row.AutohostEnabled != 0,
			MaxRounds:       row.MaxRounds,
			TokenBudget:     row.TokenBudget,
			StopOnDecision:  row.StopOnDecision != 0,
			RequireReview:   row.RequireReview != 0,
		},
		Source: SourceProjectDefault,
	}, nil
}

func (r *Resolver) profileBelongsToIssue(ctx context.Context, issue store.Issue, profileProjectID int64) bool {
	col, err := r.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		// Can't prove a mismatch; trust the FK-validated assignment.
		return true
	}
	return col.ProjectID == profileProjectID
}

func policyOfProfile(p store.CompletionProfile) Policy {
	return Policy{
		AutohostEnabled: p.AutohostEnabled != 0,
		MaxRounds:       p.MaxRounds,
		TokenBudget:     p.TokenBudget,
		StopOnDecision:  p.StopOnDecision != 0,
		RequireReview:   p.RequireReview != 0,
	}
}

// EnsureProjectDefaults makes sure a project has its completion_settings row and
// the four built-in profiles. Idempotent (INSERT OR IGNORE relies on the
// completion_settings PK and the (project_id, name) unique constraint), so it is
// safe to call from every project-creation path and again after a partial run.
func EnsureProjectDefaults(ctx context.Context, db *store.DB, projectID int64) error {
	if db == nil || projectID <= 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO completion_settings (project_id) VALUES (?)`,
		projectID,
	); err != nil {
		return err
	}
	for _, p := range BuiltinProfiles() {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO completion_profiles (
				project_id, name, autohost_enabled, max_rounds, token_budget, stop_on_decision, require_review
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, p.Name, boolToInt64(p.Policy.AutohostEnabled), p.Policy.MaxRounds,
			p.Policy.TokenBudget, boolToInt64(p.Policy.StopOnDecision), boolToInt64(p.Policy.RequireReview),
		); err != nil {
			return err
		}
	}
	return nil
}

// BackfillProjectDefaults seeds projects created before completion policy
// existed. A project counts as uninitialized when it has no completion_settings
// row; projects created after this feature always get one at creation time, so
// a user who deletes the built-in profiles is not re-seeded on the next boot.
func BackfillProjectDefaults(ctx context.Context, db *store.DB) error {
	if db == nil {
		return nil
	}
	uninitialized := `
		NOT EXISTS (SELECT 1 FROM completion_settings cs WHERE cs.project_id = projects.id)`
	for _, p := range BuiltinProfiles() {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO completion_profiles (
				project_id, name, autohost_enabled, max_rounds, token_budget, stop_on_decision, require_review
			) SELECT id, ?, ?, ?, ?, ?, ? FROM projects WHERE `+uninitialized,
			p.Name, boolToInt64(p.Policy.AutohostEnabled), p.Policy.MaxRounds,
			p.Policy.TokenBudget, boolToInt64(p.Policy.StopOnDecision), boolToInt64(p.Policy.RequireReview),
		); err != nil {
			return err
		}
	}
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO completion_settings (project_id) SELECT id FROM projects`)
	return err
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
