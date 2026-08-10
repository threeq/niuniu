package service

import (
	"context"
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
