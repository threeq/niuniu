package service_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strPtr is a tiny helper for *string literals in test cases.
func strPtr(s string) *string { return &s }

// TestCreateProject_WithColor exercises the happy path: passing a non-empty
// color string persists it as a NOT-NULL TEXT and surfaces through Get.
func TestCreateProject_WithColor(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	got, err := svc.Create(ctx, service.CreateProjectParams{
		Name:      "Colored Project",
		OwnerType: "user",
		OwnerID:   0,
		Color:     strPtr("emerald"),
	})
	require.NoError(t, err)
	require.True(t, got.Color.Valid, "Color should be Valid")
	assert.Equal(t, "emerald", got.Color.String)

	// Verify round-trip via GetProject (covers Scan path).
	retrieved, err := q.GetProject(ctx, got.ID)
	require.NoError(t, err)
	require.True(t, retrieved.Color.Valid)
	assert.Equal(t, "emerald", retrieved.Color.String)
}

// TestCreateProject_WithoutColor: Color: nil → NULL.
func TestCreateProject_WithoutColor(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	got, err := svc.Create(ctx, service.CreateProjectParams{
		Name:      "Plain Project",
		OwnerType: "user",
		OwnerID:   0,
		Color:     nil,
	})
	require.NoError(t, err)
	assert.False(t, got.Color.Valid, "Color should be NULL when omitted")
}

// TestCreateProject_EmptyColorTreatedAsNull: Color: &"" → NULL.
// Service layer trims away empty strings so the HTTP layer doesn't need to.
func TestCreateProject_EmptyColorTreatedAsNull(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	empty := ""
	got, err := svc.Create(ctx, service.CreateProjectParams{
		Name:      "Empty-Color Project",
		OwnerType: "user",
		OwnerID:   0,
		Color:     &empty,
	})
	require.NoError(t, err)
	assert.False(t, got.Color.Valid, "empty-string color should be stored as NULL")
}

// TestUpdateColor_SetValue: UpdateColor with a non-empty string sets the color.
func TestUpdateColor_SetValue(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	created, err := svc.Create(ctx, service.CreateProjectParams{
		Name:      "To Color",
		OwnerType: "user",
		OwnerID:   0,
	})
	require.NoError(t, err)
	require.False(t, created.Color.Valid)

	updated, err := svc.UpdateColor(ctx, created.ID, strPtr("amber"))
	require.NoError(t, err)
	require.True(t, updated.Color.Valid)
	assert.Equal(t, "amber", updated.Color.String)

	// Re-read to confirm persistence.
	retrieved, err := q.GetProject(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "amber", retrieved.Color.String)
}

// TestUpdateColor_ClearToNull: UpdateColor with nil clears whatever was there.
func TestUpdateColor_ClearToNull(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	created, err := svc.Create(ctx, service.CreateProjectParams{
		Name:      "To Clear",
		OwnerType: "user",
		OwnerID:   0,
		Color:     strPtr("rose"),
	})
	require.NoError(t, err)
	require.True(t, created.Color.Valid)

	cleared, err := svc.UpdateColor(ctx, created.ID, nil)
	require.NoError(t, err)
	assert.False(t, cleared.Color.Valid, "nil color should clear to NULL")

	// Round-trip.
	retrieved, err := q.GetProject(ctx, created.ID)
	require.NoError(t, err)
	assert.False(t, retrieved.Color.Valid)
}

// TestUpdateColor_EmptyStringClears: &"" also clears (service trims).
func TestUpdateColor_EmptyStringClears(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	created, err := svc.Create(ctx, service.CreateProjectParams{
		Name:      "Clear via empty",
		OwnerType: "user",
		OwnerID:   0,
		Color:     strPtr("sky"),
	})
	require.NoError(t, err)

	empty := ""
	cleared, err := svc.UpdateColor(ctx, created.ID, &empty)
	require.NoError(t, err)
	assert.False(t, cleared.Color.Valid)
}

// TestListProjects_IncludesColor: ListByStatuses returns each project's color
// (covers the manual SELECT branch in service/project.go that has to be
// kept in sync with the projects schema).
func TestListProjects_IncludesColor(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mk := func(name string, color *string) int64 {
		t.Helper()
		p, err := svc.Create(ctx, service.CreateProjectParams{
			Name: name, OwnerType: "user", Color: color,
		})
		require.NoError(t, err)
		return p.ID
	}
	id1 := mk("alpha", strPtr("emerald"))
	id2 := mk("beta", nil)
	id3 := mk("gamma", strPtr("amber"))

	// Multi-status path (>1 status) exercises the dynamic SELECT branch.
	got, err := svc.ListByStatuses(ctx, []string{"active", "hidden"})
	require.NoError(t, err)
	byID := make(map[int64]store.Project, len(got))
	for _, p := range got {
		byID[p.ID] = p
	}
	require.Contains(t, byID, id1)
	require.Contains(t, byID, id2)
	require.Contains(t, byID, id3)
	assert.True(t, byID[id1].Color.Valid)
	assert.Equal(t, "emerald", byID[id1].Color.String)
	assert.False(t, byID[id2].Color.Valid)
	assert.True(t, byID[id3].Color.Valid)
	assert.Equal(t, "amber", byID[id3].Color.String)

	// Single-status path (uses ListProjects sqlc query directly).
	got2, err := svc.ListByStatuses(ctx, []string{"active"})
	require.NoError(t, err)
	byID2 := make(map[int64]store.Project, len(got2))
	for _, p := range got2 {
		byID2[p.ID] = p
	}
	assert.True(t, byID2[id1].Color.Valid)
	assert.Equal(t, "emerald", byID2[id1].Color.String)
	assert.False(t, byID2[id2].Color.Valid)
}

// TestGetProject_IncludesColor: single-row GET path.
func TestGetProject_IncludesColor(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	created, err := svc.Create(ctx, service.CreateProjectParams{
		Name: "single", OwnerType: "user", Color: strPtr("violet"),
	})
	require.NoError(t, err)

	got, err := svc.Get(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, got.Color.Valid)
	assert.Equal(t, "violet", got.Color.String)
}
