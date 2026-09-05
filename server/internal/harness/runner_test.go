package harness_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChecker is a TypedChecker that always returns a preset status, registered
// under a caller-chosen Kind.
type mockChecker struct {
	kind   string
	status string
}

func (m *mockChecker) Kind() string { return m.kind }
func (m *mockChecker) Run(_ context.Context, _ harness.Spec, _ harness.CheckEnv) harness.CheckResult {
	return harness.CheckResult{Status: m.status, Message: "mock"}
}

func TestCheckRunner_AllPass(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.RegisterTyped(&mockChecker{kind: "kind_a", status: "pass"})
	runner.RegisterTyped(&mockChecker{kind: "kind_b", status: "pass"})

	specs := []harness.Spec{
		{ID: 1, Kind: "kind_a", Category: "commit", Name: "lint", Enabled: true, Severity: "error"},
		{ID: 2, Kind: "kind_b", Category: "branch", Name: "name", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 2)
	assert.Equal(t, "pass", results[0].Status)
	assert.Equal(t, "pass", results[1].Status)
	assert.False(t, runner.HasBlockingFailure(specs, results))
}

func TestCheckRunner_SkipsDisabled(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.RegisterTyped(&mockChecker{kind: "kind_a", status: "pass"})
	runner.RegisterTyped(&mockChecker{kind: "kind_b", status: "fail"})

	specs := []harness.Spec{
		{ID: 1, Kind: "kind_a", Category: "commit", Name: "lint", Enabled: true, Severity: "error"},
		{ID: 2, Kind: "kind_b", Category: "branch", Name: "name", Enabled: false, Severity: "error"}, // disabled
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	// Only the enabled spec should produce a result
	require.Len(t, results, 1)
	assert.Equal(t, int64(1), results[0].SpecID)
	assert.Equal(t, "pass", results[0].Status)
}

// A spec whose Kind has no registered checker must surface as an "error", not be
// dropped. Silently omitting it made a gate built on that spec pass vacuously —
// which is exactly how the floor gate stayed broken while looking green.
func TestCheckRunner_MissingChecker(t *testing.T) {
	runner := harness.NewCheckRunner()

	specs := []harness.Spec{
		{ID: 1, Kind: "no_such_kind", Category: "coverage", Name: "threshold", Enabled: true, Severity: "error"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 1)
	assert.Equal(t, "error", results[0].Status)
	assert.Equal(t, int64(1), results[0].SpecID)
	assert.Contains(t, results[0].Message, "no_such_kind")
	// An unexecutable error-severity spec is not evidence the standard was met.
	assert.True(t, runner.HasBlockingFailure(specs, results))
}

// The same unexecutable spec at warning severity is advisory, not blocking.
func TestCheckRunner_MissingChecker_WarnNotBlocking(t *testing.T) {
	runner := harness.NewCheckRunner()

	specs := []harness.Spec{
		{ID: 1, Kind: "no_such_kind", Category: "coverage", Name: "threshold", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 1)
	assert.Equal(t, "error", results[0].Status)
	assert.False(t, runner.HasBlockingFailure(specs, results))
}

func TestCheckRunner_HasBlockingFailure(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.RegisterTyped(&mockChecker{kind: "kind_a", status: "fail"})
	runner.RegisterTyped(&mockChecker{kind: "kind_b", status: "fail"})

	specs := []harness.Spec{
		{ID: 1, Kind: "kind_a", Category: "commit", Name: "lint", Enabled: true, Severity: "error"},
		{ID: 2, Kind: "kind_b", Category: "branch", Name: "name", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 2)
	// spec 1 is error severity + fail → blocking
	assert.True(t, runner.HasBlockingFailure(specs, results))
}

func TestCheckRunner_HasBlockingFailure_WarnOnly(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.RegisterTyped(&mockChecker{kind: "kind_b", status: "fail"})

	specs := []harness.Spec{
		{ID: 2, Kind: "kind_b", Category: "branch", Name: "name", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	// warn severity failures are not blocking
	assert.False(t, runner.HasBlockingFailure(specs, results))
}

// A "skip" (spec present but not configured enough to judge) never blocks, even at
// error severity — the shipped defaults ship unconfigured and must not false-block.
func TestCheckRunner_SkipNotBlocking(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.RegisterTyped(&mockChecker{kind: "kind_a", status: "skip"})

	specs := []harness.Spec{
		{ID: 1, Kind: "kind_a", Category: "quality", Name: "build", Enabled: true, Severity: "error"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 1)
	assert.Equal(t, "skip", results[0].Status)
	assert.False(t, runner.HasBlockingFailure(specs, results))
}
