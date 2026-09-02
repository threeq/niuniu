package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Service-level coverage for the two orthogonal concerns split apart in #683
// wave 1: the review VERDICT (resolved) vs DELIVERY (sent_to_agent), and
// anchoring a comment so it can never silently drift.

// reviewWorkspaceWithRepo wires a workspace whose single worktree is a real git
// repo, so CreateComment can pin a commit/blob and snapshot context the way it
// does in production.
func reviewWorkspaceWithRepo(t *testing.T, svc *ReviewService, q *store.Queries, filename, content string) (int64, string, string) {
	t.Helper()
	ctx := context.Background()
	dir := anchorRepo(t, filename, content)
	repo, err := q.CreateRepository(ctx, store.CreateRepositoryParams{
		Name: "niuniu", Path: dir, OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws", Path: dir, Status: "active", OwnerType: "user", OwnerID: 1, CliType: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateWorktree(ctx, store.CreateWorktreeParams{
		WorkspaceID: ws.ID, RepositoryID: repo.ID, WorktreePath: dir, Branch: "main", BaseBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	return ws.ID, dir, repo.Name
}

// CreateComment must pin the anchor at creation: without a blob and a snapshot
// there is nothing to detect drift against later.
func TestCreateComment_PinsAnchorAtCreation(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()
	wsID, _, repoName := reviewWorkspaceWithRepo(t, svc, q, "main.go", anchorSrc)

	line := 6
	c, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "main.go", LineNumber: &line, Content: "use a raw concat",
	})
	if err != nil {
		t.Fatal(err)
	}

	if c.Side != CommentSideNew {
		t.Errorf("side: want %q by default, got %q", CommentSideNew, c.Side)
	}
	if c.CommitSha == "" {
		t.Error("commit_sha not pinned — the comment records no version it was written against")
	}
	if c.BlobSha == "" {
		t.Error("blob_sha not pinned — drift cannot be detected without it")
	}
	snap := decodeCommentContext(c.ContextLines)
	if snap == nil || !strings.Contains(snap.Line, "Sprintf") {
		t.Errorf("context snapshot missing or wrong: %q", c.ContextLines)
	}
	if c.Resolved {
		t.Error("a new comment must start unresolved")
	}
}

// A comment on a DELETED line — previously impossible, because the only anchor
// was a new-side line number and a deleted line has none. The client supplies
// the snapshot (the old side isn't in the working tree) and the server must
// store it as-is without trying to hash current content.
func TestCreateComment_DeletedLineIsCommentable(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()
	wsID, _, repoName := reviewWorkspaceWithRepo(t, svc, q, "main.go", anchorSrc)

	line := 6
	snapshot := `{"before":["func greet(name string) string {"],"line":"\treturn fmt.Sprintf(\"hello %s\", name)","after":["}"]}`
	c, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "main.go", LineNumber: &line,
		Content:      "you shouldn't have deleted this",
		Side:         CommentSideOld,
		ContextLines: snapshot,
	})
	if err != nil {
		t.Fatalf("commenting on a deleted line must be possible: %v", err)
	}

	if c.Side != CommentSideOld {
		t.Errorf("side: want %q, got %q", CommentSideOld, c.Side)
	}
	if c.ContextLines != snapshot {
		t.Errorf("client snapshot must be stored verbatim:\n got %q\nwant %q", c.ContextLines, snapshot)
	}
	// The old-side line does not exist in the working tree, so hashing current
	// content would pin a blob the comment isn't about.
	if c.BlobSha != "" {
		t.Errorf("old-side comment must not pin a working-tree blob, got %q", c.BlobSha)
	}

	// It survives a full rewrite of the file, still anchored by its snapshot.
	anchors, err := svc.ListCommentsWithAnchors(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors) != 1 {
		t.Fatalf("want 1 comment, got %d", len(anchors))
	}
	if anchors[0].Anchor.Side != CommentSideOld || anchors[0].Anchor.Status != AnchorStatusCurrent {
		t.Errorf("old-side anchor: want current/old, got %s/%s", anchors[0].Anchor.Status, anchors[0].Anchor.Side)
	}
}

// End-to-end anti-drift through the service: after the agent edits the file, the
// list endpoint reports relocated (and re-pins the row) or outdated — never a
// stale line presented as current.
func TestListCommentsWithAnchors_ReportsDriftInsteadOfHiding(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()
	wsID, dir, repoName := reviewWorkspaceWithRepo(t, svc, q, "main.go", anchorSrc)

	shifted := 6
	moved, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "main.go", LineNumber: &shifted, Content: "this one moves",
	})
	if err != nil {
		t.Fatal(err)
	}
	gone := 10
	vanished, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "main.go", LineNumber: &gone, Content: "this one disappears",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The agent edits: three lines inserted at the top (shifting the greet body
	// down) and the Println line rewritten away.
	writeAnchorFile(t, dir, "main.go", `// added
// added
// added
package main

import "fmt"

func greet(name string) string {
	return fmt.Sprintf("hello %s", name)
}

func main() {
	println(greet("world"))
}
`)

	anchors, err := svc.ListCommentsWithAnchors(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]AnchoredComment{}
	for _, a := range anchors {
		byID[a.ID] = a
	}

	got := byID[moved.ID].Anchor
	if got.Status != AnchorStatusRelocated {
		t.Errorf("shifted comment: want %q, got %q", AnchorStatusRelocated, got.Status)
	}
	if got.EffectiveLine != 9 {
		t.Errorf("shifted comment: want effective line 9, got %d", got.EffectiveLine)
	}

	if s := byID[vanished.ID].Anchor.Status; s != AnchorStatusOutdated {
		t.Errorf("vanished comment: want %q, got %q", AnchorStatusOutdated, s)
	}

	// The relocated comment is re-pinned in the DB, so the next read is a cheap
	// blob match rather than another content search.
	stored, err := q.GetComment(ctx, moved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LineNumber.Valid || stored.LineNumber.Int64 != 9 {
		t.Errorf("relocated comment not re-pinned: line = %v", stored.LineNumber)
	}
	second, err := svc.ListCommentsWithAnchors(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range second {
		if a.ID == moved.ID && a.Anchor.Status != AnchorStatusCurrent {
			t.Errorf("after re-pinning, want %q on re-read, got %q", AnchorStatusCurrent, a.Anchor.Status)
		}
	}
}

// Delivery and verdict are independent, asserted at the service boundary: this
// is the pairing that used to be conflated (code read sent_to_agent and called
// it "unresolved").
func TestSetCommentResolved_IndependentOfDelivery(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()
	wsID, _, repoName := reviewWorkspaceWithRepo(t, svc, q, "main.go", anchorSrc)

	line := 6
	c, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "main.go", LineNumber: &line, Content: "fix",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Deliver: the agent has been told, but nobody has judged the result.
	svc.SetAgentProxy(&fakeAgentProxy{})
	if err := svc.SendCommentToAgent(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetComment(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SentToAgent.Bool {
		t.Fatal("delivery flag not set")
	}
	if after.Resolved {
		t.Error("delivering a comment must not resolve it — that conflation is the bug")
	}

	// Resolve, then reopen; delivery state is untouched throughout.
	resolved, err := svc.SetCommentResolved(ctx, c.ID, true, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Resolved || resolved.ResolvedBy != "alice" {
		t.Errorf("resolve: got resolved=%v by=%q", resolved.Resolved, resolved.ResolvedBy)
	}
	if !resolved.SentToAgent.Bool {
		t.Error("resolving cleared the delivery flag")
	}

	reopened, err := svc.SetCommentResolved(ctx, c.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Resolved || reopened.ResolvedBy != "" || reopened.ResolvedAt.Valid {
		t.Errorf("reopen left residue: resolved=%v by=%q at=%v",
			reopened.Resolved, reopened.ResolvedBy, reopened.ResolvedAt)
	}
	if !reopened.SentToAgent.Bool {
		t.Error("reopening cleared the delivery flag")
	}
}

// Re-pinning a relocated comment must move the POSITION only. The snapshot is
// the evidence of what the reviewer actually saw; overwriting it with current
// content would quietly destroy the 原文-vs-现在 comparison — and would make an
// unaddressed comment look addressed, because its "original" would keep
// re-syncing to whatever the agent last wrote.
func TestListCommentsWithAnchors_RepinKeepsOriginalSnapshot(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()
	wsID, dir, repoName := reviewWorkspaceWithRepo(t, svc, q, "main.go", anchorSrc)

	line := 6
	c, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "main.go", LineNumber: &line, Content: "这里要改",
	})
	if err != nil {
		t.Fatal(err)
	}
	originalSnapshot := c.ContextLines

	// Shift the line down and rewrite a neighbour inside the window.
	writeAnchorFile(t, dir, "main.go", `// added
// added
// added
package main

import "fmt"

func greet(name, greeting string) string {
	return fmt.Sprintf("hello %s", name)
}
`)

	if _, err := svc.ListCommentsWithAnchors(ctx, wsID); err != nil {
		t.Fatal(err)
	}

	stored, err := q.GetComment(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ContextLines != originalSnapshot {
		t.Errorf("re-pin overwrote the comment-time snapshot:\n before %q\n after  %q",
			originalSnapshot, stored.ContextLines)
	}
	// Position did move, so the re-pin itself happened.
	if !stored.LineNumber.Valid || stored.LineNumber.Int64 != 9 {
		t.Errorf("expected the position to be re-pinned to 9, got %v", stored.LineNumber)
	}
}

// A comment whose repo cannot be resolved to a worktree must report outdated
// rather than silently claiming its line is fine.
func TestListCommentsWithAnchors_UnresolvableRepoIsOutdated(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws", Path: t.TempDir(), Status: "active", OwnerType: "user", OwnerID: 1, CliType: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	line := 3
	if _, err := svc.CreateComment(ctx, ws.ID, CreateCommentInput{
		Repo: "ghost", FilePath: "a.go", LineNumber: &line, Content: "?",
	}); err != nil {
		t.Fatal(err)
	}

	anchors, err := svc.ListCommentsWithAnchors(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors) != 1 {
		t.Fatalf("want 1, got %d", len(anchors))
	}
	if anchors[0].Anchor.Status != AnchorStatusOutdated {
		t.Errorf("want %q for an unresolvable repo, got %q", AnchorStatusOutdated, anchors[0].Anchor.Status)
	}
}

// Path traversal must not let a comment read outside its worktree.
func TestCreateComment_RejectsPathEscape(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()
	wsID, dir, repoName := reviewWorkspaceWithRepo(t, svc, q, "main.go", anchorSrc)

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("top\nsecret\nvalue\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	line := 2
	c, err := svc.CreateComment(ctx, wsID, CreateCommentInput{
		Repo: repoName, FilePath: "../" + filepath.Base(secret), LineNumber: &line, Content: "peek",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.ContextLines != "" || c.BlobSha != "" {
		t.Errorf("path escape leaked content outside the worktree: ctx=%q blob=%q", c.ContextLines, c.BlobSha)
	}
}
