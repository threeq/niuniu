package memory

import (
	"strings"
	"testing"
)

// RecallFor：任务相关评分优先；无命中回退最近；字节上限保持。
func TestRecallForPrefersRelevant(t *testing.T) {
	s := testStore(t)
	s.Save(entry("json-format-tips", "pattern", "处理 JSON 时先用解析器校验再改写", "json"))
	s.Save(entry("deploy-gate", "decision", "发布前必须过 harness gate", "deploy"))
	s.Save(entry("sql-index-gotcha", "gotcha", "大表加索引要 CONCURRENTLY", "sql"))

	// 任务 hint 与 JSON 相关：json 条目排第一，deploy 不出现（无关+超窗）。
	out, err := s.RecallFor("修改 JSON 配置解析", 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "- [pattern] json-format-tips") {
		t.Errorf("top recall = %q, want json-format-tips first", out)
	}
	if strings.Contains(out, "deploy-gate") {
		t.Errorf("irrelevant entry recalled within window:\n%s", out)
	}

	// 无命中 hint：回退最近（recency）而不是空。
	out2, err := s.RecallFor("完全不相关的话题 quantum", 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if out2 == "" {
		t.Fatal("fallback recall empty")
	}
	if strings.Contains(out2, "json-format-tips") {
		t.Errorf("fallback should prefer other entries over matched-first:\n%s", out2)
	}
}

func TestRecallForCap(t *testing.T) {
	s := testStore(t)
	s.Save(entry("long-entry", "ref", strings.Repeat("y", 3000)))
	out, err := s.RecallFor("long", 5, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 300 {
		t.Errorf("cap ignored: %d bytes", len(out))
	}
}

// 分层语义文档化测试：工作记忆（todo 文件）与经验记忆互不干扰——
// 经验召回不读 todos.json；todo 工具状态由 compact 摘要保留（P2-⑤）。
func TestLayerSeparation(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save(entry("code-style", "pattern", "用表驱动测试")); err != nil {
		t.Fatal(err)
	}
	out, err := s.RecallFor("todo 任务清单状态", 5, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "todos.json") {
		t.Errorf("experience recall leaked working-memory concerns: %s", out)
	}
}
