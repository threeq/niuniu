package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AI-native board execution, stage 5 (spec 2026-06-05-ai-native-board-execution
// -design.md §6/§23.4): inject the project's columns as an unordered "menu" into
// the per-issue agent's instruction file, pruned by relevance to the issue and
// capped by a column/char budget (a token-cost estimate is the §16 seam).
//
// op_primitive / when_to_use / phase_prompt are migrate-only (not in sqlc), so
// they are read with anchored raw SQL (project_id = ?), PG-safe (no 42P18).

const (
	// defaultBoardMenuMaxColumns caps how many `instruct` columns are injected, so
	// a large board (15+ columns) does not blow up the prompt (§23.4).
	defaultBoardMenuMaxColumns = 12
	// boardMenuMaxChars is the hard character budget for the rendered menu section.
	// Over budget, the lowest-relevance instruct lines are dropped (entry column kept).
	boardMenuMaxChars = 4000
)

// boardColumn is one column's AI-native menu fields plus its bound gate specs.
type boardColumn struct {
	id          int64
	name        string
	op          string // none | instruct | complete
	whenToUse   string // routing heuristic (AI-generated, editable); may be empty
	instruction string // phase_prompt (op_instruction); may be empty
	position    int64
	routedSpecs []string // applicability='if_routed' specs, "name(severity)"
	floorSpecs  []string // applicability='always' specs, "name(severity)"
}

// boardMenuStats reports what the menu builder produced, for cost accounting.
type boardMenuStats struct {
	totalStages    int // routable stage columns on the board (instruct + none-with-when_to_use)
	includedStages int // stage columns actually injected (after pruning/budget)
	chars          int // rendered section length
	estTokens      int // rough token estimate (chars/4); §16 chain-budget seam
}

// listBoardColumns reads every column of a project with its AI-native fields and
// its bound gate specs (partitioned into if_routed / always). Raw SQL: the AI-native
// columns are migrate-only and not modelled in sqlc (stage-1a/4 convention).
func (s *EpicExecutionService) listBoardColumns(ctx context.Context, projectID int64) ([]boardColumn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, op_primitive, COALESCE(when_to_use, ''), COALESCE(phase_prompt, ''), position
		 FROM columns WHERE project_id = ? ORDER BY position`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list board columns: %w", err)
	}
	var cols []boardColumn
	for rows.Next() {
		var c boardColumn
		if err := rows.Scan(&c.id, &c.name, &c.op, &c.whenToUse, &c.instruction, &c.position); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan board column: %w", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Close the first result set before opening the second: a single-connection
	// SQLite pool (test default) cannot hold two open result sets at once.
	rows.Close()

	byID := make(map[int64]*boardColumn, len(cols))
	for i := range cols {
		byID[cols[i].id] = &cols[i]
	}

	specRows, err := s.db.QueryContext(ctx, `
		SELECT cgs.column_id, hs.name, hs.severity, cgs.applicability
		FROM column_gate_specs cgs
		JOIN columns c ON c.id = cgs.column_id
		JOIN harness_specs hs ON hs.id = cgs.spec_id
		WHERE c.project_id = ? AND hs.enabled = 1
		ORDER BY cgs.column_id, cgs.position`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list board specs: %w", err)
	}
	defer specRows.Close()
	for specRows.Next() {
		var colID int64
		var name, severity, applicability string
		if err := specRows.Scan(&colID, &name, &severity, &applicability); err != nil {
			return nil, fmt.Errorf("scan board spec: %w", err)
		}
		c, ok := byID[colID]
		if !ok {
			continue
		}
		label := fmt.Sprintf("%s(%s)", name, severity)
		if applicability == "always" {
			c.floorSpecs = append(c.floorSpecs, label)
		} else {
			c.routedSpecs = append(c.routedSpecs, label)
		}
	}
	return cols, specRows.Err()
}

// tokenize splits text into a set of comparable tokens: CJK (Han) characters are
// taken per-character (no word boundaries in Chinese), ASCII/Latin runs of length
// >=2 are taken as lowercased words. Punctuation and single Latin letters are
// dropped as noise.
func tokenize(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var word []rune
	flush := func() {
		if len(word) >= 2 {
			out[string(word)] = struct{}{}
		}
		word = word[:0]
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			out[string(r)] = struct{}{}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// scoreColumnRelevance counts how many of the column's tokens appear in the issue
// text — a cheap, deterministic relevance heuristic (no LLM, §23.4).
func scoreColumnRelevance(colText, issueText string) int {
	issueToks := tokenize(issueText)
	n := 0
	for t := range tokenize(colText) {
		if _, ok := issueToks[t]; ok {
			n++
		}
	}
	return n
}

// buildBoardMenu renders the pruned, budget-capped board menu section for an issue.
// instruct columns are scored by relevance to the issue, the entry column is always
// kept, complete columns and the project floor (always) specs are always shown.
func (s *EpicExecutionService) buildBoardMenu(ctx context.Context, projectID, entryColumnID int64, issue store.Issue) (string, boardMenuStats, error) {
	cols, err := s.listBoardColumns(ctx, projectID)
	if err != nil {
		return "", boardMenuStats{}, err
	}
	issueText := issue.Title + " " + nullStringVal(issue.Description)

	var stageCols, completeCols []boardColumn
	floorSeen := make(map[string]struct{})
	var floorSpecs []string
	for _, c := range cols {
		for _, fs := range c.floorSpecs {
			if _, ok := floorSeen[fs]; !ok {
				floorSeen[fs] = struct{}{}
				floorSpecs = append(floorSpecs, fs)
			}
		}
		switch {
		case c.op == "complete":
			completeCols = append(completeCols, c)
		case c.op == "instruct" || strings.TrimSpace(c.whenToUse) != "":
			// Any column the AI can route into: instruct lanes, plus none-primitive
			// lanes that carry a when_to_use hint (e.g. 人工审查 — a human-review
			// parking lane the AI should be able to escalate into). Pure parking
			// lanes with no when_to_use (e.g. 待办) stay out of the menu.
			stageCols = append(stageCols, c)
		}
	}
	stats := boardMenuStats{totalStages: len(stageCols)}

	// Priority order: entry column first, then by relevance desc, then board order.
	type scored struct {
		c     boardColumn
		score int
		entry bool
	}
	ranked := make([]scored, 0, len(stageCols))
	for _, c := range stageCols {
		colText := c.name + " " + c.whenToUse + " " + c.instruction
		ranked = append(ranked, scored{c: c, score: scoreColumnRelevance(colText, issueText), entry: c.id == entryColumnID})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].entry != ranked[j].entry {
			return ranked[i].entry
		}
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].c.position < ranked[j].c.position
	})

	// Apply the column-count budget (entry column is exempt so it is never dropped).
	priority := make([]boardColumn, 0, len(ranked))
	for _, x := range ranked {
		if len(priority) >= defaultBoardMenuMaxColumns && !x.entry {
			continue
		}
		priority = append(priority, x.c)
	}

	// Apply the char budget: render, and while over budget drop the lowest-priority
	// instruct column (the tail of `priority`; the entry column is at the head).
	for {
		display := append([]boardColumn(nil), priority...)
		sort.SliceStable(display, func(i, j int) bool { return display[i].position < display[j].position })
		section := renderBoardMenu(display, completeCols, floorSpecs, len(priority) < stats.totalStages)
		if len(section) <= boardMenuMaxChars || len(priority) <= 1 {
			stats.includedStages = len(priority)
			stats.chars = len(section)
			stats.estTokens = len(section) / 4
			return section, stats, nil
		}
		priority = priority[:len(priority)-1]
	}
}

// renderBoardMenu renders the menu markdown (spec §6 shape): an unordered list of
// processing stages, the floor (底线) line, and the routing instruction.
func renderBoardMenu(stages, complete []boardColumn, floorSpecs []string, pruned bool) string {
	var b strings.Builder
	b.WriteString("## 看板处理阶段（菜单，非有序流程）\n\n")
	b.WriteString("本看板可用处理阶段（按需选用，不必全走、不必按板上顺序）：\n")
	for _, c := range stages {
		b.WriteString("- ")
		b.WriteString(c.name)
		b.WriteString(" : ")
		hint := strings.TrimSpace(c.whenToUse)
		if hint == "" {
			hint = strings.TrimSpace(c.instruction)
		}
		if hint == "" {
			hint = "（无说明）"
		}
		b.WriteString(hint)
		if len(c.routedSpecs) > 0 {
			b.WriteString("。工程规范[")
			b.WriteString(strings.Join(c.routedSpecs, ", "))
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	for _, c := range complete {
		b.WriteString("- ")
		b.WriteString(c.name)
		b.WriteString(" : 收尾（达成 goal_condition 后用 advance_issue 移到此列）。移入前必须已提交全部代码改动并按工作流合并到目标分支；工作空间存在未提交改动时该移动会被拒绝。\n")
	}
	if len(floorSpecs) > 0 {
		b.WriteString("【底线工程规范，无论怎么走，完成前必须全绿】: ")
		b.WriteString(strings.Join(floorSpecs, ", "))
		b.WriteString("\n")
	}
	b.WriteString("请根据本 issue 具体情况，自行决定经过哪些阶段、顺序、各阶段哪些工程规范适用；用 ")
	b.WriteString("`advance_issue(issue_id, to_column)` 自报进度（允许跳列/回退）。底线不可绕过。\n")
	if pruned {
		b.WriteString("（菜单已按与本 issue 的相关性裁剪，仅列出候选阶段；完整列表见看板。）\n")
	}
	return b.String()
}

// renderAutohostSentinel renders the "[AUTOHOST_DONE]" early-completion guidance
// as its own "## 自动托管" section, semantically separate from the board column
// menu (renderBoardMenu): the sentinel governs whether the autohost watchdog stops
// the current turn, a concern orthogonal to how the agent routes across columns.
// It is appended after the menu inside the same injected BOARD block (rather than
// in a second HTML-comment marker pair) so the two sections never collapse onto one
// rendered line at the marker seam.
//
// goalCondition is the per-issue completion criterion (issues.goal_condition); when
// present it is spelled out so the agent knows what "整体目标" concretely means
// instead of being told to self-judge against an unstated goal.
//
// 标记位与 agentproxy.autohostStopSentinel（"[AUTOHOST_DONE]"）保持一致。把该提示放进
// CLAUDE.md，agent 在【第一轮】完成整体任务时就能自报收尾，看门狗当轮即停，省去一次仅为
// 补发标记位而存在的自动续跑确认轮（autohostMaybeContinue 每轮都检测该标记）。严格限定在
// “整体目标达成”才输出，避免按单步完成而提前误停。
func renderAutohostSentinel(goalCondition string) string {
	var b strings.Builder
	b.WriteString("## 自动托管 · 提前收尾标记\n\n")
	b.WriteString("> 本节独立于上面的看板流程，仅决定【自动托管看门狗是否当轮停止】，与看板列如何流转无关。\n\n")

	b.WriteString("**高效少步**：用最少的必要步骤达成下面的整体目标——行动前先想清楚路径，避免无关探索、重复读取和冗长输出，控制 token 消耗；简单任务直接做完，不要走多余阶段。\n\n")

	b.WriteString("**本 issue 的整体目标（达成判据）**\n\n")
	if goal := strings.TrimSpace(goalCondition); goal != "" {
		b.WriteString("> ")
		b.WriteString(strings.ReplaceAll(goal, "\n", "\n> "))
		b.WriteString("\n\n")
	} else {
		b.WriteString("> 以本 issue 标题与描述所定义的需求为准（本 issue 未单独配置 goal_condition）。\n\n")
	}

	b.WriteString("**输出收尾标记的条件**（必须同时满足）：\n\n")
	b.WriteString("1. 上述整体目标已【真正全部】达成——不是仅完成某个步骤或中间阶段。\n")
	b.WriteString("2. 已按看板「完成」列要求把本 issue 收尾移入完成列（含提交全部代码改动并合并到目标分支）。\n\n")

	b.WriteString("**两条都满足时**：在本轮回复的最末尾，【单独一行】输出下面这一行（前后不要有其它字符）：\n\n")
	b.WriteString("```\n[AUTOHOST_DONE]\n```\n\n")
	b.WriteString("系统据此当轮立即停止，省去多余的自动续跑确认轮。\n\n")
	b.WriteString("**只要有一条未满足**：请继续推进，【切勿】输出该标记。\n")
	return b.String()
}

// injectBoardMenu writes the issue's pruned board menu into the workspace's CLI
// instruction file (best-effort: a missing path / write error is logged, never
// fatal). The estimated token cost is logged as the §16 chain-budget seam.
func (s *EpicExecutionService) injectBoardMenu(ctx context.Context, ws store.Workspace, issue store.Issue) {
	if s.db == nil || ws.Path == "" || !ws.IssueID.Valid {
		return
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		slog.Warn("board menu: load issue column", "workspaceID", ws.ID, "error", err)
		return
	}
	section, stats, err := s.buildBoardMenu(ctx, col.ProjectID, issue.ColumnID, issue)
	if err != nil {
		slog.Warn("board menu: build", "workspaceID", ws.ID, "error", err)
		return
	}
	if strings.TrimSpace(section) == "" {
		return
	}
	// Append the autohost sentinel as its own "## 自动托管" section within the same
	// injected BOARD block. It stays semantically separate from the column menu (own
	// heading, explicit "本节独立于看板流程" note) but shares one marker pair, so the
	// two never collapse onto a single rendered line at a BOARD:END/AUTOHOST:START seam.
	section += "\n" + renderAutohostSentinel(issue.GoalCondition)
	if err := harness.InjectBoardSection(ws.Path, ws.CliType, section); err != nil {
		slog.Warn("board menu: inject", "workspaceID", ws.ID, "error", err)
		return
	}
	slog.Info("board menu: injected",
		"workspaceID", ws.ID, "stagesIncluded", stats.includedStages,
		"stagesTotal", stats.totalStages, "chars", stats.chars, "estTokens", stats.estTokens)
}
