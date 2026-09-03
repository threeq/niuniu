package service_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitRepoWithFile builds a real one-file git worktree, so anchor resolution runs
// against actual content instead of degrading to "unverifiable".
func gitRepoWithFile(t *testing.T, name, content string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	writeFileForTest(t, dir, name, content)
	out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput()
	require.NoErrorf(t, err, "git add: %s", out)
	out, err = exec.Command("git", "-C", dir, "commit", "-qm", "init").CombinedOutput()
	require.NoErrorf(t, err, "git commit: %s", out)
	return dir
}

func writeFileForTest(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// makeReviewColumn adds an `instruct` 审查 (review) column to the project.
func makeReviewColumn(t *testing.T, e *epicTestEnv, projectID int64, position int64) int64 {
	t.Helper()
	id := makeColumn(t, e, projectID, "审查", position)
	setColumnOp(t, e, id, "instruct", "对当前实现做严格 review，列出问题并就地修复")
	return id
}

// addDiffComment inserts a workspace diff (code-review) comment. sent marks it
// already DELIVERED (sent_to_agent=TRUE) — which since #683 wave 1 is delivery
// state only and does NOT hide the comment from the pending view; use
// resolveDiffComment for the review verdict.
func addDiffComment(t *testing.T, e *epicTestEnv, wsID int64, repo, file string, line int, content string, sent bool) int64 {
	t.Helper()
	c, err := e.q.CreateComment(e.ctx, store.CreateCommentParams{
		WorkspaceID: wsID,
		Repo:        repo,
		FilePath:    file,
		LineNumber:  sql.NullInt64{Int64: int64(line), Valid: line > 0},
		Content:     content,
		Side:        "new",
	})
	require.NoError(t, err)
	if sent {
		require.NoError(t, e.q.MarkCommentSent(e.ctx, c.ID))
	}
	return c.ID
}

// resolveDiffComment records the review VERDICT on a diff comment — the only
// thing that removes it from the rework view.
func resolveDiffComment(t *testing.T, e *epicTestEnv, id int64) {
	t.Helper()
	_, err := e.q.ResolveComment(e.ctx, store.ResolveCommentParams{ResolvedBy: "reviewer", ID: id})
	require.NoError(t, err)
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

	// Three layers of pre-existing feedback:
	//  - a RESOLVED diff comment (verdict recorded → must NOT be re-injected),
	//  - two unresolved diff comments (MUST be injected).
	// Note a merely-delivered comment is deliberately NOT excluded any more (#683
	// wave 1): sent_to_agent is delivery, not a verdict, so an unfixed comment has
	// to keep coming back.
	resolved := addDiffComment(t, e, wsID, "niuniu", "internal/auth.go", 42, "OLD-already-handled", true)
	resolveDiffComment(t, e, resolved)
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
	assert.Contains(t, msg, "internal/auth.go") // diff location
	// This workspace has no real worktree, so no anchor can be verified against
	// content. That must surface as "锚点已失效" WITHOUT a line number — shipping
	// "auth.go:7" here would assert a position nothing checked (#683 wave 1).
	assert.Contains(t, msg, "锚点已失效")
	assert.NotContains(t, msg, "internal/auth.go:7",
		"an unverifiable anchor must not present a line number as if it were current")
	assert.NotContains(t, msg, "OLD-already-handled", "a RESOLVED diff comment must not be re-injected")
	assert.Contains(t, msg, `to_column="审查"`, "directive tells agent to self-advance back to review")

	// The injected diff comments are marked DELIVERED — but they stay unresolved,
	// so they will reappear next round until a reviewer actually judges them.
	all, err := e.q.ListCommentsByWorkspace(e.ctx, wsID)
	require.NoError(t, err)
	sentByID := map[int64]bool{}
	resolvedByID := map[int64]bool{}
	for _, c := range all {
		sentByID[c.ID] = c.SentToAgent.Valid && c.SentToAgent.Bool
		resolvedByID[c.ID] = c.Resolved
	}
	assert.True(t, sentByID[unresolvedA], "unresolved diff A injected → marked delivered")
	assert.True(t, sentByID[unresolvedB], "unresolved diff B injected → marked delivered")
	assert.False(t, resolvedByID[unresolvedA], "delivery must not resolve: the agent may not have fixed it")
	assert.False(t, resolvedByID[unresolvedB], "delivery must not resolve: the agent may not have fixed it")
}

// Injected comments must NOT disappear from the pending view (#683 wave 1). The
// old code filtered on sent_to_agent, so a comment vanished the moment it was
// handed to the agent — whether or not anything was fixed — and a reviewer could
// never answer "which of my 5 comments actually got addressed". Only an explicit
// verdict removes a comment from the loop.
func TestRequestChanges_InjectedCommentsSurviveUntilResolved(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, implID := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")
	wsID := e.makeWorkspace(t, issueID)

	keepsFailing := addDiffComment(t, e, wsID, "niuniu", "internal/auth.go", 7, "空指针没判空", false)
	getsFixed := addDiffComment(t, e, wsID, "niuniu", "internal/auth.go", 9, "错误没有包装", false)

	bounce := func() string {
		t.Helper()
		proxy := &fakeAgentProxy{}
		e.svc.SetAgentProxy(proxy)
		// Park the card back in review so the next bounce is a real review→implement move.
		require.NoError(t, e.q.MoveIssue(e.ctx, store.MoveIssueParams{ID: issueID, ColumnID: reviewID}))
		_, err := e.svc.RequestChanges(e.ctx, service.RequestChangesInput{
			IssueID: issueID, Comment: "再改", CallerUserID: 1,
		})
		require.NoError(t, err)
		require.Len(t, proxy.kickoffs, 1)
		return proxy.kickoffs[0]
	}

	first := bounce()
	assert.Contains(t, first, "空指针没判空")
	assert.Contains(t, first, "错误没有包装")

	// Round two with NOTHING resolved: both comments must come back. Under the old
	// sent_to_agent filter this view would have been empty.
	second := bounce()
	assert.Contains(t, second, "空指针没判空", "an unresolved comment must survive injection")
	assert.Contains(t, second, "错误没有包装", "an unresolved comment must survive injection")

	// The reviewer judges one of them fixed; only that one leaves the loop.
	resolveDiffComment(t, e, getsFixed)
	third := bounce()
	assert.Contains(t, third, "空指针没判空", "still unresolved → still injected")
	assert.NotContains(t, third, "错误没有包装", "resolved → drops out of the loop")

	// Sanity: the surviving comment is delivered-but-unresolved, exactly the state
	// the old schema could not represent.
	got, err := e.q.GetComment(e.ctx, keepsFailing)
	require.NoError(t, err)
	assert.True(t, got.SentToAgent.Valid && got.SentToAgent.Bool, "delivered")
	assert.False(t, got.Resolved, "but not resolved")
	assert.Equal(t, implID, issueColumn(t, e, issueID))
}

// The rework kickoff must carry each comment's anchor state resolved against
// CURRENT content, not just the stored line number (#683 wave 1). Handing an
// agent "auth.go:42" for a line that no longer holds the reviewed code is the
// same silent drift the anchor columns exist to prevent — and the agent, unlike
// a human, has no visual cue that anything is off.
func TestRequestChanges_InjectsLiveAnchorStatePerComment(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	pid, _, _ := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")

	// A workspace backed by a REAL git worktree, so anchors resolve against
	// content rather than degrading to "unverifiable".
	dir := gitRepoWithFile(t, "auth.go", "package auth\n\nfunc Check(t string) bool {\n\treturn t != \"\"\n}\n")
	repo, err := e.q.CreateRepository(e.ctx, store.CreateRepositoryParams{
		Name: "niuniu", Path: dir, OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	ws, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issueID, Valid: true},
		Name:    "ws", Path: dir, Status: "needs_review", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	_, err = e.q.CreateWorktree(e.ctx, store.CreateWorktreeParams{
		WorkspaceID: ws.ID, RepositoryID: repo.ID, WorktreePath: dir,
		Branch: "feature", BaseBranch: "main",
	})
	require.NoError(t, err)

	// Comment on the `return` line (4), anchored the way the REST path does it.
	svc := service.NewReviewService(e.q)
	line := 4
	_, err = svc.CreateComment(e.ctx, ws.ID, service.CreateCommentInput{
		Repo: "niuniu", FilePath: "auth.go", LineNumber: &line, Content: "空串不是唯一非法情形",
	})
	require.NoError(t, err)

	// The agent inserts a doc comment above the function: the reviewed line slides
	// 4 -> 6 but is otherwise untouched.
	writeFileForTest(t, dir, "auth.go",
		"package auth\n\n// Check reports whether t is usable.\n// TODO\nfunc Check(t string) bool {\n\treturn t != \"\"\n}\n")

	_, err = e.svc.RequestChanges(e.ctx, service.RequestChangesInput{
		IssueID: issueID, Comment: "再改", CallerUserID: 1,
	})
	require.NoError(t, err)
	require.Len(t, proxy.kickoffs, 1)
	msg := proxy.kickoffs[0]

	// The line reference must be the CURRENT one, and say so.
	assert.Contains(t, msg, "auth.go:6", "must point at where the code is now")
	assert.Contains(t, msg, "原第 4 行", "must disclose that the anchor moved")
	assert.NotContains(t, msg, "auth.go:4 ", "the stale line number must not be presented as current")

	// And it must carry the evidence for judging whether the point was addressed:
	// the line matched verbatim during relocation, so it plainly was not.
	assert.Contains(t, msg, "评论时原文")
	assert.Contains(t, msg, "还没落实")
}

// An outdated anchor must be injected WITHOUT a line number: there is no honest
// current position, and a number here is exactly the false confidence being
// removed. The original snapshot rides along so the agent can still judge the
// point on its merits.
func TestRequestChanges_InjectsOutdatedAnchorWithoutALineNumber(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	pid, _, _ := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")

	dir := gitRepoWithFile(t, "auth.go", "package auth\n\nfunc Check(t string) bool {\n\treturn t != \"\"\n}\n")
	repo, err := e.q.CreateRepository(e.ctx, store.CreateRepositoryParams{
		Name: "niuniu", Path: dir, OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	ws, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issueID, Valid: true},
		Name:    "ws", Path: dir, Status: "needs_review", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	_, err = e.q.CreateWorktree(e.ctx, store.CreateWorktreeParams{
		WorkspaceID: ws.ID, RepositoryID: repo.ID, WorktreePath: dir,
		Branch: "feature", BaseBranch: "main",
	})
	require.NoError(t, err)

	svc := service.NewReviewService(e.q)
	line := 4
	_, err = svc.CreateComment(e.ctx, ws.ID, service.CreateCommentInput{
		Repo: "niuniu", FilePath: "auth.go", LineNumber: &line, Content: "空串不是唯一非法情形",
	})
	require.NoError(t, err)

	// The reviewed line is rewritten away; line 4 now holds unrelated code.
	writeFileForTest(t, dir, "auth.go",
		"package auth\n\nfunc Check(t string) bool {\n\treturn len(t) > 8\n}\n")

	_, err = e.svc.RequestChanges(e.ctx, service.RequestChangesInput{
		IssueID: issueID, Comment: "再改", CallerUserID: 1,
	})
	require.NoError(t, err)
	require.Len(t, proxy.kickoffs, 1)
	msg := proxy.kickoffs[0]

	assert.NotContains(t, msg, "auth.go:4", "an outdated anchor must not ship a line number")
	assert.Contains(t, msg, "锚点已失效")
	assert.Contains(t, msg, "空串不是唯一非法情形", "the comment itself still has to reach the agent")
	assert.Contains(t, msg, "评论时原文", "the original snapshot is all the reader has left")
}

// An old-side comment (a line the diff DELETED) must be injected as such. The
// line is not in the working tree by definition, so the kickoff must not claim
// it "hasn't changed" — that would be a statement about content that isn't
// there. "你不该删这行" is a common review note and has to survive the round trip.
func TestRequestChanges_InjectsDeletedLineCommentHonestly(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	pid, _, _ := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")

	dir := gitRepoWithFile(t, "auth.go", "package auth\n\nfunc Check(t string) bool {\n\treturn t != \"\"\n}\n")
	repo, err := e.q.CreateRepository(e.ctx, store.CreateRepositoryParams{
		Name: "niuniu", Path: dir, OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	ws, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issueID, Valid: true},
		Name:    "ws", Path: dir, Status: "needs_review", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	_, err = e.q.CreateWorktree(e.ctx, store.CreateWorktreeParams{
		WorkspaceID: ws.ID, RepositoryID: repo.ID, WorktreePath: dir,
		Branch: "feature", BaseBranch: "main",
	})
	require.NoError(t, err)

	// The reviewer objects to a line the agent DELETED — only expressible with
	// side=old, and the client supplies the snapshot (it isn't in the worktree).
	svc := service.NewReviewService(e.q)
	line := 5
	_, err = svc.CreateComment(e.ctx, ws.ID, service.CreateCommentInput{
		Repo: "niuniu", FilePath: "auth.go", LineNumber: &line,
		Content:      "这个审计日志不该删",
		Side:         "old",
		ContextLines: `{"before":["\treturn t != \"\""],"line":"\tauditLog(t)","after":["}"]}`,
	})
	require.NoError(t, err)

	_, err = e.svc.RequestChanges(e.ctx, service.RequestChangesInput{
		IssueID: issueID, Comment: "再改", CallerUserID: 1,
	})
	require.NoError(t, err)
	require.Len(t, proxy.kickoffs, 1)
	msg := proxy.kickoffs[0]

	assert.Contains(t, msg, "这个审计日志不该删")
	assert.Contains(t, msg, "已删除的旧行")
	assert.Contains(t, msg, "auditLog(t)", "the deleted line's text is the whole point of the comment")
	assert.NotContains(t, msg, "该行至今未变",
		"a deleted line is not in the worktree; claiming it is unchanged would be false")
}

// A passing review must leave a record. Before #683 wave 1 the review column
// could only bounce (request_changes): approval meant silently dragging a card,
// so "who approved this, when, on what basis" was unanswerable.
func TestApproveReview_RecordsTheApproval(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, _ := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")

	res, err := e.svc.ApproveReview(e.ctx, service.ApproveReviewInput{
		IssueID: issueID,
		Comment: "跑过全部用例，边界条件已覆盖",
		Author:  "reviewer-bob",
	})
	require.NoError(t, err)

	assert.True(t, res.Approved)
	assert.True(t, res.CommentPosted)
	// Default is record-without-moving: Epic / 人工审查 cards are moved by a human.
	assert.False(t, res.Moved)
	assert.Equal(t, reviewID, issueColumn(t, e, issueID), "approval alone must not move the card")

	comments, err := e.q.ListIssueComments(e.ctx, issueID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "reviewer-bob", comments[0].Author)
	assert.Contains(t, comments[0].Content, "审查通过")
	assert.Contains(t, comments[0].Content, "跑过全部用例")
}

// Approval can also advance the card and close out the line-level comments it
// covers — but only when explicitly asked to.
func TestApproveReview_OptionallyAdvancesAndResolves(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, implID := makeProjectWithColumns(t, e)
	reviewID := makeReviewColumn(t, e, pid, 2)
	issueID := makeStandaloneIssue(t, e, reviewID, "加登录", "用 JWT")
	wsID := e.makeWorkspace(t, issueID)

	open1 := addDiffComment(t, e, wsID, "niuniu", "a.go", 3, "改这里", false)
	open2 := addDiffComment(t, e, wsID, "niuniu", "b.go", 5, "还有这里", false)

	res, err := e.svc.ApproveReview(e.ctx, service.ApproveReviewInput{
		IssueID:         issueID,
		Comment:         "都改好了",
		Author:          "reviewer-bob",
		ToColumn:        strconv.FormatInt(implID, 10),
		ResolveComments: true,
	})
	require.NoError(t, err)

	assert.Equal(t, 2, res.ResolvedComments)
	assert.Equal(t, implID, issueColumn(t, e, issueID))

	for _, id := range []int64{open1, open2} {
		got, err := e.q.GetComment(e.ctx, id)
		require.NoError(t, err)
		assert.True(t, got.Resolved, "approval with resolve_comments must record the verdict")
		assert.Equal(t, "reviewer-bob", got.ResolvedBy)
	}
	_ = reviewID
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
	assert.Contains(t, msg, "线上发现回归")      // pre-existing issue comment injected
	assert.Contains(t, msg, "空指针没判空")      // unresolved diff comment injected
	assert.Contains(t, msg, "重新审核：需补回归用例") // the re-review comment injected
}
