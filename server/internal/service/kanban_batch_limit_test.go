package service_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchCreateIssues_OverLimit(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	c, err := e.q.GetColumn(e.ctx, col)
	require.NoError(t, err)

	e.kanban.SetMaxBatchIssues(2)

	over := []service.BatchCreateIssuesTask{{Title: "a"}, {Title: "b"}, {Title: "c"}}
	_, err = e.kanban.BatchCreateIssues(e.ctx, c.ProjectID, over, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "最多")

	ok := []service.BatchCreateIssuesTask{{Title: "a"}, {Title: "b"}}
	res, err := e.kanban.BatchCreateIssues(e.ctx, c.ProjectID, ok, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Created)
}

func TestBatchCreateIssues_UnlimitedByDefault(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	c, err := e.q.GetColumn(e.ctx, col)
	require.NoError(t, err)

	// No SetMaxBatchIssues call -> unlimited.
	tasks := make([]service.BatchCreateIssuesTask, 30)
	for i := range tasks {
		tasks[i] = service.BatchCreateIssuesTask{Title: "t"}
	}
	res, err := e.kanban.BatchCreateIssues(e.ctx, c.ProjectID, tasks, 0)
	require.NoError(t, err)
	assert.Equal(t, 30, res.Created)
}
