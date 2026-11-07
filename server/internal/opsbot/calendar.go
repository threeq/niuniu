package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/store"
)

const calendarDateLayout = "2006-01-02"

var (
	// ErrInvalidWeek is returned for a week that is not YYYY-Www or names an
	// ISO week that does not exist in its year.
	ErrInvalidWeek = errors.New("opsbot: invalid ISO week")
	// ErrInvalidDateRange is returned for a malformed or reversed date range.
	ErrInvalidDateRange = errors.New("opsbot: invalid date range")
)

// BotRef is the lightweight reference the ops-bot components accept. ID is the
// only required field; PackID, Role and Cadence are optional overrides for
// callers that already hold the bot row (tests and orchestrators) and would
// otherwise force a store round-trip. An empty override falls back to the
// persisted bot.
type BotRef struct {
	ID      int64
	PackID  string
	Role    string
	Cadence string
}

// CalendarBotStore loads the bot a calendar operation is scoped to.
type CalendarBotStore interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
}

// WeeklyPlanner materialises one content task (issue + brief) under the
// account Epic. *content.BriefBuilder satisfies it; tests inject a fake.
type WeeklyPlanner interface {
	Build(ctx context.Context, in content.BriefInput) (content.OpsContentBrief, error)
}

// CalendarStore reads a bot-owner's scheduled content issues and rewrites an
// issue's dates for manual rescheduling.
type CalendarStore interface {
	// ListCalendarEntries returns every dated content issue belonging to the
	// owner's account bots, across all channels. Filtering happens in Calendar.
	ListCalendarEntries(ctx context.Context, ownerType string, ownerID int64) ([]CalendarEntry, error)
	// ScheduleIssue sets an issue's start/due dates without recreating it.
	ScheduleIssue(ctx context.Context, issueID int64, startDate, dueDate string) error
}

// CalendarEntry is one scheduled content task on the calendar view.
type CalendarEntry struct {
	IssueID    int64  `json:"issue_id"`
	BotID      int64  `json:"bot_id"`
	BotName    string `json:"bot_name"`
	Channel    string `json:"channel"`
	AccountRef string `json:"account_ref"`
	Title      string `json:"title"`
	StartDate  string `json:"start_date"`
	DueDate    string `json:"due_date"`
	Status     string `json:"status"`
}

// CalendarFilter narrows a calendar query. All fields are optional except the
// owner; an empty From/To means unbounded in that direction.
type CalendarFilter struct {
	OwnerType string
	OwnerID   int64
	BotIDs    []int64
	Channel   string
	From      string
	To        string
}

// Calendar turns the fixed cadence into content tasks and reads the resulting
// content calendar back. The calendar is a view over issues (start_date /
// due_date under the account Epic), not a table of its own.
type Calendar struct {
	Bots    CalendarBotStore
	Planner WeeklyPlanner
	Store   CalendarStore
	// Now overrides the clock in tests.
	Now func() time.Time

	mu sync.Mutex
}

var _ CalendarStore = (*SQLCalendarStore)(nil)

// PlanWeek creates the content tasks a bot owes for one ISO week, derived from
// its cadence_json.weekly (or posts_per_week, the onboarding key). Slots that
// already exist for the same account and week are skipped, so repeated calls
// are idempotent; only newly created issue ids are returned.
func (c *Calendar) PlanWeek(ctx context.Context, ref BotRef, week string) ([]int64, error) {
	if c.Bots == nil || c.Planner == nil || c.Store == nil {
		return nil, errors.New("opsbot: calendar needs bots, planner and store")
	}
	if ref.ID <= 0 {
		return nil, fmt.Errorf("%w: bot id is required", ErrInvalidInput)
	}
	monday, _, ok := weekBounds(week)
	if !ok {
		return nil, fmt.Errorf("%w: %q must be YYYY-Www", ErrInvalidWeek, week)
	}

	bot, err := c.Bots.GetOpsBot(ctx, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("opsbot: load bot %d for planning: %w", ref.ID, err)
	}
	role := strings.TrimSpace(ref.Role)
	if role == "" {
		role = bot.Role
	}
	if role == RoleDirector {
		return nil, fmt.Errorf("%w: bot %d is a director; plan per account bot", ErrInvalidInput, ref.ID)
	}

	cadence := strings.TrimSpace(ref.Cadence)
	if cadence == "" {
		cadence = bot.CadenceJson
	}
	slots, err := weeklySlots(cadence)
	if err != nil {
		return nil, fmt.Errorf("opsbot: bot %d: %w", ref.ID, err)
	}
	if slots == 0 {
		return nil, nil
	}

	pack := strings.TrimSpace(ref.PackID)
	if pack == "" {
		pack = bot.PackID
	}
	accountRef := strings.TrimSpace(bot.AccountRef)
	if accountRef == "" {
		accountRef = strings.TrimSpace(bot.DisplayName)
	}
	goal := goalText(bot.GoalsJson)
	if goal == "" {
		goal = fallbackText(bot.DisplayName, bot.AccountRef, "账号内容")
	}
	audience := audienceText(bot.GoalsJson)
	if audience == "" {
		audience = fallbackText(bot.DisplayName, bot.AccountRef, "账号受众")
	}

	// Serialise the plan so two concurrent scheduler ticks cannot both observe
	// a slot as missing and create it twice.
	c.mu.Lock()
	defer c.mu.Unlock()

	var created []int64
	for i := 0; i < slots; i++ {
		startDate, dueDate := slotDates(monday, slots, i)
		brief, err := c.Planner.Build(ctx, content.BriefInput{
			BotID:      ref.ID,
			PackID:     pack,
			AccountRef: accountRef,
			Goal:       goal,
			Audience:   audience,
			Format:     slotFormat(pack),
			Topic:      slotTopic(goal, i),
			Trigger:    content.TriggerFixed,
			Week:       week,
			DueDate:    dueDate,
		})
		if err != nil {
			return created, fmt.Errorf("opsbot: plan %s slot %d for bot %d: %w", week, i+1, ref.ID, err)
		}
		if brief.Existing {
			continue
		}
		if err := c.Store.ScheduleIssue(ctx, brief.IssueID, startDate, dueDate); err != nil {
			return created, fmt.Errorf("opsbot: schedule issue %d for bot %d: %w", brief.IssueID, ref.ID, err)
		}
		created = append(created, brief.IssueID)
	}
	return created, nil
}

// List returns the calendar entries matching filter, ordered by start date then
// issue id. Account (bot ids), channel (pack id) and date range are all
// optional.
func (c *Calendar) List(ctx context.Context, filter CalendarFilter) ([]CalendarEntry, error) {
	if c.Store == nil {
		return nil, errors.New("opsbot: calendar has no store")
	}
	if filter.OwnerType == "" {
		filter.OwnerType = defaultOwnerType
	}
	if err := validateRange(filter.From, filter.To); err != nil {
		return nil, err
	}

	entries, err := c.Store.ListCalendarEntries(ctx, filter.OwnerType, filter.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("opsbot: list calendar: %w", err)
	}

	botIDs := make(map[int64]bool, len(filter.BotIDs))
	for _, id := range filter.BotIDs {
		botIDs[id] = true
	}
	out := make([]CalendarEntry, 0, len(entries))
	for _, entry := range entries {
		if len(botIDs) > 0 && !botIDs[entry.BotID] {
			continue
		}
		if filter.Channel != "" && entry.Channel != filter.Channel {
			continue
		}
		if !entryOverlaps(entry, filter.From, filter.To) {
			continue
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := entryStart(out[i]), entryStart(out[j])
		if left != right {
			return left < right
		}
		return out[i].IssueID < out[j].IssueID
	})
	return out, nil
}

// Reschedule moves a content task to new dates without recreating it. Empty
// arguments clear that bound; at least one bound is required.
func (c *Calendar) Reschedule(ctx context.Context, issueID int64, startDate, dueDate string) error {
	if c.Store == nil {
		return errors.New("opsbot: calendar has no store")
	}
	if issueID <= 0 {
		return fmt.Errorf("%w: issue id is required", ErrInvalidInput)
	}
	startDate, dueDate = strings.TrimSpace(startDate), strings.TrimSpace(dueDate)
	if startDate == "" && dueDate == "" {
		return fmt.Errorf("%w: start_date or due_date is required", ErrInvalidDateRange)
	}
	if err := validateRange(startDate, dueDate); err != nil {
		return err
	}
	if err := c.Store.ScheduleIssue(ctx, issueID, startDate, dueDate); err != nil {
		return fmt.Errorf("opsbot: reschedule issue %d: %w", issueID, err)
	}
	return nil
}

// SQLCalendarStore reads and writes the calendar through the existing issues
// tables: content tasks are children of the account bot's control Epic.
type SQLCalendarStore struct {
	Q *store.Queries
}

// ListCalendarEntries walks the owner's account bots and their Epic children.
func (s *SQLCalendarStore) ListCalendarEntries(ctx context.Context, ownerType string, ownerID int64) ([]CalendarEntry, error) {
	if s == nil || s.Q == nil {
		return nil, errors.New("opsbot: calendar store has no queries")
	}
	bots, err := s.Q.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
	if err != nil {
		return nil, err
	}
	var out []CalendarEntry
	for _, bot := range bots {
		if bot.Role == RoleDirector || !bot.EpicIssueID.Valid {
			continue
		}
		children, err := s.Q.ListIssuesByParent(ctx, bot.EpicIssueID)
		if err != nil {
			return nil, err
		}
		for _, issue := range children {
			if !issue.StartDate.Valid && !issue.DueDate.Valid {
				continue
			}
			out = append(out, CalendarEntry{
				IssueID:    issue.ID,
				BotID:      bot.ID,
				BotName:    bot.DisplayName,
				Channel:    bot.PackID,
				AccountRef: bot.AccountRef,
				Title:      issue.Title,
				StartDate:  issue.StartDate.String,
				DueDate:    issue.DueDate.String,
				Status:     issue.LifecycleStatus,
			})
		}
	}
	return out, nil
}

// ScheduleIssue rewrites an issue's dates, preserving every other field.
func (s *SQLCalendarStore) ScheduleIssue(ctx context.Context, issueID int64, startDate, dueDate string) error {
	if s == nil || s.Q == nil {
		return errors.New("opsbot: calendar store has no queries")
	}
	issue, err := s.Q.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	_, err = s.Q.UpdateIssue(ctx, store.UpdateIssueParams{
		Title:        issue.Title,
		Description:  issue.Description,
		Priority:     issue.Priority,
		StartDate:    nullStringValue(startDate),
		DueDate:      nullStringValue(dueDate),
		EstimateType: issue.EstimateType,
		Estimate:     issue.Estimate,
		ActualTime:   issue.ActualTime,
		ID:           issueID,
	})
	return err
}

type cadenceSpec struct {
	Weekly       int `json:"weekly"`
	PostsPerWeek int `json:"posts_per_week"`
}

// weeklySlots reads the fixed-cadence count. The spec stores it as "weekly"
// while onboarding writes "posts_per_week"; both are accepted, weekly first.
func weeklySlots(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return 0, nil
	}
	var spec cadenceSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return 0, fmt.Errorf("%w: cadence_json must be a JSON object: %v", ErrInvalidConfig, err)
	}
	slots := spec.Weekly
	if slots <= 0 {
		slots = spec.PostsPerWeek
	}
	if slots < 0 {
		return 0, fmt.Errorf("%w: cadence_json weekly must not be negative", ErrInvalidConfig)
	}
	return slots, nil
}

// goalText picks the human goal out of goals_json. Onboarding writes
// {"type","text"}; hand-written configs may use "goal"/"objective".
func goalText(raw string) string {
	fields := parseObject(raw)
	for _, key := range []string{"goal", "text", "objective"} {
		if s := strings.TrimSpace(fields[key]); s != "" {
			return s
		}
	}
	return ""
}

// audienceText picks the target audience out of goals_json.
func audienceText(raw string) string {
	fields := parseObject(raw)
	for _, key := range []string{"audience", "target"} {
		if s := strings.TrimSpace(fields[key]); s != "" {
			return s
		}
	}
	return ""
}

func parseObject(raw string) map[string]string {
	out := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return out
	}
	for key, value := range generic {
		if s, ok := value.(string); ok {
			out[key] = s
		}
	}
	return out
}

func fallbackText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// slotTopic makes every slot's topic distinct so the brief dedupe key
// (account|week|topic) dedupes per slot, and stable across repeated runs.
func slotTopic(goal string, index int) string {
	return fmt.Sprintf("%s · 第%d篇", goal, index+1)
}

// slotFormat is the platform's default content format for a scheduled slot.
func slotFormat(packID string) string {
	if packID == "wechat_mp" {
		return content.FormatArticle
	}
	return content.FormatNote
}

// weekBounds returns the Monday 00:00 and Sunday of an ISO week.
func weekBounds(week string) (time.Time, time.Time, bool) {
	year, weekNumber, ok := parseWeek(week)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	// Every ISO week 1 contains January 4th.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.Local)
	weekday := int(jan4.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := jan4.AddDate(0, 0, -(weekday-1)).AddDate(0, 0, (weekNumber-1)*7)
	return monday, monday.AddDate(0, 0, 6), true
}

func parseWeek(value string) (int, int, bool) {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, "-W", 2)
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, 0, false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 1 {
		return 0, 0, false
	}
	week, err := strconv.Atoi(parts[1])
	if err != nil || week < 1 || week > weeksInYear(year) {
		return 0, 0, false
	}
	return year, week, true
}

// weeksInYear reports 53 for long ISO years (those starting on Thursday, or
// leap years starting on Wednesday), 52 otherwise.
func weeksInYear(year int) int {
	_, week := time.Date(year, time.December, 28, 0, 0, 0, 0, time.Local).ISOWeek()
	if week == 53 {
		return 53
	}
	return 52
}

// slotDates spreads slots evenly over the week; each task gets a two-day
// window that never crosses into the next week.
func slotDates(monday time.Time, slots, index int) (string, string) {
	start := monday.AddDate(0, 0, index*7/slots)
	due := start.AddDate(0, 0, 2)
	if sunday := monday.AddDate(0, 0, 6); due.After(sunday) {
		due = sunday
	}
	return start.Format(calendarDateLayout), due.Format(calendarDateLayout)
}

// entryOverlaps reports whether an entry's [start,due] window intersects the
// filter's [from,to] window. Missing bounds are open; an entry with no dates is
// filtered out by the store.
func entryOverlaps(entry CalendarEntry, from, to string) bool {
	start, due := entry.StartDate, entry.DueDate
	if start == "" {
		start = due
	}
	if due == "" {
		due = start
	}
	if to != "" && start > to {
		return false
	}
	if from != "" && due < from {
		return false
	}
	return true
}

func entryStart(entry CalendarEntry) string {
	if entry.StartDate != "" {
		return entry.StartDate
	}
	return entry.DueDate
}

func validateRange(from, to string) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from != "" {
		if _, err := time.Parse(calendarDateLayout, from); err != nil {
			return fmt.Errorf("%w: from %q must be YYYY-MM-DD", ErrInvalidDateRange, from)
		}
	}
	if to != "" {
		if _, err := time.Parse(calendarDateLayout, to); err != nil {
			return fmt.Errorf("%w: to %q must be YYYY-MM-DD", ErrInvalidDateRange, to)
		}
	}
	if from != "" && to != "" && from > to {
		return fmt.Errorf("%w: from %s is after to %s", ErrInvalidDateRange, from, to)
	}
	return nil
}

func nullStringValue(value string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
