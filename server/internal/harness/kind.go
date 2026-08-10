package harness

// Kind constants identify the generic checker that evaluates a Spec.
// Each Kind reads a specific subset of typed Spec fields.
const (
	KindRegexMatch         = "regex_match"
	KindCommandExitCode    = "command_exit_code"
	KindCommandOutputMatch = "command_output_match"
	KindFileExists         = "file_exists"
	KindAIJudge            = "ai_judge"
)

// Target constants identify which input field a regex_match checker evaluates.
const (
	TargetCommitMessage = "commit_message"
	TargetBranchName    = "branch_name"
	TargetAgentOutput   = "agent_output"
	TargetWorkingDir    = "working_dir"
)

// Trigger constants identify when a Spec runs. phase_exit (default) preserves
// legacy behavior; on_demand fires only from the API test endpoint;
// pre_commit fires from the harness_pre_commit_check MCP tool;
// on_schedule is reserved for the scheduler integration (Phase C).
const (
	TriggerPhaseExit  = "phase_exit"
	TriggerOnDemand   = "on_demand"
	TriggerPreCommit  = "pre_commit"
	TriggerOnSchedule = "on_schedule"
)

// Threshold operators used by command_output_match.
const (
	ThresholdGTE = ">="
	ThresholdLTE = "<="
	ThresholdEQ  = "=="
	ThresholdNEQ = "!="
	ThresholdGT  = ">"
	ThresholdLT  = "<"
)

// ValidKinds returns the set of accepted Kind values for validation.
func ValidKinds() []string {
	return []string{KindRegexMatch, KindCommandExitCode, KindCommandOutputMatch, KindFileExists, KindAIJudge}
}

// IsValidKind reports whether k is one of the registered Kinds.
func IsValidKind(k string) bool {
	switch k {
	case KindRegexMatch, KindCommandExitCode, KindCommandOutputMatch, KindFileExists, KindAIJudge:
		return true
	}
	return false
}

// IsValidThresholdOp reports whether op is a known comparison operator.
func IsValidThresholdOp(op string) bool {
	switch op {
	case ThresholdGTE, ThresholdLTE, ThresholdEQ, ThresholdNEQ, ThresholdGT, ThresholdLT:
		return true
	}
	return false
}
