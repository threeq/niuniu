package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

var validLifecycleStatuses = map[string]bool{
	"created": true, "spec": true, "spec-review": true,
	"plan": true, "plan-review": true, "implement": true,
	"implement-review": true, "test": true, "completed": true,
}

func validateLifecycleMapping(mapping string) error {
	if mapping == "" {
		return nil
	}
	for _, s := range strings.Split(mapping, ",") {
		s = strings.TrimSpace(s)
		if !validLifecycleStatuses[s] {
			return fmt.Errorf("invalid lifecycle status in mapping: %s", s)
		}
	}
	return nil
}

// ErrProjectNameExists is returned when trying to create a project with a name that already exists
var ErrProjectNameExists = errors.New("project name already exists")

// Sentinel errors for issue mutation rejections. Handlers map these via
// errors.Is to JSON error codes; downstream callers should not pattern-match
// on the textual message. The wrapped errors include positional detail
// (which user id, which label id, etc.) for diagnostic logging.
var (
	// ErrTooMany is returned by replaceAssignees / replaceLabels when the
	// caller-supplied id slice exceeds the per-issue cap (100).
	ErrTooMany = errors.New("too many items in array")
	// ErrInvalidAssignee signals a user id that is not a member of the
	// project's owning org (or, for user-owned projects, not the owner).
	ErrInvalidAssignee = errors.New("invalid assignee")
	// ErrInvalidLabel signals a label id that does not belong to the same
	// project as the target issue (or could not be loaded).
	ErrInvalidLabel = errors.New("invalid label")
)

// IssueDetail couples an Issue with its populated assignees and labels arrays
// (phase-1 normalized model). The legacy `Issue.Labels`/`Issue.Assignee`
// sql.NullString fields are still present on the embedded Issue, but
// downstream callers should ignore them in favor of these arrays. Task 9 will
// re-shape the IssueResponse DTO to drop the legacy strings entirely.
//
// ProjectID, ChecklistTotal and ChecklistCompleted are populated when the
// detail comes via loadIssueDetail (the GET /issues/:id path). They let the
// handler skip the previously-separate GetColumn + GetChecklistStats round
// trips. Callers that build IssueDetail from list endpoints (which still go
// through attachAssigneesAndLabels) leave these zero.
type IssueDetail struct {
	store.Issue
	Assignees          []store.ListIssueAssigneesRow `json:"assignees"`
	Labels             []store.Label                 `json:"labels"`
	ProjectID          int64                         `json:"project_id"`
	ChecklistTotal     int64                         `json:"checklist_total"`
	ChecklistCompleted int64                         `json:"checklist_completed"`
}

type KanbanService struct {
	db          *store.DB
	q           *store.Queries
	activitySvc *IssueActivityService
	eventBus    *event.Bus
	notifyHub   *notify.NotificationHub
	runSvc      *RunService // optional; nil = legacy bare-update path only (Phase 0 tests)
	// maxBatchIssues caps batch_create_issues fan-out (orchestration cost
	// guardrail, spec 2026-06-06 section 16). 0 = unlimited. Set via
	// SetMaxBatchIssues from config at wiring time.
	maxBatchIssues int
	// orchSettings, when set, supplies the live max-batch cap; falls back to the
	// static maxBatchIssues above when nil.
	orchSettings *OrchestrationSettings
}

// SetMaxBatchIssues sets the per-call cap on BatchCreateIssues (0 = unlimited).
func (s *KanbanService) SetMaxBatchIssues(n int) { s.maxBatchIssues = n }

// SetOrchestrationSettings wires the live settings source for the batch cap.
func (s *KanbanService) SetOrchestrationSettings(o *OrchestrationSettings) { s.orchSettings = o }

func (s *KanbanService) effMaxBatch(ctx context.Context) int {
	if s.orchSettings != nil {
		return s.orchSettings.MaxBatchIssues(ctx)
	}
	return s.maxBatchIssues
}

func NewKanbanService(db *sql.DB, q *store.Queries, activitySvc *IssueActivityService, eventBus *event.Bus, notifyHub *notify.NotificationHub) *KanbanService {
	return &KanbanService{db: store.Wrap(db), q: q, activitySvc: activitySvc, eventBus: eventBus, notifyHub: notifyHub}
}

// WithRunService attaches a RunService so KanbanService.MoveIssue delegates to
// RunService.MoveIssueRunAware (Run-aware path). Called from server.go DI after
// both services are constructed. Nil-safe: if not called, MoveIssue falls back
// to the legacy bare-update path (preserves Phase 0 test compatibility).
func (s *KanbanService) WithRunService(runSvc *RunService) {
	s.runSvc = runSvc
}

// Column operations

func (s *KanbanService) ListColumns(ctx context.Context, projectID int64) ([]store.Column, error) {
	return s.q.ListColumnsByProject(ctx, projectID)
}

func (s *KanbanService) GetColumn(ctx context.Context, id int64) (store.Column, error) {
	return s.q.GetColumn(ctx, id)
}

// GetColumnsByIDs resolves many columns in one query, returning them keyed by
// id. Used to avoid a per-row GetColumn N+1 when mapping a list of issues back
// to their columns/projects. Missing ids are simply absent from the map.
func (s *KanbanService) GetColumnsByIDs(ctx context.Context, ids []int64) (map[int64]store.Column, error) {
	out := make(map[int64]store.Column, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	cols, err := s.q.GetColumnsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		out[c.ID] = c
	}
	return out, nil
}

// ListAttentionIssues returns issues in a terminal "needs human" exec_status
// (blocked-needs-human / waiting-user-input / abandoned, spec §19) across the
// caller's accessible owners (personal user id + member org ids). The handler
// resolves the owner scope via authz; an empty orgIDs is normalised to {-1} so
// the IN(...) slice never collapses to invalid SQL.
func (s *KanbanService) ListAttentionIssues(ctx context.Context, ownerUserID int64, orgIDs []int64) ([]store.Issue, error) {
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListAttentionIssuesForOwners(ctx, store.ListAttentionIssuesForOwnersParams{
		OwnerID: ownerUserID,
		OrgIds:  orgIDs,
	})
}

func (s *KanbanService) CreateColumn(ctx context.Context, projectID int64, name string, lifecycleMapping string) (store.Column, error) {
	if err := validateLifecycleMapping(lifecycleMapping); err != nil {
		return store.Column{}, err
	}

	// Auto-calculate position as max(existing positions) + 1 to handle gaps from deleted columns
	columns, err := s.q.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return store.Column{}, fmt.Errorf("list columns for position: %w", err)
	}
	var position int64
	for _, col := range columns {
		if col.Position >= position {
			position = col.Position + 1
		}
	}

	return s.q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID:        projectID,
		Name:             name,
		Position:         position,
		LifecycleMapping: lifecycleMapping,
	})
}

func (s *KanbanService) UpdateColumn(ctx context.Context, id int64, name string) (store.Column, error) {
	return s.q.UpdateColumn(ctx, store.UpdateColumnParams{
		ID:   id,
		Name: name,
	})
}

// UpdateColumnExtensionInput carries the column-based Harness model fields.
// Pointer fields keep "absent vs explicit empty" distinct so PATCH-style
// callers can set blank without confusing it with no-op.
type UpdateColumnExtensionInput struct {
	ReviewerAgent *string
	PhasePrompt   *string
	OpPrimitive   *string // nil=unchanged; enum none|instruct|complete
	WhenToUse     *string // nil=unchanged; "" clears (writes NULL)
	AutoAdvance   *bool   // nil=unchanged (the reshaped column editor no longer sends it)
}

// ColumnOpFields carries the migrate-only AI-native columns that are not on the
// sqlc store.Column struct (op_primitive / when_to_use live as migrate.go ALTERs
// outside the schema CREATE block, so SELECT*/RETURNING* never widen on regen).
type ColumnOpFields struct {
	OpPrimitive string
	WhenToUse   *string
}

func validateOpPrimitive(p string) error {
	switch p {
	case "none", "instruct", "complete":
		return nil
	}
	return fmt.Errorf("invalid op_primitive %q", p)
}

// ensureSameProject validates that an issue in childColumnID and the given parent
// Epic belong to the same project (Executable Epic constraint). Returns a non-nil
// error if they differ or any lookup fails.
func (s *KanbanService) ensureSameProject(ctx context.Context, childColumnID, parentIssueID int64) error {
	childCol, err := s.q.GetColumn(ctx, childColumnID)
	if err != nil {
		return fmt.Errorf("resolve child column: %w", err)
	}
	parent, err := s.q.GetIssue(ctx, parentIssueID)
	if err != nil {
		return fmt.Errorf("parent issue %d out of scope", parentIssueID)
	}
	parentCol, err := s.q.GetColumn(ctx, parent.ColumnID)
	if err != nil {
		return fmt.Errorf("resolve parent column: %w", err)
	}
	if childCol.ProjectID != parentCol.ProjectID {
		return fmt.Errorf("parent and child issues must belong to the same project")
	}
	return nil
}

// wouldCreateParentCycle reports whether making parentIssueID the parent of
// issueID would create a cycle (issueID is an ancestor of parentIssueID, or the
// chain already loops). Walks up parentIssueID's ancestor chain; the iteration
// cap also breaks out of any pre-existing cycle rather than spinning forever.
func (s *KanbanService) wouldCreateParentCycle(ctx context.Context, issueID, parentIssueID int64) (bool, error) {
	cur := parentIssueID
	for i := 0; i < 1000 && cur != 0; i++ {
		if cur == issueID {
			return true, nil
		}
		iss, err := s.q.GetIssue(ctx, cur)
		if err != nil {
			return false, fmt.Errorf("walk parent chain at %d: %w", cur, err)
		}
		if !iss.ParentIssueID.Valid {
			return false, nil
		}
		cur = iss.ParentIssueID.Int64
	}
	return false, nil
}

// ensureTwoLevelParent enforces the max-2-level hierarchy when attaching child
// issueID under parentIssueID: the parent must itself be top-level (no parent),
// and the child must not already have children. Pass issueID == 0 for a
// not-yet-created child (skips the has-children check).
func (s *KanbanService) ensureTwoLevelParent(ctx context.Context, issueID, parentIssueID int64) error {
	parent, err := s.q.GetIssue(ctx, parentIssueID)
	if err != nil {
		return fmt.Errorf("parent issue %d out of scope", parentIssueID)
	}
	if parent.ParentIssueID.Valid {
		return fmt.Errorf("max two levels: cannot nest under a sub-task")
	}
	if issueID != 0 {
		kids, err := s.q.ListIssuesByParent(ctx, sql.NullInt64{Int64: issueID, Valid: true})
		if err != nil {
			return err
		}
		if len(kids) > 0 {
			return fmt.Errorf("max two levels: an issue with sub-tasks cannot become a child")
		}
	}
	return nil
}

// syncIssueType derives an issue's type from whether it currently has children:
// an issue with >=1 child is an Epic, otherwise a plain task. This is the single
// source of truth for issue_type — the UI never sets it manually; the service
// calls this whenever a potential parent gains or loses a child. No-op write when
// already correct; issueID == 0 is ignored. Best-effort: errors are returned so
// callers can log, but a sync failure must not fail the originating mutation.
func (s *KanbanService) syncIssueType(ctx context.Context, issueID int64) error {
	if issueID == 0 {
		return nil
	}
	kids, err := s.q.ListIssuesByParent(ctx, sql.NullInt64{Int64: issueID, Valid: true})
	if err != nil {
		return err
	}
	want := "task"
	if len(kids) > 0 {
		want = "epic"
	}
	cur, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if cur.IssueType == want {
		return nil
	}
	return s.q.SetIssueType(ctx, store.SetIssueTypeParams{IssueType: want, ID: issueID})
}

// SetIssueExecFields sets the Executable Epic hierarchy + execution fields on an
// existing issue: parent (nil clears it), issue_type, exec_wave, exec_status.
// Enforces the same-project parent constraint and rejects self-parenting. Used by
// the UI hierarchy endpoint, MCP, and the Epic execution engine.
func (s *KanbanService) SetIssueExecFields(ctx context.Context, issueID int64, parentIssueID *int64, issueType string, execWave int64, execStatus string) (IssueDetail, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return IssueDetail{}, err
	}
	if parentIssueID != nil {
		if *parentIssueID == issueID {
			return IssueDetail{}, fmt.Errorf("an issue cannot be its own parent")
		}
		if err := s.ensureSameProject(ctx, issue.ColumnID, *parentIssueID); err != nil {
			return IssueDetail{}, err
		}
		cyclic, err := s.wouldCreateParentCycle(ctx, issueID, *parentIssueID)
		if err != nil {
			return IssueDetail{}, err
		}
		if cyclic {
			return IssueDetail{}, fmt.Errorf("assigning parent %d would create a parent cycle", *parentIssueID)
		}
		if err := s.ensureTwoLevelParent(ctx, issueID, *parentIssueID); err != nil {
			return IssueDetail{}, err
		}
	}
	execStatus = normalizeExecStatus(execStatus)
	// The CHECK on exec_status lives only in the schema files (fresh DBs); upgraded
	// DBs get the bare column, so the service is the only guard there.
	if !validExecStatus(execStatus) {
		return IssueDetail{}, fmt.Errorf("invalid exec_status %q (want idle|running|done|failed|paused|reviewing)", execStatus)
	}
	oldParent := int64(0)
	if issue.ParentIssueID.Valid {
		oldParent = issue.ParentIssueID.Int64
	}
	// issue_type is NOT chosen here — it is derived from child presence. Preserve
	// the row's current type on the write; syncIssueType below recomputes it (and
	// the affected parents') authoritatively. The issueType parameter is retained
	// for API/MCP back-compat but intentionally ignored.
	_ = issueType
	if _, err := s.q.SetIssueExecFields(ctx, store.SetIssueExecFieldsParams{
		ParentIssueID: toNullInt64(parentIssueID),
		IssueType:     issue.IssueType,
		ExecWave:      execWave,
		ExecStatus:    execStatus,
		ID:            issueID,
	}); err != nil {
		return IssueDetail{}, err
	}
	// Recompute derived issue_type for the issue itself (its own children), the new
	// parent (gained a child) and the old parent (may have lost its last child).
	newParent := int64(0)
	if parentIssueID != nil {
		newParent = *parentIssueID
	}
	for _, pid := range []int64{issueID, newParent, oldParent} {
		if err := s.syncIssueType(ctx, pid); err != nil {
			slog.Warn("syncIssueType in SetIssueExecFields failed", "issue", pid, "error", err)
		}
	}
	return s.loadIssueDetail(ctx, issueID)
}

// ListChildIssues returns the children of an Epic ordered by wave then position.
func (s *KanbanService) ListChildIssues(ctx context.Context, parentIssueID int64) ([]store.Issue, error) {
	return s.q.ListIssuesByParent(ctx, sql.NullInt64{Int64: parentIssueID, Valid: true})
}

// ListColumnGateSpecs lists the gate specs associated with a column's exit gate.
func (s *KanbanService) ListColumnGateSpecs(ctx context.Context, columnID int64) ([]store.ColumnGateSpec, error) {
	return s.q.ListColumnGateSpecs(ctx, columnID)
}

// ListColumnGateSpecsByColumns batches ListColumnGateSpecs across many columns,
// returning bindings grouped by column_id (each group in position order). Lets
// the columns endpoint avoid a per-column gate-spec query.
func (s *KanbanService) ListColumnGateSpecsByColumns(ctx context.Context, columnIDs []int64) (map[int64][]store.ColumnGateSpec, error) {
	out := make(map[int64][]store.ColumnGateSpec, len(columnIDs))
	if len(columnIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.ListColumnGateSpecsByColumns(ctx, columnIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ColumnID] = append(out[r.ColumnID], r)
	}
	return out, nil
}

// GetHarnessSpec exposes spec lookup so handlers can hydrate column DTO badges
// without bypassing the service layer. Read-only — no authz gate beyond the
// caller's existing project-scope check.
func (s *KanbanService) GetHarnessSpec(ctx context.Context, id int64) (store.HarnessSpec, error) {
	return s.q.GetHarnessSpec(ctx, id)
}

// GetHarnessSpecsByIDs batches GetHarnessSpec, returning specs keyed by id.
func (s *KanbanService) GetHarnessSpecsByIDs(ctx context.Context, ids []int64) (map[int64]store.HarnessSpec, error) {
	out := make(map[int64]store.HarnessSpec, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	specs, err := s.q.GetHarnessSpecsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, sp := range specs {
		out[sp.ID] = sp
	}
	return out, nil
}

// ReplaceColumnGateSpecs replaces the column's exit gate spec list with the given
// ordered spec_id slice. Validates that each spec belongs to the same owner as
// the column's project (no cross-owner spec leakage). Transactional.
// GateSpecBinding binds a harness gate spec to a column with an applicability:
// 'if_routed' (column-level gate, runs only when the issue routes out of this column)
// or 'always' (project floor, enforced at completion regardless of route). Blank
// applicability defaults to 'if_routed'. Spec §5/§18.
type GateSpecBinding struct {
	SpecID        int64
	Applicability string
}

func (s *KanbanService) ReplaceColumnGateSpecs(ctx context.Context, columnID int64, bindings []GateSpecBinding) ([]store.ColumnGateSpec, error) {
	col, err := s.q.GetColumn(ctx, columnID)
	if err != nil {
		return nil, fmt.Errorf("column lookup: %w", err)
	}
	if _, err := s.q.GetProject(ctx, col.ProjectID); err != nil {
		return nil, fmt.Errorf("project lookup: %w", err)
	}
	for i := range bindings {
		if bindings[i].Applicability == "" {
			bindings[i].Applicability = "if_routed"
		}
		if bindings[i].Applicability != "if_routed" && bindings[i].Applicability != "always" {
			return nil, fmt.Errorf("invalid applicability %q (want if_routed|always)", bindings[i].Applicability)
		}
		// harness_specs is a single global library — any existing spec may be
		// bound to any kanban column; only verify it exists.
		if _, err := s.q.GetHarnessSpec(ctx, bindings[i].SpecID); err != nil {
			return nil, fmt.Errorf("spec %d out of scope", bindings[i].SpecID)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	q := tx.Queries()
	if err := q.DeleteColumnGateSpecs(ctx, columnID); err != nil {
		return nil, fmt.Errorf("clear gates: %w", err)
	}
	for i, b := range bindings {
		if err := q.InsertColumnGateSpec(ctx, store.InsertColumnGateSpecParams{
			ColumnID:      columnID,
			SpecID:        b.SpecID,
			Position:      int64(i),
			Applicability: b.Applicability,
		}); err != nil {
			return nil, fmt.Errorf("insert gate %d: %w", b.SpecID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.q.ListColumnGateSpecs(ctx, columnID)
}

// UpdateColumnExtension updates the column-based Harness fields on a column.
// nil pointer = leave field unchanged; non-nil = explicit value (empty string clears).
func (s *KanbanService) UpdateColumnExtension(ctx context.Context, id int64, in UpdateColumnExtensionInput) (store.Column, error) {
	cur, err := s.q.GetColumn(ctx, id)
	if err != nil {
		return store.Column{}, err
	}
	rev := cur.ReviewerAgent
	if in.ReviewerAgent != nil {
		rev = sql.NullString{String: *in.ReviewerAgent, Valid: true}
	}
	prompt := cur.PhasePrompt
	if in.PhasePrompt != nil {
		prompt = sql.NullString{String: *in.PhasePrompt, Valid: true}
	}
	auto := cur.AutoAdvance // nil pointer = keep current value
	if in.AutoAdvance != nil {
		auto = 0
		if *in.AutoAdvance {
			auto = 1
		}
	}
	col, err := s.q.UpdateColumnExtension(ctx, store.UpdateColumnExtensionParams{
		ReviewerAgent: rev,
		PhasePrompt:   prompt,
		AutoAdvance:   auto,
		ID:            id,
	})
	if err != nil {
		return store.Column{}, err
	}
	// op_primitive / when_to_use live only as migrate.go ALTER columns (deliberately
	// outside the schema CREATE block, so they are not on the sqlc path). Write via
	// the driver-aware *store.DB raw SQL (? anchors a typed column / value, PG-safe).
	if in.OpPrimitive != nil {
		if err := validateOpPrimitive(*in.OpPrimitive); err != nil {
			return store.Column{}, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE columns SET op_primitive = ? WHERE id = ?`, *in.OpPrimitive, id); err != nil {
			return store.Column{}, err
		}
	}
	if in.WhenToUse != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE columns SET when_to_use = ? WHERE id = ?`, toNullString(*in.WhenToUse), id); err != nil {
			return store.Column{}, err
		}
	}
	return col, nil
}

// GetColumnOpFields reads a single column's migrate-only op_primitive/when_to_use.
func (s *KanbanService) GetColumnOpFields(ctx context.Context, id int64) (ColumnOpFields, error) {
	var op string
	var wtu sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT op_primitive, when_to_use FROM columns WHERE id = ?`, id)
	if err := row.Scan(&op, &wtu); err != nil {
		return ColumnOpFields{}, err
	}
	out := ColumnOpFields{OpPrimitive: op}
	if wtu.Valid {
		v := wtu.String
		out.WhenToUse = &v
	}
	return out, nil
}

// ListColumnOpFields reads op_primitive/when_to_use for every column in a project
// in one query (the settings page is rarely hit; a single read is cheaper than a
// new sqlc query that would widen the generated Column struct).
func (s *KanbanService) ListColumnOpFields(ctx context.Context, projectID int64) (map[int64]ColumnOpFields, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, op_primitive, when_to_use FROM columns WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ColumnOpFields{}
	for rows.Next() {
		var id int64
		var op string
		var wtu sql.NullString
		if err := rows.Scan(&id, &op, &wtu); err != nil {
			return nil, err
		}
		f := ColumnOpFields{OpPrimitive: op}
		if wtu.Valid {
			v := wtu.String
			f.WhenToUse = &v
		}
		out[id] = f
	}
	return out, rows.Err()
}

// UpdateColumnPosition moves a column to a new position using insert-sort within a transaction.
// newPosition is a 0-based array index in the ordered column slice.
func (s *KanbanService) UpdateColumnPosition(ctx context.Context, projectID, columnID int64, newPosition int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := tx.Queries()

	columns, err := qtx.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list columns: %w", err)
	}

	// Find the column to move
	srcIdx := -1
	for i, col := range columns {
		if col.ID == columnID {
			srcIdx = i
			break
		}
	}
	if srcIdx == -1 {
		return sql.ErrNoRows
	}

	// Clamp newPosition to valid range
	maxPos := int64(len(columns) - 1)
	if newPosition < 0 {
		newPosition = 0
	}
	if newPosition > maxPos {
		newPosition = maxPos
	}

	// No-op if same position
	if int64(srcIdx) == newPosition {
		return nil
	}

	// Remove from old position, insert at new position (using explicit copy to avoid slice aliasing)
	moved := columns[srcIdx]
	remaining := make([]store.Column, 0, len(columns)-1)
	remaining = append(remaining, columns[:srcIdx]...)
	remaining = append(remaining, columns[srcIdx+1:]...)
	newIdx := int(newPosition)
	columns = make([]store.Column, 0, len(remaining)+1)
	columns = append(columns, remaining[:newIdx]...)
	columns = append(columns, moved)
	columns = append(columns, remaining[newIdx:]...)

	// Reindex and update only changed positions
	for i, col := range columns {
		if col.Position != int64(i) {
			if err := qtx.UpdateColumnPosition(ctx, store.UpdateColumnPositionParams{
				Position: int64(i),
				ID:       col.ID,
			}); err != nil {
				return fmt.Errorf("update column %d position: %w", col.ID, err)
			}
		}
	}

	return tx.Commit()
}

func (s *KanbanService) DeleteColumn(ctx context.Context, id int64) error {
	return s.q.DeleteColumn(ctx, id)
}

func (s *KanbanService) UpdateColumnLifecycleMapping(ctx context.Context, id int64, mapping string) (store.Column, error) {
	if err := validateLifecycleMapping(mapping); err != nil {
		return store.Column{}, err
	}
	return s.q.UpdateColumnLifecycleMapping(ctx, store.UpdateColumnLifecycleMappingParams{
		LifecycleMapping: mapping,
		ID:               id,
	})
}

// Issue operations

func (s *KanbanService) ListIssues(ctx context.Context, columnID int64) ([]IssueDetail, error) {
	issues, err := s.q.ListIssuesByColumn(ctx, columnID)
	if err != nil {
		return nil, err
	}
	return s.attachAssigneesAndLabels(ctx, issues)
}

func (s *KanbanService) ListIssuesByProject(ctx context.Context, projectID int64) ([]IssueDetail, error) {
	issues, err := s.q.ListIssuesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.attachAssigneesAndLabels(ctx, issues)
}

// ListIssuesByProjectFilteredParams holds optional filters for listing project issues.
type ListIssuesByProjectFilteredParams struct {
	ColumnID        int64
	LifecycleStatus string
	LabelIDs        []int64
	Search          string
}

// ListIssuesByProjectFiltered returns project issues matching the given filters.
func (s *KanbanService) ListIssuesByProjectFiltered(ctx context.Context, projectID int64, params ListIssuesByProjectFilteredParams) ([]IssueDetail, error) {
	allIssues, err := s.q.ListIssuesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}

	var filtered []store.Issue
	for _, iss := range allIssues {
		if params.ColumnID > 0 && iss.ColumnID != params.ColumnID {
			continue
		}
		if params.LifecycleStatus != "" && iss.LifecycleStatus != params.LifecycleStatus {
			continue
		}
		if len(params.LabelIDs) > 0 {
			ids, err := s.q.ListIssueLabelIDs(ctx, iss.ID)
			if err != nil {
				return nil, err
			}
			if !anyIntersect(ids, params.LabelIDs) {
				continue
			}
		}
		if params.Search != "" {
			search := strings.ToLower(params.Search)
			if !strings.Contains(strings.ToLower(iss.Title), search) &&
				!strings.Contains(strings.ToLower(iss.Description.String), search) {
				continue
			}
		}
		filtered = append(filtered, iss)
	}
	return s.attachAssigneesAndLabels(ctx, filtered)
}

// anyIntersect reports whether slices a and b share at least one element.
func anyIntersect(a, b []int64) bool {
	set := make(map[int64]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

func (s *KanbanService) GetIssue(ctx context.Context, id int64) (IssueDetail, error) {
	return s.loadIssueDetail(ctx, id)
}

// replaceAssignees clears any existing issue_assignees rows for issueID and
// inserts ids. Validates membership against the project's owner via
// validateAssignees. Caller-supplied transaction (tx) so the entire
// replacement is atomic with the surrounding mutation; tx.QueryRowContext is
// used for the project-owner lookup so a connection pool pinned to a single
// connection (tests) doesn't deadlock on the outer BeginTx.
func (s *KanbanService) replaceAssignees(ctx context.Context, tx *store.Tx, issueID int64, ids []int64, byUserID int64) error {
	_ = byUserID
	if len(ids) > 100 {
		return fmt.Errorf("%w: %d assignees", ErrTooMany, len(ids))
	}
	q := tx.Queries()
	if err := s.validateAssignees(ctx, tx, q, issueID, ids); err != nil {
		return err
	}
	if err := q.ClearIssueAssignees(ctx, issueID); err != nil {
		return err
	}
	for _, uid := range ids {
		if err := q.AddIssueAssignee(ctx, store.AddIssueAssigneeParams{IssueID: issueID, UserID: uid}); err != nil {
			return err
		}
	}
	return nil
}

// validateAssignees enforces that each user id is a member of the project's
// owning org (or the personal owner themself for user-owned projects).
// Rejections wrap ErrInvalidAssignee so handlers can errors.Is them without
// stringly-typed matching.
//
// tx carries the raw query interface for the project-owner lookup so the SQL
// runs inside the surrounding transaction (q is the matching sqlc *Queries
// bound to the same tx).
func (s *KanbanService) validateAssignees(ctx context.Context, tx *store.Tx, q *store.Queries, issueID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var ownerType string
	var ownerID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT p.owner_type, p.owner_id
		FROM projects p
		JOIN columns c ON c.project_id = p.id
		JOIN issues i ON i.column_id = c.id
		WHERE i.id = ?`, issueID,
	).Scan(&ownerType, &ownerID); err != nil {
		return fmt.Errorf("resolve project owner for issue %d: %w", issueID, err)
	}
	allowed := make(map[int64]struct{})
	switch ownerType {
	case "user":
		allowed[ownerID] = struct{}{}
	case "org":
		members, err := q.ListAssignableUsersForOrg(ctx, ownerID)
		if err != nil {
			return err
		}
		for _, m := range members {
			allowed[m.ID] = struct{}{}
		}
	default:
		return fmt.Errorf("%w: unknown owner_type %q", ErrInvalidAssignee, ownerType)
	}
	for _, uid := range ids {
		if _, ok := allowed[uid]; !ok {
			return fmt.Errorf("%w: user %d not a project member", ErrInvalidAssignee, uid)
		}
	}
	return nil
}

// replaceLabels clears any existing issue_labels rows for issueID and inserts
// ids. Validates per-project ownership via validateLabels. tx carries the
// transaction so the project-owner lookup runs inside it (keeps the operation
// atomic, and avoids a second-connection deadlock if the pool is pinned to 1).
func (s *KanbanService) replaceLabels(ctx context.Context, tx *store.Tx, issueID int64, ids []int64) error {
	if len(ids) > 100 {
		return fmt.Errorf("%w: %d labels", ErrTooMany, len(ids))
	}
	q := tx.Queries()
	if err := s.validateLabels(ctx, tx, q, issueID, ids); err != nil {
		return err
	}
	if err := q.ClearIssueLabels(ctx, issueID); err != nil {
		return err
	}
	for _, lid := range ids {
		if err := q.AddIssueLabel(ctx, store.AddIssueLabelParams{IssueID: issueID, LabelID: lid}); err != nil {
			return err
		}
	}
	return nil
}

// validateLabels enforces that every label id belongs to the same project as
// the issue. Rejections wrap ErrInvalidLabel so handlers can errors.Is them.
func (s *KanbanService) validateLabels(ctx context.Context, tx *store.Tx, q *store.Queries, issueID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var projectID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT c.project_id FROM columns c JOIN issues i ON i.column_id=c.id WHERE i.id=?`, issueID,
	).Scan(&projectID); err != nil {
		return err
	}
	for _, lid := range ids {
		l, err := q.GetLabel(ctx, lid)
		if err != nil {
			return fmt.Errorf("%w: id %d: %v", ErrInvalidLabel, lid, err)
		}
		if l.ProjectID != projectID {
			return fmt.Errorf("%w: id %d not in project %d", ErrInvalidLabel, lid, projectID)
		}
	}
	return nil
}

// loadIssueDetail returns the IssueDetail for a single issue with its
// assignees and labels populated via the per-issue queries.
func (s *KanbanService) loadIssueDetail(ctx context.Context, issueID int64) (IssueDetail, error) {
	// Three queries fan out concurrently. Previously they ran serially
	// (3 RTs × ~150-200ms = 600ms+ on PG); errgroup collapses them to a
	// single RT bounded by the slowest. The "issue" query is the
	// composite GetIssueDetail — single statement that also pulls
	// project_id (via JOIN columns) and checklist stats (via two
	// correlated subqueries) so the handler can drop its post-fetch
	// GetColumn + GetChecklistStats calls.
	var (
		row       store.GetIssueDetailRow
		assignees []store.ListIssueAssigneesRow
		labels    []store.Label
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		r, err := s.q.GetIssueDetail(gctx, issueID)
		if err != nil {
			return err
		}
		row = r
		return nil
	})
	g.Go(func() error {
		a, err := s.q.ListIssueAssignees(gctx, issueID)
		if err != nil {
			return err
		}
		assignees = a
		return nil
	})
	g.Go(func() error {
		l, err := s.q.ListLabelsByIssue(gctx, issueID)
		if err != nil {
			return err
		}
		labels = l
		return nil
	})
	if err := g.Wait(); err != nil {
		return IssueDetail{}, err
	}
	// External-tracker fields must ride along on the Issue literal — the
	// composite GetIssueDetail SELECTs i.*, so every column on issues is
	// scanned into `row`, but a missing field on this struct literal
	// silently zeroes it in the IssueDetail returned to the handler. That
	// makes `external_*` fields disappear from GET /issues/:id responses
	// (regression r0: 5 v1 external fields; regression r1.1: external_
	// comments_snapshot{,_at}). Keep this in lock-step with store.Issue.
	return IssueDetail{
		Issue: store.Issue{
			ID:                         row.ID,
			ColumnID:                   row.ColumnID,
			Title:                      row.Title,
			Description:                row.Description,
			Priority:                   row.Priority,
			Position:                   row.Position,
			LifecycleStatus:            row.LifecycleStatus,
			StartDate:                  row.StartDate,
			DueDate:                    row.DueDate,
			EstimateType:               row.EstimateType,
			Estimate:                   row.Estimate,
			ActualTime:                 row.ActualTime,
			ExternalSource:             row.ExternalSource,
			ExternalID:                 row.ExternalID,
			ExternalUrl:                row.ExternalUrl,
			ExternalSnapshotAt:         row.ExternalSnapshotAt,
			ExternalCommentsSnapshot:   row.ExternalCommentsSnapshot,
			ExternalCommentsSnapshotAt: row.ExternalCommentsSnapshotAt,
			// goal_condition was added after the external-* batch (see the
			// "regression r0/r1.1" warning comment above). DO NOT remove it
			// — without this line `IssueDetail.GoalCondition` silently
			// returns "" from GET /issues/:id even when the DB has a value,
			// which makes the SPA's IssueGoalConditionPanel render "未设置"
			// on every page open after a save. Regression caught 2026-05-15.
			GoalCondition: row.GoalCondition,
			// Executable Epic fields — same lock-step rule as the external_*
			// and goal_condition fields above: GetIssueDetail SELECTs i.*, so a
			// missing field here silently zeroes parent/type/wave/status in the
			// IssueDetail returned to the handler (Epic badges + child grouping
			// would vanish from GET /issues/:id).
			ParentIssueID: row.ParentIssueID,
			IssueType:     row.IssueType,
			ExecWave:      row.ExecWave,
			ExecStatus:    row.ExecStatus,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
			// Same lock-step rule as above: GetIssueDetail SELECTs i.*, so the
			// new created_by must be copied or IssueDetail.CreatedBy silently
			// zeroes on GET /issues/:id (工作空间创建人确定顺序 relies on it).
			CreatedBy: row.CreatedBy,
		},
		Assignees:          assignees,
		Labels:             labels,
		ProjectID:          row.ProjectID,
		ChecklistTotal:     row.ChecklistTotal,
		ChecklistCompleted: row.ChecklistCompleted,
	}, nil
}

// attachAssigneesAndLabels enriches a slice of issues with their assignees
// and labels in two batched IN-list queries (avoids N+1).
func (s *KanbanService) attachAssigneesAndLabels(ctx context.Context, issues []store.Issue) ([]IssueDetail, error) {
	if len(issues) == 0 {
		return []IssueDetail{}, nil
	}
	ids := make([]int64, len(issues))
	for i, x := range issues {
		ids[i] = x.ID
	}
	labelRows, err := s.q.ListLabelsByIssueIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	asnRows, err := s.q.ListIssueAssigneesByIssueIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	labelsBy := map[int64][]store.Label{}
	for _, l := range labelRows {
		labelsBy[l.IssueID] = append(labelsBy[l.IssueID], store.Label{
			ID:          l.ID,
			ProjectID:   l.ProjectID,
			Name:        l.Name,
			Color:       l.Color,
			Description: l.Description,
			CreatedAt:   l.CreatedAt,
			CreatedBy:   l.CreatedBy,
		})
	}
	assigneesBy := map[int64][]store.ListIssueAssigneesRow{}
	for _, a := range asnRows {
		assigneesBy[a.IssueID] = append(assigneesBy[a.IssueID],
			store.ListIssueAssigneesRow{ID: a.ID, Username: a.Username, DisplayName: a.DisplayName})
	}
	out := make([]IssueDetail, len(issues))
	for i, x := range issues {
		out[i] = IssueDetail{Issue: x, Assignees: assigneesBy[x.ID], Labels: labelsBy[x.ID]}
	}
	return out, nil
}

// BatchCreateIssuesTask represents a single task in a batch create request.
// The Executable Epic fields are optional: ParentIssueID attaches the new issue
// as a child of an Epic (must be in the same project), IssueType is 'task'
// (default) or 'epic', ExecWave is the child execution wave.
type BatchCreateIssuesTask struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Priority      int64    `json:"priority"`
	EstimateType  string   `json:"estimate_type"`
	Estimate      float64  `json:"estimate"`
	Checklist     []string `json:"checklist"`
	ParentIssueID *int64   `json:"parent_issue_id,omitempty"`
	IssueType     string   `json:"issue_type,omitempty"`
	ExecWave      int64    `json:"exec_wave,omitempty"`
}

// BatchCreateIssuesResult holds the result of a batch create operation.
type BatchCreateIssuesResult struct {
	Created int              `json:"created"`
	Issues  []BatchIssueInfo `json:"issues"`
}

// BatchIssueInfo is a minimal issue reference returned after batch creation.
type BatchIssueInfo struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// BatchCreateIssues creates multiple issues in the first column of a project.
// The entire batch is wrapped in a transaction for atomicity.
//
// byUserID is the creating user (0 = no actor, e.g. fully automated callers);
// it is stamped as each issue's created_by so workspaces derived from these
// issues can resolve their creator (工作空间创建人确定顺序).
func (s *KanbanService) BatchCreateIssues(ctx context.Context, projectID int64, tasks []BatchCreateIssuesTask, byUserID int64) (BatchCreateIssuesResult, error) {
	var result BatchCreateIssuesResult

	// Fan-out guardrail (spec 2026-06-06 section 16): cap how many issues one
	// batch_create_issues call may create. The orchestration agent should split
	// an over-cap batch into smaller calls.
	if max := s.effMaxBatch(ctx); max > 0 && len(tasks) > max {
		return result, fmt.Errorf("一次最多创建 %d 个任务(本次 %d 个)，请拆成多批分次创建", max, len(tasks))
	}

	// Find first column
	columns, err := s.q.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return result, fmt.Errorf("list columns: %w", err)
	}
	if len(columns) == 0 {
		return result, fmt.Errorf("project has no columns")
	}
	firstColumnID := columns[0].ID

	// Get max position
	issues, err := s.q.ListIssuesByColumn(ctx, firstColumnID)
	if err != nil {
		return result, fmt.Errorf("list issues: %w", err)
	}
	var maxPos int64
	for _, iss := range issues {
		if iss.Position > maxPos {
			maxPos = iss.Position
		}
	}

	// Executable Epic: validate issue_type + same-project parents up front,
	// before opening the write tx. The SQLite single-writer connection means a
	// read issued while the tx is open would deadlock, so all reads happen here.
	for i := range tasks {
		it := normalizeIssueType(tasks[i].IssueType)
		if it != "task" && it != "epic" {
			return result, fmt.Errorf("task %d: invalid issue_type %q (want task|epic)", i, it)
		}
		tasks[i].IssueType = it
		if tasks[i].ParentIssueID != nil {
			if err := s.ensureSameProject(ctx, firstColumnID, *tasks[i].ParentIssueID); err != nil {
				return result, fmt.Errorf("task %d: %w", i, err)
			}
			if err := s.ensureTwoLevelParent(ctx, 0, *tasks[i].ParentIssueID); err != nil {
				return result, fmt.Errorf("task %d: %w", i, err)
			}
		}
	}

	// Use transaction for atomicity
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	txq := tx.Queries()

	for i, task := range tasks {
		pos := maxPos + int64(i) + 1
		issue, err := txq.CreateIssue(ctx, store.CreateIssueParams{
			ColumnID:      firstColumnID,
			Title:         task.Title,
			Description:   toNullString(task.Description),
			Priority:      sql.NullInt64{Int64: task.Priority, Valid: true},
			Position:      pos,
			EstimateType:  toNullString(task.EstimateType),
			Estimate:      sql.NullFloat64{Float64: task.Estimate, Valid: true},
			ParentIssueID: toNullInt64(task.ParentIssueID),
			CreatedBy:     sql.NullInt64{Int64: byUserID, Valid: byUserID > 0},
		})
		if err != nil {
			return result, fmt.Errorf("create issue %d: %w", i, err)
		}

		// Apply Epic exec fields (keeping the parent set above) only when the
		// caller asked for a non-default type/wave — otherwise the DB defaults
		// 'task'/0/'idle' stand.
		if task.IssueType != "task" || task.ExecWave != 0 {
			if _, err := txq.SetIssueExecFields(ctx, store.SetIssueExecFieldsParams{
				ParentIssueID: toNullInt64(task.ParentIssueID),
				IssueType:     task.IssueType,
				ExecWave:      task.ExecWave,
				ExecStatus:    "idle",
				ID:            issue.ID,
			}); err != nil {
				return result, fmt.Errorf("set exec fields for issue %d: %w", i, err)
			}
		}

		for _, item := range task.Checklist {
			if _, err := txq.CreateChecklist(ctx, store.CreateChecklistParams{
				IssueID:   issue.ID,
				Title:     item,
				IssueID_2: issue.ID,
			}); err != nil {
				return result, fmt.Errorf("create checklist for issue %d: %w", i, err)
			}
		}

		result.Issues = append(result.Issues, BatchIssueInfo{ID: issue.ID, Title: issue.Title})
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit: %w", err)
	}

	// Auto-promote any parents that gained children to Epics (deduped).
	seenParents := map[int64]struct{}{}
	for _, task := range tasks {
		if task.ParentIssueID == nil {
			continue
		}
		if _, done := seenParents[*task.ParentIssueID]; done {
			continue
		}
		seenParents[*task.ParentIssueID] = struct{}{}
		if err := s.syncIssueType(ctx, *task.ParentIssueID); err != nil {
			slog.Warn("syncIssueType after batch create failed", "parent", *task.ParentIssueID, "error", err)
		}
	}

	// Log activity outside transaction
	for _, info := range result.Issues {
		s.activitySvc.LogCreated(ctx, info.ID, "")
	}

	result.Created = len(result.Issues)
	return result, nil
}

// CreateIssue creates an issue and replaces its assignees/labels in a single
// transaction, then returns the populated IssueDetail. Caps both arrays at
// 100 entries (enforced inside replaceAssignees/replaceLabels). Validates
// that assignee user ids belong to the project's owner and that label ids
// belong to the project — returns "invalid_assignee:"/"invalid_label:"
// prefixed errors, which roll back the entire transaction (issue insert
// included) so a half-created issue with rejected metadata never persists.
// CreateIssue creates an issue. The Executable Epic fields are optional:
// parentIssueID (nil = top-level; non-nil attaches the issue as a child of the
// given Epic and is rejected if the parent lives in a different project),
// issueType (” -> 'task'; 'epic' marks an Epic), and execWave (child execution
// wave). exec_status always starts 'idle' on create.
func (s *KanbanService) CreateIssue(ctx context.Context, columnID int64, title, description string,
	priority int64, position int64,
	startDate, dueDate, estimateType string, estimate float64,
	parentIssueID *int64, issueType string, execWave int64,
	assigneeUserIDs, labelIDs []int64, byUserID int64) (IssueDetail, error) {

	issueType = normalizeIssueType(issueType)

	// Executable Epic: a child must live in the same project as its parent, and
	// the hierarchy is capped at two levels (parent must be top-level).
	if parentIssueID != nil {
		if err := s.ensureSameProject(ctx, columnID, *parentIssueID); err != nil {
			return IssueDetail{}, err
		}
		if err := s.ensureTwoLevelParent(ctx, 0, *parentIssueID); err != nil {
			return IssueDetail{}, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssueDetail{}, err
	}
	defer tx.Rollback()
	q := tx.Queries()

	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID:      columnID,
		Title:         title,
		Description:   toNullString(description),
		Priority:      sql.NullInt64{Int64: priority, Valid: true},
		Position:      position,
		StartDate:     toNullString(startDate),
		DueDate:       toNullString(dueDate),
		EstimateType:  toNullString(estimateType),
		Estimate:      sql.NullFloat64{Float64: estimate, Valid: true},
		ParentIssueID: toNullInt64(parentIssueID),
		// Record the creator so workspaces derived from this issue can resolve
		// their creator (工作空间创建人确定顺序). byUserID==0 means no actor
		// (system/automation paths) and is stored as NULL.
		CreatedBy: sql.NullInt64{Int64: byUserID, Valid: byUserID > 0},
	})
	if err != nil {
		return IssueDetail{}, err
	}

	// issue_type/exec_wave default to 'task'/0 in the DB; only write them back
	// (keeping the parent set above) when the caller asked for something else.
	if issueType != "task" || execWave != 0 {
		if _, err := q.SetIssueExecFields(ctx, store.SetIssueExecFieldsParams{
			ParentIssueID: toNullInt64(parentIssueID),
			IssueType:     issueType,
			ExecWave:      execWave,
			ExecStatus:    "idle",
			ID:            issue.ID,
		}); err != nil {
			return IssueDetail{}, err
		}
	}

	if err := s.replaceAssignees(ctx, tx, issue.ID, assigneeUserIDs, byUserID); err != nil {
		return IssueDetail{}, err
	}
	if err := s.replaceLabels(ctx, tx, issue.ID, labelIDs); err != nil {
		return IssueDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssueDetail{}, err
	}
	// Auto-promote the parent to an Epic now that it has a child (issue_type is
	// derived from child presence, never chosen manually).
	if parentIssueID != nil {
		if err := s.syncIssueType(ctx, *parentIssueID); err != nil {
			slog.Warn("syncIssueType after create failed", "parent", *parentIssueID, "error", err)
		}
	}
	s.activitySvc.LogCreated(ctx, issue.ID, "")
	return s.loadIssueDetail(ctx, issue.ID)
}

// UpdateIssue updates only the scalar fields of an issue. Assignees and
// labels are managed separately via SetAssignees/SetLabels.
//
// goalCondition is optional (pointer): nil = no-op; non-nil = persist as the
// autohost LLM-judge stop condition. The empty string clears the condition.
// Length is capped at 4000 characters.
func (s *KanbanService) UpdateIssue(ctx context.Context, id int64, title, description string,
	priority int64, startDate, dueDate, estimateType string, estimate, actualTime float64,
	goalCondition *string) (IssueDetail, error) {

	old, err := s.q.GetIssue(ctx, id)
	if err != nil {
		return IssueDetail{}, err
	}

	if goalCondition != nil && len(*goalCondition) > 4000 {
		return IssueDetail{}, fmt.Errorf("goal_condition too long (max 4000)")
	}

	scalarUnchanged := old.Title == title &&
		nullStringVal(old.Description) == description &&
		nullInt64Val(old.Priority) == priority &&
		nullStringVal(old.StartDate) == startDate &&
		nullStringVal(old.DueDate) == dueDate &&
		nullStringVal(old.EstimateType) == estimateType &&
		nullFloat64Val(old.Estimate) == estimate &&
		nullFloat64Val(old.ActualTime) == actualTime
	goalUnchanged := goalCondition == nil || *goalCondition == old.GoalCondition

	// Skip DB write if nothing changed
	if scalarUnchanged && goalUnchanged {
		return s.loadIssueDetail(ctx, id)
	}

	if !scalarUnchanged {
		if _, err := s.q.UpdateIssue(ctx, store.UpdateIssueParams{
			ID:           id,
			Title:        title,
			Description:  toNullString(description),
			Priority:     sql.NullInt64{Int64: priority, Valid: true},
			StartDate:    toNullString(startDate),
			DueDate:      toNullString(dueDate),
			EstimateType: toNullString(estimateType),
			Estimate:     sql.NullFloat64{Float64: estimate, Valid: true},
			ActualTime:   sql.NullFloat64{Float64: actualTime, Valid: true},
		}); err != nil {
			return IssueDetail{}, err
		}
	}

	if !goalUnchanged {
		if err := s.q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{
			GoalCondition: *goalCondition,
			ID:            id,
		}); err != nil {
			return IssueDetail{}, err
		}
	}

	author := ""
	s.activitySvc.LogFieldChange(ctx, id, "title", old.Title, title, author)
	s.activitySvc.LogFieldChange(ctx, id, "description", nullStringVal(old.Description), description, author)
	s.activitySvc.LogFieldChange(ctx, id, "priority", fmt.Sprintf("%d", nullInt64Val(old.Priority)), fmt.Sprintf("%d", priority), author)
	s.activitySvc.LogFieldChange(ctx, id, "start_date", nullStringVal(old.StartDate), startDate, author)
	s.activitySvc.LogFieldChange(ctx, id, "due_date", nullStringVal(old.DueDate), dueDate, author)
	s.activitySvc.LogFieldChange(ctx, id, "estimate_type", nullStringVal(old.EstimateType), estimateType, author)
	s.activitySvc.LogFieldChange(ctx, id, "estimate", fmt.Sprintf("%.1f", nullFloat64Val(old.Estimate)), fmt.Sprintf("%.1f", estimate), author)
	s.activitySvc.LogFieldChange(ctx, id, "actual_time", fmt.Sprintf("%.1f", nullFloat64Val(old.ActualTime)), fmt.Sprintf("%.1f", actualTime), author)
	if !goalUnchanged {
		s.activitySvc.LogFieldChange(ctx, id, "goal_condition", old.GoalCondition, *goalCondition, author)
	}

	return s.loadIssueDetail(ctx, id)
}

// SetAssignees replaces the entire assignees set for an issue inside a
// transaction. Reads back the populated IssueDetail after commit.
func (s *KanbanService) SetAssignees(ctx context.Context, issueID int64, ids []int64, byUserID int64) (IssueDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssueDetail{}, err
	}
	defer tx.Rollback()
	if err := s.replaceAssignees(ctx, tx, issueID, ids, byUserID); err != nil {
		return IssueDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssueDetail{}, err
	}
	return s.loadIssueDetail(ctx, issueID)
}

// AssignableUser represents a user who is assignable to issues in a project.
// For org-owned projects, Role is set from org_members.role; for user-owned
// projects (single owner), Role is nil.
type AssignableUser struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Role        *string `json:"role,omitempty"`
}

// ListAssignableUsersForProject returns the set of users who may be assigned
// to issues in projectID. For user-owned projects this is just the owner; for
// org-owned projects this is every member of the owning org with their role
// attached.
func (s *KanbanService) ListAssignableUsersForProject(ctx context.Context, projectID int64) ([]AssignableUser, error) {
	var ownerType string
	var ownerID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM projects WHERE id = ?`, projectID,
	).Scan(&ownerType, &ownerID); err != nil {
		return nil, err
	}
	switch ownerType {
	case "user":
		u, err := s.q.GetUserBasic(ctx, ownerID)
		if err != nil {
			return nil, err
		}
		return []AssignableUser{{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}}, nil
	case "org":
		members, err := s.q.ListAssignableUsersForOrg(ctx, ownerID)
		if err != nil {
			return nil, err
		}
		out := make([]AssignableUser, len(members))
		for i, m := range members {
			role := m.Role
			out[i] = AssignableUser{ID: m.ID, Username: m.Username, DisplayName: m.DisplayName, Role: &role}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown owner_type %q", ownerType)
	}
}

// SetLabels replaces the entire labels set for an issue inside a transaction.
// Reads back the populated IssueDetail after commit.
func (s *KanbanService) SetLabels(ctx context.Context, issueID int64, ids []int64) (IssueDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssueDetail{}, err
	}
	defer tx.Rollback()
	if err := s.replaceLabels(ctx, tx, issueID, ids); err != nil {
		return IssueDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssueDetail{}, err
	}
	return s.loadIssueDetail(ctx, issueID)
}

// MoveIssue moves an issue to a different column and position.
// When a RunService is wired (via WithRunService), delegates to
// RunService.MoveIssueRunAware which enforces the §4.2 decision matrix
// (Run-aware path: gate checking, agent lifecycle, event bus).
// When RunService is not wired, falls back to moveIssueLegacy (Phase 0 path).
//
// callerUserID is forwarded into the Run path for actor attribution; 0
// means "no actor" (legacy MCP / system-driven moves).
func (s *KanbanService) MoveIssue(ctx context.Context, issueID int64, targetColumnID int64, targetPosition int64, callerUserID int64) error {
	return s.MoveIssueWithSource(ctx, issueID, targetColumnID, targetPosition, callerUserID, MoveSourceDrag)
}

// MoveIssueWithSource is MoveIssue with an explicit initiator. The HTTP drag /
// bulk-move paths use the MoveSourceDrag default (a human action); the AI-native
// orchestration (advance_issue / abandon_issue) passes an MCP source so the §18
// last-writer rule does NOT treat the AI's own card move as a human override —
// otherwise advancing a card would interrupt the very agent doing the work.
func (s *KanbanService) MoveIssueWithSource(ctx context.Context, issueID int64, targetColumnID int64, targetPosition int64, callerUserID int64, source MoveSource) error {
	if s.runSvc != nil {
		_, err := s.runSvc.MoveIssueRunAware(ctx, MoveIssueInput{
			IssueID:        issueID,
			TargetColumnID: targetColumnID,
			TargetPosition: targetPosition,
			Source:         source,
			ActorUserID:    callerUserID,
		})
		return err
	}
	return s.moveIssueLegacy(ctx, issueID, targetColumnID, targetPosition, callerUserID)
}

// applyLifecycleReverseSync implements "Direction 2": when an Issue is dragged
// to a column with a lifecycle_mapping, the Issue's lifecycle_status is synced
// to the first value in the mapping (if not already covered). Both moveIssueLegacy
// and MoveIssueRunAware call this helper so the behaviour is identical on both paths.
//
// q must be the queries handle bound to the current transaction (or s.q for the
// legacy non-tx path). activitySvc may be nil (RunService path); logging is skipped.
func applyLifecycleReverseSync(
	ctx context.Context,
	q *store.Queries,
	activitySvc *IssueActivityService,
	issue store.Issue,
	targetColumnID int64,
) error {
	targetCol, err := q.GetColumn(ctx, targetColumnID)
	if err != nil || targetCol.LifecycleMapping == "" {
		return nil
	}
	mappings := strings.Split(targetCol.LifecycleMapping, ",")
	for i := range mappings {
		mappings[i] = strings.TrimSpace(mappings[i])
	}
	for _, m := range mappings {
		if m == issue.LifecycleStatus {
			return nil // already covered by mapping; keep current status
		}
	}
	newStatus := mappings[0]
	if newStatus == "" || !validLifecycleStatuses[newStatus] {
		return nil
	}
	if _, err := q.UpdateIssueLifecycle(ctx, store.UpdateIssueLifecycleParams{
		LifecycleStatus: newStatus, ID: issue.ID,
	}); err != nil {
		return fmt.Errorf("reverse sync lifecycle: %w", err)
	}
	if activitySvc != nil {
		activitySvc.LogLifecycleChange(ctx, issue.ID, issue.LifecycleStatus, newStatus, "")
	}
	return nil
}

// moveIssueLegacy is the previous body of MoveIssue, preserved verbatim
// for the no-RunService case (Phase 0 tests that don't construct a RunService
// pass nil to NewKanbanService, which goes through this path).
//
// Was previously wrapped in a tx to atomicize column update + outbox enqueue
// (M3.3 writeback design); the tx is preserved post-writeback-removal because
// applyLifecycleReverseSync must serialise against concurrent moves — the tx's
// BEGIN IMMEDIATE write lock (SQLite) / row locks (PG) provide that regardless
// of pool size. callerUserID is kept on the signature for actor
// attribution in activity logs but is no longer used for outbox routing.
func (s *KanbanService) moveIssueLegacy(ctx context.Context, issueID int64, targetColumnID int64, targetPosition int64, callerUserID int64) error {
	if s.db == nil {
		return s.moveIssueLegacyNoTx(ctx, issueID, targetColumnID, targetPosition, callerUserID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	q := tx.Queries()

	issue, err := q.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if err := q.MoveIssue(ctx, store.MoveIssueParams{
		ColumnID: targetColumnID, Position: targetPosition, ID: issueID,
	}); err != nil {
		return err
	}
	if err := applyLifecycleReverseSync(ctx, q, nil, issue, targetColumnID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Post-commit: activity log + kanban broadcast (best-effort, must
	// not block on tx).
	if issue.ColumnID != targetColumnID {
		oldColName, newColName := "", ""
		if col, err := s.q.GetColumn(ctx, issue.ColumnID); err == nil {
			oldColName = col.Name
		}
		if col, err := s.q.GetColumn(ctx, targetColumnID); err == nil {
			newColName = col.Name
		}
		if s.activitySvc != nil {
			s.activitySvc.LogMoved(ctx, issueID, oldColName, newColName, "")
		}
	}
	if s.notifyHub != nil {
		targetCol, _ := s.q.GetColumn(ctx, targetColumnID)
		s.notifyHub.Broadcast(notify.Notification{
			Topic:  notify.TopicKanban,
			Action: "issue_moved",
			ID:     targetColumnID,
			Extra:  map[string]int64{"projectId": targetCol.ProjectID, "issueId": issueID},
		})
	}
	return nil
}

// moveIssueLegacyNoTx is the historical non-tx body, kept for fixtures
// that build a KanbanService with a nil *sql.DB (so store.Wrap returns
// nil). Will not enqueue writeback rows — those require a real tx.
func (s *KanbanService) moveIssueLegacyNoTx(ctx context.Context, issueID int64, targetColumnID int64, targetPosition int64, _ int64) error {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	if err := s.q.MoveIssue(ctx, store.MoveIssueParams{
		ColumnID: targetColumnID, Position: targetPosition, ID: issueID,
	}); err != nil {
		return err
	}
	if issue.ColumnID != targetColumnID {
		oldColName, newColName := "", ""
		if col, err := s.q.GetColumn(ctx, issue.ColumnID); err == nil {
			oldColName = col.Name
		}
		if col, err := s.q.GetColumn(ctx, targetColumnID); err == nil {
			newColName = col.Name
		}
		if s.activitySvc != nil {
			s.activitySvc.LogMoved(ctx, issueID, oldColName, newColName, "")
		}
	}
	if err := applyLifecycleReverseSync(ctx, s.q, s.activitySvc, issue, targetColumnID); err != nil {
		return err
	}
	if s.notifyHub != nil {
		targetCol, _ := s.q.GetColumn(ctx, targetColumnID)
		s.notifyHub.Broadcast(notify.Notification{
			Topic:  notify.TopicKanban,
			Action: "issue_moved",
			ID:     targetColumnID,
			Extra:  map[string]int64{"projectId": targetCol.ProjectID, "issueId": issueID},
		})
	}
	return nil
}

func (s *KanbanService) DeleteIssue(ctx context.Context, id int64) error {
	// Capture the parent before deletion so we can re-derive its type afterwards
	// (it may have just lost its last child and should revert from Epic to task).
	var parentID int64
	if iss, err := s.q.GetIssue(ctx, id); err == nil && iss.ParentIssueID.Valid {
		parentID = iss.ParentIssueID.Int64
	}
	if err := s.q.DeleteIssue(ctx, id); err != nil {
		return err
	}
	if parentID != 0 {
		if err := s.syncIssueType(ctx, parentID); err != nil {
			slog.Warn("syncIssueType after delete failed", "parent", parentID, "error", err)
		}
	}
	return nil
}

// UpdateIssueLifecycle updates the lifecycle status of an issue
func (s *KanbanService) UpdateIssueLifecycle(ctx context.Context, id int64, status string) (store.Issue, error) {
	if !validLifecycleStatuses[status] {
		return store.Issue{}, errors.New("invalid lifecycle status")
	}

	old, err := s.q.GetIssue(ctx, id)
	if err != nil {
		return store.Issue{}, err
	}

	issue, err := s.q.UpdateIssueLifecycle(ctx, store.UpdateIssueLifecycleParams{
		ID: id, LifecycleStatus: status,
	})
	if err != nil {
		return issue, err
	}

	// Direction 1: auto-move to column mapped to this lifecycle
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err == nil {
		columns, err := s.q.ListColumnsByProject(ctx, col.ProjectID)
		if err == nil {
			for _, c := range columns {
				if c.ID == issue.ColumnID || c.LifecycleMapping == "" {
					continue
				}
				for _, m := range strings.Split(c.LifecycleMapping, ",") {
					if strings.TrimSpace(m) == status {
						targetIssues, _ := s.q.ListIssuesByColumn(ctx, c.ID)
						appendPos := int64(len(targetIssues))
						s.q.MoveIssue(ctx, store.MoveIssueParams{ColumnID: c.ID, Position: appendPos, ID: issue.ID})
						if s.eventBus != nil {
							kanbanEvt, _ := json.Marshal(event.KanbanUpdateEvent{
								ProjectID: col.ProjectID,
								IssueID:   issue.ID,
								Action:    "moved",
							})
							s.eventBus.Publish(event.OutputEvent{
								Type:    event.EventKanbanUpdate,
								Content: string(kanbanEvt),
								Role:    "system",
								Ts:      time.Now().UnixMilli(),
							})
						}
						goto moved
					}
				}
			}
		}
	}
moved:
	s.activitySvc.LogLifecycleChange(ctx, id, old.LifecycleStatus, status, "")

	// Broadcast kanban lifecycle change notification
	if s.notifyHub != nil {
		col, _ := s.q.GetColumn(ctx, issue.ColumnID)
		s.notifyHub.Broadcast(notify.Notification{
			Topic:  notify.TopicKanban,
			Action: "lifecycle_changed",
			ID:     col.ProjectID,
			Extra:  map[string]int64{"projectId": col.ProjectID, "issueId": id},
		})
	}
	return issue, nil
}

func nullStringVal(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func nullInt64Val(ni sql.NullInt64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return 0
}

// toNullInt64 maps a *int64 to sql.NullInt64 (nil -> NULL). Treats a non-nil
// pointer to 0 as a real value so callers can distinguish "unset" (nil) from
// an explicit 0.
func toNullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{Valid: false}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// normalizeIssueType defaults an empty type to 'task'. The DB CHECK rejects ”
// on fresh schemas, so this guards every write path.
func normalizeIssueType(t string) string {
	if t == "" {
		return "task"
	}
	return t
}

// normalizeExecStatus defaults an empty exec_status to 'idle'.
func normalizeExecStatus(s string) string {
	if s == "" {
		return "idle"
	}
	return s
}

// validExecStatus reports whether s is one of the Epic execution lifecycle
// states (mirrors the CHECK in schema.sql / schema_postgres.sql).
func validExecStatus(s string) bool {
	switch s {
	case "idle", "running", "done", "failed", "paused", "reviewing":
		return true
	}
	return false
}

func nullFloat64Val(nf sql.NullFloat64) float64 {
	if nf.Valid {
		return nf.Float64
	}
	return 0
}

// SetIssueGoalCondition writes (or clears) the autohost completion condition
// on an issue without touching its other fields. The condition is injected into
// the autohost watchdog's continue prompt so the agent self-judges completion
// and emits the [AUTOHOST_DONE] sentinel — see docs CLAUDE.md "Autohost
// sentinel-based completion". Used by the conversational assistant quick-create
// flow (#388).
func (s *KanbanService) SetIssueGoalCondition(ctx context.Context, issueID int64, condition string) error {
	return s.q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{
		GoalCondition: condition,
		ID:            issueID,
	})
}

// CreateProjectWithDefaults creates a project with default columns
func (s *KanbanService) CreateProjectWithDefaults(ctx context.Context, name, description, ownerType string, ownerID int64) (store.Project, error) {
	// Check if this owner already has a project with the same name. Names are
	// unique per owner (idx_projects_owner_name_unique), not globally, so the
	// lookup must be owner-scoped — otherwise another owner's project would
	// wrongly block this one.
	existing, err := s.q.GetProjectByOwnerAndName(ctx, store.GetProjectByOwnerAndNameParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Name:      name,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No project with this name exists, proceed with creation
		} else {
			return store.Project{}, err
		}
	} else if existing.Name == name {
		return store.Project{}, fmt.Errorf("%w: project name %q already exists", ErrProjectNameExists, name)
	}

	// Create the project
	project, err := s.q.CreateProject(ctx, store.CreateProjectParams{
		Name:        name,
		Description: toNullString(description),
		OwnerType:   ownerType,
		OwnerID:     ownerID,
	})
	if err != nil {
		// The per-owner DB unique index (idx_projects_owner_name_unique) is the
		// real source of truth; the pre-check above is advisory and a concurrent
		// create can slip past it. Map the unique violation to the same typed
		// error so the race loser gets a clean "name taken", not a 500.
		if isUniqueViolation(err) {
			return store.Project{}, fmt.Errorf("%w: project name %q already exists", ErrProjectNameExists, name)
		}
		return project, err
	}

	// Create default columns (\u00a79 of the AI-native board execution design,
	// phase 1b; extended 2026-06-07 with a human-review lane): five op-primitive
	// columns \u2014 \u5f85\u529e(none) / \u5b9e\u73b0(instruct) / AI \u5ba1\u67e5(instruct) / \u4eba\u5de5\u5ba1\u67e5(none) /
	// \u5b8c\u6210(complete) \u2014 replacing the legacy 6 lifecycle columns.
	// Gate-spec bindings (\u5b9e\u73b0\u2192linter, AI \u5ba1\u67e5\u2192ai-judge\u00b7code-review, \u5b8c\u6210\u2192a
	// severity=error floor) are seeded later, alongside the column-native gate
	// dispatch that actually consumes them (see DefaultSpecs build-test-pass note);
	// this phase only seeds the columns + their primitives, op_instructions, and
	// routing hints.
	//
	// \u4eba\u5de5\u5ba1\u67e5 is a `none` parking lane (\u65e0\u6307\u4ee4): entering it never triggers/dispatches
	// the agent \u2014 a human (or a human drag) parks issues needing human judgment,
	// stakeholder approval, or a trade-off decision here. Because it carries a
	// when_to_use hint it DOES appear in the AI board menu (a none lane is included
	// once it has a when_to_use), so the agent can escalate into it via advance_issue;
	// only when_to_use-less none lanes like \u5f85\u529e stay out of the menu.
	//
	// lifecycle_mapping is kept consistent with op_primitive (per the \u00a717
	// lifecycle\u2192primitive backfill: created\u2192none, implement\u2192instruct,
	// completed\u2192complete) so the legacy lifecycle-group sidebar and any residual
	// lifecycle_mapping consumers keep working for newly seeded projects. \u4eba\u5de5\u5ba1\u67e5
	// maps to "" deliberately: it is a new op-primitive lane, not a legacy lifecycle
	// stage, so it neither auto-moves issues via lifecycle reverse-sync nor appears
	// as a lifecycle sidebar group.
	defaultColumns := []struct {
		name        string
		position    int64
		lifecycle   string
		opPrimitive string
		instruction string // op_instruction (stored in phase_prompt); "" \u2192 NULL
		whenToUse   string // AI routing hint; "" \u2192 NULL
	}{
		{"\u5f85\u529e", 0, "created", "none", "", ""},
		{"\u5b9e\u73b0", 1, "implement", "instruct", "\u5b9e\u73b0\u672c issue \u7684\u4efb\u52a1\u5206\u6790\u3001\u9700\u6c42\u6539\u52a8\u6216\u95ee\u9898\u4fee\u590d", "\u9700\u8981\u5f00\u59cb\u3001\u5b9e\u73b0\u3001\u89e3\u51b3\u4efb\u52a1\u65f6"},
		{"AI \u5ba1\u67e5", 2, "implement-review", "instruct", "\u5bf9\u672c issue \u7684\u4ea7\u51fa\u505a\u4e25\u683c\u5ba1\u67e5\uff0c\u5217\u51fa\u95ee\u9898\u5e76\u5c31\u5730\u4fee\u590d", "\u4ea7\u51fa\u6709\u590d\u6742\u5ea6\u3001\u503c\u5f97\u4e25\u683c\u5ba1\u67e5\u65f6"},
		{"\u4eba\u5de5\u5ba1\u67e5", 3, "", "none", "", "\u9700\u8981\u4eba\u5de5\u5224\u65ad\u3001\u5229\u76ca\u76f8\u5173\u65b9\u6279\u51c6\u6216\u9700\u8981\u6743\u8861\u591a\u4e2a\u65b9\u6848\u7684\u95ee\u9898"},
		{"\u5b8c\u6210", 4, "completed", "complete", "", ""},
	}

	for _, col := range defaultColumns {
		created, err := s.q.CreateColumn(ctx, store.CreateColumnParams{
			ProjectID:        project.ID,
			Name:             col.name,
			Position:         col.position,
			LifecycleMapping: col.lifecycle,
		})
		if err != nil {
			return project, err
		}
		// op_primitive / when_to_use live only as migrate.go ALTERs (deliberately
		// outside the schema CREATE block, so SELECT*/RETURNING* don't widen on the
		// next sqlc generate); they are not on the sqlc CreateColumn path. Set them
		// \u2014 together with op_instruction (phase_prompt) \u2014 via raw SQL on the
		// driver-aware *store.DB (rewrites ?\u2192$N for Postgres).
		if _, err := s.db.ExecContext(ctx,
			`UPDATE columns SET op_primitive = ?, when_to_use = ?, phase_prompt = ? WHERE id = ?`,
			col.opPrimitive, toNullString(col.whenToUse), toNullString(col.instruction), created.ID,
		); err != nil {
			return project, err
		}
	}

	// Org-owned projects emit a "project.created" entry in org_audit_log.
	// The API handler calls THIS method (not ProjectService.Create), so the
	// audit hook lives here — duplicating it on ProjectService.Create would
	// be dead code given current routing.
	appendResourceAudit(ctx, s.db, nil, ownerType, ownerID, 0, "project.created", "project", project.ID,
		resourceAuditPayload(name))

	return project, nil
}

// ColumnSeed describes one kanban column to create when seeding a project from
// a blueprint/template. Instruction is the op_instruction stored in the
// phase_prompt column; an empty Instruction / WhenToUse is persisted as NULL.
type ColumnSeed struct {
	Name        string
	Position    int64
	Lifecycle   string
	OpPrimitive string
	Instruction string
	WhenToUse   string
}

// defaultColumnSeeds returns the canonical five op-primitive lanes that
// CreateProjectWithDefaults hardcodes — 待办(none) / 实现(instruct) /
// AI 审查(instruct) / 人工审查(none) / 完成(complete). Exposed as seeds so the
// builtin "standard-dev" project blueprint can mirror the same default board.
func defaultColumnSeeds() []ColumnSeed {
	return []ColumnSeed{
		{Name: "待办", Position: 0, Lifecycle: "created", OpPrimitive: "none"},
		{Name: "实现", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "实现本 issue 的任务分析、需求改动或问题修复", WhenToUse: "需要开始、实现、解决任务时"},
		{Name: "AI 审查", Position: 2, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "对本 issue 的产出做严格审查，列出问题并就地修复", WhenToUse: "产出有复杂度、值得严格审查时"},
		{Name: "人工审查", Position: 3, Lifecycle: "", OpPrimitive: "none", WhenToUse: "需要人工判断、利益相关方批准或需要权衡多个方案的问题"},
		{Name: "完成", Position: 4, Lifecycle: "completed", OpPrimitive: "complete"},
	}
}

// CreateProjectWithColumns creates a project and seeds it with the given
// columns (in position order). It mirrors CreateProjectWithDefaults but takes an
// explicit column list, used when applying a project blueprint/template. If
// seeds is empty it returns an error rather than producing a column-less project
// — callers wanting the default five lanes should use CreateProjectWithDefaults.
func (s *KanbanService) CreateProjectWithColumns(ctx context.Context, name, description, ownerType string, ownerID int64, seeds []ColumnSeed) (store.Project, error) {
	if len(seeds) == 0 {
		return store.Project{}, fmt.Errorf("CreateProjectWithColumns: empty column seeds")
	}

	// Owner-scoped duplicate check: names are unique per owner, not globally.
	existing, err := s.q.GetProjectByOwnerAndName(ctx, store.GetProjectByOwnerAndNameParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Name:      name,
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return store.Project{}, err
		}
	} else if existing.Name == name {
		return store.Project{}, fmt.Errorf("%w: project name %q already exists", ErrProjectNameExists, name)
	}

	project, err := s.q.CreateProject(ctx, store.CreateProjectParams{
		Name:        name,
		Description: toNullString(description),
		OwnerType:   ownerType,
		OwnerID:     ownerID,
	})
	if err != nil {
		// See CreateProjectWithDefaults: the per-owner unique index is
		// authoritative; translate a concurrent-insert violation to the typed
		// error so the race loser gets "name taken" rather than a 500.
		if isUniqueViolation(err) {
			return store.Project{}, fmt.Errorf("%w: project name %q already exists", ErrProjectNameExists, name)
		}
		return project, err
	}

	for _, col := range seeds {
		created, err := s.q.CreateColumn(ctx, store.CreateColumnParams{
			ProjectID:        project.ID,
			Name:             col.Name,
			Position:         col.Position,
			LifecycleMapping: col.Lifecycle,
		})
		if err != nil {
			return project, err
		}
		// op_primitive / when_to_use are migrate.go ALTERs (not on the sqlc
		// CreateColumn path); set them + op_instruction (phase_prompt) via raw SQL
		// on the driver-aware *store.DB, same as CreateProjectWithDefaults.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE columns SET op_primitive = ?, when_to_use = ?, phase_prompt = ? WHERE id = ?`,
			col.OpPrimitive, toNullString(col.WhenToUse), toNullString(col.Instruction), created.ID,
		); err != nil {
			return project, err
		}
	}

	appendResourceAudit(ctx, s.db, nil, ownerType, ownerID, 0, "project.created", "project", project.ID,
		resourceAuditPayload(name))

	return project, nil
}

// ─── Batch issue mutations (shared by REST + MCP batch endpoints) ──────────────

// SkippedItem records one issue a batch op could not apply to, with a machine
// reason ("forbidden" | "not_found" | "cross_project").
type SkippedItem struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// BatchResult is the uniform return for every batch issue mutation. Succeeded
// and Skipped together cover every input id; the handler appends authz-level
// skipped items before serialising.
type BatchResult struct {
	Succeeded []int64       `json:"succeeded"`
	Skipped   []SkippedItem `json:"skipped"`
}

// BatchMoveIssues moves each issue to the END of targetColumnID, preserving
// input order. Issues whose current project differs from the target column's
// project are skipped with reason "cross_project". Authz is the handler's
// responsibility; here we only enforce project consistency. Each move runs
// through MoveIssue (which honours the Run-aware path when wired), so we
// cannot wrap the whole loop in a single tx without re-implementing that
// logic — we accept per-row commits, matching how the single-issue endpoint
// already behaves.
func (s *KanbanService) BatchMoveIssues(ctx context.Context, issueIDs []int64, targetColumnID, callerUserID int64) (BatchResult, error) {
	res := BatchResult{Succeeded: []int64{}, Skipped: []SkippedItem{}}
	if len(issueIDs) == 0 {
		return res, nil
	}
	targetCol, err := s.GetColumn(ctx, targetColumnID)
	if err != nil {
		return res, fmt.Errorf("get target column %d: %w", targetColumnID, err)
	}
	// Snapshot target column length once; each successful move appends past
	// the current end so relative order of the selection is preserved.
	endIssues, err := s.q.ListIssuesByColumn(ctx, targetColumnID)
	if err != nil {
		return res, err
	}
	nextPos := int64(len(endIssues))
	for _, id := range issueIDs {
		iss, err := s.q.GetIssue(ctx, id)
		if err != nil {
			res.Skipped = append(res.Skipped, SkippedItem{ID: id, Reason: "not_found"})
			continue
		}
		// Issue already in target column: append-to-end equals current position
		// after dedup; skip the move call rather than reorder noise.
		if iss.ColumnID == targetColumnID {
			res.Succeeded = append(res.Succeeded, id)
			continue
		}
		curCol, err := s.q.GetColumn(ctx, iss.ColumnID)
		if err != nil {
			res.Skipped = append(res.Skipped, SkippedItem{ID: id, Reason: "not_found"})
			continue
		}
		if curCol.ProjectID != targetCol.ProjectID {
			res.Skipped = append(res.Skipped, SkippedItem{ID: id, Reason: "cross_project"})
			continue
		}
		if err := s.MoveIssue(ctx, id, targetColumnID, nextPos, callerUserID); err != nil {
			res.Skipped = append(res.Skipped, SkippedItem{ID: id, Reason: "not_found"})
			continue
		}
		nextPos++
		res.Succeeded = append(res.Succeeded, id)
	}
	return res, nil
}

// BatchUpdatePriority sets priority (0..3) on each issue, in a single tx.
// Per-issue failures (deleted between snapshot and write) are recorded as
// skipped/not_found and do NOT roll back the rest — matches the
// "best-effort + skip-report" contract of the other batch ops.
//
// SECURITY (IDOR contract): authorization is the caller's responsibility.
// issueIDs MUST be pre-filtered to ids the caller may modify (the kanban
// handler does this via authorizeIssues + Authz.CanAccessIssue before
// calling here). This mirrors how BatchCreateIssues / DeleteIssue / single-row
// UpdateIssue trust handler-side authz today — pushing the gate into the
// service would duplicate work and require the service to hold an *Authz
// pointer that none of its sibling methods need. Direct callers (tests,
// future internal jobs) MUST replicate the per-id authz check before reaching
// this method.
func (s *KanbanService) BatchUpdatePriority(ctx context.Context, issueIDs []int64, priority int64) (BatchResult, error) {
	res := BatchResult{Succeeded: []int64{}, Skipped: []SkippedItem{}}
	if len(issueIDs) == 0 {
		return res, nil
	}
	if priority < 0 || priority > 3 {
		return res, fmt.Errorf("priority must be between 0 and 3")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	q := tx.Queries()
	for _, id := range issueIDs {
		if err := q.UpdateIssuePriority(ctx, store.UpdateIssuePriorityParams{
			Priority: sql.NullInt64{Int64: priority, Valid: true},
			ID:       id,
		}); err != nil {
			res.Skipped = append(res.Skipped, SkippedItem{ID: id, Reason: "not_found"})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	return res, nil
}

// BatchDeleteIssues hard-deletes each issue. Reuses DeleteIssue for parity with
// the single-issue endpoint. No outer tx: wrapping a thousand deletes in one tx
// would balloon write latency for no atomicity benefit (partial deletion is
// acceptable here).
//
// SECURITY (IDOR contract): see BatchUpdatePriority. Caller MUST pre-authorize
// every id via Authz.CanAccessIssue. The kanban handler does this in
// authorizeIssues before calling.
func (s *KanbanService) BatchDeleteIssues(ctx context.Context, issueIDs []int64) (BatchResult, error) {
	res := BatchResult{Succeeded: []int64{}, Skipped: []SkippedItem{}}
	for _, id := range issueIDs {
		if err := s.DeleteIssue(ctx, id); err != nil {
			res.Skipped = append(res.Skipped, SkippedItem{ID: id, Reason: "not_found"})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	return res, nil
}
