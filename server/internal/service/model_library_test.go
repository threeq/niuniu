package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

func newTestModelLibrary(t *testing.T) *ModelLibraryService {
	t.Helper()
	db := setupModelSeedTestDB(t)
	q := store.New(db)
	require.NoError(t, SeedBuiltinModels(context.Background(), q))
	return NewModelLibraryService(q)
}

func names(list []ModelDTO) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.Name
	}
	return out
}

func TestModelLibraryMergesSystemAndOwner(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	_, err := svc.Upsert(ctx, owner, "glm-5.1", "GLM 5.1", true)
	require.NoError(t, err)
	_, err = svc.Upsert(ctx, owner, "my-private-model", "", true)
	require.NoError(t, err)

	list, err := svc.List(ctx, owner, false)
	require.NoError(t, err)
	require.Contains(t, names(list), "glm-5.1")
	require.Contains(t, names(list), "my-private-model")

	var privateID int64
	for _, m := range list {
		if m.Name == "my-private-model" {
			privateID = m.ID
		}
	}
	require.NotZero(t, privateID)
	_, err = svc.Update(ctx, owner, privateID, ptr("私有模型"), ptr(false))
	require.NoError(t, err)
	list, err = svc.List(ctx, owner, false)
	require.NotContains(t, names(list), "my-private-model")

	list, err = svc.List(ctx, owner, true)
	require.NoError(t, err)
	require.Contains(t, names(list), "my-private-model")
	for _, m := range list {
		if m.Name == "my-private-model" {
			require.False(t, m.Enabled)
			require.Equal(t, "私有模型", m.DisplayName)
		}
	}
}

// TestModelLibraryOwnerRowOverridesSystem pins the merge rule: the owner row is
// the visible row, so disabling a builtin is expressed as an override row.
func TestModelLibraryOwnerRowOverridesSystem(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	all, err := svc.List(ctx, owner, true)
	require.NoError(t, err)
	var systemID int64
	for _, m := range all {
		if m.Name == "glm-5.1" && m.OwnerID == 0 {
			systemID = m.ID
		}
	}
	require.NotZero(t, systemID)

	override, err := svc.Upsert(ctx, owner, "glm-5.1", "My GLM", false)
	require.NoError(t, err)
	require.Equal(t, owner.ID, override.OwnerID)
	require.NotEqual(t, systemID, override.ID)

	enabledOnly, err := svc.List(ctx, owner, false)
	require.NoError(t, err)
	require.NotContains(t, names(enabledOnly), "glm-5.1")

	all, err = svc.List(ctx, owner, true)
	require.NoError(t, err)
	matches := 0
	for _, m := range all {
		if m.Name == "glm-5.1" {
			matches++
			require.Equal(t, override.ID, m.ID)
			require.Equal(t, "My GLM", m.DisplayName)
			require.False(t, m.Enabled)
		}
	}
	require.Equal(t, 1, matches)

	// Listing as another owner still sees the system row untouched.
	other, err := svc.List(ctx, OwnerRef{Type: "user", ID: 2}, false)
	require.NoError(t, err)
	for _, m := range other {
		if m.Name == "glm-5.1" {
			require.Equal(t, int64(0), m.OwnerID)
			require.True(t, m.Enabled)
		}
	}
}

func TestModelLibraryCannotDeleteSystemModel(t *testing.T) {
	svc := newTestModelLibrary(t)
	owner := OwnerRef{Type: "user", ID: 1}
	list, err := svc.List(context.Background(), owner, true)
	require.NoError(t, err)
	var systemID int64
	for _, m := range list {
		if m.OwnerID == 0 {
			systemID = m.ID
			break
		}
	}
	require.NotZero(t, systemID)
	require.ErrorIs(t, svc.Delete(context.Background(), owner, systemID), ErrSystemModelImmutable)
}

func TestModelLibraryCannotUpdateSystemModel(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	list, err := svc.List(ctx, owner, true)
	require.NoError(t, err)
	var systemID int64
	for _, m := range list {
		if m.OwnerID == 0 {
			systemID = m.ID
			break
		}
	}
	require.NotZero(t, systemID)

	_, err = svc.Update(ctx, owner, systemID, ptr("hijack"), ptr(false))
	require.ErrorIs(t, err, ErrSystemModelImmutable)
}

func TestModelLibraryOwnerScoped(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	ownerA := OwnerRef{Type: "user", ID: 1}
	ownerB := OwnerRef{Type: "user", ID: 2}

	mine, err := svc.Upsert(ctx, ownerA, "owner-a-only", "", true)
	require.NoError(t, err)

	listB, err := svc.List(ctx, ownerB, true)
	require.NoError(t, err)
	require.NotContains(t, names(listB), "owner-a-only")

	require.ErrorIs(t, svc.Delete(ctx, ownerB, mine.ID), ErrNotFound)
	_, err = svc.Update(ctx, ownerB, mine.ID, ptr("stolen"), nil)
	require.ErrorIs(t, err, ErrNotFound)

	require.ErrorIs(t, svc.Delete(ctx, ownerA, 999999), ErrNotFound)
}

func TestModelLibraryImport(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	added, skipped, err := svc.Import(ctx, owner,
		[]string{"refreshed-a", "refreshed-b", "glm-5.1", "refreshed-a", "  ", ""})
	require.NoError(t, err)
	require.Equal(t, 2, added)
	require.Equal(t, 2, skipped)

	// A second import of the same names adds nothing.
	added, skipped, err = svc.Import(ctx, owner, []string{"refreshed-a", "refreshed-b"})
	require.NoError(t, err)
	require.Equal(t, 0, added)
	require.Equal(t, 2, skipped)

	list, err := svc.List(ctx, owner, false)
	require.NoError(t, err)
	for _, m := range list {
		if m.Name == "refreshed-a" {
			require.Equal(t, "refreshed", m.Source)
			require.Equal(t, "refreshed-a", m.DisplayName)
			require.True(t, m.Enabled)
		}
	}
}

// Upsert on an existing owner row updates in place instead of creating a second
// row, and accepts an override name that shadows a builtin.
func TestModelLibraryUpsertUpdatesExistingOwnerRow(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	first, err := svc.Upsert(ctx, owner, "custom", "Custom", true)
	require.NoError(t, err)
	second, err := svc.Upsert(ctx, owner, "custom", "Custom v2", false)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, "Custom v2", second.DisplayName)
	require.False(t, second.Enabled)

	list, err := svc.List(ctx, owner, true)
	require.NoError(t, err)
	count := 0
	for _, m := range list {
		if m.Name == "custom" {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestModelLibraryRequiresName(t *testing.T) {
	svc := newTestModelLibrary(t)
	_, err := svc.Upsert(context.Background(), OwnerRef{Type: "user", ID: 1}, "   ", "", true)
	require.Error(t, err)
}
