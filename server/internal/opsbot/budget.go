package opsbot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrBudgetExceeded is returned when a cost would push an account past its
// monthly budget. The account is paused and the operator notified before it is
// returned.
var ErrBudgetExceeded = errors.New("opsbot: budget exceeded")

// ErrInvalidBudgetLimit is returned by SetLimit for a negative or non-finite
// monthly limit.
var ErrInvalidBudgetLimit = errors.New("opsbot: invalid budget limit")

// ErrBudgetNotConfigured is returned by SetLimit when no persistence hook is
// wired.
var ErrBudgetNotConfigured = errors.New("opsbot: budget not configured")

const (
	// defaultMonthlyLimit applies only when no Limit resolver is wired. Without
	// it a misconfigured Budget would silently allow unlimited spend.
	defaultMonthlyLimit = 1000
	defaultCurrency     = "USD"
)

// UsageItem is one cost category's consumption inside a budget period. It is
// also the shape stored in issue_exec_events.detail_json for kind='cost'
// events: {"category":"text|image|video|api","unit":"token|image|second","quantity":N}.
type UsageItem struct {
	Category string  `json:"category"`
	Unit     string  `json:"unit"`
	Quantity float64 `json:"quantity"`
}

// BudgetUsage is an account's budget position for the current calendar month.
type BudgetUsage struct {
	BotID      int64       `json:"bot_id"`
	PeriodFrom time.Time   `json:"period_from"`
	PeriodTo   time.Time   `json:"period_to"`
	Limit      float64     `json:"limit"`
	Currency   string      `json:"currency"`
	Unlimited  bool        `json:"unlimited"`
	Spent      float64     `json:"spent"`
	Remaining  float64     `json:"remaining"`
	Items      []UsageItem `json:"items,omitempty"`
}

// Budget enforces a per-account monthly budget. Its dependencies are hooks so
// the service stays decoupled from the store, the OpsBot service and the
// notifier; production wires them in server.New.
type Budget struct {
	// Sum returns the cost consumed by an account Epic's subtree in [from, to).
	Sum func(ctx context.Context, epicIssueID int64, from, to time.Time) (float64, error)
	// Pause pauses an account bot's automatic actions.
	Pause func(ctx context.Context, botID int64) error
	// Notify tells the operator why the bot was paused.
	Notify func(ctx context.Context, botID int64, reason string) error
	// Limit reads the bot's monthly limit and currency from budget_json. A
	// limit <= 0 means unlimited.
	Limit func(ctx context.Context, botID int64) (float64, string, error)
	// Breakdown returns per-category consumption in [from, to). Optional.
	Breakdown func(ctx context.Context, epicIssueID int64, from, to time.Time) ([]UsageItem, error)
	// SaveLimit persists the monthly limit and currency to budget_json.
	SaveLimit func(ctx context.Context, botID int64, limit float64, currency string) error
	// Resume clears a pause, but must only clear one caused by budget.
	Resume func(ctx context.Context, botID int64) error
	// Now overrides the clock in tests. Optional.
	Now func() time.Time
}

// Check reports whether spending cost more is allowed for the account Epic this
// month. Over-limit checks pause the bot and notify the operator, and still
// return ErrBudgetExceeded when those hooks fail.
func (b *Budget) Check(ctx context.Context, botID, epicIssueID int64, cost float64) error {
	limit, currency, err := b.monthlyLimit(ctx, botID)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return nil
	}

	from, to := monthWindow(b.now())
	spent, err := b.spentBetween(ctx, epicIssueID, from, to)
	if err != nil {
		return err
	}
	if spent+cost <= limit {
		return nil
	}

	reason := fmt.Sprintf(
		"预算超限：本月上限 %.2f %s，已用 %.2f，本次 %.2f；账号已暂停自动动作",
		limit, currency, spent, cost,
	)
	errs := []error{ErrBudgetExceeded}
	if b.Pause != nil {
		if pauseErr := b.Pause(ctx, botID); pauseErr != nil {
			errs = append(errs, fmt.Errorf("pause bot: %w", pauseErr))
		}
	}
	if b.Notify != nil {
		if notifyErr := b.Notify(ctx, botID, reason); notifyErr != nil {
			errs = append(errs, fmt.Errorf("notify operator: %w", notifyErr))
		}
	}
	return errors.Join(errs...)
}

// Spent returns the account Epic's spend for the current calendar month.
func (b *Budget) Spent(ctx context.Context, epicIssueID int64) (float64, error) {
	from, to := monthWindow(b.now())
	return b.spentBetween(ctx, epicIssueID, from, to)
}

// Usage reports the account's limit, spend, remaining budget and per-category
// consumption for the current calendar month.
func (b *Budget) Usage(ctx context.Context, botID, epicIssueID int64) (*BudgetUsage, error) {
	limit, currency, err := b.monthlyLimit(ctx, botID)
	if err != nil {
		return nil, err
	}
	from, to := monthWindow(b.now())
	spent, err := b.spentBetween(ctx, epicIssueID, from, to)
	if err != nil {
		return nil, err
	}

	usage := &BudgetUsage{
		BotID:      botID,
		PeriodFrom: from,
		PeriodTo:   to,
		Limit:      limit,
		Currency:   currency,
		Unlimited:  limit <= 0,
		Spent:      spent,
	}
	if !usage.Unlimited {
		usage.Remaining = limit - spent
	}
	if b.Breakdown != nil {
		items, err := b.Breakdown(ctx, epicIssueID, from, to)
		if err != nil {
			return nil, err
		}
		usage.Items = items
	}
	return usage, nil
}

// SetLimit writes a new monthly limit and resumes the bot if it had been paused
// by a budget overrun. A limit of zero means unlimited.
func (b *Budget) SetLimit(ctx context.Context, botID int64, limit float64, currency string) error {
	if math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0 {
		return fmt.Errorf("%w: %v", ErrInvalidBudgetLimit, limit)
	}
	if b.SaveLimit == nil {
		return ErrBudgetNotConfigured
	}
	if currency == "" {
		currency = defaultCurrency
	}
	if err := b.SaveLimit(ctx, botID, limit, currency); err != nil {
		return err
	}
	if b.Resume != nil {
		return b.Resume(ctx, botID)
	}
	return nil
}

func (b *Budget) monthlyLimit(ctx context.Context, botID int64) (float64, string, error) {
	if b.Limit == nil {
		return defaultMonthlyLimit, defaultCurrency, nil
	}
	limit, currency, err := b.Limit(ctx, botID)
	if err != nil {
		return 0, "", err
	}
	if currency == "" {
		currency = defaultCurrency
	}
	return limit, currency, nil
}

func (b *Budget) spentBetween(ctx context.Context, epicIssueID int64, from, to time.Time) (float64, error) {
	if b.Sum == nil {
		return 0, nil
	}
	return b.Sum(ctx, epicIssueID, from, to)
}

func (b *Budget) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func monthWindow(now time.Time) (from, to time.Time) {
	from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return from, from.AddDate(0, 1, 0)
}
