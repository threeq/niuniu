package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// OrchestrationLimits are the cost guardrails that bound the AI-native board
// orchestrator (spec 2026-06-05 section 16, phase 6). A value <= 0 disables that
// particular guardrail, so the zero value is "no limits" and the guard stays a
// safe no-op until configured.
type OrchestrationLimits struct {
	// MaxBatchIssues caps how many issues one batch_create_issues call may create.
	MaxBatchIssues int
	// MaxConcurrentWorkspaces caps concurrently-active (non-archived, not-completed)
	// workspaces per owner; over the cap, start_workspace queues instead of rejects.
	MaxConcurrentWorkspaces int
	// ChainCostBudgetUSD caps the cumulative cost of one orchestration tree (an
	// epic plus its child workspaces).
	ChainCostBudgetUSD float64
	// ChainCostWarnRatio is the fraction of the budget at which start_workspace
	// still admits but flags the orchestrator to ask_user before fanning out more.
	ChainCostWarnRatio float64
}

// Guard decision actions returned by AdmitWorkspace.
const (
	GuardAdmit = "admit" // proceed to create the workspace now
	GuardQueue = "queue" // owner at the concurrency cap; the issue was enqueued
	GuardBlock = "block" // chain cost budget exhausted; do not fan out further
)

// GuardDecision is the outcome of an AdmitWorkspace check. Warn is set independently
// of Action: an admitted or queued workspace may still carry a near-budget
// warning so the orchestration agent can choose to ask_user.
type GuardDecision struct {
	Action        string  // admit | queue | block
	QueuePosition int     // 1-based queue position when Action == queue
	Warn          string  // near-budget advisory (may accompany admit or queue)
	Reason        string  // human-readable reason for queue / block
	SpentUSD      float64 // chain spend so far (0 for non-orchestration issues)
	BudgetUSD     float64 // chain budget in effect (0 when disabled)
}

// WorkspaceStarter starts a queued issue's workspace, bypassing admission (the
// queue already decided to admit it). *EpicExecutionService implements it.
type WorkspaceStarter interface {
	StartQueuedWorkspace(ctx context.Context, issueID int64) error
}

// OrchestrationGuard enforces the fan-out / concurrency / chain-cost limits in
// front of the orchestrator's expensive operations. It is safe to use as a nil
// pointer: every method treats a nil receiver as "no limits", so callers that
// have not wired a guard keep their pre-guardrail behaviour.
type OrchestrationGuard struct {
	q       *store.Queries
	limits  OrchestrationLimits
	starter WorkspaceStarter
	// settings, when set, supplies live (runtime-tunable) limit values; the
	// static `limits` field becomes the fallback used only when settings==nil
	// (keeps pre-settings unit tests behaving exactly as before).
	settings *OrchestrationSettings
	// mu serialises queue draining so a burst of completion events cannot
	// double-start the same queued entry.
	mu sync.Mutex
}

// NewOrchestrationGuard constructs the guard. q is the shared sqlc queries; the
// limits come from config. Wire the starter with SetStarter after the workspace
// engine exists (it closes the create loop for queue draining).
func NewOrchestrationGuard(q *store.Queries, limits OrchestrationLimits) *OrchestrationGuard {
	return &OrchestrationGuard{q: q, limits: limits}
}

// SetStarter wires the callback used to start a queued workspace when a slot
// frees. Optional; without it OnSlotFreed is a no-op (the queue still records
// entries, they just are not auto-drained).
func (g *OrchestrationGuard) SetStarter(s WorkspaceStarter) {
	if g == nil {
		return
	}
	g.starter = s
}

// SetSettings wires the live settings source. When set, the guard reads limits
// from it on every decision instead of the static config snapshot. Nil-safe.
func (g *OrchestrationGuard) SetSettings(s *OrchestrationSettings) {
	if g == nil {
		return
	}
	g.settings = s
}

func (g *OrchestrationGuard) effBudgetUSD(ctx context.Context) float64 {
	if g.settings != nil {
		return g.settings.BudgetUSD(ctx)
	}
	return g.limits.ChainCostBudgetUSD
}

func (g *OrchestrationGuard) effWarnRatio(ctx context.Context) float64 {
	if g.settings != nil {
		return g.settings.WarnRatio(ctx)
	}
	return g.limits.ChainCostWarnRatio
}

func (g *OrchestrationGuard) effMaxConcurrent(ctx context.Context) int {
	if g.settings != nil {
		return g.settings.MaxConcurrentWorkspaces(ctx)
	}
	return g.limits.MaxConcurrentWorkspaces
}

func (g *OrchestrationGuard) effMaxBatch(ctx context.Context) int {
	if g.settings != nil {
		return g.settings.MaxBatchIssues(ctx)
	}
	return g.limits.MaxBatchIssues
}

// CheckFanOutBatch rejects a batch_create_issues call that exceeds the per-call
// cap. The error is phrased so the orchestration agent knows to split the batch.
func (g *OrchestrationGuard) CheckFanOutBatch(ctx context.Context, n int) error {
	if g == nil {
		return nil
	}
	max := g.effMaxBatch(ctx)
	if max <= 0 {
		return nil
	}
	if n > max {
		return fmt.Errorf("一次最多创建 %d 个任务(本次 %d 个)，请拆成多批分次创建", max, n)
	}
	return nil
}

// AdmitWorkspace decides whether an issue's workspace may start now. Order of
// checks: (1) chain cost budget — block when exhausted; (2) per-owner concurrency
// — queue when at the cap. A near-budget warning attaches to whatever admit/queue
// decision results. A nil guard always admits.
func (g *OrchestrationGuard) AdmitWorkspace(ctx context.Context, issueID int64) (GuardDecision, error) {
	if g == nil {
		return GuardDecision{Action: GuardAdmit}, nil
	}
	issue, err := g.q.GetIssue(ctx, issueID)
	if err != nil {
		return GuardDecision{}, fmt.Errorf("load issue %d: %w", issueID, err)
	}
	ownerRow, err := g.q.GetProjectAndLifecycleByIssueID(ctx, issueID)
	if err != nil {
		return GuardDecision{}, fmt.Errorf("resolve issue owner: %w", err)
	}
	ownerType, ownerID := ownerRow.ProjectOwnerType, ownerRow.ProjectOwnerID

	// (1) Chain cost budget — only orchestration-tree issues (an epic or a child
	// of an epic) accumulate against it; standalone issues are unbounded here.
	var warn string
	var spent, budget float64
	if epicID, managed := epicIDForIssue(issue); managed && g.effBudgetUSD(ctx) > 0 {
		budget = g.effBudgetUSD(ctx)
		spent, err = g.q.SumEpicSubtreeCostUSD(ctx, store.SumEpicSubtreeCostUSDParams{
			ID:            epicID,
			ParentIssueID: sql.NullInt64{Int64: epicID, Valid: true},
		})
		if err != nil {
			return GuardDecision{}, fmt.Errorf("sum chain cost: %w", err)
		}
		if spent >= budget {
			return GuardDecision{
				Action:    GuardBlock,
				Reason:    fmt.Sprintf("编排树累计成本 $%.2f 已达预算上限 $%.2f，请暂停 fan-out 并用 niuniu_ask_user_question 与用户确认是否继续", spent, budget),
				SpentUSD:  spent,
				BudgetUSD: budget,
			}, nil
		}
		if warnRatio := g.effWarnRatio(ctx); warnRatio > 0 && spent >= budget*warnRatio {
			warn = fmt.Sprintf("编排树累计成本 $%.2f 已接近预算上限 $%.2f，继续 fan-out 前建议用 niuniu_ask_user_question 与用户确认", spent, budget)
		}
	}

	// (2) Per-owner concurrency — queue (never reject) when at the cap.
	//
	// This count-then-create is a deliberate best-effort snapshot, NOT a hard
	// transactional gate (spec 2026-06-06 section 6): two concurrent
	// start_workspace calls for the same owner can each read active==cap-1 and
	// both proceed, transiently overshooting the cap by a small amount. That is a
	// cost guardrail, not a safety invariant — the overshoot self-corrects on the
	// next completion (which drains nothing while over-cap) and the intended caller
	// is a single orchestration agent dispatching serially. Strong per-owner
	// serialization is left to the Postgres path / a later phase; do not wrap this
	// in g.mu, which is reserved for drain serialization (OnSlotFreed).
	if maxConc := g.effMaxConcurrent(ctx); maxConc > 0 {
		active, err := g.q.CountActiveWorkspacesForOwner(ctx, store.CountActiveWorkspacesForOwnerParams{
			OwnerType: ownerType,
			OwnerID:   ownerID,
		})
		if err != nil {
			return GuardDecision{}, fmt.Errorf("count active workspaces: %w", err)
		}
		if active >= int64(maxConc) {
			pos, err := g.enqueueOnce(ctx, ownerType, ownerID, issueID)
			if err != nil {
				return GuardDecision{}, err
			}
			return GuardDecision{
				Action:        GuardQueue,
				QueuePosition: pos,
				Warn:          warn,
				Reason:        fmt.Sprintf("owner 并发工作空间已达上限 %d，已排队，空出名额后自动启动", maxConc),
				SpentUSD:      spent,
				BudgetUSD:     budget,
			}, nil
		}
	}

	return GuardDecision{Action: GuardAdmit, Warn: warn, SpentUSD: spent, BudgetUSD: budget}, nil
}

// enqueueOnce appends the issue to the owner's start queue unless it is already
// queued (idempotent re-dispatch), and returns its 1-based queue position.
func (g *OrchestrationGuard) enqueueOnce(ctx context.Context, ownerType string, ownerID, issueID int64) (int, error) {
	already, err := g.q.CountQueuedForIssue(ctx, issueID)
	if err != nil {
		return 0, fmt.Errorf("check queued: %w", err)
	}
	if already == 0 {
		if err := g.q.EnqueueWorkspaceStart(ctx, store.EnqueueWorkspaceStartParams{
			OwnerType: ownerType,
			OwnerID:   ownerID,
			IssueID:   issueID,
		}); err != nil {
			return 0, fmt.Errorf("enqueue workspace start: %w", err)
		}
	}
	pos, err := g.q.CountQueuedForOwner(ctx, store.CountQueuedForOwnerParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
	if err != nil {
		return 0, fmt.Errorf("queue position: %w", err)
	}
	return int(pos), nil
}

// DrainAllQueuedOwners drains the start queue for every owner that currently has
// queued entries. It is invoked when the concurrency cap is raised or set to
// unlimited so the freed capacity is used immediately, instead of leaving the
// already-queued issues to wait for the next workspace-completion event (which a
// settings change does not itself produce). nil-safe; a no-op without a starter or
// when the queue is empty. Each owner's drain is serialised by OnSlotFreed's lock.
func (g *OrchestrationGuard) DrainAllQueuedOwners(ctx context.Context) {
	if g == nil || g.starter == nil {
		return
	}
	owners, err := g.q.ListOwnersWithQueuedStarts(ctx)
	if err != nil {
		slog.Error("orchestration guard: list owners with queued starts", "error", err)
		return
	}
	for _, o := range owners {
		g.OnSlotFreed(ctx, o.OwnerType, o.OwnerID)
	}
}

// OnSlotFreed drains the owner's queue when a workspace completes/archives and a
// slot opens. It starts as many queued issues as there is freed capacity, marking
// each 'started' before launching so a concurrent drain cannot double-start it. A
// start failure (e.g. the issue was archived while queued) cancels that entry and
// the drain moves on. Serialised by g.mu; nil-safe.
func (g *OrchestrationGuard) OnSlotFreed(ctx context.Context, ownerType string, ownerID int64) {
	if g == nil || g.starter == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		// maxConc <= 0 means "no concurrency cap". When the cap is removed (e.g. the
		// user sets it to unlimited) there is always free capacity, so drain the WHOLE
		// queue — otherwise entries enqueued while the cap was still finite would be
		// stranded forever: the admission path no longer queues new work, and nothing
		// else revisits the old queue, so a queued issue stays queued=true with no path
		// to start. A positive cap still drains only up to the active==cap boundary.
		if maxConc := g.effMaxConcurrent(ctx); maxConc > 0 {
			active, err := g.q.CountActiveWorkspacesForOwner(ctx, store.CountActiveWorkspacesForOwnerParams{
				OwnerType: ownerType,
				OwnerID:   ownerID,
			})
			if err != nil {
				slog.Error("orchestration guard drain: count active", "ownerType", ownerType, "ownerID", ownerID, "error", err)
				return
			}
			if active >= int64(maxConc) {
				return // no free capacity
			}
		}
		next, err := g.q.DequeueNextForOwner(ctx, store.DequeueNextForOwnerParams{
			OwnerType: ownerType,
			OwnerID:   ownerID,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return // queue empty
			}
			slog.Error("orchestration guard drain: dequeue", "ownerType", ownerType, "ownerID", ownerID, "error", err)
			return
		}
		// Claim the entry before starting so a re-entrant drain skips it. A crash
		// between this mark and a successful create orphans the entry as 'started'
		// with no workspace; that is fail-safe (the owner is down one dispatch, never
		// a runaway) and is not auto-recovered in this phase — the orchestration
		// agent re-dispatches if it still needs the sub-task.
		if err := g.q.MarkStartQueueStarted(ctx, next.ID); err != nil {
			slog.Error("orchestration guard drain: mark started", "queueID", next.ID, "error", err)
			return
		}
		if err := g.starter.StartQueuedWorkspace(ctx, next.IssueID); err != nil {
			slog.Warn("orchestration guard drain: start queued workspace failed, canceling entry",
				"queueID", next.ID, "issueID", next.IssueID, "error", err)
			if cErr := g.q.MarkStartQueueCanceled(ctx, next.ID); cErr != nil {
				slog.Error("orchestration guard drain: cancel entry", "queueID", next.ID, "error", cErr)
			}
			continue // a failed start did not consume a slot; try the next entry
		}
		slog.Info("orchestration guard drain: started queued workspace", "queueID", next.ID, "issueID", next.IssueID)
	}
}
