package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// setupSceneTestDB mirrors setupEnvPresetTestDB (in-memory SQLite + ApplySchema
// + Migrate) so scene-service tests don't depend on workspace fixtures.
func setupSceneTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	store.Driver = "sqlite"
	require.NoError(t, store.ApplySchema(db))
	store.Migrate(db)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedBuiltinScene inserts a row with source='builtin' via UpsertBuiltinScene
// (the same path SceneSeeder uses). Returns the scene's id.
func seedBuiltinScene(t *testing.T, db *sql.DB, slug, displayName string) int64 {
	t.Helper()
	q := store.New(db)
	def := &SceneDefinition{
		MCP: []MCPDecl{{Name: "memory"}},
	}
	defJSON, _ := json.Marshal(def)
	require.NoError(t, q.UpsertBuiltinScene(context.Background(), store.UpsertBuiltinSceneParams{
		Slug:        slug,
		DisplayName: displayName,
		Description: "test seed",
		Tags:        "[]",
		SourceSlug:  slug,
		Definition:  string(defJSON),
		ContentHash: HashDefinition(def),
	}))
	got, err := q.GetSceneByOwnerSlug(context.Background(), store.GetSceneByOwnerSlugParams{
		OwnerType: "user",
		OwnerID:   0,
		Slug:      slug,
	})
	require.NoError(t, err)
	return got.ID
}

func TestSceneService_Create_HappyPath(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)

	owner := OwnerRef{Type: "user", ID: 42}
	def := &SceneDefinition{
		MCP:     []MCPDecl{{Name: "memory"}, {Name: "context7"}},
		Plugins: []PluginDecl{{Source: "github:foo/bar", Optional: true}},
	}
	scene, err := svc.Create(ctx, owner, "my-scene", "My Scene", "desc", []string{"a", "b"}, def)
	require.NoError(t, err)
	assert.Equal(t, "user", scene.OwnerType)
	assert.Equal(t, int64(42), scene.OwnerID)
	assert.Equal(t, "my-scene", scene.Slug)
	assert.Equal(t, "user", scene.Source)
	assert.Equal(t, HashDefinition(def), scene.ContentHash)
	assert.Equal(t, `["a","b"]`, scene.Tags)
}

func TestSceneService_Create_ValidatesDefinition(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)
	owner := OwnerRef{Type: "user", ID: 1}

	_, err := svc.Create(ctx, owner, "bad", "Bad", "", nil, &SceneDefinition{
		Plugins: []PluginDecl{{Source: "evil://nope"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestSceneService_Create_RejectsEmptySlug(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)
	_, err := svc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "", "x", "", nil, nil)
	require.Error(t, err)
}

func TestSceneService_Update_BuiltinImmutable(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)

	id := seedBuiltinScene(t, db, "go-dev", "Go 开发")
	err := svc.Update(ctx, id, "Renamed", "", nil, &SceneDefinition{})
	assert.ErrorIs(t, err, ErrBuiltinImmutable)
}

func TestSceneService_Delete_BuiltinImmutable(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)

	id := seedBuiltinScene(t, db, "go-dev", "Go 开发")
	err := svc.Delete(ctx, id)
	assert.ErrorIs(t, err, ErrBuiltinImmutable)
}

func TestSceneService_Update_UserSceneRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)
	owner := OwnerRef{Type: "user", ID: 1}

	scene, err := svc.Create(ctx, owner, "s", "S", "", nil, &SceneDefinition{})
	require.NoError(t, err)

	def := &SceneDefinition{MCP: []MCPDecl{{Name: "memory"}}}
	require.NoError(t, svc.Update(ctx, scene.ID, "S2", "new desc", []string{"x"}, def))

	got, err := svc.Get(ctx, scene.ID)
	require.NoError(t, err)
	assert.Equal(t, "S2", got.DisplayName)
	assert.Equal(t, "new desc", got.Description)
	assert.Equal(t, HashDefinition(def), got.ContentHash)
}

func TestSceneService_UpdateWithOwner_MovesSceneOwner(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)

	scene, err := svc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "shared-scene", "Shared", "", nil, &SceneDefinition{})
	require.NoError(t, err)

	newOwner := OwnerRef{Type: "org", ID: 9}
	require.NoError(t, svc.UpdateWithOwner(ctx, scene.ID, &newOwner, "Shared", "", nil, &SceneDefinition{}))

	got, err := svc.Get(ctx, scene.ID)
	require.NoError(t, err)
	assert.Equal(t, "org", got.OwnerType)
	assert.Equal(t, int64(9), got.OwnerID)
	assert.Equal(t, "shared-scene", got.Slug)
}

func TestSceneService_Delete_RemovesUserScene(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)
	scene, err := svc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "s", "S", "", nil, &SceneDefinition{})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, scene.ID))
	_, err = svc.Get(ctx, scene.ID)
	assert.Error(t, err)
}

func TestSceneService_Fork(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)

	srcID := seedBuiltinScene(t, db, "go-dev", "Go 开发")
	owner := OwnerRef{Type: "user", ID: 7}
	fork, err := svc.Fork(ctx, owner, srcID, "my-go-dev")
	require.NoError(t, err)
	assert.Equal(t, "user", fork.Source)
	assert.Equal(t, "go-dev", fork.SourceSlug)
	assert.Equal(t, "my-go-dev", fork.Slug)
	assert.Equal(t, int64(7), fork.OwnerID)
	// Forks ARE mutable.
	assert.NoError(t, svc.Update(ctx, fork.ID, "Custom", "", nil, &SceneDefinition{}))
}

func TestSceneService_ListAccessible_IncludesBuiltinPlusOwn(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	svc := NewSceneService(db)

	seedBuiltinScene(t, db, "go-dev", "Go 开发")
	owner := OwnerRef{Type: "user", ID: 5}
	_, err := svc.Create(ctx, owner, "personal", "Personal", "", nil, &SceneDefinition{})
	require.NoError(t, err)

	got, err := svc.ListAccessible(ctx, owner, 5)
	require.NoError(t, err)
	slugs := map[string]bool{}
	for _, s := range got {
		slugs[s.Slug] = true
	}
	assert.True(t, slugs["go-dev"], "builtin should be visible")
	assert.True(t, slugs["personal"], "own scene should be visible")
}

func TestDecodeDefinition_HandlesEmpty(t *testing.T) {
	def, err := DecodeDefinition("")
	require.NoError(t, err)
	assert.NotNil(t, def)
}

func TestDecodeTags_HandlesNullishInputs(t *testing.T) {
	assert.Equal(t, []string{}, DecodeTags(""))
	assert.Equal(t, []string{}, DecodeTags("null"))
	assert.Equal(t, []string{}, DecodeTags("[]"))
	assert.Equal(t, []string{"a", "b"}, DecodeTags(`["a","b"]`))
}
