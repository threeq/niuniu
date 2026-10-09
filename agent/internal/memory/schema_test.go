package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLifecycleDomainRoundTrip(t *testing.T) {
	s := testStore(t)
	due := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	_, err := s.Save(Entry{
		Title:     "interview-prep",
		Type:      TypeRef,
		Domain:    DomainOpenItem,
		Content:   "明天上午的产品面试，带作品集。",
		Lifecycle: LifecycleOpen,
		ExpiresAt: due,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	hits, err := s.Search("面试")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	e := hits[0]
	if e.Domain != DomainOpenItem {
		t.Errorf("Domain = %q, want %q", e.Domain, DomainOpenItem)
	}
	if e.Lifecycle != LifecycleOpen {
		t.Errorf("Lifecycle = %q, want open", e.Lifecycle)
	}
	if !e.ExpiresAt.Equal(due) {
		t.Errorf("ExpiresAt = %v, want %v", e.ExpiresAt, due)
	}

	// 落盘 frontmatter 带新字段，expires_at 为 RFC3339。
	data, err := os.ReadFile(filepath.Join(s.projectDir, "interview-prep.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"domain: open_item", "lifecycle: open", "expires_at: 2026-10-20T09:00:00Z"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("entry file missing %q:\n%s", want, data)
		}
	}

	// 全字段 round-trip：非默认 lifecycle 也原样读回。
	if _, err := s.Save(Entry{
		Title:     "old-pnpm-rule",
		Type:      TypeDecision,
		Domain:    DomainPreference,
		Content:   "已被新决策取代。",
		Lifecycle: LifecycleDeprecated,
	}); err != nil {
		t.Fatalf("Save deprecated: %v", err)
	}
	all, _ := s.Search("取代")
	if len(all) != 1 || all[0].Lifecycle != LifecycleDeprecated || all[0].Domain != DomainPreference {
		t.Fatalf("entries = %+v", all)
	}
}

func TestOldFormatFilesReadAsOpen(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(s.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 旧格式：没有 lifecycle / expires_at / domain 字段。
	old := "---\ntitle: legacy-entry\ntype: gotcha\ntags: win\ncreated: 2025-01-01T00:00:00Z\nupdated: 2025-01-01T00:00:00Z\n---\n\n旧版本写入的条目，无需迁移。\n"
	if err := os.WriteFile(filepath.Join(s.projectDir, "legacy-entry.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("无需迁移")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	e := hits[0]
	if e.Lifecycle != LifecycleOpen {
		t.Errorf("Lifecycle = %q, want default open", e.Lifecycle)
	}
	if !e.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero", e.ExpiresAt)
	}
	if e.Domain != "" {
		t.Errorf("Domain = %q, want empty", e.Domain)
	}
}

func TestUnknownLifecycleKeptVerbatim(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(s.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	future := "---\ntitle: exotic\ntype: ref\ndomain: weird-domain\nlifecycle: archived-v2\nexpires_at: not-a-time\ntags: \ncreated: 2025-01-01T00:00:00Z\nupdated: 2025-01-01T00:00:00Z\n---\n\n未知取值原样保留，不报错。\n"
	if err := os.WriteFile(filepath.Join(s.projectDir, "exotic.md"), []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("原样保留")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Lifecycle != "archived-v2" {
		t.Errorf("Lifecycle = %q, want kept verbatim", hits[0].Lifecycle)
	}
	if hits[0].Domain != "weird-domain" {
		t.Errorf("Domain = %q, want kept verbatim", hits[0].Domain)
	}
	if !hits[0].ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero for unparseable value", hits[0].ExpiresAt)
	}
}

func TestUpdatePreservesLifecycleUnlessSet(t *testing.T) {
	s := testStore(t)
	due := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Save(Entry{
		Title:     "migration-plan",
		Type:      TypeDecision,
		Domain:    DomainOpenItem,
		Content:   "v1 计划",
		Lifecycle: LifecycleOpen,
		ExpiresAt: due,
	}); err != nil {
		t.Fatal(err)
	}

	// 内容更新未带 lifecycle/expires_at → 保留原状态，不悄悄重开/清空。
	if _, err := s.Save(Entry{Title: "migration-plan", Type: TypeDecision, Content: "v2 计划"}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Search("v2 计划")
	if len(all) != 1 || all[0].Lifecycle != LifecycleOpen || !all[0].ExpiresAt.Equal(due) {
		t.Fatalf("after plain update = %+v, want lifecycle/expires_at preserved", all)
	}

	// 显式 lifecycle → 生效；expires_at 未带 → 仍保留。
	if _, err := s.Save(Entry{Title: "migration-plan", Type: TypeDecision, Content: "v3 计划", Lifecycle: LifecycleDone}); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Search("v3 计划")
	if len(all) != 1 || all[0].Lifecycle != LifecycleDone || !all[0].ExpiresAt.Equal(due) {
		t.Fatalf("after explicit close = %+v", all)
	}

	// 新条目空 lifecycle → 默认 open。
	if _, err := s.Save(Entry{Title: "fresh-entry", Type: TypePattern, Content: "新条目"}); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Search("新条目")
	if len(all) != 1 || all[0].Lifecycle != LifecycleOpen {
		t.Fatalf("new entry = %+v, want default open", all)
	}
}

// NewStoreLayers：显式指定两层目录，空目录 = 该层缺席。eval 沙箱用它召回
// fixture 而不把宿主 user 层二次注入（宿主 project 层 store 已含 user 层）。
func TestNewStoreLayersEmptyUserDir(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoreLayers(projectDir, "").Save(Entry{
		Title: "sandbox-fixture", Type: TypeUser, Domain: DomainOpenItem,
		Content: "沙箱内的进行中事项",
	}); err != nil {
		t.Fatal(err)
	}
	s := NewStoreLayers(projectDir, "")
	out, err := s.Recall(5, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sandbox-fixture") {
		t.Errorf("project-layer fixture missing from recall:\n%s", out)
	}
}
