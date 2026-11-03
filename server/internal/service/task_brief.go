package service

// Task Brief read surface (design §6.4 / §6.9). task_briefs is append-only:
// every edit adds a version, and the current brief is the highest version. The
// write side (version creation + acceptance materialization) belongs to the
// brief backend; this service renders the stored JSON columns into the stable
// contract the Issue detail card and issue_get_brief consume.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// DeliverySpec is where a completion is reported back to.
type DeliverySpec struct {
	SourceKind  string `json:"source_kind"`
	ThreadExtID string `json:"thread_ext_id"`
	ReplyMode   string `json:"reply_mode"`
}

// Provenance records which decision/rule produced a brief version.
type Provenance struct {
	DecisionID    int64   `json:"decision_id"`
	PolicyVersion int64   `json:"policy_version"`
	Strength      string  `json:"strength"`
	ActorExtID    string  `json:"actor_ext_id"`
	RuleIDs       []int64 `json:"rule_ids"`
}

// TaskBriefView is one brief version with its JSON columns decoded.
type TaskBriefView struct {
	ID               int64        `json:"id"`
	IssueID          int64        `json:"issue_id"`
	Version          int64        `json:"version"`
	Goal             string       `json:"goal"`
	Acceptance       []string     `json:"acceptance"`
	ScopeIn          []string     `json:"scope_in"`
	ScopeOut         []string     `json:"scope_out"`
	Delivery         DeliverySpec `json:"delivery"`
	Provenance       Provenance   `json:"provenance"`
	IntakeDecisionID *int64       `json:"intake_decision_id"`
	PolicyVersion    int64        `json:"policy_version"`
	CreatedAt        time.Time    `json:"created_at"`
}

// IssueBriefView is the GET /api/issues/:id/brief response. Brief is null for
// issues that did not come from Intake, which is how the UI decides whether to
// render the Task Brief card at all.
type IssueBriefView struct {
	IssueID       int64           `json:"issue_id"`
	GoalCondition string          `json:"goal_condition"`
	Brief         *TaskBriefView  `json:"brief"`
	Versions      []TaskBriefView `json:"versions"`
}

// TaskBriefService reads Task Brief versions for an issue.
type TaskBriefService struct {
	q *store.Queries
}

func NewTaskBriefService(q *store.Queries) *TaskBriefService {
	return &TaskBriefService{q: q}
}

// GetIssueBrief returns the current brief plus the full version history
// (newest first). No brief is not an error — it yields brief=null.
func (s *TaskBriefService) GetIssueBrief(ctx context.Context, issueID int64) (IssueBriefView, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssueBriefView{}, ErrNotFound
		}
		return IssueBriefView{}, err
	}
	out := IssueBriefView{IssueID: issue.ID, GoalCondition: issue.GoalCondition, Versions: []TaskBriefView{}}

	latest, err := s.q.GetLatestTaskBrief(ctx, issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return IssueBriefView{}, err
	}
	view := taskBriefView(latest)
	out.Brief = &view

	rows, err := s.q.ListTaskBriefs(ctx, issueID)
	if err != nil {
		return IssueBriefView{}, err
	}
	out.Versions = make([]TaskBriefView, 0, len(rows))
	for _, r := range rows {
		out.Versions = append(out.Versions, taskBriefView(r))
	}
	return out, nil
}

func taskBriefView(r store.TaskBrief) TaskBriefView {
	view := TaskBriefView{
		ID:            r.ID,
		IssueID:       r.IssueID,
		Version:       r.Version,
		Goal:          r.Goal,
		Acceptance:    decodeStringList(r.AcceptanceJson),
		ScopeIn:       decodeStringList(r.ScopeInJson),
		ScopeOut:      decodeStringList(r.ScopeOutJson),
		PolicyVersion: r.PolicyVersion,
		CreatedAt:     r.CreatedAt,
	}
	if r.IntakeDecisionID.Valid {
		v := r.IntakeDecisionID.Int64
		view.IntakeDecisionID = &v
	}
	_ = json.Unmarshal([]byte(orJSONObject(r.DeliveryJson)), &view.Delivery)
	_ = json.Unmarshal([]byte(orJSONObject(r.ProvenanceJson)), &view.Provenance)
	if view.Provenance.RuleIDs == nil {
		view.Provenance.RuleIDs = []int64{}
	}
	return view
}
