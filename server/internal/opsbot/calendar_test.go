package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

type fakeCalendarBots struct {
	bots map[int64]store.OpsBot
}

func (f *fakeCalendarBots) GetOpsBot(_ context.Context, id int64) (store.OpsBot, error) {
	bot, ok := f.bots[id]
	if !ok {
		return store.OpsBot{}, errors.New("bot not found")
	}
	return bot, nil
}

type fakePlanner struct {
	next  int64
	seen  map[string]int64
	calls []content.BriefInput
	fail  error
}

func (f *fakePlanner) Build(_ context.Context, in content.BriefInput) (content.OpsContentBrief, error) {
	if f.fail != nil {
		return content.OpsContentBrief{}, f.fail
	}
	f.calls = append(f.calls, in)
	key := in.AccountRef + "|" + in.Week + "|" + in.Topic
	if id, ok := f.seen[key]; ok {
		return content.OpsContentBrief{IssueID: id, Week: in.Week, DueDate: in.DueDate, Existing: true}, nil
	}
	f.next++
	f.seen[key] = f.next
	return content.OpsContentBrief{
		Channel: in.PackID, AccountRef: in.AccountRef, Topic: in.Topic,
		Week: in.Week, DueDate: in.DueDate, IssueID: f.next,
	}, nil
}

type scheduledIssue struct {
	ownerType string
	ownerID   int64
	issueID   int64
	startDate string
	dueDate   string
}

type fakeCalendarStore struct {
	entries   []CalendarEntry
	scheduled []scheduledIssue
	listErr   error
}

func (f *fakeCalendarStore) ListCalendarEntries(_ context.Context, _ string, _ int64) ([]CalendarEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.entries, nil
}

func (f *fakeCalendarStore) ScheduleIssue(_ context.Context, ownerType string, ownerID, issueID int64, startDate, dueDate string) error {
	f.scheduled = append(f.scheduled, scheduledIssue{
		ownerType: ownerType, ownerID: ownerID, issueID: issueID,
		startDate: startDate, dueDate: dueDate,
	})
	return nil
}

type calendarFixture struct {
	calendar *Calendar
	bots     *fakeCalendarBots
	planner  *fakePlanner
	store    *fakeCalendarStore
}

func newCalendarFixture(t *testing.T) *calendarFixture {
	t.Helper()
	bots := &fakeCalendarBots{bots: map[int64]store.OpsBot{
		1: {
			ID:          1,
			OwnerType:   defaultOwnerType,
			OwnerID:     7,
			Role:        RoleAccount,
			PackID:      "xiaohongshu",
			AccountRef:  "brand_xhs",
			DisplayName: "品牌号",
			GoalsJson:   `{"type":"brand","text":"护肤知识科普","audience":"25-35 女性"}`,
			CadenceJson: `{"posts_per_week":2,"hot_spot_boost":false,"text":"每周2篇"}`,
			Status:      StatusActive,
		},
		2: {
			ID:          2,
			OwnerType:   defaultOwnerType,
			OwnerID:     7,
			Role:        RoleDirector,
			DisplayName: "运营总监",
			CadenceJson: `{"weekly":1}`,
			Status:      StatusActive,
		},
	}}
	planner := &fakePlanner{seen: map[string]int64{}}
	calendarStore := &fakeCalendarStore{}
	return &calendarFixture{
		calendar: &Calendar{Bots: bots, Planner: planner, Store: calendarStore},
		bots:     bots,
		planner:  planner,
		store:    calendarStore,
	}
}

func newTestCalendar(t *testing.T) *Calendar {
	t.Helper()
	return newCalendarFixture(t).calendar
}

func TestWeeklyPlanCreatesIssuesAndDedupes(t *testing.T) {
	c := newTestCalendar(t)
	ctx := context.Background()
	ids, err := c.PlanWeek(ctx, BotRef{ID: 1, Cadence: `{"weekly":2}`}, "2026-W45")
	require.NoError(t, err)
	require.Len(t, ids, 2)
	again, err := c.PlanWeek(ctx, BotRef{ID: 1, Cadence: `{"weekly":2}`}, "2026-W45")
	require.NoError(t, err)
	require.Empty(t, again, "same week must not duplicate")
}

func TestWeeklyPlanReadsPersistedCadenceAndSpreadsDates(t *testing.T) {
	fixture := newCalendarFixture(t)
	ctx := context.Background()

	ids, err := fixture.calendar.PlanWeek(ctx, BotRef{ID: 1}, "2026-W45")
	require.NoError(t, err)
	require.Len(t, ids, 2, "onboarding writes posts_per_week")

	// ISO week 2026-W45 is Mon 2026-11-02 .. Sun 2026-11-08.
	require.Len(t, fixture.store.scheduled, 2)
	first, second := fixture.store.scheduled[0], fixture.store.scheduled[1]
	require.Equal(t, defaultOwnerType, first.ownerType, "scheduling is owner-scoped")
	require.Equal(t, int64(7), first.ownerID)
	require.Equal(t, "2026-11-02", first.startDate)
	require.Equal(t, "2026-11-04", first.dueDate)
	require.Equal(t, "2026-11-05", second.startDate)
	require.Equal(t, "2026-11-07", second.dueDate)

	firstCall := fixture.planner.calls[0]
	require.Equal(t, int64(1), firstCall.BotID)
	require.Equal(t, "xiaohongshu", firstCall.PackID)
	require.Equal(t, "brand_xhs", firstCall.AccountRef)
	require.Equal(t, "护肤知识科普", firstCall.Goal)
	require.Equal(t, "25-35 女性", firstCall.Audience)
	require.Equal(t, content.FormatNote, firstCall.Format)
	require.Equal(t, content.TriggerFixed, firstCall.Trigger)
	require.Equal(t, "2026-W45", firstCall.Week)
	require.Equal(t, "护肤知识科普 · 第1篇", firstCall.Topic)
	require.Equal(t, "护肤知识科普 · 第2篇", fixture.planner.calls[1].Topic)
}

func TestWeeklyPlanNoCadenceIsNoop(t *testing.T) {
	fixture := newCalendarFixture(t)
	fixture.bots.bots[1] = store.OpsBot{ID: 1, Role: RoleAccount, CadenceJson: "{}"}

	ids, err := fixture.calendar.PlanWeek(context.Background(), BotRef{ID: 1}, "2026-W45")
	require.NoError(t, err)
	require.Empty(t, ids)
	require.Empty(t, fixture.planner.calls)
}

func TestWeeklyPlanRejectsDirectorAndBadInput(t *testing.T) {
	c := newTestCalendar(t)
	ctx := context.Background()

	_, err := c.PlanWeek(ctx, BotRef{ID: 2}, "2026-W45")
	require.ErrorIs(t, err, ErrInvalidInput, "a director has no publishing account")

	_, err = c.PlanWeek(ctx, BotRef{ID: 1}, "2026-45")
	require.ErrorIs(t, err, ErrInvalidWeek)

	_, err = c.PlanWeek(ctx, BotRef{ID: 1}, "2026-W54")
	require.ErrorIs(t, err, ErrInvalidWeek)

	_, err = c.PlanWeek(ctx, BotRef{}, "2026-W45")
	require.ErrorIs(t, err, ErrInvalidInput)

	_, err = c.PlanWeek(ctx, BotRef{ID: 99}, "2026-W45")
	require.Error(t, err)
}

func TestWeeklySlotsAcceptsBothCadenceKeys(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`{"weekly":3}`, 3},
		{`{"posts_per_week":1,"hot_spot_boost":true}`, 1},
		{`{"weekly":2,"posts_per_week":5}`, 2},
		{`{}`, 0},
		{``, 0},
	}
	for _, tc := range cases {
		got, err := weeklySlots(tc.raw)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
	}
	_, err := weeklySlots(`not-json`)
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestCalendarListFiltersByAccountChannelAndRange(t *testing.T) {
	fixture := newCalendarFixture(t)
	fixture.store.entries = []CalendarEntry{
		{IssueID: 30, BotID: 2, Channel: "wechat_mp", StartDate: "2026-11-09", DueDate: "2026-11-11"},
		{IssueID: 20, BotID: 1, Channel: "xiaohongshu", StartDate: "2026-11-04", DueDate: "2026-11-06"},
		{IssueID: 10, BotID: 1, Channel: "xiaohongshu", StartDate: "2026-11-02", DueDate: "2026-11-04"},
		{IssueID: 40, BotID: 1, Channel: "xiaohongshu", DueDate: "2026-11-20"},
	}
	ctx := context.Background()

	all, err := fixture.calendar.List(ctx, CalendarFilter{OwnerID: 7})
	require.NoError(t, err)
	require.Equal(t, []int64{10, 20, 30, 40}, issueIDs(all), "ordered by start date")

	byBot, err := fixture.calendar.List(ctx, CalendarFilter{OwnerID: 7, BotIDs: []int64{1}})
	require.NoError(t, err)
	require.Equal(t, []int64{10, 20, 40}, issueIDs(byBot))

	byChannel, err := fixture.calendar.List(ctx, CalendarFilter{OwnerID: 7, Channel: "wechat_mp"})
	require.NoError(t, err)
	require.Equal(t, []int64{30}, issueIDs(byChannel))

	inRange, err := fixture.calendar.List(ctx, CalendarFilter{OwnerID: 7, From: "2026-11-05", To: "2026-11-10"})
	require.NoError(t, err)
	require.Equal(t, []int64{20, 30}, issueIDs(inRange), "overlapping windows included, others excluded")
}

func TestCalendarListRejectsBadRange(t *testing.T) {
	c := newTestCalendar(t)
	_, err := c.List(context.Background(), CalendarFilter{OwnerID: 7, From: "2026-11-10", To: "2026-11-01"})
	require.ErrorIs(t, err, ErrInvalidDateRange)
	_, err = c.List(context.Background(), CalendarFilter{OwnerID: 7, From: "11/05/2026"})
	require.ErrorIs(t, err, ErrInvalidDateRange)
}

func TestCalendarRescheduleUpdatesDates(t *testing.T) {
	fixture := newCalendarFixture(t)
	ctx := context.Background()
	owner := OwnerRef{Type: defaultOwnerType, ID: 7}

	require.NoError(t, fixture.calendar.Reschedule(ctx, owner, 42, "2026-12-01", "2026-12-03"))
	require.Equal(t, []scheduledIssue{{
		ownerType: defaultOwnerType, ownerID: 7, issueID: 42,
		startDate: "2026-12-01", dueDate: "2026-12-03",
	}}, fixture.store.scheduled)

	require.ErrorIs(t, fixture.calendar.Reschedule(ctx, owner, 42, "", ""), ErrInvalidDateRange)
	require.ErrorIs(t, fixture.calendar.Reschedule(ctx, owner, 42, "2026-12-05", "2026-12-01"), ErrInvalidDateRange)
	require.ErrorIs(t, fixture.calendar.Reschedule(ctx, owner, 0, "2026-12-01", ""), ErrInvalidInput)
	require.ErrorIs(t, fixture.calendar.Reschedule(ctx, OwnerRef{}, 42, "2026-12-01", ""), ErrInvalidInput,
		"owner scope is mandatory")
	require.Len(t, fixture.store.scheduled, 1, "rejected calls must not reach the store")
}

func TestOwnsContentEpicScopesToOwnerAccountBots(t *testing.T) {
	bots := []store.OpsBot{
		{ID: 1, Role: RoleAccount, EpicIssueID: sqlNullInt64(100)},
		{ID: 2, Role: RoleDirector, EpicIssueID: sqlNullInt64(200)},
		{ID: 3, Role: RoleAccount}, // never provisioned
	}
	require.True(t, ownsContentEpic(bots, 100))
	require.False(t, ownsContentEpic(bots, 200), "a director epic is not a content calendar")
	require.False(t, ownsContentEpic(bots, 0))
	require.False(t, ownsContentEpic(bots, 999), "another owner's epic is rejected")
	require.False(t, ownsContentEpic(nil, 100))
}

func sqlNullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v > 0}
}

func TestWeekBounds(t *testing.T) {
	monday, sunday, ok := weekBounds("2026-W45")
	require.True(t, ok)
	require.Equal(t, "2026-11-02", monday.Format(calendarDateLayout))
	require.Equal(t, "2026-11-08", sunday.Format(calendarDateLayout))
	require.Equal(t, time.Monday, monday.Weekday())

	monday, sunday, ok = weekBounds("2025-W01")
	require.True(t, ok)
	require.Equal(t, "2024-12-30", monday.Format(calendarDateLayout))
	require.Equal(t, "2025-01-05", sunday.Format(calendarDateLayout))
}

func issueIDs(entries []CalendarEntry) []int64 {
	ids := make([]int64, len(entries))
	for i, entry := range entries {
		ids[i] = entry.IssueID
	}
	return ids
}
