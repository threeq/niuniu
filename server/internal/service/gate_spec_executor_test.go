package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// newGateTestExec builds a real GateSpecExecutor over an in-memory store, with
// the same checker registrations as NewHarnessService, so these tests exercise
// ExecuteSpec end-to-end (store row -> conversion -> dispatch -> verdict).
func newGateTestExec(t *testing.T) (GateSpecExecutor, *store.Queries) {
	t.Helper()
	db := setupTestDB(t)
	q := store.New(db)

	cr := harness.NewCheckRunner()
	cr.RegisterTyped(checkers.NewRegexMatch())
	cr.RegisterTyped(checkers.NewCmdExit())
	cr.RegisterTyped(checkers.NewCmdOutputMatch())
	cr.RegisterTyped(checkers.NewFileExistsV2())

	return NewCheckRunnerExec(cr, q), q
}

// seedSpec inserts a spec row shaped the way the settings UI writes one: typed
// columns populated, Config left as "{}".
func seedSpec(t *testing.T, q *store.Queries, category, name, kind, command string, severity string) int64 {
	t.Helper()
	row, err := q.CreateHarnessSpec(context.Background(), store.CreateHarnessSpecParams{
		Category: category, Name: name, Enabled: 1, Severity: severity,
		Config: "{}", Kind: kind, Command: command, TimeoutSec: 30,
		FilePaths: "[]", TriggerOn: harness.TriggerPhaseExit,
	})
	require.NoError(t, err)
	return row.ID
}

// A UI-configured spec populates the typed columns and leaves Config as "{}".
// ExecuteSpec must carry Kind through so the right typed checker runs. A prior
// hand-rolled conversion dropped Kind, and every gate built on such a spec
// silently passed.
func TestExecuteSpecHonorsTypedKind(t *testing.T) {
	// Separate stores per case because (category, name) is UNIQUE.
	ctx := context.Background()

	execFail, qFail := newGateTestExec(t)
	failID := seedSpec(t, qFail, "workflow", "command-exit-code", harness.KindCommandExitCode, "exit 7", "error")
	passed, out, err := execFail.ExecuteSpec(ctx, 0, failID, t.TempDir())
	require.NoError(t, err)
	require.False(t, passed, "a spec whose command exits 7 must block")
	// Assert the verdict came from actually running the command, not from a
	// mis-dispatch that merely happened to look like a failure.
	require.Contains(t, out, "exited with code 7",
		"verdict must come from running the typed command")

	execOK, qOK := newGateTestExec(t)
	okID := seedSpec(t, qOK, "workflow", "command-exit-code", harness.KindCommandExitCode, "exit 0", "error")
	passed, _, err = execOK.ExecuteSpec(ctx, 0, okID, t.TempDir())
	require.NoError(t, err)
	require.True(t, passed, "a spec whose command exits 0 must pass")
}

// quality/build-test-pass is the designated severity=error floor spec. With Kind
// dropped it produced no result, which ExecuteSpec reported as passed — so the
// floor gate it anchors could never hold the line.
func TestExecuteSpecFloorSpecCanActuallyBlock(t *testing.T) {
	exec, q := newGateTestExec(t)
	id := seedSpec(t, q, "quality", "build-test-pass", harness.KindCommandExitCode, "exit 1", "error")
	passed, _, err := exec.ExecuteSpec(context.Background(), 0, id, t.TempDir())
	require.NoError(t, err)
	require.False(t, passed, "a configured floor spec must block on failure")
}

// The shipped defaults are all disabled with an empty command and are documented
// as never false-blocking. An unconfigured spec yields "skip", which must not be
// treated as a failure.
func TestExecuteSpecUnconfiguredSpecDoesNotBlock(t *testing.T) {
	exec, q := newGateTestExec(t)
	id := seedSpec(t, q, "quality", "build-test-pass", harness.KindCommandExitCode, "", "error")
	passed, _, err := exec.ExecuteSpec(context.Background(), 0, id, t.TempDir())
	require.NoError(t, err)
	require.True(t, passed, "an unconfigured spec must not block (skip != fail)")
}
