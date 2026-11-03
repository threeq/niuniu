package service

// Intake (任务来源) configuration + dry-run decision queue — the contract surface
// defined in design §6.9. This is deliberately the *contract* layer: it reads and
// writes intake_settings / intake_decisions and exposes stable JSON shapes for the
// frontend. The decision pipeline itself (dedupe → authorize → resolve → decide)
// lives in the intake package; when it lands it satisfies IntakeDecisionAcceptor
// and the accept endpoint starts producing issues. Until then accept returns
// ErrDecisionAcceptorUnavailable instead of silently dropping the queue entry.

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

// ErrDecisionAcceptorUnavailable is returned by AcceptDecision when no pipeline
// is wired yet. The handler maps it to 501 so the UI can distinguish "not built
// yet" from a real failure.
var ErrDecisionAcceptorUnavailable = errors.New("intake decision acceptor is not wired")

// Intake source kinds carry the same values as intake_settings.source_kind.
// Unknown kinds are accepted (forward-compatible); these are the ones the
// settings page pre-renders with defaults.
var intakeSourceKinds = []string{"im_bot", "github", "ci", "email", "webhook"}

// Defaults mirror the schema defaults in §6.8.
const (
	defaultIntakeMode     = "observe"
	defaultIntakeDailyCap = 4
)

var validIntakeModes = map[string]bool{"off": true, "observe": true, "auto": true}

// IntakeSettingDTO is one (project, source) intake configuration.
type IntakeSettingDTO struct {
	SourceKind string     `json:"source_kind"`
	Mode       string     `json:"mode"`
	DailyCap   int64      `json:"daily_cap"`
	AllowList  []string   `json:"allow_list"`
	BotList    []string   `json:"bot_list"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// IntakeSettingsView is the GET /intake/settings response. Every known source
// kind is present: kinds without a stored row are rendered with defaults so the
// settings page never has to synthesize them.
type IntakeSettingsView struct {
	ProjectID int64 `json:"project_id"`
	// Paused is the project-level stop switch: while true the pipeline forces
	// every source to mode off, so inferred/observe work stops immediately
	// while explicit requests keep working.
	Paused   bool               `json:"paused"`
	Settings []IntakeSettingDTO `json:"settings"`
}

// IntakeSettingInput is one settings row in a PUT body.
type IntakeSettingInput struct {
	SourceKind string   `json:"source_kind"`
	Mode       string   `json:"mode"`
	DailyCap   *int64   `json:"daily_cap"`
	AllowList  []string `json:"allow_list"`
	BotList    []string `json:"bot_list"`
}

// UpdateIntakeSettingsRequest is the PUT /intake/settings body.
type UpdateIntakeSettingsRequest struct {
	Settings []IntakeSettingInput `json:"settings"`
}

// IntakeDecisionDTO is one dry-run decision in the calibration queue, joined with
// its originating event so the page can render source/actor/thread without a
// second round trip.
type IntakeDecisionDTO struct {
	ID             int64           `json:"id"`
	EventID        int64           `json:"event_id"`
	ProjectID      int64           `json:"project_id"`
	SourceKind     string          `json:"source_kind"`
	EventKind      string          `json:"event_kind"`
	ActorExtID     string          `json:"actor_ext_id"`
	ActorName      string          `json:"actor_name"`
	ThreadExtID    string          `json:"thread_ext_id"`
	ModeAtDecision string          `json:"mode_at_decision"`
	Strength       string          `json:"strength"`
	Decision       string          `json:"decision"`
	Executed       bool            `json:"executed"`
	ReasonCode     string          `json:"reason_code"`
	ReasonText     string          `json:"reason_text"`
	MatchedRuleIDs []int64         `json:"matched_rule_ids"`
	PolicyVersion  int64           `json:"policy_version"`
	IssueID        *int64          `json:"issue_id"`
	WorkspaceID    *int64          `json:"workspace_id"`
	WouldDo        json.RawMessage `json:"would_do"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
}

// IgnoreDecisionInput records why a dry-run decision was rejected. A reason is
// mandatory: it is what makes the accept-rate metric meaningful.
type IgnoreDecisionInput struct {
	Reason     string `json:"reason"`
	ReasonCode string `json:"reason_code"`
	ReasonText string `json:"reason_text"`
}

// IntakeDecisionAcceptor executes an accepted dry-run decision (creates the
// issue/workspace from the suggested Task Brief). Implemented by the intake
// pipeline; nil until Phase 1.
type IntakeDecisionAcceptor interface {
	AcceptDecision(ctx context.Context, decision store.IntakeDecision) (issueID, workspaceID int64, err error)
}

// IntakeService backs the intake configuration + calibration queue endpoints.
type IntakeService struct {
	db       *store.DB
	q        *store.Queries
	acceptor IntakeDecisionAcceptor
}

func NewIntakeService(db *sql.DB, q *store.Queries) *IntakeService {
	return &IntakeService{db: store.Wrap(db), q: q}
}

// SetAcceptor wires the decision pipeline. Called from server.go once it exists.
func (s *IntakeService) SetAcceptor(a IntakeDecisionAcceptor) { s.acceptor = a }

// GetSettings returns every known source kind, stored rows first-class and
// missing kinds filled with schema defaults.
func (s *IntakeService) GetSettings(ctx context.Context, projectID int64) (IntakeSettingsView, error) {
	rows, err := s.q.ListIntakeSettingsByProject(ctx, projectID)
	if err != nil {
		return IntakeSettingsView{}, err
	}
	paused, err := s.GetPaused(ctx, projectID)
	if err != nil {
		return IntakeSettingsView{}, err
	}
	byKind := make(map[string]store.IntakeSetting, len(rows))
	for _, r := range rows {
		byKind[r.SourceKind] = r
	}
	view := IntakeSettingsView{ProjectID: projectID, Paused: paused, Settings: make([]IntakeSettingDTO, 0, len(byKind)+len(intakeSourceKinds))}
	seen := make(map[string]bool, len(byKind))
	for _, kind := range intakeSourceKinds {
		if r, ok := byKind[kind]; ok {
			view.Settings = append(view.Settings, intakeSettingDTO(r))
			seen[kind] = true
			continue
		}
		view.Settings = append(view.Settings, IntakeSettingDTO{
			SourceKind: kind,
			Mode:       defaultIntakeMode,
			DailyCap:   defaultIntakeDailyCap,
			AllowList:  []string{},
			BotList:    []string{},
		})
	}
	// Any customized kind outside the known set still appears.
	for _, r := range rows {
		if !seen[r.SourceKind] {
			view.Settings = append(view.Settings, intakeSettingDTO(r))
		}
	}
	return view, nil
}

// GetPaused reports the project-level intake stop switch. A missing row means
// not paused — the default for every project.
func (s *IntakeService) GetPaused(ctx context.Context, projectID int64) (bool, error) {
	row, err := s.q.GetIntakeProjectState(ctx, projectID)
	switch {
	case err == nil:
		return row.Paused != 0, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// SetPaused flips the project-level stop switch. It is deliberately separate
// from source settings: pausing must take effect immediately, not on save.
func (s *IntakeService) SetPaused(ctx context.Context, projectID int64, paused bool) (bool, error) {
	pausedInt := int64(0)
	if paused {
		pausedInt = 1
	}
	row, err := s.q.UpsertIntakeProjectState(ctx, store.UpsertIntakeProjectStateParams{
		ProjectID: projectID,
		Paused:    pausedInt,
	})
	if err != nil {
		return false, err
	}
	return row.Paused != 0, nil
}

// UpdateSettings upserts each provided row and, per §6.8, writes one policy
// version snapshot per touched (project, source) so the change is auditable.
func (s *IntakeService) UpdateSettings(ctx context.Context, projectID int64, req UpdateIntakeSettingsRequest) (IntakeSettingsView, error) {
	if len(req.Settings) == 0 {
		return IntakeSettingsView{}, fmt.Errorf("settings must not be empty")
	}
	for _, in := range req.Settings {
		if err := validateIntakeSetting(in); err != nil {
			return IntakeSettingsView{}, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntakeSettingsView{}, err
	}
	defer tx.Rollback()

	qtx := tx.Queries()
	for _, in := range req.Settings {
		dailyCap := int64(defaultIntakeDailyCap)
		if in.DailyCap != nil {
			dailyCap = *in.DailyCap
		}
		allowJSON, err := json.Marshal(nonNilStrings(in.AllowList))
		if err != nil {
			return IntakeSettingsView{}, err
		}
		botJSON, err := json.Marshal(nonNilStrings(in.BotList))
		if err != nil {
			return IntakeSettingsView{}, err
		}
		if _, err := qtx.UpsertIntakeSetting(ctx, store.UpsertIntakeSettingParams{
			ProjectID:     projectID,
			SourceKind:    strings.TrimSpace(in.SourceKind),
			Mode:          in.Mode,
			DailyCap:      dailyCap,
			AllowListJson: string(allowJSON),
			BotListJson:   string(botJSON),
		}); err != nil {
			return IntakeSettingsView{}, err
		}
		if err := s.writePolicyVersion(ctx, qtx, projectID, strings.TrimSpace(in.SourceKind)); err != nil {
			return IntakeSettingsView{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IntakeSettingsView{}, err
	}
	return s.GetSettings(ctx, projectID)
}

// SetMode is the narrow write used by the intake_set_mode MCP tool: it changes
// one source's mode and leaves the allow/bot lists and cap untouched.
func (s *IntakeService) SetMode(ctx context.Context, projectID int64, sourceKind, mode string) (IntakeSettingDTO, error) {
	sourceKind = strings.TrimSpace(sourceKind)
	if sourceKind == "" {
		return IntakeSettingDTO{}, fmt.Errorf("source_kind is required")
	}
	if !validIntakeModes[mode] {
		return IntakeSettingDTO{}, fmt.Errorf("mode must be one of off, observe, auto")
	}
	cur, err := s.q.GetIntakeSetting(ctx, store.GetIntakeSettingParams{
		ProjectID: projectID, SourceKind: sourceKind,
	})
	allow := []string{}
	bot := []string{}
	dailyCap := int64(defaultIntakeDailyCap)
	if err == nil {
		allow = decodeStringList(cur.AllowListJson)
		bot = decodeStringList(cur.BotListJson)
		dailyCap = cur.DailyCap
	} else if !errors.Is(err, sql.ErrNoRows) {
		return IntakeSettingDTO{}, err
	}
	view, err := s.UpdateSettings(ctx, projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: sourceKind, Mode: mode, DailyCap: &dailyCap, AllowList: allow, BotList: bot,
		}},
	})
	if err != nil {
		return IntakeSettingDTO{}, err
	}
	for _, st := range view.Settings {
		if st.SourceKind == sourceKind {
			return st, nil
		}
	}
	return IntakeSettingDTO{}, fmt.Errorf("setting not found after write")
}

// ─── Rules + policy versions ─────────────────────────────────────────────────

// IntakeRuleDTO is one intake rule with its structured conditions decoded.
type IntakeRuleDTO struct {
	ID          int64                 `json:"id"`
	ProjectID   int64                 `json:"project_id"`
	SourceKind  string                `json:"source_kind"`
	Name        string                `json:"name"`
	Instruction string                `json:"instruction"`
	Conditions  intake.RuleConditions `json:"conditions"`
	Priority    int64                 `json:"priority"`
	Enabled     bool                  `json:"enabled"`
	CreatedAt   time.Time             `json:"created_at"`
}

// UpsertIntakeRuleInput is the create/update body. Enabled defaults to true on
// create and keeps the current value on update.
type UpsertIntakeRuleInput struct {
	SourceKind  string                `json:"source_kind"`
	Name        string                `json:"name"`
	Instruction string                `json:"instruction"`
	Conditions  intake.RuleConditions `json:"conditions"`
	Priority    int64                 `json:"priority"`
	Enabled     *bool                 `json:"enabled"`
}

// IntakePolicyVersionDTO is one auditable policy snapshot.
type IntakePolicyVersionDTO struct {
	ID         int64           `json:"id"`
	ProjectID  int64           `json:"project_id"`
	SourceKind string          `json:"source_kind"`
	Version    int64           `json:"version"`
	Snapshot   json.RawMessage `json:"snapshot"`
	CreatedAt  time.Time       `json:"created_at"`
}

// ListRules returns every rule for a project, optionally filtered by source.
// Order is priority DESC so it matches evaluation order.
func (s *IntakeService) ListRules(ctx context.Context, projectID int64, sourceKind string) ([]IntakeRuleDTO, error) {
	rows, err := s.q.ListIntakeRulesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sourceKind = strings.TrimSpace(sourceKind)
	out := make([]IntakeRuleDTO, 0, len(rows))
	for _, r := range rows {
		if sourceKind != "" && r.SourceKind != sourceKind {
			continue
		}
		out = append(out, intakeRuleDTO(r))
	}
	return out, nil
}

// CreateRule adds a rule and records a policy version for its source, so the
// change is auditable and the next event evaluates against it.
func (s *IntakeService) CreateRule(ctx context.Context, projectID int64, in UpsertIntakeRuleInput) (IntakeRuleDTO, error) {
	normalized, conditionsJSON, enabled, err := normalizeIntakeRule(in, true)
	if err != nil {
		return IntakeRuleDTO{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntakeRuleDTO{}, err
	}
	defer tx.Rollback()

	qtx := tx.Queries()
	row, err := qtx.CreateIntakeRule(ctx, store.CreateIntakeRuleParams{
		ProjectID:      projectID,
		SourceKind:     normalized.SourceKind,
		Name:           normalized.Name,
		Instruction:    normalized.Instruction,
		ConditionsJson: conditionsJSON,
		Priority:       normalized.Priority,
		Enabled:        enabled,
	})
	if err != nil {
		return IntakeRuleDTO{}, err
	}
	if err := s.writePolicyVersion(ctx, qtx, projectID, normalized.SourceKind); err != nil {
		return IntakeRuleDTO{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntakeRuleDTO{}, err
	}
	return intakeRuleDTO(row), nil
}

// UpdateRule edits a rule in place. The source kind is immutable: moving a rule
// between sources would silently change two policies, so the caller deletes and
// recreates instead.
func (s *IntakeService) UpdateRule(ctx context.Context, projectID, ruleID int64, in UpsertIntakeRuleInput) (IntakeRuleDTO, error) {
	cur, err := s.ruleInProject(ctx, projectID, ruleID)
	if err != nil {
		return IntakeRuleDTO{}, err
	}
	in.SourceKind = cur.SourceKind
	normalized, conditionsJSON, enabled, err := normalizeIntakeRule(in, cur.Enabled != 0)
	if err != nil {
		return IntakeRuleDTO{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntakeRuleDTO{}, err
	}
	defer tx.Rollback()

	qtx := tx.Queries()
	row, err := qtx.UpdateIntakeRule(ctx, store.UpdateIntakeRuleParams{
		Name:           normalized.Name,
		Instruction:    normalized.Instruction,
		ConditionsJson: conditionsJSON,
		Priority:       normalized.Priority,
		Enabled:        enabled,
		ID:             ruleID,
	})
	if err != nil {
		return IntakeRuleDTO{}, err
	}
	if err := s.writePolicyVersion(ctx, qtx, projectID, cur.SourceKind); err != nil {
		return IntakeRuleDTO{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntakeRuleDTO{}, err
	}
	return intakeRuleDTO(row), nil
}

// DeleteRule removes a rule and records a policy version for its source.
func (s *IntakeService) DeleteRule(ctx context.Context, projectID, ruleID int64) error {
	cur, err := s.ruleInProject(ctx, projectID, ruleID)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qtx := tx.Queries()
	if err := qtx.DeleteIntakeRule(ctx, ruleID); err != nil {
		return err
	}
	if err := s.writePolicyVersion(ctx, qtx, projectID, cur.SourceKind); err != nil {
		return err
	}
	return tx.Commit()
}

// ListPolicyVersions returns a source's policy history, newest first.
func (s *IntakeService) ListPolicyVersions(ctx context.Context, projectID int64, sourceKind string, limit int) ([]IntakePolicyVersionDTO, error) {
	sourceKind = strings.TrimSpace(sourceKind)
	if sourceKind == "" {
		return nil, fmt.Errorf("source_kind is required")
	}
	rows, err := s.q.ListIntakePolicyVersions(ctx, store.ListIntakePolicyVersionsParams{
		ProjectID: projectID, SourceKind: sourceKind,
	})
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]IntakePolicyVersionDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, IntakePolicyVersionDTO{
			ID: r.ID, ProjectID: r.ProjectID, SourceKind: r.SourceKind,
			Version: r.Version, Snapshot: json.RawMessage(orJSONObject(r.SnapshotJson)), CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// writePolicyVersion snapshots the effective policy — settings plus every rule
// for the source — under the next monotonic version. Called in the same
// transaction as the settings/rule write so a version never describes a change
// that did not land.
func (s *IntakeService) writePolicyVersion(ctx context.Context, qtx *store.Queries, projectID int64, sourceKind string) error {
	sourceKind = strings.TrimSpace(sourceKind)
	snapshot := map[string]any{
		"source_kind": sourceKind,
		// No stored setting means the runtime policy is off (see
		// imbotPolicy.LoadPolicy): explicit work only, no inferred work.
		"mode":       "off",
		"daily_cap":  int64(0),
		"allow_list": []string{},
		"bot_list":   []string{},
		"rules":      []any{},
	}
	row, err := qtx.GetIntakeSetting(ctx, store.GetIntakeSettingParams{ProjectID: projectID, SourceKind: sourceKind})
	switch {
	case err == nil:
		snapshot["mode"] = row.Mode
		snapshot["daily_cap"] = row.DailyCap
		snapshot["allow_list"] = decodeStringList(row.AllowListJson)
		snapshot["bot_list"] = decodeStringList(row.BotListJson)
	case errors.Is(err, sql.ErrNoRows):
		// defaults above
	default:
		return err
	}

	rules, err := qtx.ListIntakeRulesByProject(ctx, projectID)
	if err != nil {
		return err
	}
	rulesSnapshot := make([]map[string]any, 0)
	for _, r := range rules {
		if r.SourceKind != sourceKind {
			continue
		}
		var conditions intake.RuleConditions
		_ = json.Unmarshal([]byte(r.ConditionsJson), &conditions)
		rulesSnapshot = append(rulesSnapshot, map[string]any{
			"id":          r.ID,
			"name":        r.Name,
			"instruction": r.Instruction,
			"conditions":  conditions,
			"priority":    r.Priority,
			"enabled":     r.Enabled != 0,
		})
	}
	snapshot["rules"] = rulesSnapshot

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = qtx.CreateIntakePolicyVersion(ctx, store.CreateIntakePolicyVersionParams{
		ProjectID:    projectID,
		SourceKind:   sourceKind,
		ProjectID_2:  projectID,
		SourceKind_2: sourceKind,
		SnapshotJson: string(encoded),
	})
	return err
}

func (s *IntakeService) ruleInProject(ctx context.Context, projectID, ruleID int64) (store.IntakeRule, error) {
	rule, err := s.q.GetIntakeRule(ctx, ruleID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.IntakeRule{}, ErrNotFound
		}
		return store.IntakeRule{}, err
	}
	if rule.ProjectID != projectID {
		return store.IntakeRule{}, ErrNotFound
	}
	return rule, nil
}

func intakeRuleDTO(r store.IntakeRule) IntakeRuleDTO {
	var conditions intake.RuleConditions
	if strings.TrimSpace(r.ConditionsJson) != "" {
		_ = json.Unmarshal([]byte(r.ConditionsJson), &conditions)
	}
	return IntakeRuleDTO{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		SourceKind:  r.SourceKind,
		Name:        r.Name,
		Instruction: r.Instruction,
		Conditions:  conditions,
		Priority:    r.Priority,
		Enabled:     r.Enabled != 0,
		CreatedAt:   r.CreatedAt,
	}
}

// normalizeIntakeRule trims and validates a rule input and encodes its
// conditions. A rule with no condition group is rejected: it could never match
// (an empty rule is not a catch-all), so storing it would only mislead.
func normalizeIntakeRule(in UpsertIntakeRuleInput, defaultEnabled bool) (UpsertIntakeRuleInput, string, int64, error) {
	in.SourceKind = strings.TrimSpace(in.SourceKind)
	in.Name = strings.TrimSpace(in.Name)
	in.Instruction = strings.TrimSpace(in.Instruction)
	if in.SourceKind == "" {
		return in, "", 0, fmt.Errorf("source_kind is required")
	}
	if in.Name == "" {
		return in, "", 0, fmt.Errorf("name is required")
	}
	in.Conditions.Actors = trimNonEmpty(in.Conditions.Actors)
	in.Conditions.Keywords = trimNonEmpty(in.Conditions.Keywords)
	if len(in.Conditions.Actors) == 0 && len(in.Conditions.Keywords) == 0 {
		return in, "", 0, fmt.Errorf("a rule needs at least one condition (actors or keywords)")
	}
	encoded, err := json.Marshal(in.Conditions)
	if err != nil {
		return in, "", 0, err
	}
	enabled := int64(0)
	if defaultEnabled {
		enabled = 1
	}
	if in.Enabled != nil {
		enabled = 0
		if *in.Enabled {
			enabled = 1
		}
	}
	return in, string(encoded), enabled, nil
}

func trimNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ListDecisions returns decisions joined with their events. executed/decision
// are optional filters; the calibration queue passes executed=0&decision=accept.
func (s *IntakeService) ListDecisions(ctx context.Context, projectID int64, executed *bool, decision string, limit int) ([]IntakeDecisionDTO, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
SELECT d.id, d.event_id, d.project_id, d.mode_at_decision, d.strength, d.decision,
       d.executed, d.reason_code, d.reason_text, d.matched_rule_ids_json,
       d.policy_version, d.issue_id, d.workspace_id, d.would_do_json, d.created_at,
       e.source_kind, e.event_kind, e.actor_ext_id, e.actor_name, e.thread_ext_id, e.payload_json
FROM intake_decisions d
JOIN intake_events e ON e.id = d.event_id
WHERE d.project_id = ?`
	args := []any{projectID}
	if executed != nil {
		query += " AND d.executed = ?"
		if *executed {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if decision != "" {
		query += " AND d.decision = ?"
		args = append(args, decision)
	}
	query += " ORDER BY d.created_at DESC, d.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]IntakeDecisionDTO, 0)
	for rows.Next() {
		var (
			d         IntakeDecisionDTO
			executedV int64
			matched   string
			wouldDo   string
			payload   string
			issueID   sql.NullInt64
			workspace sql.NullInt64
		)
		if err := rows.Scan(
			&d.ID, &d.EventID, &d.ProjectID, &d.ModeAtDecision, &d.Strength, &d.Decision,
			&executedV, &d.ReasonCode, &d.ReasonText, &matched,
			&d.PolicyVersion, &issueID, &workspace, &wouldDo, &d.CreatedAt,
			&d.SourceKind, &d.EventKind, &d.ActorExtID, &d.ActorName, &d.ThreadExtID, &payload,
		); err != nil {
			return nil, err
		}
		d.Executed = executedV != 0
		d.MatchedRuleIDs = decodeInt64List(matched)
		d.WouldDo = json.RawMessage(orJSONObject(wouldDo))
		d.Payload = json.RawMessage(orJSONObject(payload))
		if issueID.Valid {
			v := issueID.Int64
			d.IssueID = &v
		}
		if workspace.Valid {
			v := workspace.Int64
			d.WorkspaceID = &v
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDecision returns one decision for authorization before accept/ignore.
func (s *IntakeService) GetDecision(ctx context.Context, decisionID int64) (store.IntakeDecision, error) {
	return s.q.GetIntakeDecision(ctx, decisionID)
}

// AcceptDecision executes a pending dry-run decision through the wired pipeline.
func (s *IntakeService) AcceptDecision(ctx context.Context, decisionID int64) (IntakeDecisionDTO, error) {
	d, err := s.q.GetIntakeDecision(ctx, decisionID)
	if err != nil {
		return IntakeDecisionDTO{}, err
	}
	if d.Executed != 0 {
		return IntakeDecisionDTO{}, fmt.Errorf("decision %d is already executed", decisionID)
	}
	if s.acceptor == nil {
		return IntakeDecisionDTO{}, ErrDecisionAcceptorUnavailable
	}
	issueID, workspaceID, err := s.acceptor.AcceptDecision(ctx, d)
	if err != nil {
		return IntakeDecisionDTO{}, err
	}
	updated, err := s.q.SetIntakeDecisionExecuted(ctx, store.SetIntakeDecisionExecutedParams{
		IssueID:     sql.NullInt64{Int64: issueID, Valid: issueID > 0},
		WorkspaceID: sql.NullInt64{Int64: workspaceID, Valid: workspaceID > 0},
		ID:          decisionID,
	})
	if err != nil {
		return IntakeDecisionDTO{}, err
	}
	return s.decisionByID(ctx, updated.ProjectID, updated.ID)
}

// IgnoreDecision re-labels a pending decision as ignored, recording the reason.
func (s *IntakeService) IgnoreDecision(ctx context.Context, decisionID int64, in IgnoreDecisionInput) (IntakeDecisionDTO, error) {
	reasonText := strings.TrimSpace(in.ReasonText)
	if reasonText == "" {
		reasonText = strings.TrimSpace(in.Reason)
	}
	reasonCode := strings.TrimSpace(in.ReasonCode)
	if reasonText == "" && reasonCode == "" {
		return IntakeDecisionDTO{}, fmt.Errorf("a reason is required to ignore a decision")
	}
	d, err := s.q.GetIntakeDecision(ctx, decisionID)
	if err != nil {
		return IntakeDecisionDTO{}, err
	}
	if d.Executed != 0 {
		return IntakeDecisionDTO{}, fmt.Errorf("decision %d is already executed", decisionID)
	}
	updated, err := s.q.SetIntakeDecisionOutcome(ctx, store.SetIntakeDecisionOutcomeParams{
		Decision:   "ignore",
		ReasonCode: reasonCode,
		ReasonText: reasonText,
		ID:         decisionID,
	})
	if err != nil {
		return IntakeDecisionDTO{}, err
	}
	return s.decisionByID(ctx, updated.ProjectID, updated.ID)
}

// decisionByID re-reads one decision through the joined list query so accept and
// ignore return the same shape the queue list does.
func (s *IntakeService) decisionByID(ctx context.Context, projectID, decisionID int64) (IntakeDecisionDTO, error) {
	items, err := s.ListDecisions(ctx, projectID, nil, "", 500)
	if err != nil {
		return IntakeDecisionDTO{}, err
	}
	for _, it := range items {
		if it.ID == decisionID {
			return it, nil
		}
	}
	return IntakeDecisionDTO{}, sql.ErrNoRows
}

func intakeSettingDTO(r store.IntakeSetting) IntakeSettingDTO {
	updated := r.UpdatedAt
	return IntakeSettingDTO{
		SourceKind: r.SourceKind,
		Mode:       r.Mode,
		DailyCap:   r.DailyCap,
		AllowList:  decodeStringList(r.AllowListJson),
		BotList:    decodeStringList(r.BotListJson),
		UpdatedAt:  &updated,
	}
}

func validateIntakeSetting(in IntakeSettingInput) error {
	if strings.TrimSpace(in.SourceKind) == "" {
		return fmt.Errorf("source_kind is required")
	}
	if !validIntakeModes[in.Mode] {
		return fmt.Errorf("mode must be one of off, observe, auto")
	}
	if in.DailyCap != nil && *in.DailyCap < 0 {
		return fmt.Errorf("daily_cap must be >= 0")
	}
	return nil
}

func decodeStringList(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func decodeInt64List(raw string) []int64 {
	out := []int64{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []int64{}
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func orJSONObject(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}
