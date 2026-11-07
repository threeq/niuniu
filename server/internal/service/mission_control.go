package service

// Mission control metrics (design §6.6 / §6.7.8): two independent panels,
// Intake (任务来源) and Completion (自动完成), each backed by the tables that
// capability already writes. The two panels are deliberately not folded into a
// single "autonomy score": the intake numbers answer "did we pick up the right
// work", the completion numbers answer "did the work land".
//
// Every metric is a MissionControlMetric so the SPA can render one generic tile.
// A nil Value means "no data" and must render as "—", never 0 — a rate over an
// empty denominator is unknown, not zero.
//
// Scope: the caller's accessible projects (personal + joined orgs), optionally
// narrowed by the same ?owner=user:<id>|org:<id> filter the overview uses.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Windows and warning thresholds. Thresholds only mark a tile "warning" (amber +
// text in the SPA); they never hide the underlying number.
const (
	missionRateWindow       = 30 * 24 * time.Hour
	missionMisfireWindow    = 24 * time.Hour
	missionDuplicateWindow  = 60 * time.Minute
	missionFakeReopenWindow = 7 * 24 * time.Hour
	missionPerSourceDays    = 7

	missionMisfireWarnPct   = 10.0
	missionDuplicateWarnPct = 10.0
	missionFakeWarnPct      = 10.0
	missionStuckWarnPct     = 20.0
	missionLatencyWarnMin   = 60.0
	missionApprovalWarnPct  = 80.0 // below this is abnormal once calibrated
	missionApprovalMinN     = 10   // do not warn on a tiny sample
	missionStuckThreshold   = 2 * time.Hour
)

// MissionControlMetric is one number in a panel.
type MissionControlMetric struct {
	Key    string   `json:"key"`
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit"`   // count | percent | minutes | rounds | tokens | usd
	Status string   `json:"status"` // ok | warning | no_data
	Drill  string   `json:"drill"`  // intake | intake_source | workspaces | tokens
	// Num/Denom carry the sample the value was computed from (e.g. 3 of 12) so
	// the tile can show a muted ratio without the server sending prose.
	Num   int64 `json:"num,omitempty"`
	Denom int64 `json:"denom,omitempty"`
}

// IntakeDailyCount is one (source|chat, local day) bucket of auto-picked tasks.
type IntakeDailyCount struct {
	SourceKind  string `json:"source_kind"`
	ThreadExtID string `json:"thread_ext_id,omitempty"`
	Day         string `json:"day"` // YYYY-MM-DD, local time
	Count       int64  `json:"count"`
}

// MissionControlIntake is the 接活 panel.
type MissionControlIntake struct {
	Paused       bool                   `json:"paused"`
	ProjectCount int                    `json:"project_count"`
	Metrics      []MissionControlMetric `json:"metrics"`
	PerSource    []IntakeDailyCount     `json:"per_source"`
	PerChat      []IntakeDailyCount     `json:"per_chat"`
	// Sources is the delivery-health group (design §12): one row per ci/email/
	// webhook binding, with its recent success/failure and 24h counters.
	Sources []IntakeSourceHealth `json:"sources"`
}

// MissionControlCompletion is the 完成 panel.
type MissionControlCompletion struct {
	Metrics []MissionControlMetric `json:"metrics"`
}

// MissionControlView is the GET /api/mission-control payload.
type MissionControlView struct {
	Intake     MissionControlIntake     `json:"intake"`
	Completion MissionControlCompletion `json:"completion"`
}

// MissionControlService aggregates the two panel payloads.
type MissionControlService struct {
	q      *store.Queries
	db     *store.DB
	authz  *Authz
	intake *IntakeService

	// intakeSources supplies the per-binding delivery-health group. It is wired
	// with a setter because the source service is constructed later in DI.
	intakeSources *IntakeSourceService

	// now is the clock used for every time-bounded metric. nil means time.Now;
	// tests inject a fixed instant to pin day boundaries.
	now func() time.Time
}

// SetIntakeSources wires the binding service used for the delivery-health
// group (design §12).
func (s *MissionControlService) SetIntakeSources(svc *IntakeSourceService) {
	s.intakeSources = svc
}

func (s *MissionControlService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func NewMissionControlService(db *store.DB, q *store.Queries, authz *Authz, intake *IntakeService) *MissionControlService {
	return &MissionControlService{q: q, db: db, authz: authz, intake: intake}
}

// missionProject is the minimal project identity the metrics need.
type missionProject struct {
	ID        int64
	OwnerType string
	OwnerID   int64
}

// accessibleProjects resolves the caller's projects, narrowed by ownerFilter
// when set. The owner filter is applied to the returned rows so every metric
// follows the same scope as the overview cards.
func (s *MissionControlService) accessibleProjects(ctx context.Context, userID int64, ownerFilter *OwnerRef) ([]missionProject, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	rows, err := s.q.ListProjectsForOwners(ctx, store.ListProjectsForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]missionProject, 0, len(rows))
	for _, p := range rows {
		if ownerFilter != nil && (p.OwnerType != ownerFilter.Type || p.OwnerID != ownerFilter.ID) {
			continue
		}
		out = append(out, missionProject{ID: p.ID, OwnerType: p.OwnerType, OwnerID: p.OwnerID})
	}
	return out, nil
}

// workspaceOwnerScope builds the owner predicate for workspace-grain metrics.
// It mirrors the project scope: when an explicit owner filter is set, only that
// owner's workspaces count; otherwise the caller's personal + org workspaces.
func workspaceOwnerScope(userID int64, ownerFilter *OwnerRef, owners *AccessibleOwners) (string, []any) {
	if ownerFilter != nil {
		return "((w.owner_type = ? AND w.owner_id = ?))", []any{ownerFilter.Type, ownerFilter.ID}
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		return "((w.owner_type = 'user' AND w.owner_id = ?))", []any{userID}
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(orgIDs)), ",")
	args := []any{userID}
	for _, id := range orgIDs {
		args = append(args, id)
	}
	return "((w.owner_type = 'user' AND w.owner_id = ?) OR (w.owner_type = 'org' AND w.owner_id IN (" + ph + ")))", args
}

func inPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(ids []missionProject) []any {
	args := make([]any, len(ids))
	for i, p := range ids {
		args[i] = p.ID
	}
	return args
}

func fptr(v float64) *float64 { return &v }

func pct(num, denom int64) *float64 {
	if denom <= 0 {
		return nil
	}
	return fptr(float64(num) * 100 / float64(denom))
}

func rounded(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return roundFloat(*v)
}

func roundFloat(v float64) *float64 {
	r := float64(int64(v*10+0.5)) / 10
	if v < 0 {
		r = float64(int64(v*10-0.5)) / 10
	}
	return &r
}

func countMetric(key, drill string, n int64, available bool) MissionControlMetric {
	if !available {
		return MissionControlMetric{Key: key, Unit: "count", Status: "no_data", Drill: drill}
	}
	v := float64(n)
	return MissionControlMetric{Key: key, Value: &v, Unit: "count", Status: "ok", Drill: drill}
}

// rateMetric builds a percentage tile. num/denom of 0/0 → no data. warnAbove is
// disabled with a negative value so callers that have no meaningful threshold
// (e.g. first-pass rate) never paint a warning.
func rateMetric(key, drill string, num, denom int64, warnAbove float64) MissionControlMetric {
	v := rounded(pct(num, denom))
	if v == nil {
		return MissionControlMetric{Key: key, Unit: "percent", Status: "no_data", Drill: drill, Denom: denom}
	}
	status := "ok"
	if warnAbove >= 0 && *v > warnAbove {
		status = "warning"
	}
	return MissionControlMetric{Key: key, Value: v, Unit: "percent", Status: status, Drill: drill, Num: num, Denom: denom}
}

// View returns both independent panels.
func (s *MissionControlService) View(ctx context.Context, userID int64, ownerFilter *OwnerRef) (MissionControlView, error) {
	now := s.clock()
	// Stored timestamps are UTC (CURRENT_TIMESTAMP), and SQLite binds a
	// time.Time as its local wall-clock string, so a boundary must be expressed
	// in UTC or the comparison is shifted by the zone offset. The day is still
	// the user's local day: local midnight is converted to the equivalent UTC
	// instant (00:00 +08 == 16:00Z the previous day).
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UTC()
	windowStart := now.UTC().Add(-missionRateWindow)

	projects, err := s.accessibleProjects(ctx, userID, ownerFilter)
	if err != nil {
		return MissionControlView{}, err
	}
	projectIDs := make([]int64, len(projects))
	for i, p := range projects {
		projectIDs[i] = p.ID
	}
	args := idArgs(projects)

	// Zero projects: every value is unknown, not zero.
	if len(projects) == 0 {
		return MissionControlView{
			Intake: MissionControlIntake{
				Metrics:   emptyIntakeMetrics(),
				PerSource: []IntakeDailyCount{},
				PerChat:   []IntakeDailyCount{},
				Sources:   []IntakeSourceHealth{},
			},
			Completion: MissionControlCompletion{Metrics: emptyCompletionMetrics()},
		}, nil
	}

	intake, err := s.intakeView(ctx, userID, projectIDs, args, todayStart, windowStart)
	if err != nil {
		return MissionControlView{}, err
	}
	completion, err := s.completionView(ctx, userID, ownerFilter, projectIDs, args, todayStart, windowStart)
	if err != nil {
		return MissionControlView{}, err
	}
	return MissionControlView{Intake: intake, Completion: completion}, nil
}

func (s *MissionControlService) intakeView(ctx context.Context, userID int64, projectIDs []int64, args []any, todayStart, windowStart time.Time) (MissionControlIntake, error) {
	in := inPlaceholders(len(projectIDs))
	view := MissionControlIntake{
		ProjectCount: len(projectIDs),
		PerSource:    []IntakeDailyCount{},
		PerChat:      []IntakeDailyCount{},
		Sources:      []IntakeSourceHealth{},
	}

	// Paused means EVERY project in scope is paused: the panel switch turns the
	// whole scope on/off, so a partial pause must not read as "paused".
	pausedAll := true
	for _, pid := range projectIDs {
		p, err := s.intake.GetPaused(ctx, pid)
		if err != nil {
			return MissionControlIntake{}, err
		}
		if !p {
			pausedAll = false
			break
		}
	}
	view.Paused = pausedAll

	// Whether intake ever recorded anything in scope. Without this, a count of 0
	// is indistinguishable from "the capability never ran" and would render as a
	// noisy 0 instead of "—".
	var eventCount int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT CAST(COUNT(*) AS BIGINT) FROM intake_events WHERE project_id IN ("+in+")", args...).
		Scan(&eventCount); err != nil {
		return MissionControlIntake{}, err
	}
	available := eventCount > 0

	var todayAccepted, pending int64
	if available {
		if err := s.db.QueryRowContext(ctx,
			"SELECT CAST(COUNT(*) AS BIGINT) FROM intake_decisions WHERE project_id IN ("+in+
				") AND executed = 1 AND decision IN ('accept','resume') AND created_at >= ?",
			append(args, todayStart)...).Scan(&todayAccepted); err != nil {
			return MissionControlIntake{}, err
		}
		if err := s.db.QueryRowContext(ctx,
			"SELECT CAST(COUNT(*) AS BIGINT) FROM intake_decisions WHERE project_id IN ("+in+
				") AND executed = 0 AND decision IN ('accept','resume')",
			args...).Scan(&pending); err != nil {
			return MissionControlIntake{}, err
		}
	}

	// Approval rate = executed accept/resume vs ignored, over the rate window.
	var accepted, ignored int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT "+
			"CAST(COALESCE(SUM(CASE WHEN decision IN ('accept','resume') AND executed = 1 THEN 1 ELSE 0 END),0) AS BIGINT), "+
			"CAST(COALESCE(SUM(CASE WHEN decision = 'ignore' THEN 1 ELSE 0 END),0) AS BIGINT) "+
			"FROM intake_decisions WHERE project_id IN ("+in+") AND created_at >= ?",
		append(args, windowStart)...).Scan(&accepted, &ignored); err != nil {
		return MissionControlIntake{}, err
	}
	approvalDenom := accepted + ignored
	approval := rateMetric("approval_rate", "intake", accepted, approvalDenom, -1)
	if approval.Value != nil && *approval.Value < missionApprovalWarnPct && approvalDenom >= missionApprovalMinN {
		approval.Status = "warning"
	}

	// Misfire: auto-executed decisions whose issue was abandoned within 24h.
	autoDecisions, err := s.autoExecutedDecisions(ctx, in, args, windowStart)
	if err != nil {
		return MissionControlIntake{}, err
	}
	misfires, err := s.countMisfires(ctx, autoDecisions)
	if err != nil {
		return MissionControlIntake{}, err
	}
	misfire := rateMetric("misfire_rate", "intake", misfires, int64(len(autoDecisions)), missionMisfireWarnPct)

	// Duplicate rate: another auto-picked task for the same source+thread within
	// an hour before this one.
	duplicates, dupDenom, err := s.duplicateRate(ctx, in, args, windowStart)
	if err != nil {
		return MissionControlIntake{}, err
	}
	duplicate := rateMetric("duplicate_rate", "intake", duplicates, dupDenom, missionDuplicateWarnPct)

	latency, latencyN, err := s.eventToStartMinutes(ctx, in, args, windowStart)
	if err != nil {
		return MissionControlIntake{}, err
	}
	latencyMetric := MissionControlMetric{Key: "event_to_start", Unit: "minutes", Drill: "intake", Status: "no_data"}
	if latencyN > 0 {
		v := rounded(&latency)
		latencyMetric.Value = v
		latencyMetric.Denom = latencyN
		latencyMetric.Status = "ok"
		if v != nil && *v > missionLatencyWarnMin {
			latencyMetric.Status = "warning"
		}
	}

	perSource, perChat, err := s.intakeDaily(ctx, in, args, todayStart.AddDate(0, 0, -(missionPerSourceDays-1)))
	if err != nil {
		return MissionControlIntake{}, err
	}
	view.PerSource = perSource
	view.PerChat = perChat

	if s.intakeSources != nil {
		for _, pid := range projectIDs {
			health, err := s.intakeSources.HealthByProject(ctx, pid)
			if err != nil {
				return MissionControlIntake{}, err
			}
			view.Sources = append(view.Sources, health...)
		}
	}

	view.Metrics = []MissionControlMetric{
		countMetric("today_accepted", "intake", todayAccepted, available),
		countMetric("pending", "intake", pending, available),
		approval,
		misfire,
		duplicate,
		latencyMetric,
	}
	return view, nil
}

type autoDecision struct {
	ID        int64
	IssueID   int64
	CreatedAt time.Time
}

func (s *MissionControlService) autoExecutedDecisions(ctx context.Context, in string, args []any, windowStart time.Time) ([]autoDecision, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, issue_id, created_at FROM intake_decisions WHERE project_id IN ("+in+
			") AND executed = 1 AND mode_at_decision = 'auto' AND issue_id IS NOT NULL AND created_at >= ?",
		append(args, windowStart)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []autoDecision{}
	for rows.Next() {
		var d autoDecision
		if err := rows.Scan(&d.ID, &d.IssueID, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// countMisfires counts auto decisions whose issue got a terminal "abandoned"
// exec event inside the 24h misfire window. Terminal events are the only
// durable signal that a human closed the task, and they already carry a
// timestamp; comparing in Go keeps the query portable across SQLite/PG.
func (s *MissionControlService) countMisfires(ctx context.Context, decisions []autoDecision) (int64, error) {
	if len(decisions) == 0 {
		return 0, nil
	}
	ids := make(map[int64]struct{}, len(decisions))
	for _, d := range decisions {
		ids[d.IssueID] = struct{}{}
	}
	issueArgs := make([]any, 0, len(ids))
	for id := range ids {
		issueArgs = append(issueArgs, id)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT issue_id, created_at FROM issue_exec_events WHERE kind = 'terminal' AND summary LIKE 'abandoned%' "+
			"AND issue_id IN ("+inPlaceholders(len(issueArgs))+")", issueArgs...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	abandoned := make(map[int64][]time.Time)
	for rows.Next() {
		var issueID int64
		var at time.Time
		if err := rows.Scan(&issueID, &at); err != nil {
			return 0, err
		}
		abandoned[issueID] = append(abandoned[issueID], at)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var misfires int64
	for _, d := range decisions {
		for _, at := range abandoned[d.IssueID] {
			if !at.Before(d.CreatedAt) && at.Sub(d.CreatedAt) <= missionMisfireWindow {
				misfires++
				break
			}
		}
	}
	return misfires, nil
}

// duplicateRate counts executed accept/resume decisions that repeat the same
// (project, source, thread) of an earlier executed pick inside the duplicate
// window. Denom is every such decision considered.
func (s *MissionControlService) duplicateRate(ctx context.Context, in string, args []any, windowStart time.Time) (int64, int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT d.project_id, e.source_kind, e.thread_ext_id, d.created_at "+
			"FROM intake_decisions d JOIN intake_events e ON e.id = d.event_id "+
			"WHERE d.project_id IN ("+in+") AND d.executed = 1 AND d.decision IN ('accept','resume') "+
			"AND d.created_at >= ? AND e.thread_ext_id <> ''",
		append(args, windowStart)...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	type key struct {
		project int64
		source  string
		thread  string
	}
	byKey := make(map[key][]time.Time)
	var denom int64
	for rows.Next() {
		var project int64
		var source, thread string
		var at time.Time
		if err := rows.Scan(&project, &source, &thread, &at); err != nil {
			return 0, 0, err
		}
		byKey[key{project, source, thread}] = append(byKey[key{project, source, thread}], at)
		denom++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	var duplicates int64
	for _, times := range byKey {
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for i := 1; i < len(times); i++ {
			if times[i].Sub(times[i-1]) <= missionDuplicateWindow {
				duplicates++
			}
		}
	}
	return duplicates, denom, nil
}

// eventToStartMinutes averages received_at → workspace.created_at for executed
// decisions that produced a workspace.
func (s *MissionControlService) eventToStartMinutes(ctx context.Context, in string, args []any, windowStart time.Time) (float64, int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT e.received_at, w.created_at FROM intake_decisions d "+
			"JOIN intake_events e ON e.id = d.event_id "+
			"JOIN workspaces w ON w.id = d.workspace_id "+
			"WHERE d.project_id IN ("+in+") AND d.executed = 1 AND d.workspace_id IS NOT NULL AND d.created_at >= ?",
		append(args, windowStart)...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var total float64
	var n int64
	for rows.Next() {
		var received, started time.Time
		if err := rows.Scan(&received, &started); err != nil {
			return 0, 0, err
		}
		d := started.Sub(received).Minutes()
		if d < 0 {
			d = 0 // clock skew: never report a negative latency
		}
		total += d
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, nil
	}
	return total / float64(n), n, nil
}

// intakeDaily returns per-source and per-chat daily pick counts over the last
// week (the "每 source/chat 每日任务数" metric). Both slices are sorted newest
// day first, then by descending count so the UI can render a compact list.
func (s *MissionControlService) intakeDaily(ctx context.Context, in string, args []any, since time.Time) ([]IntakeDailyCount, []IntakeDailyCount, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT e.source_kind, COALESCE(e.thread_ext_id,''), d.created_at "+
			"FROM intake_decisions d JOIN intake_events e ON e.id = d.event_id "+
			"WHERE d.project_id IN ("+in+") AND d.executed = 1 AND d.decision IN ('accept','resume') AND d.created_at >= ?",
		append(args, since)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	sourceKey := func(source, day string) string { return source + "\x00" + day }
	chatKey := func(source, thread, day string) string { return source + "\x00" + thread + "\x00" + day }
	sourceCounts := map[string]int64{}
	chatCounts := map[string]int64{}
	for rows.Next() {
		var source, thread string
		var at time.Time
		if err := rows.Scan(&source, &thread, &at); err != nil {
			return nil, nil, err
		}
		day := at.In(time.Local).Format("2006-01-02")
		sourceCounts[sourceKey(source, day)]++
		if thread != "" {
			chatCounts[chatKey(source, thread, day)]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	perSource := make([]IntakeDailyCount, 0, len(sourceCounts))
	for k, count := range sourceCounts {
		parts := strings.SplitN(k, "\x00", 2)
		perSource = append(perSource, IntakeDailyCount{SourceKind: parts[0], Day: parts[1], Count: count})
	}
	perChat := make([]IntakeDailyCount, 0, len(chatCounts))
	for k, count := range chatCounts {
		parts := strings.SplitN(k, "\x00", 3)
		perChat = append(perChat, IntakeDailyCount{SourceKind: parts[0], ThreadExtID: parts[1], Day: parts[2], Count: count})
	}
	sortDaily := func(items []IntakeDailyCount) {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Day != items[j].Day {
				return items[i].Day > items[j].Day
			}
			if items[i].Count != items[j].Count {
				return items[i].Count > items[j].Count
			}
			return items[i].SourceKind < items[j].SourceKind
		})
	}
	sortDaily(perSource)
	sortDaily(perChat)
	return perSource, perChat, nil
}

func (s *MissionControlService) completionView(ctx context.Context, userID int64, ownerFilter *OwnerRef, projectIDs []int64, args []any, todayStart, windowStart time.Time) (MissionControlCompletion, error) {
	in := inPlaceholders(len(projectIDs))
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return MissionControlCompletion{}, err
	}
	ownerSQL, ownerArgs := workspaceOwnerScope(userID, ownerFilter, owners)

	var running, stuck int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT CAST(COALESCE(SUM(CASE WHEN w.status = 'running' AND w.is_archived = 0 THEN 1 ELSE 0 END),0) AS BIGINT), "+
			"CAST(COALESCE(SUM(CASE WHEN w.status = 'running' AND w.is_archived = 0 AND w.updated_at < ? THEN 1 ELSE 0 END),0) AS BIGINT) "+
			"FROM workspaces w WHERE "+ownerSQL,
		append([]any{s.clock().UTC().Add(-missionStuckThreshold)}, ownerArgs...)...).
		Scan(&running, &stuck); err != nil {
		return MissionControlCompletion{}, err
	}
	var workspaceCount int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT CAST(COUNT(*) AS BIGINT) FROM workspaces w WHERE "+ownerSQL, ownerArgs...).
		Scan(&workspaceCount); err != nil {
		return MissionControlCompletion{}, err
	}
	inProgress := countMetric("in_progress", "workspaces", running, workspaceCount > 0)
	stuckMetric := rateMetric("stuck_rate", "workspaces", stuck, running, missionStuckWarnPct)

	// Issues that are in a complete column and were touched inside the window.
	completedIDs, err := s.completedIssues(ctx, in, args, windowStart)
	if err != nil {
		return MissionControlCompletion{}, err
	}
	completedN := int64(len(completedIDs))

	// First-pass: completed with no intervention/ask_user event, ever.
	var firstPass int64
	advanceRounds := make(map[int64]int64)
	if completedN > 0 {
		present := make(map[int64]struct{}, len(completedIDs))
		for _, id := range completedIDs {
			present[id] = struct{}{}
		}
		withIntervention, err := s.issueIDsWithKinds(ctx,
			"issue_id IN ("+inPlaceholders(len(completedIDs))+") AND kind IN ('intervention','ask_user')",
			toAny(completedIDs))
		if err != nil {
			return MissionControlCompletion{}, err
		}
		firstPass = completedN - int64(len(withIntervention))
		roundRows, err := s.issueIDCounts(ctx,
			"issue_id IN ("+inPlaceholders(len(completedIDs))+") AND kind = 'advance'",
			toAny(completedIDs))
		if err != nil {
			return MissionControlCompletion{}, err
		}
		for id, n := range roundRows {
			if _, ok := present[id]; ok {
				advanceRounds[id] = n
			}
		}
	}
	firstPassMetric := rateMetric("first_pass_rate", "workspaces", firstPass, completedN, -1)

	var totalRounds int64
	for _, id := range completedIDs {
		totalRounds += advanceRounds[id]
	}
	avgRounds := MissionControlMetric{Key: "avg_rounds", Unit: "rounds", Drill: "workspaces", Status: "no_data"}
	if completedN > 0 {
		avgRounds.Value = roundFloat(float64(totalRounds) / float64(completedN))
		avgRounds.Denom = completedN
		avgRounds.Status = "ok"
	}

	reopened, entered, err := s.fakeCompletions(ctx, in, args, windowStart)
	if err != nil {
		return MissionControlCompletion{}, err
	}
	fakeMetric := rateMetric("fake_completion_rate", "workspaces", reopened, entered, missionFakeWarnPct)

	var interventions int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT CAST(COUNT(*) AS BIGINT) FROM issue_exec_events e "+
			"JOIN issues i ON i.id = e.issue_id JOIN columns c ON c.id = i.column_id "+
			"WHERE e.kind IN ('intervention','ask_user') AND e.created_at >= ? AND c.project_id IN ("+in+")",
		append([]any{todayStart}, args...)...).Scan(&interventions); err != nil {
		return MissionControlCompletion{}, err
	}
	interventionMetric := countMetric("interventions", "workspaces", interventions, workspaceCount > 0)

	var tokens int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT CAST(COALESCE(SUM(h.input_tokens + h.output_tokens + h.cache_creation_tokens + h.cache_read_tokens),0) AS BIGINT) "+
			"FROM workspace_token_hourly h JOIN workspaces w ON w.id = h.workspace_id "+
			"WHERE h.bucket_hour >= ? AND "+ownerSQL,
		append([]any{todayStart}, ownerArgs...)...).Scan(&tokens); err != nil {
		return MissionControlCompletion{}, err
	}
	tokenMetric := MissionControlMetric{Key: "today_tokens", Unit: "tokens", Drill: "tokens", Status: "no_data"}
	if workspaceCount > 0 {
		v := float64(tokens)
		tokenMetric.Value = &v
		tokenMetric.Status = "ok"
	}

	var cost float64
	if err := s.db.QueryRowContext(ctx,
		"SELECT CAST(COALESCE(SUM(c.cost_usd),0) AS REAL) FROM workspace_costs c JOIN workspaces w ON w.id = c.workspace_id "+
			"WHERE c.created_at >= ? AND "+ownerSQL,
		append([]any{todayStart}, ownerArgs...)...).Scan(&cost); err != nil {
		return MissionControlCompletion{}, err
	}
	costMetric := MissionControlMetric{Key: "today_cost", Unit: "usd", Drill: "tokens", Status: "no_data"}
	if workspaceCount > 0 {
		v := rounded(&cost)
		costMetric.Value = v
		costMetric.Status = "ok"
	}

	return MissionControlCompletion{Metrics: []MissionControlMetric{
		inProgress,
		stuckMetric,
		firstPassMetric,
		avgRounds,
		fakeMetric,
		interventionMetric,
		tokenMetric,
		costMetric,
	}}, nil
}

// completedIssues returns the IDs of issues currently in an op_primitive
// 'complete' column whose updated_at falls inside the window.
func (s *MissionControlService) completedIssues(ctx context.Context, in string, args []any, windowStart time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT i.id FROM issues i JOIN columns c ON c.id = i.column_id "+
			"WHERE c.op_primitive = 'complete' AND c.project_id IN ("+in+") AND i.updated_at >= ?",
		append(args, windowStart)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *MissionControlService) issueIDsWithKinds(ctx context.Context, where string, args []any) (map[int64]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT issue_id FROM issue_exec_events WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (s *MissionControlService) issueIDCounts(ctx context.Context, where string, args []any) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT issue_id, CAST(COUNT(*) AS BIGINT) FROM issue_exec_events WHERE "+where+" GROUP BY issue_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func toAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// fakeCompletions detects "完成后 7 天内被重开": an issue that was moved into a
// complete column inside the window and then moved out again within 7 days.
// issue_activities stores column names, so the target column is resolved by
// (project, name) and its op_primitive decides whether the move left the done
// lane.
func (s *MissionControlService) fakeCompletions(ctx context.Context, in string, args []any, windowStart time.Time) (reopened, entered int64, err error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT a.issue_id, a.created_at FROM issue_activities a "+
			"JOIN issues i ON i.id = a.issue_id JOIN columns ic ON ic.id = i.column_id "+
			"JOIN columns c ON c.project_id = ic.project_id AND c.name = a.new_value AND c.op_primitive = 'complete' "+
			"WHERE a.action = 'moved' AND ic.project_id IN ("+in+") AND a.created_at >= ?",
		append(args, windowStart)...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	enteredAt := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return 0, 0, err
		}
		// Keep the earliest entry: an issue that bounces through done twice is
		// still one "entered complete" observation for the rate's denominator.
		if prev, ok := enteredAt[id]; !ok || at.Before(prev) {
			enteredAt[id] = at
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	entered = int64(len(enteredAt))
	if entered == 0 {
		return 0, 0, nil
	}

	issueIDs := make([]int64, 0, len(enteredAt))
	for id := range enteredAt {
		issueIDs = append(issueIDs, id)
	}
	moves, err := s.db.QueryContext(ctx,
		"SELECT a.issue_id, a.created_at, COALESCE(c.op_primitive,'') "+
			"FROM issue_activities a JOIN issues i ON i.id = a.issue_id JOIN columns ic ON ic.id = i.column_id "+
			"LEFT JOIN columns c ON c.project_id = ic.project_id AND c.name = a.new_value "+
			"WHERE a.action = 'moved' AND a.issue_id IN ("+inPlaceholders(len(issueIDs))+")",
		toAny(issueIDs)...)
	if err != nil {
		return 0, 0, err
	}
	defer moves.Close()
	type move struct {
		at        time.Time
		primitive string
	}
	byIssue := map[int64][]move{}
	for moves.Next() {
		var id int64
		var m move
		if err := moves.Scan(&id, &m.at, &m.primitive); err != nil {
			return 0, 0, err
		}
		byIssue[id] = append(byIssue[id], m)
	}
	if err := moves.Err(); err != nil {
		return 0, 0, err
	}
	for id, entry := range enteredAt {
		for _, m := range byIssue[id] {
			if m.primitive == "" || m.primitive == "complete" {
				continue
			}
			if !m.at.Before(entry) && m.at.Sub(entry) <= missionFakeReopenWindow {
				reopened++
				break
			}
		}
	}
	return reopened, entered, nil
}

// SetPaused flips the intake stop switch for every project in scope. Returns the
// number of projects touched. Pausing stops inferred/auto work only; explicit
// requests keep flowing (the pipeline enforces that, not this method).
func (s *MissionControlService) SetPaused(ctx context.Context, userID int64, ownerFilter *OwnerRef, paused bool) (int, error) {
	projects, err := s.accessibleProjects(ctx, userID, ownerFilter)
	if err != nil {
		return 0, err
	}
	// Validate write access for every project before mutating any: this
	// endpoint is a bulk write, so a partially-authorized scope must fail
	// closed instead of leaving half the projects paused. It mirrors the
	// per-project gate IntakeHandler.SetPause applies on its sibling route.
	for _, p := range projects {
		owner := OwnerRef{Type: p.OwnerType, ID: p.OwnerID}
		if err := s.authz.EnsureOwnerWritable(ctx, userID, owner); err != nil {
			return 0, err
		}
	}
	for _, p := range projects {
		if _, err := s.intake.SetPaused(ctx, p.ID, paused); err != nil {
			return 0, fmt.Errorf("set intake pause for project %d: %w", p.ID, err)
		}
	}
	return len(projects), nil
}

func emptyIntakeMetrics() []MissionControlMetric {
	keys := []struct{ key, unit, drill string }{
		{"today_accepted", "count", "intake"},
		{"pending", "count", "intake"},
		{"approval_rate", "percent", "intake"},
		{"misfire_rate", "percent", "intake"},
		{"duplicate_rate", "percent", "intake"},
		{"event_to_start", "minutes", "intake"},
	}
	out := make([]MissionControlMetric, 0, len(keys))
	for _, k := range keys {
		out = append(out, MissionControlMetric{Key: k.key, Unit: k.unit, Status: "no_data", Drill: k.drill})
	}
	return out
}

func emptyCompletionMetrics() []MissionControlMetric {
	keys := []struct{ key, unit, drill string }{
		{"in_progress", "count", "workspaces"},
		{"stuck_rate", "percent", "workspaces"},
		{"first_pass_rate", "percent", "workspaces"},
		{"avg_rounds", "rounds", "workspaces"},
		{"fake_completion_rate", "percent", "workspaces"},
		{"interventions", "count", "workspaces"},
		{"today_tokens", "tokens", "tokens"},
		{"today_cost", "usd", "tokens"},
	}
	out := make([]MissionControlMetric, 0, len(keys))
	for _, k := range keys {
		out = append(out, MissionControlMetric{Key: k.key, Unit: k.unit, Status: "no_data", Drill: k.drill})
	}
	return out
}

// --- Ops Bot console aggregation (design §14.3) -----------------------------
//
// The ops panel is grouped owner → channel (Platform Pack) → account, never
// folded into one score. Each account carries the four things an operator
// checks before leaving it alone: status, output, budget headroom, and risk.
//
// A single account is enriched in isolation: any failure while reading its
// metrics/budget/approvals fills Account.Error and the rest of the panel still
// renders. The frontend shows that row with an explicit error state instead of
// letting one broken account blank the console.

// MissionControlOpsOutput is today's and this week's published output.
type MissionControlOpsOutput struct {
	Today int64 `json:"today"`
	Week  int64 `json:"week"`
}

// MissionControlOpsMetrics is the most recent normalized daily metric row.
type MissionControlOpsMetrics struct {
	Date   string          `json:"date"`
	Values json.RawMessage `json:"values"`
	Source string          `json:"source"`
}

// MissionControlOpsBudget is the account's current-month budget position.
// Configured is false when budget_json carries no monthly_limit, in which case
// the other numbers are not meaningful and the UI renders "not set".
type MissionControlOpsBudget struct {
	Limit      float64   `json:"limit"`
	Currency   string    `json:"currency"`
	Spent      float64   `json:"spent"`
	Remaining  float64   `json:"remaining"`
	Unlimited  bool      `json:"unlimited"`
	Configured bool      `json:"configured"`
	PeriodFrom time.Time `json:"period_from"`
	PeriodTo   time.Time `json:"period_to"`
}

// MissionControlOpsBrowser is the account's most recently updated browser
// session state (the row a worker leases).
type MissionControlOpsBrowser struct {
	Backend     string `json:"backend"`
	LoginStatus string `json:"login_status"`
	Health      string `json:"health"`
	RiskState   string `json:"risk_state"`
	LastError   string `json:"last_error,omitempty"`
}

// Risk levels. A risk flag never hides the account; it only marks the row.
const (
	OpsRiskNormal   = "normal"
	OpsRiskWarning  = "warning"
	OpsRiskCritical = "critical"
)

// MissionControlOpsAccount is one account row (and the drilldown source).
type MissionControlOpsAccount struct {
	BotID               int64                     `json:"bot_id"`
	Role                string                    `json:"role"`
	PackID              string                    `json:"pack_id"`
	PackVersion         int64                     `json:"pack_version"`
	AccountRef          string                    `json:"account_ref"`
	DisplayName         string                    `json:"display_name"`
	Status              string                    `json:"status"`
	Operator            string                    `json:"operator"`
	DirectorBotID       *int64                    `json:"director_bot_id,omitempty"`
	EpicIssueID         *int64                    `json:"epic_issue_id,omitempty"`
	Output              MissionControlOpsOutput   `json:"output"`
	Metrics             *MissionControlOpsMetrics `json:"metrics,omitempty"`
	Budget              MissionControlOpsBudget   `json:"budget"`
	PendingApprovals    int64                     `json:"pending_approvals"`
	HighRiskEngagements int64                     `json:"high_risk_engagements"`
	Risk                string                    `json:"risk"`
	RiskReasons         []string                  `json:"risk_reasons"`
	Browser             *MissionControlOpsBrowser `json:"browser,omitempty"`
	Error               string                    `json:"error,omitempty"`
	UpdatedAt           time.Time                 `json:"updated_at"`
}

// MissionControlOpsChannel groups accounts that publish to the same platform
// pack ("channel"). A channel is a pack id, not a bot: one pack can drive many
// accounts.
type MissionControlOpsChannel struct {
	PackID      string                     `json:"pack_id"`
	PackVersion int64                      `json:"pack_version"`
	Accounts    []MissionControlOpsAccount `json:"accounts"`
}

// MissionControlOpsOwner is one owner's slice of the console.
type MissionControlOpsOwner struct {
	OwnerType  string                     `json:"owner_type"`
	OwnerID    int64                      `json:"owner_id"`
	OwnerLabel string                     `json:"owner_label"`
	Channels   []MissionControlOpsChannel `json:"channels"`
	Error      string                     `json:"error,omitempty"`
}

// MissionControlOpsView is the GET /api/mission-control/ops payload.
type MissionControlOpsView struct {
	Owners       []MissionControlOpsOwner `json:"owners"`
	AccountCount int                      `json:"account_count"`
}

// OpsView aggregates the ops console. ownerFilter, when set, narrows the scope
// to that owner after the accessibility check, mirroring View.
func (s *MissionControlService) OpsView(ctx context.Context, userID int64, ownerFilter *OwnerRef) (MissionControlOpsView, error) {
	now := s.clock()
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return MissionControlOpsView{}, err
	}

	// Scope is owner-level, exactly matching OpsBotHandler.ListBots: a bot
	// belongs to an owner, not to a project, so there is no project-level
	// filter to apply here. A personal owner can only ever see their own row;
	// org owners require membership (authz.Accessible). The explicit
	// ownerFilter below is validated against this same set, never trusted.
	scope := []OwnerRef{{Type: "user", ID: userID}}
	for _, orgID := range owners.OrgIDs {
		scope = append(scope, OwnerRef{Type: "org", ID: orgID})
	}
	if ownerFilter != nil {
		scope = scope[:0]
		if ownerFilter.Type == "user" && ownerFilter.ID == userID {
			scope = append(scope, *ownerFilter)
		} else {
			for _, orgID := range owners.OrgIDs {
				if ownerFilter.Type == "org" && ownerFilter.ID == orgID {
					scope = append(scope, *ownerFilter)
					break
				}
			}
		}
	}

	view := MissionControlOpsView{Owners: []MissionControlOpsOwner{}}
	for _, owner := range scope {
		group := s.opsOwner(ctx, owner, now)
		view.AccountCount += countOpsAccounts(group)
		view.Owners = append(view.Owners, group)
	}
	return view, nil
}

func countOpsAccounts(group MissionControlOpsOwner) int {
	n := 0
	for _, ch := range group.Channels {
		n += len(ch.Accounts)
	}
	return n
}

func (s *MissionControlService) opsOwner(ctx context.Context, owner OwnerRef, now time.Time) MissionControlOpsOwner {
	group := MissionControlOpsOwner{
		OwnerType:  owner.Type,
		OwnerID:    owner.ID,
		OwnerLabel: s.ownerLabel(ctx, owner),
		Channels:   []MissionControlOpsChannel{},
	}
	bots, err := s.q.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		group.Error = err.Error()
		return group
	}
	pending, pendingErr := s.opsPendingApprovals(ctx, owner.Type, owner.ID)

	byPack := map[string]*MissionControlOpsChannel{}
	order := []string{}
	for _, bot := range bots {
		if bot.Role != "account" {
			continue
		}
		ch, ok := byPack[bot.PackID]
		if !ok {
			ch = &MissionControlOpsChannel{PackID: bot.PackID, Accounts: []MissionControlOpsAccount{}}
			byPack[bot.PackID] = ch
			order = append(order, bot.PackID)
		}
		if bot.PackVersion > ch.PackVersion {
			ch.PackVersion = bot.PackVersion
		}
		account := s.opsAccount(ctx, bot, group.OwnerLabel, now, pending)
		if pendingErr != nil {
			// A failed approval lookup must not lose the rest of the row.
			account.Error = joinNonEmpty(account.Error, pendingErr.Error())
		}
		ch.Accounts = append(ch.Accounts, account)
	}
	sort.Strings(order)
	for _, packID := range order {
		ch := byPack[packID]
		sort.Slice(ch.Accounts, func(i, j int) bool { return ch.Accounts[i].BotID < ch.Accounts[j].BotID })
		group.Channels = append(group.Channels, *ch)
	}
	return group
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// ownerLabel resolves a human label for the owner without widening scope: a
// personal owner renders the user's display name, an org renders its name.
func (s *MissionControlService) ownerLabel(ctx context.Context, owner OwnerRef) string {
	if owner.Type == "user" {
		var label string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(display_name,''), username) FROM users WHERE id = ?`, owner.ID).Scan(&label); err == nil && label != "" {
			return label
		}
		return fmt.Sprintf("user:%d", owner.ID)
	}
	var label string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM organizations WHERE id = ?`, owner.ID).Scan(&label); err == nil && label != "" {
		return label
	}
	return fmt.Sprintf("org:%d", owner.ID)
}

// opsAccount enriches one bot. Every sub-read is best-effort and records its
// error instead of aborting the panel, so one broken account is a broken row.
func (s *MissionControlService) opsAccount(ctx context.Context, bot store.OpsBot, ownerLabel string, now time.Time, pending map[int64]int64) MissionControlOpsAccount {
	acc := MissionControlOpsAccount{
		BotID:       bot.ID,
		Role:        bot.Role,
		PackID:      bot.PackID,
		PackVersion: bot.PackVersion,
		AccountRef:  bot.AccountRef,
		DisplayName: bot.DisplayName,
		Status:      bot.Status,
		Operator:    ownerLabel,
		Risk:        OpsRiskNormal,
		RiskReasons: []string{},
		UpdatedAt:   bot.UpdatedAt,
	}
	if bot.DirectorBotID.Valid {
		id := bot.DirectorBotID.Int64
		acc.DirectorBotID = &id
	}
	if bot.EpicIssueID.Valid {
		id := bot.EpicIssueID.Int64
		acc.EpicIssueID = &id
	}
	if pending != nil {
		acc.PendingApprovals = pending[bot.ID]
	}

	todayStart, weekStart, monthStart, monthEnd := s.opsWindows(now)

	if out, err := s.opsOutput(ctx, bot.ID, todayStart, weekStart); err != nil {
		acc.Error = joinNonEmpty(acc.Error, err.Error())
	} else {
		acc.Output = out
	}

	if m, err := s.opsLatestMetrics(ctx, bot.ID); err != nil {
		acc.Error = joinNonEmpty(acc.Error, err.Error())
	} else {
		acc.Metrics = m
	}

	budget, err := s.opsBudget(ctx, bot, monthStart, monthEnd)
	if err != nil {
		acc.Error = joinNonEmpty(acc.Error, err.Error())
	}
	acc.Budget = budget

	if n, err := s.opsHighRiskEngagements(ctx, bot.ID); err != nil {
		acc.Error = joinNonEmpty(acc.Error, err.Error())
	} else {
		acc.HighRiskEngagements = n
	}

	browser, err := s.opsBrowser(ctx, bot.ID)
	if err != nil {
		acc.Error = joinNonEmpty(acc.Error, err.Error())
	} else {
		acc.Browser = browser
	}

	acc.Risk, acc.RiskReasons = opsRisk(bot, acc.Budget, acc.Browser, acc.HighRiskEngagements)
	return acc
}

// opsWindows returns the local-day/week/month boundaries converted to UTC, the
// same normalization the rest of this file uses for stored timestamps.
func (s *MissionControlService) opsWindows(now time.Time) (todayStart, weekStart, monthStart, monthEnd time.Time) {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayStart = midnight.UTC()
	weekStart = midnight.AddDate(0, 0, -6).UTC()
	monthStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).UTC()
	monthEnd = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location()).UTC()
	return
}

func (s *MissionControlService) opsOutput(ctx context.Context, botID int64, todayStart, weekStart time.Time) (MissionControlOpsOutput, error) {
	var out MissionControlOpsOutput
	err := s.db.QueryRowContext(ctx,
		`SELECT CAST(COALESCE(SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END),0) AS BIGINT), `+
			`CAST(COUNT(*) AS BIGINT) FROM ops_publish_records `+
			`WHERE bot_id = ? AND status = 'published' AND created_at >= ?`,
		todayStart, botID, weekStart).Scan(&out.Today, &out.Week)
	return out, err
}

func (s *MissionControlService) opsLatestMetrics(ctx context.Context, botID int64) (*MissionControlOpsMetrics, error) {
	var (
		date   string
		raw    string
		source string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT date, metrics_json, source FROM ops_metrics_daily WHERE bot_id = ? ORDER BY date DESC LIMIT 1`,
		botID).Scan(&date, &raw, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &MissionControlOpsMetrics{Date: date, Values: opsJSONObject(raw), Source: source}, nil
}

// opsJSONObject normalizes a stored JSON column to a JSON object so the client
// never receives an empty/invalid blob.
func opsJSONObject(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

// opsBudget reads monthly_limit/currency from budget_json and sums the account
// Epic subtree's spend in the current local month.
func (s *MissionControlService) opsBudget(ctx context.Context, bot store.OpsBot, from, to time.Time) (MissionControlOpsBudget, error) {
	cfg := map[string]any{}
	if strings.TrimSpace(bot.BudgetJson) != "" {
		_ = json.Unmarshal([]byte(bot.BudgetJson), &cfg)
	}
	limit, _ := cfg["monthly_limit"].(float64)
	currency, _ := cfg["currency"].(string)
	if currency == "" {
		currency = "USD"
	}
	out := MissionControlOpsBudget{
		Limit:      limit,
		Currency:   currency,
		Unlimited:  limit <= 0,
		Configured: limit > 0,
		PeriodFrom: from,
		PeriodTo:   to,
	}
	if !bot.EpicIssueID.Valid {
		return out, nil
	}
	spent, err := s.q.SumOpsAccountCost(ctx, store.SumOpsAccountCostParams{
		ParentIssueID: sql.NullInt64{Int64: bot.EpicIssueID.Int64, Valid: true},
		CreatedAt:     from,
		CreatedAt_2:   to,
	})
	if err != nil {
		return out, err
	}
	out.Spent = spent
	if out.Configured {
		out.Remaining = limit - spent
	}
	return out, nil
}

func (s *MissionControlService) opsPendingApprovals(ctx context.Context, ownerType string, ownerID int64) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT metadata_json FROM agent_ask_user_requests `+
			`WHERE owner_type = ? AND owner_id = ? AND kind = 'ops_approval' AND status = 'pending'`,
		ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var meta struct {
			BotID int64 `json:"bot_id"`
		}
		if err := json.Unmarshal([]byte(raw), &meta); err != nil || meta.BotID <= 0 {
			continue
		}
		out[meta.BotID]++
	}
	return out, rows.Err()
}

func (s *MissionControlService) opsHighRiskEngagements(ctx context.Context, botID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT CAST(COUNT(*) AS BIGINT) FROM ops_engagements `+
			`WHERE bot_id = ? AND risk_level = 'high' AND status IN ('new','classified','suggested','escalated')`,
		botID).Scan(&n)
	return n, err
}

func (s *MissionControlService) opsBrowser(ctx context.Context, botID int64) (*MissionControlOpsBrowser, error) {
	var b MissionControlOpsBrowser
	err := s.db.QueryRowContext(ctx,
		`SELECT backend, login_status, health, risk_state, last_error FROM ops_browser_sessions `+
			`WHERE bot_id = ? ORDER BY updated_at DESC LIMIT 1`,
		botID).Scan(&b.Backend, &b.LoginStatus, &b.Health, &b.RiskState, &b.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// opsRisk derives a discrete risk level plus machine-readable reasons. Status
// and budget are always considered; browser and engagement signals add to them.
func opsRisk(bot store.OpsBot, budget MissionControlOpsBudget, browser *MissionControlOpsBrowser, highRisk int64) (string, []string) {
	reasons := []string{}
	level := OpsRiskNormal
	raise := func(l, reason string) {
		reasons = append(reasons, reason)
		if l == OpsRiskCritical || (l == OpsRiskWarning && level == OpsRiskNormal) {
			level = l
		}
	}
	switch bot.Status {
	case "paused":
		raise(OpsRiskWarning, "paused")
	case "disabled":
		raise(OpsRiskWarning, "disabled")
	}
	if budget.Configured {
		switch {
		case budget.Spent > budget.Limit:
			raise(OpsRiskCritical, "budget_exceeded")
		case budget.Remaining <= budget.Limit*0.1:
			raise(OpsRiskWarning, "budget_low")
		}
	}
	if browser != nil {
		switch browser.RiskState {
		case "blocked":
			raise(OpsRiskCritical, "browser_blocked")
		case "throttled":
			raise(OpsRiskWarning, "browser_throttled")
		}
		switch browser.Health {
		case "down":
			raise(OpsRiskCritical, "browser_down")
		case "degraded":
			raise(OpsRiskWarning, "browser_degraded")
		}
		if browser.LoginStatus == "expired" {
			raise(OpsRiskWarning, "browser_login_expired")
		}
	}
	if highRisk > 0 {
		raise(OpsRiskWarning, "high_risk_engagement")
	}
	return level, reasons
}
