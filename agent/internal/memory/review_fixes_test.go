package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// —— P0：更新保留被省略字段（纠错协议规定的 {title,content,lifecycle} 形态）——

// 部分重存（如当轮纠错的 deprecate 调用，只带 title/content/lifecycle）不得
// 静默重分类条目或清掉 domain/tags——这些字段省略时应保留磁盘现值。
func TestUpdatePreservesOmittedTypeDomainTags(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save(Entry{
		Title: "coffee-preference", Type: TypeUser, Domain: DomainPreference,
		Tags: []string{"coffee", "drink"}, Content: "用户喜欢拿铁。",
	}); err != nil {
		t.Fatal(err)
	}
	// 纠错协议形态：只带 title/content/lifecycle。
	if _, err := s.Save(Entry{
		Title: "coffee-preference", Content: "用户喜欢拿铁。", Lifecycle: LifecycleDeprecated,
	}); err != nil {
		t.Fatal(err)
	}
	e := findByTitle(t, s, "coffee-preference")
	if e.Type != TypeUser || e.Domain != DomainPreference {
		t.Errorf("partial re-save erased classification: type=%q domain=%q", e.Type, e.Domain)
	}
	if len(e.Tags) != 2 || e.Tags[0] != "coffee" {
		t.Errorf("partial re-save erased tags: %v", e.Tags)
	}
	if e.Lifecycle != LifecycleDeprecated {
		t.Errorf("explicit lifecycle must win: %q", e.Lifecycle)
	}

	// 纯内容编辑（连 lifecycle 也不带）：生命周期与 domain 同样保留。
	if _, err := s.Save(Entry{Title: "coffee-preference", Content: "用户喜欢拿铁，双份浓缩。"}); err != nil {
		t.Fatal(err)
	}
	e = findByTitle(t, s, "coffee-preference")
	if e.Lifecycle != LifecycleDeprecated || e.Domain != DomainPreference {
		t.Errorf("content edit reopened/reclassified entry: lifecycle=%q domain=%q", e.Lifecycle, e.Domain)
	}

	// 显式值始终优先：换 domain 应当生效。
	if _, err := s.Save(Entry{Title: "coffee-preference", Content: "x", Domain: DomainOther}); err != nil {
		t.Fatal(err)
	}
	if e = findByTitle(t, s, "coffee-preference"); e.Domain != DomainOther {
		t.Errorf("explicit domain must win: %q", e.Domain)
	}
}

// 标题里的换行不得注入 frontmatter 键（如伪造 expires_at 让条目静默过期）。
func TestSaveTitleCannotInjectFrontmatter(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save(Entry{
		Title: "evil\nexpires_at: 2000-01-01T00:00:00Z", Type: TypeRef, Content: "x",
	}); err != nil {
		t.Fatal(err)
	}
	all := mustSearchAll(t, s)
	if len(all) != 1 {
		t.Fatalf("entries = %+v", all)
	}
	if strings.ContainsAny(all[0].Title, "\r\n") {
		t.Errorf("title kept line breaks: %q", all[0].Title)
	}
	if !all[0].ExpiresAt.IsZero() {
		t.Errorf("forged expires_at took effect: %v", all[0].ExpiresAt)
	}
}

// —— P0：expires_at 语义（重开 / none / date-only）——

// 显式 lifecycle:"open" 重开已过期条目必须真正生效：死掉的截止时间被清除，
// 否则惰性过期判定会在下一次读取时立刻再次关闭它（工具却还宣称会被召回）。
func TestReopenClearsPastDeadline(t *testing.T) {
	s := testStore(t)
	past := time.Now().Add(-48 * time.Hour)
	if _, err := s.Save(Entry{
		Title: "interview", Type: TypeRef, Domain: DomainOpenItem,
		Content: "产品面试", Lifecycle: LifecycleOpen, ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Recall(10, 1<<16); strings.Contains(out, "interview") {
		t.Fatalf("past-due entry must not be recalled:\n%s", out)
	}

	if _, err := s.Save(Entry{
		Title: "interview", Content: "产品面试（改到下周）", Lifecycle: LifecycleOpen,
	}); err != nil {
		t.Fatal(err)
	}
	e := findByTitle(t, s, "interview")
	if !e.ExpiresAt.IsZero() {
		t.Errorf("dead deadline must be cleared on reopen, got %v", e.ExpiresAt)
	}
	if out, _ := s.Recall(10, 1<<16); !strings.Contains(out, "interview") {
		t.Errorf("reopened entry must be recalled again:\n%s", out)
	}
}

// SaveTool 的截止时间解析：date-only 覆盖本地全天（不再被静默忽略）、
// "none" 显式清除、非法值报错。
func TestSaveToolDeadlineFormats(t *testing.T) {
	s := testStore(t)
	save := NewSaveTool(s)
	ctx := context.Background()

	dateOnly := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	if _, err := save.Execute(ctx, json.RawMessage(
		`{"title":"t1","content":"c","domain":"open_item","expires_at":"`+dateOnly+`"}`)); err != nil {
		t.Fatalf("date-only deadline rejected: %v", err)
	}
	e := findByTitle(t, s, "t1")
	if e.ExpiresAt.IsZero() {
		t.Fatal("date-only deadline was silently ignored")
	}
	if got := e.ExpiresAt.Local().Format("2006-01-02"); got != dateOnly {
		t.Errorf("deadline day = %s, want %s", got, dateOnly)
	}
	if h := e.ExpiresAt.Local().Hour(); h != 23 {
		t.Errorf("deadline hour = %d, want end of local day (23)", h)
	}

	// "none" 清除既有截止时间。
	if _, err := save.Execute(ctx, json.RawMessage(
		`{"title":"t1","content":"c","expires_at":"none"}`)); err != nil {
		t.Fatal(err)
	}
	if e = findByTitle(t, s, "t1"); !e.ExpiresAt.IsZero() {
		t.Errorf("expires_at none must clear the deadline, got %v", e.ExpiresAt)
	}

	// 非法值必须报错，不得静默当无期限。
	if _, err := save.Execute(ctx, json.RawMessage(
		`{"title":"t1","content":"c","expires_at":"下周"}`)); err == nil {
		t.Error("invalid expires_at must error")
	}
}

// —— P0：惰性过期只在消费点折叠，绝不落盘 ——

// consolidate 会重写全部幸存条目；loadAll 若把惰性过期盖章到字段上，
// 标记就会随重写泄漏到磁盘，把条目永久关闭（注释却宣称"从不回写文件"）。
func TestConsolidateDoesNotStampLazyExpiryToDisk(t *testing.T) {
	s := testStore(t)
	past := time.Now().Add(-24 * time.Hour)
	if _, err := s.Save(Entry{
		Title: "due-item", Type: TypeRef, Domain: DomainOpenItem,
		Content: "过期事项", Lifecycle: LifecycleOpen, ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consolidate(ConsolidateOptions{MergeSimilar: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.projectDir, "due-item.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "lifecycle: open") {
		t.Errorf("lazy expiry leaked to disk:\n%s", data)
	}
	if !strings.Contains(string(data), "expires_at:") {
		t.Errorf("deadline line lost on rewrite:\n%s", data)
	}
}

// —— P0：consolidate 生命周期/域感知合并 ——

// 当轮纠错把旧条目打 deprecated 另存新条目后，consolidate 不得把被撤回的
// 内容并回活跃条目（也不得删除 deprecated 文件）——否则纠错被"复活"。
func TestConsolidateDoesNotResurrectDeprecated(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save(Entry{
		Title: "coffee-preference", Type: TypeUser, Domain: DomainPreference, Content: "用户喜欢拿铁。",
	}); err != nil {
		t.Fatal(err)
	}
	// 纠错协议：deprecate 旧条目 + 新事实另存。
	if _, err := s.Save(Entry{
		Title: "coffee-preference", Content: "用户喜欢拿铁。", Lifecycle: LifecycleDeprecated,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{
		Title: "americano-preference", Type: TypeUser, Domain: DomainPreference, Content: "用户改喝美式了。",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Consolidate(ConsolidateOptions{MergeSimilar: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 {
		t.Errorf("closed entry merged: %+v", res)
	}
	old := findByTitle(t, s, "coffee-preference")
	if old.Lifecycle != LifecycleDeprecated {
		t.Errorf("deprecated entry state changed: %q", old.Lifecycle)
	}
	if _, err := os.Stat(filepath.Join(s.projectDir, "coffee-preference.md")); err != nil {
		t.Errorf("deprecated file removed by merge: %v", err)
	}
	live := findByTitle(t, s, "americano-preference")
	if strings.Contains(live.Content, "拿铁") {
		t.Errorf("retracted content resurrected into live entry: %q", live.Content)
	}
	if out, _ := s.Recall(10, 1<<16); strings.Contains(out, "拿铁") {
		t.Errorf("retracted fact still recalled:\n%s", out)
	}
}

// 合并键含 domain：同型同标签但不同域的条目不得互相折叠。
func TestConsolidateMergesOnlySameDomain(t *testing.T) {
	s := testStore(t)
	save := func(title, domain string) {
		t.Helper()
		if _, err := s.Save(Entry{
			Title: title, Type: TypePattern, Domain: domain, Tags: []string{"go"}, Content: "内容-" + title,
		}); err != nil {
			t.Fatal(err)
		}
	}
	save("pattern-decision", DomainDecision)
	save("pattern-env", DomainEnvironment)
	res, err := s.Consolidate(ConsolidateOptions{MergeSimilar: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 {
		t.Errorf("cross-domain merge happened: %+v", res)
	}
	if all := mustSearchAll(t, s); len(all) != 2 {
		t.Errorf("entries = %d, want both kept", len(all))
	}

	save("pattern-decision-2", DomainDecision)
	res, err = s.Consolidate(ConsolidateOptions{MergeSimilar: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 1 {
		t.Errorf("same-domain merge = %+v, want 1", res)
	}
}

// —— P1：召回路由 ——

// 无域（""）是未分类而非噪声：旧数据不得因为出现了一条带域条目就整体挨限流。
func TestRecallLegacyDomainlessNotStarved(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.Save(Entry{
			Title: fmt.Sprintf("legacy-%d", i), Type: TypePattern, Content: "旧记忆",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Save(Entry{
		Title: "decision-a", Type: TypeDecision, Domain: DomainDecision, Content: "决策",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Recall(3, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "- ["); n != 3 {
		t.Errorf("recall lines = %d, want 3 (legacy entries must not be capped as noise):\n%s", n, out)
	}
}

// 噪声域限流必须原位进行：与任务最相关的 environment 条目不能被挤到
// 队尾、被 topN 截断悄悄丢掉。
func TestRecallKeepsTopRelevanceNoiseEntry(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Save(Entry{
			Title: fmt.Sprintf("decision-%d", i), Type: TypeDecision, Domain: DomainDecision, Content: "无关决策事项",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Save(Entry{
		Title: "windows-path-quirk", Type: TypeGotcha, Domain: DomainEnvironment,
		Content: "windows powershell 路径处理要用反斜杠",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.RecallFor("windows powershell 路径", 3, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "windows-path-quirk") {
		t.Errorf("top-relevance environment entry must survive the cap:\n%s", out)
	}
}

// —— P1：时区与解析 ——

// date-only 覆盖本地全天；RFC3339 照常；垃圾值归零。
func TestParseTimeDateOnlyCoversLocalDay(t *testing.T) {
	got := parseTime("2026-10-20")
	want := time.Date(2026, 10, 20, 23, 59, 59, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("parseTime(date-only) = %v, want %v", got, want)
	}
	if parseTime("2026-10-20T09:00:00Z").IsZero() {
		t.Error("RFC3339 no longer parses")
	}
	if !parseTime("下周").IsZero() {
		t.Error("garbage must degrade to zero")
	}
}

// 到期标注按本地日历日显示：UTC 表示的凌晨截止不得显示成前一天。
func TestRecallAnnotationUsesLocalCalendarDay(t *testing.T) {
	local := time.Date(2026, 10, 20, 2, 0, 0, 0, time.Local)
	e := Entry{Title: "x", Type: TypeRef, Domain: DomainOpenItem, Lifecycle: LifecycleOpen, ExpiresAt: local}
	if got := recallAnnotation(e); !strings.Contains(got, "due 2026-10-20") {
		t.Errorf("annotation = %q, want local day 2026-10-20", got)
	}
}

// —— P0：Search 原始/生效语义分离 ——

// 默认结果按生效生命周期过滤（过期即隐）；显式 lifecycle:"open" 按磁盘原始
// 值匹配，让过期条目可被找回续期或废弃。
func TestSearchToolDefaultHidesPastDueButOpenFilterFindsIt(t *testing.T) {
	s := testStore(t)
	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(72 * time.Hour)
	if _, err := s.Save(Entry{
		Title: "piano-tuning", Type: TypeRef, Domain: DomainOpenItem,
		Content: "钢琴调音", Lifecycle: LifecycleOpen, ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Entry{
		Title: "guitar-lesson", Type: TypeRef, Domain: DomainOpenItem,
		Content: "吉他课", Lifecycle: LifecycleOpen, ExpiresAt: future,
	}); err != nil {
		t.Fatal(err)
	}
	search := NewSearchTool(s)
	ctx := context.Background()

	out, err := search.Execute(ctx, json.RawMessage(`{"query":"piano"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "piano-tuning") {
		t.Errorf("past-due entry must be hidden from default search:\n%s", out)
	}
	out, err = search.Execute(ctx, json.RawMessage(`{"query":"piano","lifecycle":"open"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "piano-tuning") {
		t.Errorf("explicit lifecycle:open must find the past-due entry for renewal:\n%s", out)
	}
	// 未过期条目默认可见。
	out, err = search.Execute(ctx, json.RawMessage(`{"query":"guitar"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "guitar-lesson") {
		t.Errorf("live entry missing from default search:\n%s", out)
	}
}

// reflect 产出带 DOMAIN：自动记忆生产者不再恒为未分类（否则在分域路由下
// 至多占 1 个噪声槽）。无 DOMAIN 行（旧输出/模型省略）仍按未分类解析。
func TestParseReflectOutputDomain(t *testing.T) {
	e, err := parseReflectOutput("TITLE: go-mod-tidy\nTYPE: pattern\nDOMAIN: environment\n---\n先跑 go mod tidy。")
	if err != nil {
		t.Fatal(err)
	}
	if e.Domain != DomainEnvironment || e.Title != "go-mod-tidy" || e.Content != "先跑 go mod tidy。" {
		t.Errorf("parsed = %+v", e)
	}
	e, err = parseReflectOutput("TITLE: x\nTYPE: pattern\n---\n正文")
	if err != nil {
		t.Fatal(err)
	}
	if e.Domain != "" || e.Content != "正文" {
		t.Errorf("domainless output = %+v", e)
	}
}

// —— helpers ——

func mustSearchAll(t *testing.T, s *Store) []Entry {
	t.Helper()
	all, err := s.Search("")
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func findByTitle(t *testing.T, s *Store, title string) Entry {
	t.Helper()
	for _, e := range mustSearchAll(t, s) {
		if e.Title == title {
			return e
		}
	}
	t.Fatalf("entry %q not found", title)
	return Entry{}
}
