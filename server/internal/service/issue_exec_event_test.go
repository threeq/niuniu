package service_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecEventService_RecordsListsAndSumsCost(t *testing.T) {
	e := setupEpicTest(t)
	_, backlogID, _ := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "任务", "")

	svc := service.NewExecEventService(e.db)
	svc.Record(e.ctx, service.ExecEvent{IssueID: issueID, Kind: "advance", Summary: "移动到「实现」"})
	svc.Record(e.ctx, service.ExecEvent{IssueID: issueID, WorkspaceID: 7, Kind: "cost", Summary: "agent turn", CostUSD: 0.12})
	svc.Record(e.ctx, service.ExecEvent{IssueID: issueID, Kind: "gate", Summary: "底线闸: 通过"})

	events, err := svc.List(e.ctx, issueID)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, "advance", events[0].Kind, "oldest first")
	assert.Equal(t, "gate", events[2].Kind)

	total, err := svc.TotalCost(e.ctx, issueID)
	require.NoError(t, err)
	assert.InDelta(t, 0.12, total, 1e-9)
}

func TestExecEventService_RecordIsNoOpOnInvalidInput(t *testing.T) {
	e := setupEpicTest(t)
	svc := service.NewExecEventService(e.db)
	// Missing issue id / kind -> silently ignored, never panics.
	svc.Record(e.ctx, service.ExecEvent{Kind: "advance", Summary: "no issue"})
	svc.Record(e.ctx, service.ExecEvent{IssueID: 1, Summary: "no kind"})
	events, err := svc.List(e.ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, events)
}
