package service_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// onboardingTestEnv wires the minimum dependencies the OnboardingSeeder needs
// over an in-memory SQLite DB with the real schema + builtin scenes seeded.
func onboardingTestEnv(t *testing.T) (*sql.DB, *store.Queries, *service.KanbanService, *service.ProjectBlueprintService, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)

	q := store.New(db)
	ctx := context.Background()
	// Seed builtin scenes (office-doc among them) via the real seeder so the
	// default-scene attach exercises the production lookup path.
	require.NoError(t, service.NewSceneSeeder(q).Run(ctx))

	kanban := service.NewKanbanService(db, q, service.NewIssueActivityService(q), nil, nil)
	bp := service.NewProjectBlueprintService(db, q)
	return db, q, kanban, bp, ctx
}

func seedLocalUser(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('local', 'x', 'admin')`)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func TestOnboardingSeeder_SeedsOfficeProject(t *testing.T) {
	db, _, kanban, bp, ctx := onboardingTestEnv(t)
	uid := seedLocalUser(t, db)
	owner := service.OwnerRef{Type: "user", ID: uid}

	seeder := service.NewOnboardingSeeder(db, kanban, bp, true, owner)
	require.NoError(t, seeder.Run(ctx))

	// 1) Exactly one project for the owner, named 牛牛助手.
	var projID int64
	var projName string
	require.NoError(t, db.QueryRow(
		`SELECT id, name FROM projects WHERE owner_type = ? AND owner_id = ?`,
		owner.Type, owner.ID).Scan(&projID, &projName))
	assert.Equal(t, "牛牛助手", projName)

	// 2) Default five columns seeded.
	var colCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM columns WHERE project_id = ?`, projID).Scan(&colCount))
	assert.Equal(t, 5, colCount)

	// 3) office-doc attached as a project default scene.
	var defScenes int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*) FROM project_default_scenes pds
		JOIN scenes s ON s.id = pds.scene_id
		WHERE pds.project_id = ? AND s.slug = 'office-doc'`, projID).Scan(&defScenes))
	assert.Equal(t, 1, defScenes, "office-doc should be a default scene of the seeded project")

	// 4) One guide issue with a goal_condition, in the first column.
	var issueCount int
	var goal string
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*), COALESCE(MAX(goal_condition), '') FROM issues
		WHERE column_id IN (SELECT id FROM columns WHERE project_id = ?)`, projID).Scan(&issueCount, &goal))
	assert.Equal(t, 1, issueCount)
	assert.NotEmpty(t, goal, "guide issue should carry a goal_condition for autohost")
}

func TestOnboardingSeeder_Idempotent(t *testing.T) {
	db, _, kanban, bp, ctx := onboardingTestEnv(t)
	uid := seedLocalUser(t, db)
	owner := service.OwnerRef{Type: "user", ID: uid}

	require.NoError(t, service.NewOnboardingSeeder(db, kanban, bp, true, owner).Run(ctx))
	// Second run must not create a second project (owner already has one).
	require.NoError(t, service.NewOnboardingSeeder(db, kanban, bp, true, owner).Run(ctx))

	var projCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE owner_type = ? AND owner_id = ?`,
		owner.Type, owner.ID).Scan(&projCount))
	assert.Equal(t, 1, projCount)
}

func TestOnboardingSeeder_DisabledOrExistingProjectsSkips(t *testing.T) {
	db, _, kanban, bp, ctx := onboardingTestEnv(t)
	uid := seedLocalUser(t, db)
	owner := service.OwnerRef{Type: "user", ID: uid}

	// Disabled (team edition): no seeding regardless of empty board.
	require.NoError(t, service.NewOnboardingSeeder(db, kanban, bp, false, owner).Run(ctx))
	var projCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE owner_type = ? AND owner_id = ?`,
		owner.Type, owner.ID).Scan(&projCount))
	assert.Equal(t, 0, projCount, "disabled seeder must not seed")

	// Owner already has a project: enabled seeder is a no-op (no second project).
	_, err := kanban.CreateProjectWithDefaults(ctx, "我的项目", "", owner.Type, owner.ID)
	require.NoError(t, err)
	require.NoError(t, service.NewOnboardingSeeder(db, kanban, bp, true, owner).Run(ctx))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE owner_type = ? AND owner_id = ?`,
		owner.Type, owner.ID).Scan(&projCount))
	assert.Equal(t, 1, projCount, "existing-project owner must not get an onboarding seed")
}
