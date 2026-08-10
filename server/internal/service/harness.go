package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// HarnessService combines spec CRUD, config resolution, and prompt generation.
// NOTE: Legacy harness template CRUD (harnesses, harness_phases, harness_phase_agents,
// harness_phase_gates) was removed in Phase 7 (drop_legacy_phase7_v1).
type HarnessService struct {
	q           *store.Queries
	checkRunner *harness.CheckRunner
	authz       *Authz
}

// NewHarnessService creates the service and registers built-in checkers.
//
// Typed checkers (4) are the canonical implementations dispatched by Kind.
// Legacy category/name checkers (8) remain registered as a fallback so rows
// that have not been migrated to a known Kind still execute.
func NewHarnessService(q *store.Queries, authz *Authz) *HarnessService {
	cr := harness.NewCheckRunner()

	cr.RegisterTyped(checkers.NewRegexMatch())
	cr.RegisterTyped(checkers.NewCmdExit())
	cr.RegisterTyped(checkers.NewCmdOutputMatch())
	cr.RegisterTyped(checkers.NewFileExistsV2())
	cr.RegisterTyped(checkers.NewAIJudge())

	cr.Register("commit/conventional-commits", &checkers.CommitLint{})
	cr.Register("commit/branch-name", &checkers.BranchName{})
	cr.Register("quality/linter", &checkers.Linter{})
	cr.Register("quality/test-coverage", &checkers.TestCoverage{})
	cr.Register("workflow/output-pattern", &checkers.OutputPattern{})
	cr.Register("workflow/file-exists", &checkers.FileExists{})
	cr.Register("workflow/command-exit-code", &checkers.CommandExitCode{})
	cr.Register("workflow/command-output", &checkers.CommandOutput{})
	return &HarnessService{q: q, checkRunner: cr, authz: authz}
}

// CheckRunner returns the underlying runner for pipeline use.
func (s *HarnessService) CheckRunner() *harness.CheckRunner {
	return s.checkRunner
}

// SeedDefaults idempotently inserts default global specs.
//
// Per-row check (not whole-table) so adding a new entry to DefaultSpecs() in
// a later release reaches existing installs on upgrade. The UNIQUE constraint
// `(scope, project_id, category, name)` already guards duplicates; we look up
// by (category, name) on the global+sentinel set and skip when present so the
// error surface is a clear log line rather than a constraint violation.
func (s *HarnessService) SeedDefaults(ctx context.Context) error {
	existing, err := s.q.ListGlobalHarnessSpecs(ctx)
	if err != nil {
		return fmt.Errorf("list global specs: %w", err)
	}
	have := make(map[string]bool, len(existing))
	for _, r := range existing {
		have[r.Category+"/"+r.Name] = true
	}

	for _, spec := range harness.DefaultSpecs() {
		key := spec.Category + "/" + spec.Name
		if have[key] {
			continue
		}
		enabled := int64(0)
		if spec.Enabled {
			enabled = 1
		}
		// harness_specs is a single global library — no owner / scope / project.
		_, err := s.q.CreateHarnessSpec(ctx, store.CreateHarnessSpecParams{
			Category:         spec.Category,
			Name:             spec.Name,
			Enabled:          enabled,
			Severity:         spec.Severity,
			Config:           spec.Config,
			Kind:             spec.Kind,
			Target:           spec.Target,
			Pattern:          spec.Pattern,
			PatternFlags:     spec.PatternFlags,
			Command:          spec.Command,
			TimeoutSec:       int64(spec.TimeoutSec),
			ExpectedExitCode: int64(spec.ExpectedExitCode),
			ExtractRegex:     spec.ExtractRegex,
			ThresholdValue:   spec.ThresholdValue,
			ThresholdOp:      spec.ThresholdOp,
			FilePaths:        spec.FilePaths,
			TriggerOn:        spec.TriggerOn,
			JudgePrompt:      spec.JudgePrompt,
			JudgeModel:       spec.JudgeModel,
		})
		if err != nil {
			return fmt.Errorf("create spec %s/%s: %w", spec.Category, spec.Name, err)
		}
	}
	return nil
}

// ---- Input types ----

type CreateSpecInput struct {
	Scope            string  `json:"scope"`
	ProjectID        *int64  `json:"project_id,omitempty"`
	Category         string  `json:"category"`
	Name             string  `json:"name"`
	Enabled          int64   `json:"enabled"`
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

type UpdateSpecInput struct {
	Enabled          int64   `json:"enabled"`
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

// ---- Spec CRUD ----

func (s *HarnessService) ListGlobalSpecs(ctx context.Context) ([]store.HarnessSpec, error) {
	return s.q.ListGlobalHarnessSpecs(ctx)
}

// ListGlobalSpecsForUser returns the global engineering-standards library.
//
// harness specs are NOT owner-scoped: they form one deployment-wide global
// library (the runtime gate evaluator ListGlobalHarnessSpecs is likewise
// owner-blind). Every caller sees the same set; per-kanban application is
// expressed via column_gate_specs, not via spec ownership. The userID argument
// is retained for signature stability but no longer narrows the result.
func (s *HarnessService) ListGlobalSpecsForUser(ctx context.Context, userID int64) ([]store.HarnessSpec, error) {
	_ = userID
	return s.q.ListGlobalHarnessSpecs(ctx)
}

func (s *HarnessService) CreateSpec(ctx context.Context, input CreateSpecInput, ownerType string, ownerID int64) (store.HarnessSpec, error) {
	// harness_specs is a single GLOBAL library — owner/scope/project are ignored.
	_, _ = ownerType, ownerID
	if err := validateSpec(input.Kind, input.Pattern, input.ExtractRegex, input.FilePaths, input.ThresholdOp); err != nil {
		return store.HarnessSpec{}, err
	}
	kind := input.Kind
	if kind == "" {
		kind = harness.KindRegexMatch
	}
	triggerOn := input.TriggerOn
	if triggerOn == "" {
		triggerOn = harness.TriggerPhaseExit
	}
	timeoutSec := input.TimeoutSec
	if timeoutSec == 0 {
		timeoutSec = 120
	}
	filePaths := input.FilePaths
	if filePaths == "" {
		filePaths = "[]"
	}
	cfg := input.Config
	if cfg == "" {
		cfg = "{}"
	}
	return s.q.CreateHarnessSpec(ctx, store.CreateHarnessSpecParams{
		Category:         input.Category,
		Name:             input.Name,
		Enabled:          input.Enabled,
		Severity:         input.Severity,
		Config:           cfg,
		Kind:             kind,
		Target:           input.Target,
		Pattern:          input.Pattern,
		PatternFlags:     input.PatternFlags,
		Command:          input.Command,
		TimeoutSec:       int64(timeoutSec),
		ExpectedExitCode: int64(input.ExpectedExitCode),
		ExtractRegex:     input.ExtractRegex,
		ThresholdValue:   input.ThresholdValue,
		ThresholdOp:      input.ThresholdOp,
		FilePaths:        filePaths,
		TriggerOn:        triggerOn,
		JudgePrompt:      input.JudgePrompt,
		JudgeModel:       input.JudgeModel,
	})
}

func (s *HarnessService) UpdateSpec(ctx context.Context, id int64, input UpdateSpecInput) error {
	if err := validateSpec(input.Kind, input.Pattern, input.ExtractRegex, input.FilePaths, input.ThresholdOp); err != nil {
		return err
	}
	// Update can't change scope, but can change trigger_on — only the
	// trigger-only invariants apply (on_schedule is reserved).
	if err := validateTriggerScope("", input.TriggerOn); err != nil {
		return err
	}
	kind := input.Kind
	if kind == "" {
		kind = harness.KindRegexMatch
	}
	triggerOn := input.TriggerOn
	if triggerOn == "" {
		triggerOn = harness.TriggerPhaseExit
	}
	timeoutSec := input.TimeoutSec
	if timeoutSec == 0 {
		timeoutSec = 120
	}
	filePaths := input.FilePaths
	if filePaths == "" {
		filePaths = "[]"
	}
	cfg := input.Config
	if cfg == "" {
		cfg = "{}"
	}
	return s.q.UpdateHarnessSpec(ctx, store.UpdateHarnessSpecParams{
		Enabled:          input.Enabled,
		Severity:         input.Severity,
		Config:           cfg,
		Kind:             kind,
		Target:           input.Target,
		Pattern:          input.Pattern,
		PatternFlags:     input.PatternFlags,
		Command:          input.Command,
		TimeoutSec:       int64(timeoutSec),
		ExpectedExitCode: int64(input.ExpectedExitCode),
		ExtractRegex:     input.ExtractRegex,
		ThresholdValue:   input.ThresholdValue,
		ThresholdOp:      input.ThresholdOp,
		FilePaths:        filePaths,
		TriggerOn:        triggerOn,
		JudgePrompt:      input.JudgePrompt,
		JudgeModel:       input.JudgeModel,
		ID:               id,
	})
}

// CurrentSeverity returns a spec's current severity ('' if the spec is missing).
// Used to detect a floor severity downgrade before applying an update (spec §18).
func (s *HarnessService) CurrentSeverity(ctx context.Context, id int64) (string, error) {
	spec, err := s.q.GetHarnessSpec(ctx, id)
	if err != nil {
		return "", err
	}
	return spec.Severity, nil
}

// IsSpecBoundAsFloor reports whether the spec is bound as a project floor
// (applicability='always') on any column, so its severity cannot be silently
// downgraded by a non-admin (spec §18 floor-config write permission).
func (s *HarnessService) IsSpecBoundAsFloor(ctx context.Context, id int64) (bool, error) {
	n, err := s.q.CountSpecFloorBindings(ctx, id)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Validation sentinels — public so the API layer can use errors.Is rather than
// string-prefix matching.
var (
	ErrInvalidKind             = errors.New("invalid kind")
	ErrInvalidThresholdOp      = errors.New("invalid threshold_op")
	ErrInvalidPattern          = errors.New("invalid pattern")
	ErrInvalidExtractRegex     = errors.New("invalid extract_regex")
	ErrInvalidFilePaths        = errors.New("invalid file_paths")
	ErrUnsupportedScope        = errors.New("unsupported scope")
	ErrUnsupportedTriggerScope = errors.New("unsupported trigger + scope combination")
)

func validateSpecScope(scope string, projectID *int64) error {
	if (scope == "" || scope == "global") && projectID == nil {
		return nil
	}
	return fmt.Errorf("%w: harness specs are global-only", ErrUnsupportedScope)
}

// validateTriggerScope is retained as the validation surface in case future
// runtime-restricted combinations need to be added. After Phase C-2 it is a
// no-op: every documented trigger (phase_exit / on_demand / pre_commit /
// on_schedule) is wired end-to-end.
//
// Pass scope="" from Update where the scope cannot change but the trigger can.
func validateTriggerScope(_, _ string) error {
	return nil
}

// RunForWorkspace evaluates every enabled global spec with the given trigger
// against the workspace. Results are persisted to harness_checks with
// phase_name = triggerOn. Returns blocked=true iff any error-severity spec
// produced status='fail'.
//
// Implements the scheduler.HarnessGate interface — registered via
// scheduler.SetHarnessGate at server startup. Return type lives in the
// harness package so both this and scheduler can refer to the same struct.
func (s *HarnessService) RunForWorkspace(ctx context.Context, workspaceID int64, triggerOn string) ([]harness.GateResult, bool, error) {
	allSpecs, err := s.ResolveForProject(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("resolve specs: %w", err)
	}

	filtered := make([]harness.Spec, 0, len(allSpecs))
	for _, sp := range allSpecs {
		if sp.Enabled && sp.TriggerOn == triggerOn {
			filtered = append(filtered, sp)
		}
	}
	if len(filtered) == 0 {
		return nil, false, nil
	}

	// Resolve workspace.path so command-* specs run in the user's checkout.
	wsRow, _ := s.q.GetWorkspace(ctx, workspaceID)

	results := s.checkRunner.RunAll(ctx, filtered, harness.CheckOpts{
		Phase:         triggerOn,
		WorkspacePath: wsRow.Path,
	})

	// Persist results so on_schedule blocks are visible in harness_checks.
	for _, r := range results {
		if r.Status == "skip" {
			continue
		}
		_, perr := s.q.CreateHarnessCheck(ctx, store.CreateHarnessCheckParams{
			WorkspaceID: workspaceID,
			RunID:       sql.NullInt64{},
			SpecID:      r.SpecID,
			PhaseName:   triggerOn,
			Status:      r.Status,
			Message:     r.Message,
			Details:     r.Details,
			DurationMs:  r.DurationMs,
			CostUsd:     r.CostUSD,
		})
		if perr != nil {
			// Persistence is best-effort; surface via log, not error.
			slog.Warn("harness: persist gate check failed",
				"workspaceID", workspaceID, "spec", r.SpecID, "trigger", triggerOn, "err", perr)
		}
	}

	out := make([]harness.GateResult, len(results))
	for i, r := range results {
		out[i] = harness.GateResult{SpecID: r.SpecID, Status: r.Status, Message: r.Message}
	}

	blocked := s.checkRunner.HasBlockingFailure(filtered, results)
	return out, blocked, nil
}

// validateSpec enforces typed-config invariants shared by Create and Update.
// Validates: kind enum, threshold op enum, pattern + extract_regex regex
// compilability, file_paths JSON array shape. Empty values are tolerated to
// support partially-configured specs (the checker will return skip at runtime).
func validateSpec(kind, pattern, extractRegex, filePaths, thresholdOp string) error {
	if kind != "" && !harness.IsValidKind(kind) {
		return fmt.Errorf("%w %q (valid: %s)", ErrInvalidKind, kind, strings.Join(harness.ValidKinds(), ","))
	}
	if kind == harness.KindCommandOutputMatch && thresholdOp != "" && !harness.IsValidThresholdOp(thresholdOp) {
		return fmt.Errorf("%w %q", ErrInvalidThresholdOp, thresholdOp)
	}
	if kind == harness.KindRegexMatch && pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPattern, err)
		}
	}
	if kind == harness.KindCommandOutputMatch && extractRegex != "" {
		if _, err := regexp.Compile(extractRegex); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidExtractRegex, err)
		}
	}
	if kind == harness.KindFileExists && filePaths != "" && filePaths != "[]" {
		var paths []string
		if err := json.Unmarshal([]byte(filePaths), &paths); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFilePaths, err)
		}
	}
	return nil
}

func (s *HarnessService) DeleteSpec(ctx context.Context, id int64) error {
	return s.q.DeleteHarnessSpec(ctx, id)
}

// ResolveForProject returns global specs. The projectID argument is ignored;
// project-level engineering standards were removed so projects only consume
// globally managed specs.
func (s *HarnessService) ResolveForProject(ctx context.Context, projectID *int64) ([]harness.Spec, error) {
	_ = projectID
	globalRows, err := s.q.ListGlobalHarnessSpecs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list global specs: %w", err)
	}
	return storeSpecsToHarness(globalRows), nil
}

func storeSpecsToHarness(rows []store.HarnessSpec) []harness.Spec {
	specs := make([]harness.Spec, len(rows))
	for i, r := range rows {
		specs[i] = harness.Spec{
			ID:               r.ID,
			// Scope/ProjectID are legacy domain fields; harness_specs is global.
			Scope:            "global",
			Category:         r.Category,
			Name:             r.Name,
			Enabled:          r.Enabled != 0,
			Severity:         r.Severity,
			Config:           r.Config,
			Kind:             r.Kind,
			Target:           r.Target,
			Pattern:          r.Pattern,
			PatternFlags:     r.PatternFlags,
			Command:          r.Command,
			TimeoutSec:       int(r.TimeoutSec),
			ExpectedExitCode: int(r.ExpectedExitCode),
			ExtractRegex:     r.ExtractRegex,
			ThresholdValue:   r.ThresholdValue,
			ThresholdOp:      r.ThresholdOp,
			FilePaths:        r.FilePaths,
			TriggerOn:        r.TriggerOn,
			JudgePrompt:      r.JudgePrompt,
			JudgeModel:       r.JudgeModel,
		}
	}
	return specs
}

// ---- Prompt generation (legacy, kept for existing callers) ----

// GeneratePrompt builds a prompt using the old phase-based HarnessPromptConfig.
// The harness template is no longer stored in DB; this method is a no-op
// preserved for build compatibility. It returns an empty string.
//
// Deprecated: Use buildPromptForTemplate in the harness package instead.
func (s *HarnessService) GeneratePrompt(_ context.Context, _ int64, goal string) (string, error) {
	// Legacy harness templates removed in Phase 7. Return minimal prompt.
	return strings.TrimSpace("Goal: " + goal), nil
}
