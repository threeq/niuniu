package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// boardTestEnv is a minimal harness for the (unexported) board-menu builder.
type boardTestEnv struct {
	svc *EpicExecutionService
	db  *sql.DB
	q   *store.Queries
	ctx context.Context
}

func setupBoardTest(t *testing.T) *boardTestEnv {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)

	q := store.New(db)
	activity := NewIssueActivityService(q)
	kanban := NewKanbanService(db, q, activity, nil, nil)
	svc := NewEpicExecutionService(db, q, kanban, nil, nil)
	return &boardTestEnv{svc: svc, db: db, q: q, ctx: context.Background()}
}

func (e *boardTestEnv) addColumn(t *testing.T, projectID int64, name string, pos int64, op, whenToUse, instruction string) int64 {
	t.Helper()
	col, err := e.q.CreateColumn(e.ctx, store.CreateColumnParams{ProjectID: projectID, Name: name, Position: pos})
	require.NoError(t, err)
	_, err = e.db.ExecContext(e.ctx,
		`UPDATE columns SET op_primitive = ?, when_to_use = ?, phase_prompt = ? WHERE id = ?`,
		op, whenToUse, instruction, col.ID)
	require.NoError(t, err)
	return col.ID
}

func (e *boardTestEnv) bindSpec(t *testing.T, columnID int64, name, severity, applicability string) {
	t.Helper()
	res, err := e.db.ExecContext(e.ctx,
		`INSERT INTO harness_specs (category, name, enabled, severity, config)
		 VALUES ('quality',?,1,?, '')`, name, severity)
	require.NoError(t, err)
	specID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = e.db.ExecContext(e.ctx,
		`INSERT INTO column_gate_specs (column_id, spec_id, position, applicability) VALUES (?, ?, 0, ?)`,
		columnID, specID, applicability)
	require.NoError(t, err)
}

func TestScoreColumnRelevance(t *testing.T) {
	// CJK matches per character; an unrelated column scores 0.
	assert.Greater(t, scoreColumnRelevance("登录鉴权", "实现登录功能用 JWT"), 0)
	assert.Equal(t, 0, scoreColumnRelevance("部署运维", "实现登录功能用 JWT"))
	// ASCII words match case-insensitively (len>=2).
	assert.Greater(t, scoreColumnRelevance("review the code", "please REVIEW this change"), 0)
}

func TestBuildBoardMenu_RendersStagesFloorAndSkipsNone(t *testing.T) {
	e := setupBoardTest(t)
	p, err := e.q.CreateProject(e.ctx, store.CreateProjectParams{Name: "board", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	backlog := e.addColumn(t, p.ID, "待办", 0, "none", "", "")
	impl := e.addColumn(t, p.ID, "实现", 1, "instruct", "需要写或改代码时", "实现本 issue 的需求改动")
	e.addColumn(t, p.ID, "审查", 2, "instruct", "改动有复杂度值得严格 review 时", "严格 review 并就地修复")
	// none-primitive human-review lane WITH a when_to_use hint: must appear in the
	// menu so the AI can escalate into it via advance_issue.
	e.addColumn(t, p.ID, "人工审查", 3, "none", "需要人工判断或利益相关方批准时", "")
	done := e.addColumn(t, p.ID, "完成", 4, "complete", "", "")
	_ = done
	e.bindSpec(t, impl, "linter", "warning", "if_routed")
	e.bindSpec(t, done, "build-test-pass", "error", "always")

	issue, err := e.q.CreateIssue(e.ctx, store.CreateIssueParams{ColumnID: impl, Title: "加登录", Position: 0})
	require.NoError(t, err)

	section, stats, err := e.svc.buildBoardMenu(e.ctx, p.ID, impl, issue)
	require.NoError(t, err)

	assert.Contains(t, section, "实现 :")
	assert.Contains(t, section, "审查 :")
	assert.Contains(t, section, "人工审查 :")                  // none lane WITH when_to_use rendered
	assert.Contains(t, section, "需要人工判断或利益相关方批准时") // its when_to_use hint
	assert.Contains(t, section, "完成 :")            // complete column rendered
	assert.Contains(t, section, "存在未提交改动时该移动会被拒绝") // uncommitted-code rule on the complete line
	assert.Contains(t, section, "linter(warning)") // if_routed spec on its column
	assert.Contains(t, section, "build-test-pass(error)")
	assert.Contains(t, section, "advance_issue")
	// The autohost sentinel guidance — including the 高效少步 working principle — is
	// rendered as its own section, NOT mixed into the board column menu (see
	// TestRenderAutohostSentinel).
	assert.NotContains(t, section, "[AUTOHOST_DONE]")
	assert.NotContains(t, section, "高效少步")
	assert.NotContains(t, section, "待办") // none lane WITHOUT when_to_use excluded

	assert.Equal(t, 3, stats.totalStages) // 实现 + 审查 + 人工审查 (待办 excluded, 完成 separate)
	assert.Equal(t, 3, stats.includedStages)
	assert.Greater(t, stats.estTokens, 0)
	assert.NotContains(t, section, "相关性裁剪") // nothing pruned
	_ = backlog
}

func TestRenderAutohostSentinel(t *testing.T) {
	// With a goal_condition: the concrete goal is spelled out so the agent knows
	// what "整体目标" means instead of self-judging against an unstated goal.
	section := renderAutohostSentinel("登录接口返回 200 且 e2e 通过")
	assert.Contains(t, section, "## 自动托管")
	assert.Contains(t, section, "[AUTOHOST_DONE]")
	assert.Contains(t, section, "整体目标")
	assert.Contains(t, section, "登录接口返回 200 且 e2e 通过") // goal echoed verbatim
	assert.Contains(t, section, "本节独立于上面的看板流程")
	assert.Contains(t, section, "高效少步") // moved here from the board menu
	// It must NOT carry any board-menu content (that lives in renderBoardMenu).
	assert.NotContains(t, section, "看板处理阶段")
	assert.NotContains(t, section, "advance_issue")

	// Without a goal_condition: falls back to the issue title/description, no empty
	// quote block.
	fallback := renderAutohostSentinel("")
	assert.Contains(t, fallback, "以本 issue 标题与描述所定义的需求为准")
}

func TestBuildBoardMenu_PrunesByColumnBudgetKeepsEntry(t *testing.T) {
	e := setupBoardTest(t)
	p, err := e.q.CreateProject(e.ctx, store.CreateProjectParams{Name: "big", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	// One clearly-relevant entry column + many irrelevant instruct columns (> budget).
	entry := e.addColumn(t, p.ID, "实现", 0, "instruct", "实现登录鉴权改代码", "写代码")
	for i := 0; i < defaultBoardMenuMaxColumns+5; i++ {
		e.addColumn(t, p.ID, "杂项阶段"+strings.Repeat("x", i+1), int64(i+1), "instruct", "与本任务无关的运维部署排程", "无关")
	}
	issue, err := e.q.CreateIssue(e.ctx, store.CreateIssueParams{ColumnID: entry, Title: "实现登录", Position: 0})
	require.NoError(t, err)

	section, stats, err := e.svc.buildBoardMenu(e.ctx, p.ID, entry, issue)
	require.NoError(t, err)

	assert.LessOrEqual(t, stats.includedStages, defaultBoardMenuMaxColumns)
	assert.Less(t, stats.includedStages, stats.totalStages)
	assert.Contains(t, section, "实现 :", "entry column must always be kept")
	assert.Contains(t, section, "相关性裁剪", "pruning note shown when columns dropped")
}
