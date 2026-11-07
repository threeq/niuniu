package opsbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudgetRejectsOverLimitAndPauses(t *testing.T) {
	var (
		paused   bool
		notified string
	)
	b := &Budget{
		Sum: func(context.Context, int64, time.Time, time.Time) (float64, error) { return 990, nil },
		Pause: func(_ context.Context, botID int64) error {
			require.Equal(t, int64(1), botID)
			paused = true
			return nil
		},
		Notify: func(_ context.Context, botID int64, reason string) error {
			require.Equal(t, int64(1), botID)
			notified = reason
			return nil
		},
	}

	err := b.Check(context.Background(), 1, 901, 20)
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.True(t, paused)
	require.Contains(t, notified, "预算超限")
	require.Contains(t, notified, "1000.00")
}

func TestBudgetAllowsWithinLimit(t *testing.T) {
	b := &Budget{
		Sum: func(context.Context, int64, time.Time, time.Time) (float64, error) { return 10, nil },
	}
	require.NoError(t, b.Check(context.Background(), 1, 901, 20))
}

func TestBudgetAllowsWhenLimitIsNotPositive(t *testing.T) {
	paused := false
	b := &Budget{
		Sum:    func(context.Context, int64, time.Time, time.Time) (float64, error) { return 1_000_000, nil },
		Limit:  func(context.Context, int64) (float64, string, error) { return 0, "CNY", nil },
		Pause:  func(context.Context, int64) error { paused = true; return nil },
		Notify: func(context.Context, int64, string) error { return nil },
	}

	require.NoError(t, b.Check(context.Background(), 1, 901, 50_000))
	require.False(t, paused)
}

func TestBudgetUsesConfiguredLimitAndCurrency(t *testing.T) {
	b := &Budget{
		Sum:   func(context.Context, int64, time.Time, time.Time) (float64, error) { return 100, nil },
		Limit: func(context.Context, int64) (float64, string, error) { return 150, "CNY", nil },
	}

	require.ErrorIs(t, b.Check(context.Background(), 1, 901, 60), ErrBudgetExceeded)
	require.NoError(t, b.Check(context.Background(), 1, 901, 50))
}

func TestBudgetUsesCalendarMonthWindow(t *testing.T) {
	var from, to time.Time
	b := &Budget{
		Now: func() time.Time {
			return time.Date(2026, time.November, 7, 15, 4, 5, 0, time.FixedZone("CST", 8*3600))
		},
		Sum: func(_ context.Context, epicIssueID int64, start, end time.Time) (float64, error) {
			require.Equal(t, int64(901), epicIssueID)
			from, to = start, end
			return 0, nil
		},
	}

	_, err := b.Spent(context.Background(), 901)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, time.November, 1, 0, 0, 0, 0, from.Location()), from)
	require.Equal(t, time.Date(2026, time.December, 1, 0, 0, 0, 0, from.Location()), to)
}

func TestBudgetUsageReportsTotalsAndBreakdown(t *testing.T) {
	b := &Budget{
		Now:   func() time.Time { return time.Date(2026, time.November, 7, 0, 0, 0, 0, time.UTC) },
		Sum:   func(context.Context, int64, time.Time, time.Time) (float64, error) { return 500, nil },
		Limit: func(context.Context, int64) (float64, string, error) { return 1000, "CNY", nil },
		Breakdown: func(_ context.Context, epicIssueID int64, _, _ time.Time) ([]UsageItem, error) {
			require.Equal(t, int64(901), epicIssueID)
			return []UsageItem{
				{Category: "text", Unit: "token", Quantity: 1200},
				{Category: "image", Unit: "image", Quantity: 4},
				{Category: "video", Unit: "second", Quantity: 30},
			}, nil
		},
	}

	usage, err := b.Usage(context.Background(), 7, 901)
	require.NoError(t, err)
	require.Equal(t, int64(7), usage.BotID)
	require.Equal(t, 1000.0, usage.Limit)
	require.Equal(t, 500.0, usage.Spent)
	require.Equal(t, 500.0, usage.Remaining)
	require.Equal(t, "CNY", usage.Currency)
	require.False(t, usage.Unlimited)
	require.Len(t, usage.Items, 3)
	require.Equal(t, UsageItem{Category: "image", Unit: "image", Quantity: 4}, usage.Items[1])
}

func TestBudgetUsageMarksUnlimited(t *testing.T) {
	b := &Budget{
		Sum:   func(context.Context, int64, time.Time, time.Time) (float64, error) { return 42, nil },
		Limit: func(context.Context, int64) (float64, string, error) { return 0, "CNY", nil },
	}

	usage, err := b.Usage(context.Background(), 7, 901)
	require.NoError(t, err)
	require.True(t, usage.Unlimited)
	require.Equal(t, 42.0, usage.Spent)
	require.Zero(t, usage.Remaining)
}

func TestBudgetSetLimitSavesAndResumes(t *testing.T) {
	var (
		gotLimit    float64
		gotCurrency string
		resumed     bool
	)
	b := &Budget{
		SaveLimit: func(_ context.Context, botID int64, limit float64, currency string) error {
			require.Equal(t, int64(7), botID)
			gotLimit, gotCurrency = limit, currency
			return nil
		},
		Resume: func(_ context.Context, botID int64) error {
			require.Equal(t, int64(7), botID)
			resumed = true
			return nil
		},
	}

	require.NoError(t, b.SetLimit(context.Background(), 7, 1500, "CNY"))
	require.Equal(t, 1500.0, gotLimit)
	require.Equal(t, "CNY", gotCurrency)
	require.True(t, resumed)
}

func TestBudgetSetLimitRejectsInvalidAmount(t *testing.T) {
	b := &Budget{
		SaveLimit: func(context.Context, int64, float64, string) error {
			t.Fatal("must not persist an invalid limit")
			return nil
		},
	}

	require.ErrorIs(t, b.SetLimit(context.Background(), 7, -1, "CNY"), ErrInvalidBudgetLimit)
}

func TestBudgetKeepsExceededWhenPauseFails(t *testing.T) {
	b := &Budget{
		Sum:    func(context.Context, int64, time.Time, time.Time) (float64, error) { return 5000, nil },
		Pause:  func(context.Context, int64) error { return errors.New("db down") },
		Notify: func(context.Context, int64, string) error { return nil },
	}

	err := b.Check(context.Background(), 1, 901, 1)
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.Contains(t, err.Error(), "db down")
}
