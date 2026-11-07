package opsbot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Publish statuses persisted on ops_publish_records.status.
const (
	PublishStatusPending       = "pending"
	PublishStatusPublished     = "published"
	PublishStatusManualPending = "manual_pending"
	PublishStatusFailed        = "failed"
)

// Publish modes reported by PublishResult.Mode. They describe how the record
// was produced, which a status alone cannot: a pending record is either a
// deferred publish or one a scheduler is about to fire.
const (
	PublishModeAdapter       = "adapter"
	PublishModeScheduled     = "scheduled"
	PublishModeManualPackage = "manual_package"
	PublishModeFailed        = "failed"
)

const (
	defaultPublishAttempts = 3
	defaultPublishBackoff  = 500 * time.Millisecond
	maxPublishBackoff      = 30 * time.Second
	maxStoredReceiptRaw    = 2048
)

var (
	// ErrPublishNotConfigured is returned when a required dependency is missing.
	ErrPublishNotConfigured = errors.New("opsbot: publish not configured")
	// ErrInvalidPublishInput is returned for a PublishInput that cannot name a
	// publish.
	ErrInvalidPublishInput = errors.New("opsbot: invalid publish input")
	// ErrScheduleUnavailable is returned when a future publish is requested but
	// no scheduler is wired to fire it.
	ErrScheduleUnavailable = errors.New("opsbot: scheduled publish has no scheduler")
	// ErrNoManualFallback is returned when a publish degrades but the manual
	// package packer is not configured.
	ErrNoManualFallback = errors.New("opsbot: no manual publish fallback")
)

// degradableAdapterCodes are the adapter failure codes that mean the platform
// cannot be driven automatically right now, so the only useful next step is a
// manual publish package. Everything else (expired credential, invalid payload)
// stays a failure with a concrete issue.
var degradableAdapterCodes = map[string]bool{
	"unsupported":            true,
	"capability_unavailable": true,
	"recipe_broken":          true,
	"no_adapter":             true,
	"manual":                 true,
	// An expired browser login cannot be driven automatically right now; the
	// content still needs to go out, so hand it to the operator as a package.
	channel.CodeLoginExpired: true,
}

// PublishInput describes one publish attempt. BotID and IssueID are the only
// required fields; ContentVersion defaults to 1.
type PublishInput struct {
	BotID          int64
	IssueID        int64
	ContentVersion int64
	// Platform names the channel being published to. It takes part in the
	// derived idempotency key and in the manual package.
	Platform   string
	PackID     string
	AccountRef string
	// IdempotencyKey overrides the derived key. Two calls with the same key
	// share one publish record and one adapter call.
	IdempotencyKey string

	// ScheduledAt defers the adapter call until that instant. Requires
	// Publisher.Schedule.
	ScheduledAt *time.Time

	// Payload is handed to the channel adapter unchanged.
	Payload any

	// Manual package content, used when the publish degrades to a human.
	Title      string
	Body       string
	MediaPaths []string
	Params     map[string]string

	// ScreenshotPath is optional evidence captured before Publish is called.
	ScreenshotPath string
}

// PublishResult reports the durable outcome of a publish attempt.
type PublishResult struct {
	RecordID       int64
	Status         string
	Mode           string
	Receipt        string
	URL            string
	ScreenshotPath string
	PackageDir     string
	ProblemIssueID int64
	Attempts       int
	ScheduledAt    *time.Time
	PublishedAt    *time.Time
	// Duplicate is true when the call matched an existing record and therefore
	// did not touch the platform.
	Duplicate bool
}

// PublishStore is the durable half of the orchestrator. *store.Queries
// satisfies it; tests use a real SQLite DB.
type PublishStore interface {
	GetOpsPublishRecord(ctx context.Context, arg store.GetOpsPublishRecordParams) (store.OpsPublishRecord, error)
	CreateOpsPublishRecord(ctx context.Context, arg store.CreateOpsPublishRecordParams) (store.OpsPublishRecord, error)
}

var _ PublishStore = (*store.Queries)(nil)

// PublishEventWriter appends the publish outcome to the issue timeline.
// *store.Queries satisfies it.
type PublishEventWriter interface {
	InsertIssueExecEvent(ctx context.Context, arg store.InsertIssueExecEventParams) error
}

var _ PublishEventWriter = (*store.Queries)(nil)

// PublishProblem is the failure context handed to a PublishProblemReporter.
type PublishProblem struct {
	BotID          int64
	IssueID        int64
	RecordID       int64
	Platform       string
	AccountRef     string
	IdempotencyKey string
	Code           string
	Retryable      bool
	Attempts       int
	Error          string
}

// PublishProblemReporter opens a concrete issue for an unrecoverable publish
// failure so the operator has something to act on.
type PublishProblemReporter interface {
	OpenPublishProblem(ctx context.Context, p PublishProblem) (int64, error)
}

// Publisher orchestrates publishing: one adapter call per idempotency key, the
// durable record, retry/backoff, receipt and evidence capture, manual package
// degradation and problem issues for unrecoverable failures.
//
// Optional hooks (Events, Problems, Notify, CheckBudget, Schedule,
// CaptureScreenshot) are wired in server.New; a nil hook simply skips that
// side effect.
type Publisher struct {
	Q  PublishStore
	DB *store.DB

	// Adapter resolves the platform adapter for a bot. Production wires it to
	// the credential store + pack registry.
	AdapterFor func(ctx context.Context, botID int64) (channel.ChannelAdapter, error)
	// Adapter is the single-adapter fallback used when AdapterFor is nil.
	Adapter channel.ChannelAdapter

	// Manual materializes publish packages when the adapter cannot publish.
	// Nil turns degradation into a failure.
	Manual *channel.ManualAdapter

	Events   PublishEventWriter
	Problems PublishProblemReporter
	Notify   func(ctx context.Context, botID int64, event string, detail map[string]any) error
	// CheckBudget blocks a publish before any record is written.
	CheckBudget func(ctx context.Context, botID int64) error
	// BotStatus gates the actual send on the account lifecycle. A paused or
	// disabled account (including a kill switch) is never published to; the
	// record stays pending. Optional: nil skips the check.
	BotStatus func(ctx context.Context, botID int64) (string, error)
	// OnAdapterFailure observes every failed adapter attempt before degradation
	// or failure handling, so the account risk controller can pause on login
	// expiry / rate limits. Optional.
	OnAdapterFailure func(ctx context.Context, botID int64, err error)
	// Schedule registers run to fire at at. The run callback reloads the
	// pending record and dispatches it.
	Schedule func(ctx context.Context, at time.Time, key string, run func(context.Context)) error
	// CaptureScreenshot records post-publish evidence. When it returns an empty
	// path the input's ScreenshotPath is kept.
	CaptureScreenshot func(ctx context.Context, botID int64, receipt channel.Receipt) (string, error)

	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error
	MaxAttempts int
	Backoff     time.Duration

	mu     sync.Mutex
	firing map[int64]bool
}

// PublishIdempotencyKey derives the key from (bot, issue, content version,
// platform, scheduled instant).
func PublishIdempotencyKey(in PublishInput) string {
	in = in.normalized()
	scheduled := ""
	if in.ScheduledAt != nil {
		scheduled = in.ScheduledAt.Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"opsbot:publish:v1|%d|%d|%d|%s|%s",
		in.BotID, in.IssueID, in.ContentVersion, in.Platform, scheduled)))
	return hex.EncodeToString(sum[:])
}

// Publish dispatches in. A second call with the same idempotency key returns
// the existing record and never calls the adapter again.
func (p *Publisher) Publish(ctx context.Context, in PublishInput) (PublishResult, error) {
	in = in.normalized()
	if err := in.validate(); err != nil {
		return PublishResult{}, err
	}
	if err := p.configured(); err != nil {
		return PublishResult{}, err
	}

	key := in.idempotencyKey()
	rec, err := p.getRecord(ctx, in.BotID, key)
	switch {
	case err == nil:
		return p.resultFromRecord(rec, true), nil
	case !errors.Is(err, sql.ErrNoRows):
		return PublishResult{}, fmt.Errorf("opsbot: load publish record: %w", err)
	}

	if p.CheckBudget != nil {
		if err := p.CheckBudget(ctx, in.BotID); err != nil {
			return PublishResult{}, fmt.Errorf("opsbot: publish blocked: %w", err)
		}
	}

	if in.ScheduledAt != nil && in.ScheduledAt.After(p.now()) {
		return p.schedule(ctx, in, key)
	}

	rec, created, err := p.createRecord(ctx, in, key)
	if err != nil {
		return PublishResult{}, err
	}
	if !created {
		return p.resultFromRecord(rec, true), nil
	}
	return p.dispatch(ctx, in, key, rec.ID)
}

func (in PublishInput) normalized() PublishInput {
	if in.ContentVersion <= 0 {
		in.ContentVersion = 1
	}
	in.Platform = strings.TrimSpace(in.Platform)
	if in.Platform == "" {
		in.Platform = strings.TrimSpace(in.PackID)
	}
	in.PackID = strings.TrimSpace(in.PackID)
	in.AccountRef = strings.TrimSpace(in.AccountRef)
	if in.ScheduledAt != nil {
		at := in.ScheduledAt.UTC()
		in.ScheduledAt = &at
	}
	return in
}

func (in PublishInput) validate() error {
	switch {
	case in.BotID <= 0:
		return fmt.Errorf("%w: bot_id is required", ErrInvalidPublishInput)
	case in.IssueID <= 0:
		return fmt.Errorf("%w: issue_id is required", ErrInvalidPublishInput)
	}
	return nil
}

func (in PublishInput) idempotencyKey() string {
	if key := strings.TrimSpace(in.IdempotencyKey); key != "" {
		return key
	}
	return PublishIdempotencyKey(in)
}

func (p *Publisher) configured() error {
	switch {
	case p.Q == nil:
		return fmt.Errorf("%w: no publish store", ErrPublishNotConfigured)
	case p.DB == nil:
		return fmt.Errorf("%w: no database handle", ErrPublishNotConfigured)
	}
	return nil
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Publisher) getRecord(ctx context.Context, botID int64, key string) (store.OpsPublishRecord, error) {
	return p.Q.GetOpsPublishRecord(ctx, store.GetOpsPublishRecordParams{BotID: botID, IdempotencyKey: key})
}

// createRecord inserts the pending record. created is false when a concurrent
// caller won the unique constraint, in which case rec is the existing row.
func (p *Publisher) createRecord(ctx context.Context, in PublishInput, key string) (store.OpsPublishRecord, bool, error) {
	rec, err := p.Q.CreateOpsPublishRecord(ctx, store.CreateOpsPublishRecordParams{
		IssueID:        in.IssueID,
		BotID:          in.BotID,
		PackID:         in.PackID,
		AccountRef:     in.AccountRef,
		ContentVersion: in.ContentVersion,
		IdempotencyKey: key,
		Status:         PublishStatusPending,
	})
	if err == nil {
		return rec, true, nil
	}
	existing, gerr := p.getRecord(ctx, in.BotID, key)
	if gerr == nil {
		return existing, false, nil
	}
	return store.OpsPublishRecord{}, false, fmt.Errorf("opsbot: create publish record: %w", err)
}

// schedule persists a pending record and arms the deferred dispatch.
func (p *Publisher) schedule(ctx context.Context, in PublishInput, key string) (PublishResult, error) {
	if p.Schedule == nil {
		return PublishResult{}, fmt.Errorf("%w: %w", ErrPublishNotConfigured, ErrScheduleUnavailable)
	}

	rec, created, err := p.createRecord(ctx, in, key)
	if err != nil {
		return PublishResult{}, err
	}
	if !created {
		return p.resultFromRecord(rec, true), nil
	}

	encoded := publishRecordDetail{ScheduledAt: in.ScheduledAt}.encode()
	res := PublishResult{
		RecordID:    rec.ID,
		Status:      PublishStatusPending,
		Mode:        PublishModeScheduled,
		ScheduledAt: in.ScheduledAt,
		Receipt:     encoded,
	}

	if err := p.setStatus(ctx, rec.ID, PublishStatusPending, encoded, "", "", false); err != nil {
		return res, fmt.Errorf("opsbot: record publish schedule: %w", err)
	}

	run := func(runCtx context.Context) { p.runScheduled(runCtx, in, key) }
	if err := p.Schedule(ctx, *in.ScheduledAt, key, run); err != nil {
		return p.failPublish(ctx, in, key, rec.ID, fmt.Errorf("opsbot: register scheduled publish: %w", err), 0)
	}
	return res, nil
}

// runScheduled is the scheduler callback. It only fires a record that is still
// pending, so a publish finished by another path is never repeated. A
// process-local in-flight guard covers two timers firing at once: the record
// only flips to published after the adapter returns.
func (p *Publisher) runScheduled(ctx context.Context, in PublishInput, key string) {
	rec, err := p.getRecord(ctx, in.BotID, key)
	if err != nil || rec.Status != PublishStatusPending {
		return
	}
	if !p.beginFiring(rec.ID) {
		return
	}
	defer p.endFiring(rec.ID)
	_, _ = p.dispatch(ctx, in, key, rec.ID)
}

func (p *Publisher) beginFiring(recordID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.firing == nil {
		p.firing = make(map[int64]bool)
	}
	if p.firing[recordID] {
		return false
	}
	p.firing[recordID] = true
	return true
}

func (p *Publisher) endFiring(recordID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.firing, recordID)
}

// dispatch drives one platform publish and records its outcome.
func (p *Publisher) dispatch(ctx context.Context, in PublishInput, key string, recordID int64) (PublishResult, error) {
	if p.BotStatus != nil {
		status, err := p.BotStatus(ctx, in.BotID)
		if err != nil {
			return PublishResult{RecordID: recordID, Status: PublishStatusPending}, fmt.Errorf("opsbot: check bot status: %w", err)
		}
		if status != StatusActive {
			// A kill switch or pause must stop the send even for a record that
			// was queued before it; the record stays pending for a re-run.
			return PublishResult{RecordID: recordID, Status: PublishStatusPending},
				fmt.Errorf("%w: %s", ErrBotNotActive, status)
		}
	}
	adapter, err := p.adapterFor(ctx, in.BotID)
	if err != nil {
		return p.handleFailure(ctx, in, key, recordID, err, 0)
	}
	if caps := adapter.Capabilities(); !caps.Supports(channel.CapPublish) {
		return p.handleFailure(ctx, in, key, recordID,
			fmt.Errorf("%w: %s adapter cannot publish", channel.ErrCapabilityUnavailable, in.Platform), 0)
	}

	receipt, attempts, err := p.publishWithRetry(ctx, adapter, in)
	if err != nil {
		if ctx.Err() != nil {
			// The caller walked away; keep the record pending so it can be
			// retried instead of being recorded as a platform failure.
			return PublishResult{
				RecordID: recordID,
				Status:   PublishStatusPending,
				Mode:     PublishModeAdapter,
				Attempts: attempts,
			}, errors.Join(err, ctx.Err())
		}
		return p.handleFailure(ctx, in, key, recordID, err, attempts)
	}

	screenshot := in.ScreenshotPath
	if p.CaptureScreenshot != nil {
		if shot, cerr := p.CaptureScreenshot(ctx, in.BotID, receipt); cerr == nil && shot != "" {
			screenshot = shot
		}
	}

	detail := publishRecordDetail{ExternalID: receipt.ExternalID, URL: receipt.URL, Raw: truncatePublish(receipt.Raw, maxStoredReceiptRaw), Attempts: attempts}
	encoded := detail.encode()
	if err := p.setStatus(ctx, recordID, PublishStatusPublished, encoded, receipt.URL, screenshot, true); err != nil {
		return PublishResult{RecordID: recordID, Status: PublishStatusPending, Mode: PublishModeAdapter, Attempts: attempts},
			fmt.Errorf("opsbot: record publish receipt: %w", err)
	}

	p.emitEvent(ctx, in.IssueID, detail, fmt.Sprintf("published to %s: %s", publishPlatform(in), receipt.ExternalID))
	p.notify(ctx, in.BotID, "publish_succeeded", map[string]any{
		"record_id":   recordID,
		"issue_id":    in.IssueID,
		"platform":    publishPlatform(in),
		"external_id": receipt.ExternalID,
		"url":         receipt.URL,
	})

	now := p.now()
	return PublishResult{
		RecordID:       recordID,
		Status:         PublishStatusPublished,
		Mode:           PublishModeAdapter,
		Receipt:        encoded,
		URL:            receipt.URL,
		ScreenshotPath: screenshot,
		Attempts:       attempts,
		PublishedAt:    &now,
	}, nil
}

func (p *Publisher) adapterFor(ctx context.Context, botID int64) (channel.ChannelAdapter, error) {
	if p.AdapterFor != nil {
		adapter, err := p.AdapterFor(ctx, botID)
		if err != nil {
			return nil, err
		}
		if adapter == nil {
			return nil, fmt.Errorf("%w: no adapter for bot %d", channel.ErrCapabilityUnavailable, botID)
		}
		return adapter, nil
	}
	if p.Adapter != nil {
		return p.Adapter, nil
	}
	return nil, fmt.Errorf("%w: no channel adapter", ErrPublishNotConfigured)
}

// publishWithRetry calls EnsureSession + Publish up to MaxAttempts times,
// backing off between retryable failures. It returns the attempt count so the
// record and problem issue show how hard it tried.
func (p *Publisher) publishWithRetry(ctx context.Context, adapter channel.ChannelAdapter, in PublishInput) (channel.Receipt, int, error) {
	for attempts := 1; ; attempts++ {
		receipt, err := p.attempt(ctx, adapter, in)
		if err == nil {
			return receipt, attempts, nil
		}
		if !p.shouldRetry(err, attempts) {
			return channel.Receipt{}, attempts, err
		}
		if serr := p.sleep(ctx, p.backoff(attempts)); serr != nil {
			return channel.Receipt{}, attempts, errors.Join(err, serr)
		}
	}
}

func (p *Publisher) attempt(ctx context.Context, adapter channel.ChannelAdapter, in PublishInput) (channel.Receipt, error) {
	if err := adapter.EnsureSession(ctx, in.AccountRef); err != nil {
		return channel.Receipt{}, err
	}
	return adapter.Publish(ctx, in.Payload)
}

func (p *Publisher) shouldRetry(err error, attempts int) bool {
	if attempts >= p.maxAttempts() {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var adapterErr *channel.AdapterError
	if errors.As(err, &adapterErr) {
		return adapterErr.Retryable
	}
	// Unknown errors are treated as transient; the attempt cap bounds them.
	return true
}

func (p *Publisher) maxAttempts() int {
	if p.MaxAttempts > 0 {
		return p.MaxAttempts
	}
	return defaultPublishAttempts
}

func (p *Publisher) backoff(attempt int) time.Duration {
	base := p.Backoff
	if base <= 0 {
		base = defaultPublishBackoff
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxPublishBackoff {
			return maxPublishBackoff
		}
	}
	return d
}

func (p *Publisher) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// handleFailure degrades a capability/recipe gap to a manual package and
// records everything else as a terminal failure with a concrete issue.
func (p *Publisher) handleFailure(ctx context.Context, in PublishInput, key string, recordID int64, cause error, attempts int) (PublishResult, error) {
	if p.OnAdapterFailure != nil {
		p.OnAdapterFailure(ctx, in.BotID, cause)
	}
	if isDegradablePublishError(cause) {
		return p.degradeToManual(ctx, in, key, recordID, cause, attempts)
	}
	return p.failPublish(ctx, in, key, recordID, cause, attempts)
}

func (p *Publisher) degradeToManual(ctx context.Context, in PublishInput, key string, recordID int64, cause error, attempts int) (PublishResult, error) {
	if p.Manual == nil {
		return p.failPublish(ctx, in, key, recordID, errors.Join(cause, ErrNoManualFallback), attempts)
	}

	receipt, err := p.Manual.Publish(ctx, channel.ManualPublishPayload{
		Bot:            strconv.FormatInt(in.BotID, 10),
		IdempotencyKey: key,
		Platform:       in.Platform,
		Title:          in.Title,
		Body:           in.Body,
		MediaPaths:     append([]string(nil), in.MediaPaths...),
		Params:         in.Params,
	})
	if err != nil {
		return p.failPublish(ctx, in, key, recordID, errors.Join(cause, err), attempts)
	}

	dir := strings.TrimPrefix(receipt.ExternalID, "manual:")
	detail := publishRecordDetail{
		PackageDir: dir,
		Code:       publishErrorCode(cause),
		Attempts:   attempts,
		Error:      truncatePublish(cause.Error(), maxStoredReceiptRaw),
	}
	encoded := detail.encode()
	res := PublishResult{
		RecordID:       recordID,
		Status:         PublishStatusManualPending,
		Mode:           PublishModeManualPackage,
		Receipt:        encoded,
		PackageDir:     dir,
		Attempts:       attempts,
		ScreenshotPath: in.ScreenshotPath,
	}
	if err := p.setStatus(ctx, recordID, PublishStatusManualPending, encoded, "", in.ScreenshotPath, false); err != nil {
		return res, fmt.Errorf("opsbot: record manual publish hand-off: %w", err)
	}

	p.emitEvent(ctx, in.IssueID, detail, fmt.Sprintf("publish degraded to manual package: %s", dir))
	p.notify(ctx, in.BotID, "publish_manual_pending", map[string]any{
		"record_id":    recordID,
		"issue_id":     in.IssueID,
		"platform":     publishPlatform(in),
		"package_dir":  dir,
		"reason":       publishErrorCode(cause),
		"retry_count":  attempts,
		"manual_title": in.Title,
	})
	return res, nil
}

func (p *Publisher) failPublish(ctx context.Context, in PublishInput, key string, recordID int64, cause error, attempts int) (PublishResult, error) {
	code := publishErrorCode(cause)
	detail := publishRecordDetail{
		Code:      code,
		Retryable: isRetryablePublishError(cause),
		Attempts:  attempts,
		Error:     truncatePublish(cause.Error(), maxStoredReceiptRaw),
	}
	encoded := detail.encode()

	var errs []error
	if err := p.setStatus(ctx, recordID, PublishStatusFailed, encoded, "", in.ScreenshotPath, false); err != nil {
		errs = append(errs, fmt.Errorf("opsbot: record publish failure: %w", err))
	}

	problemID := int64(0)
	if p.Problems != nil {
		id, perr := p.Problems.OpenPublishProblem(ctx, PublishProblem{
			BotID:          in.BotID,
			IssueID:        in.IssueID,
			RecordID:       recordID,
			Platform:       publishPlatform(in),
			AccountRef:     in.AccountRef,
			IdempotencyKey: key,
			Code:           code,
			Retryable:      isRetryablePublishError(cause),
			Attempts:       attempts,
			Error:          cause.Error(),
		})
		if perr != nil {
			errs = append(errs, fmt.Errorf("opsbot: open publish problem: %w", perr))
		} else {
			problemID = id
		}
	}

	res := PublishResult{
		RecordID:       recordID,
		Status:         PublishStatusFailed,
		Mode:           PublishModeFailed,
		Receipt:        encoded,
		ProblemIssueID: problemID,
		Attempts:       attempts,
		ScreenshotPath: in.ScreenshotPath,
	}

	p.emitEvent(ctx, in.IssueID, detail, fmt.Sprintf("publish failed (%s): %s", code, truncatePublish(cause.Error(), 160)))
	p.notify(ctx, in.BotID, "publish_failed", map[string]any{
		"record_id":  recordID,
		"issue_id":   in.IssueID,
		"platform":   publishPlatform(in),
		"code":       code,
		"attempts":   attempts,
		"retryable":  isRetryablePublishError(cause),
		"problem_id": problemID,
		"error":      cause.Error(),
	})

	errs = append(errs, fmt.Errorf("opsbot: publish failed: %w", cause))
	return res, errors.Join(errs...)
}

func (p *Publisher) resultFromRecord(rec store.OpsPublishRecord, duplicate bool) PublishResult {
	var detail publishRecordDetail
	if strings.TrimSpace(rec.Receipt) != "" {
		_ = json.Unmarshal([]byte(rec.Receipt), &detail)
	}
	res := PublishResult{
		RecordID:       rec.ID,
		Status:         rec.Status,
		Receipt:        rec.Receipt,
		URL:            rec.Url,
		ScreenshotPath: rec.ScreenshotPath,
		PackageDir:     detail.PackageDir,
		Attempts:       detail.Attempts,
		ScheduledAt:    detail.ScheduledAt,
		Duplicate:      duplicate,
	}
	switch rec.Status {
	case PublishStatusPublished:
		res.Mode = PublishModeAdapter
		if rec.PublishedAt.Valid {
			published := rec.PublishedAt.Time
			res.PublishedAt = &published
		}
	case PublishStatusManualPending:
		res.Mode = PublishModeManualPackage
	case PublishStatusFailed:
		res.Mode = PublishModeFailed
	default:
		res.Mode = PublishModeScheduled
	}
	return res
}

// setStatus persists one publish outcome. published_at is stamped only for a
// published record; the generated query would stamp it on every transition.
func (p *Publisher) setStatus(ctx context.Context, recordID int64, status, receipt, url, screenshot string, published bool) error {
	query := updatePublishRecordUnpublished
	if published {
		query = updatePublishRecordPublished
	}
	_, err := p.DB.ExecContext(ctx, query, status, receipt, url, screenshot, recordID)
	return err
}

const (
	updatePublishRecordPublished = `UPDATE ops_publish_records SET
    status = ?, receipt = ?, url = ?, screenshot_path = ?,
    published_at = CURRENT_TIMESTAMP
WHERE id = ?`
	updatePublishRecordUnpublished = `UPDATE ops_publish_records SET
    status = ?, receipt = ?, url = ?, screenshot_path = ?,
    published_at = NULL
WHERE id = ?`
)

// emitEvent appends the publish outcome to the issue timeline. Best-effort: a
// timeline write must not turn a successful publish into a failed one.
func (p *Publisher) emitEvent(ctx context.Context, issueID int64, detail publishRecordDetail, summary string) {
	if p.Events == nil {
		return
	}
	raw := detail.encode()
	_ = p.Events.InsertIssueExecEvent(ctx, store.InsertIssueExecEventParams{
		IssueID:    issueID,
		Kind:       "terminal",
		Summary:    summary,
		DetailJson: sql.NullString{String: raw, Valid: raw != ""},
	})
}

func (p *Publisher) notify(ctx context.Context, botID int64, event string, detail map[string]any) {
	if p.Notify == nil {
		return
	}
	_ = p.Notify(ctx, botID, event, detail)
}

// publishRecordDetail is the structured body stored in ops_publish_records.receipt.
// One shape covers every status so the record itself is the audit trail: the
// scheduled instant, the platform receipt, the manual package directory, or the
// failure context.
type publishRecordDetail struct {
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	ExternalID  string     `json:"external_id,omitempty"`
	URL         string     `json:"url,omitempty"`
	Raw         string     `json:"raw,omitempty"`
	PackageDir  string     `json:"package_dir,omitempty"`
	Code        string     `json:"code,omitempty"`
	Retryable   bool       `json:"retryable,omitempty"`
	Attempts    int        `json:"attempts,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (d publishRecordDetail) encode() string {
	raw, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	return string(raw)
}

// isDegradablePublishError reports whether the only useful next step is a
// manual package. AdapterError.Is reports ErrCapabilityUnavailable for every
// adapter failure, so the code (not the sentinel) decides.
func isDegradablePublishError(err error) bool {
	if !errors.Is(err, channel.ErrCapabilityUnavailable) {
		return false
	}
	var adapterErr *channel.AdapterError
	if !errors.As(err, &adapterErr) {
		return true
	}
	return degradableAdapterCodes[adapterErr.Code]
}

func isRetryablePublishError(err error) bool {
	var adapterErr *channel.AdapterError
	if errors.As(err, &adapterErr) {
		return adapterErr.Retryable
	}
	return false
}

func publishErrorCode(err error) string {
	var adapterErr *channel.AdapterError
	if errors.As(err, &adapterErr) && adapterErr.Code != "" {
		return adapterErr.Code
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "error"
}

func publishPlatform(in PublishInput) string {
	if in.Platform != "" {
		return in.Platform
	}
	return "channel"
}

func truncatePublish(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
