package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Engagement statuses (design 2026-11-06 section 7.3).
const (
	EngagementNew        = "new"
	EngagementClassified = "classified"
	EngagementSuggested  = "suggested"
	EngagementApproved   = "approved"
	EngagementSent       = "sent"
	EngagementRejected   = "rejected"
	EngagementEscalated  = "escalated"
	EngagementFailed     = "failed"
)

// Engagement categories.
const (
	CategoryInquiry   = "inquiry"
	CategoryPraise    = "praise"
	CategoryComplaint = "complaint"
	CategoryNegative  = "negative"
	CategorySpam      = "spam"
	CategoryLead      = "lead"
)

// Engagement risk levels.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

const (
	defaultReplyHourlyLimit = 20
	defaultEngagementTries  = 3
	defaultEngagementRetry  = 200 * time.Millisecond
	// rateLimitScanLimit bounds the fallback scan when no *store.DB is wired.
	rateLimitScanLimit = 1000
)

var (
	// ErrEngagementNotConfigured is returned when a required dependency is missing.
	ErrEngagementNotConfigured = errors.New("opsbot: engagement service not configured")
	// ErrEngagementNotFound is returned when the id does not name an engagement
	// the caller's bot owns.
	ErrEngagementNotFound = errors.New("opsbot: engagement not found")
	// ErrEngagementStateChanged is returned when a transition races another
	// worker or the row is not in the state the operation requires.
	ErrEngagementStateChanged = errors.New("opsbot: engagement state changed")
	// ErrHighRiskEscalated is returned when a reply is attempted on an
	// escalated (high-risk) engagement. High-risk items are never auto-sent.
	ErrHighRiskEscalated = errors.New("opsbot: high-risk engagement is escalated and cannot be sent")
	// ErrRateLimited is returned when the bot's hourly send cap is reached. The
	// engagement stays approved and is retried in the next window.
	ErrRateLimited = errors.New("opsbot: reply hourly limit reached")
	// ErrBotNotActive is returned when the bot is paused or disabled.
	ErrBotNotActive = errors.New("opsbot: bot is not active")
	// ErrEngagementRejected is returned when a rejected engagement is sent.
	ErrEngagementRejected = errors.New("opsbot: engagement was rejected")
)

// EngagementStore is the durable half of the engagement service.
// *store.Queries satisfies it.
type EngagementStore interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
	GetOpsEngagement(ctx context.Context, id int64) (store.OpsEngagement, error)
	UpsertOpsEngagement(ctx context.Context, arg store.UpsertOpsEngagementParams) (store.OpsEngagement, error)
	UpdateOpsEngagementStatus(ctx context.Context, arg store.UpdateOpsEngagementStatusParams) (store.OpsEngagement, error)
	ListOpsEngagementsByStatus(ctx context.Context, arg store.ListOpsEngagementsByStatusParams) ([]store.OpsEngagement, error)
}

var _ EngagementStore = (*store.Queries)(nil)

// EngagementApprovals is the approval-card surface the engagement service
// needs. *Approval satisfies it.
type EngagementApprovals interface {
	Create(ctx context.Context, in ApprovalInput) (ApprovalRequest, error)
	Decide(ctx context.Context, id int64, d Decision) (ApprovalRequest, error)
	Get(ctx context.Context, id int64) (ApprovalRequest, error)
}

var _ EngagementApprovals = (*Approval)(nil)

// Engagement runs the comment / DM lifecycle: pull, dedupe, classify, suggest,
// approve, send, receipt, escalate.
//
// Trust boundary: like the rest of the opsbot service layer, Engagement does
// not authenticate callers. Bot-scoped methods re-check that the engagement
// belongs to ref.ID, but the entry layer must first resolve ref.ID from a bot
// the authenticated principal owns (see OpsBotHandler.botForID). Send takes no
// BotRef, so its caller must authorize the engagement id before calling.
type Engagement struct {
	Q  EngagementStore
	DB *store.DB

	// Adapter serves pull and reply. AdapterFor takes precedence so a caller
	// can resolve the adapter per bot; Adapter is the single-adapter fallback.
	AdapterFor func(ctx context.Context, botID int64) (channel.ChannelAdapter, error)
	Adapter    channel.ChannelAdapter

	Approvals EngagementApprovals

	// Generate overrides the built-in suggestion templates. Production wires
	// it to the pack prompts + LLM; nil keeps the deterministic templates.
	Generate func(ctx context.Context, bot store.OpsBot, cur store.OpsEngagement) (string, error)
	// OnEscalated is notified after an engagement is escalated. Best-effort:
	// the state is already committed and an error here does not undo it.
	OnEscalated func(ctx context.Context, cur store.OpsEngagement, reason string) error

	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error
	MaxAttempts int
	Backoff     time.Duration
}

// Pull fetches the next page of interactions from the bot's channel and
// upserts them. The dedupe key is (bot_id, platform_engagement_id), so a
// replayed page updates the existing row instead of creating a duplicate.
func (e *Engagement) Pull(ctx context.Context, ref BotRef, cursor string) ([]store.OpsEngagement, error) {
	if e.Q == nil {
		return nil, ErrEngagementNotConfigured
	}
	adapter, err := e.adapterFor(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	if !adapter.Capabilities().Supports(channel.CapEngagements) {
		return nil, &channel.AdapterError{
			Capability: channel.CapEngagements,
			Code:       "capability_unavailable",
			Err:        channel.ErrCapabilityUnavailable,
		}
	}

	items, err := adapter.ListEngagements(ctx, cursor)
	if err != nil {
		return nil, err
	}
	out := make([]store.OpsEngagement, 0, len(items))
	for _, item := range items {
		extID := strings.TrimSpace(item.ExtID)
		if extID == "" {
			continue
		}
		kind := strings.TrimSpace(item.Kind)
		if kind == "" {
			kind = "comment"
		}
		row, err := e.Q.UpsertOpsEngagement(ctx, store.UpsertOpsEngagementParams{
			BotID:                ref.ID,
			PlatformEngagementID: extID,
			Kind:                 kind,
			Author:               item.Author,
			Text:                 item.Text,
		})
		if err != nil {
			return nil, fmt.Errorf("upsert engagement %s: %w", extID, err)
		}
		out = append(out, row)
	}
	return out, nil
}

// Classify assigns a category and risk level. A high-risk engagement is
// escalated instead of continuing down the reply path.
func (e *Engagement) Classify(ctx context.Context, ref BotRef, id int64) error {
	cur, err := e.engagementFor(ctx, ref, id)
	if err != nil {
		return err
	}
	if cur.Status != EngagementNew {
		return fmt.Errorf("%w: classify from %q", ErrEngagementStateChanged, cur.Status)
	}

	category, risk := classifyEngagement(cur.Text)
	next := EngagementClassified
	if risk == RiskHigh {
		next = EngagementEscalated
	}
	_, err = e.transition(ctx, cur, next, func(p *store.UpdateOpsEngagementStatusParams) {
		p.Category = category
		p.RiskLevel = risk
	})
	return err
}

// Suggest writes a proposed reply. Only classified, non-escalated items are
// eligible; the item then waits for approval.
func (e *Engagement) Suggest(ctx context.Context, ref BotRef, id int64) error {
	cur, err := e.engagementFor(ctx, ref, id)
	if err != nil {
		return err
	}
	if cur.Status != EngagementClassified {
		return fmt.Errorf("%w: suggest from %q", ErrEngagementStateChanged, cur.Status)
	}
	if cur.RiskLevel == RiskHigh {
		return ErrHighRiskEscalated
	}

	reply := suggestionFor(cur)
	if e.Generate != nil {
		bot, err := e.Q.GetOpsBot(ctx, cur.BotID)
		if err != nil {
			return fmt.Errorf("load bot %d: %w", cur.BotID, err)
		}
		reply, err = e.Generate(ctx, bot, cur)
		if err != nil {
			return fmt.Errorf("generate reply: %w", err)
		}
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return fmt.Errorf("%w: empty suggested reply", ErrInvalidInput)
	}

	_, err = e.transition(ctx, cur, EngagementSuggested, func(p *store.UpdateOpsEngagementStatusParams) {
		p.SuggestedReply = reply
	})
	return err
}

// RequestApproval creates the reply approval card and records its id on the
// engagement. A bot whose reply approval switch is off is auto-approved.
func (e *Engagement) RequestApproval(ctx context.Context, ref BotRef, id int64) (ApprovalRequest, error) {
	cur, err := e.engagementFor(ctx, ref, id)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if cur.Status != EngagementSuggested {
		return ApprovalRequest{}, fmt.Errorf("%w: request approval from %q", ErrEngagementStateChanged, cur.Status)
	}
	if cur.RiskLevel == RiskHigh {
		return ApprovalRequest{}, ErrHighRiskEscalated
	}

	bot, err := e.Q.GetOpsBot(ctx, cur.BotID)
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("load bot %d: %w", cur.BotID, err)
	}

	if !replyApprovalEnabled(bot.ApprovalPolicyJson) {
		if _, err := e.transition(ctx, cur, EngagementApproved, func(p *store.UpdateOpsEngagementStatusParams) {
			p.ReplyText = cur.SuggestedReply
		}); err != nil {
			return ApprovalRequest{}, err
		}
		return ApprovalRequest{Status: EngagementApproved}, nil
	}

	if e.Approvals == nil {
		return ApprovalRequest{}, ErrEngagementNotConfigured
	}
	if !bot.ControlWorkspaceID.Valid || bot.ControlWorkspaceID.Int64 == 0 {
		return ApprovalRequest{}, fmt.Errorf("%w: bot %d has no control workspace", ErrEngagementNotConfigured, bot.ID)
	}

	preview, err := json.Marshal(map[string]any{
		"platform_engagement_id": cur.PlatformEngagementID,
		"kind":                   cur.Kind,
		"author":                 cur.Author,
		"text":                   cur.Text,
		"category":               cur.Category,
		"risk_level":             cur.RiskLevel,
		"suggested_reply":        cur.SuggestedReply,
	})
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("marshal approval preview: %w", err)
	}

	req, err := e.Approvals.Create(ctx, ApprovalInput{
		BotID:       bot.ID,
		WorkspaceID: bot.ControlWorkspaceID.Int64,
		IssueID:     nullInt64Value(cur.IssueID),
		Type:        ApprovalReply,
		PreviewJSON: string(preview),
	})
	if err != nil {
		return ApprovalRequest{}, err
	}

	if _, err := e.transition(ctx, cur, EngagementSuggested, func(p *store.UpdateOpsEngagementStatusParams) {
		p.ApprovalRequestID = sql.NullInt64{Int64: req.ID, Valid: req.ID > 0}
	}); err != nil {
		return ApprovalRequest{}, err
	}
	return req, nil
}

// Send applies the operator decision and, when approved, hands the reply to
// the channel adapter. Rejected items are closed. Approved items are rate
// limited per bot and per hour: over the cap the item stays approved so a
// later dispatch can retry it. High-risk items are never sent.
func (e *Engagement) Send(ctx context.Context, id, approvalRequestID int64, d Decision) error {
	if e.Q == nil {
		return ErrEngagementNotConfigured
	}
	cur, err := e.Get(ctx, id)
	if err != nil {
		return err
	}

	switch cur.Status {
	case EngagementEscalated:
		return ErrHighRiskEscalated
	case EngagementRejected:
		return ErrEngagementRejected
	case EngagementSent:
		return nil
	case EngagementFailed:
		if d.Action == ActionRejected {
			_, err := e.transition(ctx, cur, EngagementRejected, nil)
			return err
		}
		// A failed send is retried only after an explicit re-approval.
		if _, err := e.transition(ctx, cur, EngagementApproved, nil); err != nil {
			return err
		}
		cur, _ = e.Get(ctx, id)
	case EngagementSuggested:
		next, err := e.applyDecision(ctx, cur, approvalRequestID, d)
		if err != nil {
			return err
		}
		if next == EngagementRejected || next == EngagementSuggested {
			return nil
		}
		cur, err = e.Get(ctx, id)
		if err != nil {
			return err
		}
	case EngagementApproved:
		// Already approved by the operator (possibly through the HTTP API).
	case EngagementNew, EngagementClassified:
		return fmt.Errorf("%w: send from %q", ErrEngagementStateChanged, cur.Status)
	default:
		return fmt.Errorf("%w: send from %q", ErrEngagementStateChanged, cur.Status)
	}

	bot, err := e.Q.GetOpsBot(ctx, cur.BotID)
	if err != nil {
		return fmt.Errorf("load bot %d: %w", cur.BotID, err)
	}
	if bot.Status != StatusActive {
		return fmt.Errorf("%w: %s", ErrBotNotActive, bot.Status)
	}

	if limit := replyHourlyLimit(bot.ApprovalPolicyJson); limit > 0 {
		sent, err := e.sentLastHour(ctx, bot.ID)
		if err != nil {
			return fmt.Errorf("count replies: %w", err)
		}
		if sent >= int64(limit) {
			return ErrRateLimited
		}
	}

	adapter, err := e.adapterFor(ctx, bot.ID)
	if err != nil {
		return err
	}
	if !adapter.Capabilities().Supports(channel.CapReply) {
		return &channel.AdapterError{
			Capability: channel.CapReply,
			Code:       "capability_unavailable",
			Err:        channel.ErrCapabilityUnavailable,
		}
	}

	reply := strings.TrimSpace(cur.ReplyText)
	if reply == "" {
		reply = strings.TrimSpace(cur.SuggestedReply)
	}
	if reply == "" {
		return fmt.Errorf("%w: no reply text", ErrInvalidInput)
	}

	receipt, attemptErr := e.replyWithRetry(ctx, adapter, cur.PlatformEngagementID, reply)
	if attemptErr != nil {
		if _, err := e.transition(ctx, cur, EngagementFailed, func(p *store.UpdateOpsEngagementStatusParams) {
			p.ReplyText = reply
		}); err != nil {
			return err
		}
		return fmt.Errorf("send engagement %d: %w", cur.ID, attemptErr)
	}

	_, err = e.transition(ctx, cur, EngagementSent, func(p *store.UpdateOpsEngagementStatusParams) {
		p.ReplyText = reply
		p.Receipt = receiptString(receipt)
	})
	return err
}

// Escalate moves an engagement to escalated so it is handled by a human. It is
// the manual counterpart of the automatic high-risk escalation in Classify.
func (e *Engagement) Escalate(ctx context.Context, ref BotRef, id int64, reason string) error {
	cur, err := e.engagementFor(ctx, ref, id)
	if err != nil {
		return err
	}
	switch cur.Status {
	case EngagementEscalated:
		return nil
	case EngagementSent, EngagementRejected:
		return fmt.Errorf("%w: escalate from %q", ErrEngagementStateChanged, cur.Status)
	}
	updated, err := e.transition(ctx, cur, EngagementEscalated, func(p *store.UpdateOpsEngagementStatusParams) {
		p.RiskLevel = RiskHigh
	})
	if err != nil {
		return err
	}
	if e.OnEscalated != nil {
		_ = e.OnEscalated(ctx, updated, reason)
	}
	return nil
}

// Get returns one engagement.
func (e *Engagement) Get(ctx context.Context, id int64) (store.OpsEngagement, error) {
	if e.Q == nil {
		return store.OpsEngagement{}, ErrEngagementNotConfigured
	}
	cur, err := e.Q.GetOpsEngagement(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.OpsEngagement{}, ErrEngagementNotFound
		}
		return store.OpsEngagement{}, err
	}
	return cur, nil
}

// applyDecision records the operator's answer and returns the engagement's
// status after it. Suggested stays suggested for regenerate.
func (e *Engagement) applyDecision(ctx context.Context, cur store.OpsEngagement, approvalRequestID int64, d Decision) (string, error) {
	action := strings.TrimSpace(d.Action)
	if action == "" {
		return "", fmt.Errorf("%w: missing decision action", ErrInvalidDecision)
	}

	if approvalRequestID > 0 {
		if e.Approvals == nil {
			return "", fmt.Errorf("%w: approval request needs an approval service", ErrEngagementNotConfigured)
		}
		_, err := e.Approvals.Decide(ctx, approvalRequestID, d)
		if err != nil && !errors.Is(err, ErrAlreadyDecided) {
			return "", err
		}
		if err != nil {
			// Already decided: the operator may have used the UI, so trust the
			// recorded answer only when it agrees with the requested action.
			req, getErr := e.Approvals.Get(ctx, approvalRequestID)
			if getErr != nil {
				return "", getErr
			}
			var answer approvalAnswer
			if jsonErr := json.Unmarshal([]byte(req.AnswersJSON), &answer); jsonErr != nil {
				return "", fmt.Errorf("read approval answer: %w", jsonErr)
			}
			if answer.Action != action {
				return "", fmt.Errorf("%w: approval recorded %q, requested %q", ErrInvalidDecision, answer.Action, action)
			}
		}
	}

	reply := strings.TrimSpace(cur.SuggestedReply)
	if modified := strings.TrimSpace(d.Answers["reply"]); modified != "" {
		reply = modified
	} else if modified := strings.TrimSpace(d.Answers["reply_text"]); modified != "" {
		reply = modified
	}

	switch action {
	case ActionApproved, ActionModified:
		decided := decisionTime(e.Now)
		if _, err := e.transition(ctx, cur, EngagementApproved, func(p *store.UpdateOpsEngagementStatusParams) {
			p.ReplyText = reply
			p.DecidedBy = sql.NullInt64{Int64: d.OperatorID, Valid: d.OperatorID != 0}
			p.DecidedAt = decided
		}); err != nil {
			return "", err
		}
		return EngagementApproved, nil
	case ActionRejected:
		decided := decisionTime(e.Now)
		if _, err := e.transition(ctx, cur, EngagementRejected, func(p *store.UpdateOpsEngagementStatusParams) {
			p.DecidedBy = sql.NullInt64{Int64: d.OperatorID, Valid: d.OperatorID != 0}
			p.DecidedAt = decided
		}); err != nil {
			return "", err
		}
		return EngagementRejected, nil
	case ActionRegenerate:
		return EngagementSuggested, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidDecision, action)
	}
}

func (e *Engagement) replyWithRetry(ctx context.Context, adapter channel.ChannelAdapter, platformID, text string) (channel.Receipt, error) {
	attempts := e.MaxAttempts
	if attempts <= 0 {
		attempts = defaultEngagementTries
	}
	backoff := e.Backoff
	if backoff <= 0 {
		backoff = defaultEngagementRetry
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		receipt, err := adapter.Reply(ctx, platformID, text)
		if err == nil {
			return receipt, nil
		}
		lastErr = err
		var adapterErr *channel.AdapterError
		retryable := errors.As(err, &adapterErr) && adapterErr.Retryable
		if !retryable || i == attempts-1 {
			break
		}
		if err := e.sleep(ctx, backoff<<i); err != nil {
			return channel.Receipt{}, err
		}
	}
	return channel.Receipt{}, lastErr
}

// sentLastHour counts replies already sent in the trailing hour. It uses the
// store wrapper so the placeholder style follows the active driver.
func (e *Engagement) sentLastHour(ctx context.Context, botID int64) (int64, error) {
	since := e.now().Add(-time.Hour)
	if e.DB != nil {
		// The SQLite driver writes a bound time.Time as t.String() (local
		// zone), which does not sort against CURRENT_TIMESTAMP's UTC text.
		// Passing the cutoff as SQLite's own text format keeps the comparison
		// valid; PostgreSQL binds the time.Time natively.
		var cutoff any = since
		if store.Driver != "postgres" {
			cutoff = since.UTC().Format("2006-01-02 15:04:05")
		}
		var n int64
		err := e.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM ops_engagements
			 WHERE bot_id = ? AND status = 'sent' AND updated_at >= ?`,
			botID, cutoff,
		).Scan(&n)
		return n, err
	}

	rows, err := e.Q.ListOpsEngagementsByStatus(ctx, store.ListOpsEngagementsByStatusParams{
		BotID:  botID,
		Status: EngagementSent,
		Limit:  rateLimitScanLimit,
	})
	if err != nil {
		return 0, err
	}
	var n int64
	for _, row := range rows {
		if !row.UpdatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (e *Engagement) engagementFor(ctx context.Context, ref BotRef, id int64) (store.OpsEngagement, error) {
	cur, err := e.Get(ctx, id)
	if err != nil {
		return store.OpsEngagement{}, err
	}
	if ref.ID != 0 && cur.BotID != ref.ID {
		return store.OpsEngagement{}, ErrEngagementNotFound
	}
	return cur, nil
}

func (e *Engagement) transition(ctx context.Context, cur store.OpsEngagement, next string, mutate func(*store.UpdateOpsEngagementStatusParams)) (store.OpsEngagement, error) {
	params := store.UpdateOpsEngagementStatusParams{
		Status:            next,
		Category:          cur.Category,
		RiskLevel:         cur.RiskLevel,
		SuggestedReply:    cur.SuggestedReply,
		ApprovalRequestID: cur.ApprovalRequestID,
		ReplyText:         cur.ReplyText,
		Receipt:           cur.Receipt,
		DecidedBy:         cur.DecidedBy,
		DecidedAt:         cur.DecidedAt,
		ID:                cur.ID,
		Status_2:          cur.Status,
	}
	if mutate != nil {
		mutate(&params)
	}
	updated, err := e.Q.UpdateOpsEngagementStatus(ctx, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.OpsEngagement{}, fmt.Errorf("%w: %s -> %s", ErrEngagementStateChanged, cur.Status, next)
		}
		return store.OpsEngagement{}, err
	}
	return updated, nil
}

func (e *Engagement) adapterFor(ctx context.Context, botID int64) (channel.ChannelAdapter, error) {
	if e.AdapterFor != nil {
		return e.AdapterFor(ctx, botID)
	}
	if e.Adapter != nil {
		return e.Adapter, nil
	}
	return nil, fmt.Errorf("%w: no channel adapter", ErrEngagementNotConfigured)
}

func (e *Engagement) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engagement) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	if d <= 0 {
		return nil
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

// decisionTime stamps a decision, encoded as a nullable column value.
func decisionTime(now func() time.Time) sql.NullTime {
	if now == nil {
		now = time.Now
	}
	return sql.NullTime{Time: now().UTC(), Valid: true}
}

func nullInt64Value(v sql.NullInt64) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

func receiptString(r channel.Receipt) string {
	for _, candidate := range []string{r.ExternalID, r.URL, r.Raw} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return "ok"
}

type engagementReplyPolicy struct {
	Reply            *bool `json:"reply"`
	ReplyHourlyLimit *int  `json:"reply_hourly_limit"`
}

func parseEngagementReplyPolicy(raw string) engagementReplyPolicy {
	var policy engagementReplyPolicy
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &policy)
	return policy
}

func replyApprovalEnabled(raw string) bool {
	policy := parseEngagementReplyPolicy(raw)
	return policy.Reply == nil || *policy.Reply
}

func replyHourlyLimit(raw string) int {
	policy := parseEngagementReplyPolicy(raw)
	if policy.ReplyHourlyLimit == nil {
		return defaultReplyHourlyLimit
	}
	if *policy.ReplyHourlyLimit < 0 {
		return 0
	}
	return *policy.ReplyHourlyLimit
}

var (
	highRiskTerms = []string{
		"投诉", "退款", "退货", "差评", "维权", "起诉", "律师", "12315", "消协",
		"曝光", "举报", "欺诈", "诈骗", "违法", "侵权", "法律", "监管", "赔偿",
		"道歉", "refund", "lawsuit", "sue", "legal", "complaint",
	}
	complaintTerms = []string{
		"投诉", "退款", "退货", "差评", "骗", "坑", "垃圾", "太差", "很差",
		"失望", "后悔", "恶心", "无语", "举报", "赔偿", "道歉",
		"complaint", "refund", "terrible", "awful", "worst", "scam", "disappointed",
	}
	negativeTerms = []string{
		"差", "不好", "不行", "难用", "烂", "崩", "卡顿", "太慢", "贵",
		"坑人", "bad", "poor", "slow", "broken", "hate",
	}
	spamTerms = []string{
		"加微", "加我微信", "微信号", "代购", "刷单", "优惠券", "免费领",
		"点击链接", "私聊我", "微商", "兼职", "日赚", "推广", "广告",
		"http://", "https://", "t.me/", "spam",
	}
	leadTerms = []string{
		"多少钱", "价格", "购买", "怎么买", "哪里买", "优惠", "合作", "代理",
		"批发", "报价", "下单", "咨询", "price", "buy", "order", "quote",
		"collaboration",
	}
	praiseTerms = []string{
		"喜欢", "好用", "好看", "太棒", "很棒", "赞", "感谢", "谢谢", "好评",
		"爱了", "支持", "love", "great", "awesome", "amazing", "thanks",
		"thank you", "nice",
	}
)

// classifyEngagement is deliberately keyword-based: it is deterministic,
// offline, and safe to run on every inbound item. An LLM classifier can
// override it later without changing the state machine.
func classifyEngagement(text string) (category, risk string) {
	switch {
	case containsAny(text, highRiskTerms...):
		return CategoryComplaint, RiskHigh
	case containsAny(text, complaintTerms...):
		return CategoryComplaint, RiskMedium
	case containsAny(text, spamTerms...):
		return CategorySpam, RiskLow
	case containsAny(text, negativeTerms...):
		return CategoryNegative, RiskMedium
	case containsAny(text, leadTerms...):
		return CategoryLead, RiskLow
	case containsAny(text, praiseTerms...):
		return CategoryPraise, RiskLow
	default:
		return CategoryInquiry, RiskLow
	}
}

func suggestionFor(cur store.OpsEngagement) string {
	switch cur.Category {
	case CategoryPraise:
		return "谢谢您的喜欢和支持！我们会继续努力，带来更多好内容～"
	case CategoryLead:
		return "您好，感谢关注！购买与合作事宜可以私信我们，会有专人与您联系～"
	case CategoryComplaint, CategoryNegative:
		return "非常抱歉给您带来了不好的体验，我们已收到反馈，会尽快核实并改进。方便的话请私信我们说明具体情况，谢谢！"
	case CategorySpam:
		return "感谢关注～"
	default:
		return "您好，感谢您的咨询～可以私信我们了解更多细节，我们会尽快回复您。"
	}
}
