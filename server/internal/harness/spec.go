package harness

// Spec is a harness rule definition stored in harness_specs.
// Typed fields (Kind, Target, Pattern, Command, etc.) are the canonical config
// since 2026-05-20; the legacy Config JSON string is retained for backward
// compatibility with rows created before the typed-column migration.
type Spec struct {
	ID               int64   `json:"id"`
	Scope            string  `json:"scope"`
	ProjectID        *int64  `json:"project_id,omitempty"`
	Category         string  `json:"category"`
	Name             string  `json:"name"`
	Enabled          bool    `json:"enabled"`
	Severity         string  `json:"severity"`
	Config           string  `json:"config"`
	Kind             string  `json:"kind"`
	Target           string  `json:"target"`
	Pattern          string  `json:"pattern"`
	PatternFlags     string  `json:"pattern_flags"`
	Command          string  `json:"command"`
	TimeoutSec       int     `json:"timeout_sec"`
	ExpectedExitCode int     `json:"expected_exit_code"`
	ExtractRegex     string  `json:"extract_regex"`
	ThresholdValue   float64 `json:"threshold_value"`
	ThresholdOp      string  `json:"threshold_op"`
	FilePaths        string  `json:"file_paths"`
	TriggerOn        string  `json:"trigger_on"`
	JudgePrompt      string  `json:"judge_prompt"`
	JudgeModel       string  `json:"judge_model"`
}

type CheckResult struct {
	SpecID     int64   `json:"spec_id"`
	Status     string  `json:"status"`
	Message    string  `json:"message"`
	Details    string  `json:"details"`
	DurationMs int64   `json:"duration_ms"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

// GateResult is the cross-package view of a single check outcome used by the
// scheduler -> harness gate hook. Kept slim on purpose so the scheduler
// package can depend on this type without pulling the full CheckResult shape
// (which carries duration / cost / details).
type GateResult struct {
	SpecID  int64
	Status  string
	Message string
}

func SpecKey(s Spec) string { return s.Category + "/" + s.Name }
