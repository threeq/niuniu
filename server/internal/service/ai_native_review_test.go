package service_test

import (
	"database/sql"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeReviewColumn adds an `instruct` 审查 (review) column to the project.
func makeReviewColumn(t *testing.T, e *epicTestEnv, projectID int64, position int64) int64 {
	t.Helper()
	id := makeColumn(t, e, projectID, "审查", position)
	setColumnOp(t, e, id, "instruct", "对当前实现做严格 review，列出问题并就地修复")
	return id
}

// addDiffComment inserts a workspace diff (code-review) comment; sent marks it
// already-resolved (sent_to_agent=TRUE).
func addDiffComment(t *testing.T, e *epicTestEnv, wsID int64, repo, file string, line int, content string, sent bool) int64 {
	t.Helper()
	c, err := e.q.CreateComment(e.ctx, store.CreateCommentParams{
		WorkspaceID: wsID,
		Repo:        repo,
		FilePath:    file,
		LineNumber:  sql.NullInt64{Int64: int64(line), Valid: line > 0},
		Content:     content,
	})
	require.NoError(t, err)
	if sent {
		require.NoError(t, e.q.MarkCommentSent(e.ctx, c.ID))
	}
	return c.ID
}

// TestRequestChanges_BouncesBackAndInjectsTwoLayerContext is the core Review 闭环
// (#623) test: a reviewer marks 需修改 with an issue-level comment; the card bounces
// from 审查 back to 实现 and the agent's continuation kickoff carries BOTH the kanban
// issue comments (macro) and the UNRESOLVED diff comments (micro) — but not the diff
// comments that were already sent — and the consumed diff comments are marked sent.
func TestRequestChanges_BouncesBackAndInjectsTwoLayerContext(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	pid, _, implID := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)

	// Issue sits in the review column with an active (idle) workspace.
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")
	wsID := e.makeWorkspace(t, issueID)

	// Two layers of pre-existing feedback:
	//  - a diff comment already sent (must NOT be re-injected),
	//  - two unresolved diff comments (MUST be injected + then marked sent).
	addDiffComment(t, e, wsID, "niuniu", "internal/auth.go", 42, "OLD-already-handled", true)
	unresolvedA := addDiffComment(t, e, wsID, "niuniu", "internal/auth.go", 7, "空指针没判空", false)
	unresolvedB := addDiffComment(t, e, wsID, "", "web/login.tsx", 0, "缺少错误提示文案", false)

	res, err := e.svc.RequestChanges(e.ctx, service.RequestChangesInput{
		IssueID:      issueID,
		Comment:      "验收未通过：缺少失败重试与错误处理，需补齐后再审",
		Author:       "reviewer-bob",
		CallerUserID: 1,
	})
	require.NoError(t, err)

	// Card bounced to the implement lane.
	assert.Equal(t, implID, res.ColumnID)
	assert.Equal(t, implID, issueColumn(t, e, issueID))
	assert.True(t, res.Instructed)
	assert.True(t, res.CommentPosted)
	assert.False(t, res.Blocked)

	// The issue-level review comment was persisted.
	comments, err := e.q.ListIssueComments(e.ctx, issueID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "reviewer-bob", comments[0].Author)
	assert.Contains(t, comments[0].Content, "验收未通过")

	// The continuation kickoff weaves in BOTH layers.
	require.Len(t, proxy.kickoffs, 1)
	msg := proxy.kickoffs[0]
	assert.Contains(t, msg, "审查打回")             // continuation, not restart
	assert.Contains(t, msg, "在已有改动基础上修改")       // do not overwrite prior work
	assert.Contains(t, msg, "验收未通过")            // layer 1: issue comment (macro)
	assert.Contains(t, msg, "空指针没判空")           // layer 2: unresolved diff (micro)
	assert.Contains(t, msg, "缺少错误提示文案")         // layer 2: unresolved diff (micro)
	assert.Contains(t, msg, "internal/auth.go:7") // diff location + line
	assert.NotContains(t, msg, "OLD-already-handled", "already-sent diff comment must not be re-injected")
	assert.Contains(t, msg, `to_column="审查"`, "directive tells agent to self-advance back to review")

	// The injected (previously unresolved) diff comments are now marked sent.
	all, err := e.q.ListCommentsByWorkspace(e.ctx, wsID)
	require.NoError(t, err)
	sentByID := map[int64]bool{}
	for _, c := range all {
		sentByID[c.ID] = c.SentToAgent.Valid && c.SentToAgent.Bool
	}
	assert.True(t, sentByID[unresolvedA], "unresolved diff A consumed → marked sent")
	assert.True(t, sentByID[unresolvedB], "unresolved diff B consumed → marked sent")
}

// TestAdvanceIssue_IntoReviewColumn_RecordsChangeSummary verifies the loop's other
// half: when the worker self-advances back to 审查 with a reason (本轮改动说明), that
// summary is persisted as a durable issue comment so the reviewer sees it (#623).
func TestAdvanceIssue_IntoReviewColumn_RecordsChangeSummary(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, implID := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, implID, "加登录", "用 JWT")

	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     strconv.FormatInt(reviewID, 10),
		Reason:       "已补齐失败重试并加了错误提示；针对 diff 评论修正了空指针判空",
		CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, reviewID, issueColumn(t, e, issueID))

	comments, err := e.q.ListIssueComments(e.ctx, issueID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "续跑", comments[0].Author)
	assert.Contains(t, comments[0].Content, "本轮改动说明")
	assert.Contains(t, comments[0].Content, "失败重试")
}

// TestAdvanceIssue_IntoImplementNotFromReview_NoInjection guards that a normal
// forward move into 实现 (not a review bounce) does NOT inject review context.
func TestAdvanceIssue_IntoImplementNotFromReview_NoInjection(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, implID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "加登录", "用 JWT")
	e.makeWorkspace(t, issueID)

	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     strconv.FormatInt(implID, 10),
		CallerUserID: 1,
	})
	require.NoError(t, err)
	require.Len(t, proxy.kickoffs, 1)
	assert.NotContains(t, proxy.kickoffs[0], "审查打回", "a non-review→implement move must not inject review context")
}

// TestRequestChanges_FromDoneColumn_ReopensWithInjection guards the re-review of an
// already-completed issue: RequestChanges forces the two-layer context injection even
// though the source is the 完成 (complete) column, not a review lane.
func TestRequestChanges_FromDoneColumn_ReopensWithInjection(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	pid, _, implID := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	// Issue already 完成, with an active workspace + prior feedback.
	issueID := makeStandaloneIssue(t, e, doneID, "加登录", "用 JWT")
	wsID := e.makeWorkspace(t, issueID)
	addDiffComment(t, e, wsID, "niuniu", "internal/auth.go", 7, "空指针没判空", false)
	_, err := e.q.CreateIssueComment(e.ctx, store.CreateIssueCommentParams{IssueID: issueID, Author: "u", Content: "线上发现回归"})
	require.NoError(t, err)

	res, err := e.svc.RequestChanges(e.ctx, service.RequestChangesInput{
		IssueID: issueID, Comment: "重新审核：需补回归用例", CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, implID, res.ColumnID, "re-opened from 完成 back to 实现")

	require.Len(t, proxy.kickoffs, 1)
	msg := proxy.kickoffs[0]
	assert.Contains(t, msg, "审查打回", "forced injection fires even from a non-review source column")
	assert.Contains(t, msg, "线上发现回归")       // pre-existing issue comment injected
	assert.Contains(t, msg, "空指针没判空")       // unresolved diff comment injected
	assert.Contains(t, msg, "重新审核：需补回归用例") // the re-review comment injected
}
