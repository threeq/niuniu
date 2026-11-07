package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ApprovalType names the three approval cards from the design spec §10.
type ApprovalType string

const (
	ApprovalPublish ApprovalType = "publish"
	ApprovalReply   ApprovalType = "reply"
	ApprovalVideo   ApprovalType = "video"
)

// Approval statuses mirror agent_ask_user_requests.status.
const (
	ApprovalStatusPending   = "pending"
	ApprovalStatusAnswered  = "answered"
	ApprovalStatusTimeout   = "timeout"
	ApprovalStatusCancelled = "cancelled"
)

// Decision actions accepted by Decide.
const (
	ActionApproved   = "approved"
	ActionRejected   = "rejected"
	ActionModified   = "modified"
	ActionRegenerate = "regenerate"
)

const (
	opsApprovalKind      = "ops_approval"
	opsApprovalSessionID = "ops-approval"
	defaultApprovalTTL   = 24 * time.Hour
)

var (
	// ErrAlreadyDecided is returned when a decision races another decision or
	// arrives after the row left pending (timeout / cancelled).
	ErrAlreadyDecided = errors.New("opsbot: approval already decided")
	// ErrApprovalNotFound is returned when the id does not name an ops approval.
	ErrApprovalNotFound = errors.New("opsbot: approval not found")
	// ErrInvalidApprovalType is returned for an unknown approval card type.
	ErrInvalidApprovalType = errors.New("opsbot: invalid approval type")
	// ErrInvalidDecision is returned for an unknown decision action.
	ErrInvalidDecision = errors.New("opsbot: invalid approval decision")
	// ErrApprovalDisabled is returned when the bot's policy turns this card off.
	ErrApprovalDisabled = errors.New("opsbot: approval type disabled by policy")
)

// ApprovalPolicy is the bot-level switchboard for the three approval cards.
// The zero value enables all three with the default TTL.
type ApprovalPolicy struct {
	Enabled    map[ApprovalType]bool
	DefaultTTL time.Duration
}

func (p ApprovalPolicy) allows(t ApprovalType) bool {
	if len(p.Enabled) == 0 {
		return true
	}
	return p.Enabled[t]
}

func (p ApprovalPolicy) ttl(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if p.DefaultTTL > 0 {
		return p.DefaultTTL
	}
	return defaultApprovalTTL
}

// ApprovalInput describes one approval card to create. Owner is resolved from
// WorkspaceID so callers cannot forge cross-owner approvals.
type ApprovalInput struct {
	BotID          int64
	WorkspaceID    int64
	IssueID        int64
	Type           ApprovalType
	ContentVersion int64
	PreviewJSON    string
	TTL            time.Duration
}

// Decision is the operator's answer. ProxyFor is set when a director decides
// on behalf of the account operator; it is recorded for audit.
type Decision struct {
	OperatorID int64
	Action     string // approved | rejected | modified | regenerate
	Reason     string
	ProxyFor   int64
	Answers    map[string]string
}

// ApprovalRequest is the audit-facing view of one approval row.
type ApprovalRequest struct {
	ID             int64
	WorkspaceID    int64
	Status         string
	MetadataJSON   string
	AnswersJSON    string
	DecisionSource string
	DecidedBy      int64
	RequestedAt    time.Time
	DecidedAt      time.Time
	ExpiresAt      time.Time
}

// Approval is the approval state machine over agent_ask_user_requests. It
// reuses the existing ask-user table instead of creating an approval table.
type Approval struct {
	db     *store.DB
	policy ApprovalPolicy

	// OnDecided runs after any decision (approved/rejected/modified/regenerate)
	// commits; callers must branch on Decision.Action. It is never invoked for
	// timeout or cancellation rows.
	OnDecided func(ctx context.Context, req ApprovalRequest, d Decision) error
}

// NewApproval builds the service over the driver-aware store wrapper.
func NewApproval(db *store.DB, policy ApprovalPolicy) *Approval {
	return &Approval{db: db, policy: policy}
}

var approvalOptions = map[ApprovalType][]string{
	ApprovalPublish: {ActionApproved, ActionRejected, ActionModified},
	ApprovalReply:   {ActionApproved, ActionModified, ActionRejected},
	ApprovalVideo:   {ActionApproved, ActionRejected, ActionRegenerate},
}

type approvalQuestion struct {
	Type    string          `json:"type"`
	Options []string        `json:"options"`
	Preview json.RawMessage `json:"preview,omitempty"`
}

type approvalAnswer struct {
	Action  string            `json:"action"`
	Reason  string            `json:"reason,omitempty"`
	Answers map[string]string `json:"answers,omitempty"`
}

type approvalMetadata struct {
	BotID          int64           `json:"bot_id"`
	IssueID        int64           `json:"issue_id"`
	Type           ApprovalType    `json:"type"`
	ContentVersion int64           `json:"content_version"`
	Preview        json.RawMessage `json:"preview"`
}

// Create inserts a pending ops approval card.
func (a *Approval) Create(ctx context.Context, in ApprovalInput) (ApprovalRequest, error) {
	opts, ok := approvalOptions[in.Type]
	if !ok {
		return ApprovalRequest{}, ErrInvalidApprovalType
	}
	if !a.policy.allows(in.Type) {
		return ApprovalRequest{}, ErrApprovalDisabled
	}

	preview := json.RawMessage(in.PreviewJSON)
	if len(preview) == 0 {
		preview = json.RawMessage(`{}`)
	}
	if !json.Valid(preview) {
		return ApprovalRequest{}, fmt.Errorf("opsbot: preview is not valid JSON")
	}

	ownerType, ownerID, err := a.resolveOwner(ctx, in.WorkspaceID)
	if err != nil {
		return ApprovalRequest{}, err
	}

	questionsJSON, err := json.Marshal(approvalQuestion{
		Type:    opsApprovalKind,
		Options: opts,
		Preview: preview,
	})
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("marshal approval questions: %w", err)
	}
	metadataJSON, err := json.Marshal(approvalMetadata{
		BotID:          in.BotID,
		IssueID:        in.IssueID,
		Type:           in.Type,
		ContentVersion: in.ContentVersion,
		Preview:        preview,
	})
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("marshal approval metadata: %w", err)
	}

	expiresAt := time.Now().Add(a.policy.ttl(in.TTL))
	id, err := a.db.Queries().InsertAskUserRequest(ctx, store.InsertAskUserRequestParams{
		WorkspaceID:   in.WorkspaceID,
		OwnerType:     ownerType,
		OwnerID:       ownerID,
		SessionID:     opsApprovalSessionID,
		QuestionsJson: string(questionsJSON),
		Kind:          opsApprovalKind,
		MetadataJson:  string(metadataJSON),
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("insert ops approval: %w", err)
	}

	return ApprovalRequest{
		ID:           id,
		WorkspaceID:  in.WorkspaceID,
		Status:       ApprovalStatusPending,
		MetadataJSON: string(metadataJSON),
		ExpiresAt:    expiresAt,
	}, nil
}

// Decide transitions pending -> answered exactly once. A lost CAS (timeout,
// cancellation, or a concurrent decision) returns ErrAlreadyDecided and never
// overwrites the recorded outcome.
func (a *Approval) Decide(ctx context.Context, id int64, d Decision) (ApprovalRequest, error) {
	if d.Action == "" {
		return ApprovalRequest{}, ErrInvalidDecision
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRequest{}, err
	}
	defer tx.Rollback()
	q := tx.Queries()

	req, err := q.GetOpsApproval(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApprovalRequest{}, ErrApprovalNotFound
		}
		return ApprovalRequest{}, err
	}
	if req.Status != ApprovalStatusPending {
		return ApprovalRequest{}, ErrAlreadyDecided
	}
	if !actionAllowedForCard(req.MetadataJson, d.Action) {
		return ApprovalRequest{}, ErrInvalidDecision
	}
	// An expired row that the sweeper has not reached yet must not be
	// actionable: retire it as timeout in the same transaction.
	if !req.ExpiresAt.IsZero() && !time.Now().Before(req.ExpiresAt) {
		if _, err := q.MarkAskUserRequestTimeout(ctx, id); err != nil {
			return ApprovalRequest{}, err
		}
		if err := tx.Commit(); err != nil {
			return ApprovalRequest{}, err
		}
		return ApprovalRequest{}, ErrAlreadyDecided
	}

	answersJSON, err := json.Marshal(approvalAnswer{
		Action:  d.Action,
		Reason:  d.Reason,
		Answers: d.Answers,
	})
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("marshal approval answer: %w", err)
	}

	source := "user"
	if d.ProxyFor != 0 {
		source = "proxy"
	}
	n, err := q.MarkAskUserRequestAnswered(ctx, store.MarkAskUserRequestAnsweredParams{
		ID:             id,
		DecisionSource: sql.NullString{String: source, Valid: true},
		DecidedBy:      sql.NullInt64{Int64: d.OperatorID, Valid: d.OperatorID != 0},
		AnswersJson:    sql.NullString{String: string(answersJSON), Valid: true},
	})
	if err != nil {
		return ApprovalRequest{}, err
	}
	if n == 0 {
		return ApprovalRequest{}, ErrAlreadyDecided
	}

	metadataJSON, err := mergeDecisionMetadata(req.MetadataJson, d)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_ask_user_requests SET metadata_json = ? WHERE id = ?`,
		metadataJSON, id,
	); err != nil {
		return ApprovalRequest{}, fmt.Errorf("record approval metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRequest{}, err
	}

	// Read back so the caller sees the committed truth, not an assumption.
	got, err := a.Get(ctx, id)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if a.OnDecided != nil {
		// The decision is already committed; a callback error means the
		// downstream effect failed, not that the approval is still pending.
		if err := a.OnDecided(ctx, got, d); err != nil {
			return got, fmt.Errorf("approval on-decided callback: %w", err)
		}
	}
	return got, nil
}

// Get returns one ops approval, or ErrApprovalNotFound.
func (a *Approval) Get(ctx context.Context, id int64) (ApprovalRequest, error) {
	req, err := a.db.Queries().GetOpsApproval(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApprovalRequest{}, ErrApprovalNotFound
		}
		return ApprovalRequest{}, err
	}
	return toApprovalRequest(req), nil
}

// ListPending returns pending approvals for one owner (cross-owner isolation).
func (a *Approval) ListPending(ctx context.Context, ownerType string, ownerID int64) ([]ApprovalRequest, error) {
	rows, err := a.db.Queries().ListPendingOpsApprovalsByOwner(ctx, store.ListPendingOpsApprovalsByOwnerParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ApprovalRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, toApprovalRequest(r))
	}
	return out, nil
}

// CancelByBot implements the kill switch: every pending approval belonging to
// botID is cancelled. Rows are filtered in Go because SQLite and PostgreSQL
// disagree on JSON functions.
func (a *Approval) CancelByBot(ctx context.Context, botID int64) (int, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, metadata_json FROM agent_ask_user_requests
		  WHERE kind = ? AND status = ?`,
		opsApprovalKind, ApprovalStatusPending)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		var metadataJSON string
		if err := rows.Scan(&id, &metadataJSON); err != nil {
			return 0, err
		}
		var meta approvalMetadata
		if err := json.Unmarshal([]byte(metadataJSON), &meta); err != nil {
			continue
		}
		if meta.BotID == botID {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	q := a.db.Queries()
	cancelled := 0
	for _, id := range ids {
		n, err := q.MarkAskUserRequestCancelled(ctx, store.MarkAskUserRequestCancelledParams{
			ID:             id,
			DecisionSource: sql.NullString{String: "kill_switch", Valid: true},
		})
		if err != nil {
			return cancelled, err
		}
		cancelled += int(n)
	}
	return cancelled, nil
}

// SweepExpired marks overdue pending approvals as timeout. It never approves
// and never invokes OnDecided — expiry is a notification-only state.
func (a *Approval) SweepExpired(ctx context.Context) error {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id FROM agent_ask_user_requests
		  WHERE kind = ? AND status = ? AND expires_at < ?`,
		opsApprovalKind, ApprovalStatusPending, time.Now())
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	q := a.db.Queries()
	for _, id := range ids {
		if _, err := q.MarkAskUserRequestTimeout(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (a *Approval) resolveOwner(ctx context.Context, workspaceID int64) (string, int64, error) {
	var ownerType string
	var ownerID int64
	err := a.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM workspaces WHERE id = ?`, workspaceID,
	).Scan(&ownerType, &ownerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, fmt.Errorf("opsbot: workspace %d not found", workspaceID)
		}
		return "", 0, err
	}
	return ownerType, ownerID, nil
}

// actionAllowedForCard checks an action against the card's own option list.
// Malformed metadata or an unknown type falls back to the full known set so
// externally inserted rows stay decidable.
func actionAllowedForCard(metadataJSON, action string) bool {
	var meta approvalMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &meta); err == nil {
		if opts, ok := approvalOptions[meta.Type]; ok {
			return slices.Contains(opts, action)
		}
	}
	for _, opts := range approvalOptions {
		if slices.Contains(opts, action) {
			return true
		}
	}
	return false
}

// mergeDecisionMetadata preserves every existing key (preview included) and
// adds the audit fields. RawMessage decoding keeps the preview byte-identical.
func mergeDecisionMetadata(raw string, d Decision) (string, error) {
	meta := map[string]json.RawMessage{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			return "", fmt.Errorf("parse approval metadata: %w", err)
		}
	}
	if meta == nil { // a literal "null" decodes to a nil map
		meta = map[string]json.RawMessage{}
	}
	if d.OperatorID != 0 {
		b, _ := json.Marshal(d.OperatorID)
		meta["approved_by"] = b
	}
	if d.ProxyFor != 0 {
		b, _ := json.Marshal(d.ProxyFor)
		meta["proxy_for"] = b
	}
	if d.Action != "" {
		b, _ := json.Marshal(d.Action)
		meta["action"] = b
	}
	out, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encode approval metadata: %w", err)
	}
	return string(out), nil
}

func toApprovalRequest(r store.AgentAskUserRequest) ApprovalRequest {
	out := ApprovalRequest{
		ID:           r.ID,
		WorkspaceID:  r.WorkspaceID,
		Status:       r.Status,
		MetadataJSON: r.MetadataJson,
		RequestedAt:  r.RequestedAt,
		ExpiresAt:    r.ExpiresAt,
	}
	if r.AnswersJson.Valid {
		out.AnswersJSON = r.AnswersJson.String
	}
	if r.DecisionSource.Valid {
		out.DecisionSource = r.DecisionSource.String
	}
	if r.DecidedBy.Valid {
		out.DecidedBy = r.DecidedBy.Int64
	}
	if r.DecidedAt.Valid {
		out.DecidedAt = r.DecidedAt.Time
	}
	return out
}
