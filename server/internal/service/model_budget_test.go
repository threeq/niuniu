package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func addBudgetUsage(t *testing.T, q *store.Queries, ownerID, projectID, tokens int64) {
	t.Helper()
	require.NoError(t, q.UpsertModelUsageHourly(context.Background(), store.UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: ownerID, ProjectID: projectID, WorkspaceID: 3,
		AgentCli: "claude", ModelName: "glm-5.1", ProviderID: 4,
		BucketHour: time.Now().UTC(), InputTokens: tokens, InteractionCount: 1,
	}))
}

func setBudget(t *testing.T, q *store.Queries, projectID, monthlyTokens int64) {
	t.Helper()
	_, err := q.UpsertModelBudget(context.Background(), store.UpsertModelBudgetParams{
		OwnerType: "user", OwnerID: 1, ProjectID: projectID,
		MonthlyTokens: monthlyTokens, Enabled: 1,
	})
	require.NoError(t, err)
}

// newTestBudget builds a service over an owner (id=1) with an optional owner
// hard cap, used tokens spread over project 2, and an optional project-2 cap.
func newTestBudget(t *testing.T, ownerLimit, usedTokens int64, projectLimit ...int64) *ModelBudgetService {
	t.Helper()
	q := store.New(setupTestDB(t))
	if ownerLimit > 0 {
		setBudget(t, q, 0, ownerLimit)
	}
	if usedTokens > 0 {
		addBudgetUsage(t, q, 1, 2, usedTokens)
	}
	if len(projectLimit) > 0 && projectLimit[0] > 0 {
		setBudget(t, q, 2, projectLimit[0])
	}
	return NewModelBudgetService(q, nil)
}

func TestBudgetCheckOwnerHardCap(t *testing.T) {
	svc := newTestBudget(t, 1000, 900)

	dec, err := svc.Check(context.Background(), "user", 1, 0)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.False(t, dec.OwnerExceeded)
	require.EqualValues(t, 900, dec.UsedTokens)
	require.EqualValues(t, 1000, dec.LimitTokens)
	require.NotEmpty(t, dec.Period)

	setBudget(t, svc.q, 0, 800)
	dec, err = svc.Check(context.Background(), "user", 1, 0)
	require.NoError(t, err)
	require.False(t, dec.Allowed)
	require.True(t, dec.OwnerExceeded)
	require.EqualValues(t, 900, dec.UsedTokens)
}

func TestBudgetCheckProjectSoftCap(t *testing.T) {
	svc := newTestBudget(t, 1000, 100, 50)

	dec, err := svc.Check(context.Background(), "user", 1, 2)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.False(t, dec.OwnerExceeded)
	require.True(t, dec.ProjectWarning)

	// No project configured for another project id → no warning.
	dec, err = svc.Check(context.Background(), "user", 1, 99)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.False(t, dec.ProjectWarning)
}

func TestBudgetCheckNoBudgetConfigured(t *testing.T) {
	svc := newTestBudget(t, 0, 5000)

	dec, err := svc.Check(context.Background(), "user", 1, 2)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.False(t, dec.OwnerExceeded)
	require.False(t, dec.ProjectWarning)
	require.Zero(t, dec.LimitTokens)
}

func TestBudgetCheckFailOpenOnLookupFailure(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	// No schema applied: every query fails. Check must fail open.
	svc := NewModelBudgetService(store.New(db), nil)

	dec, err := svc.Check(context.Background(), "user", 1, 2)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.False(t, dec.OwnerExceeded)
}

func TestBudgetNotifyOncePerPeriod(t *testing.T) {
	svc := newTestBudget(t, 100, 100)
	hub := notify.NewNotificationHub()
	t.Cleanup(hub.Stop)
	ch := hub.Register("w1", 1, nil)
	svc.hub = hub

	for i := 0; i < 3; i++ {
		dec, err := svc.Check(context.Background(), "user", 1, 0)
		require.NoError(t, err)
		require.False(t, dec.Allowed)
	}

	var got notify.Notification
	select {
	case raw := <-ch:
		require.NoError(t, json.Unmarshal(raw, &got))
	case <-time.After(time.Second):
		t.Fatal("expected one budget notification")
	}
	require.Equal(t, notify.TopicModelBudget, got.Topic)
	require.Equal(t, "exceeded", got.Action)

	select {
	case raw := <-ch:
		t.Fatalf("expected no duplicate notification, got %s", raw)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBudgetNotifyProjectOncePerPeriod(t *testing.T) {
	svc := newTestBudget(t, 1000, 100, 50)
	hub := notify.NewNotificationHub()
	t.Cleanup(hub.Stop)
	ch := hub.Register("w1", 1, nil)
	svc.hub = hub

	for i := 0; i < 3; i++ {
		dec, err := svc.Check(context.Background(), "user", 1, 2)
		require.NoError(t, err)
		require.True(t, dec.Allowed)
		require.True(t, dec.ProjectWarning)
	}

	var got notify.Notification
	select {
	case raw := <-ch:
		require.NoError(t, json.Unmarshal(raw, &got))
	case <-time.After(time.Second):
		t.Fatal("expected one project warning")
	}
	require.Equal(t, "project_warning", got.Action)

	select {
	case raw := <-ch:
		t.Fatalf("expected no duplicate project warning, got %s", raw)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBudgetSetGetDelete(t *testing.T) {
	q := store.New(setupTestDB(t))
	addBudgetUsage(t, q, 1, 7, 300)
	addBudgetUsage(t, q, 1, 2, 200)
	svc := NewModelBudgetService(q, nil)
	owner := OwnerRef{Type: "user", ID: 1}
	ctx := context.Background()

	require.NoError(t, svc.Set(ctx, owner, 5000))
	require.NoError(t, svc.SetProject(ctx, owner, 7, 1000))

	got, err := svc.Get(ctx, owner)
	require.NoError(t, err)
	require.EqualValues(t, 5000, got.MonthlyTokens)
	require.EqualValues(t, 500, got.UsedTokens)
	require.Len(t, got.Projects, 1)
	require.EqualValues(t, 7, got.Projects[0].ProjectID)
	require.EqualValues(t, 1000, got.Projects[0].MonthlyTokens)
	require.EqualValues(t, 300, got.Projects[0].UsedTokens)

	require.NoError(t, svc.DeleteProject(ctx, owner, 7))
	got, err = svc.Get(ctx, owner)
	require.NoError(t, err)
	require.Empty(t, got.Projects)

	require.NoError(t, svc.Set(ctx, owner, 0))
	got, err = svc.Get(ctx, owner)
	require.NoError(t, err)
	require.Zero(t, got.MonthlyTokens)
}
