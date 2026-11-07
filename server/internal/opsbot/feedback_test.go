package opsbot

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type feedbackBotGetter struct {
	bots map[int64]store.OpsBot
}

func (f feedbackBotGetter) GetOpsBot(_ context.Context, id int64) (store.OpsBot, error) {
	bot, ok := f.bots[id]
	if !ok {
		return store.OpsBot{}, sql.ErrNoRows
	}
	return bot, nil
}

// fakeHitKnowledge is the in-memory knowledge/memory sink. Search mimics a
// topic-scoped knowledge lookup: an entry matches when its content mentions
// the topic, which is how the next brief finds the previous week's hits.
type fakeHitKnowledge struct {
	written bool
	entries []HitKnowledgeEntry
}

func (f *fakeHitKnowledge) Store(_ context.Context, _ int64, entries []HitKnowledgeEntry) error {
	f.written = true
	f.entries = append([]HitKnowledgeEntry(nil), entries...)
	return nil
}

func (f *fakeHitKnowledge) Search(_ context.Context, _ int64, topic string) ([]string, error) {
	var refs []string
	for _, e := range f.entries {
		if topic == "" || strings.Contains(e.Content, topic) {
			refs = append(refs, e.Refs...)
		}
	}
	return refs, nil
}

func feedbackNow() time.Time {
	return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
}

func newTestFeedback(t *testing.T) *Feedback {
	t.Helper()
	now := feedbackNow()
	published := []PublishedContent{
		{
			IssueID: 101, Title: "3步搞定秋季护肤", Cover: "大字标题+暖色",
			Structure: "痛点开场→3步方法→CTA", TopicType: "教程",
			PublishedAt: time.Date(2026, 11, 2, 20, 30, 0, 0, time.UTC),
			Views:       1000, Interactions: 120, Followers: 80, Conversions: 20,
		},
		{
			IssueID: 102, Title: "通勤穿搭清单", Cover: "真人出镜",
			Structure: "清单体", TopicType: "清单",
			PublishedAt: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
			Views:       500, Interactions: 50, Followers: 30, Conversions: 5,
		},
		{
			IssueID: 103, Title: "周末探店vlog", Cover: "生活感随手拍",
			Structure: "vlog叙事", TopicType: "vlog",
			PublishedAt: time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC),
			Views:       100, Interactions: 5, Followers: 10, Conversions: 1,
		},
	}
	return &Feedback{
		Bots: feedbackBotGetter{bots: map[int64]store.OpsBot{
			1: {
				ID: 1, Role: RoleAccount, DisplayName: "护肤号", OwnerType: "user", OwnerID: 2,
				GoalsJson: `{"followers":100,"conversion":50,"brand":"品牌认知提升"}`,
			},
		}},
		Published: func(context.Context, int64, string, string) ([]PublishedContent, error) {
			return published, nil
		},
		Spend: func(context.Context, int64, string, string) (SpendBreakdown, error) {
			return SpendBreakdown{
				Total: 320.5, Currency: "CNY",
				Items: []UsageItem{{Category: "text", Unit: "token", Quantity: 1200}},
			}, nil
		},
		Approvals: func(context.Context, int64, string, string) ([]ApprovalOutcome, error) {
			return []ApprovalOutcome{
				{Type: ApprovalPublish, Status: ApprovalStatusAnswered, Action: ActionApproved,
					RequestedAt: now.Add(-5 * time.Hour), DecidedAt: now.Add(-4 * time.Hour)},
				{Type: ApprovalPublish, Status: ApprovalStatusAnswered, Action: ActionApproved,
					RequestedAt: now.Add(-3 * time.Hour), DecidedAt: now.Add(-2 * time.Hour)},
				{Type: ApprovalReply, Status: ApprovalStatusAnswered, Action: ActionRejected,
					RequestedAt: now.Add(-2 * time.Hour), DecidedAt: now.Add(-1 * time.Hour)},
				{Type: ApprovalVideo, Status: ApprovalStatusTimeout},
			}, nil
		},
		Risks: func(context.Context, int64, string, string) ([]RiskEvent, error) {
			return []RiskEvent{
				{BotID: 1, Kind: "rate_limited", Detail: "触发平台限流"},
				{BotID: 1, Kind: "captcha", Detail: "发布前出现验证码"},
			}, nil
		},
		Knowledge: &fakeHitKnowledge{},
		Now:       feedbackNow,
	}
}

func TestWeeklyReportAndBreakdown(t *testing.T) {
	f := newTestFeedback(t)
	ctx := context.Background()
	bot := BotRef{ID: 1}

	rep, err := f.WeeklyReport(ctx, bot, "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.Equal(t, int64(1), rep.BotID)
	require.Equal(t, RoleAccount, rep.Role)
	require.Equal(t, 7, rep.Days)
	require.Equal(t, 3, rep.Published)

	// Metrics are derived from the published pieces when no Metrics collector
	// is wired, so a bare Feedback still produces a complete report.
	require.InDelta(t, 1600.0, rep.Views, 1e-9)
	require.InDelta(t, 1600.0/3, rep.AvgViews, 1e-9)
	require.InDelta(t, 175.0, rep.Interactions, 1e-9)
	require.InDelta(t, 175.0/1600.0, rep.InteractionRate, 1e-9)
	require.InDelta(t, 120.0, rep.Followers, 1e-9)
	require.InDelta(t, 26.0, rep.Conversions, 1e-9)
	require.InDelta(t, 26.0/1600.0, rep.ConversionRate, 1e-9)

	require.InDelta(t, 320.5, rep.Budget.Total, 1e-9)
	require.Equal(t, "CNY", rep.Budget.Currency)
	require.Len(t, rep.Budget.Items, 1)

	require.Equal(t, 4, rep.Approvals.Total)
	require.Equal(t, 3, rep.Approvals.Answered)
	require.Equal(t, 2, rep.Approvals.Approved)
	require.Equal(t, 1, rep.Approvals.Rejected)
	require.Equal(t, 1, rep.Approvals.TimedOut)
	require.InDelta(t, 2.0/3.0, rep.Approvals.PassRate, 1e-9)
	require.InDelta(t, 3600.0, rep.Approvals.AvgDecisionSeconds, 1e-9)

	require.Equal(t, 2, rep.RiskCount)
	require.Len(t, rep.Risks, 2)

	// Goal achievement is computed from goals_json: numeric goals produce a
	// ratio, a non-numeric brand goal is carried through as text.
	require.Equal(t, "品牌认知提升", rep.BrandGoal)
	require.InDelta(t, 100.0, rep.Goals["followers"], 1e-9)
	require.InDelta(t, 1.2, rep.Achievement["followers"], 1e-9)
	require.InDelta(t, 0.52, rep.Achievement["conversion"], 1e-9)
	require.InDelta(t, 0.86, rep.OverallAchievement, 1e-9)

	hits, err := f.Breakdown(ctx, bot, "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, int64(101), hits[0].IssueID, "highest composite score ranks first")
	require.Equal(t, 1, hits[0].Rank)
	require.Equal(t, 1, hits[0].RankByViews)
	require.Equal(t, 1, hits[0].RankByInteractions)
	require.Equal(t, 1, hits[0].RankByFollowers)
	require.Equal(t, "20:00-22:00", hits[0].PublishSlot)
	require.NotEmpty(t, hits[0].Elements)
	require.Contains(t, strings.Join(hits[0].Elements, "\n"), "标题含数字")
	require.Contains(t, strings.Join(hits[0].Elements, "\n"), "封面：大字标题+暖色")
	require.NotEmpty(t, hits.Reusable())
	require.Contains(t, strings.Join(hits.Reusable(), "\n"), "可复用选题类型：教程")

	require.NoError(t, f.WriteBack(ctx, bot, hits))
	kb := f.Knowledge.(*fakeHitKnowledge)
	require.True(t, kb.written)
	require.NotEmpty(t, kb.entries)

	refs, err := f.KnowledgeRefs(ctx, 1, "教程")
	require.NoError(t, err)
	require.Contains(t, refs, "爆款要素：标题含数字")
}

func TestWeeklyReportUsesMetricsSummary(t *testing.T) {
	f := newTestFeedback(t)
	metricsStore := newFakeMetricsStore()
	metricsStore.seed(1, "2026-11-01",
		`{"views":1000,"likes":10,"comments":5,"shares":5,"followers":30}`, "api")
	metricsStore.seed(1, "2026-11-02",
		`{"views":500,"likes":5,"comments":5,"shares":5,"followers":10}`, "api")
	f.Metrics = &Metrics{Store: metricsStore, Bots: fakeBotGetter{goals: `{"followers":100}`}}
	f.Published = func(context.Context, int64, string, string) ([]PublishedContent, error) {
		return []PublishedContent{{IssueID: 1, Title: "t", Views: 1}}, nil
	}

	rep, err := f.WeeklyReport(context.Background(), BotRef{ID: 1}, "2026-11-01", "2026-11-02")
	require.NoError(t, err)
	require.Equal(t, 1, rep.Published, "published volume still comes from the publish source")
	require.InDelta(t, 1500.0, rep.Views, 1e-9)
	// Interaction keys fall back to likes+comments+shares when the platform
	// exposes no unified interactions total.
	require.InDelta(t, 35.0, rep.Interactions, 1e-9)
	require.InDelta(t, 40.0, rep.Followers, 1e-9)
	require.InDelta(t, 0.4, rep.Achievement["followers"], 1e-9)
	require.InDelta(t, 0.4, rep.OverallAchievement, 1e-9)
}

func TestWriteBackFeedsNextBrief(t *testing.T) {
	f := newTestFeedback(t)
	ctx := context.Background()

	hits, err := f.Breakdown(ctx, BotRef{ID: 1}, "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.NoError(t, f.WriteBack(ctx, BotRef{ID: 1}, hits))

	// The next brief pulls its SourceRefs from the same knowledge sink, which
	// is how last week's hit elements reappear in the following round.
	builder := &content.BriefBuilder{
		Knowledge: func(ctx context.Context, topic string) ([]string, error) {
			return f.KnowledgeRefs(ctx, 1, topic)
		},
		Trends: func(context.Context, string) ([]string, error) {
			return []string{"秋季护肤"}, nil
		},
		Store: feedbackBriefStore{},
		Now:   feedbackNow,
	}
	brief, err := builder.Build(ctx, content.BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: content.FormatNote, Topic: "教程",
	})
	require.NoError(t, err)
	require.Contains(t, brief.SourceRefs, "爆款要素：标题含数字")
	require.Contains(t, brief.SourceRefs, "秋季护肤")
}

func TestDirectorWeeklyReportRollsUpAccounts(t *testing.T) {
	f := newTestFeedback(t)
	f.Bots = feedbackBotGetter{bots: map[int64]store.OpsBot{
		9: {ID: 9, Role: RoleDirector, DisplayName: "总监", OwnerType: "user", OwnerID: 2},
		1: {ID: 1, Role: RoleAccount, DisplayName: "护肤号", OwnerType: "user", OwnerID: 2, GoalsJson: `{"followers":100}`},
		2: {ID: 2, Role: RoleAccount, DisplayName: "穿搭号", OwnerType: "user", OwnerID: 2, GoalsJson: `{"followers":50}`},
	}}
	f.Accounts = func(context.Context, int64) ([]int64, error) { return []int64{1, 2}, nil }
	f.HitTopN = 10
	byBot := map[int64][]PublishedContent{
		1: {
			{IssueID: 201, Title: "秋季护肤三步", TopicType: "教程", Views: 900, Interactions: 90, Followers: 60},
			{IssueID: 202, Title: "通勤穿搭", TopicType: "穿搭", Views: 400, Interactions: 30, Followers: 20},
		},
		2: {
			{IssueID: 203, Title: "秋季护肤3步", TopicType: "教程", Views: 700, Interactions: 70, Followers: 40},
			{IssueID: 204, Title: "周末探店", TopicType: "探店", Views: 300, Interactions: 20, Followers: 10},
		},
	}
	f.Published = func(_ context.Context, botID int64, _, _ string) ([]PublishedContent, error) {
		return byBot[botID], nil
	}

	rep, err := f.WeeklyReport(context.Background(), BotRef{ID: 9}, "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.Equal(t, RoleDirector, rep.Role)
	require.Equal(t, 4, rep.Published)
	require.InDelta(t, 2300.0, rep.Views, 1e-9)
	require.NotNil(t, rep.Director)
	require.Equal(t, 2, rep.Director.Accounts)
	require.Equal(t, 4, rep.Director.Hits)
	require.InDelta(t, 1.0, rep.Director.HitRate, 1e-9)
	require.InDelta(t, 0.5, rep.Director.TopicReuseRate, 1e-9)
	require.InDelta(t, 0.5, rep.Director.HomogeneousRatio, 1e-9)
	require.Len(t, rep.Director.Detail, 2)
}

func TestFeedbackRejectsInvalidInput(t *testing.T) {
	f := newTestFeedback(t)
	ctx := context.Background()

	_, err := f.WeeklyReport(ctx, BotRef{}, "2026-11-01", "2026-11-07")
	require.ErrorIs(t, err, ErrInvalidInput)

	_, err = f.WeeklyReport(ctx, BotRef{ID: 1}, "2026-11-07", "2026-11-01")
	require.ErrorIs(t, err, ErrInvalidMetricsRange)

	_, err = f.Breakdown(ctx, BotRef{ID: 1}, "bad-date", "2026-11-07")
	require.ErrorIs(t, err, ErrInvalidMetricsRange)

	hits := Hits{{IssueID: 1, Title: "t"}}
	f.Knowledge = nil
	require.ErrorIs(t, f.WriteBack(ctx, BotRef{ID: 1}, hits), ErrFeedbackNotConfigured)
}

func TestSQLHitKnowledgeWritesMemoryAndBlackboard(t *testing.T) {
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)
	for _, stmt := range []string{
		`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (10, 'ws', '/tmp/ws', 'user', 2)`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, control_workspace_id) VALUES (1, 'user', 2, 'account', 10)`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	q := store.New(raw)
	k := &SQLHitKnowledge{Q: q}
	ctx := context.Background()
	entries := []HitKnowledgeEntry{{
		Key:     "ops-hit/1/101",
		Title:   "爆款拆解：3步搞定秋季护肤",
		Content: "选题类型：教程\n可复用要素：标题含数字",
		Refs:    []string{"爆款要素：标题含数字"},
		Metadata: map[string]any{
			"issue_id": 101,
		},
	}}
	require.NoError(t, k.Store(ctx, 1, entries))

	refs, err := k.Search(ctx, 1, "教程")
	require.NoError(t, err)
	require.Equal(t, []string{"爆款要素：标题含数字"}, refs)

	memories, err := q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: "user", OwnerID: 2,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	require.Equal(t, "爆款拆解：3步搞定秋季护肤", memories[0].Title)
	require.Contains(t, memories[0].Content, "引用：爆款要素：标题含数字")

	entry, err := q.GetBlackboardEntry(ctx, store.GetBlackboardEntryParams{
		WorkspaceID: 10, EntryKey: "ops-hit/1/101",
	})
	require.NoError(t, err)
	require.Contains(t, entry.Content, "标题含数字")

	// A repeated weekly review refreshes the same memory instead of duplicating.
	require.NoError(t, k.Store(ctx, 1, entries))
	memories, err = q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: "user", OwnerID: 2,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	require.EqualValues(t, 2, memories[0].Version)
}

// feedbackBriefStore is a minimal content.BriefStore for the brief round-trip.
type feedbackBriefStore struct{}

func (feedbackBriefStore) FindBriefByDedupeKey(context.Context, int64, string) (int64, bool, error) {
	return 0, false, nil
}
func (feedbackBriefStore) CreateBriefIssue(context.Context, content.BriefIssueInput) (int64, error) {
	return 1, nil
}
func (feedbackBriefStore) SaveBrief(context.Context, content.BriefSave) (int64, error) {
	return 1, nil
}
func (feedbackBriefStore) ReplaceAcceptanceChecklists(context.Context, int64, int64, []string) error {
	return nil
}
func (feedbackBriefStore) DeleteIssue(context.Context, int64) error { return nil }
