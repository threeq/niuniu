package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// 召回过滤：closed lifecycle（done/cancelled/expired/deprecated）不再进入
// Recall / RecallFor 的注入结果；MemorySearch 默认同样跳过。
func TestRecallSkipsClosedLifecycle(t *testing.T) {
	s := testStore(t)
	live := []Entry{
		{Title: "live-pattern", Type: TypePattern, Content: "还活着的教训"},
		{Title: "live-pref", Type: TypeUser, Domain: DomainPreference, Content: "用户偏好"},
	}
	closed := []Entry{
		{Title: "done-entry", Type: TypeDecision, Content: "已完成", Lifecycle: LifecycleDone},
		{Title: "cancelled-entry", Type: TypeDecision, Content: "已撤回", Lifecycle: LifecycleCancelled},
		{Title: "expired-entry", Type: TypeDecision, Content: "已过期", Lifecycle: LifecycleExpired},
		{Title: "deprecated-entry", Type: TypeDecision, Content: "已废弃", Lifecycle: LifecycleDeprecated},
	}
	for _, e := range append(live, closed...) {
		if _, err := s.Save(e); err != nil {
			t.Fatalf("Save %s: %v", e.Title, err)
		}
	}

	out, err := s.Recall(10, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range live {
		if !strings.Contains(out, e.Title) {
			t.Errorf("live entry %q missing from recall:\n%s", e.Title, out)
		}
	}
	for _, e := range closed {
		if strings.Contains(out, e.Title) {
			t.Errorf("closed entry %q must not be recalled:\n%s", e.Title, out)
		}
	}

	out2, err := s.RecallFor("还活着的教训", 10, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "live-pattern") {
		t.Errorf("RecallFor missing live entry:\n%s", out2)
	}
	if strings.Contains(out2, "done-entry") || strings.Contains(out2, "deprecated-entry") {
		t.Errorf("RecallFor leaked closed entries:\n%s", out2)
	}
}

// 惰性过期：lifecycle=open 但 ExpiresAt 已过的条目，读取/召回时按 expired
// 处理（判定在读写路径上，无后台任务）。
func TestLazyExpiryOnRead(t *testing.T) {
	s := testStore(t)
	past := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	future := time.Date(2099, 10, 20, 12, 0, 0, 0, time.UTC)
	if _, err := s.Save(Entry{
		Title: "tomorrow-interview", Type: TypeRef, Domain: DomainOpenItem,
		Content: "明天面试，带作品集。", Lifecycle: LifecycleOpen, ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{
		Title: "later-review", Type: TypeRef, Domain: DomainOpenItem,
		Content: "尚未到期。", Lifecycle: LifecycleOpen, ExpiresAt: future,
	}); err != nil {
		t.Fatal(err)
	}

	// 读取路径：条目原样呈现磁盘状态（stored lifecycle=open）——惰性过期
	// 判定只在消费点折叠，绝不回写字段本身（否则 consolidate 重写幸存
	// 条目时会把 expired 盖章泄漏到磁盘）。
	hits, err := s.Search("面试")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Lifecycle != LifecycleOpen {
		t.Fatalf("expired-by-time entry reads as %+v, want stored lifecycle=open", hits)
	}
	if got := effectiveLifecycle(hits[0], time.Now()); got != LifecycleExpired {
		t.Fatalf("effective lifecycle = %q, want %q", got, LifecycleExpired)
	}

	// 召回路径：过期条目停注入，未到期条目保留。
	out, err := s.Recall(10, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "tomorrow-interview") {
		t.Errorf("past-due entry must stop being recalled:\n%s", out)
	}
	if !strings.Contains(out, "later-review") {
		t.Errorf("unexpired open_item missing from recall:\n%s", out)
	}
}

// open_item 标注：未过期的 open_item 在注入体里带状态标注，便于模型识别
// 待回访信号。
func TestRecallAnnotatesOpenItems(t *testing.T) {
	s := testStore(t)
	future := time.Date(2099, 10, 20, 12, 0, 0, 0, time.UTC)
	if _, err := s.Save(Entry{
		Title: "interview", Type: TypeRef, Domain: DomainOpenItem,
		Content: "产品面试", Lifecycle: LifecycleOpen, ExpiresAt: future,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{
		Title: "follow-up", Type: TypeRef, Domain: DomainOpenItem,
		Content: "无明确期限的回访", Lifecycle: LifecycleOpen,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{Title: "plain", Type: TypePattern, Content: "普通条目"}); err != nil {
		t.Fatal(err)
	}

	out, err := s.Recall(10, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "interview: 产品面试 [open, due 2099-10-20]") {
		t.Errorf("due open_item missing [open, due …] annotation:\n%s", out)
	}
	if !strings.Contains(out, "follow-up: 无明确期限的回访 [open]") {
		t.Errorf("due-less open_item missing [open] annotation:\n%s", out)
	}
	if strings.Count(out, "[open") != 2 {
		t.Errorf("non-open-item entries must not be annotated:\n%s", out)
	}
}

// 分域路由：preference/decision/open_item 等信号域优先；environment/other/
// 未分类域降权且限流（有信号域条目时最多注入 1 条低权域）；全是低权域时
// 不设限（存量库不塌缩）。
func TestRecallRoutesDomains(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save(Entry{Title: "pref-vim", Type: TypeUser, Domain: DomainPreference, Content: "偏好"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{Title: "decision-pnpm", Type: TypeDecision, Domain: DomainDecision, Content: "决策"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{Title: "open-follow", Type: TypeRef, Domain: DomainOpenItem, Content: "待回访"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{Title: "env-shell", Type: TypePattern, Domain: DomainEnvironment, Content: "环境习惯"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{Title: "other-note", Type: TypePattern, Domain: DomainOther, Content: "其它"}); err != nil {
		t.Fatal(err)
	}

	out, err := s.Recall(10, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pref-vim", "decision-pnpm", "open-follow"} {
		if !strings.Contains(out, want) {
			t.Errorf("signal-domain entry %q must be recalled:\n%s", want, out)
		}
	}
	lowCount := strings.Count(out, "env-shell") + strings.Count(out, "other-note")
	if lowCount > 1 {
		t.Errorf("low-priority domains must be capped at 1, got %d:\n%s", lowCount, out)
	}
	// 信号域条目应排在低权域之前（分域优先，非仅过滤）。
	if strings.Index(out, "env-shell") != -1 && strings.Index(out, "env-shell") < strings.Index(out, "pref-vim") {
		t.Errorf("low-priority domain ranked above signal domains:\n%s", out)
	}

	// 全是低权域（存量未分类库）：不限流，召回不塌缩。
	s2 := testStore(t)
	for _, title := range []string{"legacy-a", "legacy-b", "legacy-c"} {
		if _, err := s2.Save(Entry{Title: title, Type: TypePattern, Content: "旧条目 " + title}); err != nil {
			t.Fatal(err)
		}
	}
	out2, err := s2.Recall(3, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"legacy-a", "legacy-b", "legacy-c"} {
		if !strings.Contains(out2, title) {
			t.Errorf("all-legacy store must keep recalling (no domain cap), missing %q:\n%s", title, out2)
		}
	}
}

// MemorySearch 工具默认只列 live 条目；显式 lifecycle 过滤可查 closed
// （用于找回并修正旧记忆）。
func TestMemorySearchDefaultsToLive(t *testing.T) {
	s := testStore(t)
	reg := newTestRegistry(t, s)
	if _, err := reg.Execute(testCtx(), "MemorySave",
		jsonRaw(`{"title":"live-rule","type":"decision","content":"仍然有效"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Execute(testCtx(), "MemorySave",
		jsonRaw(`{"title":"stale-rule","type":"decision","lifecycle":"deprecated","content":"已被取代"}`)); err != nil {
		t.Fatal(err)
	}

	// 默认：closed 不出现。
	def, err := reg.Execute(testCtx(), "MemorySearch", jsonRaw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "live-rule") {
		t.Errorf("default search missing live entry:\n%s", def)
	}
	if strings.Contains(def, "stale-rule") {
		t.Errorf("default search must skip closed entries:\n%s", def)
	}

	// 显式 lifecycle 过滤：仍能查到 closed 条目（找回去更新）。
	explicit, err := reg.Execute(testCtx(), "MemorySearch", jsonRaw(`{"lifecycle":"deprecated"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explicit, "stale-rule") {
		t.Errorf("explicit lifecycle filter must still find closed entries:\n%s", explicit)
	}
}

func newTestRegistry(t *testing.T, s *Store) *tools.Registry {
	t.Helper()
	return tools.NewRegistry(NewSaveTool(s), NewSearchTool(s))
}

func testCtx() context.Context { return context.Background() }

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

// Section 固定段包含 advisory 确认句式规范（缓存友好：文字固定）与
// open_item 标注图例。
func TestSectionAdvisoryPhrasing(t *testing.T) {
	sec := Section("- [pattern] x: y")
	for _, want := range []string{"之前记录", "沿用吗", "[open, due"} {
		if !strings.Contains(sec, want) {
			t.Errorf("section missing advisory guidance %q:\n%s", want, sec)
		}
	}
}
