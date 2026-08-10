// Package service: ProjectBlueprintService — persistence + apply logic for
// project blueprints (UI: "项目模板" / project template). A blueprint is a
// reusable snapshot of a project's kanban columns + default scenes. It is saved
// from an existing project, seeded as builtins on first boot, and applied when
// creating a new one.
//
// This service is pure data: authorization is enforced by the API handler
// (mirroring ProjectSceneHandler). Storage uses raw SQL over the driver-aware
// *store.DB (no sqlc), matching the raw-SQL precedent in CreateProjectWithDefaults
// — op_primitive / when_to_use live outside the sqlc column model.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrBlueprintNameExists is returned when a blueprint with the same name already
// exists under the same owner (the per-owner UNIQUE index).
var ErrBlueprintNameExists = errors.New("blueprint name already exists")

// builtinDefaultSlug identifies the builtin blueprint that is the system-wide
// fallback default (used when an owner has set no per-owner default pointer).
const builtinDefaultSlug = "standard-dev"

// builtinOpenSpecSuperpowersSlug identifies the OpenSpec+Superpowers spec-driven
// development builtin. It was added after the initial builtin set shipped, so
// SeedBuiltins alone (a no-op once any builtin exists) does not reach upgraded
// DBs — BackfillOpenSpecSuperpowers backfills those once via the migration ledger.
const builtinOpenSpecSuperpowersSlug = "openspec-superpowers"

// Marketing/ops builtins were added after the initial set shipped, so like
// openspec-superpowers they are backfilled once into upgraded DBs via
// BackfillMarketingBlueprints (SeedBuiltins is a no-op once any builtin exists).
const (
	marketingContentFlowSlug = "content-marketing-flow"
	marketingSocialOpsSlug   = "social-weekly-ops"
)

// The运营闭环 (ops-loop) loop blueprints were added after the marketing builtins
// shipped, so like them they are backfilled once into upgraded DBs via
// BackfillOpsLoopBlueprints (SeedBuiltins is a no-op once any builtin exists).
// Their slugs are derived from the shared opsLoopPlatforms() registry so the
// blueprint defs and the backfill stay in lock-step.

// blueprintSelectCols is the shared column list for blueprint row reads, kept in
// one place so every SELECT and scanBlueprintRow stay in lock-step.
const blueprintSelectCols = `id, owner_type, owner_id, name, description, content, source, slug, is_default, created_at`

// BlueprintScene is one scene reference inside a blueprint. Scenes are owner
// scoped, so a blueprint stores the stable slug (+ display fields) rather than a
// scene_id, and resolves it under the target project's owner at apply time.
type BlueprintScene struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Source      string `json:"source"`
}

// blueprintColumn is the on-disk JSON shape for one column in a blueprint.
type blueprintColumn struct {
	Name        string `json:"name"`
	Position    int64  `json:"position"`
	Lifecycle   string `json:"lifecycle_mapping"`
	OpPrimitive string `json:"op_primitive"`
	Instruction string `json:"op_instruction"`
	WhenToUse   string `json:"when_to_use"`
}

// blueprintContent is the JSON persisted in project_blueprints.content.
type blueprintContent struct {
	Columns []blueprintColumn `json:"columns"`
	Scenes  []BlueprintScene  `json:"scenes"`
}

// ProjectBlueprint is the decoded, in-memory view of a blueprint row.
type ProjectBlueprint struct {
	ID          int64
	Owner       OwnerRef
	Name        string
	Description string
	Source      string // 'user' | 'builtin'
	Slug        string
	IsDefault   bool // system-default marker (only meaningful on builtins)
	Columns     []ColumnSeed
	Scenes      []BlueprintScene
	CreatedAt   time.Time
}

// ProjectBlueprintService persists and applies project blueprints.
type ProjectBlueprintService struct {
	db *store.DB
	q  *store.Queries
}

// NewProjectBlueprintService constructs the service. db is the raw *sql.DB; it
// is wrapped for driver-aware placeholder rewriting.
func NewProjectBlueprintService(db *sql.DB, q *store.Queries) *ProjectBlueprintService {
	return &ProjectBlueprintService{db: store.Wrap(db), q: q}
}

// SaveFromProject snapshots a project's columns + default scenes into a new
// user blueprint owned by (ownerType, ownerID). The caller (handler) is
// responsible for verifying the caller may read the project and write to owner.
func (s *ProjectBlueprintService) SaveFromProject(ctx context.Context, projectID int64, name, description, ownerType string, ownerID int64) (ProjectBlueprint, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectBlueprint{}, fmt.Errorf("blueprint name is required")
	}

	cols, err := s.snapshotColumns(ctx, projectID)
	if err != nil {
		return ProjectBlueprint{}, err
	}
	if len(cols) == 0 {
		return ProjectBlueprint{}, fmt.Errorf("project has no columns to snapshot")
	}
	scenes, err := s.snapshotScenes(ctx, projectID)
	if err != nil {
		return ProjectBlueprint{}, err
	}
	return s.insertUserBlueprint(ctx, OwnerRef{Type: ownerType, ID: ownerID}, name, description, cols, scenes)
}

// insertUserBlueprint inserts a new source='user' blueprint with the given
// columns + scenes (positions are reindexed 0..n). Shared by SaveFromProject,
// Create and Duplicate.
func (s *ProjectBlueprintService) insertUserBlueprint(ctx context.Context, owner OwnerRef, name, description string, cols []blueprintColumn, scenes []BlueprintScene) (ProjectBlueprint, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectBlueprint{}, fmt.Errorf("blueprint name is required")
	}
	if len(cols) == 0 {
		return ProjectBlueprint{}, fmt.Errorf("a template needs at least one column")
	}
	for i := range cols {
		cols[i].Position = int64(i)
	}
	if scenes == nil {
		scenes = []BlueprintScene{}
	}
	raw, err := json.Marshal(blueprintContent{Columns: cols, Scenes: scenes})
	if err != nil {
		return ProjectBlueprint{}, err
	}
	var id int64
	var createdAt time.Time
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
		 VALUES (?, ?, ?, ?, ?, 'user', '', 0) RETURNING id, created_at`,
		owner.Type, owner.ID, name, description, string(raw),
	).Scan(&id, &createdAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ProjectBlueprint{}, fmt.Errorf("%w: %q", ErrBlueprintNameExists, name)
		}
		return ProjectBlueprint{}, err
	}
	return ProjectBlueprint{
		ID: id, Owner: owner, Name: name, Description: description, Source: "user",
		Columns: seedsFromColumns(cols), Scenes: scenes, CreatedAt: createdAt,
	}, nil
}

// Create builds a brand-new user template from explicit columns + scenes
// (settings → "add").
func (s *ProjectBlueprintService) Create(ctx context.Context, owner OwnerRef, name, description string, columns []ColumnSeed, scenes []BlueprintScene) (ProjectBlueprint, error) {
	return s.insertUserBlueprint(ctx, owner, name, description, toBlueprintColumns(columns), scenes)
}

// Update replaces a template's name / description / columns. When replaceScenes
// is true the scenes are replaced with the given list; otherwise the existing
// scenes are kept. source / slug / is_default / owner are always preserved.
// Returns ErrNotFound for a missing id.
func (s *ProjectBlueprintService) Update(ctx context.Context, id int64, name, description string, columns []ColumnSeed, scenes []BlueprintScene, replaceScenes bool) (ProjectBlueprint, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return ProjectBlueprint{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectBlueprint{}, fmt.Errorf("blueprint name is required")
	}
	if len(columns) == 0 {
		return ProjectBlueprint{}, fmt.Errorf("a template needs at least one column")
	}
	cols := toBlueprintColumns(columns)
	for i := range cols {
		cols[i].Position = int64(i)
	}
	finalScenes := existing.Scenes
	if replaceScenes {
		finalScenes = scenes
		if finalScenes == nil {
			finalScenes = []BlueprintScene{}
		}
	}
	raw, err := json.Marshal(blueprintContent{Columns: cols, Scenes: finalScenes})
	if err != nil {
		return ProjectBlueprint{}, err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE project_blueprints SET name = ?, description = ?, content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		name, description, string(raw), id)
	if err != nil {
		if isUniqueViolation(err) {
			return ProjectBlueprint{}, fmt.Errorf("%w: %q", ErrBlueprintNameExists, name)
		}
		return ProjectBlueprint{}, err
	}
	existing.Name = name
	existing.Description = description
	existing.Columns = seedsFromColumns(cols)
	existing.Scenes = finalScenes
	return existing, nil
}

// Duplicate copies an existing template's columns + scenes into a new user
// template owned by owner. The caller must be able to read the source (handler).
func (s *ProjectBlueprintService) Duplicate(ctx context.Context, id int64, owner OwnerRef, name string) (ProjectBlueprint, error) {
	src, err := s.Get(ctx, id)
	if err != nil {
		return ProjectBlueprint{}, err
	}
	return s.insertUserBlueprint(ctx, owner, name, src.Description, toBlueprintColumns(src.Columns), src.Scenes)
}

// ListForOwner returns every blueprint usable by owner: all builtins plus the
// owner's own blueprints. Builtins + the owner's default sort first.
func (s *ProjectBlueprintService) ListForOwner(ctx context.Context, owner OwnerRef) ([]ProjectBlueprint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+blueprintSelectCols+` FROM project_blueprints
		 WHERE source = 'builtin' OR (owner_type = ? AND owner_id = ?)
		 ORDER BY is_default DESC, source ASC, created_at ASC, id ASC`,
		owner.Type, owner.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectBlueprints(rows)
}

// Get loads a single blueprint (with decoded columns + scenes). Returns
// ErrNotFound when the id does not exist.
func (s *ProjectBlueprintService) Get(ctx context.Context, id int64) (ProjectBlueprint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+blueprintSelectCols+` FROM project_blueprints WHERE id = ?`, id)
	bp, err := scanBlueprintRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectBlueprint{}, ErrNotFound
	}
	if err != nil {
		return ProjectBlueprint{}, err
	}
	return bp, nil
}

// Delete removes a blueprint by id. Authorization is the handler's job.
func (s *ProjectBlueprintService) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_blueprints WHERE id = ?`, id)
	return err
}

// ResolveDefaultID returns the blueprint id pre-selected when creating a project
// for owner: the owner's pointer if set (and still present), else the builtin
// system default, else any builtin. Returns 0 when no blueprint exists at all
// (caller falls back to the hardcoded default columns).
func (s *ProjectBlueprintService) ResolveDefaultID(ctx context.Context, owner OwnerRef) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT b.id FROM default_project_blueprints d
		   JOIN project_blueprints b ON b.id = d.blueprint_id
		  WHERE d.owner_type = ? AND d.owner_id = ?`, owner.Type, owner.ID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// Builtin system default.
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM project_blueprints WHERE source = 'builtin' AND is_default = 1
		 ORDER BY id ASC LIMIT 1`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// Any builtin.
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM project_blueprints WHERE source = 'builtin' ORDER BY id ASC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SetDefault upserts owner's default-blueprint pointer. The blueprint must be
// usable by owner (builtin or owned by owner); otherwise ErrForbidden.
func (s *ProjectBlueprintService) SetDefault(ctx context.Context, owner OwnerRef, blueprintID int64) error {
	bp, err := s.Get(ctx, blueprintID)
	if err != nil {
		return err
	}
	usable := bp.Source == "builtin" || (bp.Owner.Type == owner.Type && bp.Owner.ID == owner.ID)
	if !usable {
		return ErrForbidden
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO default_project_blueprints (owner_type, owner_id, blueprint_id)
		 VALUES (?, ?, ?)
		 ON CONFLICT (owner_type, owner_id)
		 DO UPDATE SET blueprint_id = excluded.blueprint_id, updated_at = CURRENT_TIMESTAMP`,
		owner.Type, owner.ID, blueprintID)
	return err
}

// AttachScenesToProject resolves each blueprint scene under the target project's
// owner (preferring an owner-owned scene by slug, falling back to a builtin
// scene with that slug) and attaches it as a project default. Scenes that
// cannot be resolved under this owner are silently skipped (best-effort).
func (s *ProjectBlueprintService) AttachScenesToProject(ctx context.Context, projectID int64, owner OwnerRef, scenes []BlueprintScene) error {
	pos := int64(0)
	for _, sc := range scenes {
		if sc.Slug == "" {
			continue
		}
		var sceneID int64
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM scenes WHERE slug = ? AND owner_type = ? AND owner_id = ? LIMIT 1`,
			sc.Slug, owner.Type, owner.ID).Scan(&sceneID)
		if errors.Is(err, sql.ErrNoRows) {
			err = s.db.QueryRowContext(ctx,
				`SELECT id FROM scenes WHERE slug = ? AND source = 'builtin' LIMIT 1`,
				sc.Slug).Scan(&sceneID)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue // not available under this owner — skip
		}
		if err != nil {
			return err
		}
		if _, err := s.q.AttachProjectDefault(ctx, store.AttachProjectDefaultParams{
			ProjectID: projectID,
			SceneID:   sceneID,
			Position:  pos,
		}); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
		pos++
	}
	return nil
}

// SeedBuiltins inserts the builtin blueprint set the first time none exist
// ("如果没有就初始化"). It is a no-op once any builtin row is present, so user
// edits/deletes of builtins are never resurrected per-row. Idempotent + boot-safe.
func (s *ProjectBlueprintService) SeedBuiltins(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_blueprints WHERE source = 'builtin'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, def := range builtinBlueprintDefs() {
		scenes := def.scenes
		if scenes == nil {
			scenes = []BlueprintScene{}
		}
		raw, err := json.Marshal(blueprintContent{Columns: toBlueprintColumns(def.columns), Scenes: scenes})
		if err != nil {
			return err
		}
		isDefault := 0
		if def.slug == builtinDefaultSlug {
			isDefault = 1
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
			 VALUES ('user', 0, ?, ?, ?, 'builtin', ?, ?)`,
			def.name, def.description, string(raw), def.slug, isDefault); err != nil {
			// A name/owner UNIQUE clash (e.g. a user blueprint already took the
			// name in personal edition) is non-fatal — skip that builtin.
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
	}
	return nil
}

// BackfillOpenSpecSuperpowers seeds the openspec-superpowers builtin into DBs
// that were initialized before this builtin was added. SeedBuiltins is
// all-or-nothing (a no-op once any builtin row exists), so a builtin added later
// never reaches upgraded installs through it. This inserts it exactly once per DB
// via the migration ledger: the slug never shipped before, so a one-time insert
// resurrects nothing, and a later user delete stays deleted (the migration key is
// already recorded, and the guarded INSERT is a WHERE-NOT-EXISTS no-op anyway).
// Fresh installs already get it through SeedBuiltins; this call is then a no-op.
func (s *ProjectBlueprintService) BackfillOpenSpecSuperpowers(ctx context.Context) error {
	def, ok := builtinBlueprintDefBySlug(builtinOpenSpecSuperpowersSlug)
	if !ok {
		return nil
	}
	scenes := def.scenes
	if scenes == nil {
		scenes = []BlueprintScene{}
	}
	raw, err := json.Marshal(blueprintContent{Columns: toBlueprintColumns(def.columns), Scenes: scenes})
	if err != nil {
		return err
	}
	// Literal-valued INSERT (no ? placeholders) so it runs verbatim on both
	// drivers; single quotes are doubled for SQL-literal safety.
	q := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	insert := fmt.Sprintf(
		`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
		 SELECT 'user', 0, %s, %s, %s, 'builtin', %s, 0
		 WHERE NOT EXISTS (SELECT 1 FROM project_blueprints WHERE slug = %s)`,
		q(def.name), q(def.description), q(string(raw)), q(def.slug), q(def.slug),
	)
	return store.EnsureMigration(s.db, "seed_openspec_superpowers_blueprint_v1", insert)
}

// BackfillMarketingBlueprints seeds the marketing/ops builtins
// (content-marketing-flow, social-weekly-ops) into DBs initialized before they
// were added. Same upgrade contract as BackfillOpenSpecSuperpowers: idempotent
// via the migration ledger, WHERE-NOT-EXISTS guarded per slug, and a later user
// delete stays deleted. Fresh installs get them through SeedBuiltins, making this
// a no-op there.
func (s *ProjectBlueprintService) BackfillMarketingBlueprints(ctx context.Context) error {
	q := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	for _, slug := range []string{marketingContentFlowSlug, marketingSocialOpsSlug} {
		def, ok := builtinBlueprintDefBySlug(slug)
		if !ok {
			continue
		}
		scenes := def.scenes
		if scenes == nil {
			scenes = []BlueprintScene{}
		}
		raw, err := json.Marshal(blueprintContent{Columns: toBlueprintColumns(def.columns), Scenes: scenes})
		if err != nil {
			return err
		}
		insert := fmt.Sprintf(
			`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
			 SELECT 'user', 0, %s, %s, %s, 'builtin', %s, 0
			 WHERE NOT EXISTS (SELECT 1 FROM project_blueprints WHERE slug = %s)`,
			q(def.name), q(def.description), q(string(raw)), q(def.slug), q(def.slug),
		)
		if err := store.EnsureMigration(s.db, "seed_"+strings.ReplaceAll(def.slug, "-", "_")+"_blueprint_v1", insert); err != nil {
			return err
		}
	}
	return nil
}

// BackfillOpsLoopBlueprints seeds the运营闭环 (ops-loop) loop blueprints — one per
// platform in opsLoopPlatforms() (wechat-mp-ops, content-marketing-ops) — into DBs
// initialized before they were added. Same upgrade contract as
// BackfillMarketingBlueprints: idempotent via the migration ledger, WHERE-NOT-EXISTS
// guarded per slug, and a later user delete stays deleted. Fresh installs get them
// through SeedBuiltins, making this a no-op there.
func (s *ProjectBlueprintService) BackfillOpsLoopBlueprints(ctx context.Context) error {
	q := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	for _, p := range opsLoopPlatforms() {
		def, ok := builtinBlueprintDefBySlug(opsLoopBlueprintSlug(p))
		if !ok {
			continue
		}
		scenes := def.scenes
		if scenes == nil {
			scenes = []BlueprintScene{}
		}
		raw, err := json.Marshal(blueprintContent{Columns: toBlueprintColumns(def.columns), Scenes: scenes})
		if err != nil {
			return err
		}
		insert := fmt.Sprintf(
			`INSERT INTO project_blueprints (owner_type, owner_id, name, description, content, source, slug, is_default)
			 SELECT 'user', 0, %s, %s, %s, 'builtin', %s, 0
			 WHERE NOT EXISTS (SELECT 1 FROM project_blueprints WHERE slug = %s)`,
			q(def.name), q(def.description), q(string(raw)), q(def.slug), q(def.slug),
		)
		if err := store.EnsureMigration(s.db, "seed_"+strings.ReplaceAll(def.slug, "-", "_")+"_blueprint_v1", insert); err != nil {
			return err
		}
	}
	return nil
}

// builtinBlueprintDefBySlug returns the seeded builtin definition with the given
// slug — the single source of truth shared by SeedBuiltins and the backfill path.
func builtinBlueprintDefBySlug(slug string) (builtinBlueprintDef, bool) {
	for _, d := range builtinBlueprintDefs() {
		if d.slug == slug {
			return d, true
		}
	}
	return builtinBlueprintDef{}, false
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *ProjectBlueprintService) snapshotColumns(ctx context.Context, projectID int64) ([]blueprintColumn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, position, COALESCE(lifecycle_mapping, ''), COALESCE(op_primitive, 'none'),
		        COALESCE(when_to_use, ''), COALESCE(phase_prompt, '')
		 FROM columns WHERE project_id = ? ORDER BY position ASC, id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []blueprintColumn{}
	for rows.Next() {
		var c blueprintColumn
		if err := rows.Scan(&c.Name, &c.Position, &c.Lifecycle, &c.OpPrimitive, &c.WhenToUse, &c.Instruction); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *ProjectBlueprintService) snapshotScenes(ctx context.Context, projectID int64) ([]BlueprintScene, error) {
	rows, err := s.q.ListProjectDefaults(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := []BlueprintScene{}
	for _, r := range rows {
		out = append(out, BlueprintScene{Slug: r.SceneSlug, DisplayName: r.SceneDisplayName, Source: r.SceneSource})
	}
	return out, nil
}

// rowScanner abstracts *sql.Row and *sql.Rows for scanBlueprintRow.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBlueprintRow(r rowScanner) (ProjectBlueprint, error) {
	var (
		bp        ProjectBlueprint
		ownerType string
		ownerID   int64
		content   string
		isDefault int64
	)
	if err := r.Scan(&bp.ID, &ownerType, &ownerID, &bp.Name, &bp.Description, &content,
		&bp.Source, &bp.Slug, &isDefault, &bp.CreatedAt); err != nil {
		return ProjectBlueprint{}, err
	}
	bp.Owner = OwnerRef{Type: ownerType, ID: ownerID}
	bp.IsDefault = isDefault != 0
	var decoded blueprintContent
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return ProjectBlueprint{}, fmt.Errorf("decode blueprint %d content: %w", bp.ID, err)
	}
	bp.Columns = seedsFromColumns(decoded.Columns)
	bp.Scenes = decoded.Scenes
	if bp.Scenes == nil {
		bp.Scenes = []BlueprintScene{}
	}
	return bp, nil
}

func collectBlueprints(rows *sql.Rows) ([]ProjectBlueprint, error) {
	out := []ProjectBlueprint{}
	for rows.Next() {
		bp, err := scanBlueprintRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, bp)
	}
	return out, rows.Err()
}

func seedsFromColumns(cols []blueprintColumn) []ColumnSeed {
	out := make([]ColumnSeed, 0, len(cols))
	for _, c := range cols {
		out = append(out, ColumnSeed{
			Name: c.Name, Position: c.Position, Lifecycle: c.Lifecycle,
			OpPrimitive: c.OpPrimitive, Instruction: c.Instruction, WhenToUse: c.WhenToUse,
		})
	}
	return out
}

func toBlueprintColumns(seeds []ColumnSeed) []blueprintColumn {
	out := make([]blueprintColumn, 0, len(seeds))
	for _, c := range seeds {
		out = append(out, blueprintColumn{
			Name: c.Name, Position: c.Position, Lifecycle: c.Lifecycle,
			OpPrimitive: c.OpPrimitive, Instruction: c.Instruction, WhenToUse: c.WhenToUse,
		})
	}
	return out
}

// builtinBlueprintDef is one seeded builtin template.
type builtinBlueprintDef struct {
	slug        string
	name        string
	description string
	columns     []ColumnSeed
	// scenes snapshots default scenes by slug; resolved per target owner at apply
	// time (best-effort, unresolved slugs silently skipped). nil = no scenes.
	scenes []BlueprintScene
}

// builtinBlueprintDefs returns the generic kanban templates seeded on first
// boot. "standard-dev" mirrors the hardcoded CreateProjectWithDefaults five-lane
// board and is the system default; the rest are lighter generic boards.
func builtinBlueprintDefs() []builtinBlueprintDef {
	return []builtinBlueprintDef{
		{
			slug:        builtinDefaultSlug,
			name:        "标准开发流程",
			description: "待办 / 实现 / AI 审查 / 人工审查 / 完成，适合大多数编码项目。",
			columns:     defaultColumnSeeds(),
		},
		{
			slug:        "simple-kanban",
			name:        "简单看板",
			description: "待办 / 进行中 / 完成 三列，最轻量的通用看板。",
			columns: []ColumnSeed{
				{Name: "待办", Position: 0, Lifecycle: "created", OpPrimitive: "none"},
				{Name: "进行中", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "推进并完成本 issue 的工作", WhenToUse: "需要开始或推进任务时"},
				{Name: "完成", Position: 2, Lifecycle: "completed", OpPrimitive: "complete"},
			},
		},
		{
			slug:        "bug-tracking",
			name:        "缺陷跟踪",
			description: "待办 / 复现 / 修复 / 验证 / 完成，面向缺陷处理流程。",
			columns: []ColumnSeed{
				{Name: "待办", Position: 0, Lifecycle: "created", OpPrimitive: "none"},
				{Name: "复现", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "稳定复现缺陷并定位根因", WhenToUse: "需要确认并复现缺陷时"},
				{Name: "修复", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "实施修复并补充必要的测试", WhenToUse: "已定位根因、需要修复时"},
				{Name: "验证", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "验证修复有效且无回归", WhenToUse: "修复完成、需要验证时"},
				{Name: "完成", Position: 4, Lifecycle: "completed", OpPrimitive: "complete"},
			},
		},
		{
			slug:        "content-creation",
			name:        "内容创作",
			description: "选题 / 撰写 / 审校 / 发布，面向写作与内容生产。",
			columns: []ColumnSeed{
				{Name: "选题", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "需要确定选题与大纲时"},
				{Name: "撰写", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "根据选题与大纲完成初稿", WhenToUse: "需要撰写正文时"},
				{Name: "审校", Position: 2, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "对初稿做事实核查、润色与校对", WhenToUse: "初稿完成、需要审校时"},
				{Name: "发布", Position: 3, Lifecycle: "completed", OpPrimitive: "complete"},
			},
		},
		// ── 规格驱动开发：OpenSpec（WHAT，规格链 propose/apply/archive）+ Superpowers ──
		// （HOW，TDD 与双阶段审查）焊成端到端可追溯流水线。两点纪律以 phase prompt 内建：
		// 意图锁 + 内容级门禁（规格设计→实现），让降级可见（AI 审查）。gate 不随蓝图快照，
		// 故以列指令的形式表达为软纪律，需硬闸时另在项目上绑定 column_gate_specs。
		{
			slug:        builtinOpenSpecSuperpowersSlug,
			name:        "规格驱动开发（OpenSpec+Superpowers）",
			description: "待办 / 规格设计 / 实现 / AI 审查 / 人工审查 / 完成，融合 OpenSpec 规格链与 Superpowers 的 TDD 与双阶段审查，内建意图锁与降级可见纪律。",
			columns: []ColumnSeed{
				{Name: "待办", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "新建需求或待分配的工作项时"},
				{Name: "规格设计", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "OpenSpec propose 阶段：先头脑风暴澄清需求，再产出 proposal / specs / tasks 规格文档。锁定意图（Intent Lock）——用一句话固化本次变更的目的与范围边界，作为后续防止范围蔓延的基准。", WhenToUse: "需求已明确、需要在写代码前把 WHAT 落成规格与意图锁时"},
				{Name: "实现", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "OpenSpec apply + Superpowers TDD：每个 task 派独立子代理，严格按 RED→GREEN→REFACTOR 落地。动工前用当前改动范围比对锁定意图（内容级门禁），发现范围蔓延先回「规格设计」更新规格与意图锁再继续。", WhenToUse: "规格与意图锁就绪、需要按 task 实现代码时"},
				{Name: "AI 审查", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "Superpowers 双阶段审查：规格合规 + 代码质量，列出问题并就地修复。额外核查「让降级可见」——每处 fallback / 回退 / 兜底路径被触发时必须留下显式信号（日志或标记），不允许静默通过；工具与检索的可用性要实测，不能凭持久化产物（磁盘目录、历史标记）推断当前会话可用。", WhenToUse: "实现完成、产出有复杂度值得严格审查时"},
				{Name: "人工审查", Position: 4, Lifecycle: "", OpPrimitive: "none", WhenToUse: "需要人工判断、利益相关方批准，或 Epic / 高风险变更进入完成前的硬门禁时"},
				{Name: "完成", Position: 5, Lifecycle: "completed", OpPrimitive: "complete", WhenToUse: "已提交并合并到目标分支、且按 OpenSpec archive 归档 delta spec 后移入"},
			},
		},
		// ── 办公类模板：看板从「写代码」泛化为「办公任务」，产出物为文档/表格/海报 ──
		// 每套快照一组默认场景（按 slug 引用，套用时在目标 owner 下解析；未命中静默跳过），
		// 让非技术用户新建项目即获得对应的办公技能与流程骨架。
		{
			slug:        "office-report-ppt",
			name:        "汇报 PPT",
			description: "素材收集 / 大纲设计 / 制作幻灯片 / 审阅修订 / 完成，面向汇报演示文稿（输出 PPT）。",
			columns: []ColumnSeed{
				{Name: "素材收集", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "收集汇报所需的数据、要点与素材时"},
				{Name: "大纲设计", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "梳理汇报逻辑，产出分页大纲与每页要点", WhenToUse: "需要规划演示文稿结构时"},
				{Name: "制作幻灯片", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "根据大纲生成 PPTX 文件，逐页填充标题、要点与图表", WhenToUse: "需要产出幻灯片文件时"},
				{Name: "审阅修订", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "检查表达、排版与数据准确性，按反馈修订", WhenToUse: "初稿完成、需要审阅时"},
				{Name: "完成", Position: 4, Lifecycle: "completed", OpPrimitive: "complete"},
			},
			scenes: []BlueprintScene{
				{Slug: "writing-studio", DisplayName: "写作舱", Source: "builtin"},
			},
		},
		{
			slug:        "office-weekly-report",
			name:        "数据周报",
			description: "数据采集 / 分析洞察 / 生成报表 / 复核 / 完成，面向周期性数据汇报（输出表格/文档）。",
			columns: []ColumnSeed{
				{Name: "数据采集", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "明确周报指标并采集本期数据时"},
				{Name: "分析洞察", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "对本期数据做环比/同比与趋势分析，提炼关键洞察", WhenToUse: "需要分析数据时"},
				{Name: "生成报表", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "生成数据周报（Excel/文档），含图表与结论摘要", WhenToUse: "需要产出周报文件时"},
				{Name: "复核", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "复核数据口径与结论准确性", WhenToUse: "报表初稿完成、需要复核时"},
				{Name: "完成", Position: 4, Lifecycle: "completed", OpPrimitive: "complete"},
			},
			scenes: []BlueprintScene{
				{Slug: "data-analysis", DisplayName: "数据分析", Source: "builtin"},
			},
		},
		{
			slug:        "office-poster",
			name:        "海报设计",
			description: "需求梳理 / 文案与构图 / 设计制作 / 评审 / 完成，面向海报与视觉物料（输出图片/PDF）。",
			columns: []ColumnSeed{
				{Name: "需求梳理", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "明确海报主题、受众与尺寸时"},
				{Name: "文案与构图", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "确定海报主标题、副文案与视觉构图方案", WhenToUse: "需要确定文案与版式时"},
				{Name: "设计制作", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "生成海报（图片/PDF），落实配色、排版与图形元素", WhenToUse: "需要产出海报成品时"},
				{Name: "评审", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "评审视觉效果与信息传达，按反馈优化", WhenToUse: "初稿完成、需要评审时"},
				{Name: "完成", Position: 4, Lifecycle: "completed", OpPrimitive: "complete"},
			},
			scenes: []BlueprintScene{
				{Slug: "media-studio", DisplayName: "媒体舱", Source: "builtin"},
			},
		},
		// ── 营销 / 运营模板：把跨职能营销任务建模为 no_repo workspace 上的阶段化看板 ──
		// 每套快照一个营销场景（curated MCP + 阶段动作 + gate 模板），套用时在目标 owner 下
		// 按 slug 解析（未命中静默跳过）。gate 不随蓝图快照，故『审校/复核』列以列指令表达
		// 为软纪律，需硬闸时在项目上绑定场景带来的「营销文案审校门禁」等 column_gate_specs。
		{
			slug:        marketingContentFlowSlug,
			name:        "内容营销全流程",
			description: "需求 / 选题·关键词 / 素材·文案 / 审校 / 发布 / 复盘，面向跨境与品牌内容营销的端到端闭环（配「内容营销全流程」场景）。",
			columns: []ColumnSeed{
				{Name: "需求", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "对齐业务目标、受众、市场/语言与内容基线时"},
				{Name: "选题/关键词", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "做选题与关键词研究、竞品内容差距分析：定选题、扩关键词、找差异化机会。搜索量/竞争度以接口或抓取为准，抓不到就标注，不臆造数字。", WhenToUse: "需求已对齐、需要定选题与关键词时"},
				{Name: "素材/文案", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "根据选题与大纲产出结构完整、事实可溯源的文案初稿，附 meta 与社媒分发短文案。不堆砌关键词、不编造数据/案例，缺一手素材就标占位并索取。", WhenToUse: "选题与关键词就绪、需要产出稿件时"},
				{Name: "审校", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "发布前审校门禁：逐项核查事实与可溯源、合规（不夸大/不违规）、品牌一致、SEO/GEO、可读性，列出「必须改」为硬性阻塞项。这是内容级门禁——存在无法溯源的关键断言或明显合规风险即不进入发布；需硬闸时在本列绑定场景提供的「营销文案审校门禁」gate。", WhenToUse: "初稿完成、需要发布前把关时"},
				{Name: "发布", Position: 4, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "核对可发布性清单（最终标题/meta、结构化数据、内外链、图片 alt、utm 追踪、canonical/robots、移动端与速度），落地页可跑 site-audit 技术审计，给出「可发布 / 待修复」结论。", WhenToUse: "审校通过、准备发布时"},
				{Name: "复盘", Position: 5, Lifecycle: "completed", OpPrimitive: "complete", WhenToUse: "内容已发布、拉搜索/排名/AI 引用表现做效果复盘并沉淀迭代方向后移入（可挂 cron 周期跑）"},
			},
			scenes: []BlueprintScene{
				{Slug: "content-marketing", DisplayName: "内容营销全流程", Source: "builtin"},
			},
		},
		{
			slug:        marketingSocialOpsSlug,
			name:        "社媒运营周报",
			description: "规划 / 排期 / 数据采集 / 周报撰写 / 复核 / 完成，面向社媒运营的周期化闭环，主打周报类定时任务（配「社媒运营周报」场景）。",
			columns: []ColumnSeed{
				{Name: "规划", Position: 0, Lifecycle: "created", OpPrimitive: "none", WhenToUse: "结合运营目标与热点排本期内容日历时"},
				{Name: "排期", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "把内容日历落成逐条可执行的发文排期与平台适配文案，跨平台差异化改写而非复制，标注需协作/需确认项。文案符合平台调性与广告合规。", WhenToUse: "内容日历已定、需要排期与写文案时"},
				{Name: "数据采集", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "归并各平台/投放数据成结构化本期快照（平台×指标），标注口径与来源。平台原生数字需 API 或用户导出，取不到就标「待补」并引导用户提供，不臆造社媒数字。", WhenToUse: "本期结束、需要采集运营数据时"},
				{Name: "周报撰写", Position: 3, Lifecycle: "implement", OpPrimitive: "instruct", Instruction: "基于采集快照生成运营周报：概览+环比 → 分平台表现 → 爆款拆解 → 问题诊断 → 下期建议，结论配下一步动作。数字全部来自快照、环比清晰、缺失标「待补」。这是核心定时产物，可由 cron 触发自动生成并建 issue / 通知。", WhenToUse: "数据采集完成、需要产出周报时"},
				{Name: "复核", Position: 4, Lifecycle: "implement-review", OpPrimitive: "instruct", Instruction: "复核数据口径、结论准确性与合规表述后定稿，登记为交付物。", WhenToUse: "周报初稿完成、需要复核时"},
				{Name: "完成", Position: 5, Lifecycle: "completed", OpPrimitive: "complete", WhenToUse: "周报已定稿并交付/推送、沉淀爆款方向与下期计划后移入"},
			},
			scenes: []BlueprintScene{
				{Slug: "social-ops", DisplayName: "社媒运营周报", Source: "builtin"},
			},
		},
		// ── 运营闭环模板（通用能力落地）：把各运营平台建模为 no_repo workspace 上的阶段化 ──
		// 闭环看板（采集/选题→撰稿→排版→审校门禁→发布→数据复盘·回流），由 ops_loop.go 的
		// 共享 opsLoopColumns 骨架按平台参数生成——微信公众号 + 内容营销 复用【同一】实现，
		// 非各写一份。每套快照对应运营场景（curated MCP + 阶段动作 + 发布前审校 gate），套用时
		// 在目标 owner 下按 slug 解析（未命中静默跳过）。gate 不随蓝图快照，故『审校』列以列指令
		// 表达为软纪律，需硬闸时在项目上绑定场景带来的「发布前审校门禁」gate。
		opsLoopBlueprintDef(opsLoopPlatforms()[0], "采集·选题 / 撰稿 / 排版 / 审校 / 发布·定时 / 数据复盘·回流，面向微信公众号运营的端到端闭环（配「微信公众号运营」官方 API 场景）。"),
		opsLoopBlueprintDef(opsLoopPlatforms()[1], "采集·选题 / 撰稿 / 排版 / 审校 / 发布 / 数据复盘·回流，面向内容营销的端到端闭环（配「内容营销全流程」场景，与线性版 content-marketing-flow 互补）。"),
	}
}
