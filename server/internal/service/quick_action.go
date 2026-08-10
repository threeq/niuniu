package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// SceneQuickAction is a quick action contributed by the workspace's active
// scene(s). It is parsed live from the cached scene projection and is never
// persisted to the quick_actions table, so it carries no DB id — the scene
// slug is its stable identity.
type SceneQuickAction struct {
	Slug    string
	Label   string
	Content string
}

type QuickActionService struct {
	q     *store.Queries
	db    *store.DB
	authz *Authz
}

func NewQuickActionService(q *store.Queries, db *sql.DB, authz *Authz) *QuickActionService {
	return &QuickActionService{q: q, db: store.Wrap(db), authz: authz}
}

func (s *QuickActionService) List(ctx context.Context) ([]store.QuickAction, error) {
	return s.q.ListQuickActions(ctx)
}

// ListForUser returns quick actions accessible to userID (personal + org memberships).
func (s *QuickActionService) ListForUser(ctx context.Context, userID int64) ([]store.QuickAction, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListQuickActionsForOwners(ctx, store.ListQuickActionsForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

func (s *QuickActionService) Get(ctx context.Context, id int64) (store.QuickAction, error) {
	return s.q.GetQuickAction(ctx, id)
}

// ListSceneQuickActionsForWorkspace returns the quick actions contributed by the
// workspace's active scene(s), parsed live from the cached projection. These are
// never stored in the quick_actions table. Returns an empty slice when no
// projection has been computed yet (e.g. workspace with no scene layers).
func (s *QuickActionService) ListSceneQuickActionsForWorkspace(ctx context.Context, wsID int64) ([]SceneQuickAction, error) {
	row, err := s.q.GetProjection(ctx, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var proj Projection
	if err := json.Unmarshal([]byte(row.ProjectedDefinition), &proj); err != nil {
		slog.Warn("scene quick actions: bad projection json", "workspace_id", wsID, "err", err)
		return nil, nil
	}
	out := make([]SceneQuickAction, 0, len(proj.Assets.QuickActions))
	for _, qa := range proj.Assets.QuickActions {
		out = append(out, SceneQuickAction{Slug: qa.Slug, Label: qa.Label, Content: qa.Prompt})
	}
	return out, nil
}

func (s *QuickActionService) Create(ctx context.Context, label, content string, autoSend bool, ownerType string, ownerID int64) (store.QuickAction, error) {
	var autoSendInt int64
	if autoSend {
		autoSendInt = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.QuickAction{}, err
	}
	defer tx.Rollback()

	qtx := tx.Queries()

	actions, err := qtx.ListQuickActions(ctx)
	if err != nil {
		return store.QuickAction{}, err
	}

	qa, err := qtx.CreateQuickAction(ctx, store.CreateQuickActionParams{
		Label:     label,
		Content:   content,
		AutoSend:  autoSendInt,
		Position:  int64(len(actions)),
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
	if err != nil {
		return store.QuickAction{}, err
	}

	return qa, tx.Commit()
}

// studioQuickActionDefaults are the owner-level (user/0 sentinel) presets that
// make generic "studio" tasks one-click (issue #234). They drive the AI to run
// git/dev workflows via Bash. Most auto-send; 放弃改动 (destructive) does not, so
// the user sees the file list in the input box before confirming.
//
// Idempotency is keyed on `slug` (see SeedDefaults), so editing a label/content
// here only affects FRESH installs — existing rows are never overwritten on
// upgrade. New entries appended to this slice get seeded on the next start.
// Slice order is the seed order (positions assigned via MAX(position)+1).
var studioQuickActionDefaults = []struct {
	slug     string
	label    string
	content  string
	autoSend int64
}{
	{
		slug:     "studio-deliver",
		label:    "交付（落回主目录）",
		content:  "把当前分支 merge 回主分支，让改动写回原始目录。如果遇到冲突，请停下来把冲突情况报告给我，不要强行解决，也不要丢弃任何改动。不允许 ff 模式，消息格式需要包含：Merge branch A into B：<摘要描述>。",
		autoSend: 1,
	},
	{
		slug:     "studio-save",
		label:    "保存工作",
		content:  "提交并保存当前所有改动。commit message 由你拟定，用简洁的中文概括这次改动。",
		autoSend: 1,
	},
	{
		slug:     "studio-continue",
		label:    "同意继续工作",
		content:  "OK，同意，继续工作。Subagent-Driven (recommended)",
		autoSend: 1,
	},
	{
		slug:     "studio-discard",
		label:    "放弃改动",
		content:  "丢弃工作区中未保存的改动，回到上次保存的状态。执行前请先列出将要被丢弃的文件清单，等我确认后再执行。",
		autoSend: 0,
	},
	{
		slug:     "studio-sync",
		label:    "同步外部产出到本工作空间",
		content:  "合并 main 分支最新提交到当前 worktree",
		autoSend: 1,
	},
	{
		slug:     "studio-review",
		label:    "审查工作",
		content:  "code reviewer and fixed",
		autoSend: 1,
	},
	{
		slug:     "studio-autopilot",
		label:    "自主完成工作",
		content:  "开发自动化，按照以下流程完成开发工作：\n1、架构评审，根据规则自动完成方案设计与决策；\n2、subagent driven development，尽量启动多个 agent 并行开发；\n3、请资深开发完成 code review 和 bug fix；\n4、完成测试和 bug fix；\n5、输出工作总结，包含：需求完成情况、重要技术决策等。\n\n决策规则：\n1. 当遇到需要多个方案选择，请根据产品最佳实践自动选择推荐方案；\n2. 当遇到高危决策的时候需要停下来询问我，比如：系统可能崩溃且无规避方案、用户资金安全会影响等。",
		autoSend: 1,
	},
}

// SeedDefaults inserts the studio quick-action presets under the global
// (owner_type='user', owner_id=0) sentinel if absent. Idempotent: keyed on slug.
// Mirrors EnvPresetService.SeedDefaults; surfaced to all callers via
// ListQuickActionsForOwners. Raw SQL goes through the *store.DB wrapper so `?`
// placeholders are rewritten for Postgres; every `?` anchors a typed column.
func (s *QuickActionService) SeedDefaults(ctx context.Context) error {
	for _, d := range studioQuickActionDefaults {
		var existingID int64
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM quick_actions WHERE owner_type = 'user' AND owner_id = 0 AND slug = ?`,
			d.slug).Scan(&existingID)
		if err == nil {
			continue // already seeded
		}
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("seed quick action: lookup failed", "slug", d.slug, "err", err)
			continue
		}
		var nextPos int64
		_ = s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position), 0) + 1 FROM quick_actions WHERE owner_type = 'user' AND owner_id = 0`).
			Scan(&nextPos)
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO quick_actions (label, content, auto_send, position, owner_type, owner_id, slug)
			   VALUES (?, ?, ?, ?, 'user', 0, ?)`,
			d.label, d.content, d.autoSend, nextPos, d.slug); err != nil {
			slog.Warn("seed quick action: insert failed", "slug", d.slug, "err", err)
		}
	}
	return nil
}

func (s *QuickActionService) Update(ctx context.Context, id int64, label, content string, autoSend bool) (store.QuickAction, error) {
	var autoSendInt int64
	if autoSend {
		autoSendInt = 1
	}
	return s.q.UpdateQuickAction(ctx, store.UpdateQuickActionParams{
		ID:       id,
		Label:    label,
		Content:  content,
		AutoSend: autoSendInt,
	})
}

func (s *QuickActionService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteQuickAction(ctx, id)
}

func (s *QuickActionService) Reorder(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qtx := tx.Queries()
	for i, id := range ids {
		if err := qtx.UpdateQuickActionPosition(ctx, store.UpdateQuickActionPositionParams{
			ID:       id,
			Position: int64(i),
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
