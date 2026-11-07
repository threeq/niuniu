package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Signal source names (design spec §6.3). Sources are pluggable so the
// info-radar scene, social-ops scene, a data source and a manual operator
// entry all funnel through the same poll/evaluate path.
const (
	SignalSourceInfoRadar  = "info-radar"
	SignalSourceSocialOps  = "social-ops"
	SignalSourceDataSource = "data_source"
	SignalSourceManual     = "manual"
)

// Hot-topic actions (design spec §6.3).
const (
	// TrendAddNow inserts an extra piece into this week's calendar. It only
	// takes an empty day, so the fixed cadence is never overwritten.
	TrendAddNow = "add_now"
	// TrendMergeNextWeek folds the signal into next week's calendar.
	TrendMergeNextWeek = "merge_next_week"
	// TrendSkip drops the signal (irrelevant, duplicate, over budget, or the
	// account has no room and no hot-topic boost).
	TrendSkip = "skip"
)

const (
	// trendAddNowScore is the relevance × fit × heat score at which a signal
	// is worth an immediate extra slot.
	trendAddNowScore = 0.5
	// trendMergeScore is the floor below which a signal is dropped instead of
	// being carried into next week.
	trendMergeScore = 0.1
	// trendDedupeWindow is how long the same topic must stay silent before it
	// may be boosted again.
	trendDedupeWindow = 7 * 24 * time.Hour
)

// Signal is one hot-topic signal. Heat is 0-100; Relevance and every Fit
// value are 0-1 (Relevance 0 means "unscored" and is treated as 1). Fit maps
// account bot ids to how well the topic matches that account.
type Signal struct {
	Title      string            `json:"title"`
	Source     string            `json:"source,omitempty"`
	URL        string            `json:"url,omitempty"`
	Summary    string            `json:"summary,omitempty"`
	Heat       float64           `json:"heat"`
	Relevance  float64           `json:"relevance,omitempty"`
	Fit        map[int64]float64 `json:"fit,omitempty"`
	CapturedAt time.Time         `json:"captured_at,omitempty"`
}

// TrendDecision is the evaluation result for one (signal, account) pair.
type TrendDecision struct {
	Signal  Signal  `json:"signal"`
	BotID   int64   `json:"bot_id"`
	Action  string  `json:"action"`
	Score   float64 `json:"score"`
	Reason  string  `json:"reason,omitempty"`
	IssueID int64   `json:"issue_id,omitempty"`
	Week    string  `json:"week,omitempty"`
	DueDate string  `json:"due_date,omitempty"`
}

// TrendSource supplies hot-topic signals. Name identifies the source
// (info-radar / social-ops / data_source / manual).
type TrendSource interface {
	Name() string
	Poll(ctx context.Context) ([]Signal, error)
}

// TrendSourceFunc adapts a function to TrendSource.
type TrendSourceFunc struct {
	SourceName string
	PollFn     func(ctx context.Context) ([]Signal, error)
}

func (f TrendSourceFunc) Name() string { return f.SourceName }

func (f TrendSourceFunc) Poll(ctx context.Context) ([]Signal, error) {
	if f.PollFn == nil {
		return nil, nil
	}
	return f.PollFn(ctx)
}

// TrendBotStore loads the account bot a signal maps to, for its cadence,
// channel, goal and audience. Optional: without it signals are evaluated with
// neutral defaults.
type TrendBotStore interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
}

// TrendCalendar reports the dates already taken by the bot's content tasks.
// Dates are YYYY-MM-DD. A hot-topic add-on only ever takes a free date, so the
// fixed cadence is never displaced. Optional: without it all days are free.
type TrendCalendar interface {
	OccupiedDates(ctx context.Context, botID int64, from, to string) ([]string, error)
}

// TrendHistory reports topics the bot already covered since a cutoff. It backs
// the 7-day same-topic dedupe. Optional.
type TrendHistory interface {
	RecentTopics(ctx context.Context, botID int64, since time.Time) ([]string, error)
}

// TrendPlanner materialises a trend content task. *content.BriefBuilder
// satisfies it; tests inject a fake. Optional: without it decisions are
// returned without an issue id.
type TrendPlanner interface {
	Build(ctx context.Context, in content.BriefInput) (content.OpsContentBrief, error)
}

// TrendBudget is the pre-check run before an add-on is created. *Budget
// satisfies it.
type TrendBudget interface {
	Check(ctx context.Context, botID, epicIssueID int64, cost float64) error
}

// Trend turns hot-topic signals into calendar actions for the director.
//
// Trust boundary: like Calendar, Trend does not authenticate callers. The entry
// layer must resolve the director and every Fit bot id from an authenticated
// principal before calling Evaluate.
type Trend struct {
	Sources []TrendSource
	// Manual holds operator-entered signals (a hint from chat, a pasted link).
	Manual   []Signal
	Bots     TrendBotStore
	Calendar TrendCalendar
	History  TrendHistory
	Planner  TrendPlanner
	Budget   TrendBudget
	// EstimatedCost is the per-piece cost used by the budget pre-check.
	EstimatedCost float64
	// Now overrides the clock in tests. Optional.
	Now func() time.Time
}

// Poll collects signals from every configured source plus the manual entries.
// One failing source does not discard the others: the returned error joins the
// per-source failures while the signals that did arrive are still returned.
func (t *Trend) Poll(ctx context.Context) ([]Signal, error) {
	var (
		out  []Signal
		errs []error
	)
	for _, src := range t.Sources {
		if src == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(src.Name()))
		signals, err := src.Poll(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("opsbot: trend source %s: %w", fallbackText(name, "unknown"), err))
			continue
		}
		for _, sig := range signals {
			sig = sig.normalized()
			if sig.Title == "" {
				continue
			}
			if sig.Source == "" {
				sig.Source = name
			}
			out = append(out, sig)
		}
	}
	for _, sig := range t.Manual {
		sig = sig.normalized()
		if sig.Title == "" {
			continue
		}
		if sig.Source == "" {
			sig.Source = SignalSourceManual
		}
		out = append(out, sig)
	}
	return dedupeSignals(out), errors.Join(errs...)
}

// Evaluate decides, per account, whether each signal becomes an immediate
// extra piece, moves to next week, or is dropped. The decision considers brand
// relevance, per-account fit, the account's hot-topic boost preference, budget,
// remaining calendar days and the 7-day same-topic rule.
//
// Only the director evaluates signals; an empty director.Role is accepted for
// callers that already resolved it.
func (t *Trend) Evaluate(ctx context.Context, director BotRef, signals []Signal) ([]TrendDecision, error) {
	if director.Role != "" && director.Role != RoleDirector {
		return nil, fmt.Errorf("%w: only a director evaluates hot-topic signals", ErrInvalidInput)
	}

	now := t.now()
	week := isoWeekString(now)
	monday, _, ok := weekBounds(week)
	if !ok {
		return nil, fmt.Errorf("%w: current week %q", ErrInvalidWeek, week)
	}
	nextMonday := monday.AddDate(0, 0, 7)
	nextWeek := isoWeekString(nextMonday)
	today := now.Format(calendarDateLayout)
	since := now.Add(-trendDedupeWindow)

	decisions := make([]TrendDecision, 0, len(signals))
	// Topics already scheduled in this call, per account, so two signals with
	// the same topic cannot both be added.
	scheduled := map[int64][]string{}

	for _, raw := range signals {
		sig := raw.normalized()
		if sig.Title == "" {
			decisions = append(decisions, TrendDecision{Signal: sig, Action: TrendSkip, Reason: "信号没有标题"})
			continue
		}
		candidates := sortedFit(sig.Fit)
		if len(candidates) == 0 {
			decisions = append(decisions, TrendDecision{Signal: sig, Action: TrendSkip, Reason: "没有匹配的账号"})
			continue
		}
		for _, candidate := range candidates {
			decision, err := t.evaluateAccount(ctx, candidate.botID, candidate.fit, sig, now, week, nextWeek, monday, nextMonday, today, since, scheduled[candidate.botID])
			if err != nil {
				return decisions, err
			}
			if decision.Action == TrendAddNow || decision.Action == TrendMergeNextWeek {
				scheduled[candidate.botID] = append(scheduled[candidate.botID], sig.Title)
			}
			decisions = append(decisions, decision)
		}
	}
	return decisions, nil
}

type fitCandidate struct {
	botID int64
	fit   float64
}

func (t *Trend) evaluateAccount(ctx context.Context, botID int64, fit float64, sig Signal, now time.Time, week, nextWeek string, monday, nextMonday time.Time, today string, since time.Time, prior []string) (TrendDecision, error) {
	decision := TrendDecision{
		Signal: sig,
		BotID:  botID,
		Action: TrendSkip,
		Score:  trendScore(sig, fit),
	}

	info, err := t.botContext(ctx, botID)
	if err != nil {
		return decision, err
	}
	if !info.boost {
		decision.Reason = "账号未开启热点加更"
		return decision, nil
	}
	if decision.Score < trendMergeScore {
		decision.Reason = "热度或账号适配度不足"
		return decision, nil
	}

	duplicate, err := t.isDuplicate(ctx, botID, sig.Title, prior, since)
	if err != nil {
		return decision, err
	}
	if duplicate {
		decision.Reason = "7 天内已加更过同主题"
		return decision, nil
	}

	if err := t.checkBudget(ctx, botID, info.epicIssueID); err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			decision.Reason = "预算预检未通过：" + err.Error()
			return decision, nil
		}
		return decision, fmt.Errorf("opsbot: trend budget check for bot %d: %w", botID, err)
	}

	action, targetWeek, dueDate := TrendMergeNextWeek, nextWeek, ""
	if decision.Score >= trendAddNowScore {
		free, err := t.freeDates(ctx, botID, monday, today)
		if err != nil {
			return decision, err
		}
		if len(free) > 0 {
			action, targetWeek, dueDate = TrendAddNow, week, free[0]
		}
	}
	if action == TrendMergeNextWeek {
		free, err := t.freeDates(ctx, botID, nextMonday, "")
		if err != nil {
			return decision, err
		}
		if len(free) == 0 {
			decision.Reason = "本周与下周日历已满，无法插入加更"
			return decision, nil
		}
		targetWeek, dueDate = nextWeek, free[0]
	}

	issueID, err := t.schedule(ctx, info, botID, sig.Title, targetWeek, dueDate)
	if err != nil {
		return decision, err
	}

	decision.Action = action
	decision.Week = targetWeek
	decision.DueDate = dueDate
	decision.IssueID = issueID
	if action == TrendAddNow {
		decision.Reason = "热点相关且日历有空档，插入本周加更"
	} else {
		decision.Reason = "本周无空档或信号温和，并入下周"
	}
	return decision, nil
}

type trendBotInfo struct {
	packID      string
	accountRef  string
	goal        string
	audience    string
	format      string
	epicIssueID int64
	boost       bool
}

func (t *Trend) botContext(ctx context.Context, botID int64) (trendBotInfo, error) {
	info := trendBotInfo{
		packID:     "generic",
		accountRef: fmt.Sprintf("bot_%d", botID),
		goal:       "账号内容",
		audience:   "账号受众",
		format:     content.FormatNote,
		boost:      true,
	}
	if t.Bots == nil {
		return info, nil
	}
	bot, err := t.Bots.GetOpsBot(ctx, botID)
	if err != nil {
		return info, fmt.Errorf("opsbot: load bot %d for trend: %w", botID, err)
	}
	info.packID = fallbackText(strings.TrimSpace(bot.PackID), "generic")
	info.accountRef = fallbackText(strings.TrimSpace(bot.AccountRef), strings.TrimSpace(bot.DisplayName), info.accountRef)
	info.goal = fallbackText(goalText(bot.GoalsJson), info.goal)
	info.audience = fallbackText(audienceText(bot.GoalsJson), info.audience)
	info.format = slotFormat(bot.PackID)
	info.epicIssueID = bot.EpicIssueID.Int64
	if boost, set := cadenceHotSpot(bot.CadenceJson); set {
		info.boost = boost
	}
	return info, nil
}

func (t *Trend) isDuplicate(ctx context.Context, botID int64, topic string, prior []string, since time.Time) (bool, error) {
	for _, prev := range prior {
		if similarTopic(prev, topic) {
			return true, nil
		}
	}
	if t.History == nil {
		return false, nil
	}
	recent, err := t.History.RecentTopics(ctx, botID, since)
	if err != nil {
		return false, fmt.Errorf("opsbot: trend history for bot %d: %w", botID, err)
	}
	for _, prev := range recent {
		if similarTopic(prev, topic) {
			return true, nil
		}
	}
	return false, nil
}

func (t *Trend) checkBudget(ctx context.Context, botID, epicIssueID int64) error {
	if t.Budget == nil {
		return nil
	}
	return t.Budget.Check(ctx, botID, epicIssueID, t.EstimatedCost)
}

func (t *Trend) freeDates(ctx context.Context, botID int64, monday time.Time, notBefore string) ([]string, error) {
	from := monday.Format(calendarDateLayout)
	to := monday.AddDate(0, 0, 6).Format(calendarDateLayout)

	var free []string
	for i := 0; i < 7; i++ {
		date := monday.AddDate(0, 0, i).Format(calendarDateLayout)
		if notBefore != "" && date < notBefore {
			continue
		}
		free = append(free, date)
	}
	if t.Calendar == nil {
		return free, nil
	}

	occupied, err := t.Calendar.OccupiedDates(ctx, botID, from, to)
	if err != nil {
		return nil, fmt.Errorf("opsbot: trend calendar for bot %d: %w", botID, err)
	}
	taken := make(map[string]bool, len(occupied))
	for _, date := range occupied {
		taken[strings.TrimSpace(date)] = true
	}
	open := free[:0]
	for _, date := range free {
		if !taken[date] {
			open = append(open, date)
		}
	}
	return open, nil
}

func (t *Trend) schedule(ctx context.Context, info trendBotInfo, botID int64, topic, week, dueDate string) (int64, error) {
	if t.Planner == nil {
		return 0, nil
	}
	brief, err := t.Planner.Build(ctx, content.BriefInput{
		BotID:      botID,
		PackID:     info.packID,
		AccountRef: info.accountRef,
		Goal:       info.goal,
		Audience:   info.audience,
		Format:     info.format,
		Topic:      topic,
		Trigger:    content.TriggerTrend,
		Week:       week,
		DueDate:    dueDate,
	})
	if err != nil {
		return 0, fmt.Errorf("opsbot: create trend task for bot %d: %w", botID, err)
	}
	return brief.IssueID, nil
}

func (t *Trend) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (s Signal) normalized() Signal {
	s.Title = strings.TrimSpace(s.Title)
	s.Source = strings.ToLower(strings.TrimSpace(s.Source))
	s.URL = strings.TrimSpace(s.URL)
	s.Summary = strings.TrimSpace(s.Summary)
	s.Heat = clampFloat(s.Heat, 0, 100)
	if s.Relevance > 1 {
		s.Relevance = 1
	}
	if s.Relevance < 0 {
		s.Relevance = 0
	}
	if len(s.Fit) > 0 {
		fit := make(map[int64]float64, len(s.Fit))
		for botID, value := range s.Fit {
			fit[botID] = clampFloat(value, 0, 1)
		}
		s.Fit = fit
	}
	return s
}

func sortedFit(fit map[int64]float64) []fitCandidate {
	out := make([]fitCandidate, 0, len(fit))
	for botID, value := range fit {
		if botID <= 0 {
			continue
		}
		out = append(out, fitCandidate{botID: botID, fit: value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].botID < out[j].botID })
	return out
}

// trendScore weighs heat (0-100) against brand relevance and account fit. A
// zero Relevance is treated as fully relevant (the source did not score it).
func trendScore(sig Signal, fit float64) float64 {
	heat := clampFloat(sig.Heat, 0, 100) / 100
	relevance := sig.Relevance
	if relevance <= 0 {
		relevance = 1
	}
	return heat * clampFloat(relevance, 0, 1) * clampFloat(fit, 0, 1)
}

func dedupeSignals(signals []Signal) []Signal {
	byTitle := map[string]int{}
	out := make([]Signal, 0, len(signals))
	for _, sig := range signals {
		key := normalizeTopic(sig.Title)
		if key == "" {
			continue
		}
		if idx, ok := byTitle[key]; ok {
			if sig.Heat > out[idx].Heat {
				out[idx] = sig
			}
			continue
		}
		byTitle[key] = len(out)
		out = append(out, sig)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Heat != out[j].Heat {
			return out[i].Heat > out[j].Heat
		}
		return out[i].Title < out[j].Title
	})
	return out
}

func cadenceHotSpot(raw string) (bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return false, false
	}
	value, ok := fields["hot_spot_boost"]
	if !ok {
		return false, false
	}
	var boost bool
	if err := json.Unmarshal(value, &boost); err != nil {
		return false, false
	}
	return boost, true
}

// normalizeTopic keeps letters and digits only, so spacing and punctuation
// differences do not defeat the same-topic check.
func normalizeTopic(topic string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(topic)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func similarTopic(a, b string) bool {
	left, right := normalizeTopic(a), normalizeTopic(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	if strings.Contains(left, right) || strings.Contains(right, left) {
		shorter := len([]rune(left))
		if n := len([]rune(right)); n < shorter {
			shorter = n
		}
		return shorter >= 2
	}
	return bigramJaccard(left, right) >= 0.8
}

func bigramJaccard(a, b string) float64 {
	left, right := bigrams(a), bigrams(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	inter := 0
	for gram := range left {
		if right[gram] {
			inter++
		}
	}
	union := len(left) + len(right) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func bigrams(value string) map[string]bool {
	runes := []rune(value)
	out := make(map[string]bool, len(runes))
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])] = true
	}
	return out
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func isoWeekString(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// SQLTrendCalendar reads calendar occupancy from the account Epic's children.
type SQLTrendCalendar struct {
	Q *store.Queries
}

var _ TrendCalendar = (*SQLTrendCalendar)(nil)

func (s *SQLTrendCalendar) OccupiedDates(ctx context.Context, botID int64, from, to string) ([]string, error) {
	q, err := trendQueries(s.Q)
	if err != nil {
		return nil, err
	}
	bot, err := q.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !bot.EpicIssueID.Valid {
		return nil, nil
	}
	children, err := q.ListIssuesByParent(ctx, bot.EpicIssueID)
	if err != nil {
		return nil, err
	}
	var dates []string
	for _, issue := range children {
		start, due := issue.StartDate.String, issue.DueDate.String
		if start == "" {
			start = due
		}
		if due == "" {
			due = start
		}
		if start == "" {
			continue
		}
		if to != "" && start > to {
			continue
		}
		if from != "" && due < from {
			continue
		}
		dates = append(dates, start)
	}
	sort.Strings(dates)
	return dates, nil
}

// SQLTrendHistory reads topics the account already covered since a cutoff by
// walking the account Epic's children and their latest briefs.
type SQLTrendHistory struct {
	Q *store.Queries
}

var _ TrendHistory = (*SQLTrendHistory)(nil)

func (s *SQLTrendHistory) RecentTopics(ctx context.Context, botID int64, since time.Time) ([]string, error) {
	q, err := trendQueries(s.Q)
	if err != nil {
		return nil, err
	}
	bot, err := q.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !bot.EpicIssueID.Valid {
		return nil, nil
	}
	children, err := q.ListIssuesByParent(ctx, bot.EpicIssueID)
	if err != nil {
		return nil, err
	}

	cutoff := since.Format(calendarDateLayout)
	seen := map[string]bool{}
	var topics []string
	for _, issue := range children {
		start := issue.StartDate.String
		if start == "" {
			start = issue.DueDate.String
		}
		if start == "" {
			if issue.CreatedAt.Before(since) {
				continue
			}
		} else if start < cutoff {
			continue
		}
		topic := trendIssueTopic(ctx, q, issue)
		if topic == "" {
			continue
		}
		key := normalizeTopic(topic)
		if seen[key] {
			continue
		}
		seen[key] = true
		topics = append(topics, topic)
	}
	return topics, nil
}

func trendIssueTopic(ctx context.Context, q *store.Queries, issue store.Issue) string {
	brief, err := q.GetLatestTaskBrief(ctx, issue.ID)
	if err == nil {
		var scopeIn []string
		if json.Unmarshal([]byte(brief.ScopeInJson), &scopeIn) == nil {
			for _, item := range scopeIn {
				if topic, ok := strings.CutPrefix(item, "topic:"); ok {
					if topic = strings.TrimSpace(topic); topic != "" {
						return topic
					}
				}
			}
		}
	}
	title := strings.TrimSpace(issue.Title)
	if i := strings.LastIndex(title, "·"); i >= 0 {
		return strings.TrimSpace(title[i+len("·"):])
	}
	return title
}

func trendQueries(q *store.Queries) (*store.Queries, error) {
	if q == nil {
		return nil, errors.New("opsbot: trend store has no queries")
	}
	return q, nil
}
