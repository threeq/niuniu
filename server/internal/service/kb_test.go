package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/kbindex"
	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func newKBTest(t *testing.T) (*KBService, OwnerRef) {
	t.Helper()
	dataDir := t.TempDir()
	_, q := pgtest.SetupSQLiteDB(t)
	mgr := kbindex.NewManager("sqlite", nil)
	t.Cleanup(func() { mgr.Close() })
	svc := NewKBService(q, dataDir, mgr)
	// Seed a user so owner_id=1 is a real owner (FKs are permissive here).
	return svc, OwnerRef{Type: "user", ID: 1}
}

func writeKBFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKBCreateListDelete(t *testing.T) {
	svc, owner := newKBTest(t)
	ctx := context.Background()
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "docs", SourceKind: "local", SourceAddr: "/tmp/x"})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if kb.ID == 0 || kb.Name != "docs" || kb.SourceKind != "local" {
		t.Fatalf("unexpected kb: %+v", kb)
	}
	// Duplicate name for the same owner is rejected.
	if _, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "docs", SourceKind: "local"}); err == nil {
		t.Fatalf("expected duplicate-name error")
	}
	list, err := svc.ListKBs(ctx, owner)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListKBs: err=%v len=%d", err, len(list))
	}
	if err := svc.DeleteKB(ctx, owner, kb.ID); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}
	if list, _ := svc.ListKBs(ctx, owner); len(list) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(list))
	}
}

func TestKBIngestAndSearchChinese(t *testing.T) {
	allowLocal(t) // reading a local corpus dir is a personal-edition feature
	svc, owner := newKBTest(t)
	ctx := context.Background()
	src := t.TempDir()
	writeKBFile(t, src, "guide.md", "# 知识库\n\n这是一个关于全文检索的中文文档，演示中文命中。\n")
	writeKBFile(t, src, "sub/notes.txt", "english full text search content here\n")
	writeKBFile(t, src, "ignore.bin", "\x00\x01\x02binary")

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "kb1", SourceKind: "local", SourceAddr: src})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	res, err := svc.Ingest(ctx, owner, kb.ID, IngestOptions{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.FilesIngested != 2 {
		t.Fatalf("expected 2 text files ingested, got %d (scanned=%d)", res.FilesIngested, res.FilesScanned)
	}
	if res.ChunksWritten < 2 {
		t.Fatalf("expected chunks written, got %d", res.ChunksWritten)
	}

	// Chinese keyword hit returns a pointer (rel_path) + snippet.
	hits, err := svc.Search(ctx, owner, kb.ID, "全文检索", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].RelPath != "guide.md" {
		t.Fatalf("expected chinese hit in guide.md, got %+v", hits)
	}
	// English hit too.
	if h, _ := svc.Search(ctx, owner, kb.ID, "full text search", 10); len(h) != 1 || h[0].RelPath != filepath.ToSlash("sub/notes.txt") {
		t.Fatalf("expected english hit in sub/notes.txt, got %+v", h)
	}
}

// bindKB scopes a KB to a project so a workspace on that project can see it.
func bindKB(t *testing.T, svc *KBService, kbID, projectID int64) {
	t.Helper()
	if err := svc.q.CreateKBBinding(context.Background(), store.CreateKBBindingParams{
		KbID: kbID, TargetType: "project", TargetID: projectID,
	}); err != nil {
		t.Fatalf("CreateKBBinding: %v", err)
	}
}

func TestKBVisibleSearchBindingAndIsolation(t *testing.T) {
	allowLocal(t) // local corpus dirs (personal-edition feature)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	const projectID int64 = 100

	// Two KBs for the same owner: one bound to the project, one unbound.
	boundSrc := t.TempDir()
	writeKBFile(t, boundSrc, "policy.md", "退款政策：七天无理由退款，关键词 全文检索 命中。\n")
	bound, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "bound-kb", SourceKind: "local", SourceAddr: boundSrc})
	if _, err := svc.Ingest(ctx, owner, bound.ID, IngestOptions{}); err != nil {
		t.Fatalf("ingest bound: %v", err)
	}
	bindKB(t, svc, bound.ID, projectID)

	unboundSrc := t.TempDir()
	writeKBFile(t, unboundSrc, "secret.md", "未绑定文档同样包含 全文检索 字样但不应可见。\n")
	unbound, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "unbound-kb", SourceKind: "local", SourceAddr: unboundSrc})
	if _, err := svc.Ingest(ctx, owner, unbound.ID, IngestOptions{}); err != nil {
		t.Fatalf("ingest unbound: %v", err)
	}

	// Visible list = only the bound KB.
	vis, err := svc.ListVisibleKBs(ctx, owner, ptr(projectID))
	if err != nil || len(vis) != 1 || vis[0].ID != bound.ID {
		t.Fatalf("ListVisibleKBs: err=%v got=%+v", err, vis)
	}
	// nil project sees nothing.
	if v, _ := svc.ListVisibleKBs(ctx, owner, nil); len(v) != 0 {
		t.Fatalf("project-less workspace should see no KB, got %d", len(v))
	}

	// SearchVisible returns hits only from the bound KB, tagged with its name.
	hits, err := svc.SearchVisible(ctx, owner, ptr(projectID), "全文检索", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit from the bound KB only, got %d: %+v", len(hits), hits)
	}
	if hits[0].KBID != bound.ID || hits[0].KBName != "bound-kb" || hits[0].RelPath != "policy.md" {
		t.Fatalf("hit not tagged to bound KB: %+v", hits[0])
	}

	// Tenant isolation: a second owner binds its OWN KB to the SAME project id.
	// It must never surface owner-1's KB, and owner-1 must never see owner-2's.
	other := OwnerRef{Type: "user", ID: 2}
	otherSrc := t.TempDir()
	writeKBFile(t, otherSrc, "other.md", "他人知识库的 全文检索 内容，跨租户不可见。\n")
	otherKB, _ := svc.CreateKB(ctx, other, CreateKBParams{Name: "bound-kb", SourceKind: "local", SourceAddr: otherSrc})
	if _, err := svc.Ingest(ctx, other, otherKB.ID, IngestOptions{}); err != nil {
		t.Fatalf("ingest other: %v", err)
	}
	bindKB(t, svc, otherKB.ID, projectID)

	ownerHits, _ := svc.SearchVisible(ctx, owner, ptr(projectID), "全文检索", 10)
	if len(ownerHits) != 1 || ownerHits[0].KBID != bound.ID {
		t.Fatalf("owner-1 leaked cross-tenant KB: %+v", ownerHits)
	}
	otherHits, _ := svc.SearchVisible(ctx, other, ptr(projectID), "全文检索", 10)
	if len(otherHits) != 1 || otherHits[0].KBID != otherKB.ID {
		t.Fatalf("owner-2 leaked cross-tenant KB: %+v", otherHits)
	}
}

// TestKBResolveDatasetDirs covers the "C" ability (KB base4): the read-only
// dataset directories exposed to a workspace agent are exactly those of the
// project-bound, owner-scoped KBs whose source resolves on disk.
func TestKBResolveDatasetDirs(t *testing.T) {
	allowLocal(t) // local corpus dirs (personal-edition feature)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	const projectID int64 = 200

	boundSrc := t.TempDir()
	writeKBFile(t, boundSrc, "guide.md", "可直读内容\n")
	bound, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "bound", SourceKind: "local", SourceAddr: boundSrc})
	bindKB(t, svc, bound.ID, projectID)

	// Unbound KB: must not be exposed.
	unboundSrc := t.TempDir()
	writeKBFile(t, unboundSrc, "x.md", "不可见\n")
	svc.CreateKB(ctx, owner, CreateKBParams{Name: "unbound", SourceKind: "local", SourceAddr: unboundSrc})

	// Bound url-kind KB that hasn't been downloaded: resolves to nothing on disk
	// and must be skipped (not error).
	urlKB, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "remote", SourceKind: "url", SourceAddr: "https://example.com/x"})
	bindKB(t, svc, urlKB.ID, projectID)

	dirs, err := svc.ResolveDatasetDirs(ctx, owner, projectID)
	if err != nil {
		t.Fatalf("ResolveDatasetDirs: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected exactly the bound, resolvable KB, got %d: %+v", len(dirs), dirs)
	}
	if dirs[0].KBID != bound.ID || dirs[0].Name != "bound" || dirs[0].Root != boundSrc {
		t.Fatalf("unexpected exposed dir: %+v", dirs[0])
	}

	// Project-less resolution sees nothing.
	if d, _ := svc.ResolveDatasetDirs(ctx, owner, 0); len(d) != 0 {
		t.Fatalf("project-less should expose nothing, got %d", len(d))
	}

	// Cross-tenant isolation: owner-2 binding its own KB to the same project id
	// must not leak owner-1's directory and vice versa.
	other := OwnerRef{Type: "user", ID: 2}
	otherSrc := t.TempDir()
	writeKBFile(t, otherSrc, "o.md", "他人内容\n")
	otherKB, _ := svc.CreateKB(ctx, other, CreateKBParams{Name: "bound", SourceKind: "local", SourceAddr: otherSrc})
	bindKB(t, svc, otherKB.ID, projectID)
	od, _ := svc.ResolveDatasetDirs(ctx, other, projectID)
	if len(od) != 1 || od[0].Root != otherSrc {
		t.Fatalf("owner-2 resolution wrong/leaky: %+v", od)
	}
	o1, _ := svc.ResolveDatasetDirs(ctx, owner, projectID)
	if len(o1) != 1 || o1[0].Root != boundSrc {
		t.Fatalf("owner-1 resolution leaked cross-tenant dir: %+v", o1)
	}
}

func TestKBReingestIdempotentAndRebuild(t *testing.T) {
	allowLocal(t) // local corpus dirs (personal-edition feature)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	src := t.TempDir()
	writeKBFile(t, src, "a.md", "原始内容关于检索引擎的说明\n")
	kb, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "kb", SourceKind: "local", SourceAddr: src})

	if _, err := svc.Ingest(ctx, owner, kb.ID, IngestOptions{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// Re-ingest unchanged: file should be skipped.
	res, _ := svc.Ingest(ctx, owner, kb.ID, IngestOptions{})
	if res.FilesUnchanged != 1 || res.FilesIngested != 0 {
		t.Fatalf("unchanged re-ingest should skip: %+v", res)
	}
	// Change the file: it should be re-ingested.
	writeKBFile(t, src, "a.md", "更新后的内容关于向量数据库的说明\n")
	res, _ = svc.Ingest(ctx, owner, kb.ID, IngestOptions{})
	if res.FilesIngested != 1 {
		t.Fatalf("changed file should re-ingest: %+v", res)
	}
	if h, _ := svc.Search(ctx, owner, kb.ID, "向量数据库", 10); len(h) != 1 {
		t.Fatalf("updated content should be searchable, got %d", len(h))
	}
	if h, _ := svc.Search(ctx, owner, kb.ID, "检索引擎", 10); len(h) != 0 {
		t.Fatalf("stale content should be gone, got %d", len(h))
	}

	// Rebuild from source: index dropped then repopulated, search still works.
	if err := svc.RebuildIndex(ctx, owner, kb.ID); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if h, _ := svc.Search(ctx, owner, kb.ID, "向量数据库", 10); len(h) != 1 {
		t.Fatalf("rebuilt index should still hit, got %d", len(h))
	}
}

// newWorkspaceTest creates a KBService + a workspace owned by owner, returning
// the workspace id and its on-disk path.
func newWorkspaceTest(t *testing.T, svc *KBService, owner OwnerRef) (int64, string) {
	t.Helper()
	wsPath := t.TempDir()
	ws, err := svc.q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "ws-test", Path: wsPath, Status: "active",
		OwnerType: owner.Type, OwnerID: owner.ID, CliType: "claude",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return ws.ID, wsPath
}

// TestKBWorkspaceMount covers the KB as a first-class workspace citizen: mounting
// a KB materializes its source read-only into <workspace>/datasets/<name>/,
// auto-ingests it, exposes that dir to the agent, and unmount removes both.
func TestKBWorkspaceMount(t *testing.T) {
	allowLocal(t) // local corpus dirs (personal-edition feature)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	wsID, wsPath := newWorkspaceTest(t, svc, owner)

	src := t.TempDir()
	writeKBFile(t, src, "guide.md", "工作空间挂载的 全文检索 演示内容\n")
	kb, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "mount-me", SourceKind: "local", SourceAddr: src})

	// Mount: materializes the dir + auto-ingests.
	mount, err := svc.MountKB(ctx, owner, wsID, kb.ID)
	if err != nil {
		t.Fatalf("MountKB: %v", err)
	}
	wantDir := filepath.Join(wsPath, "datasets", "mount-me")
	if mount.DatasetPath != wantDir {
		t.Fatalf("expected dataset dir %q, got %q", wantDir, mount.DatasetPath)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "guide.md")); err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	// Auto-ingested: the mounted corpus is searchable.
	if h, _ := svc.Search(ctx, owner, kb.ID, "全文检索", 10); len(h) != 1 {
		t.Fatalf("mounted KB should be searchable, got %d hits", len(h))
	}

	// ListWorkspaceKBs returns the one mount.
	mounts, err := svc.ListWorkspaceKBs(ctx, owner, wsID)
	if err != nil || len(mounts) != 1 || mounts[0].KBID != kb.ID {
		t.Fatalf("ListWorkspaceKBs: err=%v got=%+v", err, mounts)
	}

	// Agent dataset resolution points at the materialized dir (in the tree), not
	// the source root.
	dirs, err := svc.WorkspaceDatasetDirs(ctx, wsID)
	if err != nil || len(dirs) != 1 || dirs[0].KBID != kb.ID || dirs[0].Root != wantDir {
		t.Fatalf("WorkspaceDatasetDirs: err=%v got=%+v", err, dirs)
	}

	// Sync propagates a changed source into the materialized dir.
	writeKBFile(t, src, "new.txt", "新增 同步 内容\n")
	if err := svc.SyncWorkspaceKB(ctx, owner, wsID, kb.ID); err != nil {
		t.Fatalf("SyncWorkspaceKB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "new.txt")); err != nil {
		t.Fatalf("synced file missing: %v", err)
	}

	// Unmount removes the row + the materialized dir.
	if err := svc.UnmountKB(ctx, owner, wsID, kb.ID); err != nil {
		t.Fatalf("UnmountKB: %v", err)
	}
	if m, _ := svc.ListWorkspaceKBs(ctx, owner, wsID); len(m) != 0 {
		t.Fatalf("expected no mounts after unmount, got %d", len(m))
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Fatalf("materialized dir should be removed after unmount, stat err=%v", err)
	}
	// Unmount is idempotent.
	if err := svc.UnmountKB(ctx, owner, wsID, kb.ID); err != nil {
		t.Fatalf("double unmount should be a no-op, got %v", err)
	}
}

// TestKBWorkspaceMountIsolation verifies per-workspace + cross-tenant isolation:
// a workspace only sees its own mounts, and another tenant cannot mount to (or
// read) another's workspace.
func TestKBWorkspaceMountIsolation(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()

	other := OwnerRef{Type: "user", ID: 2}
	wsID, _ := newWorkspaceTest(t, svc, owner)

	src := t.TempDir()
	writeKBFile(t, src, "a.md", "内容甲\n")
	kbA, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "kb-a", SourceKind: "local", SourceAddr: src})
	kbB, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "kb-b", SourceKind: "local", SourceAddr: t.TempDir()})

	// Mount only kb-a — kb-b stays unmounted (per-workspace choice).
	if _, err := svc.MountKB(ctx, owner, wsID, kbA.ID); err != nil {
		t.Fatalf("MountKB: %v", err)
	}
	m, _ := svc.ListWorkspaceKBs(ctx, owner, wsID)
	if len(m) != 1 || m[0].KBID != kbA.ID {
		t.Fatalf("expected only kb-a mounted, got %+v", m)
	}

	// Cross-tenant: another owner cannot mount to owner's workspace.
	if _, err := svc.MountKB(ctx, other, wsID, kbA.ID); err == nil {
		t.Fatalf("other tenant should not mount to owner's workspace")
	}
	// ...nor unmount from it, nor list it.
	if err := svc.UnmountKB(ctx, other, wsID, kbA.ID); err == nil {
		t.Fatalf("other tenant should not unmount owner's workspace")
	}
	if m, _ := svc.ListWorkspaceKBs(ctx, other, wsID); len(m) != 0 {
		t.Fatalf("other tenant should see no mounts, got %d", len(m))
	}
	_ = kbB // kb-b is never mounted
}

// TestKBWorkspaceDatasetDirsDisabledNoFallback verifies that a workspace with ANY
// explicit mount never falls back to project-bound KBs, even when its mounted KB
// is disabled. Regression: the earlier code keyed the fallback decision off the
// ENABLED mount count, so an all-disabled explicit mount set spuriously inherited
// every project-bound KB the workspace never chose.
func TestKBWorkspaceDatasetDirsDisabledNoFallback(t *testing.T) {
	allowLocal(t)
	dataDir := t.TempDir()
	rawDB, q := pgtest.SetupSQLiteDB(t)
	mgr := kbindex.NewManager("sqlite", nil)
	t.Cleanup(func() { mgr.Close() })
	svc := NewKBService(q, dataDir, mgr)
	owner := OwnerRef{Type: "user", ID: 1}
	ctx := context.Background()

	// Project + column + two issues → two workspaces linked to the SAME project
	// so the project fallback has a real candidate KB.
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "proj", OwnerType: "user", OwnerID: 1})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "待办", Position: 0})
	if err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}
	newWS := func(name string, pos int64) int64 {
		t.Helper()
		issue, ierr := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: name, Position: pos})
		if ierr != nil {
			t.Fatalf("CreateIssue: %v", ierr)
		}
		ws, werr := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			Name: name, Path: t.TempDir(), Status: "active",
			OwnerType: "user", OwnerID: 1, CliType: "claude",
			IssueID: sql.NullInt64{Int64: issue.ID, Valid: true},
		})
		if werr != nil {
			t.Fatalf("CreateWorkspace: %v", werr)
		}
		return ws.ID
	}
	wsA := newWS("ws-a", 0)
	wsB := newWS("ws-b", 1)

	// A project-bound, enabled KB — the fallback candidate.
	pbSrc := t.TempDir()
	writeKBFile(t, pbSrc, "p.md", "项目绑定内容\n")
	pb, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "proj-kb", SourceKind: "local", SourceAddr: pbSrc})
	bindKB(t, svc, pb.ID, proj.ID)

	// wsB has NO explicit mount → falls back to the project-bound KB.
	dirsB, err := svc.WorkspaceDatasetDirs(ctx, wsB)
	if err != nil || len(dirsB) != 1 || dirsB[0].KBID != pb.ID {
		t.Fatalf("wsB should fall back to the project-bound KB, got %+v (err=%v)", dirsB, err)
	}

	// wsA explicitly mounts a KB, then that KB is disabled. Its explicit mount
	// must NOT trigger the project fallback → empty dirs.
	kbSrc := t.TempDir()
	writeKBFile(t, kbSrc, "k.md", "挂载内容\n")
	kb, _ := svc.CreateKB(ctx, owner, CreateKBParams{Name: "mounted", SourceKind: "local", SourceAddr: kbSrc})
	if _, err := svc.MountKB(ctx, owner, wsA, kb.ID); err != nil {
		t.Fatalf("MountKB: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `UPDATE knowledge_bases SET status = 'disabled' WHERE id = ?`, kb.ID); err != nil {
		t.Fatalf("disable KB: %v", err)
	}
	dirsA, err := svc.WorkspaceDatasetDirs(ctx, wsA)
	if err != nil {
		t.Fatalf("WorkspaceDatasetDirs A: %v", err)
	}
	if len(dirsA) != 0 {
		t.Fatalf("wsA has an explicit (disabled) mount and must NOT fall back to project KBs, got %+v", dirsA)
	}
}
