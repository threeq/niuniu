package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// AI-native board Review 闭环 (#623): the "审查意见自动回退 + 带上下文续跑" loop.
//
// A reviewer (human via REST, or a review-column agent via the request_changes MCP
// tool) leaves an issue-level review comment and marks the card "需修改". The system
// then bounces the card back to the implement lane and injects TWO layers of review
// context into the agent's continuation:
//   1. 看板 issue 级评论 (macro: why it did not pass / pass criteria / 验收 gap);
//   2. 工作空间未解决的 diff 评论 (micro: line-level "fix this line").
// Both layers ride the same Deliver path a normal turn uses, so the running autohost
// agent CONTINUES in its existing worktree (changes intact) rather than restarting.
//
// The actual move + injection is done by AdvanceIssue, which detects the review→
// implement transition (isReviewColumnByFields) and appends buildReviewReworkContext
// to the instruct kickoff. RequestChanges is the reviewer-facing entry that first
// records the bounce comment, then advances back to the implement lane.

// isReviewColumnByFields reports whether a column is a review lane, from its name +
// lifecycle_mapping. Covers both the AI review column ("审查", lifecycle
// "implement-review") and the human review column ("人工审查", empty lifecycle), so
// review意见 from either走同一回路 (#623 验收: 人工审查列保留人工放行, 走同一回路).
func isReviewColumnByFields(name, lifecycle string) bool {
	if strings.Contains(strings.ToLower(strings.TrimSpace(lifecycle)), "review") {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "审查") || strings.Contains(n, "review")
}

// recordReviewChangeSummary persists a rework round's change summary (the advance
// reason) as a durable issue comment when the agent routes a card back INTO a review
// column, so the reviewer sees "本轮改动说明" in the issue thread (#623 验收: 续跑完成
// 自动回审查列且附本轮改动说明). Best-effort; a blank reason is a no-op.
func (s *EpicExecutionService) recordReviewChangeSummary(ctx context.Context, issueID int64, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	reason = truncateRunes(reason, 4000)
	if _, err := s.q.CreateIssueComment(ctx, store.CreateIssueCommentParams{
		IssueID: issueID,
		Author:  "续跑",
		Content: "本轮改动说明：" + reason,
	}); err != nil {
		slog.Warn("advance_issue: record review change summary", "issueID", issueID, "error", err)
	}
}

// buildReviewReworkContext composes the two-layer review context appended to the
// implement-lane kickoff on a review→implement bounce, and returns the ids of the
// diff comments it consumed (marked DELIVERED by the caller — delivery only; the
// comments stay unresolved and keep reappearing until a reviewer resolves them).
// It reads ALL kanban issue comments (macro) and the UNRESOLVED workspace diff
// comments (resolved = FALSE, micro).
func (s *EpicExecutionService) buildReviewReworkContext(ctx context.Context, issue store.Issue) (string, []int64) {
	var b strings.Builder
	b.WriteString("\n\n---\n\n## 🔁 审查打回 · 带上下文续跑（在已有改动基础上修改，勿重写覆盖）\n\n")
	b.WriteString("本 issue 刚从「审查」列被打回到「实现」列。你上一轮的改动仍在工作空间里，请在其基础上【针对性修改】，不要重头重跑、不要覆盖已有改动。\n\n")

	// Layer 1 — kanban issue-level comments (macro: 为什么不过 / 通过条件 / 验收 gap).
	b.WriteString("### 一、看板 issue 级审查意见（宏观：为什么不过 / 通过条件 / 验收 gap）\n")
	if comments, err := s.q.ListIssueComments(ctx, issue.ID); err == nil && len(comments) > 0 {
		for _, c := range comments {
			author := strings.TrimSpace(c.Author)
			if author == "" {
				author = "审查"
			}
			content := strings.TrimSpace(c.Content)
			if content == "" {
				continue
			}
			b.WriteString("- [")
			b.WriteString(author)
			b.WriteString("] ")
			b.WriteString(content)
			b.WriteString("\n")
		}
	} else {
		b.WriteString("（无看板 issue 级评论）\n")
	}
	b.WriteString("\n")

	// Layer 2 — UNRESOLVED workspace diff comments (micro: 具体每行怎么改).
	//
	// "Unresolved" means resolved = FALSE — the review verdict — NOT sent_to_agent
	// (#683 wave 1). Those are orthogonal: sent_to_agent is a one-shot delivery
	// flag, so filtering on it made every comment disappear from this view the
	// moment it was injected, whether or not the agent changed anything. A comment
	// now keeps reappearing each round until somebody actually resolves it.
	//
	// Each comment is rendered with its anchor re-resolved against CURRENT content
	// (原文 vs 现在), so the agent is never handed a bare "file:42" that quietly
	// points at unrelated code, and can see per comment whether its target still
	// looks the way the reviewer described it.
	b.WriteString("### 二、工作空间未解决的行级 diff 评论（微观：具体每行怎么改）\n")
	var consumed []int64
	if ws, ok := s.activeWorkspaceForIssue(ctx, issue.ID); ok {
		if diffs, err := s.q.ListCommentsByWorkspace(ctx, ws.ID); err == nil {
			paths := worktreePathsByRepo(ctx, s.q, ws.ID)
			n := 0
			for _, d := range diffs {
				if d.Resolved {
					continue // reviewer已判定改好
				}
				loc := d.FilePath
				if strings.TrimSpace(d.Repo) != "" {
					loc = d.Repo + " › " + d.FilePath
				}
				anchor := ResolveCommentAnchor(paths[d.Repo], d)
				b.WriteString("- ")
				b.WriteString(loc)
				b.WriteString(anchorLineRef(anchor, d))
				b.WriteString(" — ")
				b.WriteString(strings.TrimSpace(d.Content))
				b.WriteString("\n")
				writeAnchorDetail(&b, anchor)
				consumed = append(consumed, d.ID)
				n++
			}
			if n == 0 {
				b.WriteString("（无未解决的行级 diff 评论）\n")
			}
		} else {
			b.WriteString("（无法读取工作空间 diff 评论）\n")
		}
	} else {
		b.WriteString("（无关联工作空间的 diff 评论）\n")
	}
	b.WriteString("\n")

	// Directive: continue, then self-report back to the review lane with a summary.
	b.WriteString("### 完成后\n")
	b.WriteString("逐条落实以上两层评论。全部处理完毕后，调用 advance_issue(issue_id=")
	b.WriteString(strconv.FormatInt(issue.ID, 10))
	b.WriteString(`, to_column="审查", reason="本轮针对每条评论的改动说明：…") 把 issue 送回「审查」列，并在 reason 中说明本轮分别针对 issue 评论与 diff 评论各改了什么。`)
	b.WriteString("\n")
	b.WriteString("行级评论只有审查方判定「已解决」才会消失——你读过并不等于已解决，所以下一轮它们仍会原样出现，请确保真的改到位。\n")

	return b.String(), consumed
}

// anchorLineRef renders the location suffix for a diff comment: the line the
// comment applies to NOW, plus an explicit marker when that is not simply where
// it was written. An outdated anchor deliberately shows NO line number — handing
// the agent "file:42" for a line that no longer holds the reviewed code is the
// silent drift this whole mechanism exists to prevent (#683 wave 1).
func anchorLineRef(a CommentAnchor, c store.Comment) string {
	if a.Side == CommentSideOld {
		// The commented line was DELETED by the diff; it has no new-side number.
		if a.OriginalLine > 0 {
			return fmt.Sprintf(":%d（已删除的旧行）", a.OriginalLine)
		}
		return "（已删除的旧行）"
	}
	switch a.Status {
	case AnchorStatusOutdated:
		if a.OriginalLine > 0 {
			return fmt.Sprintf("（原第 %d 行，锚点已失效：该处内容已不在，行号不可信）", a.OriginalLine)
		}
		return "（锚点已失效）"
	case AnchorStatusRelocated:
		return fmt.Sprintf(":%d（原第 %d 行，已随内容移动）", a.EffectiveLine, a.OriginalLine)
	default:
		if a.EffectiveLine > 0 {
			return fmt.Sprintf(":%d", a.EffectiveLine)
		}
		if c.LineNumber.Valid {
			return fmt.Sprintf(":%d", c.LineNumber.Int64)
		}
		return ""
	}
}

// writeAnchorDetail appends the evidence a reader needs to judge "was this
// actually fixed?" — the line as the reviewer saw it, and, when the file has
// since changed, the line as it stands now. For an outdated anchor only the
// original survives, which is exactly why it must be shown.
func writeAnchorDetail(b *strings.Builder, a CommentAnchor) {
	orig := ""
	if a.Context != nil {
		orig = strings.TrimSpace(a.Context.Line)
	}
	if orig == "" {
		return
	}
	b.WriteString("    · 评论时原文：")
	b.WriteString(orig)
	b.WriteString("\n")
	// An old-side comment is about a line the diff DELETED. It is not in the
	// working tree by definition, so neither "unchanged" nor "now reads" applies —
	// claiming either would be a statement about content that isn't there.
	if a.Side == CommentSideOld {
		b.WriteString("    · 这是被删除的旧行；若意见是「不该删」，需要把它改回来\n")
		return
	}
	if a.Current != nil {
		if now := strings.TrimSpace(a.Current.Line); now != "" && now != orig {
			b.WriteString("    · 该行现在是：")
			b.WriteString(now)
			b.WriteString("\n")
			return
		}
	}
	if a.Status == AnchorStatusOutdated {
		b.WriteString("    · 现在：找不到这段内容（已被删除或重写），请据原文判断该意见是否仍适用\n")
		return
	}
	// The anchored line still matches the snapshot verbatim (that is how it was
	// located), so it is direct evidence the comment was NOT addressed. Say so
	// rather than staying silent, which would read as "nothing to see".
	b.WriteString("    · 该行至今未变——这条意见看起来还没落实\n")
}

// implementColumn resolves the project's implement (instruct) lane — the destination
// for a review bounce. Prefers the first op_primitive='instruct' column that is NOT
// itself a review lane, falling back to a "实现"/"implement" name match.
func (s *EpicExecutionService) implementColumn(ctx context.Context, projectID int64) (int64, string, error) {
	cols, err := s.q.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return 0, "", fmt.Errorf("list project columns: %w", err)
	}
	for _, c := range cols { // position-ordered: 实现 precedes 审查
		op, oerr := s.getColumnOp(ctx, c.ID)
		if oerr != nil {
			continue
		}
		if op.opPrimitive == "instruct" && !isReviewColumnByFields(op.name, op.lifecycle) {
			return c.ID, c.Name, nil
		}
	}
	for _, c := range cols {
		n := strings.ToLower(strings.TrimSpace(c.Name))
		if strings.Contains(c.Name, "实现") || strings.Contains(n, "implement") {
			return c.ID, c.Name, nil
		}
	}
	return 0, "", errors.New("no implement (instruct) column found in this project")
}

// RequestChangesInput is the request for RequestChanges (the request_changes MCP tool
// / POST .../request-changes).
type RequestChangesInput struct {
	IssueID int64
	// Comment is the issue-level review feedback (打回原因 / 通过条件 / 验收 gap). It is
	// persisted as a kanban issue comment and re-injected into the continuation.
	Comment string
	// Author is the reviewer identity for the issue comment; defaults to "审查".
	Author string
	// CallerUserID is the authenticated MCP/API user (threaded into the move + audit).
	CallerUserID int64
}

// RequestChangesResult is the response for RequestChanges.
type RequestChangesResult struct {
	IssueID       int64  `json:"issue_id"`
	ColumnID      int64  `json:"column_id"`
	ColumnName    string `json:"column_name"`
	WorkspaceID   int64  `json:"workspace_id,omitempty"`
	Instructed    bool   `json:"instructed"`
	CommentPosted bool   `json:"comment_posted"`
	Blocked       bool   `json:"blocked,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// RequestChanges is the reviewer-facing "需修改 / 打回" entry of the Review 闭环 (#623):
// it records the issue-level review comment, then bounces the card back to the
// implement lane. The actual two-layer context injection + continuation happens
// inside AdvanceIssue, which RequestChanges forces via InjectReviewContext so the
// bounce carries the two-layer context from ANY source column — including a re-review
// of an issue already sitting in 完成 (not only the normal 审查→实现 transition).
func (s *EpicExecutionService) RequestChanges(ctx context.Context, in RequestChangesInput) (RequestChangesResult, error) {
	issue, err := s.q.GetIssue(ctx, in.IssueID)
	if err != nil {
		return RequestChangesResult{}, fmt.Errorf("load issue %d: %w", in.IssueID, err)
	}
	srcCol, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return RequestChangesResult{}, fmt.Errorf("load source column: %w", err)
	}
	implID, implName, err := s.implementColumn(ctx, srcCol.ProjectID)
	if err != nil {
		return RequestChangesResult{}, err
	}

	// 1) Persist the issue-level review comment (macro feedback / pass criteria) so it
	//    survives in the issue thread AND gets re-injected by buildReviewReworkContext.
	author := strings.TrimSpace(in.Author)
	if author == "" {
		author = "审查"
	}
	posted := false
	if c := strings.TrimSpace(in.Comment); c != "" {
		c = truncateRunes(c, 8000)
		if _, cerr := s.q.CreateIssueComment(ctx, store.CreateIssueCommentParams{
			IssueID: in.IssueID, Author: author, Content: c,
		}); cerr != nil {
			slog.Warn("request_changes: create issue comment", "issueID", in.IssueID, "error", cerr)
		} else {
			posted = true
		}
	}
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{
			IssueID: in.IssueID, Kind: "intervention",
			Summary: "审查打回「需修改」→ 回退「" + implName + "」续跑",
		})
	}

	// 2) Bounce back to the implement lane. AdvanceIssue weaves the two-layer review
	//    context into the kickoff (continuation, not restart) and marks the injected
	//    diff comments consumed.
	adv, err := s.AdvanceIssue(ctx, AdvanceIssueInput{
		IssueID:      in.IssueID,
		ToColumn:     strconv.FormatInt(implID, 10),
		Reason:       "审查打回：需修改",
		CallerUserID: in.CallerUserID,
		// Force injection so a bounce works from ANY source column — including a
		// re-review of an issue already in 完成 (not just the审查→实现 transition).
		InjectReviewContext: true,
	})
	if err != nil {
		return RequestChangesResult{IssueID: in.IssueID, CommentPosted: posted}, err
	}
	return RequestChangesResult{
		IssueID:       in.IssueID,
		ColumnID:      adv.ColumnID,
		ColumnName:    adv.ColumnName,
		WorkspaceID:   adv.WorkspaceID,
		Instructed:    adv.Instructed,
		CommentPosted: posted,
		Blocked:       adv.Blocked,
		BlockedReason: adv.BlockedReason,
	}, nil
}

// ApproveReviewInput is the request for ApproveReview (the approve_review MCP tool
// / POST .../approve-review).
type ApproveReviewInput struct {
	IssueID int64
	// Comment is the approval rationale (what was checked / why it passes). Recorded
	// as a kanban issue comment.
	Comment string
	// Author is the reviewer identity; defaults to "审查".
	Author string
	// ToColumn optionally advances the card on approval, given as a column id or
	// name. Empty means record the approval WITHOUT moving — the default, because
	// several columns (Epic, 人工审查) require a human to make the move.
	ToColumn string
	// ResolveComments marks every one of the workspace's outstanding line-level diff
	// comments resolved. Only set this when the approval genuinely covers them all;
	// otherwise resolve them individually so the record stays truthful.
	ResolveComments bool
	// CallerUserID is the authenticated MCP/API user (threaded into the move + audit).
	CallerUserID int64
}

// ApproveReviewResult is the response for ApproveReview.
type ApproveReviewResult struct {
	IssueID  int64 `json:"issue_id"`
	Approved bool  `json:"approved"`
	// ColumnID/ColumnName are the post-move position, or the unchanged current
	// column when the approval did not move the card.
	ColumnID      int64  `json:"column_id"`
	ColumnName    string `json:"column_name"`
	Moved         bool   `json:"moved"`
	CommentPosted bool   `json:"comment_posted"`
	// ResolvedComments is how many line-level diff comments this approval resolved.
	ResolvedComments int    `json:"resolved_comments"`
	Blocked          bool   `json:"blocked,omitempty"`
	BlockedReason    string `json:"blocked_reason,omitempty"`
}

// ApproveReview is the POSITIVE review conclusion — the counterpart RequestChanges
// never had (#683 wave 1). Before this, a review that PASSED left no trace: the
// only recorded outcome was a bounce, and approving meant silently dragging the
// card, so "who approved this, when, and on what basis" was unanswerable.
//
// It records the approval as a durable issue comment plus an exec event, and
// optionally resolves the outstanding line-level comments and advances the card.
// Moving is opt-in via ToColumn: Epic and 人工审查 cards must be moved by a human,
// so approval and movement are deliberately separate decisions.
func (s *EpicExecutionService) ApproveReview(ctx context.Context, in ApproveReviewInput) (ApproveReviewResult, error) {
	issue, err := s.q.GetIssue(ctx, in.IssueID)
	if err != nil {
		return ApproveReviewResult{}, fmt.Errorf("load issue %d: %w", in.IssueID, err)
	}
	srcCol, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return ApproveReviewResult{}, fmt.Errorf("load source column: %w", err)
	}

	author := strings.TrimSpace(in.Author)
	if author == "" {
		author = "审查"
	}
	res := ApproveReviewResult{
		IssueID:    in.IssueID,
		Approved:   true,
		ColumnID:   srcCol.ID,
		ColumnName: srcCol.Name,
	}

	// 1) Durable record of the approval itself.
	body := "✅ 审查通过"
	if c := strings.TrimSpace(in.Comment); c != "" {
		// Rune-safe: review text here is routinely Chinese, and a byte cut would
		// split a rune and store invalid UTF-8.
		body += "：" + truncateRunes(c, 8000)
	}
	if _, cerr := s.q.CreateIssueComment(ctx, store.CreateIssueCommentParams{
		IssueID: in.IssueID, Author: author, Content: body,
	}); cerr != nil {
		slog.Warn("approve_review: create issue comment", "issueID", in.IssueID, "error", cerr)
	} else {
		res.CommentPosted = true
	}
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{
			IssueID: in.IssueID, Kind: "intervention",
			Summary: "审查通过（" + author + "）",
		})
	}

	// 2) Optionally close out the line-level comments this approval covers.
	if in.ResolveComments {
		if ws, ok := s.activeWorkspaceForIssue(ctx, in.IssueID); ok {
			if diffs, lerr := s.q.ListCommentsByWorkspace(ctx, ws.ID); lerr == nil {
				for _, d := range diffs {
					if d.Resolved {
						continue
					}
					if _, rerr := s.q.ResolveComment(ctx, store.ResolveCommentParams{
						ResolvedBy: author, ID: d.ID,
					}); rerr != nil {
						slog.Warn("approve_review: resolve diff comment", "commentID", d.ID, "error", rerr)
						continue
					}
					res.ResolvedComments++
				}
			}
		}
	}

	// 3) Optionally advance. A failed move must not un-record the approval — the
	//    review verdict is already true and durable at this point.
	if strings.TrimSpace(in.ToColumn) != "" {
		adv, aerr := s.AdvanceIssue(ctx, AdvanceIssueInput{
			IssueID:      in.IssueID,
			ToColumn:     in.ToColumn,
			Reason:       "审查通过",
			CallerUserID: in.CallerUserID,
		})
		if aerr != nil {
			return res, aerr
		}
		res.ColumnID = adv.ColumnID
		res.ColumnName = adv.ColumnName
		res.Moved = !adv.Blocked
		res.Blocked = adv.Blocked
		res.BlockedReason = adv.BlockedReason
	}
	return res, nil
}
