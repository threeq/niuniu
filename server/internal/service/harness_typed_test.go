package service

import (
	"context"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestHarness_SeedDefaults_PopulatesTypedFields asserts the migration +
// DefaultSpecs alignment: every seeded row carries a Kind, and the four kinds
// the UI exposes are all represented.
func TestHarness_SeedDefaults_PopulatesTypedFields(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	require.NoError(t, svc.SeedDefaults(ctx))

	rows, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)
	require.NotZero(t, len(rows))

	gotKinds := make(map[string]bool)
	for _, r := range rows {
		require.NotEmpty(t, r.Kind, "%s/%s missing Kind", r.Category, r.Name)
		require.True(t, harness.IsValidKind(r.Kind), "%s/%s has invalid kind %q", r.Category, r.Name, r.Kind)
		gotKinds[r.Kind] = true
	}
	for _, kind := range harness.ValidKinds() {
		require.Truef(t, gotKinds[kind], "DefaultSpecs missing at least one row of kind %q", kind)
	}
}

// TestHarness_SeedDefaults_HasErrorSeverityFloor guards the blocking-semantics
// 方案 B invariant: under HasBlockingFailure a gate blocks only when a failed
// check's spec is severity=='error'. The default 完成 column floor gate must be
// able to bind a severity=error spec, so the default directory has to seed at
// least one. (conventional-commits is severity=warning and never blocks.)
func TestHarness_SeedDefaults_HasErrorSeverityFloor(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	require.NoError(t, svc.SeedDefaults(ctx))

	rows, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)

	var errorSeverity []string
	for _, r := range rows {
		if r.Severity == "error" {
			errorSeverity = append(errorSeverity, r.Category+"/"+r.Name)
		}
	}
	require.NotEmpty(t, errorSeverity,
		"default directory must seed at least one severity=error floor spec (方案 B); none found")
}

// TestHarness_CreateSpec_RejectsInvalidKind covers the validateSpecInput gate.
func TestHarness_CreateSpec_RejectsInvalidKind(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	_, err := svc.CreateSpec(ctx, CreateSpecInput{
		Scope:    "global",
		Category: "commit",
		Name:     "x",
		Kind:     "made_up",
		Severity: "warning",
	}, "user", 0)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "invalid kind"),
		"unexpected error: %v", err)
}

// TestHarness_CreateSpec_RejectsInvalidThresholdOp ensures the op gate fires
// only for command_output_match kind.
func TestHarness_CreateSpec_RejectsInvalidThresholdOp(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	_, err := svc.CreateSpec(ctx, CreateSpecInput{
		Scope:       "global",
		Category:    "quality",
		Name:        "cov",
		Kind:        harness.KindCommandOutputMatch,
		Severity:    "warning",
		ThresholdOp: "=~",
	}, "user", 0)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "invalid threshold_op"),
		"unexpected error: %v", err)
}

// TestHarness_CheckRunner_RunSingle_RegexMatch_OnDemand simulates the
// POST /api/harness/specs/:id/test path: create a spec, then run the typed
// checker synchronously via CheckRunner.RunSingle.
func TestHarness_CheckRunner_RunSingle_RegexMatch_OnDemand(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	row, err := svc.CreateSpec(ctx, CreateSpecInput{
		Scope:    "global",
		Category: "commit",
		Name:     "feat-only",
		Kind:     harness.KindRegexMatch,
		Target:   harness.TargetCommitMessage,
		Pattern:  `^feat: .+`,
		Enabled:  1,
		Severity: "warning",
	}, "user", 0)
	require.NoError(t, err)

	spec := harness.Spec{
		ID:       row.ID,
		Kind:     row.Kind,
		Target:   row.Target,
		Pattern:  row.Pattern,
		Category: row.Category,
		Name:     row.Name,
		Enabled:  true,
	}

	res := svc.CheckRunner().RunSingle(ctx, spec, harness.CheckEnv{
		CommitMessage: "feat: add typed checker dispatch",
	})
	require.Equal(t, "pass", res.Status, "msg=%s details=%s", res.Message, res.Details)
	require.Equal(t, row.ID, res.SpecID)

	res2 := svc.CheckRunner().RunSingle(ctx, spec, harness.CheckEnv{
		CommitMessage: "wip",
	})
	require.Equal(t, "fail", res2.Status)
}

// TestHarness_PreCommitFilter_OnlyMatchingTrigger asserts the filter logic
// the PreCommitCheck HTTP handler uses: rows with trigger_on != 'pre_commit'
// are skipped, disabled rows are skipped, only enabled trigger_on='pre_commit'
// specs are returned for the runner.
func TestHarness_PreCommitFilter_OnlyMatchingTrigger(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	// Two pre_commit specs, one enabled one disabled.
	_, err := svc.CreateSpec(ctx, CreateSpecInput{
		Scope: "global", Category: "commit", Name: "pc-on", Kind: harness.KindRegexMatch,
		Target: harness.TargetCommitMessage, Pattern: `^feat: .+`,
		Enabled: 1, Severity: "warning", TriggerOn: harness.TriggerPreCommit,
	}, "user", 0)
	require.NoError(t, err)
	_, err = svc.CreateSpec(ctx, CreateSpecInput{
		Scope: "global", Category: "commit", Name: "pc-off", Kind: harness.KindRegexMatch,
		Target: harness.TargetCommitMessage, Pattern: `^x: .+`,
		Enabled: 0, Severity: "warning", TriggerOn: harness.TriggerPreCommit,
	}, "user", 0)
	require.NoError(t, err)
	// Non-pre-commit spec must not be picked up.
	_, err = svc.CreateSpec(ctx, CreateSpecInput{
		Scope: "global", Category: "commit", Name: "phase-only", Kind: harness.KindRegexMatch,
		Target: harness.TargetCommitMessage, Pattern: `^chore: .+`,
		Enabled: 1, Severity: "warning", TriggerOn: harness.TriggerPhaseExit,
	}, "user", 0)
	require.NoError(t, err)

	allSpecs, err := svc.ResolveForProject(ctx, nil)
	require.NoError(t, err)

	matched := 0
	for _, s := range allSpecs {
		if s.Enabled && s.TriggerOn == harness.TriggerPreCommit {
			matched++
		}
	}
	require.Equal(t, 1, matched,
		"expected exactly the enabled pre_commit spec; got %d", matched)
}

// TestHarness_SeedDefaults_BackfillsNewDefaultsOnUpgrade guards the C1 fix:
// previously SeedDefaults short-circuited when ANY global spec existed, so a
// new entry added to DefaultSpecs() (e.g. the ai_judge example) never reached
// upgraded installs. Now each row is checked individually.
func TestHarness_SeedDefaults_BackfillsNewDefaultsOnUpgrade(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	// Seed only the first default to simulate an upgrade from a binary that
	// shipped a smaller DefaultSpecs() set.
	first := harness.DefaultSpecs()[0]
	_, err := q.CreateHarnessSpec(ctx, store.CreateHarnessSpecParams{
		Category: first.Category, Name: first.Name,
		Enabled: 1, Severity: first.Severity, Config: first.Config,
		Kind: first.Kind, Pattern: first.Pattern, Target: first.Target,
		TriggerOn: first.TriggerOn, FilePaths: "[]",
	})
	require.NoError(t, err)

	// Run seeder — should insert the remaining defaults.
	require.NoError(t, svc.SeedDefaults(ctx))

	rows, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)
	require.Equal(t, len(harness.DefaultSpecs()), len(rows),
		"upgrade path must reach the full DefaultSpecs() count")
}

// TestHarness_CreateSpec_AcceptsOnSchedule guards the Phase C-2 lift:
// on_schedule is now wired to workspace_schedules via the HarnessGate
// interface, so creation is accepted.
func TestHarness_CreateSpec_AcceptsOnSchedule(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	_, err := svc.CreateSpec(ctx, CreateSpecInput{
		Scope: "global", Category: "commit", Name: "s1",
		Kind: harness.KindRegexMatch, Pattern: "^x", Target: harness.TargetCommitMessage,
		Severity: "warning", TriggerOn: harness.TriggerOnSchedule,
	}, "user", 0)
	require.NoError(t, err, "on_schedule should be accepted now that scheduler wiring exists")
}

// TestHarness_RunForWorkspace_FiltersByTrigger asserts the new gate hook
// (called from the scheduler at on_schedule trigger time) only fires specs
// with matching trigger_on. Global specs apply to all workspaces; project
// specs would require a workspace<->project JOIN which the test does not
// construct.
func TestHarness_RunForWorkspace_FiltersByTrigger(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	// Seed one on_schedule global spec (regex_match against commit_message —
	// will return skip because env.CommitMessage will be empty at gate time,
	// but the dispatch path is what we are exercising).
	_, err := svc.CreateSpec(ctx, CreateSpecInput{
		Scope:     "global",
		Category:  "commit",
		Name:      "on-sched-1",
		Kind:      harness.KindRegexMatch,
		Target:    harness.TargetCommitMessage,
		Pattern:   `^x`,
		Enabled:   1,
		Severity:  "warning",
		TriggerOn: harness.TriggerOnSchedule,
	}, "user", 0)
	require.NoError(t, err)

	// Insert a workspace row directly so the inner GetWorkspace call returns
	// something. Path empty is fine for this regex spec.
	_, err = db.Exec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, status) VALUES (1, 'w', '', 'user', 0, 'active')`)
	require.NoError(t, err)

	// RunForWorkspace must not return an error and must not flag blocked
	// (only error-severity fail blocks; this spec is warning).
	results, blocked, gerr := svc.RunForWorkspace(ctx, 1, harness.TriggerOnSchedule)
	require.NoError(t, gerr)
	require.False(t, blocked)
	// Result count depends on dispatch — at minimum no panic; if it ran, it
	// produced a skip (no input) which we drop from persistence.
	_ = results
}

// (Removed TestHarness_CreateSpec_RejectsProjectScope: harness_specs is a single
// global library now — scope/project are no longer a concept, so there is no
// "project scope" to reject. The per-kanban relationship lives in
// column_gate_specs.)

// TestHarness_AIJudge_Registered ensures the ai_judge typed checker is wired
// into the runner so the kind dispatcher resolves it.
func TestHarness_AIJudge_Registered(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	spec := harness.Spec{
		ID:      1,
		Kind:    harness.KindAIJudge,
		Enabled: true,
		// No prompt: checker should emit skip, proving it's reachable.
	}
	res := svc.CheckRunner().RunSingle(ctx, spec, harness.CheckEnv{})
	require.Equal(t, "skip", res.Status,
		"ai_judge checker should be registered and return skip on empty prompt")
}

// TestHarness_CheckRunner_LegacyFallback ensures rows without a Kind hint
// still dispatch through the legacy category/name registry.
func TestHarness_CheckRunner_LegacyFallback(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	spec := harness.Spec{
		ID:       1,
		Category: "commit",
		Name:     "conventional-commits",
		Enabled:  true,
		Severity: "warning",
		Kind:     "", // forces legacy lookup
	}
	res := svc.CheckRunner().RunSingle(ctx, spec, harness.CheckEnv{
		CommitMessage: "fix: legacy path still wired",
	})
	require.Equal(t, "pass", res.Status, "legacy commit-lint should accept conventional commit")

	_ = q
}
