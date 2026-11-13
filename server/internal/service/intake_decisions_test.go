package service

import (
	"context"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// The calibration queue narrows server-side by source, so a mission-control
// drill-down fetches only that source's pending decisions.
func TestListDecisions_FiltersBySource(t *testing.T) {
	f := newIMBotFixture(t)
	q, projectID := f.q, f.projectID
	ctx := context.Background()

	seed := func(sourceKind, eventExtID string) {
		t.Helper()
		event, err := q.CreateIntakeEvent(ctx, store.CreateIntakeEventParams{
			ProjectID: projectID, SourceKind: sourceKind, SourceRef: "ref",
			EventExtID: eventExtID, EventKind: "message", ThreadExtID: "t-1",
			PayloadJson: `{}`,
		})
		require.NoError(t, err)
		_, err = q.CreateIntakeDecision(ctx, store.CreateIntakeDecisionParams{
			EventID: event.ID, ProjectID: projectID, ModeAtDecision: "observe",
			Strength: "inferred", Decision: "accept", Executed: 0,
			ReasonCode: "observed", MatchedRuleIdsJson: `[]`, WouldDoJson: `{}`,
		})
		require.NoError(t, err)
	}
	seed("github", "gh-1")
	seed("im_bot", "im-1")

	svc := NewIntakeService(f.db, q)
	all, err := svc.ListDecisions(ctx, projectID, nil, "", "", 100)
	require.NoError(t, err)
	require.Len(t, all, 2)

	gh, err := svc.ListDecisions(ctx, projectID, nil, "", "github", 100)
	require.NoError(t, err)
	require.Len(t, gh, 1)
	require.Equal(t, "github", gh[0].SourceKind)

	none, err := svc.ListDecisions(ctx, projectID, nil, "", "email", 100)
	require.NoError(t, err)
	require.Empty(t, none)
}

// CountPendingDecisions is the nav-badge source: it must count only still
// actionable suggestions (accept/resume, not executed) and respect the
// caller's owner scope.
func TestCountPendingDecisions_ScopesByOwner(t *testing.T) {
	f := newIMBotFixture(t)
	q := f.q
	ctx := context.Background()

	seq := 0
	seed := func(projectID int64, decision string, executed int64) {
		t.Helper()
		seq++
		event, err := q.CreateIntakeEvent(ctx, store.CreateIntakeEventParams{
			ProjectID: projectID, SourceKind: "im_bot", SourceRef: "ref",
			EventExtID: "evt-" + strconv.Itoa(seq), EventKind: "message",
			PayloadJson: `{}`,
		})
		require.NoError(t, err)
		_, err = q.CreateIntakeDecision(ctx, store.CreateIntakeDecisionParams{
			EventID: event.ID, ProjectID: projectID, ModeAtDecision: "observe",
			Strength: "inferred", Decision: decision, Executed: executed,
			ReasonCode: "observed", MatchedRuleIdsJson: `[]`, WouldDoJson: `{}`,
		})
		require.NoError(t, err)
	}

	// Fixture project (owner user:1): two pending, one ignored, one executed.
	seed(f.projectID, "accept", 0)
	seed(f.projectID, "resume", 0)
	seed(f.projectID, "ignore", 0)
	seed(f.projectID, "accept", 1)

	// Another owner's project with one pending suggestion.
	other, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "other", OwnerType: "user", OwnerID: 2})
	require.NoError(t, err)
	seed(other.ID, "accept", 0)

	svc := NewIntakeService(f.db, q)

	all, err := svc.CountPendingDecisions(ctx, 0, nil)
	require.NoError(t, err)
	require.Equal(t, int64(3), all, "personal edition counts every project")

	mine, err := svc.CountPendingDecisions(ctx, 1, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), mine, "only user 1's projects")

	theirs, err := svc.CountPendingDecisions(ctx, 2, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), theirs, "only user 2's projects")
}
