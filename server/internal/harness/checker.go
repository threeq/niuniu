package harness

import "context"

// CheckOpts is the caller-facing input struct. It mirrors CheckEnv; the two are
// kept separate so callers keep passing CheckOpts while checkers receive the
// narrower CheckEnv.
type CheckOpts struct {
	WorkspacePath string
	WorktreePaths []string
	CommitMessage string
	BranchName    string
	Phase         string
	AgentOutput   string // accumulated assistant output for the current phase
	// IssueText is the linked issue's title + description, supplied by callers
	// that know which issue a check belongs to (the pre-commit path). It is what
	// the issue_conformance judge compares the change against.
	IssueText string
}

// CheckEnv carries runtime inputs for a checker. Typed checkers read their
// configuration off the Spec itself.
type CheckEnv struct {
	WorkspacePath string
	WorktreePaths []string
	CommitMessage string
	BranchName    string
	AgentOutput   string
	Phase         string
	IssueText     string
}

// TypedChecker evaluates a Spec. It receives the full Spec (reading its typed
// config fields directly) plus a CheckEnv of runtime inputs, and is registered
// under its Kind() in CheckRunner.
type TypedChecker interface {
	Kind() string
	Run(ctx context.Context, spec Spec, env CheckEnv) CheckResult
}

// CheckOptsToEnv converts caller opts into the checker-facing CheckEnv.
func CheckOptsToEnv(opts CheckOpts) CheckEnv {
	return CheckEnv{
		WorkspacePath: opts.WorkspacePath,
		WorktreePaths: opts.WorktreePaths,
		CommitMessage: opts.CommitMessage,
		BranchName:    opts.BranchName,
		AgentOutput:   opts.AgentOutput,
		Phase:         opts.Phase,
		IssueText:     opts.IssueText,
	}
}
