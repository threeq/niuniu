package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

func TestSaveToolAndSearchTool(t *testing.T) {
	s := testStore(t)
	reg := tools.NewRegistry(NewSaveTool(s), NewSearchTool(s))

	for _, d := range reg.Defs() {
		if d.Name != "MemorySave" && d.Name != "MemorySearch" {
			t.Fatalf("unexpected tool def %+v", d)
		}
		if d.Description == "" || len(d.InputSchema) == 0 {
			t.Errorf("def %q incomplete", d.Name)
		}
	}

	out, err := reg.Execute(context.Background(), "MemorySave",
		json.RawMessage(`{"title":"pnpm-only","type":"decision","content":"本项目只用 pnpm。","tags":["build"]}`))
	if err != nil {
		t.Fatalf("MemorySave: %v", err)
	}
	if !strings.Contains(out, "pnpm-only") {
		t.Errorf("save output = %q, want it to name the id", out)
	}
	// 同 title 再存 → updated 而非新增。
	out2, err := reg.Execute(context.Background(), "MemorySave",
		json.RawMessage(`{"title":"pnpm-only","type":"decision","content":"v2"}`))
	if err != nil {
		t.Fatalf("MemorySave again: %v", err)
	}
	if !strings.Contains(out2, "updated") {
		t.Errorf("second save output = %q, want updated", out2)
	}
	all, _ := s.Search("")
	if len(all) != 1 {
		t.Fatalf("entries = %d, want 1 (dedupe by title)", len(all))
	}

	// MemorySearch 命中输出带 id。
	hits, err := reg.Execute(context.Background(), "MemorySearch", json.RawMessage(`{"query":"pnpm"}`))
	if err != nil {
		t.Fatalf("MemorySearch: %v", err)
	}
	if !strings.Contains(hits, "pnpm-only") || !strings.Contains(hits, "v2") {
		t.Errorf("search output = %q", hits)
	}
	// 空查询 = 全列。
	listed, err := reg.Execute(context.Background(), "MemorySearch", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("MemorySearch empty: %v", err)
	}
	if !strings.Contains(listed, "pnpm-only") {
		t.Errorf("list output = %q", listed)
	}
	// 缺必填 → error。
	if _, err := reg.Execute(context.Background(), "MemorySave", json.RawMessage(`{"content":"x"}`)); err == nil {
		t.Error("want error for missing title")
	}
}

func TestSaveToolLifecycleAndDomain(t *testing.T) {
	s := testStore(t)
	reg := tools.NewRegistry(NewSaveTool(s), NewSearchTool(s))

	// 带 lifecycle/expires_at/domain 保存。
	out, err := reg.Execute(context.Background(), "MemorySave",
		json.RawMessage(`{"title":"明天面试","type":"ref","domain":"open_item","lifecycle":"open","expires_at":"2026-10-20T09:00:00Z","content":"带作品集"}`))
	if err != nil {
		t.Fatalf("MemorySave: %v", err)
	}
	if !strings.Contains(out, "明天面试") {
		t.Errorf("save output = %q", out)
	}
	hits, _ := s.Search("面试")
	if len(hits) != 1 || hits[0].Domain != "open_item" || hits[0].Lifecycle != "open" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].ExpiresAt.IsZero() || hits[0].ExpiresAt.UTC() != time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC) {
		t.Errorf("ExpiresAt = %v", hits[0].ExpiresAt)
	}

	// 非法 expires_at → error（让模型纠正格式）。
	if _, err := reg.Execute(context.Background(), "MemorySave",
		json.RawMessage(`{"title":"x","content":"y","expires_at":"明天上午"}`)); err == nil {
		t.Error("want error for non-RFC3339 expires_at")
	}

	// MemorySearch 支持 lifecycle / domain 过滤，输出带状态标注。
	if _, err := reg.Execute(context.Background(), "MemorySave",
		json.RawMessage(`{"title":"老规则","type":"decision","lifecycle":"deprecated","content":"已被取代"}`)); err != nil {
		t.Fatal(err)
	}
	dep, err := reg.Execute(context.Background(), "MemorySearch",
		json.RawMessage(`{"query":"","lifecycle":"deprecated"}`))
	if err != nil {
		t.Fatalf("MemorySearch lifecycle filter: %v", err)
	}
	if !strings.Contains(dep, "老规则") || !strings.Contains(dep, "lifecycle: deprecated") {
		t.Errorf("filtered output = %q", dep)
	}
	if strings.Contains(dep, "明天面试") {
		t.Errorf("lifecycle filter leaked open entries: %q", dep)
	}
	openList, err := reg.Execute(context.Background(), "MemorySearch",
		json.RawMessage(`{"lifecycle":"open","domain":"open_item"}`))
	if err != nil {
		t.Fatalf("MemorySearch combined filter: %v", err)
	}
	if !strings.Contains(openList, "明天面试") || !strings.Contains(openList, "due: 2026-10-20") {
		t.Errorf("combined filter output = %q", openList)
	}
	if strings.Contains(openList, "老规则") {
		t.Errorf("filter leaked deprecated entries: %q", openList)
	}
	// 无命中。
	if got, _ := reg.Execute(context.Background(), "MemorySearch",
		json.RawMessage(`{"lifecycle":"done"}`)); got != "(no matching memories)" {
		t.Errorf("no-hit output = %q", got)
	}
}

func TestSection(t *testing.T) {
	if Section("") != "" {
		t.Error("empty body must omit the section")
	}
	sec := Section("- [gotcha] win-paths: 转义")
	for _, want := range []string{"# Memory", "ADVISORY", "MemorySearch", "win-paths"} {
		if !strings.Contains(sec, want) {
			t.Errorf("section missing %q:\n%s", want, sec)
		}
	}
}

// scriptedModel replays fixed assistant texts.
type scriptedModel struct {
	texts []string
	calls int
	reqs  []model.Request
}

func (m *scriptedModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	if m.calls >= len(m.texts) {
		return nil, fmt.Errorf("script exhausted")
	}
	m.reqs = append(m.reqs, req)
	text := m.texts[m.calls]
	m.calls++
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
		{Type: model.BlockText, Text: text}}}}, nil
}

func TestReflectPersistsLesson(t *testing.T) {
	s := testStore(t)
	m := &scriptedModel{texts: []string{
		"TITLE: pnpm-only-workspace\nTYPE: decision\n---\n本项目只用 pnpm 安装依赖。",
	}}
	id, err := Reflect(context.Background(), m, "USER: 用什么包管理器?\nASSISTANT: 用了 pnpm", s)
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if id != "pnpm-only-workspace" {
		t.Fatalf("id = %q", id)
	}
	all, _ := s.Search("")
	if len(all) != 1 || all[0].Type != "decision" || !strings.Contains(all[0].Content, "只用 pnpm") {
		t.Fatalf("entries = %+v", all)
	}
	// 提炼请求带 transcript 且 System 是提炼指令。
	if len(m.reqs) != 1 || !strings.Contains(m.reqs[0].System, "lesson") && !strings.Contains(m.reqs[0].System, "记忆") {
		t.Errorf("reflect request system = %q", m.reqs[0].System)
	}
	if !strings.Contains(m.reqs[0].Messages[0].Text(), "pnpm") {
		t.Errorf("transcript not passed: %q", m.reqs[0].Messages[0].Text())
	}
}

func TestReflectNoneMeansNoEntry(t *testing.T) {
	s := testStore(t)
	m := &scriptedModel{texts: []string{"NONE"}}
	id, err := Reflect(context.Background(), m, "USER: hi\nASSISTANT: hello", s)
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty for NONE", id)
	}
	if all, _ := s.Search(""); len(all) != 0 {
		t.Fatalf("entries = %+v, want none", all)
	}
	// 空 transcript 直接跳过（不调模型）。
	m2 := &scriptedModel{}
	if id, err := Reflect(context.Background(), m2, "  ", s); err != nil || id != "" || m2.calls != 0 {
		t.Errorf("empty transcript: id=%q err=%v calls=%d", id, err, m2.calls)
	}
}

func TestReflectDedupesByTitle(t *testing.T) {
	s := testStore(t)
	m := &scriptedModel{texts: []string{
		"TITLE: same-lesson\nTYPE: gotcha\n---\nv1",
		"TITLE: same-lesson\nTYPE: gotcha\n---\nv2",
	}}
	if _, err := Reflect(context.Background(), m, "t1", s); err != nil {
		t.Fatal(err)
	}
	if _, err := Reflect(context.Background(), m, "t2", s); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Search("")
	if len(all) != 1 || !strings.Contains(all[0].Content, "v2") {
		t.Fatalf("entries = %+v, want single updated entry", all)
	}
}

func TestReflectMalformedOutput(t *testing.T) {
	s := testStore(t)
	m := &scriptedModel{texts: []string{"完全不符合格式的闲话"}}
	if _, err := Reflect(context.Background(), m, "t", s); err == nil {
		t.Fatal("want error for unparseable reflect output")
	}
	if all, _ := s.Search(""); len(all) != 0 {
		t.Fatalf("malformed output must not persist entries: %+v", all)
	}
}
