package service

// Task Brief surface (design §6.4 / §6.9). task_briefs is append-only: every
// edit adds a version, and the current brief is the highest version.
// TaskBriefService renders the stored JSON columns into the stable contract the
// Issue detail card and issue_get_brief consume; TaskBriefWriter is the write
// side that also refreshes the two derived copies (issues.goal_condition and
// the kind='acceptance' checklist rows).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
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
	CreatedBy        *int64       `json:"created_by"`
	CreatedAt        time.Time    `json:"created_at"`
}

// ProvenanceEventView is the original intake event behind the current Brief,
// rendered into the provenance popover. It travels with the Brief so the detail
// panel does not issue a second request.
type ProvenanceEventView struct {
	SourceKind  string          `json:"source_kind"`
	EventKind   string          `json:"event_kind"`
	ActorExtID  string          `json:"actor_ext_id"`
	ActorName   string          `json:"actor_name"`
	ThreadExtID string          `json:"thread_ext_id"`
	EventExtID  string          `json:"event_ext_id"`
	SourceRef   string          `json:"source_ref"`
	Payload     json.RawMessage `json:"payload"`
	ReceivedAt  time.Time       `json:"received_at"`
	RuleNames   []string        `json:"rule_names"`
}

// IssueBriefView is the GET /api/issues/:id/brief response. Brief is null for
// issues that did not come from Intake, which is how the UI decides whether to
// render the Task Brief card at all.
type IssueBriefView struct {
	IssueID         int64                `json:"issue_id"`
	GoalCondition   string               `json:"goal_condition"`
	Brief           *TaskBriefView       `json:"brief"`
	Versions        []TaskBriefView      `json:"versions"`
	ProvenanceEvent *ProvenanceEventView `json:"provenance_event"`
}

// TaskBriefInput is the PUT /api/issues/:id/brief body. Delivery and provenance
// are optional on an edit and inherit the current version when omitted, so a
// caller changing the goal does not have to restate where the result goes.
type TaskBriefInput struct {
	Goal       string        `json:"goal"`
	Acceptance []string      `json:"acceptance"`
	ScopeIn    []string      `json:"scope_in"`
	ScopeOut   []string      `json:"scope_out"`
	Delivery   *DeliverySpec `json:"delivery"`
	Provenance *Provenance   `json:"provenance"`
}

// TaskBriefService reads Task Brief versions for an issue and appends a new
// version on an explicit edit.
type TaskBriefService struct {
	q      *store.Queries
	writer *TaskBriefWriter
}

func NewTaskBriefService(q *store.Queries, db *store.DB) *TaskBriefService {
	return &TaskBriefService{q: q, writer: NewTaskBriefWriter(db)}
}

// PutIssueBrief appends a new Brief version. task_briefs is append-only, so an
// edit never rewrites history; acceptance rows and issues.goal_condition are
// refreshed by the writer in the same transaction. Delivery/provenance fall
// back to the current version, and an issue without a brief gets version 1.
func (s *TaskBriefService) PutIssueBrief(ctx context.Context, issueID int64, in TaskBriefInput, createdBy int64) (IssueBriefView, error) {
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		return IssueBriefView{}, errors.New("goal is required")
	}
	if s.writer == nil || s.writer.db == nil {
		return IssueBriefView{}, errors.New("task brief: no database")
	}
	if _, err := s.q.GetIssue(ctx, issueID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssueBriefView{}, ErrNotFound
		}
		return IssueBriefView{}, err
	}

	delivery := DeliverySpec{}
	provenance := Provenance{RuleIDs: []int64{}}
	cur, err := s.q.GetLatestTaskBrief(ctx, issueID)
	switch {
	case err == nil:
		view := taskBriefView(cur)
		delivery = view.Delivery
		provenance = view.Provenance
	case !errors.Is(err, sql.ErrNoRows):
		return IssueBriefView{}, err
	}
	if in.Delivery != nil {
		delivery = *in.Delivery
	}
	if in.Provenance != nil {
		provenance = *in.Provenance
		if provenance.RuleIDs == nil {
			provenance.RuleIDs = []int64{}
		}
	}

	if _, err := s.writer.Append(ctx, issueID, intake.TaskBrief{
		Goal:       goal,
		Acceptance: in.Acceptance,
		ScopeIn:    in.ScopeIn,
		ScopeOut:   in.ScopeOut,
		Delivery: intake.DeliverySpec{
			SourceKind:  delivery.SourceKind,
			ThreadExtID: delivery.ThreadExtID,
			ReplyMode:   delivery.ReplyMode,
		},
		Provenance: intake.Provenance{
			DecisionID:    provenance.DecisionID,
			PolicyVersion: provenance.PolicyVersion,
			Strength:      intake.Strength(provenance.Strength),
			ActorExtID:    provenance.ActorExtID,
			RuleIDs:       provenance.RuleIDs,
		},
	}, createdBy); err != nil {
		return IssueBriefView{}, err
	}
	return s.GetIssueBrief(ctx, issueID)
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
	out.ProvenanceEvent = s.provenanceEvent(ctx, view.Provenance)

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

// provenanceEvent loads the decision + event that produced the current Brief.
// A brief without an intake origin (or whose decision was pruned) simply has no
// event; the popover still renders the stored provenance fields. Rule names are
// resolved best-effort so a deleted rule does not blank the whole trace.
func (s *TaskBriefService) provenanceEvent(ctx context.Context, prov Provenance) *ProvenanceEventView {
	if prov.DecisionID <= 0 {
		return nil
	}
	decision, err := s.q.GetIntakeDecision(ctx, prov.DecisionID)
	if err != nil {
		return nil
	}
	event, err := s.q.GetIntakeEvent(ctx, decision.EventID)
	if err != nil {
		return nil
	}
	payload := json.RawMessage(orJSONObject(event.PayloadJson))
	view := &ProvenanceEventView{
		SourceKind:  event.SourceKind,
		EventKind:   event.EventKind,
		ActorExtID:  event.ActorExtID,
		ActorName:   event.ActorName,
		ThreadExtID: event.ThreadExtID,
		EventExtID:  event.EventExtID,
		SourceRef:   event.SourceRef,
		Payload:     payload,
		ReceivedAt:  event.ReceivedAt,
		RuleNames:   []string{},
	}
	for _, ruleID := range prov.RuleIDs {
		if rule, rerr := s.q.GetIntakeRule(ctx, ruleID); rerr == nil && rule.Name != "" {
			view.RuleNames = append(view.RuleNames, rule.Name)
		}
	}
	return view
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
	if r.CreatedBy.Valid {
		v := r.CreatedBy.Int64
		view.CreatedBy = &v
	}
	_ = json.Unmarshal([]byte(orJSONObject(r.DeliveryJson)), &view.Delivery)
	_ = json.Unmarshal([]byte(orJSONObject(r.ProvenanceJson)), &view.Provenance)
	if view.Provenance.RuleIDs == nil {
		view.Provenance.RuleIDs = []int64{}
	}
	return view
}

// TaskBriefWriter is the write side of the Task Brief contract (§6.4). It owns
// one transaction across three tables because a brief version and its derived
// copies must never disagree:
//
//   - task_briefs: append-only history, version = MAX(version)+1;
//   - issue_checklists: acceptance materialized as kind='acceptance' + brief_id;
//   - issues.goal_condition: the rendered cache autohost reads.
//
// Manual kind='task' checklist rows are never touched. A resumed task keeps its
// current Brief; only an explicit scope expansion appends a version.
type TaskBriefWriter struct{ db *store.DB }

func NewTaskBriefWriter(db *store.DB) *TaskBriefWriter { return &TaskBriefWriter{db: db} }

// Append records a new Brief version for issueID and refreshes its derived
// copies in the same transaction. Old versions stay queryable.
func (w *TaskBriefWriter) Append(ctx context.Context, issueID int64, brief intake.TaskBrief, createdBy int64) (store.TaskBrief, error) {
	if issueID <= 0 {
		return store.TaskBrief{}, errors.New("task brief: invalid issue id")
	}
	if w == nil || w.db == nil {
		return store.TaskBrief{}, errors.New("task brief: no database")
	}

	acceptance := trimNonEmpty(brief.Acceptance)
	acceptanceJSON, err := json.Marshal(nonNilStrings(acceptance))
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: encode acceptance: %w", err)
	}
	scopeInJSON, err := json.Marshal(nonNilStrings(trimNonEmpty(brief.ScopeIn)))
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: encode scope_in: %w", err)
	}
	scopeOutJSON, err := json.Marshal(nonNilStrings(trimNonEmpty(brief.ScopeOut)))
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: encode scope_out: %w", err)
	}
	// Reuse the read-view shapes so the stored JSON round-trips into
	// TaskBriefView without a second decoder.
	deliveryJSON, err := json.Marshal(DeliverySpec{
		SourceKind:  brief.Delivery.SourceKind,
		ThreadExtID: brief.Delivery.ThreadExtID,
		ReplyMode:   brief.Delivery.ReplyMode,
	})
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: encode delivery: %w", err)
	}
	rules := brief.Provenance.RuleIDs
	if rules == nil {
		rules = []int64{}
	}
	provenanceJSON, err := json.Marshal(Provenance{
		DecisionID:    brief.Provenance.DecisionID,
		PolicyVersion: brief.Provenance.PolicyVersion,
		Strength:      string(brief.Provenance.Strength),
		ActorExtID:    brief.Provenance.ActorExtID,
		RuleIDs:       rules,
	})
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: encode provenance: %w", err)
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	q := tx.Queries()

	row, err := q.CreateTaskBrief(ctx, store.CreateTaskBriefParams{
		IssueID:          issueID,
		IssueID_2:        issueID,
		Goal:             brief.Goal,
		AcceptanceJson:   string(acceptanceJSON),
		ScopeInJson:      string(scopeInJSON),
		ScopeOutJson:     string(scopeOutJSON),
		DeliveryJson:     string(deliveryJSON),
		ProvenanceJson:   string(provenanceJSON),
		IntakeDecisionID: sql.NullInt64{Int64: brief.Provenance.DecisionID, Valid: brief.Provenance.DecisionID > 0},
		PolicyVersion:    brief.Provenance.PolicyVersion,
		CreatedBy:        sql.NullInt64{Int64: createdBy, Valid: createdBy > 0},
	})
	if err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: create version: %w", err)
	}

	// Replace only the acceptance rows: the previous version's items are stale,
	// while manual execution items must survive a Brief change.
	if err := q.DeleteAcceptanceChecklistsForIssue(ctx, issueID); err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: clear acceptance: %w", err)
	}
	for _, item := range acceptance {
		if _, err := q.CreateChecklistWithKind(ctx, store.CreateChecklistWithKindParams{
			IssueID:   issueID,
			Title:     item,
			IssueID_2: issueID,
			Kind:      "acceptance",
			BriefID:   sql.NullInt64{Int64: row.ID, Valid: true},
		}); err != nil {
			return store.TaskBrief{}, fmt.Errorf("task brief: materialize acceptance: %w", err)
		}
	}

	if err := q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{
		GoalCondition: brief.GoalCondition(),
		ID:            issueID,
	}); err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: render goal_condition: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return store.TaskBrief{}, fmt.Errorf("task brief: commit: %w", err)
	}
	return row, nil
}
