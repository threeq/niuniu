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
	if len(reason) > 4000 {
		reason = reason[:4000]
	}
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
// diff comments it consumed (marked sent by the caller so a later bounce does not
// re-inject them). It reads ALL kanban issue comments (macro) and only the UNRESOLVED
// workspace diff comments (sent_to_agent = FALSE, micro).
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

	// Layer 2 — unresolved workspace diff comments (micro: 具体每行怎么改).
	b.WriteString("### 二、工作空间未解决的行级 diff 评论（微观：具体每行怎么改）\n")
	var consumed []int64
	if ws, ok := s.activeWorkspaceForIssue(ctx, issue.ID); ok {
		if diffs, err := s.q.ListCommentsByWorkspace(ctx, ws.ID); err == nil {
			n := 0
			for _, d := range diffs {
				if d.SentToAgent.Valid && d.SentToAgent.Bool {
					continue // already resolved / previously sent
				}
				loc := d.FilePath
				if strings.TrimSpace(d.Repo) != "" {
					loc = d.Repo + " › " + d.FilePath
				}
				line := ""
				if d.LineNumber.Valid {
					line = fmt.Sprintf(":%d", d.LineNumber.Int64)
				}
				b.WriteString("- ")
				b.WriteString(loc)
				b.WriteString(line)
				b.WriteString(" — ")
				b.WriteString(strings.TrimSpace(d.Content))
				b.WriteString("\n")
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

	return b.String(), consumed
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
		if len(c) > 8000 {
			c = c[:8000]
		}
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
