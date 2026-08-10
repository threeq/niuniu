package service

import "context"

// Orchestration server-settings keys (stored in the server_settings table,
// runtime-tunable via /api/admin/settings/:key without a restart).
const (
	KeyOrchBudgetUSD     = "orchestration.chain_cost_budget_usd"
	KeyOrchMaxConcurrent = "orchestration.max_concurrent_workspaces"
	KeyOrchMaxBatch      = "orchestration.max_batch_issues"
	KeyOrchWarnPct       = "orchestration.chain_cost_warn_ratio"
)

// OrchestrationSettings is the single runtime source for the orchestration cost
// guardrails. It reads live values from the server_settings store (5s cached)
// and falls back to the config-derived defaults captured at wiring time when a
// key has never been written. Both OrchestrationGuard and KanbanService read
// through it so a settings change applies everywhere at once.
type OrchestrationSettings struct {
	store *ServerSettingsService
	// defaults captured from config at construction; ints so they ride the
	// existing GetInt rail. Budget is whole USD; warn is whole percent 0..100.
	defBudgetUSD     int
	defMaxConcurrent int
	defMaxBatch      int
	defWarnPct       int
}

// NewOrchestrationSettings wires the live store and the config-derived defaults.
func NewOrchestrationSettings(s *ServerSettingsService, defBudgetUSD, defMaxConcurrent, defMaxBatch, defWarnPct int) *OrchestrationSettings {
	return &OrchestrationSettings{
		store:            s,
		defBudgetUSD:     defBudgetUSD,
		defMaxConcurrent: defMaxConcurrent,
		defMaxBatch:      defMaxBatch,
		defWarnPct:       defWarnPct,
	}
}

// BudgetUSD is the per-orchestration-tree cost cap in USD. 0 disables the cap.
func (o *OrchestrationSettings) BudgetUSD(ctx context.Context) float64 {
	if o == nil || o.store == nil {
		return 0
	}
	return float64(o.store.GetInt(ctx, KeyOrchBudgetUSD, o.defBudgetUSD))
}

// MaxConcurrentWorkspaces caps concurrently-active workspaces per owner.
func (o *OrchestrationSettings) MaxConcurrentWorkspaces(ctx context.Context) int {
	if o == nil || o.store == nil {
		return 0
	}
	return o.store.GetInt(ctx, KeyOrchMaxConcurrent, o.defMaxConcurrent)
}

// MaxBatchIssues caps how many issues one batch_create_issues call may create.
func (o *OrchestrationSettings) MaxBatchIssues(ctx context.Context) int {
	if o == nil || o.store == nil {
		return 0
	}
	return o.store.GetInt(ctx, KeyOrchMaxBatch, o.defMaxBatch)
}

// WarnRatio is the fraction (0..1) of the budget at which to flag near-budget.
func (o *OrchestrationSettings) WarnRatio(ctx context.Context) float64 {
	if o == nil || o.store == nil {
		return 0
	}
	return float64(o.store.GetInt(ctx, KeyOrchWarnPct, o.defWarnPct)) / 100.0
}
