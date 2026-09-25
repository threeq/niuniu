package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(dir, name, content string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func TestConsolidateMergesSameTopic(t *testing.T) {
	s := testStore(t)
	s.Save(entry("deploy-gate-v1", "decision", "v1 内容", "deploy"))
	s.Save(entry("deploy-gate-v2", "decision", "v2 内容", "deploy"))
	s.Save(entry("unrelated", "gotcha", "别的主题", "misc"))

	res, err := s.Consolidate(ConsolidateOptions{MergeSimilar: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Merged != 1 {
		t.Errorf("merged = %d, want 1 (group of 2 folds into 1)", res.Merged)
	}
	all, _ := s.Search("")
	if len(all) != 2 { // merged entry + unrelated
		t.Fatalf("entries = %d, want 2", len(all))
	}
	var merged *Entry
	for i := range all {
		if strings.Contains(all[i].Title, "deploy") {
			merged = &all[i]
		}
	}
	if merged == nil {
		t.Fatal("merged entry missing")
	}
	// 合并条目保留全部原文（按 title 分行）。
	if !strings.Contains(merged.Content, "v1 内容") || !strings.Contains(merged.Content, "v2 内容") {
		t.Errorf("merged content lost originals: %q", merged.Content)
	}
	if len(merged.Tags) != 1 || merged.Tags[0] != "deploy" {
		t.Errorf("merged tags = %v", merged.Tags)
	}
}

func TestConsolidateExpiresOld(t *testing.T) {
	s := testStore(t)
	s.Save(entry("fresh", "ref", "新"))
	if err := writeFile(s.projectDir, "ancient.md", "---\ntitle: ancient\ntype: ref\ntags: \ncreated: 2020-01-01T00:00:00Z\nupdated: 2020-01-01T00:00:00Z\n---\n\n老掉牙\n"); err != nil {
		t.Fatal(err)
	}

	res, err := s.Consolidate(ConsolidateOptions{MaxAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if res.Expired != 1 {
		t.Errorf("expired = %d, want 1", res.Expired)
	}
	all, _ := s.Search("")
	if len(all) != 1 || all[0].Title != "fresh" {
		t.Fatalf("entries = %+v", all)
	}
}

func TestConsolidateLRUEviction(t *testing.T) {
	s := testStore(t)
	s.MaxEntriesPerLayer = 2
	// 手写三个不同 updated 的条目文件（Save 只挡新增不淘汰，淘汰由
	// Consolidate 负责）。
	for _, e := range []struct{ id, updated string }{
		{"old-a", "2020-03-03T00:00:03Z"},
		{"mid-b", "2020-03-03T00:00:02Z"},
		{"new-c", "2020-03-03T00:00:01Z"},
	} {
		body := fmt.Sprintf("---\ntitle: %s\ntype: ref\ntags: \ncreated: %s\nupdated: %s\n---\n\n%s body\n",
			e.id, e.updated, e.updated, e.id)
		if err := writeFile(s.projectDir, e.id+".md", body); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.Consolidate(ConsolidateOptions{MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Evicted != 1 {
		t.Errorf("evicted = %d, want 1", res.Evicted)
	}
	all, _ := s.Search("")
	if len(all) != 2 {
		t.Fatalf("entries = %d, want 2", len(all))
	}
	for _, e := range all {
		// new-c 的 updated 最旧（00:00:01），LRU 首个淘汰。
		if e.Title == "new-c" {
			t.Errorf("oldest-updated entry must be evicted first, got %+v", all)
		}
	}
}

func TestMemoryConsolidateTool(t *testing.T) {
	s := testStore(t)
	s.Save(entry("t1", "decision", "x", "k"))
	s.Save(entry("t2", "decision", "y", "k"))
	tool := NewConsolidateTool(s)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "merged=1") {
		t.Errorf("tool output = %q", out)
	}
}
