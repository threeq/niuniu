package harness_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChecker is a simple checker that always returns a preset result.
type mockChecker struct {
	name   string
	status string
}

func (m *mockChecker) Name() string { return m.name }
func (m *mockChecker) Check(_ context.Context, _ harness.CheckOpts) harness.CheckResult {
	return harness.CheckResult{Status: m.status, Message: "mock"}
}

func TestCheckRunner_AllPass(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.Register("commit/lint", &mockChecker{name: "commit/lint", status: "pass"})
	runner.Register("branch/name", &mockChecker{name: "branch/name", status: "pass"})

	specs := []harness.Spec{
		{ID: 1, Category: "commit", Name: "lint", Enabled: true, Severity: "error"},
		{ID: 2, Category: "branch", Name: "name", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 2)
	assert.Equal(t, "pass", results[0].Status)
	assert.Equal(t, "pass", results[1].Status)
	assert.False(t, runner.HasBlockingFailure(specs, results))
}

func TestCheckRunner_SkipsDisabled(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.Register("commit/lint", &mockChecker{name: "commit/lint", status: "pass"})
	runner.Register("branch/name", &mockChecker{name: "branch/name", status: "fail"})

	specs := []harness.Spec{
		{ID: 1, Category: "commit", Name: "lint", Enabled: true, Severity: "error"},
		{ID: 2, Category: "branch", Name: "name", Enabled: false, Severity: "error"}, // disabled
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	// Only the enabled spec should produce a result
	require.Len(t, results, 1)
	assert.Equal(t, int64(1), results[0].SpecID)
	assert.Equal(t, "pass", results[0].Status)
}

func TestCheckRunner_MissingChecker(t *testing.T) {
	runner := harness.NewCheckRunner()
	// No checker registered for coverage/threshold

	specs := []harness.Spec{
		{ID: 1, Category: "coverage", Name: "threshold", Enabled: true, Severity: "error"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	// Missing checker should be skipped silently
	assert.Empty(t, results)
}

func TestCheckRunner_HasBlockingFailure(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.Register("commit/lint", &mockChecker{name: "commit/lint", status: "fail"})
	runner.Register("branch/name", &mockChecker{name: "branch/name", status: "fail"})

	specs := []harness.Spec{
		{ID: 1, Category: "commit", Name: "lint", Enabled: true, Severity: "error"},
		{ID: 2, Category: "branch", Name: "name", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	require.Len(t, results, 2)
	// spec 1 is error severity + fail → blocking
	assert.True(t, runner.HasBlockingFailure(specs, results))
}

func TestCheckRunner_HasBlockingFailure_WarnOnly(t *testing.T) {
	runner := harness.NewCheckRunner()
	runner.Register("branch/name", &mockChecker{name: "branch/name", status: "fail"})

	specs := []harness.Spec{
		{ID: 2, Category: "branch", Name: "name", Enabled: true, Severity: "warning"},
	}

	results := runner.RunAll(context.Background(), specs, harness.CheckOpts{})

	// warn severity failures are not blocking
	assert.False(t, runner.HasBlockingFailure(specs, results))
}
