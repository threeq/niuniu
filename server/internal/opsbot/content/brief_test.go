package content

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// fakeBriefStore is the in-memory BriefStore used by the builder tests.
type fakeBriefStore struct {
	nextIssueID int64
	nextBriefID int64
	issues      map[int64]BriefIssueInput
	briefs      map[int64]BriefSave
	checklists  map[int64][]string
	created     int
	saved       int
	deleted     []int64
}

func newFakeBriefStore() *fakeBriefStore {
	return &fakeBriefStore{
		issues:     map[int64]BriefIssueInput{},
		briefs:     map[int64]BriefSave{},
		checklists: map[int64][]string{},
	}
}

func (f *fakeBriefStore) FindBriefByDedupeKey(_ context.Context, botID int64, key string) (int64, bool, error) {
	for _, rec := range f.briefs {
		if rec.BotID == botID && rec.DedupeKey == key {
			return rec.IssueID, true, nil
		}
	}
	return 0, false, nil
}

func (f *fakeBriefStore) CreateBriefIssue(_ context.Context, in BriefIssueInput) (int64, error) {
	f.nextIssueID++
	f.created++
	f.issues[f.nextIssueID] = in
	return f.nextIssueID, nil
}

func (f *fakeBriefStore) SaveBrief(_ context.Context, in BriefSave) (int64, error) {
	f.nextBriefID++
	f.saved++
	f.briefs[f.nextBriefID] = in
	return f.nextBriefID, nil
}

func (f *fakeBriefStore) ReplaceAcceptanceChecklists(_ context.Context, issueID, _ int64, items []string) error {
	f.checklists[issueID] = append([]string(nil), items...)
	return nil
}

func (f *fakeBriefStore) DeleteIssue(_ context.Context, issueID int64) error {
	f.deleted = append(f.deleted, issueID)
	return nil
}

func fixedNow() time.Time {
	return time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
}

func newTestBriefBuilder(store BriefStore) *BriefBuilder {
	return &BriefBuilder{
		Knowledge: func(context.Context, string) ([]string, error) {
			return []string{"品牌语气：克制", "爆款：清单体"}, nil
		},
		Trends: func(context.Context, string) ([]string, error) {
			return []string{"秋季穿搭"}, nil
		},
		Store: store,
		Now:   fixedNow,
	}
}

func TestBuildBriefUsesKnowledgeHitsAndTrends(t *testing.T) {
	b := newTestBriefBuilder(newFakeBriefStore())
	brief, err := b.Build(context.Background(), BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note",
	})
	require.NoError(t, err)
	require.Contains(t, brief.SourceRefs, "品牌语气：克制")
	require.Contains(t, brief.SourceRefs, "秋季穿搭")
	require.NotEmpty(t, brief.Acceptance)
	require.Equal(t, "秋季穿搭", brief.Topic)
	require.Equal(t, "涨粉", brief.Goal)
	require.Equal(t, "25-35 女性", brief.Audience)
	require.Equal(t, "note", brief.Format)
	require.NotZero(t, brief.IssueID)
	require.False(t, brief.Existing)
}

func TestBuildBriefAcceptanceDiffersByPlatform(t *testing.T) {
	cases := []struct {
		name       string
		packID     string
		format     string
		wantPhrase []string
	}{
		{
			name: "wechat article", packID: "wechat_mp", format: "article",
			wantPhrase: []string{"标题", "64", "来源", "封面", "排版"},
		},
		{
			name: "xiaohongshu note", packID: "xiaohongshu", format: "note",
			wantPhrase: []string{"标题", "20", "图片", "标签", "3:4"},
		},
		{
			name: "video", packID: "xiaohongshu", format: "video",
			wantPhrase: []string{"时长", "字幕", "比例", "封面"},
		},
	}

	acceptance := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBriefBuilder(newFakeBriefStore())
			brief, err := b.Build(context.Background(), BriefInput{
				BotID: 1, PackID: tc.packID, AccountRef: "a1",
				Goal: "品牌", Audience: "泛人群", Format: tc.format,
				Topic: "秋季护理",
			})
			require.NoError(t, err)
			require.Len(t, brief.Acceptance, 4)
			joined := strings.Join(brief.Acceptance, "\n")
			for _, phrase := range tc.wantPhrase {
				require.Contains(t, joined, phrase)
			}
			acceptance[tc.format] = joined
		})
	}

	require.NotEqual(t, acceptance["article"], acceptance["note"])
	require.NotEqual(t, acceptance["note"], acceptance["video"])
	require.NotEqual(t, acceptance["article"], acceptance["video"])
}

func TestBuildBriefVideoLimitsFollowPack(t *testing.T) {
	build := func(packID string) string {
		b := newTestBriefBuilder(newFakeBriefStore())
		brief, err := b.Build(context.Background(), BriefInput{
			BotID: 1, PackID: packID, AccountRef: "v1",
			Goal: "涨粉", Audience: "泛人群", Format: "video", Topic: "护肤三步",
		})
		require.NoError(t, err)
		return strings.Join(brief.Acceptance, "\n")
	}
	require.Contains(t, build("xiaohongshu"), "15 分钟")
	require.Contains(t, build("wechat_mp"), "30 分钟")
}

func TestBuildBriefDeduplicatesSameAccountWeekTopic(t *testing.T) {
	fs := newFakeBriefStore()
	b := newTestBriefBuilder(fs)
	in := BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note", Topic: "秋季穿搭",
	}

	first, err := b.Build(context.Background(), in)
	require.NoError(t, err)
	require.False(t, first.Existing)

	second, err := b.Build(context.Background(), in)
	require.NoError(t, err)
	require.True(t, second.Existing)
	require.Equal(t, first.IssueID, second.IssueID)
	require.Equal(t, 1, fs.created)
	require.Equal(t, 1, fs.saved)
}

func TestBuildBriefCreatesIssueForNewWeekOrTopic(t *testing.T) {
	fs := newFakeBriefStore()
	b := newTestBriefBuilder(fs)
	base := BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note", Topic: "秋季穿搭",
	}

	_, err := b.Build(context.Background(), base)
	require.NoError(t, err)

	nextWeek := base
	nextWeek.Week = "2026-W46"
	got, err := b.Build(context.Background(), nextWeek)
	require.NoError(t, err)
	require.False(t, got.Existing)

	otherTopic := base
	otherTopic.Topic = "冬季护肤"
	got, err = b.Build(context.Background(), otherTopic)
	require.NoError(t, err)
	require.False(t, got.Existing)
	require.Equal(t, 3, fs.created)
}

func TestBuildBriefRejectsInvalidInput(t *testing.T) {
	valid := BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note", Topic: "秋季穿搭",
	}
	b := newTestBriefBuilder(newFakeBriefStore())

	_, err := b.Build(context.Background(), BriefInput{})
	require.ErrorIs(t, err, ErrInvalidBriefInput)

	badFormat := valid
	badFormat.Format = "podcast"
	_, err = b.Build(context.Background(), badFormat)
	require.ErrorIs(t, err, ErrInvalidBriefInput)

	noGoal := valid
	noGoal.Goal = ""
	_, err = b.Build(context.Background(), noGoal)
	require.ErrorIs(t, err, ErrInvalidBriefInput)

	badDueDate := valid
	badDueDate.DueDate = "next friday"
	_, err = b.Build(context.Background(), badDueDate)
	require.ErrorIs(t, err, ErrInvalidBriefInput)
}

func TestBuildBriefRejectsMissingTopicWhenNoTrend(t *testing.T) {
	b := newTestBriefBuilder(newFakeBriefStore())
	b.Trends = func(context.Context, string) ([]string, error) { return nil, nil }
	_, err := b.Build(context.Background(), BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note",
	})
	require.ErrorIs(t, err, ErrInvalidBriefInput)
}

func TestSQLBriefStorePersistsBriefAndAcceptance(t *testing.T) {
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash) VALUES (7, 'operator', 'x')`,
		`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'ops', 'user', 2)`,
		`INSERT INTO columns (id, project_id, name, position) VALUES (10, 1, '待办', 0)`,
		`INSERT INTO issues (id, column_id, title) VALUES (20, 10, '运营 Bot：小红书 A')`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, pack_id, account_ref, display_name, epic_issue_id)
		 VALUES (1, 'user', 2, 'account', 'xiaohongshu', 'x1', '小红书 A', 20)`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err, stmt)
	}

	b := newTestBriefBuilder(&SQLBriefStore{Q: store.New(raw)})
	in := BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note", Topic: "秋季穿搭",
		CreatedBy: 7,
	}
	brief, err := b.Build(context.Background(), in)
	require.NoError(t, err)
	require.NotZero(t, brief.IssueID)

	var goal, acceptanceJSON string
	require.NoError(t, raw.QueryRow(
		`SELECT goal, acceptance_json FROM task_briefs WHERE issue_id = ? ORDER BY version DESC LIMIT 1`,
		brief.IssueID,
	).Scan(&goal, &acceptanceJSON))
	require.Equal(t, "涨粉", goal)
	require.Contains(t, acceptanceJSON, "标签")

	var count int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM issue_checklists WHERE issue_id = ? AND kind = 'acceptance'`,
		brief.IssueID,
	).Scan(&count))
	require.Equal(t, len(brief.Acceptance), count)

	var parent sql.NullInt64
	require.NoError(t, raw.QueryRow(`SELECT parent_issue_id FROM issues WHERE id = ?`, brief.IssueID).Scan(&parent))
	require.True(t, parent.Valid)
	require.Equal(t, int64(20), parent.Int64)

	again, err := b.Build(context.Background(), in)
	require.NoError(t, err)
	require.True(t, again.Existing)
	require.Equal(t, brief.IssueID, again.IssueID)

	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM issues WHERE parent_issue_id = 20`,
	).Scan(&count))
	require.Equal(t, 1, count)
}
