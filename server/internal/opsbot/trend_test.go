package opsbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

func newTestTrend(t *testing.T) *Trend {
	t.Helper()
	return &Trend{
		Now: func() time.Time {
			return time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local) // Wednesday, 2026-W45
		},
	}
}

type fakeTrendSource struct {
	name string
	sigs []Signal
	err  error
}

func (f *fakeTrendSource) Name() string { return f.name }

func (f *fakeTrendSource) Poll(context.Context) ([]Signal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sigs, nil
}

type fakeTrendCalendar struct {
	occupied map[int64][]string
	err      error
	queries  []string
}

func (f *fakeTrendCalendar) OccupiedDates(_ context.Context, botID int64, from, to string) ([]string, error) {
	f.queries = append(f.queries, from+".."+to)
	if f.err != nil {
		return nil, f.err
	}
	return f.occupied[botID], nil
}

type fakeTrendHistory struct {
	topics map[int64][]string
	since  time.Time
	err    error
}

func (f *fakeTrendHistory) RecentTopics(_ context.Context, botID int64, since time.Time) ([]string, error) {
	f.since = since
	if f.err != nil {
		return nil, f.err
	}
	return f.topics[botID], nil
}

type fakeTrendBots struct {
	bots map[int64]store.OpsBot
	err  error
}

func (f *fakeTrendBots) GetOpsBot(_ context.Context, id int64) (store.OpsBot, error) {
	if f.err != nil {
		return store.OpsBot{}, f.err
	}
	bot, ok := f.bots[id]
	if !ok {
		return store.OpsBot{}, ErrNotFound
	}
	return bot, nil
}

type fakeTrendPlanner struct {
	calls []content.BriefInput
	next  int64
	err   error
}

func (f *fakeTrendPlanner) Build(_ context.Context, in content.BriefInput) (content.OpsContentBrief, error) {
	if f.err != nil {
		return content.OpsContentBrief{}, f.err
	}
	f.calls = append(f.calls, in)
	f.next++
	return content.OpsContentBrief{
		Channel: in.PackID, AccountRef: in.AccountRef, Topic: in.Topic,
		Week: in.Week, DueDate: in.DueDate, IssueID: f.next,
	}, nil
}

type fakeTrendBudget struct {
	err  error
	seen int
}

func (f *fakeTrendBudget) Check(context.Context, int64, int64, float64) error {
	f.seen++
	return f.err
}

func TestTrendDecisionAddsOrSkips(t *testing.T) {
	tr := newTestTrend(t)
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	signals := []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
		{Title: "冷门话题", Heat: 10, Fit: map[int64]float64{1: 0.1}},
	}
	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, signals)
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	require.Equal(t, TrendAddNow, decisions[0].Action)
	require.Equal(t, TrendSkip, decisions[1].Action)
	require.Equal(t, int64(1), decisions[0].IssueID)
	require.Len(t, planner.calls, 1, "only the qualifying signal becomes a task")
	require.Equal(t, content.TriggerTrend, planner.calls[0].Trigger)
	require.Equal(t, "2026-W45", planner.calls[0].Week)
}

func TestTrendPollMergesSourcesAndKeepsPartialResults(t *testing.T) {
	tr := newTestTrend(t)
	tr.Sources = []TrendSource{
		&fakeTrendSource{
			name: SignalSourceInfoRadar,
			sigs: []Signal{
				{Title: "秋季护肤", Heat: 60},
				{Title: "通勤穿搭", Heat: 80},
			},
		},
		&fakeTrendSource{
			name: SignalSourceSocialOps,
			sigs: []Signal{
				{Title: "通勤穿搭", Heat: 40},
				{Title: "季节限定", Heat: 70},
			},
		},
		&fakeTrendSource{name: SignalSourceDataSource, err: errors.New("radar offline")},
	}
	tr.Manual = []Signal{{Title: "手动选题", Heat: 50}}

	signals, err := tr.Poll(context.Background())
	require.Error(t, err, "a failed source is reported")
	require.ErrorContains(t, err, "data_source")
	require.Len(t, signals, 4, "duplicate titles collapse, the rest survive")

	require.Equal(t, "通勤穿搭", signals[0].Title)
	require.Equal(t, float64(80), signals[0].Heat, "highest heat wins a duplicate")
	require.Equal(t, SignalSourceInfoRadar, signals[0].Source)

	sources := map[string]bool{}
	for _, sig := range signals {
		sources[sig.Source] = true
	}
	require.True(t, sources[SignalSourceInfoRadar])
	require.True(t, sources[SignalSourceSocialOps])
	require.True(t, sources[SignalSourceManual])
}

func TestTrendInsertAvoidsFixedCadenceDays(t *testing.T) {
	tr := newTestTrend(t)
	calendar := &fakeTrendCalendar{occupied: map[int64][]string{
		1: {"2026-11-02", "2026-11-03", "2026-11-04"},
	}}
	planner := &fakeTrendPlanner{}
	tr.Calendar = calendar
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, TrendAddNow, decisions[0].Action)
	require.Equal(t, "2026-11-05", decisions[0].DueDate, "first free day after the fixed slots")
	require.Equal(t, "2026-W45", decisions[0].Week)
	require.Len(t, planner.calls, 1)
	require.Equal(t, "2026-11-05", planner.calls[0].DueDate)
}

func TestTrendMergesModerateSignalNextWeek(t *testing.T) {
	tr := newTestTrend(t)
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "换季穿搭", Heat: 30, Fit: map[int64]float64{1: 0.5}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendMergeNextWeek, decisions[0].Action)
	require.Equal(t, "2026-W46", decisions[0].Week)
	require.Equal(t, "2026-11-09", decisions[0].DueDate)
	require.Len(t, planner.calls, 1)
}

func TestTrendMergesNextWeekWhenThisWeekIsFull(t *testing.T) {
	tr := newTestTrend(t)
	tr.Calendar = &fakeTrendCalendar{occupied: map[int64][]string{
		1: {"2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06", "2026-11-07", "2026-11-08"},
	}}
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendMergeNextWeek, decisions[0].Action)
	require.Equal(t, "2026-W46", decisions[0].Week)
	require.Equal(t, "2026-11-09", decisions[0].DueDate)
	require.Equal(t, content.TriggerTrend, planner.calls[0].Trigger)
}

func TestTrendSkipsWhenBothWeeksAreFull(t *testing.T) {
	tr := newTestTrend(t)
	tr.Calendar = &fakeTrendCalendar{occupied: map[int64][]string{
		1: {
			"2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06", "2026-11-07", "2026-11-08",
			"2026-11-09", "2026-11-10", "2026-11-11", "2026-11-12", "2026-11-13", "2026-11-14", "2026-11-15",
		},
	}}
	tr.Planner = &fakeTrendPlanner{}

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendSkip, decisions[0].Action)
	require.Contains(t, decisions[0].Reason, "日历已满")
}

func TestTrendDedupesSameTopicWithinSevenDays(t *testing.T) {
	tr := newTestTrend(t)
	history := &fakeTrendHistory{topics: map[int64][]string{
		1: {"秋季护肤三步"},
	}}
	tr.History = history
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendSkip, decisions[0].Action)
	require.Contains(t, decisions[0].Reason, "同主题")
	require.Empty(t, planner.calls, "a duplicate must not create a task")
	require.Equal(t, time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local), history.since)
}

func TestTrendDedupesRepeatedSignalInOneEvaluation(t *testing.T) {
	tr := newTestTrend(t)
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
		{Title: "秋季护肤 3 步", Heat: 85, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendAddNow, decisions[0].Action)
	require.Equal(t, TrendSkip, decisions[1].Action)
	require.Len(t, planner.calls, 1)
}

func TestTrendRespectsHotSpotBoostSetting(t *testing.T) {
	tr := newTestTrend(t)
	tr.Bots = &fakeTrendBots{bots: map[int64]store.OpsBot{
		1: {
			ID: 1, Role: RoleAccount, PackID: "xiaohongshu",
			CadenceJson: `{"posts_per_week":2,"hot_spot_boost":false}`,
		},
	}}
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendSkip, decisions[0].Action)
	require.Contains(t, decisions[0].Reason, "未开启热点加更")
	require.Empty(t, planner.calls)
}

func TestTrendBudgetPrecheckBlocksAdd(t *testing.T) {
	tr := newTestTrend(t)
	budget := &fakeTrendBudget{err: ErrBudgetExceeded}
	planner := &fakeTrendPlanner{}
	tr.Budget = budget
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendSkip, decisions[0].Action)
	require.Contains(t, decisions[0].Reason, "预算")
	require.Equal(t, 1, budget.seen)
	require.Empty(t, planner.calls)
}

func TestTrendUsesBotContextForBrief(t *testing.T) {
	tr := newTestTrend(t)
	tr.Bots = &fakeTrendBots{bots: map[int64]store.OpsBot{
		1: {
			ID: 1, Role: RoleAccount, PackID: "wechat_mp", AccountRef: "wx_main",
			DisplayName: "主号", EpicIssueID: nullInt64(901),
			GoalsJson:   `{"type":"brand","text":"护肤知识科普","audience":"25-35 女性"}`,
			CadenceJson: `{"posts_per_week":2,"hot_spot_boost":true}`,
		},
	}}
	planner := &fakeTrendPlanner{}
	tr.Planner = planner

	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: RoleDirector}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.NoError(t, err)
	require.Equal(t, TrendAddNow, decisions[0].Action)
	require.Len(t, planner.calls, 1)
	call := planner.calls[0]
	require.Equal(t, "wechat_mp", call.PackID)
	require.Equal(t, "wx_main", call.AccountRef)
	require.Equal(t, "护肤知识科普", call.Goal)
	require.Equal(t, "25-35 女性", call.Audience)
	require.Equal(t, content.FormatArticle, call.Format)
}

func TestTrendRejectsNonDirectorEvaluation(t *testing.T) {
	tr := newTestTrend(t)
	_, err := tr.Evaluate(context.Background(), BotRef{ID: 1, Role: RoleAccount}, []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestTrendSimilarTopic(t *testing.T) {
	require.True(t, similarTopic("秋季护肤", "秋季护肤 3 步"))
	require.True(t, similarTopic("秋季护肤三步", "秋季护肤三步！"))
	require.False(t, similarTopic("秋季护肤", "通勤穿搭"))
}
