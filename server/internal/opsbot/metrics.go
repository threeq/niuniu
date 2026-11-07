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

	"github.com/niuniu-dev/niuniu/internal/store"
)

const metricsDateLayout = "2006-01-02"

var (
	// ErrInvalidMetricsRange is returned for an unparseable date or a range whose
	// start is after its end.
	ErrInvalidMetricsRange = errors.New("opsbot: invalid metrics date range")
	// ErrMetricsNotConfigured is returned when a required dependency is missing.
	ErrMetricsNotConfigured = errors.New("opsbot: metrics not configured")
)

// MetricsSample is one day of platform metrics as returned by a channel
// adapter. Fields use the platform's own metric keys; Sync normalizes them.
type MetricsSample struct {
	Date   string
	Fields map[string]float64
	Source string
}

// MetricsDailyStore persists normalized daily metrics. *store.Queries
// satisfies it; tests use an in-memory fake.
type MetricsDailyStore interface {
	UpsertOpsMetricsDaily(ctx context.Context, arg store.UpsertOpsMetricsDailyParams) (store.OpsMetricsDaily, error)
	ListOpsMetricsDaily(ctx context.Context, arg store.ListOpsMetricsDailyParams) ([]store.OpsMetricsDaily, error)
}

var _ MetricsDailyStore = (*store.Queries)(nil)

// BotGetter reads a bot row; Summary uses it for goals_json.
type BotGetter interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
}

var _ BotGetter = (*store.Queries)(nil)

// Metrics collects, normalizes and aggregates per-account daily metrics.
// Dependencies are hooks so the collector stays decoupled from the channel
// adapter and the event store; production wires them in server.New.
type Metrics struct {
	// Fetch returns the platform's daily samples for [from, to], inclusive.
	Fetch func(ctx context.Context, botID int64, from, to string) ([]MetricsSample, error)
	// Map translates platform metric keys to the unified namespace from the
	// Pack metrics_map. Unmapped keys keep their original name.
	Map map[string]string
	// Store persists normalized rows.
	Store MetricsDailyStore
	// Bots supplies goals_json for Summary. Optional: without it a summary has
	// no goals or achievement values.
	Bots BotGetter
	// Cost records the cost of one collection call. Optional.
	Cost func(ctx context.Context, botID int64, item UsageItem) error
}

// MetricsDay is one normalized day in a summary.
type MetricsDay struct {
	Date    string             `json:"date"`
	Metrics map[string]float64 `json:"metrics"`
	Source  string             `json:"source,omitempty"`
}

// Summary aggregates one account's metrics over an inclusive date range.
// Totals sum the days, Averages divide by the range length, Previous and Delta
// compare against the immediately preceding range of the same length, and
// Achievement is Totals[key]/goal for every positive goal in goals_json.
type Summary struct {
	BotID       int64              `json:"bot_id"`
	From        string             `json:"from"`
	To          string             `json:"to"`
	Days        int                `json:"days"`
	Daily       []MetricsDay       `json:"daily"`
	Totals      map[string]float64 `json:"totals"`
	Averages    map[string]float64 `json:"averages"`
	Previous    map[string]float64 `json:"previous_totals"`
	Delta       map[string]float64 `json:"delta"`
	ChangePct   map[string]float64 `json:"change_pct"`
	Goals       map[string]float64 `json:"goals"`
	Achievement map[string]float64 `json:"achievement"`
}

// Sync fetches [from, to], normalizes each day through Map, and upserts one row
// per day. Days the platform returned nothing for are stored as empty objects
// so daily charts stay continuous; an existing row for such a day is left
// untouched.
func (m *Metrics) Sync(ctx context.Context, botID int64, from, to string) error {
	if m.Fetch == nil || m.Store == nil {
		return ErrMetricsNotConfigured
	}
	if botID <= 0 {
		return fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	dates, err := metricsDates(from, to)
	if err != nil {
		return err
	}

	samples, err := m.Fetch(ctx, botID, strings.TrimSpace(from), strings.TrimSpace(to))
	if err != nil {
		return fmt.Errorf("opsbot: fetch metrics: %w", err)
	}

	rangeSet := make(map[string]struct{}, len(dates))
	for _, d := range dates {
		rangeSet[d] = struct{}{}
	}
	sampled := make(map[string]MetricsSample, len(samples))
	for _, s := range samples {
		d := strings.TrimSpace(s.Date)
		if _, ok := rangeSet[d]; !ok {
			continue
		}
		sampled[d] = s
	}

	existing, err := m.Store.ListOpsMetricsDaily(ctx, store.ListOpsMetricsDailyParams{
		BotID:  botID,
		Date:   dates[0],
		Date_2: dates[len(dates)-1],
	})
	if err != nil {
		return fmt.Errorf("opsbot: list existing metrics: %w", err)
	}
	hasRow := make(map[string]struct{}, len(existing))
	for _, row := range existing {
		hasRow[row.Date] = struct{}{}
	}

	for _, d := range dates {
		s, ok := sampled[d]
		if !ok {
			if _, exists := hasRow[d]; exists {
				continue
			}
			s = MetricsSample{Date: d}
		}
		raw, err := json.Marshal(normalizeMetrics(s.Fields, m.Map))
		if err != nil {
			return fmt.Errorf("opsbot: encode metrics for %s: %w", d, err)
		}
		if _, err := m.Store.UpsertOpsMetricsDaily(ctx, store.UpsertOpsMetricsDailyParams{
			BotID:       botID,
			Date:        d,
			MetricsJson: string(raw),
			Source:      strings.TrimSpace(s.Source),
		}); err != nil {
			return fmt.Errorf("opsbot: store metrics for %s: %w", d, err)
		}
	}

	if m.Cost != nil {
		if err := m.Cost(ctx, botID, UsageItem{Category: "api", Unit: "call", Quantity: 1}); err != nil {
			return fmt.Errorf("opsbot: record metrics cost: %w", err)
		}
	}
	return nil
}

// Summary aggregates the stored rows for [from, to] plus the equal-length
// window immediately before it, and evaluates goals_json when Bots is wired.
func (m *Metrics) Summary(ctx context.Context, botID int64, from, to string) (Summary, error) {
	if m.Store == nil {
		return Summary{}, ErrMetricsNotConfigured
	}
	if botID <= 0 {
		return Summary{}, fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	dates, err := metricsDates(from, to)
	if err != nil {
		return Summary{}, err
	}

	current, err := m.loadDays(ctx, botID, dates[0], dates[len(dates)-1])
	if err != nil {
		return Summary{}, err
	}

	start, _ := time.Parse(metricsDateLayout, dates[0])
	prevTo := start.AddDate(0, 0, -1)
	prevFrom := prevTo.AddDate(0, 0, -(len(dates) - 1))
	previous, err := m.loadTotals(ctx, botID, prevFrom.Format(metricsDateLayout), prevTo.Format(metricsDateLayout))
	if err != nil {
		return Summary{}, err
	}

	sum := Summary{
		BotID:       botID,
		From:        dates[0],
		To:          dates[len(dates)-1],
		Days:        len(dates),
		Daily:       make([]MetricsDay, 0, len(dates)),
		Totals:      map[string]float64{},
		Averages:    map[string]float64{},
		Previous:    previous,
		Delta:       map[string]float64{},
		ChangePct:   map[string]float64{},
		Goals:       map[string]float64{},
		Achievement: map[string]float64{},
	}

	byDate := make(map[string]MetricsDay, len(current))
	for _, day := range current {
		byDate[day.Date] = day
	}
	for _, d := range dates {
		day, ok := byDate[d]
		if !ok {
			day = MetricsDay{Date: d, Metrics: map[string]float64{}}
		}
		for k, v := range day.Metrics {
			sum.Totals[k] += v
		}
		sum.Daily = append(sum.Daily, day)
	}
	for k, v := range sum.Totals {
		sum.Averages[k] = v / float64(len(dates))
	}

	for _, k := range unionMetricKeys(sum.Totals, previous) {
		delta := sum.Totals[k] - previous[k]
		sum.Delta[k] = delta
		if previous[k] != 0 {
			sum.ChangePct[k] = delta / previous[k] * 100
		}
	}

	if m.Bots != nil {
		bot, err := m.Bots.GetOpsBot(ctx, botID)
		if errors.Is(err, sql.ErrNoRows) {
			return Summary{}, fmt.Errorf("%w: %d", ErrNotFound, botID)
		}
		if err != nil {
			return Summary{}, fmt.Errorf("opsbot: load goals: %w", err)
		}
		goals, err := parseMetricGoals(bot.GoalsJson)
		if err != nil {
			return Summary{}, err
		}
		sum.Goals = goals
		for k, goal := range goals {
			if goal > 0 {
				sum.Achievement[k] = sum.Totals[k] / goal
			}
		}
	}
	return sum, nil
}

func (m *Metrics) loadDays(ctx context.Context, botID int64, from, to string) ([]MetricsDay, error) {
	rows, err := m.Store.ListOpsMetricsDaily(ctx, store.ListOpsMetricsDailyParams{
		BotID: botID, Date: from, Date_2: to,
	})
	if err != nil {
		return nil, fmt.Errorf("opsbot: list metrics: %w", err)
	}
	days := make([]MetricsDay, 0, len(rows))
	for _, row := range rows {
		var fields map[string]float64
		if err := json.Unmarshal([]byte(row.MetricsJson), &fields); err != nil {
			return nil, fmt.Errorf("opsbot: decode metrics for %s: %w", row.Date, err)
		}
		if fields == nil {
			fields = map[string]float64{}
		}
		days = append(days, MetricsDay{Date: row.Date, Metrics: fields, Source: row.Source})
	}
	return days, nil
}

func (m *Metrics) loadTotals(ctx context.Context, botID int64, from, to string) (map[string]float64, error) {
	days, err := m.loadDays(ctx, botID, from, to)
	if err != nil {
		return nil, err
	}
	totals := map[string]float64{}
	for _, day := range days {
		for k, v := range day.Metrics {
			totals[k] += v
		}
	}
	return totals, nil
}

// normalizeMetrics renames known platform keys and keeps unknown ones as-is.
// Iteration is sorted so a mapping that collides with an existing key resolves
// deterministically.
func normalizeMetrics(fields map[string]float64, mapping map[string]string) map[string]float64 {
	out := make(map[string]float64, len(fields))
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		target := strings.TrimSpace(mapping[k])
		if target == "" {
			target = k
		}
		out[target] = fields[k]
	}
	return out
}

func parseMetricGoals(raw string) (map[string]float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]float64{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("%w: goals_json must be a JSON object: %v", ErrInvalidConfig, err)
	}
	goals := make(map[string]float64, len(fields))
	for k, v := range fields {
		var n float64
		// Non-numeric goals (e.g. a brand awareness description) are not
		// measurable targets and are skipped.
		if err := json.Unmarshal(v, &n); err != nil {
			continue
		}
		goals[k] = n
	}
	return goals, nil
}

func metricsDates(from, to string) ([]string, error) {
	start, err := parseMetricsDate(from)
	if err != nil {
		return nil, err
	}
	end, err := parseMetricsDate(to)
	if err != nil {
		return nil, err
	}
	if end.Before(start) {
		return nil, fmt.Errorf("%w: %s is after %s", ErrInvalidMetricsRange, from, to)
	}
	var dates []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(metricsDateLayout))
	}
	return dates, nil
}

func parseMetricsDate(v string) (time.Time, error) {
	t, err := time.Parse(metricsDateLayout, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidMetricsRange, v)
	}
	return t, nil
}

func unionMetricKeys(a, b map[string]float64) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	keys := make([]string, 0, len(a)+len(b))
	for _, m := range []map[string]float64{a, b} {
		for k := range m {
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
