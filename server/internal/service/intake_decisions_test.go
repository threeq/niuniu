package service

import (
	"context"
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
