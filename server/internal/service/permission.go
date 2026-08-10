package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AllowResp is the JSON shape Claude CLI's --permission-prompt-tool expects
// (returned through the MCP tool result text).
type AllowResp struct {
	Behavior     string         `json:"behavior"` // "allow" | "deny"
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
	Message      string         `json:"message,omitempty"`
}

// Decision is the user's answer captured by REST.
type Decision struct {
	Allow       bool
	Always      bool
	Matcher     *Matcher // required when Always && Allow && IsHighRiskTool(tool)
	DenyMessage string
	UserID      int64
}

// Matcher describes an allowlist match rule (kind, value).
type Matcher struct {
	Kind  string // any | exact | prefix | glob | domain
	Value string
}

// ErrAlreadyDecided is returned by Decide when a request has already been
// resolved (allowed/denied/cancelled/timeout).
var ErrAlreadyDecided = errors.New("permission request already decided")

// PermissionAgentProxyAdapter adapts PermissionService to the
// agentproxy.PermissionGate interface (string-only return) so the agentproxy
// package can depend on a small interface without importing service.
//
// Wire-up in server.New after both PermissionService and AgentProxy exist:
//
//	agentProxy.SetPermissionGate(&service.PermissionAgentProxyAdapter{Svc: permSvc})
type PermissionAgentProxyAdapter struct {
	Svc *PermissionService
}

// Request forwards to PermissionService.Request and collapses the
// AllowResp{Behavior, ...} envelope to a plain string for the bridge.
func (a *PermissionAgentProxyAdapter) Request(
	ctx context.Context,
	workspaceID int64, ownerType string, ownerID int64,
	sessionID, toolName string, toolInput map[string]any,
) (string, error) {
	if a == nil || a.Svc == nil {
		return "deny", nil
	}
	resp, err := a.Svc.Request(ctx, workspaceID, ownerType, ownerID, sessionID, toolName, toolInput)
	if err != nil {
		return "deny", err
	}
	return resp.Behavior, nil
}

// ErrMatcherAnyForHighRisk is returned by Decide when an always-allow
// decision uses matcher kind 'any' for a high-risk tool. The REST handler
// maps this to HTTP 422 (the service layer is the single source of truth
// for this rule — see spec §6).
var ErrMatcherAnyForHighRisk = errors.New("matcher 'any' not allowed for high-risk tool")

// decideDrainGrace is how long Request waits on the per-request channel
// when its timeout/ctx fires AFTER Decide already won the CAS race.
// 500ms is generous given Decide does only an in-memory map lookup +
// non-blocking channel send after tx.Commit; if it exceeds this we
// accept a benign deny/allow divergence.
const decideDrainGrace = 500 * time.Millisecond

// PermissionService coordinates Claude CLI permission prompts. Spec:
// docs/superpowers/specs/2026-05-01-chat-permission-prompt-design.md
type PermissionService struct {
	db        *store.DB
	bus       *event.Bus
	notifyHub *notify.NotificationHub
	timeout   time.Duration

	mu      sync.Mutex
	pending map[int64]chan AllowResp // requestID → buffered chan(1)
}

// NewPermissionService — constructor takes RAW *sql.DB per CLAUDE.md
// service-layer convention (allows nil-DB tests). Wraps once internally.
// hub may be nil (tests).
func NewPermissionService(rawDB *sql.DB, bus *event.Bus, hub *notify.NotificationHub, timeout time.Duration) *PermissionService {
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	return &PermissionService{
		db:        store.Wrap(rawDB),
		bus:       bus,
		notifyHub: hub,
		timeout:   timeout,
		pending:   make(map[int64]chan AllowResp),
	}
}

// Git Bash allowlist preset (issue #235). The studio "from local directory"
// flow grants the agent unattended git/git-lfs so 保存/交付 quick actions don't
// pop a permission card on every commit/merge. Scope is deliberately tight: a
// command-prefix match on "git " (trailing space anchors the executable and
// never matches e.g. "github-foo") — non-git Bash (rm, curl, ...) still
// prompts, keeping the blast radius minimal.
const (
	gitBashMatcherKind  = "prefix"
	gitBashMatcherValue = "git "
)

// PresetGitBashAllowlist inserts the always-allow Bash(git:*) entry for a
// workspace. Idempotent: the agent_permission_allowlist UNIQUE index +
// ON CONFLICT DO NOTHING make repeat calls harmless. createdBy may be 0
// (system / unknown caller, e.g. personal edition).
func (s *PermissionService) PresetGitBashAllowlist(ctx context.Context, workspaceID, createdBy int64) error {
	q := store.New(s.db)
	return q.InsertPermissionAllowlist(ctx, store.InsertPermissionAllowlistParams{
		WorkspaceID:  workspaceID,
		ToolName:     "Bash",
		MatcherKind:  gitBashMatcherKind,
		MatcherValue: gitBashMatcherValue,
		CreatedBy:    sql.NullInt64{Int64: createdBy, Valid: createdBy != 0},
	})
}

// Request blocks until decision or timeout. owner_type/owner_id are written
// to the request row at insert time (frozen — see spec §10 transfer-ownership
// edge case). Task 4 only handles the allowlist short-circuit; Task 5 will
// add the pending insert + channel + wait.
func (s *PermissionService) Request(
	ctx context.Context,
	workspaceID int64, ownerType string, ownerID int64,
	sessionID, toolName string, toolInput map[string]any,
) (AllowResp, error) {
	q := store.New(s.db)
	field := extractMatcherField(toolName, toolInput)

	// 1) Allowlist short-circuit
	rows, err := q.ListPermissionAllowlistByWorkspaceAndTool(ctx,
		store.ListPermissionAllowlistByWorkspaceAndToolParams{
			WorkspaceID: workspaceID, ToolName: toolName,
		})
	if err != nil {
		return AllowResp{}, fmt.Errorf("list allowlist: %w", err)
	}
	for _, r := range rows {
		if matcherMatches(r.MatcherKind, r.MatcherValue, field) {
			label := fmt.Sprintf("%s:%s", r.MatcherKind, r.MatcherValue)
			s.recordAllowlistHit(ctx, workspaceID, ownerType, ownerID, sessionID, toolName, toolInput, label)
			return AllowResp{Behavior: "allow", UpdatedInput: toolInput}, nil
		}
	}

	// 2) Pending insert + channel + wait — Task 5
	expires := time.Now().Add(s.timeout)
	inputJSON, _ := json.Marshal(toolInput)
	reqID, err := q.InsertPermissionRequest(ctx, store.InsertPermissionRequestParams{
		WorkspaceID: workspaceID, OwnerType: ownerType, OwnerID: ownerID,
		SessionID: sessionID, ToolName: toolName,
		ToolInput: string(inputJSON), ExpiresAt: expires,
	})
	if err != nil {
		return AllowResp{}, fmt.Errorf("insert request: %w", err)
	}

	ch := make(chan AllowResp, 1)
	s.mu.Lock()
	s.pending[reqID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, reqID)
		s.mu.Unlock()
	}()

	s.publishRequest(workspaceID, ownerType, ownerID, sessionID, reqID, toolName, toolInput, expires)

	timer := time.NewTimer(time.Until(expires))
	defer timer.Stop()
	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		// CAS — if Decide already won (n==0), drain the channel briefly before
		// falling through to deny.
		n, _ := q.MarkPermissionRequestTimeout(ctx, reqID)
		if n == 0 {
			select {
			case resp := <-ch:
				return resp, nil
			case <-time.After(decideDrainGrace):
			}
		} else {
			s.publishDecided(workspaceID, ownerType, ownerID, reqID, "timeout", 0, "timeout")
		}
		return AllowResp{Behavior: "deny", Message: "超时未答复"}, nil
	case <-ctx.Done():
		n, _ := q.MarkPermissionRequestCancelled(ctx, store.MarkPermissionRequestCancelledParams{
			ID:             reqID,
			DecisionSource: sql.NullString{String: "session_end", Valid: true},
		})
		if n == 0 {
			select {
			case resp := <-ch:
				return resp, nil
			case <-time.After(decideDrainGrace):
			}
		} else {
			s.publishDecided(workspaceID, ownerType, ownerID, reqID, "cancelled", 0, "session_end")
		}
		return AllowResp{Behavior: "deny", Message: "agent 已停止"}, nil
	}
}

// Decide records the user's decision and unblocks the corresponding Request.
// Race-correctness: the tx is committed BEFORE the channel send, so a
// tx.Commit failure cannot deliver a phantom allow. CAS row count of 0 means
// the request already reached a terminal state (timeout/cancelled/decided);
// in that case we return ErrAlreadyDecided.
func (s *PermissionService) Decide(ctx context.Context, requestID int64, d Decision) error {
	slog.Info("permission: decide entry",
		"requestID", requestID, "userID", d.UserID, "allow", d.Allow, "always", d.Always)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := tx.Queries()

	req, err := q.GetPermissionRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if req.Status != "pending" {
		return ErrAlreadyDecided
	}

	var resp AllowResp
	var matcherLabel sql.NullString
	if d.Allow {
		if d.Always && d.Matcher != nil {
			if IsHighRiskTool(req.ToolName) && d.Matcher.Kind == "any" {
				return fmt.Errorf("%w: %s", ErrMatcherAnyForHighRisk, req.ToolName)
			}
			if err := q.InsertPermissionAllowlist(ctx, store.InsertPermissionAllowlistParams{
				WorkspaceID:  req.WorkspaceID,
				ToolName:     req.ToolName,
				MatcherKind:  d.Matcher.Kind,
				MatcherValue: d.Matcher.Value,
				CreatedBy:    sql.NullInt64{Int64: d.UserID, Valid: d.UserID != 0},
			}); err != nil {
				return err
			}
			matcherLabel = sql.NullString{String: d.Matcher.Kind + ":" + d.Matcher.Value, Valid: true}
		}
		n, err := q.MarkPermissionRequestAllowed(ctx, store.MarkPermissionRequestAllowedParams{
			ID:             requestID,
			DecisionSource: sql.NullString{String: "user", Valid: true},
			DecidedBy:      sql.NullInt64{Int64: d.UserID, Valid: d.UserID != 0},
			MatcherUsed:    matcherLabel,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			slog.Warn("permission: decide CAS lost (already decided)", "requestID", requestID)
			return ErrAlreadyDecided
		}
		input := map[string]any{}
		_ = json.Unmarshal([]byte(req.ToolInput), &input)
		resp = AllowResp{Behavior: "allow", UpdatedInput: input}
	} else {
		n, err := q.MarkPermissionRequestDenied(ctx, store.MarkPermissionRequestDeniedParams{
			ID:             requestID,
			DecisionSource: sql.NullString{String: "user", Valid: true},
			DecidedBy:      sql.NullInt64{Int64: d.UserID, Valid: d.UserID != 0},
			DenyMessage:    sql.NullString{String: d.DenyMessage, Valid: d.DenyMessage != ""},
		})
		if err != nil {
			return err
		}
		if n == 0 {
			slog.Warn("permission: decide CAS lost (already decided)", "requestID", requestID)
			return ErrAlreadyDecided
		}
		msg := d.DenyMessage
		if msg == "" {
			msg = "用户拒绝"
		}
		resp = AllowResp{Behavior: "deny", Message: "用户拒绝：" + msg}
	}

	// Commit BEFORE channel send: a tx.Commit failure must not deliver a
	// phantom allow.
	if err := tx.Commit(); err != nil {
		return err
	}

	s.mu.Lock()
	ch, ok := s.pending[requestID]
	delete(s.pending, requestID)
	s.mu.Unlock()
	if ok {
		select {
		case ch <- resp:
		default:
		}
	}

	status := "denied"
	if d.Allow {
		status = "allowed"
	}
	s.publishDecided(req.WorkspaceID, req.OwnerType, req.OwnerID, requestID, status, d.UserID, "user")
	slog.Info("permission: decide done", "requestID", requestID, "status", status)
	return nil
}

// publishRequest fans a permission_request out to the in-process bus and
// (when wired) to the per-window notify hub.
func (s *PermissionService) publishRequest(
	workspaceID int64, ownerType string, ownerID int64,
	sessionID string, reqID int64, toolName string, toolInput map[string]any, expires time.Time,
) {
	now := time.Now()
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventPermissionRequest,
		WorkspaceId: workspaceID,
		Ts:          now.UnixMilli(),
		PermissionRequest: &event.PermissionRequestData{
			RequestID:   reqID,
			SessionID:   sessionID,
			ToolName:    toolName,
			ToolInput:   toolInput,
			RequestedAt: now.UnixMilli(),
			ExpiresAt:   expires.UnixMilli(),
		},
	})
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicPermissionPending,
			Action:    "request",
			ID:        reqID,
			OwnerType: ownerType,
			OwnerID:   ownerID,
			Extra: map[string]any{
				"workspaceId": workspaceID,
				"toolName":    toolName,
			},
		})
	}
}

// publishDecided fans a permission_decided out to the bus and notify hub.
func (s *PermissionService) publishDecided(
	workspaceID int64, ownerType string, ownerID int64,
	reqID int64, status string, decidedBy int64, source string,
) {
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventPermissionDecided,
		WorkspaceId: workspaceID,
		Ts:          time.Now().UnixMilli(),
		PermissionDecided: &event.PermissionDecidedData{
			RequestID:      reqID,
			Status:         status,
			DecidedByID:    decidedBy,
			DecisionSource: source,
		},
	})
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicPermissionPending,
			Action:    "resolved",
			ID:        reqID,
			OwnerType: ownerType,
			OwnerID:   ownerID,
			Extra: map[string]any{
				"workspaceId": workspaceID,
				"status":      status,
			},
		})
	}
}

// OnStartup runs once at server boot. Pending rows from a previous process
// are orphaned (their channels died with the process), so we mark them
// cancelled. No SSE broadcast needed — clients backfill via REST list
// endpoint and don't see cancelled rows by default.
func (s *PermissionService) OnStartup(ctx context.Context) error {
	q := store.New(s.db)
	_, err := q.CancelAllPendingPermissionRequests(ctx)
	return err
}

// CancelByWorkspace cancels all pending requests for one workspace, unblocks
// any waiting Request goroutines, and broadcasts permission_decided per
// cancelled request. Used by agent.Stop / workspace.Archive in Task 11.
func (s *PermissionService) CancelByWorkspace(ctx context.Context, workspaceID int64, source string) error {
	q := store.New(s.db)
	rows, err := q.CancelPendingPermissionRequestsByWorkspace(ctx,
		store.CancelPendingPermissionRequestsByWorkspaceParams{
			DecisionSource: sql.NullString{String: source, Valid: source != ""},
			WorkspaceID:    workspaceID,
		})
	if err != nil {
		return err
	}
	for _, r := range rows {
		s.mu.Lock()
		ch, ok := s.pending[r.ID]
		delete(s.pending, r.ID)
		s.mu.Unlock()
		if ok {
			select {
			case ch <- AllowResp{Behavior: "deny", Message: "agent 已停止"}:
			default:
			}
		}
		s.publishDecided(workspaceID, r.OwnerType, r.OwnerID, r.ID, "cancelled", 0, source)
	}
	return nil
}

func (s *PermissionService) recordAllowlistHit(
	ctx context.Context,
	workspaceID int64, ownerType string, ownerID int64,
	sessionID, toolName string, toolInput map[string]any, matcherLabel string,
) {
	q := store.New(s.db)
	inputJSON, _ := json.Marshal(toolInput) // safe: input is JSON-decoded already
	_, err := q.InsertPermissionRequestAllowed(ctx, store.InsertPermissionRequestAllowedParams{
		WorkspaceID: workspaceID,
		OwnerType:   ownerType,
		OwnerID:     ownerID,
		SessionID:   sessionID,
		ToolName:    toolName,
		ToolInput:   string(inputJSON),
		MatcherUsed: sql.NullString{String: matcherLabel, Valid: true},
		ExpiresAt:   time.Now().Add(s.timeout),
	})
	if err != nil {
		slog.Warn("permission: failed to record allowlist hit",
			"workspaceID", workspaceID, "tool", toolName, "matcher", matcherLabel, "err", err)
	}
}
