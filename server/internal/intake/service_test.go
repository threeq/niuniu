package intake

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	event      EventRecord
	eventErr   error
	eventCalls int
	lastEvent  Trigger
	decisions  []DecisionRecord
	marked     []markedExecution
	decisionID int64
}

type markedExecution struct {
	decisionID int64
	result     ExecutionResult
}

func (f *fakeStore) RecordEvent(ctx context.Context, t Trigger) (EventRecord, error) {
	f.eventCalls++
	f.lastEvent = t
	return f.event, f.eventErr
}

func (f *fakeStore) RecordDecision(ctx context.Context, rec DecisionRecord) (int64, error) {
	f.decisions = append(f.decisions, rec)
	if f.decisionID == 0 {
		f.decisionID = 42
	}
	return f.decisionID, nil
}

func (f *fakeStore) MarkExecuted(ctx context.Context, decisionID int64, res ExecutionResult) error {
	f.marked = append(f.marked, markedExecution{decisionID: decisionID, result: res})
	return nil
}

type fakePolicies struct {
	policy Policy
	err    error
	calls  int
}

func (f *fakePolicies) LoadPolicy(ctx context.Context, projectID int64, sourceKind string) (Policy, error) {
	f.calls++
	return f.policy, f.err
}

type fakeResolver struct {
	resolution Resolution
	err        error
	calls      int
}

func (f *fakeResolver) Resolve(ctx context.Context, t Trigger) (Resolution, error) {
	f.calls++
	return f.resolution, f.err
}

type fakeExecutor struct {
	result ExecutionResult
	err    error
	plans  []ExecutionPlan
}

func (f *fakeExecutor) Execute(ctx context.Context, plan ExecutionPlan) (ExecutionResult, error) {
	f.plans = append(f.plans, plan)
	return f.result, f.err
}

func newHarness(policy Policy) (*Service, *fakeStore, *fakePolicies, *fakeResolver, *fakeExecutor) {
	store := &fakeStore{event: EventRecord{ID: 7}, decisionID: 42}
	policies := &fakePolicies{policy: policy}
	resolver := &fakeResolver{}
	executor := &fakeExecutor{result: ExecutionResult{IssueID: 100, WorkspaceID: 200}}
	svc := NewService(Deps{
		Enabled:  true,
		Store:    store,
		Policies: policies,
		Resolver: resolver,
		Executor: executor,
		Clock:    func() time.Time { return time.Unix(1000, 0) },
	})
	return svc, store, policies, resolver, executor
}

func trigger(actorID, text string) Trigger {
	return Trigger{
		ProjectID:  1,
		SourceKind: SourceKindIMBot,
		SourceRef:  "chat-1",
		EventID:    "evt-1",
		Thread:     ThreadRef{ThreadExtID: "thread-1"},
		Actor:      ActorRef{ExtID: actorID},
		Text:       text,
	}
}

func allowPolicy() Policy {
	return Policy{Version: 5, Mode: ModeOff, AllowList: []string{"ou_alice"}}
}

func TestHandleDisabledIsInert(t *testing.T) {
	svc := NewService(Deps{Enabled: false})
	res, err := svc.Handle(context.Background(), trigger("ou_alice", "do it"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionIgnore || res.ReasonCode != ReasonDisabled {
		t.Fatalf("result = %+v", res)
	}
}

func TestHandleEnabledWithoutDeps(t *testing.T) {
	svc := NewService(Deps{Enabled: true})
	if _, err := svc.Handle(context.Background(), trigger("ou_alice", "do it")); !errors.Is(err, ErrNotWired) {
		t.Fatalf("err = %v, want ErrNotWired", err)
	}
}

func TestHandleDuplicateShortCircuits(t *testing.T) {
	svc, store, policies, resolver, executor := newHarness(allowPolicy())
	store.event = EventRecord{ID: 7, Duplicate: true}

	res, err := svc.Handle(context.Background(), trigger("ou_alice", "do it"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.Duplicate || res.Decision != DecisionIgnore || res.ReasonCode != ReasonDuplicate {
		t.Fatalf("result = %+v", res)
	}
	if policies.calls != 0 || resolver.calls != 0 || len(executor.plans) != 0 {
		t.Fatalf("pipeline continued past a duplicate: policy=%d resolve=%d exec=%d",
			policies.calls, resolver.calls, len(executor.plans))
	}
}

func TestHandleDeniedRecordsDecisionWithoutExecuting(t *testing.T) {
	svc, store, _, resolver, executor := newHarness(Policy{Mode: ModeAuto})

	res, err := svc.Handle(context.Background(), trigger("ou_mallory", "do it"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionIgnore || res.Strength != StrengthDenied {
		t.Fatalf("result = %+v", res)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver called for a denied trigger")
	}
	if len(executor.plans) != 0 || len(store.marked) != 0 {
		t.Fatalf("denied trigger executed")
	}
	if len(store.decisions) != 1 {
		t.Fatalf("decisions recorded = %d, want 1", len(store.decisions))
	}
	rec := store.decisions[0]
	if rec.Decision != DecisionIgnore || rec.Strength != StrengthDenied || rec.Brief != nil || rec.WouldDo != nil {
		t.Fatalf("recorded decision = %+v", rec)
	}
}

func TestHandleExplicitExecutesInOffMode(t *testing.T) {
	svc, store, _, _, executor := newHarness(allowPolicy())

	res, err := svc.Handle(context.Background(), trigger("ou_alice", "fix bug"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionAccept || !res.Executed || res.IssueID != 100 || res.WorkspaceID != 200 {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.plans))
	}
	plan := executor.plans[0]
	if plan.Decision != DecisionAccept || plan.DecisionID != 42 || plan.Brief.Goal != "fix bug" {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Brief.Provenance.DecisionID != 42 || plan.Brief.Provenance.PolicyVersion != 5 {
		t.Fatalf("provenance = %+v", plan.Brief.Provenance)
	}
	if len(store.marked) != 1 || store.marked[0].decisionID != 42 {
		t.Fatalf("marked = %+v", store.marked)
	}
}

func TestHandleBoundThreadResumes(t *testing.T) {
	svc, _, _, resolver, executor := newHarness(allowPolicy())
	resolver.resolution = Resolution{
		Thread:  ThreadRef{ThreadExtID: "thread-1", IssueID: 5, WorkspaceID: 6},
		IssueID: 5, WorkspaceID: 6, Bound: true,
	}

	res, err := svc.Handle(context.Background(), trigger("ou_alice", "follow up"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionResume {
		t.Fatalf("decision = %q, want resume", res.Decision)
	}
	if len(executor.plans) != 1 || executor.plans[0].Decision != DecisionResume {
		t.Fatalf("plans = %+v", executor.plans)
	}
}

func TestHandleDirectRequestExecutesWithoutPolicy(t *testing.T) {
	// The IM adapter marks an approved chat's message Explicit, so it is
	// authorized without any allow-list entry (the chat pairing was the
	// admission decision) and still executes in off mode.
	svc, _, _, _, executor := newHarness(Policy{Mode: ModeOff})
	tg := trigger("", "帮我做张表")
	tg.Explicit = true

	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Strength != StrengthExplicit || res.Decision != DecisionAccept || !res.Executed {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 1 || executor.plans[0].Decision != DecisionAccept {
		t.Fatalf("plans = %+v", executor.plans)
	}
}

func TestHandleForceNewDoesNotResumeBoundThread(t *testing.T) {
	svc, _, _, resolver, executor := newHarness(allowPolicy())
	resolver.resolution = Resolution{
		Thread:  ThreadRef{ThreadExtID: "thread-1", IssueID: 5, WorkspaceID: 6},
		IssueID: 5, WorkspaceID: 6, Bound: true,
	}
	tg := trigger("ou_alice", "另起一个任务")
	tg.ForceNew = true

	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionAccept || res.ReasonCode != ReasonAccepted {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 1 || executor.plans[0].Decision != DecisionAccept {
		t.Fatalf("plans = %+v", executor.plans)
	}
}

func TestHandleObserveRecordsDryRunWithoutSideEffects(t *testing.T) {
	policy := Policy{
		Version: 3,
		Mode:    ModeObserve,
		Rules:   []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)

	res, err := svc.Handle(context.Background(), trigger("ou_dave", "please deploy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionAccept || res.Executed || res.ReasonCode != ReasonObserved {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 || len(store.marked) != 0 {
		t.Fatalf("dry-run produced side effects")
	}
	rec := store.decisions[0]
	if rec.WouldDo == nil || rec.WouldDo.Decision != DecisionAccept {
		t.Fatalf("wouldDo = %+v", rec.WouldDo)
	}
	if rec.WouldDo.Brief == nil || rec.WouldDo.Brief.Goal != "please deploy" {
		t.Fatalf("wouldDo brief = %+v", rec.WouldDo.Brief)
	}
	if len(rec.MatchedRuleIDs) != 1 || rec.MatchedRuleIDs[0] != 11 {
		t.Fatalf("matched rules = %v", rec.MatchedRuleIDs)
	}
}

func TestHandleAutoInferredExecutes(t *testing.T) {
	policy := Policy{
		Version: 3,
		Mode:    ModeAuto,
		Rules:   []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)

	res, err := svc.Handle(context.Background(), trigger("ou_dave", "please deploy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionAccept || !res.Executed || res.Strength != StrengthInferred {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 1 || len(store.marked) != 1 {
		t.Fatalf("execution did not run: plans=%d marked=%d", len(executor.plans), len(store.marked))
	}
	if store.decisions[0].WouldDo != nil {
		t.Fatalf("executed decision carried a wouldDo payload")
	}
}

func TestHandleOffModeIgnoresInferred(t *testing.T) {
	policy := Policy{
		Mode:  ModeOff,
		Rules: []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)

	res, err := svc.Handle(context.Background(), trigger("ou_dave", "please deploy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionIgnore || res.ReasonCode != ReasonOffInferred || res.Executed {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("off mode executed inferred work")
	}
	if store.decisions[0].Brief != nil || store.decisions[0].WouldDo != nil {
		t.Fatalf("ignored trigger carried a brief")
	}
}

func TestHandleStampsReceivedAt(t *testing.T) {
	svc, store, _, _, _ := newHarness(allowPolicy())
	if _, err := svc.Handle(context.Background(), trigger("ou_alice", "do it")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	want := time.Unix(1000, 0)
	if !store.lastEvent.ReceivedAt.Equal(want) {
		t.Fatalf("received at = %v, want %v", store.lastEvent.ReceivedAt, want)
	}
}
