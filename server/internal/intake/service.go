package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Decision is the outcome of the intake pipeline for one trigger.
type Decision string

const (
	DecisionAccept      Decision = "accept"
	DecisionResume      Decision = "resume"
	DecisionIgnore      Decision = "ignore"
	DecisionUnavailable Decision = "unavailable"
)

// ErrNotWired is returned when the service is enabled without the
// collaborators the pipeline needs. Server DI leaves it disabled until the
// Phase 1 adapters exist.
var ErrNotWired = errors.New("intake: service enabled without required dependencies")

// EventRecord is the result of idempotently storing an inbound event.
type EventRecord struct {
	ID        int64
	Duplicate bool
}

// Resolution is the result of resolving which task a trigger belongs to.
type Resolution struct {
	Thread      ThreadRef
	IssueID     int64
	WorkspaceID int64
	Bound       bool // the thread is already bound to IssueID/WorkspaceID
}

// WouldDo is the dry-run payload of a decision that was not executed. It is
// what the calibration queue shows: what would have happened, and the proposed
// brief.
type WouldDo struct {
	Decision Decision   `json:"decision"`
	Brief    *TaskBrief `json:"brief,omitempty"`
	Thread   ThreadRef  `json:"thread"`
}

// DecisionRecord is written for every trigger, executed or not.
type DecisionRecord struct {
	EventID        int64
	ProjectID      int64
	Mode           Mode
	Strength       Strength
	Decision       Decision
	ReasonCode     string
	ReasonText     string
	MatchedRuleIDs []int64
	PolicyVersion  int64
	Thread         ThreadRef
	Brief          *TaskBrief
	WouldDo        *WouldDo
}

// ExecutionPlan is handed to the executor for an accepted or resumed trigger.
// IssueID/WorkspaceID are the resolved task: for a resume they name the task to
// continue; for an accept they are whatever the resolver found (possibly zero —
// the executor then routes a new task).
type ExecutionPlan struct {
	Decision    Decision
	Trigger     Trigger
	Brief       TaskBrief
	DecisionID  int64
	IssueID     int64
	WorkspaceID int64
}

// ExecutionResult reports the task the execution produced.
type ExecutionResult struct {
	IssueID     int64
	WorkspaceID int64
}

// Result summarizes one Handle call.
type Result struct {
	EventID     int64
	DecisionID  int64
	Decision    Decision
	Strength    Strength
	ReasonCode  string
	Duplicate   bool
	Executed    bool
	IssueID     int64
	WorkspaceID int64
}

// Store persists events and decisions. Implementations must make
// RecordEvent idempotent on (source_kind, source_ref, event_ext_id).
type Store interface {
	RecordEvent(ctx context.Context, t Trigger) (EventRecord, error)
	RecordDecision(ctx context.Context, rec DecisionRecord) (int64, error)
	MarkExecuted(ctx context.Context, decisionID int64, res ExecutionResult) error
	// ReserveDailySlot atomically claims one slot in the project+source daily
	// cap. It must be a single atomic operation: counting first and executing
	// later lets concurrent inferred events all pass the check and overshoot the
	// cap. Returns false when the day's cap is already used.
	ReserveDailySlot(ctx context.Context, projectID int64, sourceKind, day string, cap int64) (bool, error)
	// ReleaseDailySlot returns a reservation when the pipeline failed before
	// executing anything.
	ReleaseDailySlot(ctx context.Context, projectID int64, sourceKind, day string) error
}

// PolicyProvider loads the effective policy for one project+source. A project
// with no stored policy must yield ModeOff with empty lists — never a permissive
// default.
type PolicyProvider interface {
	LoadPolicy(ctx context.Context, projectID int64, sourceKind string) (Policy, error)
}

// TaskResolver maps a trigger to the task it belongs to. A bound thread short-
// circuits triage and produces a resume.
type TaskResolver interface {
	Resolve(ctx context.Context, t Trigger) (Resolution, error)
}

// Executor creates or resumes the task described by the plan.
type Executor interface {
	Execute(ctx context.Context, plan ExecutionPlan) (ExecutionResult, error)
}

// Deps are the collaborators of the intake pipeline.
type Deps struct {
	// Enabled is the feature flag. When false, Handle records nothing and
	// performs no side effects.
	Enabled  bool
	Store    Store
	Policies PolicyProvider
	Resolver TaskResolver
	Executor Executor
	// Clock stamps events that arrive without a ReceivedAt. Defaults to
	// time.Now.
	Clock func() time.Time
}

// Service runs the intake pipeline.
type Service struct {
	enabled  bool
	store    Store
	policies PolicyProvider
	resolver TaskResolver
	executor Executor
	clock    func() time.Time
}

// NewService builds the intake service. It is inert until Enabled is set, so
// wiring it into DI changes no existing behaviour.
func NewService(deps Deps) *Service {
	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		enabled:  deps.Enabled,
		store:    deps.Store,
		policies: deps.Policies,
		resolver: deps.Resolver,
		executor: deps.Executor,
		clock:    clock,
	}
}

// Handle runs one trigger through the fixed pipeline:
//
//	dedupe -> authorize -> resolve -> decide -> record -> execute
//
// A decision is recorded for every non-duplicate trigger, including dry-run
// decisions. Execution only happens for explicit requests and, in auto mode, for
// inferred requests inside the matching rule.
func (s *Service) Handle(ctx context.Context, t Trigger) (Result, error) {
	if !s.enabled {
		return Result{Decision: DecisionIgnore, ReasonCode: ReasonDisabled}, nil
	}
	if s.store == nil || s.policies == nil || s.resolver == nil || s.executor == nil {
		return Result{}, ErrNotWired
	}
	if t.ReceivedAt.IsZero() {
		t.ReceivedAt = s.clock()
	}

	event, err := s.store.RecordEvent(ctx, t)
	if err != nil {
		return Result{}, fmt.Errorf("intake: record event: %w", err)
	}
	if event.Duplicate {
		return Result{
			EventID:    event.ID,
			Decision:   DecisionIgnore,
			ReasonCode: ReasonDuplicate,
			Duplicate:  true,
		}, nil
	}

	policy, err := s.policies.LoadPolicy(ctx, t.ProjectID, t.SourceKind)
	if err != nil {
		return Result{EventID: event.ID}, fmt.Errorf("intake: load policy: %w", err)
	}
	// A source-provided mode (an IM chat's own intake_mode) is finer-grained
	// than the project+source setting and overrides it for this trigger.
	if t.Mode != "" {
		policy.Mode = t.Mode
	}
	// The project stop switch wins over every mode: no inferred/observe work
	// runs while paused, but explicit requests still execute in mode off.
	if policy.Paused {
		policy.Mode = ModeOff
	}

	authz := policy.Evaluate(t)
	res := Result{
		EventID:    event.ID,
		Strength:   authz.Strength,
		ReasonCode: authz.ReasonCode,
	}

	var resolution Resolution
	if authz.Strength != StrengthDenied {
		resolution, err = s.resolver.Resolve(ctx, t)
		if err != nil {
			return res, fmt.Errorf("intake: resolve task: %w", err)
		}
	}
	// A resolver that returned nothing at all (denied, or no conversation
	// context) falls back to the trigger's thread. A resolver that DID find a
	// task in a threadless conversation (an IM pinned chat or active pointer)
	// keeps its issue/workspace ids even though ThreadExtID is empty.
	if resolution.Thread == (ThreadRef{}) {
		resolution.Thread = t.Thread
	}
	res.IssueID = resolution.IssueID
	res.WorkspaceID = resolution.WorkspaceID

	decision, reason := decide(policy.Mode, authz, resolution.Bound, t.ForceNew)
	executed := shouldExecute(policy.Mode, authz.Strength) &&
		(decision == DecisionAccept || decision == DecisionResume)
	// The daily cap bounds inferred auto-intake only; explicit requests from an
	// allow-listed actor always run. The slot is claimed atomically BEFORE
	// execution so concurrent inferred events cannot all pass a check-then-act
	// window and overshoot the cap.
	var releaseSlot func()
	if executed && authz.Strength == StrengthInferred && policy.DailyCap > 0 {
		day := t.ReceivedAt.Format("2006-01-02")
		reserved, err := s.store.ReserveDailySlot(ctx, t.ProjectID, t.SourceKind, day, policy.DailyCap)
		if err != nil {
			return res, fmt.Errorf("intake: daily cap: %w", err)
		}
		if !reserved {
			decision, reason, executed = DecisionIgnore, ReasonDailyCap, false
		} else {
			// A cancelled caller context must not leak the slot.
			releaseSlot = func() {
				_ = s.store.ReleaseDailySlot(context.Background(), t.ProjectID, t.SourceKind, day)
			}
		}
	}
	res.Decision = decision
	res.ReasonCode = reason

	var brief *TaskBrief
	var wouldDo *WouldDo
	if decision == DecisionAccept || decision == DecisionResume {
		draft := NewBrief(t, Provenance{
			PolicyVersion: policy.Version,
			Strength:      authz.Strength,
			ActorExtID:    t.Actor.ExtID,
			RuleIDs:       authz.MatchedRuleIDs,
		})
		brief = &draft
		if !executed {
			wouldDo = &WouldDo{Decision: decision, Brief: brief, Thread: resolution.Thread}
		}
	}

	decisionID, err := s.store.RecordDecision(ctx, DecisionRecord{
		EventID:        event.ID,
		ProjectID:      t.ProjectID,
		Mode:           policy.Mode,
		Strength:       authz.Strength,
		Decision:       decision,
		ReasonCode:     reason,
		MatchedRuleIDs: authz.MatchedRuleIDs,
		PolicyVersion:  policy.Version,
		Thread:         resolution.Thread,
		Brief:          brief,
		WouldDo:        wouldDo,
	})
	if err != nil {
		// Nothing executed, so return the claimed slot.
		if releaseSlot != nil {
			releaseSlot()
		}
		return res, fmt.Errorf("intake: record decision: %w", err)
	}
	res.DecisionID = decisionID

	if !executed {
		if releaseSlot != nil {
			releaseSlot()
		}
		return res, nil
	}

	if brief != nil {
		brief.Provenance.DecisionID = decisionID
	}
	out, err := s.executor.Execute(ctx, ExecutionPlan{
		Decision:    decision,
		Trigger:     t,
		Brief:       *brief,
		DecisionID:  decisionID,
		IssueID:     resolution.IssueID,
		WorkspaceID: resolution.WorkspaceID,
	})
	if err != nil {
		return res, fmt.Errorf("intake: execute: %w", err)
	}
	if err := s.store.MarkExecuted(ctx, decisionID, out); err != nil {
		return res, fmt.Errorf("intake: mark executed: %w", err)
	}

	res.Executed = true
	res.IssueID = out.IssueID
	res.WorkspaceID = out.WorkspaceID
	return res, nil
}

// Proposal is work a source derived on its own initiative from observation —
// the IM employee's proactive "task" verdict — rather than a message a human
// addressed to the bot. It is inferred by definition: nobody asked for it.
type Proposal struct {
	Trigger Trigger
	Goal    string
}

// Propose records a derived proposal as a dry-run decision in observe mode. It
// never executes and never performs a side effect: no task, no workspace, no
// message. In auto mode it records nothing (the caller executes the work through
// its normal path), and in off mode inferred work is not evaluated at all, so it
// records nothing either. Callers that may execute must consult the mode before
// calling.
func (s *Service) Propose(ctx context.Context, p Proposal) (Result, error) {
	if !s.enabled {
		return Result{Decision: DecisionIgnore, ReasonCode: ReasonDisabled}, nil
	}
	if s.store == nil || s.policies == nil {
		return Result{}, ErrNotWired
	}

	t := p.Trigger
	if goal := strings.TrimSpace(p.Goal); goal != "" {
		t.Text = goal
	}
	if t.ReceivedAt.IsZero() {
		t.ReceivedAt = s.clock()
	}

	policy, err := s.policies.LoadPolicy(ctx, t.ProjectID, t.SourceKind)
	if err != nil {
		return Result{}, fmt.Errorf("intake: load policy: %w", err)
	}
	if t.Mode != "" {
		policy.Mode = t.Mode
	}
	if policy.Paused {
		policy.Mode = ModeOff
	}
	if policy.Mode != ModeObserve {
		return Result{Decision: DecisionIgnore, ReasonCode: ReasonOffInferred}, nil
	}

	event, err := s.store.RecordEvent(ctx, t)
	if err != nil {
		return Result{}, fmt.Errorf("intake: record event: %w", err)
	}
	if event.Duplicate {
		return Result{
			EventID:    event.ID,
			Decision:   DecisionIgnore,
			ReasonCode: ReasonDuplicate,
			Duplicate:  true,
		}, nil
	}

	brief := NewBrief(t, Provenance{
		PolicyVersion: policy.Version,
		Strength:      StrengthInferred,
		ActorExtID:    t.Actor.ExtID,
	})
	res := Result{
		EventID:    event.ID,
		Decision:   DecisionAccept,
		Strength:   StrengthInferred,
		ReasonCode: ReasonObserved,
	}
	decisionID, err := s.store.RecordDecision(ctx, DecisionRecord{
		EventID:       event.ID,
		ProjectID:     t.ProjectID,
		Mode:          policy.Mode,
		Strength:      StrengthInferred,
		Decision:      DecisionAccept,
		ReasonCode:    ReasonObserved,
		PolicyVersion: policy.Version,
		Thread:        t.Thread,
		Brief:         &brief,
		WouldDo:       &WouldDo{Decision: DecisionAccept, Brief: &brief, Thread: t.Thread},
	})
	if err != nil {
		return res, fmt.Errorf("intake: record decision: %w", err)
	}
	res.DecisionID = decisionID
	return res, nil
}

// decide maps authorization strength + mode + thread binding to a decision.
// ForceNew overrides a bound thread: the user asked for a separate task, so the
// bound one is not resumed.
//
// Auto mode executes inferred work only inside a matching rule: an inferred
// actor with no rule in scope has no mandate to create work (§6.2). Observe
// mode still records the candidate so it can be calibrated.
func decide(mode Mode, authz Authorization, bound, forceNew bool) (Decision, string) {
	switch authz.Strength {
	case StrengthExplicit:
		if bound && !forceNew {
			return DecisionResume, ReasonResumed
		}
		return DecisionAccept, ReasonAccepted
	case StrengthInferred:
		switch mode {
		case ModeAuto:
			if len(authz.MatchedRuleIDs) == 0 {
				return DecisionIgnore, ReasonNoRule
			}
			if bound {
				return DecisionResume, ReasonResumed
			}
			return DecisionAccept, ReasonAccepted
		case ModeObserve:
			if bound {
				return DecisionResume, ReasonObserved
			}
			return DecisionAccept, ReasonObserved
		default:
			return DecisionIgnore, ReasonOffInferred
		}
	default:
		return DecisionIgnore, ReasonDenied
	}
}

// shouldExecute reports whether the pipeline may create or resume a task.
// Explicit work always runs (any mode); inferred work only runs in auto mode.
func shouldExecute(mode Mode, strength Strength) bool {
	if strength == StrengthExplicit {
		return true
	}
	return strength == StrengthInferred && mode == ModeAuto
}
