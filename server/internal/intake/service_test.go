package intake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeStore struct {
	suggestions    []PendingSuggestion
	suggestionsErr error
	consumed       []int64
	event          EventRecord
	eventErr       error
	eventCalls     int
	lastEvent      Trigger
	decisions      []DecisionRecord
	marked         []markedExecution
	decisionID     int64
	reserved       int64
	reserveCalls   int
	reserveErr     error
	released       int
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

func (f *fakeStore) ListPendingSuggestions(ctx context.Context, projectID int64, conversationExtID string) ([]PendingSuggestion, error) {
	return f.suggestions, f.suggestionsErr
}

func (f *fakeStore) MarkSuggestionsConsumed(ctx context.Context, ids []int64) error {
	f.consumed = append(f.consumed, ids...)
	return nil
}

func (f *fakeStore) ReserveDailySlot(ctx context.Context, projectID int64, sourceKind, day string, cap int64) (bool, error) {
	f.reserveCalls++
	if f.reserveErr != nil {
		return false, f.reserveErr
	}
	if f.reserved >= cap {
		return false, nil
	}
	f.reserved++
	return true, nil
}

func (f *fakeStore) ReleaseDailySlot(ctx context.Context, projectID int64, sourceKind, day string) error {
	f.released++
	if f.reserved > 0 {
		f.reserved--
	}
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

func TestHandleAutoInferredWithoutRuleBecomesSuggestion(t *testing.T) {
	// A bot is inferred by bot_list. Inferred work only executes inside a
	// matching rule, so with no rule it must not create a task — instead it is
	// recorded as a pending suggestion for the review queue.
	policy := Policy{
		Mode:    ModeAuto,
		BotList: []string{"bot_release"},
	}
	svc, store, _, _, executor := newHarness(policy)

	tg := trigger("bot_release", "build it")
	tg.Actor.IsBot = true
	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionAccept || res.ReasonCode != ReasonNoRule || res.Executed {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 || len(store.marked) != 0 {
		t.Fatalf("inferred work without a rule executed")
	}
	if len(store.decisions) != 1 || store.decisions[0].WouldDo == nil {
		t.Fatalf("suggestion not recorded as a dry-run decision: %+v", store.decisions)
	}
}

func TestHandleObserveUnlistedReceiveAllRecordsDryRun(t *testing.T) {
	policy := Policy{Version: 4, Mode: ModeObserve, AllowList: []string{"ou_alice"}}
	svc, store, _, _, executor := newHarness(policy)

	tg := trigger("ou_bob", "整理本周进展")
	tg.Explicit = true
	tg.ReceiveAll = true
	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Strength != StrengthInferred || res.ReasonCode != ReasonObserved || res.Executed {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("observe produced a side effect")
	}
	rec := store.decisions[0]
	if rec.PolicyVersion != 4 || rec.MatchedRuleIDs != nil && len(rec.MatchedRuleIDs) != 0 {
		t.Fatalf("recorded = %+v", rec)
	}
	if rec.WouldDo == nil || rec.WouldDo.Decision != DecisionAccept {
		t.Fatalf("wouldDo = %+v", rec.WouldDo)
	}
}

func TestHandlePolicyChangeAppliesToNextEvent(t *testing.T) {
	svc, store, policies, _, executor := newHarness(Policy{
		Version: 1,
		Mode:    ModeAuto,
		Rules:   []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	})

	first := trigger("ou_dave", "please deploy")
	if res, err := svc.Handle(context.Background(), first); err != nil || !res.Executed {
		t.Fatalf("first event = %+v, err = %v", res, err)
	}
	if store.decisions[0].PolicyVersion != 1 {
		t.Fatalf("first decision version = %d, want 1", store.decisions[0].PolicyVersion)
	}

	// The rule is disabled between events: policy is reloaded per trigger, so
	// the very next event sees the new policy and its version.
	policies.policy = Policy{Version: 2, Mode: ModeAuto, BotList: []string{"ou_dave"}}
	second := trigger("ou_dave", "please deploy")
	second.EventID = "evt-2"
	second.Actor.IsBot = true
	if res, err := svc.Handle(context.Background(), second); err != nil || res.Executed {
		t.Fatalf("second event = %+v, err = %v", res, err)
	}
	if policies.calls != 2 {
		t.Fatalf("policy loads = %d, want 2", policies.calls)
	}
	if store.decisions[1].PolicyVersion != 2 || store.decisions[1].ReasonCode != ReasonNoRule {
		t.Fatalf("second decision = %+v", store.decisions[1])
	}
	if len(executor.plans) != 1 {
		t.Fatalf("executor plans = %d, want 1", len(executor.plans))
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

func TestHandleDailyCapBlocksInferred(t *testing.T) {
	policy := Policy{
		Mode:     ModeAuto,
		DailyCap: 2,
		Rules:    []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)
	store.reserved = 2

	res, err := svc.Handle(context.Background(), trigger("ou_dave", "please deploy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionIgnore || res.ReasonCode != ReasonDailyCap || res.Executed {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 || len(store.marked) != 0 {
		t.Fatalf("daily cap did not stop execution")
	}
	if store.reserveCalls != 1 {
		t.Fatalf("reserve calls = %d, want 1", store.reserveCalls)
	}
	if store.reserved != 2 {
		t.Fatalf("cap was consumed on a rejected event: reserved = %d", store.reserved)
	}
	if store.decisions[0].Decision != DecisionIgnore || store.decisions[0].WouldDo != nil {
		t.Fatalf("recorded = %+v", store.decisions[0])
	}
}

func TestHandleDailyCapDoesNotLimitExplicit(t *testing.T) {
	policy := allowPolicy()
	policy.DailyCap = 1
	svc, store, _, _, executor := newHarness(policy)
	store.reserved = 99

	res, err := svc.Handle(context.Background(), trigger("ou_alice", "do it"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.Executed || len(executor.plans) != 1 {
		t.Fatalf("explicit request was capped: %+v", res)
	}
	if store.reserveCalls != 0 {
		t.Fatalf("reserve calls = %d, want 0 for explicit work", store.reserveCalls)
	}
}

func TestHandleDailyCapZeroIsUnlimited(t *testing.T) {
	policy := Policy{
		Mode:  ModeAuto,
		Rules: []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)
	store.reserved = 99

	res, err := svc.Handle(context.Background(), trigger("ou_dave", "please deploy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.Executed || len(executor.plans) != 1 {
		t.Fatalf("cap=0 should not limit: %+v", res)
	}
	if store.reserveCalls != 0 {
		t.Fatalf("reserve calls = %d, want 0", store.reserveCalls)
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

// TestHandleTriggerModeOverridesPolicy is the per-chat mode: the trigger's own
// mode wins over the project+source setting, so a chat in observe dry-runs even
// when its project policy says auto.
func TestHandleTriggerModeOverridesPolicy(t *testing.T) {
	policy := Policy{
		Mode:  ModeAuto,
		Rules: []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)

	tg := trigger("ou_dave", "please deploy")
	tg.Mode = ModeObserve
	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionAccept || res.Executed || res.ReasonCode != ReasonObserved {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("chat-level observe mode executed")
	}
	if rec := store.decisions[0]; rec.Mode != ModeObserve || rec.WouldDo == nil {
		t.Fatalf("record = %+v, want observe mode with would-do", rec)
	}
}

// TestProposeRecordsDryRunWithoutSideEffects covers a source-derived proposal
// (the IM employee's task verdict): observe records executed=0 with the would-do
// payload; auto and off record nothing and never execute.
func TestProposeRecordsDryRunWithoutSideEffects(t *testing.T) {
	svc, store, _, resolver, executor := newHarness(Policy{Mode: ModeObserve, Version: 7})

	tg := trigger("bot:employee", "整理季度数据")
	tg.EventID = ""
	tg.EventKind = EventKindProposal
	tg.Mode = ModeObserve
	res, err := svc.Propose(context.Background(), Proposal{Trigger: tg, Goal: "整理季度数据"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if res.Decision != DecisionAccept || res.Executed || res.ReasonCode != ReasonObserved {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 || len(store.marked) != 0 {
		t.Fatalf("proposal produced side effects")
	}
	if resolver.calls != 0 {
		t.Fatalf("proposal resolved a task; it must not touch routing")
	}
	rec := store.decisions[0]
	if rec.WouldDo == nil || rec.WouldDo.Brief == nil {
		t.Fatalf("record = %+v, want a dry-run would-do", rec)
	}
	if rec.WouldDo.Brief.Goal != "整理季度数据" {
		t.Fatalf("would-do goal = %q", rec.WouldDo.Brief.Goal)
	}
	if rec.Strength != StrengthInferred || rec.PolicyVersion != 7 {
		t.Fatalf("record meta = %+v", rec)
	}
}

// A proposal is a dry-run record, never an execution, so it is recorded in both
// observe and auto; only off mode (inferred work not evaluated) drops it.
func TestProposeRecordsInObserveAndAutoNeverExecutes(t *testing.T) {
	for _, mode := range []Mode{ModeObserve, ModeAuto} {
		svc, store, _, _, executor := newHarness(Policy{Mode: mode})
		tg := trigger("bot:employee", "整理季度数据")
		tg.Mode = mode
		if _, err := svc.Propose(context.Background(), Proposal{Trigger: tg, Goal: "整理季度数据"}); err != nil {
			t.Fatalf("Propose(%s): %v", mode, err)
		}
		if len(store.decisions) != 1 || store.eventCalls != 1 {
			t.Fatalf("mode %s recorded %d events / %d decisions, want 1/1", mode, store.eventCalls, len(store.decisions))
		}
		if len(executor.plans) != 0 {
			t.Fatalf("mode %s executed", mode)
		}
	}

	svc, store, _, _, executor := newHarness(Policy{Mode: ModeOff})
	tg := trigger("bot:employee", "整理季度数据")
	tg.Mode = ModeOff
	if _, err := svc.Propose(context.Background(), Proposal{Trigger: tg, Goal: "整理季度数据"}); err != nil {
		t.Fatalf("Propose(off): %v", err)
	}
	if len(store.decisions) != 0 || store.eventCalls != 0 {
		t.Fatalf("off mode recorded or stored something")
	}
	if len(executor.plans) != 0 {
		t.Fatalf("off mode executed")
	}
}

// TestHandlePausedForcesOff covers the project stop switch: paused wins over a
// source-level auto mode, so inferred work neither executes nor gets recorded as
// a dry-run, while an explicit request still runs.
func TestHandlePausedForcesOff(t *testing.T) {
	policy := Policy{
		Mode:   ModeAuto,
		Paused: true,
		Rules:  []Rule{{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}}},
	}
	svc, store, _, _, executor := newHarness(policy)

	res, err := svc.Handle(context.Background(), trigger("ou_dave", "please deploy"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionIgnore || res.Executed || res.ReasonCode != ReasonOffInferred {
		t.Fatalf("result = %+v", res)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("paused inferred work executed")
	}
	if rec := store.decisions[0]; rec.Mode != ModeOff {
		t.Fatalf("recorded mode = %s, want off", rec.Mode)
	}

	svc, _, _, _, executor = newHarness(Policy{Mode: ModeAuto, Paused: true, AllowList: []string{"ou_alice"}})
	res, err = svc.Handle(context.Background(), trigger("ou_alice", "fix bug"))
	if err != nil {
		t.Fatalf("Handle explicit: %v", err)
	}
	if res.Decision != DecisionAccept || !res.Executed {
		t.Fatalf("explicit request while paused = %+v", res)
	}
	if len(executor.plans) != 1 {
		t.Fatalf("explicit request while paused did not execute")
	}
}

func TestProposeSkipsWhilePaused(t *testing.T) {
	svc, store, _, _, _ := newHarness(Policy{Mode: ModeObserve, Paused: true})
	tg := trigger("bot:employee", "整理季度数据")
	tg.EventID = ""
	tg.EventKind = EventKindProposal
	if _, err := svc.Propose(context.Background(), Proposal{Trigger: tg, Goal: "整理季度数据"}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(store.decisions) != 0 || store.eventCalls != 0 {
		t.Fatalf("paused proposal recorded or stored something")
	}
}

// A trigger delivered after the staleness window is recorded for audit but never
// executed: a reconnect can replay a backlog the conversation has moved past.
func TestHandleStaleTriggerRecordedNotExecuted(t *testing.T) {
	svc, store, _, _, executor := newHarness(Policy{Mode: ModeAuto, AllowList: []string{"alice"}})
	now := time.Unix(1000, 0)
	tg := trigger("alice", "这条太旧了")
	tg.ReceivedAt = now.Add(-11 * time.Minute)

	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if res.ReasonCode != ReasonStale || res.Decision != DecisionIgnore {
		t.Fatalf("result = %+v, want stale/ignore", res)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("stale trigger executed %d plan(s)", len(executor.plans))
	}
	if len(store.decisions) != 1 || store.decisions[0].ReasonCode != ReasonStale {
		t.Fatalf("decisions = %+v, want one stale record", store.decisions)
	}
}

// Inside the window the trigger runs normally.
func TestHandleRecentTriggerExecutes(t *testing.T) {
	svc, _, _, _, executor := newHarness(Policy{Mode: ModeAuto, AllowList: []string{"alice"}})
	tg := trigger("alice", "正常消息")
	tg.ReceivedAt = time.Unix(1000, 0).Add(-2 * time.Minute)

	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if res.ReasonCode == ReasonStale {
		t.Fatalf("recent trigger marked stale: %+v", res)
	}
	if len(executor.plans) != 1 {
		t.Fatalf("executed %d plans, want 1", len(executor.plans))
	}
}

// An automatic execution absorbs the conversation's pending suggestions: their
// goals ride along in the executed text, and they are marked consumed so they
// are never acted on twice.
func TestHandleExecutesConsumesPendingSuggestions(t *testing.T) {
	policy := Policy{Mode: ModeOff, AllowList: []string{"ou_alice"}}
	svc, store, _, _, executor := newHarness(policy)
	store.suggestions = []PendingSuggestion{{ID: 91, Goal: "补一条日报"}, {ID: 92, Goal: "检查昨天数据"}}

	tg := trigger("ou_alice", "发一下日报")
	tg.ConversationExtID = "chat-1"
	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.Executed {
		t.Fatalf("explicit request did not execute: %+v", res)
	}
	if len(store.consumed) != 2 || store.consumed[0] != 91 || store.consumed[1] != 92 {
		t.Fatalf("consumed = %v, want [91 92]", store.consumed)
	}
	if len(executor.plans) != 1 {
		t.Fatalf("plans = %d, want 1", len(executor.plans))
	}
	if text := executor.plans[0].Trigger.Text; !strings.Contains(text, "补一条日报") || !strings.Contains(text, "检查昨天数据") {
		t.Fatalf("execution text missing pending goals: %q", text)
	}
}

// A non-executing decision (denied or dry-run) must leave the suggestions for a
// later execution.
func TestHandleNonExecutionLeavesSuggestions(t *testing.T) {
	policy := Policy{Mode: ModeAuto, BotList: []string{"bot_x"}}
	svc, store, _, _, executor := newHarness(policy)
	store.suggestions = []PendingSuggestion{{ID: 91, Goal: "补一条日报"}}

	tg := trigger("bot_x", "build it")
	tg.Actor.IsBot = true
	tg.ConversationExtID = "chat-1"
	if _, err := svc.Handle(context.Background(), tg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("executed %d plans, want 0", len(executor.plans))
	}
	if len(store.consumed) != 0 {
		t.Fatalf("consumed = %v, want none", store.consumed)
	}
}

// A require-bound event (CI success/recovery/cancel) with no bound task is
// recorded as an ignore and creates nothing.
func TestHandleRequireBoundIgnoresUnbound(t *testing.T) {
	svc, store, _, resolver, executor := newHarness(allowPolicy())
	resolver.resolution = Resolution{Thread: ThreadRef{ThreadExtID: "thread-1"}}

	tg := trigger("ou_alice", "ci passed")
	tg.RequireBound = true
	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionIgnore || res.ReasonCode != ReasonUnbound || res.Executed {
		t.Fatalf("result = %+v, want ignore/unbound", res)
	}
	if len(executor.plans) != 0 {
		t.Fatalf("unbound require-bound event executed")
	}
	if len(store.decisions) != 1 || store.decisions[0].Decision != DecisionIgnore {
		t.Fatalf("decisions = %+v", store.decisions)
	}
}

// The same event resumes the task when the thread is bound.
func TestHandleRequireBoundResumesBoundTask(t *testing.T) {
	svc, _, _, resolver, executor := newHarness(allowPolicy())
	resolver.resolution = Resolution{
		Thread: ThreadRef{ThreadExtID: "thread-1"}, IssueID: 100, WorkspaceID: 200, Bound: true,
	}

	tg := trigger("ou_alice", "ci passed")
	tg.RequireBound = true
	res, err := svc.Handle(context.Background(), tg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Decision != DecisionResume || !res.Executed {
		t.Fatalf("result = %+v, want resume", res)
	}
	if len(executor.plans) != 1 || executor.plans[0].WorkspaceID != 200 {
		t.Fatalf("plans = %+v", executor.plans)
	}
}
