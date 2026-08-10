package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupBlueprintTest(t *testing.T) (*service.KanbanService, *service.ProjectBlueprintService, *sql.DB, *store.Queries, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)

	q := store.New(db)
	activitySvc := service.NewIssueActivityService(q)
	kanban := service.NewKanbanService(db, q, activitySvc, nil, nil)
	bp := service.NewProjectBlueprintService(db, q)
	return kanban, bp, db, q, context.Background()
}

// insertScene inserts a scene row and returns its ID.
func insertScene(t *testing.T, db *sql.DB, ownerType string, ownerID int64, slug, source string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO scenes (owner_type, owner_id, slug, display_name, description, tags, source, source_slug, definition, content_hash, enabled)
		 VALUES (?, ?, ?, ?, '', '[]', ?, '', '{}', 'h', 1)`,
		ownerType, ownerID, slug, slug, source)
	require.NoError(t, err)
	id, _ := res.LastInsertId()
	return id
}

// TestProjectBlueprint_SaveAndApply is the end-to-end happy path: snapshot a
// project's columns + default scene into a blueprint, then apply it to a new
// project and assert the columns + default scenes match.
func TestProjectBlueprint_SaveAndApply(t *testing.T) {
	kanban, bp, db, q, ctx := setupBlueprintTest(t)

	// Source project with the default five columns + one attached default scene.
	src, err := kanban.CreateProjectWithDefaults(ctx, "src", "desc", "user", 7)
	require.NoError(t, err)
	sceneID := insertScene(t, db, "user", 7, "go-dev", "user")
	_, err = q.AttachProjectDefault(ctx, store.AttachProjectDefaultParams{ProjectID: src.ID, SceneID: sceneID, Position: 0})
	require.NoError(t, err)

	// Save as blueprint.
	blueprint, err := bp.SaveFromProject(ctx, src.ID, "my-template", "tmpl desc", "user", 7)
	require.NoError(t, err)
	assert.Equal(t, "my-template", blueprint.Name)
	require.Len(t, blueprint.Columns, 5, "snapshot must capture all five columns")
	require.Len(t, blueprint.Scenes, 1)
	assert.Equal(t, "go-dev", blueprint.Scenes[0].Slug)
	// The 实现 column's op_primitive/instruction/when_to_use are snapshotted.
	assert.Equal(t, "实现", blueprint.Columns[1].Name)
	assert.Equal(t, "instruct", blueprint.Columns[1].OpPrimitive)
	assert.Equal(t, "需要开始、实现、解决任务时", blueprint.Columns[1].WhenToUse)

	// Apply to a NEW project via CreateProjectWithColumns + AttachScenesToProject
	// (the same calls ProjectHandler.Create makes for blueprint_id).
	got, err := bp.Get(ctx, blueprint.ID)
	require.NoError(t, err)
	dst, err := kanban.CreateProjectWithColumns(ctx, "dst", "", "user", 7, got.Columns)
	require.NoError(t, err)
	require.NoError(t, bp.AttachScenesToProject(ctx, dst.ID, service.OwnerRef{Type: "user", ID: 7}, got.Scenes))

	// New project columns mirror the template.
	type colRow struct {
		name      string
		position  int64
		primitive string
	}
	rows, err := db.QueryContext(ctx,
		`SELECT name, position, op_primitive FROM columns WHERE project_id = ? ORDER BY position`, dst.ID)
	require.NoError(t, err)
	defer rows.Close()
	var cols []colRow
	for rows.Next() {
		var c colRow
		require.NoError(t, rows.Scan(&c.name, &c.position, &c.primitive))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	require.Len(t, cols, 5)
	assert.Equal(t, "待办", cols[0].name)
	assert.Equal(t, "instruct", cols[1].primitive)
	assert.Equal(t, "完成", cols[4].name)

	// New project default scene resolved by slug under the same owner.
	defaults, err := q.ListProjectDefaults(ctx, dst.ID)
	require.NoError(t, err)
	require.Len(t, defaults, 1)
	assert.Equal(t, "go-dev", defaults[0].SceneSlug)
}

// TestProjectBlueprint_SceneResolvesBuiltinFallback: when the owner has no scene
// with the blueprint's slug, a builtin scene with that slug is used.
func TestProjectBlueprint_SceneResolvesBuiltinFallback(t *testing.T) {
	kanban, bp, db, q, ctx := setupBlueprintTest(t)

	// Builtin scene only (owner_id 0, source builtin).
	insertScene(t, db, "user", 0, "ts-react", "builtin")

	dst, err := kanban.CreateProjectWithDefaults(ctx, "p", "", "user", 9)
	require.NoError(t, err)
	scenes := []service.BlueprintScene{{Slug: "ts-react", DisplayName: "TS", Source: "builtin"}}
	require.NoError(t, bp.AttachScenesToProject(ctx, dst.ID, service.OwnerRef{Type: "user", ID: 9}, scenes))

	defaults, err := q.ListProjectDefaults(ctx, dst.ID)
	require.NoError(t, err)
	require.Len(t, defaults, 1)
	assert.Equal(t, "ts-react", defaults[0].SceneSlug)
}

// TestProjectBlueprint_SceneUnresolvedSkipped: a slug with no owner/builtin match
// is silently skipped rather than failing the apply.
func TestProjectBlueprint_SceneUnresolvedSkipped(t *testing.T) {
	kanban, bp, _, q, ctx := setupBlueprintTest(t)

	dst, err := kanban.CreateProjectWithDefaults(ctx, "p", "", "user", 5)
	require.NoError(t, err)
	scenes := []service.BlueprintScene{{Slug: "does-not-exist"}}
	require.NoError(t, bp.AttachScenesToProject(ctx, dst.ID, service.OwnerRef{Type: "user", ID: 5}, scenes))

	defaults, err := q.ListProjectDefaults(ctx, dst.ID)
	require.NoError(t, err)
	assert.Len(t, defaults, 0)
}

// TestProjectBlueprint_DuplicateNameRejected: same name under same owner hits the
// per-owner UNIQUE index and returns ErrBlueprintNameExists.
func TestProjectBlueprint_DuplicateNameRejected(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)

	src, err := kanban.CreateProjectWithDefaults(ctx, "src", "", "user", 3)
	require.NoError(t, err)

	_, err = bp.SaveFromProject(ctx, src.ID, "dup", "", "user", 3)
	require.NoError(t, err)
	_, err = bp.SaveFromProject(ctx, src.ID, "dup", "", "user", 3)
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrBlueprintNameExists))
}

// TestProjectBlueprint_ListOwnerScoped: List only returns blueprints owned by the
// caller's accessible owners.
func TestProjectBlueprint_ListOwnerScoped(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)

	src, err := kanban.CreateProjectWithDefaults(ctx, "src", "", "user", 1)
	require.NoError(t, err)
	_, err = bp.SaveFromProject(ctx, src.ID, "user1-tmpl", "", "user", 1)
	require.NoError(t, err)

	// User 1 sees it.
	list, err := bp.ListForOwner(ctx, service.OwnerRef{Type: "user", ID: 1})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "user1-tmpl", list[0].Name)
	assert.Equal(t, "user", list[0].Source)

	// User 2 (no shared owners, no builtins seeded here) does not.
	list2, err := bp.ListForOwner(ctx, service.OwnerRef{Type: "user", ID: 2})
	require.NoError(t, err)
	assert.Len(t, list2, 0)
}

// TestProjectBlueprint_SeedAndDefault covers first-boot builtin seeding,
// owner-default resolution (builtin fallback → per-owner override), and that
// re-seeding is a no-op once builtins exist.
func TestProjectBlueprint_SeedAndDefault(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)

	require.NoError(t, bp.SeedBuiltins(ctx))
	owner := service.OwnerRef{Type: "user", ID: 4}

	// Builtins are visible to any owner; the standard-dev builtin is the default.
	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list), 4)
	for _, b := range list {
		assert.Equal(t, "builtin", b.Source)
	}

	defID, err := bp.ResolveDefaultID(ctx, owner)
	require.NoError(t, err)
	def, err := bp.Get(ctx, defID)
	require.NoError(t, err)
	assert.Equal(t, "standard-dev", def.Slug)
	assert.True(t, def.IsDefault)

	// Re-seeding is a no-op (count unchanged).
	before := len(list)
	require.NoError(t, bp.SeedBuiltins(ctx))
	list2, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	assert.Len(t, list2, before)

	// Owner overrides the default to a different builtin.
	var simpleID int64
	for _, b := range list {
		if b.Slug == "simple-kanban" {
			simpleID = b.ID
		}
	}
	require.NotZero(t, simpleID)
	require.NoError(t, bp.SetDefault(ctx, owner, simpleID))
	got, err := bp.ResolveDefaultID(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, simpleID, got)

	// A different owner still resolves to the builtin default.
	otherDef, err := bp.ResolveDefaultID(ctx, service.OwnerRef{Type: "user", ID: 99})
	require.NoError(t, err)
	assert.Equal(t, defID, otherDef)

	// Applying the overridden default yields its 3 columns.
	applied, err := bp.Get(ctx, got)
	require.NoError(t, err)
	dst, err := kanban.CreateProjectWithColumns(ctx, "simple-proj", "", "user", 4, applied.Columns)
	require.NoError(t, err)
	cols, err := kanban.ListColumns(ctx, dst.ID)
	require.NoError(t, err)
	assert.Len(t, cols, 3)
}

// TestProjectBlueprint_SeedsOfficeBuiltins verifies the office-oriented builtins
// (汇报PPT / 数据周报 / 海报) seed with generalized office lanes and a default
// scene snapshot, so non-technical users get an office workflow on first boot.
func TestProjectBlueprint_SeedsOfficeBuiltins(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)
	require.NoError(t, bp.SeedBuiltins(ctx))
	owner := service.OwnerRef{Type: "user", ID: 7}

	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	bySlug := map[string]service.ProjectBlueprint{}
	for _, b := range list {
		bySlug[b.Slug] = b
	}

	cases := []struct {
		slug      string
		columns   int
		sceneSlug string
	}{
		{"office-report-ppt", 5, "writing-studio"},
		{"office-weekly-report", 5, "data-analysis"},
		{"office-poster", 5, "media-studio"},
	}
	for _, c := range cases {
		summ, ok := bySlug[c.slug]
		require.Truef(t, ok, "office builtin %q should be seeded", c.slug)
		assert.Equal(t, "builtin", summ.Source)

		detail, err := bp.Get(ctx, summ.ID)
		require.NoError(t, err)
		assert.Lenf(t, detail.Columns, c.columns, "%s column count", c.slug)
		require.Lenf(t, detail.Scenes, 1, "%s should snapshot one default scene", c.slug)
		assert.Equal(t, c.sceneSlug, detail.Scenes[0].Slug)
		// Last lane completes the board; first lane is a non-acting backlog.
		assert.Equal(t, "complete", detail.Columns[len(detail.Columns)-1].OpPrimitive)
		assert.Equal(t, "none", detail.Columns[0].OpPrimitive)

		// Applying the office blueprint reproduces its office lanes on a new project.
		proj, err := kanban.CreateProjectWithColumns(ctx, "office-"+c.slug, "", "user", 7, detail.Columns)
		require.NoError(t, err)
		cols, err := kanban.ListColumns(ctx, proj.ID)
		require.NoError(t, err)
		assert.Lenf(t, cols, c.columns, "%s applied column count", c.slug)
	}
}

// TestProjectBlueprint_SeedsOpenSpecSuperpowers verifies the OpenSpec+Superpowers
// spec-driven builtin seeds with its six lanes and bakes the two review
// disciplines (intent lock / content-gate on the planning+implement lanes,
// make-degradation-visible on the AI-review lane) into the column phase prompts.
func TestProjectBlueprint_SeedsOpenSpecSuperpowers(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)
	require.NoError(t, bp.SeedBuiltins(ctx))
	owner := service.OwnerRef{Type: "user", ID: 11}

	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	var id int64
	for _, b := range list {
		if b.Slug == "openspec-superpowers" {
			id = b.ID
			assert.Equal(t, "builtin", b.Source)
		}
	}
	require.NotZero(t, id, "openspec-superpowers builtin should be seeded")

	detail, err := bp.Get(ctx, id)
	require.NoError(t, err)
	require.Len(t, detail.Columns, 6)
	assert.Equal(t, "none", detail.Columns[0].OpPrimitive)
	assert.Equal(t, "complete", detail.Columns[5].OpPrimitive)
	// 意图锁 / 内容级门禁 baked into the planning + implement lanes.
	assert.Contains(t, detail.Columns[1].Instruction, "意图")
	assert.Contains(t, detail.Columns[2].Instruction, "意图")
	// 让降级可见 baked into the AI review lane.
	assert.Contains(t, detail.Columns[3].Instruction, "降级")

	// Applying it reproduces the six lanes on a new project.
	proj, err := kanban.CreateProjectWithColumns(ctx, "spec-driven", "", "user", 11, detail.Columns)
	require.NoError(t, err)
	cols, err := kanban.ListColumns(ctx, proj.ID)
	require.NoError(t, err)
	assert.Len(t, cols, 6)
}

// TestProjectBlueprint_SeedsMarketingBlueprints verifies the marketing/ops
// builtins seed with their phased lanes, bake review discipline into the
// audit/review lane phase prompt, and each snapshots its marketing scene.
func TestProjectBlueprint_SeedsMarketingBlueprints(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)
	require.NoError(t, bp.SeedBuiltins(ctx))
	owner := service.OwnerRef{Type: "user", ID: 21}

	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	bySlug := map[string]service.ProjectBlueprint{}
	for _, b := range list {
		bySlug[b.Slug] = b
	}

	cases := []struct {
		slug         string
		columns      int
		sceneSlug    string
		reviewLane   int    // index of the review/gate lane
		reviewNeedle string // discipline expected baked into that lane's phase prompt
	}{
		{"content-marketing-flow", 6, "content-marketing", 3, "门禁"},
		{"social-weekly-ops", 6, "social-ops", 4, "复核"},
	}
	for _, c := range cases {
		summ, ok := bySlug[c.slug]
		require.Truef(t, ok, "marketing builtin %q should be seeded", c.slug)
		assert.Equal(t, "builtin", summ.Source)

		detail, err := bp.Get(ctx, summ.ID)
		require.NoError(t, err)
		assert.Lenf(t, detail.Columns, c.columns, "%s column count", c.slug)
		require.Lenf(t, detail.Scenes, 1, "%s should snapshot one default scene", c.slug)
		assert.Equal(t, c.sceneSlug, detail.Scenes[0].Slug)
		// First lane is a non-acting backlog; last lane completes the board.
		assert.Equal(t, "none", detail.Columns[0].OpPrimitive)
		assert.Equal(t, "complete", detail.Columns[len(detail.Columns)-1].OpPrimitive)
		// Review/gate discipline is baked into the corresponding lane's phase prompt.
		assert.Contains(t, detail.Columns[c.reviewLane].Instruction, c.reviewNeedle)

		// Applying the blueprint reproduces its lanes on a new project.
		proj, err := kanban.CreateProjectWithColumns(ctx, "mkt-"+c.slug, "", "user", 21, detail.Columns)
		require.NoError(t, err)
		cols, err := kanban.ListColumns(ctx, proj.ID)
		require.NoError(t, err)
		assert.Lenf(t, cols, c.columns, "%s applied column count", c.slug)
	}
}

// TestProjectBlueprint_BackfillMarketingBlueprints covers the upgrade path: a DB
// seeded before these builtins existed gains both exactly once, and a later user
// delete is not resurrected (the migration keys are already recorded).
func TestProjectBlueprint_BackfillMarketingBlueprints(t *testing.T) {
	_, bp, db, _, ctx := setupBlueprintTest(t)

	// Simulate a pre-existing install: a builtin is present, but not the new ones.
	_, err := db.Exec(
		`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
		 VALUES ('user', 0, 'legacy', '', '{"columns":[],"scenes":[]}', 'builtin', 'legacy-x', 0)`)
	require.NoError(t, err)

	countBySlug := func(slug string) int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_blueprints WHERE slug = ?`, slug).Scan(&n))
		return n
	}
	for _, slug := range []string{"content-marketing-flow", "social-weekly-ops"} {
		require.Equal(t, 0, countBySlug(slug))
	}

	// First backfill inserts both once; a second call does not duplicate.
	require.NoError(t, bp.BackfillMarketingBlueprints(ctx))
	require.NoError(t, bp.BackfillMarketingBlueprints(ctx))
	for _, slug := range []string{"content-marketing-flow", "social-weekly-ops"} {
		assert.Equalf(t, 1, countBySlug(slug), "%s seeded exactly once", slug)
	}

	// A later user delete stays deleted (migration key recorded).
	_, err = db.Exec(`DELETE FROM project_blueprints WHERE slug = 'content-marketing-flow'`)
	require.NoError(t, err)
	require.NoError(t, bp.BackfillMarketingBlueprints(ctx))
	assert.Equal(t, 0, countBySlug("content-marketing-flow"), "user deletion must not be resurrected")
}

// TestProjectBlueprint_SeedsWechatMPBlueprint asserts the wechat-mp-ops builtin
// seeds with its full 选题·角度→…→数据复盘 board, snapshots the 微信公众号运营 scene,
// bakes the publish-gate discipline into the 审校 lane, and applies onto a project.
func TestProjectBlueprint_SeedsWechatMPBlueprint(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)
	require.NoError(t, bp.SeedBuiltins(ctx))
	owner := service.OwnerRef{Type: "user", ID: 22}

	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	var summ service.ProjectBlueprint
	var found bool
	for _, b := range list {
		if b.Slug == "wechat-mp-ops" {
			summ, found = b, true
		}
	}
	require.True(t, found, "wechat-mp-ops builtin should be seeded")
	assert.Equal(t, "builtin", summ.Source)

	detail, err := bp.Get(ctx, summ.ID)
	require.NoError(t, err)
	assert.Len(t, detail.Columns, 6, "选题/角度 / 撰稿 / 排版 / 审校 / 发布·定时 / 数据复盘")
	require.Len(t, detail.Scenes, 1)
	assert.Equal(t, "wechat-mp", detail.Scenes[0].Slug)
	assert.Equal(t, "none", detail.Columns[0].OpPrimitive)
	assert.Equal(t, "complete", detail.Columns[len(detail.Columns)-1].OpPrimitive)
	// The 审校 lane (index 3) bakes the publish-gate discipline as a phase prompt.
	assert.Contains(t, detail.Columns[3].Instruction, "门禁")
	assert.Contains(t, detail.Columns[3].Instruction, "合规")

	proj, err := kanban.CreateProjectWithColumns(ctx, "wechat-ops", "", "user", 22, detail.Columns)
	require.NoError(t, err)
	cols, err := kanban.ListColumns(ctx, proj.ID)
	require.NoError(t, err)
	assert.Len(t, cols, 6, "applied column count")
}

// TestProjectBlueprint_BackfillOpsLoopBlueprints covers the upgrade path: a DB
// seeded before these builtins existed gains both loop blueprints (wechat-mp-ops,
// content-marketing-ops) exactly once, and a later user delete is not resurrected
// (the migration keys are already recorded).
func TestProjectBlueprint_BackfillOpsLoopBlueprints(t *testing.T) {
	_, bp, db, _, ctx := setupBlueprintTest(t)

	// Simulate a pre-existing install: a builtin is present, but not the new ones.
	_, err := db.Exec(
		`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
		 VALUES ('user', 0, 'legacy', '', '{"columns":[],"scenes":[]}', 'builtin', 'legacy-w', 0)`)
	require.NoError(t, err)

	countBySlug := func(slug string) int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_blueprints WHERE slug = ?`, slug).Scan(&n))
		return n
	}
	loopSlugs := []string{"wechat-mp-ops", "content-marketing-ops"}
	for _, slug := range loopSlugs {
		require.Equal(t, 0, countBySlug(slug))
	}

	// First backfill inserts both once; a second call does not duplicate.
	require.NoError(t, bp.BackfillOpsLoopBlueprints(ctx))
	require.NoError(t, bp.BackfillOpsLoopBlueprints(ctx))
	for _, slug := range loopSlugs {
		assert.Equalf(t, 1, countBySlug(slug), "%s seeded exactly once", slug)
	}

	// A later user delete stays deleted (migration key recorded).
	_, err = db.Exec(`DELETE FROM project_blueprints WHERE slug = 'wechat-mp-ops'`)
	require.NoError(t, err)
	require.NoError(t, bp.BackfillOpsLoopBlueprints(ctx))
	assert.Equal(t, 0, countBySlug("wechat-mp-ops"), "user deletion must not be resurrected")
}

// TestProjectBlueprint_SeedsContentMarketingLoop asserts the 2nd运营 scene reuses
// the SAME shared opsLoopColumns skeleton (issue #637 §2: generality proven by
// ≥2 scenes reusing one capability, not copy-paste): the content-marketing-ops
// loop has the identical 6-lane shape, bakes the review discipline into the 审校
// lane, and snapshots the content-marketing scene.
func TestProjectBlueprint_SeedsContentMarketingLoop(t *testing.T) {
	_, bp, _, _, ctx := setupBlueprintTest(t)
	require.NoError(t, bp.SeedBuiltins(ctx))
	owner := service.OwnerRef{Type: "user", ID: 23}

	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	var summ service.ProjectBlueprint
	var found bool
	for _, b := range list {
		if b.Slug == "content-marketing-ops" {
			summ, found = b, true
		}
	}
	require.True(t, found, "content-marketing-ops loop builtin should be seeded")

	detail, err := bp.Get(ctx, summ.ID)
	require.NoError(t, err)
	assert.Len(t, detail.Columns, 6)
	require.Len(t, detail.Scenes, 1)
	assert.Equal(t, "content-marketing", detail.Scenes[0].Slug)
	assert.Equal(t, "none", detail.Columns[0].OpPrimitive)
	assert.Equal(t, "complete", detail.Columns[len(detail.Columns)-1].OpPrimitive)
	assert.Contains(t, detail.Columns[3].Instruction, "门禁")
	assert.Contains(t, detail.Columns[3].Instruction, "合规")
}

// TestProjectBlueprint_BackfillOpenSpecSuperpowers covers the upgrade path: a DB

// TestProjectBlueprint_BackfillOpenSpecSuperpowers covers the upgrade path: a DB
// seeded before this builtin existed gains it exactly once, and a later user
// delete is not resurrected (the migration key is already recorded).
func TestProjectBlueprint_BackfillOpenSpecSuperpowers(t *testing.T) {
	_, bp, db, _, ctx := setupBlueprintTest(t)
	owner := service.OwnerRef{Type: "user", ID: 12}

	// Simulate a pre-existing install: a builtin is present, but not the new one.
	_, err := db.Exec(
		`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
		 VALUES ('user', 0, 'legacy', '', '{"columns":[],"scenes":[]}', 'builtin', 'legacy-x', 0)`)
	require.NoError(t, err)

	countBySlug := func(slug string) int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_blueprints WHERE slug = ?`, slug).Scan(&n))
		return n
	}
	require.Equal(t, 0, countBySlug("openspec-superpowers"))

	// First backfill inserts it once; a second call does not duplicate.
	require.NoError(t, bp.BackfillOpenSpecSuperpowers(ctx))
	require.Equal(t, 1, countBySlug("openspec-superpowers"))
	require.NoError(t, bp.BackfillOpenSpecSuperpowers(ctx))
	require.Equal(t, 1, countBySlug("openspec-superpowers"))

	// It is visible to owners as a builtin.
	list, err := bp.ListForOwner(ctx, owner)
	require.NoError(t, err)
	found := false
	for _, b := range list {
		if b.Slug == "openspec-superpowers" {
			found = true
		}
	}
	assert.True(t, found)

	// A later user delete stays deleted: the migration key is recorded, so a
	// subsequent backfill does not bring it back.
	_, err = db.Exec(`DELETE FROM project_blueprints WHERE slug = 'openspec-superpowers'`)
	require.NoError(t, err)
	require.NoError(t, bp.BackfillOpenSpecSuperpowers(ctx))
	assert.Equal(t, 0, countBySlug("openspec-superpowers"), "user deletion must not be resurrected")
}

// TestProjectBlueprint_SetDefaultForbidsForeignUserBlueprint: an owner cannot set
// another owner's (non-builtin) blueprint as their default.
func TestProjectBlueprint_SetDefaultForbidsForeignUserBlueprint(t *testing.T) {
	kanban, bp, _, _, ctx := setupBlueprintTest(t)
	src, err := kanban.CreateProjectWithDefaults(ctx, "src", "", "user", 1)
	require.NoError(t, err)
	saved, err := bp.SaveFromProject(ctx, src.ID, "u1", "", "user", 1)
	require.NoError(t, err)

	err = bp.SetDefault(ctx, service.OwnerRef{Type: "user", ID: 2}, saved.ID)
	assert.ErrorIs(t, err, service.ErrForbidden)
}

// TestProjectBlueprint_CreateUpdateDuplicate covers the settings-manager CRUD:
// create from explicit columns, edit, and duplicate (incl. scene preservation).
func TestProjectBlueprint_CreateUpdateDuplicate(t *testing.T) {
	_, bp, _, _, ctx := setupBlueprintTest(t)
	owner := service.OwnerRef{Type: "user", ID: 8}

	created, err := bp.Create(ctx, owner, "my-flow", "desc", []service.ColumnSeed{
		{Name: "A", OpPrimitive: "none"},
		{Name: "B", OpPrimitive: "instruct", Instruction: "do B", WhenToUse: "when B"},
	}, []service.BlueprintScene{{Slug: "go-dev", DisplayName: "Go", Source: "builtin"}})
	require.NoError(t, err)
	assert.Equal(t, "user", created.Source)
	require.Len(t, created.Columns, 2)
	require.Len(t, created.Scenes, 1)
	assert.Equal(t, "go-dev", created.Scenes[0].Slug)
	assert.EqualValues(t, 0, created.Columns[0].Position)
	assert.EqualValues(t, 1, created.Columns[1].Position)

	// Update without replacing scenes: scenes are kept.
	keep, err := bp.Update(ctx, created.ID, "my-flow-keep", "d", []service.ColumnSeed{{Name: "A", OpPrimitive: "none"}}, nil, false)
	require.NoError(t, err)
	require.Len(t, keep.Scenes, 1, "scenes kept when replaceScenes=false")

	// Update: rename + 3 columns + replace scenes (clear).
	updated, err := bp.Update(ctx, created.ID, "my-flow-2", "desc2", []service.ColumnSeed{
		{Name: "X", OpPrimitive: "none"},
		{Name: "Y", OpPrimitive: "instruct", Instruction: "do Y"},
		{Name: "Z", OpPrimitive: "complete"},
	}, []service.BlueprintScene{}, true)
	require.NoError(t, err)
	assert.Equal(t, "my-flow-2", updated.Name)
	require.Len(t, updated.Columns, 3)
	assert.Len(t, updated.Scenes, 0, "scenes replaced (cleared)")
	assert.Equal(t, "complete", updated.Columns[2].OpPrimitive)

	got, err := bp.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, got.Columns, 3)
	assert.Equal(t, "Z", got.Columns[2].Name)

	// Duplicate → new user template with the same columns.
	dup, err := bp.Duplicate(ctx, created.ID, owner, "my-flow-copy")
	require.NoError(t, err)
	assert.NotEqual(t, created.ID, dup.ID)
	assert.Equal(t, "user", dup.Source)
	require.Len(t, dup.Columns, 3)
	assert.Equal(t, "X", dup.Columns[0].Name)
}

// TestProjectBlueprint_CreateRejectsEmptyColumns guards the no-column case.
func TestProjectBlueprint_CreateRejectsEmptyColumns(t *testing.T) {
	_, bp, _, _, ctx := setupBlueprintTest(t)
	_, err := bp.Create(ctx, service.OwnerRef{Type: "user", ID: 1}, "x", "", nil, nil)
	assert.Error(t, err)
}

// TestProjectBlueprint_GetNotFound returns ErrNotFound for a missing id.
func TestProjectBlueprint_GetNotFound(t *testing.T) {
	_, bp, _, _, ctx := setupBlueprintTest(t)
	_, err := bp.Get(ctx, 4242)
	assert.True(t, errors.Is(err, service.ErrNotFound))
}

// TestCreateProjectWithColumns_EmptySeedsRejected guards the column-less guard.
func TestCreateProjectWithColumns_EmptySeedsRejected(t *testing.T) {
	kanban, _, _, _, ctx := setupBlueprintTest(t)
	_, err := kanban.CreateProjectWithColumns(ctx, "x", "", "user", 1, nil)
	assert.Error(t, err)
}
