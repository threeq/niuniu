package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	s.MaxEntriesPerLayer = 100 // tests set tighter limits explicitly
	return s
}

func setUserHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func entry(title, typ, content string, tags ...string) Entry {
	return Entry{Title: title, Type: typ, Content: content, Tags: tags}
}

func TestSaveAndSearch(t *testing.T) {
	s := testStore(t)
	id1, err := s.Save(entry("pnpm-not-npm", "decision", "本项目用 pnpm 安装依赖，不要用 npm。", "build", "node"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id1 == "" {
		t.Fatal("empty id")
	}
	if _, err := s.Save(entry("win-paths", "gotcha", "Windows 路径分隔符问题在 glob 里要转义。")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 关键词命中：pnpm 条目排前。
	hits, err := s.Search("pnpm 依赖")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Title != "pnpm-not-npm" {
		t.Fatalf("hits = %+v", hits)
	}
	// 空查询 = 全部，按 updated 新者优先。
	all, err := s.Search("")
	if err != nil {
		t.Fatalf("Search all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %d entries", len(all))
	}
	// 条目文件落盘为 markdown + frontmatter。
	data, err := os.ReadFile(filepath.Join(s.projectDir, id1+".md"))
	if err != nil {
		t.Fatalf("read entry file: %v", err)
	}
	for _, want := range []string{"title: pnpm-not-npm", "type: decision", "tags: build,node", "pnpm 安装依赖"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("entry file missing %q:\n%s", want, data)
		}
	}
}

func TestSaveUpdatesByTitle(t *testing.T) {
	s := testStore(t)
	id1, _ := s.Save(entry("deploy-gate", "decision", "v1"))
	id2, err := s.Save(entry("deploy-gate", "decision", "v2 — 必须先过 harness gate"))
	if err != nil {
		t.Fatalf("Save again: %v", err)
	}
	if id1 != id2 {
		t.Errorf("id changed: %q vs %q (same title must update in place)", id1, id2)
	}
	all, _ := s.Search("")
	if len(all) != 1 || !strings.Contains(all[0].Content, "v2") {
		t.Fatalf("entries = %+v, want single updated entry", all)
	}
}

func TestTwoLayersProjectShadowsUser(t *testing.T) {
	home := t.TempDir()
	setUserHome(t, home)
	cwd := t.TempDir()
	s := NewStore(cwd)
	s.MaxEntriesPerLayer = 100

	// user 层预置：直接写文件（模拟其它会话沉淀的全局记忆）。
	userStore := &Store{projectDir: filepath.Join(home, ".niuniu-agent", "memory"), userDir: filepath.Join(home, ".niuniu-agent", "memory"), MaxEntriesPerLayer: 100}
	if _, err := userStore.Save(entry("shared-topic", "pattern", "USER COPY — should be shadowed")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(entry("shared-topic", "pattern", "PROJECT COPY — wins")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(entry("global-only", "ref", "只在全局层")); err != nil {
		t.Fatal(err)
	}

	all, err := s.Search("topic")
	if err != nil {
		t.Fatal(err)
	}
	var shared *Entry
	for i := range all {
		if all[i].Title == "shared-topic" {
			shared = &all[i]
		}
	}
	if shared == nil || !strings.Contains(shared.Content, "PROJECT COPY") {
		t.Fatalf("project entry must shadow user entry, got %+v", all)
	}
	// user 层条目仍可见。
	globals, _ := s.Search("全局层")
	if len(globals) != 1 {
		t.Fatalf("user-layer entry missing: %+v", globals)
	}
}

func TestLimits(t *testing.T) {
	s := testStore(t)
	s.MaxEntryBytes = 64
	if _, err := s.Save(entry("too-big", "gotcha", strings.Repeat("x", 100))); err == nil {
		t.Error("want error for oversized entry")
	}
	s.MaxEntryBytes = 8192
	s.MaxEntriesPerLayer = 2
	if _, err := s.Save(entry("a", "ref", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(entry("b", "ref", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(entry("c", "ref", "x")); err == nil {
		t.Error("want error when layer is full")
	}
	// 更新已有条目不受条目数上限影响。
	if _, err := s.Save(entry("a", "ref", "x2")); err != nil {
		t.Errorf("update within full layer must work: %v", err)
	}
}

func TestRecallRankedAndCapped(t *testing.T) {
	s := testStore(t)
	// 手写一条 updated 很旧的条目（frontmatter 格式见 render）。
	if err := os.MkdirAll(s.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "---\ntitle: old-hit\ntype: gotcha\ntags: \ncreated: 2020-01-01T00:00:00Z\nupdated: 2020-01-01T00:00:00Z\n---\n\npnpm 相关的旧教训\n"
	if err := os.WriteFile(filepath.Join(s.projectDir, "old-hit.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = time.Now
	s.Save(entry("new-hit", "decision", "pnpm 的新决策"))
	s.Save(entry("unrelated", "ref", "完全不相关的内容"))

	out, err := s.Recall(2, 4096)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if !strings.Contains(out, "new-hit") {
		t.Errorf("recall missing top hit:\n%s", out)
	}
	if strings.Count(out, "\ntitle: ") > 2 && strings.Count(out, "title: ") > 2 {
		t.Errorf("recall exceeded topN:\n%s", out)
	}

	// 字节上限：截断到很小。
	small, err := s.Recall(10, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(small) > 400 {
		t.Errorf("recall ignored byte cap: %d bytes", len(small))
	}

	// 空库。
	empty := NewStore(t.TempDir())
	if out, _ := empty.Recall(5, 2048); out != "" {
		t.Errorf("empty store recall = %q, want empty", out)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Hello World":  "hello-world",
		"Win: a/b\\c?": "win-a-b-c",
		"  trim  me  ": "trim-me",
		"记忆测试":         "记忆测试",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	s := testStore(t)
	if _, err := s.Save(entry("///", "ref", "x")); err == nil {
		t.Error("want error for unsluggable title")
	}
}
