package opsbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

type fakeMetricsStore struct {
	rows    map[string]store.OpsMetricsDaily
	upserts []store.UpsertOpsMetricsDailyParams
}

func newFakeMetricsStore() *fakeMetricsStore {
	return &fakeMetricsStore{rows: map[string]store.OpsMetricsDaily{}}
}

func (f *fakeMetricsStore) key(botID int64, date string) string {
	return fmt.Sprintf("%d/%s", botID, date)
}

func (f *fakeMetricsStore) seed(botID int64, date, raw, source string) {
	f.rows[f.key(botID, date)] = store.OpsMetricsDaily{
		BotID: botID, Date: date, MetricsJson: raw, Source: source,
	}
}

func (f *fakeMetricsStore) metrics(t *testing.T, botID int64, date string) map[string]float64 {
	t.Helper()
	row, ok := f.rows[f.key(botID, date)]
	require.True(t, ok, "no stored row for %s", date)
	var metrics map[string]float64
	require.NoError(t, json.Unmarshal([]byte(row.MetricsJson), &metrics))
	return metrics
}

func (f *fakeMetricsStore) UpsertOpsMetricsDaily(_ context.Context, p store.UpsertOpsMetricsDailyParams) (store.OpsMetricsDaily, error) {
	f.upserts = append(f.upserts, p)
	row := store.OpsMetricsDaily{BotID: p.BotID, Date: p.Date, MetricsJson: p.MetricsJson, Source: p.Source}
	f.rows[f.key(p.BotID, p.Date)] = row
	return row, nil
}

func (f *fakeMetricsStore) ListOpsMetricsDaily(_ context.Context, p store.ListOpsMetricsDailyParams) ([]store.OpsMetricsDaily, error) {
	var out []store.OpsMetricsDaily
	for _, row := range f.rows {
		if row.BotID == p.BotID && row.Date >= p.Date && row.Date <= p.Date_2 {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

var _ MetricsDailyStore = (*fakeMetricsStore)(nil)

type fakeBotGetter struct{ goals string }

func (f fakeBotGetter) GetOpsBot(_ context.Context, id int64) (store.OpsBot, error) {
	return store.OpsBot{ID: id, GoalsJson: f.goals}, nil
}

func TestMetricsSyncNormalizesAndZeroFills(t *testing.T) {
	st := newFakeMetricsStore()
	var gotBot int64
	var gotFrom, gotTo string
	m := &Metrics{
		Fetch: func(_ context.Context, botID int64, from, to string) ([]MetricsSample, error) {
			gotBot, gotFrom, gotTo = botID, from, to
			return []MetricsSample{{
				Date:   "2026-11-01",
				Fields: map[string]float64{"read_num": 100, "like_num": 5, "mystery": 3},
				Source: "xhs_browser",
			}}, nil
		},
		Map:   map[string]string{"read_num": "views", "like_num": "likes"},
		Store: st,
	}

	require.NoError(t, m.Sync(context.Background(), 7, "2026-11-01", "2026-11-03"))
	require.Equal(t, int64(7), gotBot)
	require.Equal(t, "2026-11-01", gotFrom)
	require.Equal(t, "2026-11-03", gotTo)
	require.Len(t, st.upserts, 3)

	// Known keys are renamed; unknown keys keep their original name.
	require.Equal(t, map[string]float64{"views": 100, "likes": 5, "mystery": 3}, st.metrics(t, 7, "2026-11-01"))
	require.Equal(t, "xhs_browser", st.rows[st.key(7, "2026-11-01")].Source)

	// Missing dates are stored as empty objects so daily charts stay continuous.
	require.Empty(t, st.metrics(t, 7, "2026-11-02"))
	require.Equal(t, "{}", st.rows[st.key(7, "2026-11-02")].MetricsJson)
}

func TestMetricsSyncKeepsExistingGapRowsAndOverwritesSampledDays(t *testing.T) {
	st := newFakeMetricsStore()
	st.seed(7, "2026-11-01", `{"views":1}`, "manual")
	st.seed(7, "2026-11-02", `{"views":50}`, "manual")
	m := &Metrics{
		Fetch: func(context.Context, int64, string, string) ([]MetricsSample, error) {
			return []MetricsSample{{
				Date:   "2026-11-01",
				Fields: map[string]float64{"views": 9},
			}}, nil
		},
		Store: st,
	}

	require.NoError(t, m.Sync(context.Background(), 7, "2026-11-01", "2026-11-02"))
	require.Equal(t, map[string]float64{"views": 9}, st.metrics(t, 7, "2026-11-01"))
	require.Equal(t, map[string]float64{"views": 50}, st.metrics(t, 7, "2026-11-02"))
	require.Len(t, st.upserts, 1, "an existing gap row must not be rewritten as empty")
}

func TestMetricsSyncRecordsCollectionCost(t *testing.T) {
	st := newFakeMetricsStore()
	var got []UsageItem
	m := &Metrics{
		Fetch: func(context.Context, int64, string, string) ([]MetricsSample, error) { return nil, nil },
		Store: st,
		Cost: func(_ context.Context, botID int64, item UsageItem) error {
			require.Equal(t, int64(3), botID)
			got = append(got, item)
			return nil
		},
	}

	require.NoError(t, m.Sync(context.Background(), 3, "2026-11-01", "2026-11-01"))
	require.Equal(t, []UsageItem{{Category: "api", Unit: "call", Quantity: 1}}, got)
}

func TestMetricsSyncPropagatesFetchError(t *testing.T) {
	want := errors.New("boom")
	m := &Metrics{
		Fetch: func(context.Context, int64, string, string) ([]MetricsSample, error) { return nil, want },
		Store: newFakeMetricsStore(),
	}
	require.ErrorIs(t, m.Sync(context.Background(), 1, "2026-11-01", "2026-11-02"), want)
}

func TestMetricsRejectsInvalidRangeAndMissingDeps(t *testing.T) {
	m := &Metrics{
		Fetch: func(context.Context, int64, string, string) ([]MetricsSample, error) { return nil, nil },
		Store: newFakeMetricsStore(),
	}
	for _, tc := range [][2]string{
		{"2026-11-03", "2026-11-01"},
		{"2026-13-01", "2026-11-02"},
		{"11-01", "2026-11-02"},
	} {
		require.ErrorIs(t, m.Sync(context.Background(), 1, tc[0], tc[1]), ErrInvalidMetricsRange, "%v", tc)
	}

	require.ErrorIs(t, (&Metrics{}).Sync(context.Background(), 1, "2026-11-01", "2026-11-02"), ErrMetricsNotConfigured)
	_, err := (&Metrics{}).Summary(context.Background(), 1, "2026-11-01", "2026-11-02")
	require.ErrorIs(t, err, ErrMetricsNotConfigured)
}

func TestMetricsSummaryAggregatesChangeAndGoalAchievement(t *testing.T) {
	st := newFakeMetricsStore()
	// Current window 2026-11-04..2026-11-06 (the 6th is a gap).
	st.seed(7, "2026-11-04", `{"views":10,"likes":1}`, "collector")
	st.seed(7, "2026-11-05", `{"views":20,"likes":2}`, "collector")
	// Previous equal-length window 2026-11-01..2026-11-03.
	st.seed(7, "2026-11-01", `{"views":10}`, "collector")
	st.seed(7, "2026-11-02", `{"views":10}`, "collector")

	m := &Metrics{
		Store: st,
		Bots:  fakeBotGetter{goals: `{"views":60,"followers":100,"brand":"awareness"}`},
	}

	sum, err := m.Summary(context.Background(), 7, "2026-11-04", "2026-11-06")
	require.NoError(t, err)
	require.Equal(t, int64(7), sum.BotID)
	require.Equal(t, 3, sum.Days)
	require.Equal(t, map[string]float64{"views": 30, "likes": 3}, sum.Totals)
	require.Equal(t, map[string]float64{"views": 10, "likes": 1}, sum.Averages)
	require.Equal(t, map[string]float64{"views": 20}, sum.Previous)
	require.Equal(t, map[string]float64{"views": 10, "likes": 3}, sum.Delta)
	require.Equal(t, map[string]float64{"views": 50}, sum.ChangePct)

	// Non-numeric goals (brand) are not achievement targets; zero goals are skipped.
	require.Equal(t, map[string]float64{"views": 60, "followers": 100}, sum.Goals)
	require.Equal(t, map[string]float64{"views": 0.5, "followers": 0}, sum.Achievement)

	require.Len(t, sum.Daily, 3)
	require.Equal(t, "2026-11-04", sum.Daily[0].Date)
	require.Equal(t, map[string]float64{"views": 10, "likes": 1}, sum.Daily[0].Metrics)
	require.Equal(t, "2026-11-06", sum.Daily[2].Date)
	require.Empty(t, sum.Daily[2].Metrics)
}

func TestMetricsSummaryRejectsMalformedStoredJSON(t *testing.T) {
	st := newFakeMetricsStore()
	st.seed(7, "2026-11-01", `not-json`, "")
	_, err := (&Metrics{Store: st}).Summary(context.Background(), 7, "2026-11-01", "2026-11-01")
	require.Error(t, err)
}

func TestMetricsSummaryWorksWithoutGoals(t *testing.T) {
	st := newFakeMetricsStore()
	st.seed(7, "2026-11-01", `{"views":10}`, "")
	sum, err := (&Metrics{Store: st}).Summary(context.Background(), 7, "2026-11-01", "2026-11-01")
	require.NoError(t, err)
	require.Empty(t, sum.Goals)
	require.Empty(t, sum.Achievement)
	require.Equal(t, map[string]float64{"views": 10}, sum.Totals)
}
