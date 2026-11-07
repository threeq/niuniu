package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

const (
	// defaultTopicDedupeThreshold is the similarity at or above which two
	// topics are treated as the same piece across accounts.
	defaultTopicDedupeThreshold = 0.8
	defaultDirectorCurrency     = "USD"
)

var (
	// ErrDirectorNotConfigured is returned when a director operation needs a
	// dependency that was not wired.
	ErrDirectorNotConfigured = errors.New("opsbot: director is not configured")
)

// Topic is one item in the cross-account topic pool. GoalMatch is the caller's
// 0-1 score for how well the topic fits the account's goal; it only breaks ties
// between duplicates.
type Topic struct {
	BotID       int64   `json:"bot_id"`
	Title       string  `json:"title"`
	Body        string  `json:"body,omitempty"`
	TopicType   string  `json:"topic_type,omitempty"`
	GoalMatch   float64 `json:"goal_match,omitempty"`
	DuplicateOf string  `json:"duplicate_of,omitempty"`
	Replacement string  `json:"replacement,omitempty"`
}

// DirectorAccounts lists the account bots a director coordinates. The
// directory is responsible for excluding the director itself and other owners'
// bots; *SQLDirectorAccounts is the production implementation.
type DirectorAccounts interface {
	Accounts(ctx context.Context, directorBotID int64) ([]store.OpsBot, error)
}

// DirectorPlanner plans one account's fixed cadence for an ISO week.
// *Calendar satisfies it.
type DirectorPlanner interface {
	PlanWeek(ctx context.Context, ref BotRef, week string) ([]int64, error)
}

// DirectorCalendar is the aggregate calendar the director reads and, when a
// collision is found, rewrites. *Calendar satisfies it.
type DirectorCalendar interface {
	List(ctx context.Context, filter CalendarFilter) ([]CalendarEntry, error)
	Reschedule(ctx context.Context, owner OwnerRef, issueID int64, startDate, dueDate string) error
}

// ProxyApprover decides an approval on another operator's behalf.
// *Approval satisfies it.
type ProxyApprover interface {
	Decide(ctx context.Context, approvalID int64, d Decision) (ApprovalRequest, error)
}

// DirectorMessenger dispatches a director note to one account bot's control
// workspace (send_workspace_message in the runtime). The opsbot package must
// not import service (it would cycle), so the server adapts its messaging
// service to this interface.
type DirectorMessenger interface {
	SendToBot(ctx context.Context, botID int64, text string) error
}

// DirectorBoard posts director state other agents can read (the blackboard).
type DirectorBoard interface {
	Write(ctx context.Context, key, content string) error
}

// Director coordinates accounts: it plans each account's week, keeps the
// cross-account calendar collision-free, dedupes the shared topic pool,
// allocates monthly budgets, proxies approvals with an audit trail and rolls
// the accounts up into one report.
//
// It never edits an account's content. It only creates/reschedules tasks and
// dispatches tasks and budgets through the messenger and blackboard.
type Director struct {
	// OperatorID is the user id recorded as the proxy approver. ProxyApprove
	// refuses to run without it, so no proxy decision can be unattributed.
	OperatorID int64
	// Currency labels allocated budgets. Defaults to USD.
	Currency string

	Bots      DirectorAccounts
	Planner   DirectorPlanner
	Calendar  DirectorCalendar
	Approvals ProxyApprover
	Feedback  *Feedback
	Messenger DirectorMessenger
	Board     DirectorBoard
	// SaveBudget persists an allocated monthly limit (budget_json in
	// production). Optional: without it allocations stay in memory.
	SaveBudget func(ctx context.Context, botID int64, limit float64) error

	// Replace fills Topic.Replacement for a dropped duplicate. Optional; with
	// no hook the caller re-briefs the dropped topic itself.
	Replace func(dropped, kept Topic) Topic
	// Now overrides the clock in tests. Optional.
	Now func() time.Time

	budget    budgetLedger
	approvals approvalJournal
}

// AccountPlan is one account's line in a PlanAll result.
type AccountPlan struct {
	BotID       int64           `json:"bot_id"`
	DisplayName string          `json:"display_name,omitempty"`
	Created     []int64         `json:"created,omitempty"`
	Entries     []CalendarEntry `json:"entries,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// CalendarConflict is a same-account, same-day collision that could not be
// moved inside the planned week.
type CalendarConflict struct {
	BotID    int64   `json:"bot_id"`
	Date     string  `json:"date"`
	IssueIDs []int64 `json:"issue_ids,omitempty"`
}

// PlanResult is the cross-account weekly plan.
type PlanResult struct {
	Week        string             `json:"week"`
	Accounts    []AccountPlan      `json:"accounts"`
	Calendar    []CalendarEntry    `json:"calendar,omitempty"`
	Rescheduled []int64            `json:"rescheduled,omitempty"`
	Conflicts   []CalendarConflict `json:"conflicts,omitempty"`
	Warnings    []string           `json:"warnings,omitempty"`
}

// DirectorReport is the cross-account roll-up: output, budget, hit rate, goal
// achievement and the director's allocations.
type DirectorReport struct {
	BotID              int64             `json:"bot_id"`
	From               string            `json:"from"`
	To                 string            `json:"to"`
	Accounts           int               `json:"accounts"`
	Published          int               `json:"published"`
	Views              float64           `json:"views"`
	HitRate            float64           `json:"hit_rate"`
	BudgetSpent        float64           `json:"budget_spent"`
	OverallAchievement float64           `json:"overall_achievement"`
	Allocated          map[int64]float64 `json:"allocated,omitempty"`
	Weekly             *WeeklyReport     `json:"weekly,omitempty"`
}

// ProxyApprovalRecord is the local audit line for one proxy decision. The
// approval row itself also records approved_by/proxy_for in its metadata.
type ProxyApprovalRecord struct {
	ApprovalID int64     `json:"approval_id"`
	ProxyFor   int64     `json:"proxy_for"`
	OperatorID int64     `json:"operator_id"`
	Reason     string    `json:"reason"`
	At         time.Time `json:"at"`
}

// DedupeTopics keeps one topic per group of similar topics across accounts.
// Similarity is the better of normalized Levenshtein and bigram Jaccard, on
// the title and on title+body. Within a group the topic with the highest
// GoalMatch wins (the earliest on a tie); every other topic is returned in
// dropped with DuplicateOf set.
func (d *Director) DedupeTopics(topics []Topic, threshold float64) (kept, dropped []Topic) {
	if threshold <= 0 {
		threshold = defaultTopicDedupeThreshold
	}
	if threshold > 1 {
		threshold = 1
	}

	groups := make([][]int, 0, len(topics))
	for i := range topics {
		bestGroup, bestSimilarity := -1, -1.0
		for gi := range groups {
			for _, member := range groups[gi] {
				if similarity := topicSimilarity(topics[i], topics[member]); similarity > bestSimilarity {
					bestGroup, bestSimilarity = gi, similarity
				}
			}
		}
		if bestGroup >= 0 && bestSimilarity >= threshold {
			groups[bestGroup] = append(groups[bestGroup], i)
			continue
		}
		groups = append(groups, []int{i})
	}

	keeper := make([]int, len(topics))
	for _, members := range groups {
		best := members[0]
		for _, member := range members[1:] {
			if topics[member].GoalMatch > topics[best].GoalMatch {
				best = member
			}
		}
		for _, member := range members {
			keeper[member] = best
		}
	}

	for i := range topics {
		if keeper[i] == i {
			kept = append(kept, topics[i])
			continue
		}
		droppedTopic := topics[i]
		droppedTopic.DuplicateOf = topics[keeper[i]].Title
		if d != nil && d.Replace != nil {
			droppedTopic = d.Replace(droppedTopic, topics[keeper[i]])
		}
		dropped = append(dropped, droppedTopic)
	}
	return kept, dropped
}

// AllocateBudget sets the monthly limit for the given accounts and writes each
// limit through SaveBudget (budget_json in production). A limit of zero means
// unlimited, matching Budget.SetLimit. Limits are recorded only after the save
// succeeds.
func (d *Director) AllocateBudget(ctx context.Context, limits map[int64]float64) error {
	if len(limits) == 0 {
		return fmt.Errorf("%w: at least one account limit is required", ErrInvalidInput)
	}

	ids := make([]int64, 0, len(limits))
	var errs []error
	for botID, limit := range limits {
		if botID <= 0 {
			errs = append(errs, fmt.Errorf("%w: bot id %d", ErrInvalidInput, botID))
			continue
		}
		if math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0 {
			errs = append(errs, fmt.Errorf("%w: %v", ErrInvalidBudgetLimit, limit))
			continue
		}
		if d.SaveBudget != nil {
			if err := d.SaveBudget(ctx, botID, limit); err != nil {
				errs = append(errs, fmt.Errorf("opsbot: save budget for bot %d: %w", botID, err))
				continue
			}
		}
		d.budget.set(botID, limit)
		ids = append(ids, botID)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, botID := range ids {
		if d.Messenger == nil {
			continue
		}
		text := fmt.Sprintf("总监分配本月预算：%.2f %s", limits[botID], d.currency())
		if err := d.Messenger.SendToBot(ctx, botID, text); err != nil {
			errs = append(errs, fmt.Errorf("opsbot: notify bot %d of budget: %w", botID, err))
		}
	}

	if d.Board != nil {
		period := d.now().Format("2006-01")
		payload, err := json.Marshal(map[string]any{
			"period":   period,
			"currency": d.currency(),
			"limits":   limits,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("opsbot: encode budget board entry: %w", err))
		} else if err := d.Board.Write(ctx, "ops-director/budget/"+period, string(payload)); err != nil {
			errs = append(errs, fmt.Errorf("opsbot: write budget board entry: %w", err))
		}
	}
	return errors.Join(errs...)
}

// AllocateByWeight splits total across the director's accounts by
// AllocationWeight (goal weight × activity) and allocates the result.
func (d *Director) AllocateByWeight(ctx context.Context, directorBotID int64, total float64) (map[int64]float64, error) {
	if directorBotID <= 0 {
		return nil, fmt.Errorf("%w: director bot id is required", ErrInvalidInput)
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return nil, fmt.Errorf("%w: total %v must be > 0", ErrInvalidBudgetLimit, total)
	}
	if d.Bots == nil {
		return nil, ErrDirectorNotConfigured
	}
	accounts, err := d.Bots.Accounts(ctx, directorBotID)
	if err != nil {
		return nil, fmt.Errorf("opsbot: list director accounts: %w", err)
	}

	type weightedAccount struct {
		id     int64
		weight float64
	}
	weighted := make([]weightedAccount, 0, len(accounts))
	var weightSum float64
	for _, bot := range accounts {
		weight := AllocationWeight(bot)
		if weight <= 0 {
			continue
		}
		weighted = append(weighted, weightedAccount{id: bot.ID, weight: weight})
		weightSum += weight
	}
	if len(weighted) == 0 {
		return nil, fmt.Errorf("%w: no account has a positive allocation weight", ErrInvalidInput)
	}

	limits := make(map[int64]float64, len(weighted))
	var assigned float64
	for i, account := range weighted {
		var share float64
		if i == len(weighted)-1 {
			share = roundMoney(total - assigned)
		} else {
			share = roundMoney(total * account.weight / weightSum)
			assigned += share
		}
		limits[account.id] = share
	}
	if err := d.AllocateBudget(ctx, limits); err != nil {
		return limits, err
	}
	return limits, nil
}

// AllocationWeight is an account's share weight: goals_json budget_weight (or
// weight, default 1), halved while paused and zero while disabled.
func AllocationWeight(bot store.OpsBot) float64 {
	weight := goalWeight(bot.GoalsJson)
	switch bot.Status {
	case StatusPaused:
		weight *= 0.5
	case StatusDisabled:
		weight = 0
	}
	return weight
}

// ProxyApprove approves one card on behalf of proxyFor and records the
// director as the deciding operator. A reason is mandatory; otherwise the
// audit trail cannot explain the proxy decision.
func (d *Director) ProxyApprove(ctx context.Context, proxyFor, approvalID int64, reason string) error {
	if proxyFor <= 0 || approvalID <= 0 {
		return fmt.Errorf("%w: proxy_for and approval id must be > 0", ErrInvalidInput)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: a proxy approval requires a reason", ErrInvalidInput)
	}
	if d.OperatorID <= 0 {
		return fmt.Errorf("%w: director operator id is required", ErrDirectorNotConfigured)
	}
	if d.Approvals == nil {
		return ErrDirectorNotConfigured
	}

	_, err := d.Approvals.Decide(ctx, approvalID, Decision{
		OperatorID: d.OperatorID,
		Action:     ActionApproved,
		Reason:     reason,
		ProxyFor:   proxyFor,
	})
	if err != nil {
		return fmt.Errorf("opsbot: proxy approve %d for %d: %w", approvalID, proxyFor, err)
	}
	d.approvals.record(ProxyApprovalRecord{
		ApprovalID: approvalID,
		ProxyFor:   proxyFor,
		OperatorID: d.OperatorID,
		Reason:     reason,
		At:         d.now(),
	})
	return nil
}

// ProxyApprovals returns the director's local proxy audit trail, oldest first.
func (d *Director) ProxyApprovals() []ProxyApprovalRecord {
	return d.approvals.snapshot()
}

// PlanAll plans every account's ISO week, aggregates the calendar and moves
// same-account, same-day collisions inside the week. A failed account does not
// block the others; its error is recorded on its AccountPlan and joined into
// the returned error.
func (d *Director) PlanAll(ctx context.Context, directorBotID int64, week string) (PlanResult, error) {
	if directorBotID <= 0 {
		return PlanResult{}, fmt.Errorf("%w: director bot id is required", ErrInvalidInput)
	}
	monday, sunday, ok := weekBounds(week)
	if !ok {
		return PlanResult{}, fmt.Errorf("%w: %q must be YYYY-Www", ErrInvalidWeek, week)
	}
	if d.Bots == nil || d.Planner == nil || d.Calendar == nil {
		return PlanResult{}, ErrDirectorNotConfigured
	}

	accounts, err := d.Bots.Accounts(ctx, directorBotID)
	if err != nil {
		return PlanResult{}, fmt.Errorf("opsbot: list director accounts: %w", err)
	}

	result := PlanResult{Week: week, Accounts: make([]AccountPlan, 0, len(accounts))}
	owner := OwnerRef{Type: defaultOwnerType}
	if len(accounts) > 0 {
		owner = OwnerRef{Type: accounts[0].OwnerType, ID: accounts[0].OwnerID}
		if owner.Type == "" {
			owner.Type = defaultOwnerType
		}
	}

	ids := make([]int64, 0, len(accounts))
	var errs []error
	for _, bot := range accounts {
		plan := AccountPlan{BotID: bot.ID, DisplayName: bot.DisplayName}
		ids = append(ids, bot.ID)
		created, planErr := d.Planner.PlanWeek(ctx, BotRef{ID: bot.ID}, week)
		if planErr != nil {
			plan.Error = planErr.Error()
			errs = append(errs, fmt.Errorf("opsbot: plan bot %d for %s: %w", bot.ID, week, planErr))
		} else {
			plan.Created = created
		}
		result.Accounts = append(result.Accounts, plan)
	}

	if len(ids) > 0 {
		entries, listErr := d.Calendar.List(ctx, CalendarFilter{
			OwnerType: owner.Type,
			OwnerID:   owner.ID,
			BotIDs:    ids,
			From:      monday.Format(calendarDateLayout),
			To:        sunday.Format(calendarDateLayout),
		})
		if listErr != nil {
			errs = append(errs, fmt.Errorf("opsbot: list director calendar: %w", listErr))
		} else {
			entries, rescheduled, conflicts, warnings := d.reconcileCalendar(ctx, owner, entries, sunday)
			result.Calendar = entries
			result.Rescheduled = rescheduled
			result.Conflicts = conflicts
			result.Warnings = append(result.Warnings, warnings...)

			byBot := make(map[int64][]CalendarEntry, len(accounts))
			for _, entry := range entries {
				byBot[entry.BotID] = append(byBot[entry.BotID], entry)
			}
			for i := range result.Accounts {
				result.Accounts[i].Entries = byBot[result.Accounts[i].BotID]
			}
		}
	}

	for _, plan := range result.Accounts {
		if d.Messenger == nil || plan.Error != "" {
			continue
		}
		text := fmt.Sprintf("总监排期 %s：新增 %d 篇，本周共 %d 篇", week, len(plan.Created), len(plan.Entries))
		if err := d.Messenger.SendToBot(ctx, plan.BotID, text); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("notify bot %d: %v", plan.BotID, err))
		}
	}

	if d.Board != nil {
		payload, marshalErr := json.Marshal(map[string]any{
			"week":        week,
			"accounts":    len(result.Accounts),
			"rescheduled": result.Rescheduled,
			"conflicts":   result.Conflicts,
		})
		if marshalErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("encode plan board entry: %v", marshalErr))
		} else if boardErr := d.Board.Write(ctx, "ops-director/plan/"+week, string(payload)); boardErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("write plan board entry: %v", boardErr))
		}
	}
	return result, errors.Join(errs...)
}

// Report rolls the director bot up across its accounts. It reuses Feedback's
// director report and adds the director's current budget allocations.
func (d *Director) Report(ctx context.Context, directorBotID int64, from, to string) (DirectorReport, error) {
	if directorBotID <= 0 {
		return DirectorReport{}, fmt.Errorf("%w: director bot id is required", ErrInvalidInput)
	}
	if d.Feedback == nil {
		return DirectorReport{}, ErrDirectorNotConfigured
	}

	feedback := *d.Feedback
	if feedback.Accounts == nil && d.Bots != nil {
		feedback.Accounts = func(ctx context.Context, id int64) ([]int64, error) {
			bots, err := d.Bots.Accounts(ctx, id)
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(bots))
			for _, bot := range bots {
				ids = append(ids, bot.ID)
			}
			return ids, nil
		}
	}

	weekly, err := feedback.WeeklyReport(ctx, BotRef{ID: directorBotID, Role: RoleDirector}, from, to)
	if err != nil {
		return DirectorReport{}, err
	}

	report := DirectorReport{
		BotID:              directorBotID,
		From:               weekly.From,
		To:                 weekly.To,
		Accounts:           1,
		Published:          weekly.Published,
		Views:              weekly.Views,
		BudgetSpent:        weekly.Budget.Total,
		OverallAchievement: weekly.OverallAchievement,
		Allocated:          d.budget.snapshot(),
		Weekly:             &weekly,
	}
	if weekly.Director != nil {
		report.Accounts = weekly.Director.Accounts
		report.HitRate = weekly.Director.HitRate
	}
	return report, nil
}

// reconcileCalendar moves same-account, same-publish-day collisions to the next
// free day in the week. Entries it could not move are returned as conflicts.
func (d *Director) reconcileCalendar(
	ctx context.Context, owner OwnerRef, entries []CalendarEntry, weekEnd time.Time,
) ([]CalendarEntry, []int64, []CalendarConflict, []string) {
	out := append([]CalendarEntry(nil), entries...)

	byBot := make(map[int64][]int, len(out))
	var order []int64
	for i := range out {
		if _, seen := byBot[out[i].BotID]; !seen {
			order = append(order, out[i].BotID)
		}
		byBot[out[i].BotID] = append(byBot[out[i].BotID], i)
	}

	var rescheduled []int64
	var conflicts []CalendarConflict
	var warnings []string
	for _, botID := range order {
		indices := byBot[botID]
		sort.SliceStable(indices, func(a, b int) bool {
			left, right := entryStart(out[indices[a]]), entryStart(out[indices[b]])
			if left != right {
				return left < right
			}
			return out[indices[a]].IssueID < out[indices[b]].IssueID
		})

		used := make(map[string]bool, len(indices))
		for _, index := range indices {
			start, due, ok := entryWindow(out[index])
			if !ok {
				continue
			}
			day := start.Format(calendarDateLayout)
			if !used[day] {
				used[day] = true
				continue
			}

			durationDays := int(due.Sub(start).Hours() / 24)
			next := start.AddDate(0, 0, 1)
			for !next.After(weekEnd) && used[next.Format(calendarDateLayout)] {
				next = next.AddDate(0, 0, 1)
			}
			if next.After(weekEnd) {
				conflicts = append(conflicts, CalendarConflict{
					BotID: botID, Date: day, IssueIDs: []int64{out[index].IssueID},
				})
				continue
			}
			newDue := next.AddDate(0, 0, durationDays)
			if newDue.After(weekEnd) {
				newDue = weekEnd
			}
			startDate := next.Format(calendarDateLayout)
			dueDate := newDue.Format(calendarDateLayout)
			if err := d.Calendar.Reschedule(ctx, owner, out[index].IssueID, startDate, dueDate); err != nil {
				warnings = append(warnings, fmt.Sprintf("reschedule issue %d: %v", out[index].IssueID, err))
				conflicts = append(conflicts, CalendarConflict{
					BotID: botID, Date: day, IssueIDs: []int64{out[index].IssueID},
				})
				continue
			}
			out[index].StartDate = startDate
			out[index].DueDate = dueDate
			used[startDate] = true
			rescheduled = append(rescheduled, out[index].IssueID)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		left, right := entryStart(out[i]), entryStart(out[j])
		if left != right {
			return left < right
		}
		if out[i].BotID != out[j].BotID {
			return out[i].BotID < out[j].BotID
		}
		return out[i].IssueID < out[j].IssueID
	})
	return out, rescheduled, conflicts, warnings
}

func entryWindow(entry CalendarEntry) (start, due time.Time, ok bool) {
	startDate := strings.TrimSpace(entry.StartDate)
	dueDate := strings.TrimSpace(entry.DueDate)
	if startDate == "" {
		startDate = dueDate
	}
	if dueDate == "" {
		dueDate = startDate
	}
	if startDate == "" {
		return time.Time{}, time.Time{}, false
	}
	start, startErr := time.Parse(calendarDateLayout, startDate)
	due, dueErr := time.Parse(calendarDateLayout, dueDate)
	if startErr != nil || dueErr != nil {
		return time.Time{}, time.Time{}, false
	}
	if due.Before(start) {
		due = start
	}
	return start, due, true
}

func topicSimilarity(a, b Topic) float64 {
	titleSimilarity := textSimilarity(normalizeTopic(a.Title), normalizeTopic(b.Title))
	bodySimilarity := textSimilarity(
		normalizeTopic(a.Title+" "+a.Body),
		normalizeTopic(b.Title+" "+b.Body),
	)
	if bodySimilarity > titleSimilarity {
		return bodySimilarity
	}
	return titleSimilarity
}

func textSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	similarity := 1 - float64(levenshtein(a, b))/float64(max(len([]rune(a)), len([]rune(b))))
	if jaccard := bigramJaccard(a, b); jaccard > similarity {
		return jaccard
	}
	return similarity
}

func levenshtein(a, b string) int {
	left, right := []rune(a), []rune(b)
	if len(left) == 0 {
		return len(right)
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}

func goalWeight(raw string) float64 {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &cfg); err != nil {
		return 1
	}
	for _, key := range []string{"budget_weight", "weight"} {
		switch value := cfg[key].(type) {
		case float64:
			if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
				return value
			}
		case string:
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 1
}

func roundMoney(value float64) float64 {
	return math.Round(value*100) / 100
}

func (d *Director) currency() string {
	if currency := strings.TrimSpace(d.Currency); currency != "" {
		return currency
	}
	return defaultDirectorCurrency
}

func (d *Director) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

type budgetLedger struct {
	mu     sync.Mutex
	limits map[int64]float64
}

func (l *budgetLedger) set(botID int64, limit float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limits == nil {
		l.limits = make(map[int64]float64)
	}
	l.limits[botID] = limit
}

func (l *budgetLedger) snapshot() map[int64]float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[int64]float64, len(l.limits))
	for id, limit := range l.limits {
		out[id] = limit
	}
	return out
}

type approvalJournal struct {
	mu           sync.Mutex
	lastProxyFor int64
	records      []ProxyApprovalRecord
}

func (j *approvalJournal) record(record ProxyApprovalRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastProxyFor = record.ProxyFor
	j.records = append(j.records, record)
}

func (j *approvalJournal) snapshot() []ProxyApprovalRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]ProxyApprovalRecord(nil), j.records...)
}

// BudgetSaver persists a director allocation into a bot's budget_json while
// preserving every other key. Wire Director.SaveBudget to Save.
type BudgetSaver struct {
	Service  *Service
	Currency string
}

// Save writes monthly_limit and currency into the bot's budget_json.
func (s *BudgetSaver) Save(ctx context.Context, botID int64, limit float64) error {
	if s == nil || s.Service == nil {
		return ErrBudgetNotConfigured
	}
	bot, err := s.Service.Get(ctx, botID)
	if err != nil {
		return err
	}
	cfg := map[string]any{}
	if raw := strings.TrimSpace(bot.BudgetJson); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return fmt.Errorf("%w: budget_json must be a JSON object: %v", ErrInvalidConfig, err)
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["monthly_limit"] = limit
	currency := strings.TrimSpace(s.Currency)
	if currency == "" {
		currency = defaultDirectorCurrency
	}
	cfg["currency"] = currency

	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("opsbot: encode budget_json: %w", err)
	}
	raw := string(body)
	_, err = s.Service.UpdateConfig(ctx, botID, ConfigPatch{BudgetJSON: &raw})
	return err
}

// SQLDirectorAccounts is the production DirectorAccounts: it lists the account
// bots under the director's owner. Accounts explicitly linked to the director
// win; when nobody is linked (single-director deployments) every account of the
// owner is coordinated.
type SQLDirectorAccounts struct {
	Q *store.Queries
}

var _ DirectorAccounts = (*SQLDirectorAccounts)(nil)

func (s *SQLDirectorAccounts) Accounts(ctx context.Context, directorBotID int64) ([]store.OpsBot, error) {
	if s == nil || s.Q == nil {
		return nil, ErrDirectorNotConfigured
	}
	director, err := s.Q.GetOpsBot(ctx, directorBotID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %d", ErrNotFound, directorBotID)
	}
	if err != nil {
		return nil, fmt.Errorf("opsbot: load director bot: %w", err)
	}
	if director.Role != RoleDirector {
		return nil, fmt.Errorf("%w: bot %d is not a director", ErrInvalidInput, directorBotID)
	}

	ownerType := strings.TrimSpace(director.OwnerType)
	if ownerType == "" {
		ownerType = defaultOwnerType
	}
	bots, err := s.Q.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{
		OwnerType: ownerType,
		OwnerID:   director.OwnerID,
	})
	if err != nil {
		return nil, err
	}

	var assigned, all []store.OpsBot
	for _, bot := range bots {
		if bot.Role != RoleAccount {
			continue
		}
		all = append(all, bot)
		if bot.DirectorBotID.Valid && bot.DirectorBotID.Int64 == directorBotID {
			assigned = append(assigned, bot)
		}
	}
	if len(assigned) > 0 {
		return assigned, nil
	}
	return all, nil
}
