// Package service: OnboardingSeeder — first-run "open and go" seed for the
// personal / single-user edition.
//
// A fresh personal install ships with an empty board. For a non-technical user
// that strands them at "create project → create workspace → pick scene" before
// they can run anything. To make an office task runnable out of the box we seed
// ONE ready-to-use office project whose default scene is `office-doc`, so any
// (no-repo) workspace created under it auto-attaches the docx/xlsx/pptx/pdf
// skills via the existing project_default_scenes auto-mount path.
//
// Design: docs/superpowers/specs/2026-06-14-personal-local-sandbox-hardening-design.md.
//
// This reuses existing machinery only (KanbanService.CreateProjectWithDefaults +
// ProjectBlueprintService.AttachScenesToProject) — no new table, no DDL.
package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Onboarding seed content. Kept as constants (not config) — this is a fixed
// first-run convenience, not a tunable. The strings are user-visible in the
// personal edition SPA.
const (
	onboardingProjectName = "牛牛助手"
	onboardingProjectDesc = "开箱即用的办公工作区：在这里新建一个工作空间（无需代码仓库），用一句话生成 Word / Excel / PPT / PDF。默认已挂载「办公文档」场景。"
	onboardingSceneSlug   = "office-doc"
	onboardingIssueTitle  = "试试：一句话生成一份周报"
	onboardingIssueDesc   = "在本项目下新建一个工作空间（创建时无需选择代码仓库），然后对 AI 说一句话，例如：「帮我生成一份本周工作周报的 Word 文档，包含本周完成、下周计划、风险三部分」。AI 会用 docx 技能直接产出 .docx，文件落在工作空间目录内。"
	onboardingIssueGoal   = "用户在该工作空间内通过一句话需求成功生成了一份办公文档（如 .docx/.xlsx/.pptx），文件落在工作空间目录内且可正常打开。"
)

// OnboardingSeeder seeds the first-run office project. Idempotent + non-fatal:
// it skips when disabled (team edition), when no local owner is resolved, or
// when the owner already has any project; any individual failure is logged and
// never blocks boot.
type OnboardingSeeder struct {
	db        *store.DB
	kanban    *KanbanService
	blueprint *ProjectBlueprintService
	owner     OwnerRef
	enabled   bool
}

// NewOnboardingSeeder constructs the seeder. enabled should be true only for
// the personal / single-user edition (cfg.Auth.Enabled == false); owner is the
// resolved local owner that will hold the seeded project.
func NewOnboardingSeeder(db *sql.DB, kanban *KanbanService, blueprint *ProjectBlueprintService, enabled bool, owner OwnerRef) *OnboardingSeeder {
	return &OnboardingSeeder{
		db:        store.Wrap(db),
		kanban:    kanban,
		blueprint: blueprint,
		owner:     owner,
		enabled:   enabled,
	}
}

// Run performs the one-time seed. Safe to call on every boot.
func (s *OnboardingSeeder) Run(ctx context.Context) error {
	if !s.enabled || s.kanban == nil || s.owner.Type == "" || s.owner.ID <= 0 {
		return nil
	}

	// Gate: only seed when this owner has no projects yet. Keeps the seed a
	// genuine first-run convenience; a user who later deletes everything can
	// start clean (we only re-seed once they are back to zero projects).
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE owner_type = ? AND owner_id = ?`,
		s.owner.Type, s.owner.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	project, err := s.kanban.CreateProjectWithDefaults(ctx, onboardingProjectName, onboardingProjectDesc, s.owner.Type, s.owner.ID)
	if err != nil {
		if errors.Is(err, ErrProjectNameExists) {
			return nil // this owner already has the project (raced) — leave it
		}
		return err
	}

	// Attach office-doc as the project's default scene so workspaces created
	// here auto-mount the docx/xlsx/pptx/pdf skills. Non-fatal: the project is
	// usable without it and the scene can be added via the default-scenes UI.
	if s.blueprint != nil {
		if err := s.blueprint.AttachScenesToProject(ctx, project.ID, s.owner,
			[]BlueprintScene{{Slug: onboardingSceneSlug}}); err != nil {
			slog.Warn("onboarding seeder: attach default scene failed", "project", project.ID, "error", err)
		}
	}

	// Seed one guide issue in the first column so the board isn't empty and the
	// user has a concrete first task to run. Non-fatal.
	if err := s.seedGuideIssue(ctx, project.ID); err != nil {
		slog.Warn("onboarding seeder: seed guide issue failed", "project", project.ID, "error", err)
	}

	slog.Info("onboarding seeder: seeded office project",
		"project", project.ID, "owner_type", s.owner.Type, "owner_id", s.owner.ID)
	return nil
}

// seedGuideIssue inserts the starter issue into the project's first column. Uses
// a single driver-aware INSERT (the kanban CreateIssue signature is heavy and
// has no goal_condition param); notify/activity side effects are unnecessary for
// a boot-time seed.
func (s *OnboardingSeeder) seedGuideIssue(ctx context.Context, projectID int64) error {
	var colID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM columns WHERE project_id = ? ORDER BY position LIMIT 1`,
		projectID).Scan(&colID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO issues (column_id, title, description, position, goal_condition) VALUES (?, ?, ?, 0, ?)`,
		colID, onboardingIssueTitle, onboardingIssueDesc, onboardingIssueGoal)
	return err
}
