package harness

import "context"

// CheckOpts is the legacy opts struct consumed by the old `Checker` interface.
// Kept during transition; new checkers should use CheckEnv + TypedChecker.
type CheckOpts struct {
	WorkspacePath string
	WorktreePaths []string
	CommitMessage string
	BranchName    string
	SpecConfig    string
	Phase         string
	AgentOutput   string // accumulated assistant output for the current phase
}

// Checker is the legacy interface keyed on category/name.
// Deprecated: implement TypedChecker instead.
type Checker interface {
	Name() string
	Check(ctx context.Context, opts CheckOpts) CheckResult
}

// CheckEnv carries inputs for typed checkers. It mirrors CheckOpts but drops
// SpecConfig (typed checkers read fields directly off Spec).
type CheckEnv struct {
	WorkspacePath string
	WorktreePaths []string
	CommitMessage string
	BranchName    string
	AgentOutput   string
	Phase         string
}

// TypedChecker is the new interface — receives the full Spec (so it can read
// typed config fields directly) and a CheckEnv with runtime inputs.
// Each TypedChecker is registered under its Kind() in CheckRunner.
type TypedChecker interface {
	Kind() string
	Run(ctx context.Context, spec Spec, env CheckEnv) CheckResult
}

// CheckOptsToEnv converts legacy opts into a CheckEnv, dropping SpecConfig
// which typed checkers do not consume.
func CheckOptsToEnv(opts CheckOpts) CheckEnv {
	return CheckEnv{
		WorkspacePath: opts.WorkspacePath,
		WorktreePaths: opts.WorktreePaths,
		CommitMessage: opts.CommitMessage,
		BranchName:    opts.BranchName,
		AgentOutput:   opts.AgentOutput,
		Phase:         opts.Phase,
	}
}
