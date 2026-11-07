package opsbot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Defaults for the approved-reply dispatcher.
const (
	defaultDispatchInterval = 30 * time.Second
	defaultDispatchBatch    = 100
)

// DispatchResult counts one scan of approved replies.
type DispatchResult struct {
	Scanned     int `json:"scanned"`
	Sent        int `json:"sent"`
	Skipped     int `json:"skipped"`
	Failed      int `json:"failed"`
	RateLimited int `json:"rate_limited"`
}

// DispatchStore is the durable half of the dispatcher. *store.Queries
// satisfies it.
type DispatchStore interface {
	ListOpsEngagementsByStatusGlobal(ctx context.Context, arg store.ListOpsEngagementsByStatusGlobalParams) ([]store.OpsEngagement, error)
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
}

var _ DispatchStore = (*store.Queries)(nil)

// EngagementDispatcher periodically sends interactions an operator has already
// approved. Sending goes through Engagement.Send, which re-checks the account
// status, the hourly cap and the retry window, so a kill switch or a paused
// account can never be dispatched to even if a row was approved earlier.
type EngagementDispatcher struct {
	Q DispatchStore
	// Send is Engagement.Send for one bot. Required.
	Send func(ctx context.Context, botID, id, approvalRequestID int64, d Decision) error

	Interval time.Duration
	Batch    int64
	// OnError observes a failed send without stopping the scan.
	OnError func(ctx context.Context, engagementID int64, err error)

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// Start launches the periodic scan. It is a no-op when already running.
func (d *EngagementDispatcher) Start(parent context.Context) error {
	if d == nil || d.Q == nil || d.Send == nil {
		return ErrEngagementNotConfigured
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	d.cancel = cancel
	d.done = done
	d.running = true

	interval := d.Interval
	if interval <= 0 {
		interval = defaultDispatchInterval
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := d.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("opsbot: engagement dispatch scan failed", "error", err)
				}
			}
		}
	}()
	return nil
}

// Stop cancels the loop and waits for the current scan to finish. Safe to call
// when not running.
func (d *EngagementDispatcher) Stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	d.cancel, d.done, d.running = nil, nil, false
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

// Running reports whether the loop is active (used by the wiring checks).
func (d *EngagementDispatcher) Running() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// RunOnce sends every currently approved reply, oldest first. A rate-limited
// account stops the batch: the remaining items stay approved for the next
// window instead of being recorded as failures.
func (d *EngagementDispatcher) RunOnce(ctx context.Context) (DispatchResult, error) {
	var result DispatchResult
	if d == nil || d.Q == nil || d.Send == nil {
		return result, ErrEngagementNotConfigured
	}
	batch := d.Batch
	if batch <= 0 {
		batch = defaultDispatchBatch
	}
	rows, err := d.Q.ListOpsEngagementsByStatusGlobal(ctx, store.ListOpsEngagementsByStatusGlobalParams{
		Status: EngagementApproved,
		Limit:  batch,
	})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)

	for _, row := range rows {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Fail closed before Send: a paused/disabled account (including a kill
		// switch) is never handed to the sender, even for a queued approval.
		bot, err := d.Q.GetOpsBot(ctx, row.BotID)
		if err != nil {
			result.Failed++
			if d.OnError != nil {
				d.OnError(ctx, row.ID, err)
			}
			continue
		}
		if bot.Status != StatusActive {
			result.Skipped++
			continue
		}
		approvalID := int64(0)
		if row.ApprovalRequestID.Valid {
			approvalID = row.ApprovalRequestID.Int64
		}
		err = d.Send(ctx, row.BotID, row.ID, approvalID, Decision{Action: ActionApproved})
		switch {
		case err == nil:
			result.Sent++
		case errors.Is(err, ErrBotNotActive):
			// Paused/disabled accounts (including a kill switch) are skipped,
			// and the row stays approved for when the account resumes.
			result.Skipped++
		case errors.Is(err, ErrRateLimited):
			result.RateLimited++
			return result, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return result, ctx.Err()
		default:
			result.Failed++
			if d.OnError != nil {
				d.OnError(ctx, row.ID, err)
			}
			slog.Warn("opsbot: engagement dispatch failed", "engagement", row.ID, "error", err)
		}
	}
	return result, nil
}
