package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Account risk signals (design 2026-11-06 §9, §15).
const (
	RiskSignalLoginExpired  = "login_expired"
	RiskSignalRateLimited   = "rate_limited"
	RiskSignalCaptcha       = "captcha"
	RiskSignalBanned        = "banned"
	RiskSignalAdapterFailed = "adapter_failed"
	RiskSignalKillSwitch    = "kill_switch"
)

// RiskAction is what HandleAdapterError tells the caller to do next.
type RiskAction string

const (
	// RiskActionRetry re-attempts the call after RetryAfter.
	RiskActionRetry RiskAction = "retry"
	// RiskActionPause stops the account: the failure is account-level risk.
	RiskActionPause RiskAction = "pause"
	// RiskActionDegrade hands the operation to the manual package path.
	RiskActionDegrade RiskAction = "degrade"
	// RiskActionStop gives up without pausing: the caller's context ended, so
	// the failure says nothing about the account.
	RiskActionStop RiskAction = "stop"
)

const (
	riskEngagementScanLimit = 500
	riskRetryBase           = 500 * time.Millisecond
	riskRetryJitter         = 1500 * time.Millisecond
	riskMarkerPrefix        = "risk:"
)

var (
	// ErrRiskNotConfigured is returned when a required dependency is missing.
	ErrRiskNotConfigured = errors.New("opsbot: risk service not configured")
	// ErrInvalidRiskSignal is returned for a signal Evaluate cannot classify.
	ErrInvalidRiskSignal = errors.New("opsbot: invalid risk signal")
	// ErrReloginRequired is returned by Resume until the account's login has
	// been re-verified. A paused account is never resumed on stale credentials.
	ErrReloginRequired = errors.New("opsbot: re-login required before resume")
)

// RiskBots is the bot status surface Risk needs. *Service satisfies it.
type RiskBots interface {
	Get(ctx context.Context, botID int64) (store.OpsBot, error)
	SetStatus(ctx context.Context, botID int64, status string) (store.OpsBot, error)
}

var _ RiskBots = (*Service)(nil)

// RiskEngagements is the engagement surface the kill switch drains.
// *store.Queries satisfies it.
type RiskEngagements interface {
	ListOpsEngagementsByStatus(ctx context.Context, arg store.ListOpsEngagementsByStatusParams) ([]store.OpsEngagement, error)
	UpdateOpsEngagementStatus(ctx context.Context, arg store.UpdateOpsEngagementStatusParams) (store.OpsEngagement, error)
}

var _ RiskEngagements = (*store.Queries)(nil)

// RiskSessions persists the account's login/health/risk state.
// *browser.SessionManager satisfies it.
type RiskSessions interface {
	State(ctx context.Context, botID int64, backend string) (store.OpsBrowserSession, error)
	Update(ctx context.Context, botID int64, backend string, patch browser.StatePatch) (store.OpsBrowserSession, error)
}

var _ RiskSessions = (*browser.SessionManager)(nil)

// RiskOutcome reports what one risk evaluation did to an account.
type RiskOutcome struct {
	BotID              int64  `json:"bot_id"`
	Signal             string `json:"signal"`
	Status             string `json:"status"`
	RiskState          string `json:"risk_state,omitempty"`
	Paused             bool   `json:"paused"`
	Notified           bool   `json:"notified"`
	Reason             string `json:"reason"`
	ApprovalsCancelled int    `json:"approvals_cancelled,omitempty"`
	EngagementsCleared int    `json:"engagements_cleared,omitempty"`
}

// RiskVerdict is the routing decision for one adapter failure.
type RiskVerdict struct {
	Action     RiskAction    `json:"action"`
	Code       string        `json:"code"`
	Signal     string        `json:"signal,omitempty"`
	Status     string        `json:"status,omitempty"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	Notified   bool          `json:"notified"`
	Reason     string        `json:"reason,omitempty"`
}

// Risk is the account-level safety controller: the kill switch, the risk-signal
// evaluator, adapter-failure routing and the re-login-gated resume.
//
// Signal de-duplication is durable: the last notified signal is also written to
// the browser session's last_error as "risk:<signal>", so a restarted process
// does not re-page the operator for an incident it already reported. A resume
// clears the marker, so the next incident notifies again.
type Risk struct {
	Bots        RiskBots
	Approvals   ApprovalCanceller
	Engagements RiskEngagements
	Sessions    RiskSessions
	// Backend selects the browser backend session to update. Empty means local.
	Backend string

	// Notify pages the operator. Nil disables notifications.
	Notify func(ctx context.Context, botID int64, reason string) error
	// VerifyLogin probes the platform session before a resume. When nil, Resume
	// falls back to the persisted session login_status (a fresh or unknown
	// session cannot be resumed).
	VerifyLogin func(ctx context.Context, botID int64) error

	Now  func() time.Time
	Rand *rand.Rand

	mu         sync.Mutex
	lastSignal map[int64]string
}

// Kill is the account kill switch: disable the bot first so no new action can
// start, then cancel its pending approvals and drain replies already approved
// for sending. Notification is best-effort; the kill itself always stands.
func (r *Risk) Kill(ctx context.Context, botID int64, reason string) (RiskOutcome, error) {
	if r == nil || r.Bots == nil {
		return RiskOutcome{}, ErrRiskNotConfigured
	}
	if _, err := r.Bots.Get(ctx, botID); err != nil {
		return RiskOutcome{}, err
	}

	ev := RiskOutcome{
		BotID:  botID,
		Signal: RiskSignalKillSwitch,
		Status: StatusDisabled,
		Paused: true,
		Reason: killReason(reason),
	}

	updated, err := r.Bots.SetStatus(ctx, botID, StatusDisabled)
	if err != nil {
		return ev, err
	}
	ev.Status = updated.Status

	var errs []error
	if r.Approvals != nil {
		n, err := r.Approvals.CancelByBot(ctx, botID)
		ev.ApprovalsCancelled = n
		if err != nil {
			errs = append(errs, fmt.Errorf("opsbot: kill switch cancel approvals: %w", err))
		}
	}

	cleared, err := r.clearQueuedReplies(ctx, botID)
	ev.EngagementsCleared = cleared
	if err != nil {
		errs = append(errs, err)
	}

	r.forgetSignal(ctx, botID)
	if err := r.notify(ctx, botID, ev.Reason); err != nil {
		errs = append(errs, fmt.Errorf("opsbot: kill switch notify: %w", err))
	} else {
		ev.Notified = r.Notify != nil
	}
	return ev, errors.Join(errs...)
}

// Evaluate applies a platform risk signal: it pauses the account, records the
// signal on the browser session, and pages the operator once per incident. A
// disabled account stays disabled and is not paged again; a repeated signal on
// an already-paused account is recorded but does not notify twice.
func (r *Risk) Evaluate(ctx context.Context, botID int64, signal string) (RiskOutcome, error) {
	if r == nil || r.Bots == nil {
		return RiskOutcome{}, ErrRiskNotConfigured
	}
	signal = strings.TrimSpace(signal)
	patch, ok := riskStatePatch(signal)
	if !ok {
		return RiskOutcome{}, fmt.Errorf("%w: %q", ErrInvalidRiskSignal, signal)
	}

	bot, err := r.Bots.Get(ctx, botID)
	if err != nil {
		return RiskOutcome{}, err
	}

	ev := RiskOutcome{
		BotID:     botID,
		Signal:    signal,
		Status:    bot.Status,
		RiskState: riskStateFor(signal),
		Reason:    riskReason(signal),
	}

	if bot.Status == StatusDisabled {
		// A kill switch already stopped every action; do not downgrade it to
		// paused (which would make it resumable) or page twice.
		ev.Paused = true
		return ev, r.applySession(ctx, botID, patch)
	}

	seen := r.signalSeen(ctx, botID, signal, bot.Status)
	if bot.Status != StatusPaused {
		updated, err := r.Bots.SetStatus(ctx, botID, StatusPaused)
		if err != nil {
			return ev, err
		}
		ev.Status = updated.Status
	}
	ev.Paused = true

	var errs []error
	if err := r.applySession(ctx, botID, patch); err != nil {
		errs = append(errs, err)
	}
	if !seen {
		if err := r.notify(ctx, botID, ev.Reason); err != nil {
			errs = append(errs, fmt.Errorf("opsbot: risk notify: %w", err))
		} else {
			ev.Notified = r.Notify != nil
			r.markSignal(ctx, botID, signal)
		}
	}
	return ev, errors.Join(errs...)
}

// HandleAdapterError routes one platform failure. Account-risk codes (login
// expired, rate limited, captcha, blocked) pause the account even when the
// adapter marks them retryable; recipe/capability gaps degrade to a manual
// package; anything else retries or, when not retryable, pauses.
func (r *Risk) HandleAdapterError(ctx context.Context, botID int64, err error) (RiskVerdict, error) {
	if r == nil || r.Bots == nil {
		return RiskVerdict{}, ErrRiskNotConfigured
	}
	if err == nil {
		return RiskVerdict{}, fmt.Errorf("%w: nil adapter error", ErrInvalidInput)
	}

	code := publishErrorCode(err)
	verdict := RiskVerdict{Code: code}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		verdict.Action = RiskActionStop
		return verdict, nil
	}

	if signal, ok := RiskSignalForCode(code); ok {
		ev, evalErr := r.Evaluate(ctx, botID, signal)
		verdict.Action = RiskActionPause
		verdict.Signal = signal
		verdict.Status = ev.Status
		verdict.Notified = ev.Notified
		verdict.Reason = ev.Reason
		return verdict, evalErr
	}

	if isDegradablePublishError(err) {
		verdict.Action = RiskActionDegrade
		verdict.Reason = fmt.Sprintf(
			"平台自动化不可用（%s），已降级为人工发布包；请运营人按包手工发布", code)
		if notifyErr := r.notify(ctx, botID, verdict.Reason); notifyErr != nil {
			return verdict, fmt.Errorf("opsbot: degrade alert: %w", notifyErr)
		}
		verdict.Notified = r.Notify != nil
		if bot, getErr := r.Bots.Get(ctx, botID); getErr == nil {
			verdict.Status = bot.Status
		}
		return verdict, nil
	}

	if isRetryableAdapterError(err) {
		verdict.Action = RiskActionRetry
		verdict.RetryAfter = r.retryAfter()
		return verdict, nil
	}

	ev, evalErr := r.Evaluate(ctx, botID, RiskSignalAdapterFailed)
	verdict.Action = RiskActionPause
	verdict.Signal = RiskSignalAdapterFailed
	verdict.Status = ev.Status
	verdict.Notified = ev.Notified
	verdict.Reason = ev.Reason
	return verdict, evalErr
}

// Resume reactivates a paused or disabled account, but only after the login
// check passes. When VerifyLogin is wired it is authoritative; otherwise the
// persisted session must report a valid login. A missing session row means
// there is nothing to check (API-only packs log in per call), so the resume is
// allowed. Resume clears the risk marker so the next incident notifies again.
func (r *Risk) Resume(ctx context.Context, botID int64) (store.OpsBot, error) {
	if r == nil || r.Bots == nil {
		return store.OpsBot{}, ErrRiskNotConfigured
	}
	bot, err := r.Bots.Get(ctx, botID)
	if err != nil {
		return store.OpsBot{}, err
	}
	if bot.Status == StatusActive {
		return bot, nil
	}
	if err := r.verifyLogin(ctx, botID); err != nil {
		return bot, err
	}

	updated, err := r.Bots.SetStatus(ctx, botID, StatusActive)
	if err != nil {
		return bot, err
	}
	r.clearSignal(ctx, botID)
	return updated, nil
}

// clearQueuedReplies rejects replies that were already approved and are only
// waiting for the sender, so a kill cannot be followed by an in-flight reply.
// Suggested items are left alone: their approval cards are cancelled and a new
// card is created on demand, so nothing can send without a fresh decision.
func (r *Risk) clearQueuedReplies(ctx context.Context, botID int64) (int, error) {
	if r.Engagements == nil {
		return 0, nil
	}
	now := r.now()
	cleared := 0
	for {
		rows, err := r.Engagements.ListOpsEngagementsByStatus(ctx, store.ListOpsEngagementsByStatusParams{
			BotID:  botID,
			Status: EngagementApproved,
			Limit:  riskEngagementScanLimit,
		})
		if err != nil {
			return cleared, fmt.Errorf("opsbot: kill switch list queued replies: %w", err)
		}
		if len(rows) == 0 {
			return cleared, nil
		}
		for _, row := range rows {
			_, err := r.Engagements.UpdateOpsEngagementStatus(ctx, store.UpdateOpsEngagementStatusParams{
				Status:            EngagementRejected,
				Category:          row.Category,
				RiskLevel:         row.RiskLevel,
				SuggestedReply:    row.SuggestedReply,
				ApprovalRequestID: row.ApprovalRequestID,
				ReplyText:         row.ReplyText,
				Receipt:           row.Receipt,
				DecidedBy:         row.DecidedBy,
				DecidedAt:         sql.NullTime{Time: now, Valid: true},
				ID:                row.ID,
				Status_2:          EngagementApproved,
			})
			if errors.Is(err, sql.ErrNoRows) {
				// Another worker moved it; it is no longer queued.
				continue
			}
			if err != nil {
				return cleared, fmt.Errorf("opsbot: kill switch clear engagement %d: %w", row.ID, err)
			}
			cleared++
		}
	}
}

func (r *Risk) verifyLogin(ctx context.Context, botID int64) error {
	if r.VerifyLogin != nil {
		if err := r.VerifyLogin(ctx, botID); err != nil {
			r.markLoginExpired(ctx, botID, err)
			return fmt.Errorf("%w: %v", ErrReloginRequired, err)
		}
		return nil
	}
	if r.Sessions == nil {
		return nil
	}
	sess, err := r.Sessions.State(ctx, botID, r.backend())
	if errors.Is(err, browser.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opsbot: resume check session: %w", err)
	}
	if sess.LoginStatus != browser.LoginValid {
		return fmt.Errorf("%w: login status is %q", ErrReloginRequired, sess.LoginStatus)
	}
	return nil
}

func (r *Risk) markLoginExpired(ctx context.Context, botID int64, cause error) {
	if r.Sessions == nil {
		return
	}
	expired := browser.LoginExpired
	degraded := browser.HealthDegraded
	msg := cause.Error()
	_, _ = r.Sessions.Update(ctx, botID, r.backend(), browser.StatePatch{
		LoginStatus: &expired,
		Health:      &degraded,
		LastError:   &msg,
	})
}

func (r *Risk) applySession(ctx context.Context, botID int64, patch browser.StatePatch) error {
	if r.Sessions == nil {
		return nil
	}
	if _, err := r.Sessions.Update(ctx, botID, r.backend(), patch); err != nil {
		// API-only packs have no browser session row; the account pause is
		// still authoritative, so a missing session is not a failure.
		if errors.Is(err, browser.ErrSessionNotFound) {
			return nil
		}
		return fmt.Errorf("opsbot: record risk state: %w", err)
	}
	return nil
}

// signalSeen reports whether this incident was already notified. A fresh
// (active) bot always starts a new incident; otherwise the in-memory marker and
// the persisted session marker both suppress a duplicate page.
func (r *Risk) signalSeen(ctx context.Context, botID int64, signal, status string) bool {
	if status == StatusActive {
		return false
	}
	r.mu.Lock()
	last := r.lastSignal[botID]
	r.mu.Unlock()
	if last == signal {
		return true
	}
	if r.Sessions == nil {
		return false
	}
	sess, err := r.Sessions.State(ctx, botID, r.backend())
	if err != nil {
		return false
	}
	return sess.LastError == riskMarker(signal)
}

func (r *Risk) markSignal(ctx context.Context, botID int64, signal string) {
	r.mu.Lock()
	if r.lastSignal == nil {
		r.lastSignal = make(map[int64]string)
	}
	r.lastSignal[botID] = signal
	r.mu.Unlock()

	if r.Sessions == nil {
		return
	}
	marker := riskMarker(signal)
	_, _ = r.Sessions.Update(ctx, botID, r.backend(), browser.StatePatch{LastError: &marker})
}

// forgetSignal drops the in-memory marker only. Used by Kill, whose explicit
// operator action always notifies.
func (r *Risk) forgetSignal(ctx context.Context, botID int64) {
	r.mu.Lock()
	delete(r.lastSignal, botID)
	r.mu.Unlock()

	if r.Sessions == nil {
		return
	}
	empty := ""
	_, _ = r.Sessions.Update(ctx, botID, r.backend(), browser.StatePatch{LastError: &empty})
}

// clearSignal ends the current incident: the marker is removed and the session
// is marked logged-in again, so the next risk signal notifies the operator.
func (r *Risk) clearSignal(ctx context.Context, botID int64) {
	r.forgetSignal(ctx, botID)
	if r.Sessions == nil {
		return
	}
	valid := browser.LoginValid
	normal := browser.RiskNormal
	empty := ""
	_, _ = r.Sessions.Update(ctx, botID, r.backend(), browser.StatePatch{
		LoginStatus: &valid,
		RiskState:   &normal,
		LastError:   &empty,
	})
}

func (r *Risk) notify(ctx context.Context, botID int64, reason string) error {
	if r.Notify == nil {
		return nil
	}
	return r.Notify(ctx, botID, reason)
}

func (r *Risk) retryAfter() time.Duration {
	span := int64(riskRetryJitter)
	if span <= 0 {
		return riskRetryBase
	}
	if r.Rand != nil {
		return riskRetryBase + time.Duration(r.Rand.Int63n(span))
	}
	return riskRetryBase + time.Duration(rand.Int63n(span))
}

func (r *Risk) backend() string {
	if r.Backend != "" {
		return r.Backend
	}
	return browser.BackendLocal
}

func (r *Risk) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// riskStatePatch is the browser-session state each signal records.
func riskStatePatch(signal string) (browser.StatePatch, bool) {
	expired := browser.LoginExpired
	degraded := browser.HealthDegraded
	down := browser.HealthDown
	throttled := browser.RiskThrottled
	blocked := browser.RiskBlocked
	switch signal {
	case RiskSignalLoginExpired:
		return browser.StatePatch{LoginStatus: &expired, Health: &degraded}, true
	case RiskSignalRateLimited:
		return browser.StatePatch{RiskState: &throttled, Health: &degraded}, true
	case RiskSignalCaptcha:
		return browser.StatePatch{RiskState: &blocked, Health: &degraded}, true
	case RiskSignalBanned:
		return browser.StatePatch{RiskState: &blocked, Health: &down, LoginStatus: &expired}, true
	case RiskSignalAdapterFailed:
		return browser.StatePatch{Health: &degraded}, true
	default:
		return browser.StatePatch{}, false
	}
}

func riskStateFor(signal string) string {
	switch signal {
	case RiskSignalRateLimited:
		return browser.RiskThrottled
	case RiskSignalCaptcha, RiskSignalBanned:
		return browser.RiskBlocked
	default:
		return browser.RiskNormal
	}
}

func riskReason(signal string) string {
	switch signal {
	case RiskSignalLoginExpired:
		return "登录态失效，账号已暂停自动动作；请运营人重新登录后用 /resume 恢复"
	case RiskSignalRateLimited:
		return "触发平台限流，账号已暂停自动动作；请降低频率后再恢复"
	case RiskSignalCaptcha:
		return "出现验证码挑战，账号已暂停自动动作；请人工完成验证后再恢复"
	case RiskSignalBanned:
		return "账号被平台封禁，已暂停全部自动动作；请检查账号状态"
	case RiskSignalAdapterFailed:
		return "平台操作持续失败，账号已暂停自动动作；请检查后重试"
	default:
		return "风控信号 " + signal + "：账号已暂停自动动作"
	}
}

func killReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Kill switch 已触发：账号全部自动动作已停止，待处理审批与待发送回复已取消"
	}
	return fmt.Sprintf("Kill switch 已触发（%s）：账号全部自动动作已停止，待处理审批与待发送回复已取消", reason)
}

func riskMarker(signal string) string { return riskMarkerPrefix + signal }

// RiskSignalForCode maps a platform error code to the risk signal it implies.
// Codes absent here are not account-level risk, so callers can route only
// genuine account-risk failures to Evaluate.
func RiskSignalForCode(code string) (string, bool) {
	switch code {
	case channel.CodeLoginExpired, channel.CodeCredentialInvalid:
		return RiskSignalLoginExpired, true
	case channel.CodeRateLimited:
		return RiskSignalRateLimited, true
	case RiskSignalCaptcha:
		return RiskSignalCaptcha, true
	case channel.CodeRiskBlocked, RiskSignalBanned:
		return RiskSignalBanned, true
	default:
		return "", false
	}
}

// isRetryableAdapterError treats unknown errors as transient, mirroring
// Publisher.shouldRetry: only an explicit re-queue-able adapter error retries,
// and plain errors are bounded by the caller's attempt cap.
func isRetryableAdapterError(err error) bool {
	var adapterErr *channel.AdapterError
	if errors.As(err, &adapterErr) {
		return adapterErr.Retryable
	}
	return true
}
