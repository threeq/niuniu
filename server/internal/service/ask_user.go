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

// AskUserAnswer is one entry of the user's response — selected option labels
// for the corresponding question. For multiSelect=false the slice has
// exactly one element; for multiSelect=true it may have any non-zero count.
// Notes captures free-form text when the user picked "Other".
type AskUserAnswer struct {
	Question string   `json:"question"`
	Labels   []string `json:"labels"`
	Notes    string   `json:"notes,omitempty"`
}

// AskUserResult is the JSON the MCP tool returns to Claude. The shape is
// deliberately small: one entry per asked question. When the request
// resolves through timeout / cancel, Answered is false and Reason
// describes which terminal state was hit.
type AskUserResult struct {
	Answered bool            `json:"answered"`
	Reason   string          `json:"reason,omitempty"`
	Answers  []AskUserAnswer `json:"answers,omitempty"`
}

// AskUserDecision is the user's submitted response captured by REST.
type AskUserDecision struct {
	Answers []AskUserAnswer
	UserID  int64
}

// ErrAskUserAlreadyDecided is returned by Decide when a request has already
// been resolved (answered / timeout / cancelled).
var ErrAskUserAlreadyDecided = errors.New("ask-user request already decided")

// askUserDrainGrace mirrors decideDrainGrace in permission.go. Brief window
// for Decide to deliver on the per-request channel after Request's timer
// or ctx fires concurrently.
const askUserDrainGrace = 500 * time.Millisecond

// AskUserService brokers the niuniu_ask_user_question MCP tool. It is a
// near-clone of PermissionService minus the allowlist + matcher concepts —
// every ask-user request is a one-shot interactive question.
type AskUserService struct {
	db        *store.DB
	bus       *event.Bus
	notifyHub *notify.NotificationHub
	timeout   time.Duration

	// issueExecSetter, when wired, projects the linked issue's terminal state for
	// the waiting-user-input transition (spec §19). Optional / nil in tests. status
	// is an exec_status value; a non-empty reason marks a terminal escalation
	// (ask_user timeout) so the projector also flips the workspace to attention.
	issueExecSetter func(ctx context.Context, workspaceID int64, status, reason string)

	// execEvents is optional (nil in tests). When set, ask_user round-trips are
	// recorded on the linked issue's execution timeline (spec §23.7).
	execEvents *ExecEventService

	mu      sync.Mutex
	pending map[int64]chan AskUserResult // requestID → buffered chan(1)
}

// SetExecEventService wires the execution-timeline recorder (spec §23.7). Optional.
func (s *AskUserService) SetExecEventService(e *ExecEventService) { s.execEvents = e }

// recordAskEvent appends an ask_user timeline event, resolving the issue from the
// workspace. Best-effort and nil-safe.
func (s *AskUserService) recordAskEvent(ctx context.Context, workspaceID int64, summary string) {
	if s.execEvents == nil || s.db == nil {
		return
	}
	ws, err := store.New(s.db).GetWorkspace(ctx, workspaceID)
	if err != nil || !ws.IssueID.Valid {
		return
	}
	s.execEvents.Record(ctx, ExecEvent{IssueID: ws.IssueID.Int64, WorkspaceID: workspaceID, Kind: "ask_user", Summary: summary})
}

// SetIssueExecProjector wires the waiting-user-input / restore projection used by
// Request (spec §19). Optional / nil-safe; wired once at boot (server.New) to a
// closure over EpicExecutionService.ProjectIssueExecForWorkspace.
func (s *AskUserService) SetIssueExecProjector(f func(ctx context.Context, workspaceID int64, status, reason string)) {
	s.issueExecSetter = f
}

// projectIssueExec is the nil-safe internal dispatch for issueExecSetter.
func (s *AskUserService) projectIssueExec(ctx context.Context, workspaceID int64, status, reason string) {
	if s.issueExecSetter != nil {
		s.issueExecSetter(ctx, workspaceID, status, reason)
	}
}

// NewAskUserService constructs the service. Default timeout is 2h to match
// PermissionService — questions can sit idle just as long as permission
// prompts before the agent gives up.
func NewAskUserService(rawDB *sql.DB, bus *event.Bus, hub *notify.NotificationHub, timeout time.Duration) *AskUserService {
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	return &AskUserService{
		db:        store.Wrap(rawDB),
		bus:       bus,
		notifyHub: hub,
		timeout:   timeout,
		pending:   make(map[int64]chan AskUserResult),
	}
}

// Request inserts a pending row, broadcasts the ask_user_request SSE event,
// and blocks until the user answers, the timeout elapses, or ctx is
// cancelled. Mirrors PermissionService.Request — see comments there for the
// CAS / channel-drain race handling.
func (s *AskUserService) Request(
	ctx context.Context,
	workspaceID int64, ownerType string, ownerID int64,
	sessionID string, questions []event.AskUserQuestion,
) (AskUserResult, error) {
	if len(questions) == 0 {
		return AskUserResult{}, errors.New("ask-user: no questions")
	}
	q := store.New(s.db)

	expires := time.Now().Add(s.timeout)
	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		return AskUserResult{}, fmt.Errorf("marshal questions: %w", err)
	}
	reqID, err := q.InsertAskUserRequest(ctx, store.InsertAskUserRequestParams{
		WorkspaceID:   workspaceID,
		OwnerType:     ownerType,
		OwnerID:       ownerID,
		SessionID:     sessionID,
		QuestionsJson: string(questionsJSON),
		ExpiresAt:     expires,
	})
	if err != nil {
		return AskUserResult{}, fmt.Errorf("insert request: %w", err)
	}

	ch := make(chan AskUserResult, 1)
	s.mu.Lock()
	s.pending[reqID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, reqID)
		s.mu.Unlock()
	}()

	s.publishRequest(workspaceID, ownerType, ownerID, sessionID, reqID, questions, expires)
	// Spec §19: while the question is open the linked issue is waiting-user-input.
	s.projectIssueExec(ctx, workspaceID, "waiting_input", "")
	s.recordAskEvent(ctx, workspaceID, fmt.Sprintf("提问用户(%d 项)", len(questions)))

	timer := time.NewTimer(time.Until(expires))
	defer timer.Stop()
	select {
	case resp := <-ch:
		s.projectIssueExec(ctx, workspaceID, "running", "") // answered -> back in flight
		s.recordAskEvent(ctx, workspaceID, "用户已答复")
		return resp, nil
	case <-timer.C:
		n, _ := q.MarkAskUserRequestTimeout(ctx, reqID)
		if n == 0 {
			select {
			case resp := <-ch:
				s.projectIssueExec(ctx, workspaceID, "running", "")
				return resp, nil
			case <-time.After(askUserDrainGrace):
			}
		} else {
			s.publishDecided(workspaceID, ownerType, ownerID, reqID, "timeout", 0, "timeout")
			// Stay waiting-user-input + escalate to attention; never silently dropped.
			s.projectIssueExec(ctx, workspaceID, "waiting_input", "ask_user 超时未答复")
			s.recordAskEvent(ctx, workspaceID, "提问超时未答复")
		}
		return AskUserResult{Answered: false, Reason: "timeout"}, nil
	case <-ctx.Done():
		n, _ := q.MarkAskUserRequestCancelled(ctx, store.MarkAskUserRequestCancelledParams{
			ID:             reqID,
			DecisionSource: sql.NullString{String: "session_end", Valid: true},
		})
		if n == 0 {
			select {
			case resp := <-ch:
				s.projectIssueExec(ctx, workspaceID, "running", "")
				return resp, nil
			case <-time.After(askUserDrainGrace):
			}
		} else {
			s.publishDecided(workspaceID, ownerType, ownerID, reqID, "cancelled", 0, "session_end")
			// Session ended (not a user decision): clear waiting_input back to running.
			s.projectIssueExec(ctx, workspaceID, "running", "")
		}
		return AskUserResult{Answered: false, Reason: "cancelled"}, nil
	}
}

// Decide records the user's answer and unblocks the corresponding Request.
// Same race-correct ordering as PermissionService.Decide — tx.Commit
// precedes the channel send.
func (s *AskUserService) Decide(ctx context.Context, requestID int64, d AskUserDecision) error {
	slog.Info("ask-user: decide entry", "requestID", requestID, "userID", d.UserID, "answers", len(d.Answers))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := tx.Queries()

	req, err := q.GetAskUserRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if req.Status != "pending" {
		return ErrAskUserAlreadyDecided
	}

	answersJSON, err := json.Marshal(d.Answers)
	if err != nil {
		return fmt.Errorf("marshal answers: %w", err)
	}
	n, err := q.MarkAskUserRequestAnswered(ctx, store.MarkAskUserRequestAnsweredParams{
		ID:             requestID,
		DecisionSource: sql.NullString{String: "user", Valid: true},
		DecidedBy:      sql.NullInt64{Int64: d.UserID, Valid: d.UserID != 0},
		AnswersJson:    sql.NullString{String: string(answersJSON), Valid: true},
	})
	if err != nil {
		return err
	}
	if n == 0 {
		slog.Warn("ask-user: decide CAS lost (already decided)", "requestID", requestID)
		return ErrAskUserAlreadyDecided
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	resp := AskUserResult{Answered: true, Answers: d.Answers}
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

	s.publishDecided(req.WorkspaceID, req.OwnerType, req.OwnerID, requestID, "answered", d.UserID, "user")
	slog.Info("ask-user: decide done", "requestID", requestID)
	return nil
}

// OnStartup runs once at server boot. Pending rows from a previous process
// are orphaned — channels died with the process — so mark them cancelled.
func (s *AskUserService) OnStartup(ctx context.Context) error {
	q := store.New(s.db)
	_, err := q.CancelAllPendingAskUserRequests(ctx)
	return err
}

// CancelByWorkspace cancels all pending ask-user requests for a workspace,
// unblocks waiting Request goroutines, and broadcasts ask_user_decided per
// cancelled request. Called by agent.Stop / workspace.Archive symmetrically
// with PermissionService.CancelByWorkspace.
func (s *AskUserService) CancelByWorkspace(ctx context.Context, workspaceID int64, source string) error {
	q := store.New(s.db)
	rows, err := q.CancelPendingAskUserRequestsByWorkspace(ctx,
		store.CancelPendingAskUserRequestsByWorkspaceParams{
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
			case ch <- AskUserResult{Answered: false, Reason: "cancelled"}:
			default:
			}
		}
		s.publishDecided(workspaceID, r.OwnerType, r.OwnerID, r.ID, "cancelled", 0, source)
	}
	return nil
}

func (s *AskUserService) publishRequest(
	workspaceID int64, ownerType string, ownerID int64,
	sessionID string, reqID int64, questions []event.AskUserQuestion, expires time.Time,
) {
	now := time.Now()
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventAskUserRequest,
		WorkspaceId: workspaceID,
		Ts:          now.UnixMilli(),
		AskUserRequest: &event.AskUserRequestData{
			RequestID:   reqID,
			SessionID:   sessionID,
			Questions:   questions,
			RequestedAt: now.UnixMilli(),
			ExpiresAt:   expires.UnixMilli(),
		},
	})
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicAskUserPending,
			Action:    "request",
			ID:        reqID,
			OwnerType: ownerType,
			OwnerID:   ownerID,
			Extra: map[string]any{
				"workspaceId":   workspaceID,
				"questionCount": len(questions),
			},
		})
	}
}

func (s *AskUserService) publishDecided(
	workspaceID int64, ownerType string, ownerID int64,
	reqID int64, status string, decidedBy int64, source string,
) {
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventAskUserDecided,
		WorkspaceId: workspaceID,
		Ts:          time.Now().UnixMilli(),
		AskUserDecided: &event.AskUserDecidedData{
			RequestID:      reqID,
			Status:         status,
			DecidedByID:    decidedBy,
			DecisionSource: source,
		},
	})
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicAskUserPending,
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
